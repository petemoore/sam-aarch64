// Design-iteration rendering mode (i76 P1 design iteration 1): comment-aware
// screen-layout variants rendered as PNG mockups for Pete to compare.
//
// The P1a viewer renders the flattened source as a raw text window; Pete's
// feedback (2026-06-12) is that an editor should *design* the screen: show
// instructions without inline comments, mark comment-carrying lines with a
// gutter glyph, open long comments full-screen on an F-key, and interleave
// short (≤64-char) comments next to their code line. This file implements
// those layouts as variants V1–V3 plus the F7 comment-reader screen, each
// painted through the same samscreen abstraction as the product viewer, so
// every render respects MODE 3's real constraints (4 CLUT colours including
// paper, 1-bpp software fonts).
//
// This is iteration-quality code: it exists to produce honest mockups fast,
// not to ship. The comment model is a textual parse of the rendered lines
// (good enough for layout design); the product editor will read the same
// structure from .tbn records.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/fonts"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/mockup"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/viewer"
)

// Design palette — a deliberately-programmed MODE 3 CLUT quadruple (the CLUT
// is software-loaded, TM p30; an editor is not stuck with the ROM default):
//
//	pen 0: black        (reg   0) — paper
//	pen 1: mid grey     (reg  15) — comment material: recessive, readable
//	pen 2: bright cyan  (reg  85) — labels + chrome (status bar, markers)
//	pen 3: bright white (reg 127) — instruction text
const (
	dPaper   = 0
	dComment = 1
	dChrome  = 2
	dCode    = 3
)

func designPalette() samscreen.Palette {
	p := samscreen.DefaultPalette()
	p[dPaper] = 0
	p[dComment] = 15
	p[dChrome] = 85
	p[dCode] = 127
	return p
}

// Custom glyphs (any 1-bpp pattern is fair game — the font is ours). Defined
// per cell geometry; the 8×6 hybrid reuses the 6-wide patterns letter-spaced.
const (
	glyphMarker   = 0x80 // "a comment lives here": three text-lines icon
	glyphEllipsis = 0x81 // "…": comment preview truncated
)

var customGlyphs = map[samscreen.FontSize]map[byte][]uint8{
	{W: 8, H: 8}: {
		glyphMarker:   {0x00, 0x7c, 0x00, 0x7c, 0x00, 0x70, 0x00, 0x00},
		glyphEllipsis: {0x00, 0x00, 0x00, 0x00, 0x00, 0xdb, 0xdb, 0x00},
	},
	// 6×6 rows are bit-5-leftmost (LoadRowPadded's bit (w-1) convention).
	{W: 6, H: 6}: {
		glyphMarker:   {0x00, 0x3e, 0x00, 0x3e, 0x00, 0x38},
		glyphEllipsis: {0x00, 0x00, 0x00, 0x00, 0x2a, 0x00},
	},
	{W: 8, H: 6}: {
		glyphMarker:   {0x00, 0xf8, 0x00, 0xf8, 0x00, 0xe0},
		glyphEllipsis: {0x00, 0x00, 0x00, 0x00, 0xa8, 0x00},
	},
}

// ---------------------------------------------------------------------------
// Comment-aware line model
// ---------------------------------------------------------------------------

type lineKind int

const (
	lkBlank lineKind = iota
	lkLabel
	lkInst // instruction or directive statement
	lkComment
)

// srcLine is one document line with its comment material resolved: a trailing
// `// …` split off the code, and any standalone comment block immediately
// above attached (the corpus convention: banner/note blocks precede the code
// they describe).
type srcLine struct {
	doc      int // index into doc.Lines
	kind     lineKind
	label    string // label text incl. ':' (lkLabel)
	mnemonic string // first token (lkInst)
	operands string // rest (lkInst)
	trailing string // trailing comment, '//' stripped
	block    []string // attached standalone comment lines, '//' stripped
}

func (l *srcLine) isCode() bool     { return l.kind == lkLabel || l.kind == lkInst }
func (l *srcLine) hasComment() bool { return l.trailing != "" || len(l.block) > 0 }

// commentLines is the full comment material: attached block, then trailing.
// Block and trailing are distinct comments (a sidecar block above the line
// vs the line's own annotation), so a paragraph break separates them.
func (l *srcLine) commentLines() []string {
	out := append([]string{}, l.block...)
	if l.trailing != "" {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, l.trailing)
	}
	return out
}

// shortComment returns the line's comment when it is a single ≤64-char line
// (Pete's "short comment" — displayable inline), else "".
func (l *srcLine) shortComment() string {
	cs := l.commentLines()
	if len(cs) == 1 && len(cs[0]) <= 64 && cs[0] != "" {
		return cs[0]
	}
	return ""
}

// parseDesignDoc builds the comment-aware model: one srcLine per document
// line (so model index == doc.Lines index). Pure-rule banner lines
// ("// ----") become empty block entries — paragraph breaks for the reader.
func parseDesignDoc(doc *viewer.Document) []srcLine {
	out := make([]srcLine, 0, len(doc.Lines))
	var pending []string
	for i, ln := range doc.Lines {
		text := strings.TrimRight(ln.Text, " \t")
		trimmed := strings.TrimLeft(text, " \t")
		switch {
		case trimmed == "":
			out = append(out, srcLine{doc: i, kind: lkBlank})
		case strings.HasPrefix(trimmed, "//"):
			out = append(out, srcLine{doc: i, kind: lkComment})
			body := strings.TrimPrefix(trimmed, "//")
			body = strings.TrimPrefix(body, " ")
			body = strings.TrimRight(body, " ")
			if strings.Trim(body, "-") == "" {
				body = "" // banner rule → paragraph break
			}
			pending = append(pending, body)
		default:
			code, trailing := splitTrailing(text)
			l := srcLine{doc: i, trailing: trailing, block: trimBlock(pending)}
			pending = nil
			ct := strings.TrimSpace(code)
			if strings.HasSuffix(ct, ":") && !strings.ContainsAny(ct, " \t") {
				l.kind = lkLabel
				l.label = ct
			} else {
				l.kind = lkInst
				l.mnemonic, l.operands = splitMnemonic(ct)
			}
			out = append(out, l)
		}
	}
	return out
}

// splitTrailing splits "code // comment" (the canonical render form).
func splitTrailing(text string) (code, trailing string) {
	if i := strings.Index(text, " // "); i >= 0 {
		return strings.TrimRight(text[:i], " \t"), text[i+4:]
	}
	return text, ""
}

func splitMnemonic(code string) (string, string) {
	f := strings.Fields(code)
	if len(f) == 0 {
		return "", ""
	}
	return f[0], strings.Join(f[1:], " ")
}

// trimBlock drops leading/trailing empty entries (banner rules at the edges).
func trimBlock(b []string) []string {
	for len(b) > 0 && b[0] == "" {
		b = b[1:]
	}
	for len(b) > 0 && b[len(b)-1] == "" {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return nil
	}
	return b
}

// ---------------------------------------------------------------------------
// Painting helpers
// ---------------------------------------------------------------------------

type painter struct {
	s    samscreen.Screen
	cols int
}

func newPainter(s samscreen.Screen) *painter {
	c, _ := s.Size()
	return &painter{s: s, cols: c}
}

func (p *painter) cell(x, y int, ch byte, ink, paper uint8) {
	if x < 0 || x >= p.cols {
		return
	}
	p.s.Set(x, y, samscreen.Cell{Ch: ch, Ink: ink, Paper: paper})
}

func (p *painter) text(x, y int, str string, ink, paper uint8) int {
	for i := 0; i < len(str); i++ {
		ch := str[i]
		if ch < 0x20 || ch > 0x7e {
			ch = ' '
		}
		p.cell(x+i, y, ch, ink, paper)
	}
	return x + len(str)
}

// rowPaper floods a row with paper colour (used for the reverse-video cursor
// row before painting its content).
func (p *painter) rowPaper(y int, paper uint8) {
	for x := 0; x < p.cols; x++ {
		p.cell(x, y, ' ', dPaper, paper)
	}
}

// pens resolves a pen against cursor-row reverse video: the cursor row is a
// full-width white bar with black content (a cell-drawn cursor — spec §2.1).
func pens(ink uint8, rev bool) (uint8, uint8) {
	if rev {
		return dPaper, dCode
	}
	return ink, dPaper
}

func paintStatus(p *painter, y int, left, right string) {
	for x := 0; x < p.cols; x++ {
		p.cell(x, y, ' ', dPaper, dChrome)
	}
	p.text(1, y, left, dPaper, dChrome)
	if right != "" {
		p.text(p.cols-1-len(right), y, right, dPaper, dChrome)
	}
}

// ---------------------------------------------------------------------------
// Variant row construction
// ---------------------------------------------------------------------------

// vrow is one display row of a variant: a paint closure plus whether it is
// the cursor's code row.
type vrow struct {
	paint func(p *painter, y int, rev bool)
}

// paintCodeRow paints gutter marker + label/instruction columns:
// marker col 0, labels col 2, mnemonic col 4, operands col 12.
func paintCodeRow(p *painter, y int, l srcLine, rev bool, marker bool, markerInk uint8) {
	if marker {
		ink, paper := pens(markerInk, rev)
		p.cell(0, y, glyphMarker, ink, paper)
	}
	if l.kind == lkLabel {
		ink, paper := pens(dChrome, rev)
		p.text(2, y, l.label, ink, paper)
		return
	}
	ink, paper := pens(dCode, rev)
	p.text(4, y, l.mnemonic, ink, paper)
	if l.operands != "" {
		opCol := 12
		if 4+len(l.mnemonic)+1 > opCol {
			opCol = 4 + len(l.mnemonic) + 1
		}
		p.text(opCol, y, l.operands, ink, paper)
	}
}

// codeEndCol is the column after the last code cell of a code row (for the
// V3 preview gap computation).
func codeEndCol(l srcLine) int {
	if l.kind == lkLabel {
		return 2 + len(l.label)
	}
	end := 4 + len(l.mnemonic)
	if l.operands != "" {
		opCol := 12
		if 4+len(l.mnemonic)+1 > opCol {
			opCol = 4 + len(l.mnemonic) + 1
		}
		end = opCol + len(l.operands)
	}
	return end
}

// v1Rows: code only; a gutter glyph marks comment-carrying lines (grey — it
// is metadata, not content). Standalone comment lines vanish from the flow.
func v1Rows(m []srcLine, lo, hi, cursor int) ([]vrow, int) {
	var rows []vrow
	cr := -1
	for i := lo; i < hi; i++ {
		l := m[i]
		switch l.kind {
		case lkComment:
			continue
		case lkBlank:
			rows = append(rows, vrow{paint: func(p *painter, y int, rev bool) {
				if rev {
					p.rowPaper(y, dCode)
				}
			}})
		default:
			if i == cursor {
				cr = len(rows)
			}
			rows = append(rows, vrow{paint: func(p *painter, y int, rev bool) {
				if rev {
					p.rowPaper(y, dCode)
				}
				paintCodeRow(p, y, l, rev, l.hasComment(), dComment)
			}})
		}
	}
	return rows, cr
}

// v2Rows: V1 plus short (single-line ≤64-char) comments interleaved ABOVE
// their code line — above because that is the corpus's own convention for
// standalone comments, and context-then-code matches how the eye scans
// down a listing. The annotation row carries the marker glyph so it reads
// as injected metadata, not a document line. Long comments stay behind the
// gutter marker.
func v2Rows(m []srcLine, lo, hi, cursor int) ([]vrow, int) {
	var rows []vrow
	cr := -1
	for i := lo; i < hi; i++ {
		l := m[i]
		switch l.kind {
		case lkComment:
			continue
		case lkBlank:
			rows = append(rows, vrow{paint: func(p *painter, y int, rev bool) {
				if rev {
					p.rowPaper(y, dCode)
				}
			}})
		default:
			sc := l.shortComment()
			if sc != "" {
				rows = append(rows, vrow{paint: func(p *painter, y int, rev bool) {
					if rev {
						p.rowPaper(y, dCode)
					}
					ink, paper := pens(dComment, rev)
					p.cell(2, y, glyphMarker, ink, paper)
					p.text(4, y, sc, ink, paper)
				}})
			}
			if i == cursor {
				cr = len(rows)
			}
			rows = append(rows, vrow{paint: func(p *painter, y int, rev bool) {
				if rev {
					p.rowPaper(y, dCode)
				}
				paintCodeRow(p, y, l, rev, l.hasComment() && sc == "", dComment)
			}})
		}
	}
	return rows, cr
}

// v3Rows: the annotated-listing synthesis. Four-pen hierarchy (label cyan /
// code white / comment grey / chrome cyan), aligned mnemonic+operand
// columns, and a right-aligned comment preview in the grey pen: a short
// comment that fits is shown whole; longer material is truncated with the
// ellipsis glyph; multi-line material also gets the gutter marker (cyan
// here — it is a "press F7" affordance, part of the chrome).
func v3Rows(m []srcLine, lo, hi, cursor int) ([]vrow, int) {
	var rows []vrow
	cr := -1
	for i := lo; i < hi; i++ {
		l := m[i]
		switch l.kind {
		case lkComment:
			continue
		case lkBlank:
			rows = append(rows, vrow{paint: func(p *painter, y int, rev bool) {
				if rev {
					p.rowPaper(y, dCode)
				}
			}})
		default:
			if i == cursor {
				cr = len(rows)
			}
			rows = append(rows, vrow{paint: func(p *painter, y int, rev bool) {
				if rev {
					p.rowPaper(y, dCode)
				}
				cs := l.commentLines()
				multi := len(cs) > 1
				paintCodeRow(p, y, l, rev, multi, dChrome)
				if len(cs) == 0 {
					return
				}
				first := cs[0]
				avail := p.cols - 1 - (codeEndCol(l) + 2)
				if avail < 8 {
					return
				}
				ink, paper := pens(dComment, rev)
				if len(first) <= avail && !multi {
					p.text(p.cols-1-len(first), y, first, ink, paper)
					return
				}
				cut := avail - 1
				if cut > len(first) {
					cut = len(first)
				}
				x := p.text(p.cols-1-cut-1, y, first[:cut], ink, paper)
				p.cell(x, y, glyphEllipsis, ink, paper)
			}})
		}
	}
	return rows, cr
}

// ---------------------------------------------------------------------------
// The F7 comment-reader screen
// ---------------------------------------------------------------------------

// wrapWords greedily word-wraps s to width.
func wrapWords(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var out []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) <= width {
			line += " " + w
			continue
		}
		out = append(out, line)
		line = w
	}
	return append(out, line)
}

// paragraphs groups comment lines into reflowable paragraphs: empty lines
// break paragraphs; lines with leading whitespace (deliberate indentation,
// e.g. "  TODO" under "On entry:") stay verbatim.
func paragraphs(lines []string) [][]string {
	var paras [][]string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			paras = append(paras, cur)
			cur = nil
		}
	}
	for _, ln := range lines {
		switch {
		case ln == "":
			flush()
		case strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t"):
			flush()
			paras = append(paras, []string{"\x00" + strings.TrimRight(ln, " \t")}) // verbatim marker
		default:
			cur = append(cur, ln)
		}
	}
	flush()
	return paras
}

// renderReader paints the full-screen comment view F7 opens on a comment-
// carrying line: header bar, the owning code line, the comment material
// reflowed for reading, and a status bar with the way back.
func renderReader(s samscreen.Screen, m []srcLine, cursor int, docName string, total int) {
	s.Clear(samscreen.Cell{Ch: ' ', Ink: dCode, Paper: dPaper})
	p := newPainter(s)
	_, rows := s.Size()
	l := m[cursor]

	// Header bar: what this screen is + where it came from.
	for x := 0; x < p.cols; x++ {
		p.cell(x, 0, ' ', dPaper, dChrome)
	}
	p.text(1, 0, fmt.Sprintf("COMMENT  %s  L%d", docName, l.doc+1), dPaper, dChrome)

	// The owning code line, rendered exactly as the editor shows it.
	paintCodeRow(p, 2, l, false, false, dComment)

	// Rule between code and comment body.
	for x := 0; x < p.cols; x++ {
		p.cell(x, 3, '-', dComment, dPaper)
	}

	// Body: paragraphs word-wrapped to a 2-col margin, white — the comment
	// is the focus of this screen, so it gets the reading pen.
	y := 5
	width := p.cols - 4
	for _, para := range paragraphs(l.commentLines()) {
		if y >= rows-2 {
			break
		}
		if len(para) == 1 && strings.HasPrefix(para[0], "\x00") {
			p.text(2, y, para[0][1:], dCode, dPaper)
			y++
			continue
		}
		for _, ln := range wrapWords(strings.Join(para, " "), width) {
			if y >= rows-2 {
				break
			}
			p.text(2, y, ln, dCode, dPaper)
			y++
		}
		y++ // blank row between paragraphs
	}

	n := 0
	for _, ln := range l.commentLines() {
		if ln != "" {
			n++
		}
	}
	paintStatus(p, rows-1, fmt.Sprintf("comment 1/1  ·  %d lines", n), "ESC=back")
	_ = total
}

// ---------------------------------------------------------------------------
// Driver
// ---------------------------------------------------------------------------

// renderVariant paints a code-window variant: rows built around the cursor,
// cursor row centre-locked, status line at the bottom.
func renderVariant(s samscreen.Screen, m []srcLine,
	build func([]srcLine, int, int, int) ([]vrow, int),
	cursor int, docName string, statusRight string) {

	s.Clear(samscreen.Cell{Ch: ' ', Ink: dCode, Paper: dPaper})
	p := newPainter(s)
	_, h := s.Size()
	textRows := h - 1

	lo, hi := cursor-120, cursor+120
	if lo < 0 {
		lo = 0
	}
	if hi > len(m) {
		hi = len(m)
	}
	rows, cr := build(m, lo, hi, cursor)
	top := cr - textRows/2
	if top+textRows > len(rows) {
		top = len(rows) - textRows
	}
	if top < 0 {
		top = 0
	}
	for y := 0; y < textRows && top+y < len(rows); y++ {
		rows[top+y].paint(p, y, top+y == cr)
	}
	paintStatus(p, h-1, fmt.Sprintf("%s  L%d/%d", docName, m[cursor].doc+1, len(m)), statusRight)
}

// designGeometries is the render matrix for this iteration: MODE 3 only
// (Pete rejected 32×24; MODE 3 is the working assumption) at 64×24 (8×8
// ROM-style cell), 85×32 (the 6×6 five-pixel font), and the 64×32 hybrid
// (8-wide × 6-high cell: the 6×6 glyphs letter-spaced into an 8-px-wide
// cell — built locally; not one of the §3 canonical six).
func designGeometries() []samscreen.Geometry {
	g8, err := samscreen.LookupGeometry("mode3-8x8")
	if err != nil {
		panic(err)
	}
	g6, err := samscreen.LookupGeometry("mode3-6x6")
	if err != nil {
		panic(err)
	}
	hybrid := samscreen.Geometry{
		Name: "mode3-8x6",
		Mode: samscreen.Mode3,
		Font: samscreen.FontSize{W: 8, H: 6},
		Cols: 64,
		Rows: 32,
	}
	return []samscreen.Geometry{g8, g6, hybrid}
}

// loadDesignFont loads the font for a design geometry and injects the custom
// glyphs (marker, ellipsis). The 8×6 hybrid reads the 6×6 binary with an
// 8-wide cell: the glyph bits live in bits 7..2, so the extra two columns
// come out blank — letter-spacing for free.
func loadDesignFont(size samscreen.FontSize, fontDir string) (samscreen.Font, error) {
	var base samscreen.Font
	var err error
	switch size {
	case samscreen.FontSize{W: 8, H: 8}:
		base, err = fonts.Font8x8()
	case samscreen.FontSize{W: 6, H: 6}:
		base, err = fonts.FontFor(size, fontDir)
	case samscreen.FontSize{W: 8, H: 6}:
		var data []byte
		data, err = os.ReadFile(filepath.Join(fontDir, "font6x6.sam"))
		if err == nil {
			base, err = fonts.LoadRowPadded(8, 6, data)
		}
	default:
		return nil, fmt.Errorf("no design font for cell %s", size)
	}
	if err != nil {
		return nil, err
	}
	return withGlyphOverrides(base, customGlyphs[size])
}

// withGlyphOverrides copies a font and replaces the given glyph patterns.
func withGlyphOverrides(f samscreen.Font, defs map[byte][]uint8) (samscreen.Font, error) {
	sz := f.Size()
	glyphs := make([][]uint8, 256)
	for ch := 0; ch < 256; ch++ {
		rows := make([]uint8, sz.H)
		for r := range rows {
			rows[r] = f.Row(byte(ch), r)
		}
		glyphs[ch] = rows
	}
	for ch, rows := range defs {
		glyphs[ch] = rows
	}
	return samscreen.NewBitmapFont(sz.W, sz.H, glyphs)
}

// runDesigns renders every variant at every design geometry around the given
// document line, writing v{N}-{desc}-{cols}x{rows}.png files into outDir.
func runDesigns(doc *viewer.Document, outDir, fontDir string, cursorDoc int) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	m := parseDesignDoc(doc)
	cursor := cursorDoc
	if cursor < 0 || cursor >= len(m) {
		return fmt.Errorf("design line %d out of range (0..%d)", cursorDoc, len(m)-1)
	}
	for cursor < len(m) && !m[cursor].isCode() {
		cursor++
	}
	docName := filepath.Base(doc.Path)

	variants := []struct {
		name  string
		build func([]srcLine, int, int, int) ([]vrow, int)
		right string
	}{
		{"v1-code-gutter", v1Rows, "F7=comment"},
		{"v2-short-inline", v2Rows, "F7=comment"},
		{"v3-annotated-listing", v3Rows, "F7=comment"},
	}

	for _, geom := range designGeometries() {
		font, err := loadDesignFont(geom.Font, fontDir)
		if err != nil {
			return fmt.Errorf("%s: %w", geom.Name, err)
		}
		size := fmt.Sprintf("%dx%d", geom.Cols, geom.Rows)
		for _, v := range variants {
			scr := mockup.New(geom, font, mockup.WithPalette(designPalette()))
			right := ""
			if m[cursor].hasComment() {
				right = v.right
			}
			renderVariant(scr, m, v.build, cursor, docName, right)
			path := filepath.Join(outDir, fmt.Sprintf("%s-%s.png", v.name, size))
			if err := scr.WritePNGFile(path); err != nil {
				return err
			}
			fmt.Println(path)
		}
		scr := mockup.New(geom, font, mockup.WithPalette(designPalette()))
		renderReader(scr, m, cursor, docName, len(m))
		path := filepath.Join(outDir, fmt.Sprintf("v4-comment-reader-%s.png", size))
		if err := scr.WritePNGFile(path); err != nil {
			return err
		}
		fmt.Println(path)
	}
	return nil
}
