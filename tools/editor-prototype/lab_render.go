package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
)

// lab_render.go is the configurable renderer: it turns the parsed srcLine model
// into a screen, driven entirely by a LabConfig. It reuses the iteration-2
// rendering ladder (nativizeImms / tightenSeps / stripRegPrefixes /
// truncLabelOp / exprTighten / pageMnemCol in run_iter2.go) — this file selects
// and parameterises those rules per config, it does not reinvent them. The same
// renderer drives both the interactive terminal lab and the SAM PNG export, so
// "what I see in the lab" and "how it renders on a SAM" are one code path.

// labGlyphEllipsis / labGlyphArrowLeft reuse the iteration glyph slots; the AND
// glyph is a new slot used when imm_hex_amp collides with the '&' AND operator.
const (
	labGlyphEllipsis = glyphEllipsis  // 0x81
	labGlyphArrowLeft = glyphArrowLeft // 0x82
	labGlyphMarker    = glyphMarker    // 0x80
	// labGlyphAnd is a custom "logical AND" glyph (a small wedge, ∧-style),
	// drawn in place of '&' as the AND operator when imm_hex_amp claims '&' for
	// hex. Without this the reader cannot tell `x0 & 7` from `&7`.
	labGlyphAnd = 0x83
)

// labGlyphs are the lab's custom 8x8 glyph patterns (bit 7 leftmost).
var labGlyphs = map[byte][]uint8{
	labGlyphEllipsis:  {0x00, 0x00, 0x00, 0x00, 0x00, 0xdb, 0xdb, 0x00},
	labGlyphArrowLeft: {0x00, 0x10, 0x30, 0x7e, 0x30, 0x10, 0x00, 0x00},
	labGlyphMarker:    {0x00, 0x7c, 0x00, 0x7c, 0x00, 0x70, 0x00, 0x00},
	// ∧: a wedge rising to a point — visually distinct from '&'.
	labGlyphAnd: {0x00, 0x18, 0x18, 0x24, 0x24, 0x42, 0x42, 0x00},
}

// labCell is one virtual-row cell (built unclipped, then blitted at a
// horizontal offset for the viewport feature).
type labCell struct {
	ch         byte
	ink, paper uint8
}

const labVirtWidth = 256

// labRenderer paints the configured document. It owns the resolved pens (role →
// CLUT index) so painting code never re-derives them.
type labRenderer struct {
	cfg LabConfig
	m   []srcLine
	doc *docLines // text accessor for comment bodies

	// Resolved pens (CLUT indices, or palette indices when relaxed).
	paper, code, comment, label                uint8
	regMark, immMark                           uint8
	chromeFg, chromeBg, currentBg, statusPen   uint8

	// Per-page layout, recomputed by setPage.
	pageLo, pageHi int
	mnemCol        int
	commentCol     int
}

// docLines abstracts the only Document method the renderer needs (the comment
// body text for a standalone comment line), so tests can supply lines without a
// real .tbn.
type docLines struct {
	text func(docIdx int) string
}

func newLabRenderer(cfg LabConfig, m []srcLine, doc *docLines) *labRenderer {
	r := &labRenderer{cfg: cfg, m: m, doc: doc}
	r.paper = uint8(cfg.RolePaper)
	r.code = uint8(cfg.RoleCode)
	r.comment = uint8(cfg.RoleComment)
	r.label = uint8(cfg.RoleLabel)
	r.regMark = uint8(cfg.RoleRegMark)
	r.immMark = uint8(cfg.RoleImmMark)
	r.chromeFg = uint8(cfg.RoleChromeFg)
	r.chromeBg = uint8(cfg.RoleChromeBg)
	r.currentBg = uint8(cfg.RoleCurrentBg)
	r.statusPen = uint8(cfg.RoleStatus)
	return r
}

// reLabAnd matches the AND operator spelled ' & ' inside an operand expression
// (spaces on both sides distinguish it from a leading '&' hex sigil).
var reLabAnd = regexp.MustCompile(` & `)

// --- the per-config ladder ------------------------------------------------

// immHex applies the hex-immediate transform: #0x1F -> &1F (or just 0x1F ->
// stays, only the '#'-prefixed forms move). Independent of immDrop.
func (r *labRenderer) immTransform(s string) string {
	if r.cfg.ImmHexAmp {
		s = reHexImm.ReplaceAllStringFunc(s, func(mtch string) string {
			g := reHexImm.FindStringSubmatch(mtch)
			return g[1] + "&" + strings.ToUpper(g[2])
		})
	}
	if r.cfg.ImmDropHash {
		s = reDecImm.ReplaceAllString(s, "$1")
	}
	return s
}

// renderConstantsTransform applies the render_constants base rewrite to
// operand text. "hex" forces every integer constant to SAM &HEX (or 0x when
// imm_hex_amp is off); "dec" forces decimal. "source" is a no-op. Applied
// after the immTransform pass so the two compose cleanly.

// reAnyHex matches &HEXDIGITS or 0xHEXDIGITS (existing hex constants).
var reAnyHex = regexp.MustCompile(`(?:&|0x)([0-9a-fA-F]+)`)

// reDecAfterSep matches a decimal integer following an immediate marker (#),
// a comma boundary, an open bracket, or a sign (+/-), so register indices
// like x0 and w3 are not converted.
var reDecAfterSep = regexp.MustCompile(`(#|,\s*|\[\s*|(?:[+\-]\s*))([0-9]+)`)

func (r *labRenderer) renderConstantsTransform(s string) string {
	switch r.cfg.RenderConstants {
	case rcHex:
		// Step 1: existing hex constants — normalise sigil and uppercase digits.
		s = reAnyHex.ReplaceAllStringFunc(s, func(m string) string {
			g := reAnyHex.FindStringSubmatch(m)
			if r.cfg.ImmHexAmp {
				return "&" + strings.ToUpper(g[1])
			}
			return "0x" + strings.ToUpper(g[1])
		})
		// Step 2: convert decimal immediates to hex. Only tokens after #, [, or
		// a sign are converted, so register indices (x0, w3) are safe.
		s = reDecAfterSep.ReplaceAllStringFunc(s, func(m string) string {
			g := reDecAfterSep.FindStringSubmatch(m)
			var v uint64
			fmt.Sscanf(g[2], "%d", &v)
			if r.cfg.ImmHexAmp {
				return g[1] + fmt.Sprintf("&%X", v)
			}
			return g[1] + fmt.Sprintf("0x%X", v)
		})
	case rcDec:
		// Convert hex constants to decimal.
		s = reAnyHex.ReplaceAllStringFunc(s, func(m string) string {
			g := reAnyHex.FindStringSubmatch(m)
			var v uint64
			fmt.Sscanf(g[1], "%x", &v)
			return fmt.Sprintf("%d", v)
		})
	}
	return s
}

// opsDisplay returns the operand text for line l with every config TEXT
// transform applied (immediates, tight commas, label truncation, R6 exprs,
// constant base rendering). The register strip + marking is a per-token paint
// decision (paintOperands), not a text rewrite, so it is NOT applied here.
func (r *labRenderer) opsDisplay(l srcLine) string {
	ops := l.operands
	ops = r.immTransform(ops)
	if r.cfg.RenderConstants != rcSource {
		ops = r.renderConstantsTransform(ops)
	}
	if r.cfg.TightCommas {
		ops = tightenSeps(ops)
	}
	if r.cfg.LabelTruncate > 0 {
		ops = r.truncLabel(l.mnemonic, ops)
	}
	if r.cfg.TightenExprs {
		ops = exprTighten(ops)
	}
	return ops
}

// truncLabel is truncLabelOp parameterised by the ellipsis toggle. It expects
// tight-comma operands when tight_commas is on; with spaced commas it splits on
// ", " too so the last operand is found either way.
func (r *labRenderer) truncLabel(mnem, ops string) string {
	if !isBranchy(mnem) || ops == "" {
		return ops
	}
	sep := ","
	if !r.cfg.TightCommas {
		sep = ", "
	}
	parts := strings.Split(ops, sep)
	last := parts[len(parts)-1]
	if reSymbolOp.MatchString(last) && len(last) > r.cfg.LabelTruncate {
		cut := last[:r.cfg.LabelTruncate]
		if r.cfg.LabelEllipsis {
			cut += string([]byte{labGlyphEllipsis})
		}
		parts[len(parts)-1] = cut
	}
	return strings.Join(parts, sep)
}

// mnemonicColumn returns the operand column for the current page (R1):
// adaptive (longest page mnemonic + 1) or the fixed width. A 0 fixed width
// degrades to R0 (one space after the mnemonic), handled by the caller.
func (r *labRenderer) mnemonicColumn() int {
	if r.cfg.MnemonicAdaptive {
		return r.mnemCol
	}
	return r.cfg.MnemonicFixed
}

// --- painting -------------------------------------------------------------

func newLabRow() []labCell {
	row := make([]labCell, labVirtWidth)
	return row
}

func (r *labRenderer) blankRow(into []labCell) {
	for i := range into {
		into[i] = labCell{' ', r.code, r.paper}
	}
}

// putText writes s at x in (ink,paper); custom glyphs pass through, other
// non-printables become spaces. Returns the next x.
func (r *labRenderer) putText(row []labCell, x int, s string, ink, paper uint8) int {
	for i := 0; i < len(s); i++ {
		if x+i < 0 || x+i >= len(row) {
			continue
		}
		ch := s[i]
		if !labPrintable(ch) {
			ch = ' '
		}
		row[x+i] = labCell{ch, ink, paper}
	}
	return x + len(s)
}

func labPrintable(ch byte) bool {
	if ch >= 0x20 && ch <= 0x7e {
		return true
	}
	switch ch {
	case labGlyphEllipsis, labGlyphArrowLeft, labGlyphMarker, labGlyphAnd:
		return true
	}
	return false
}

// pensFor returns (ink, paper) for a marked token of the given mark style,
// using role as the accent pen against the code pen.
func (r *labRenderer) pensFor(style markStyle, role uint8) (ink, paper uint8) {
	switch style {
	case markFg:
		return role, r.paper
	case markBg:
		return r.paper, role
	case markInverse:
		return r.paper, r.code
	default:
		return r.code, r.paper
	}
}

// commentText returns the comment string to draw for line l (semicolon prefix
// applied per config), or "" if there is none.
func (r *labRenderer) commentText(l srcLine) string {
	if l.trailing == "" {
		return ""
	}
	if r.cfg.CommentSemicolon {
		return ";" + l.trailing
	}
	return l.trailing
}

// codeWidthRaw is the raw rendered code-line width (no indent) before any
// max_instruction_width cap. Used internally by codeWidth and displayWidth.
func (r *labRenderer) codeWidthRaw(l srcLine) int {
	if !isInstruction(l) {
		// Directive/label: full statement width.
		if l.kind == lkLabel {
			return len(l.label)
		}
		w := len(l.mnemonic)
		if l.operands != "" {
			w += 1 + len(l.operands)
		}
		return w
	}
	ops := r.opsDisplay(l)
	if ops == "" {
		return len(l.mnemonic)
	}
	col := r.mnemonicColumn()
	if col == 0 {
		return len(l.mnemonic) + 1 + r.displayLen(l, ops)
	}
	if col < len(l.mnemonic)+1 {
		col = len(l.mnemonic) + 1
	}
	return col + r.displayLen(l, ops)
}

// codeWidth is the displayed code-line width for column-computation purposes.
// When max_instruction_width is set, it caps the width so truncated lines do
// not drag the comment column right.
func (r *labRenderer) codeWidth(l srcLine) int {
	w := r.codeWidthRaw(l)
	if r.cfg.MaxInstructionWidth > 0 && w > r.cfg.MaxInstructionWidth {
		// A truncated line ends at MaxInstructionWidth−1 content + 1 ellipsis.
		return r.cfg.MaxInstructionWidth
	}
	return w
}

// displayLen is the on-screen cell length of operand text after the register
// strip (xN/wN -> N) the paint path performs, so widths match what is drawn.
func (r *labRenderer) displayLen(l srcLine, ops string) int {
	if r.cfg.RegStrip {
		ops = stripRegPrefixes(ops)
	}
	return len(ops)
}

// pageCommentCol computes the comment column for the current page per the
// comment_column rule.
func (r *labRenderer) pageCommentCol(indent int) int {
	w := 0
	for i := r.pageLo; i < r.pageHi && i < len(r.m); i++ {
		l := r.m[i]
		if r.cfg.CommentColumnCommentedOnly && l.trailing == "" {
			continue
		}
		if !l.isCode() {
			continue
		}
		if cw := r.codeWidth(l); cw > w {
			w = cw
		}
	}
	return indent + w + r.cfg.CommentGap
}

// setPage recomputes the adaptive mnemonic column and the comment column for
// the document window [lo,hi). indent is the code left-indent (2 cells).
func (r *labRenderer) setPage(lo, hi, indent int) {
	r.pageLo, r.pageHi = lo, hi
	r.mnemCol = pageMnemCol(r.m, lo, hi)
	r.commentCol = r.pageCommentCol(indent)
}

// row builds the full virtual row(s) for model line l. Most lines yield one
// row; comment_layout=above yields an extra leading row. The first return is
// the code row; extras precede it in reading order. suppressComment drops the
// line's same-line comment (the cursor line in wrap-expansion mode shows its
// comment in the expansion rows instead, not duplicated on the code row).
func (r *labRenderer) row(l srcLine, indent int, suppressComment bool) (rows [][]labCell) {
	code := newLabRow()
	r.blankRow(code)

	switch l.kind {
	case lkBlank:
		return [][]labCell{code}
	case lkComment:
		// Standalone comment line: draw its body in the comment pen.
		body := r.doc.text(l.doc)
		body = strings.TrimSpace(body)
		body = strings.TrimPrefix(body, "//")
		body = strings.TrimPrefix(body, " ")
		if r.cfg.CommentSemicolon {
			body = ";" + body
		}
		r.putText(code, indent, body, r.comment, r.paper)
		return [][]labCell{code}
	case lkLabel:
		r.putText(code, indent, l.label, r.label, r.paper)
		return [][]labCell{code}
	}

	// Instruction / directive.
	if !isInstruction(l) {
		stmt := l.mnemonic
		if l.operands != "" {
			stmt += " " + l.operands
		}
		r.putText(code, indent, stmt, r.code, r.paper)
	} else {
		col := r.mnemonicColumn()
		r.putText(code, indent, l.mnemonic, r.code, r.paper)
		if ops := r.opsDisplay(l); ops != "" {
			// R0 (col==0): one space after the mnemonic. R1+: operands align to
			// the page column, but never overlap a longer-than-column mnemonic.
			ox := indent + len(l.mnemonic) + 1
			if col > 0 && indent+col > ox {
				ox = indent + col
			}
			r.paintOperands(code, ox, ops)
		}
	}

	// Comment placement.
	ct := r.commentText(l)
	if ct != "" && !suppressComment {
		switch r.cfg.CommentLayout {
		case clSameline:
			r.putText(code, r.commentCol, ct, r.comment, r.paper)
		case clMarkerOnly:
			r.putText(code, 0, string([]byte{labGlyphMarker}), r.comment, r.paper)
		case clAbove:
			above := newLabRow()
			r.blankRow(above)
			r.putText(above, indent, string([]byte{labGlyphMarker})+" "+ct, r.comment, r.paper)
			return [][]labCell{above, code}
		}
	}
	return [][]labCell{code}
}

// paintOperands writes operand text, performing the register strip + marking
// (display form of R4) and the immediate marking alternative, plus the AND
// glyph substitution when imm_hex_amp claimed '&'.
func (r *labRenderer) paintOperands(row []labCell, x int, ops string) {
	// AND-glyph substitution first: replace ' & ' (the operator) with a marker
	// byte the paint loop renders as labGlyphAnd. Only when hex '&' is active.
	andGlyph := r.cfg.ImmHexAmp
	rest := ops
	for rest != "" {
		// Find the next thing to specially paint: a register, or (if marking
		// immediates) an immediate, whichever comes first.
		locReg := reRegNum.FindStringSubmatchIndex(rest)
		locName := reRegName.FindStringIndex(rest)
		locImm := []int(nil)
		if r.cfg.MarkImmediates {
			locImm = reLabImm.FindStringIndex(rest)
		}

		// Choose the earliest match.
		next := -1
		kind := 0 // 1=regNum 2=regName 3=imm
		if locReg != nil {
			next, kind = locReg[0], 1
		}
		if locName != nil && (next < 0 || locName[0] < next) {
			next, kind = locName[0], 2
		}
		if locImm != nil && (next < 0 || locImm[0] < next) {
			next, kind = locImm[0], 3
		}
		if next < 0 {
			x = r.putPlain(row, x, rest, andGlyph)
			return
		}
		x = r.putPlain(row, x, rest[:next], andGlyph)
		switch kind {
		case 1: // xN / wN register
			digits := rest[locReg[4]:locReg[5]]
			wReg := rest[locReg[2]:locReg[3]] == "w"
			cellText := rest[locReg[0]:locReg[1]] // full token (prefix kept)
			if r.cfg.RegStrip {
				cellText = digits
			}
			style := r.cfg.RegXStyle
			if wReg {
				style = r.cfg.RegWStyle
			}
			ink, paper := r.code, r.paper
			if r.cfg.RegStrip {
				ink, paper = r.pensFor(style, r.regMark)
			}
			x = r.putText(row, x, cellText, ink, paper)
			rest = rest[locReg[1]:]
		case 2: // named register
			tok := rest[locName[0]:locName[1]]
			ink, paper := r.code, r.paper
			if r.cfg.RegStrip {
				ink, paper = r.pensFor(r.cfg.RegNamedStyle, r.regMark)
			}
			x = r.putText(row, x, tok, ink, paper)
			rest = rest[locName[1]:]
		case 3: // immediate (mark alternative)
			tok := rest[locImm[0]:locImm[1]]
			ink, paper := r.pensFor(r.cfg.MarkImmediatesStyle, r.immMark)
			x = r.putText(row, x, tok, ink, paper)
			rest = rest[locImm[1]:]
		}
	}
}

// putPlain writes plain operand text, substituting the AND operator glyph for
// ' & ' when hex '&' is active.
func (r *labRenderer) putPlain(row []labCell, x int, s string, andGlyph bool) int {
	if andGlyph && strings.Contains(s, " & ") {
		s = reLabAnd.ReplaceAllString(s, " "+string([]byte{labGlyphAnd})+" ")
	}
	return r.putText(row, x, s, r.code, r.paper)
}

// reLabImm matches an immediate operand worth marking: a bare decimal, a #-form,
// a 0x/&-hex literal, or a negative. The mark-immediates alternative paints
// these in the accent pen instead of every register.
var reLabImm = regexp.MustCompile(`#?-?(0x|&)?[0-9A-Fa-f]+\b|#-?[0-9]+`)

// truncateVirtualRow truncates row to n cells, writing the ellipsis glyph at
// position n-1 in the code pen. Cells beyond n are blanked.
func (r *labRenderer) truncateVirtualRow(row []labCell, n int) {
	if n < 1 {
		n = 1
	}
	// Write ellipsis at position n-1.
	if n-1 < len(row) {
		row[n-1] = labCell{labGlyphEllipsis, r.code, r.paper}
	}
	// Blank everything beyond n.
	for i := n; i < len(row); i++ {
		row[i] = labCell{' ', r.code, r.paper}
	}
}

// --- screen composition ---------------------------------------------------

// labStatus holds the dynamic status fields the template fills.
type labStatus struct {
	file        string
	line, total int
	mode        string
}

// fillTemplate expands {file} {line} {total} {pct} {mode} in the status
// template.
func (s labStatus) fillTemplate(tmpl string) string {
	pct := 0
	if s.total > 0 {
		pct = (s.line * 100) / s.total
	}
	rep := strings.NewReplacer(
		"{file}", s.file,
		"{line}", fmt.Sprintf("%d", s.line),
		"{total}", fmt.Sprintf("%d", s.total),
		"{pct}", fmt.Sprintf("%d%%", pct),
		"{mode}", s.mode,
		"{offset}", "", // filled by caller append when viewport shifted
	)
	return rep.Replace(tmpl)
}

// composeRow is one rendered screen row built around the document model. The
// renderer expands document lines into virtual rows around the cursor, applies
// the cursor-line expansion mode, then blits the window into the screen with
// the horizontal viewport offset.

// renderScreen paints the whole frame: text rows (centre-locked cursor),
// optional current-line band, the bottom status line(s), and (panel mode) the
// reserved comment panel. cursor is the document line index; offset is the
// horizontal viewport shift in cells.
func (r *labRenderer) renderScreen(scr samscreen.Screen, cursor, offset int, st labStatus) {
	scr.Clear(samscreen.Cell{Ch: ' ', Ink: r.code, Paper: r.paper})
	cols, rows := scr.Size()
	indent := 2

	statusRows := 1
	if r.cfg.StatusMessage != "" {
		statusRows = 2
	}
	panelRows := 0
	if r.cfg.ExpandCursorLine == expandPanel && r.cfg.ExpandK > 0 {
		panelRows = r.cfg.ExpandK
	}
	textRows := rows - statusRows - panelRows
	if textRows < 1 {
		textRows = 1
	}

	// Page window for the adaptive columns: a screenful around the cursor.
	lo := cursor - textRows
	if lo < 0 {
		lo = 0
	}
	hi := cursor + textRows
	if hi > len(r.m) {
		hi = len(r.m)
	}
	r.setPage(lo, hi, indent)

	// Build display rows from a window of document lines, centre-locking the
	// cursor's code row. Expansion (wrap mode) inserts extra rows at the cursor.
	type drow struct {
		cells    []labCell
		isCursor bool
	}
	var disp []drow
	cursorRow := -1
	wrapExpand := r.cfg.ExpandCursorLine == expandWrap && r.cfg.ExpandK > 0
	for i := lo; i < hi; i++ {
		isCursorLine := i == cursor
		// In wrap-expansion the cursor line's comment moves to the expansion
		// rows, so suppress it on the code row (no duplication).
		suppress := wrapExpand && isCursorLine && r.m[i].trailing != ""
		// When max_instruction_width is active, a non-cursor code line whose
		// raw width exceeds the limit is truncated: suppress its same-line
		// comment so it does not float against a fake column. The comment
		// remains reachable via cursor-expansion and F7 paths.
		lineTruncated := false
		if !isCursorLine && r.cfg.MaxInstructionWidth > 0 && r.m[i].isCode() {
			if r.codeWidthRaw(r.m[i]) > r.cfg.MaxInstructionWidth {
				lineTruncated = true
				suppress = true
			}
		}
		vr := r.row(r.m[i], indent, suppress)
		for vi, vrow := range vr {
			isCur := isCursorLine && vi == len(vr)-1
			if isCur {
				cursorRow = len(disp)
			}
			// Apply max_instruction_width truncation to the virtual row (not
			// the cursor line, which always shows in full).
			if lineTruncated && vi == len(vr)-1 {
				r.truncateVirtualRow(vrow, indent+r.cfg.MaxInstructionWidth)
			}
			disp = append(disp, drow{vrow, isCur})
			// Wrap-expansion: after the cursor's code row, unfold its full
			// comment into <=K extra inline rows (page below shifts down).
			if isCur && wrapExpand {
				for _, ex := range r.expandComment(r.m[i], indent, cols, r.cfg.ExpandK) {
					disp = append(disp, drow{ex, false})
				}
			}
		}
	}

	// Centre-lock: place the cursor row at the vertical centre.
	top := 0
	if cursorRow >= 0 {
		top = cursorRow - textRows/2
	}
	if top+textRows > len(disp) {
		top = len(disp) - textRows
	}
	if top < 0 {
		top = 0
	}

	for y := 0; y < textRows && top+y < len(disp); y++ {
		d := disp[top+y]
		paper := r.paper
		if d.isCursor && r.cfg.CurrentLineHighlight {
			paper = r.currentBg
			// Flood the band first so the whole row reads as highlighted.
			for x := 0; x < cols; x++ {
				scr.Set(x, y, samscreen.Cell{Ch: ' ', Ink: r.code, Paper: paper})
			}
		}
		for x := 0; x < cols; x++ {
			vx := x + offset
			if vx >= len(d.cells) {
				break
			}
			c := d.cells[vx]
			cp := c.paper
			if d.isCursor && r.cfg.CurrentLineHighlight && c.paper == r.paper {
				cp = paper
			}
			scr.Set(x, y, samscreen.Cell{Ch: c.ch, Ink: c.ink, Paper: cp})
		}
	}

	// Panel mode: the reserved bottom rows always show the cursor line's full
	// comment, with zero reflow of the page above.
	if panelRows > 0 {
		r.renderPanel(scr, cursor, rows-statusRows-panelRows, panelRows, cols)
	}

	r.renderStatus(scr, rows, statusRows, offset, st)
}

// expandComment word-wraps the cursor line's trailing comment to up to k inline
// rows under the cursor (wrap mode).
func (r *labRenderer) expandComment(l srcLine, indent, cols, k int) [][]labCell {
	if l.trailing == "" {
		return nil
	}
	width := cols - indent - 2
	if width < 8 {
		width = 8
	}
	lines := wrapWords(l.trailing, width)
	if len(lines) > k {
		lines = lines[:k]
	}
	out := make([][]labCell, 0, len(lines))
	for _, ln := range lines {
		row := newLabRow()
		r.blankRow(row)
		r.putText(row, indent+2, ln, r.comment, r.paper)
		out = append(out, row)
	}
	return out
}

// renderPanel paints the reserved bottom panel (panel mode): the cursor line's
// full comment, word-wrapped, in the comment pen, with a top rule. The page
// above never reflows.
func (r *labRenderer) renderPanel(scr samscreen.Screen, cursor, y0, panelRows, cols int) {
	for x := 0; x < cols; x++ {
		scr.Set(x, y0, samscreen.Cell{Ch: '-', Ink: r.comment, Paper: r.paper})
	}
	l := r.m[cursor]
	body := l.trailing
	if body == "" {
		body = "(no comment on this line)"
	}
	width := cols - 4
	lines := wrapWords(body, width)
	for i := 0; i < panelRows-1 && i < len(lines); i++ {
		row := newLabRow()
		r.blankRow(row)
		r.putText(row, 2, lines[i], r.comment, r.paper)
		for x := 0; x < cols; x++ {
			if x >= len(row) {
				break
			}
			c := row[x]
			scr.Set(x, y0+1+i, samscreen.Cell{Ch: c.ch, Ink: c.ink, Paper: c.paper})
		}
	}
}

// renderStatus paints the status line(s): the template line and the optional
// message line, plus the OFF-SAM PALETTE flag and the viewport-offset indicator.
func (r *labRenderer) renderStatus(scr samscreen.Screen, rows, statusRows, offset int, st labStatus) {
	cols, _ := scr.Size()
	statusY := rows - 1
	left := st.fillTemplate(r.cfg.StatusTemplate)
	if r.cfg.RelaxPalette {
		left = "OFF-SAM PALETTE  " + left
	}
	right := ""
	if offset > 0 {
		right = string([]byte{labGlyphArrowLeft}) + fmt.Sprintf("%d", offset)
	}

	band := func(y int, text, rightText string) {
		for x := 0; x < cols; x++ {
			scr.Set(x, y, samscreen.Cell{Ch: ' ', Ink: r.statusPen, Paper: r.chromeBg})
		}
		r.putStatusText(scr, 1, y, text)
		if rightText != "" {
			r.putStatusText(scr, cols-1-len(rightText), y, rightText)
		}
	}

	if statusRows == 2 {
		band(rows-2, left, right)
		band(rows-1, r.cfg.StatusMessage, "")
		return
	}
	band(statusY, left, right)
}

func (r *labRenderer) putStatusText(scr samscreen.Screen, x, y int, s string) {
	cols, _ := scr.Size()
	for i := 0; i < len(s); i++ {
		if x+i < 0 || x+i >= cols {
			continue
		}
		ch := s[i]
		if !labPrintable(ch) {
			ch = ' '
		}
		scr.Set(x+i, y, samscreen.Cell{Ch: ch, Ink: r.statusPen, Paper: r.chromeBg})
	}
}
