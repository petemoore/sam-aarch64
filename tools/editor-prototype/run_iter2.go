// Design-iteration 2 (i76): COMET-style same-line comments enabled by
// compressed instruction rendering.
//
// Pete's locked decisions (2026-06-12): MODE 3, 64×24, 8×8 font only.
// Direction: instruction + comment on ONE row, with the instruction rendering
// compressed to make room. Rendering is presentation-only over the abstract IR
// — binutils stays the storage/dump/interchange projection, so the display is
// free to differ from GNU syntax.
//
// Two modes, both driven from main.go flags:
//
//   - -iter2-stats: measure the rendering-rule ladder R0–R5 over every
//     instruction line of the real flattened release corpus and print a
//     markdown report (width distributions, comment-budget fits, label
//     lengths) to stdout.
//   - -iter2-mockups: render the Part-B PNG mockups (baseline, compressed,
//     semicolon variant, horizontal viewport, register-marking styles) at
//     MODE 3 64×24 through the samscreen abstraction.
//
// The rendering-rule ladder (cumulative — each level includes the previous):
//
//	R0 baseline   mnemonic + one space + operands (binutils as today)
//	R1 column     adaptive mnemonic column per 24-line page: gap =
//	              (longest mnemonic on page) − len(mnemonic) + 1
//	R2 immediates #0x1F → &1F, #16 → 16, #-16 → -16, #-0x10 → -&10
//	R3 separators ", " → "," (memory operands too)
//	R4 registers  x23 → 23, w5 → 5 (colour carries the register-ness;
//	              sp/lr/fp/xzr/wzr/wsp stay named)
//	R5 labels     branch/adr label operands truncated to N chars + ellipsis
//
// Iteration-quality code: it exists to produce honest numbers and mockups
// fast, not to ship.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/mockup"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/viewer"
)

// ---------------------------------------------------------------------------
// Palette — code rows share ONE MODE 3 CLUT quadruple (paper + code +
// comments + register marks). The status bar is drawn with the same four pens
// (black-on-white), so this design needs NO line-interrupt palette switch; a
// real build could still buy a cyan bar with one.
// ---------------------------------------------------------------------------

const (
	i2Paper   = 0 // black           (reg   0)
	i2Comment = 1 // mid grey        (reg  15) — comment material
	i2Mark    = 2 // bright yellow   (reg 102) — register marks
	i2Code    = 3 // bright white    (reg 127) — code
)

func iter2Palette() samscreen.Palette {
	p := samscreen.DefaultPalette()
	p[i2Paper] = 0
	p[i2Comment] = 15
	p[i2Mark] = 102
	p[i2Code] = 127
	return p
}

// glyphArrowLeft is the viewport-offset indicator in the status bar ("←14").
const glyphArrowLeft = 0x82

var iter2Glyphs = map[byte][]uint8{
	glyphArrowLeft: {0x00, 0x10, 0x30, 0x7e, 0x30, 0x10, 0x00, 0x00},
}

// ---------------------------------------------------------------------------
// The rendering-rule ladder
// ---------------------------------------------------------------------------

var (
	reHexImm = regexp.MustCompile(`#(-?)0x([0-9a-fA-F]+)`)
	reDecImm = regexp.MustCompile(`#(-?[0-9]+)`)
	// xN / wN registers. x29/x30/x31 never appear (rendered fp/lr/xzr) but the
	// pattern covers them anyway; \b keeps symbol names (foo_x21) intact.
	reRegNum = regexp.MustCompile(`\b([xw])(3[01]|[12]?[0-9])\b`)
	// Named registers, for display marking only (no width change).
	reRegName = regexp.MustCompile(`\b(sp|lr|fp|xzr|wzr|wsp)\b`)
	// A plain symbol operand (truncation candidate). Locals (1f/2b) start with
	// a digit and stay untouched.
	reSymbolOp = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
)

// nativizeImms applies R2: drop '#'; hex becomes &UPPER, decimal stays bare,
// the sign rides in front of the '&' (matching the source spelling's base).
func nativizeImms(s string) string {
	s = reHexImm.ReplaceAllStringFunc(s, func(m string) string {
		g := reHexImm.FindStringSubmatch(m)
		return g[1] + "&" + strings.ToUpper(g[2])
	})
	return reDecImm.ReplaceAllString(s, "$1")
}

// tightenSeps applies R3.
func tightenSeps(s string) string { return strings.ReplaceAll(s, ", ", ",") }

// stripRegPrefixes applies R4's width effect (the colour marking is the
// mockups' job).
func stripRegPrefixes(s string) string { return reRegNum.ReplaceAllString(s, "$2") }

// isBranchy reports whether mnem takes a label target operand (R5 scope).
func isBranchy(mnem string) bool {
	switch mnem {
	case "b", "bl", "cbz", "cbnz", "tbz", "tbnz", "adr", "adrp":
		return true
	}
	return strings.HasPrefix(mnem, "b.")
}

// truncLabelOp applies R5: the last operand of a branch/adr instruction, when
// it is a plain symbol longer than n, truncates to n chars + ellipsis glyph
// (one cell). Expects R3-tightened operands (top-level "," separators; branch
// shapes carry no brackets).
func truncLabelOp(mnem, ops string, n int) string {
	if !isBranchy(mnem) || ops == "" {
		return ops
	}
	parts := strings.Split(ops, ",")
	last := parts[len(parts)-1]
	if reSymbolOp.MatchString(last) && len(last) > n {
		parts[len(parts)-1] = last[:n] + string([]byte{glyphEllipsis})
	}
	return strings.Join(parts, ",")
}

// isInstruction reports whether l is a real instruction statement. Directives
// (leading '.') are excluded from ladder stats — they render in their own
// style — and are counted separately.
func isInstruction(l srcLine) bool {
	return l.kind == lkInst && l.mnemonic != "" && !strings.HasPrefix(l.mnemonic, ".")
}

// opsAt returns l's operand text at cumulative ladder level (0–5).
func opsAt(l srcLine, level, truncN int) string {
	ops := l.operands
	if level >= 2 {
		ops = nativizeImms(ops)
	}
	if level >= 3 {
		ops = tightenSeps(ops)
	}
	if level >= 4 {
		ops = stripRegPrefixes(ops)
	}
	if level >= 5 {
		ops = truncLabelOp(l.mnemonic, ops, truncN)
	}
	if level >= 6 {
		ops = exprTighten(ops)
	}
	return ops
}

// widthAt returns the rendered code-line width (in cells, no indent) of an
// instruction line at the given level. mnemCol is the page's operand column
// (longest mnemonic on the page + 1); it applies from R1 up.
func widthAt(l srcLine, level, mnemCol, truncN int) int {
	ops := opsAt(l, level, truncN)
	if ops == "" {
		return len(l.mnemonic)
	}
	if level == 0 {
		return len(l.mnemonic) + 1 + len(ops)
	}
	return mnemCol + len(ops)
}

// iter2PageSize is the paging window for the adaptive mnemonic column and the
// page-adaptive comment column (one 64×24 screen of document lines).
const iter2PageSize = 24

// pageMnemCol returns the operand column for the page [lo,hi): longest
// instruction mnemonic + 1.
func pageMnemCol(m []srcLine, lo, hi int) int {
	maxLen := 0
	for i := lo; i < hi && i < len(m); i++ {
		if isInstruction(m[i]) && len(m[i].mnemonic) > maxLen {
			maxLen = len(m[i].mnemonic)
		}
	}
	return maxLen + 1
}

// pageMaxWidth returns the widest instruction code line on the page at level.
// With commentedOnly, only comment-carrying lines count: the comment column
// has to clear lines that put a comment after it, while a wide *uncommented*
// line may harmlessly run past the column.
func pageMaxWidth(m []srcLine, lo, hi, level, truncN int, commentedOnly bool) int {
	col := pageMnemCol(m, lo, hi)
	w := 0
	for i := lo; i < hi && i < len(m); i++ {
		if !isInstruction(m[i]) || (commentedOnly && m[i].trailing == "") {
			continue
		}
		if lw := widthAt(m[i], level, col, truncN); lw > w {
			w = lw
		}
	}
	return w
}

// exprTighten is the EXPLORATORY R6 (not in the locked ladder): parenthesised
// constant expressions — the corpus's residual width tail — drop the spaces
// around infix operators, and decimal constants ≥ 256 inside them turn into
// &HEX. The '&' hex sigil collides with the AND operator's glyph, which is a
// real syntax question (REPORT.md) — this measures the width win only.
var (
	reExprOp  = regexp.MustCompile(` (\||&|\^|\+|-|\*|/|>>|<<) `)
	reExprNum = regexp.MustCompile(`\b[0-9]{3,}\b`)
)

func exprTighten(s string) string {
	if !strings.Contains(s, "(") {
		return s
	}
	s = reExprOp.ReplaceAllString(s, "$1")
	return reExprNum.ReplaceAllStringFunc(s, func(num string) string {
		var v uint64
		fmt.Sscanf(num, "%d", &v)
		if v < 256 {
			return num
		}
		return fmt.Sprintf("&%X", v)
	})
}

// ---------------------------------------------------------------------------
// Part A — corpus measurement
// ---------------------------------------------------------------------------

type widthDist struct{ widths []int }

func (d *widthDist) add(w int) { d.widths = append(d.widths, w) }
func (d *widthDist) sort()     { sort.Ints(d.widths) }
func (d *widthDist) n() int    { return len(d.widths) }
func (d *widthDist) min() int  { return d.widths[0] }
func (d *widthDist) max() int  { return d.widths[len(d.widths)-1] }

// pct is the nearest-rank percentile of the sorted distribution.
func (d *widthDist) pct(p float64) int {
	rank := int(p*float64(len(d.widths))+0.999999) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(d.widths) {
		rank = len(d.widths) - 1
	}
	return d.widths[rank]
}

// pctLE is the percentage of values ≤ w.
func (d *widthDist) pctLE(w int) float64 {
	n := sort.SearchInts(d.widths, w+1)
	return 100 * float64(n) / float64(len(d.widths))
}

// countIn counts values in [lo,hi].
func (d *widthDist) countIn(lo, hi int) int {
	return sort.SearchInts(d.widths, hi+1) - sort.SearchInts(d.widths, lo)
}

// ladderLevels names the cumulative levels for the report. R5 is measured at
// two truncation lengths.
var ladderLevels = []struct {
	label  string
	level  int
	truncN int
}{
	{"R0 baseline", 0, 0},
	{"R1 column", 1, 0},
	{"R2 immediates", 2, 0},
	{"R3 separators", 3, 0},
	{"R4 registers", 4, 0},
	{"R5 labels N=16", 5, 16},
	{"R5 labels N=12", 5, 12},
	{"R6 exprs (exploratory)", 6, 16},
}

func runIter2Stats(doc *viewer.Document, w io.Writer) error {
	m := parseDesignDoc(doc)

	// Corpus shape counts.
	var nInst, nDirective, nLabel, nComment, nBlank int
	for _, l := range m {
		switch {
		case isInstruction(l):
			nInst++
		case l.kind == lkInst:
			nDirective++
		case l.kind == lkLabel:
			nLabel++
		case l.kind == lkComment:
			nComment++
		default:
			nBlank++
		}
	}
	fmt.Fprintf(w, "# i76 design-iteration-2 — Part A corpus measurement\n\n")
	fmt.Fprintf(w, "Corpus: %s — %d rendered lines: %d instructions, %d directives, %d labels, %d standalone-comment lines, %d blank.\n\n",
		filepath.Base(doc.Path), len(m), nInst, nDirective, nLabel, nComment, nBlank)
	fmt.Fprintf(w, "Widths are pure code-line widths in cells (no left indent; the mockups indent code 2 cells). Pages are %d-line document windows (the adaptive mnemonic column and the comment column recompute per page).\n\n", iter2PageSize)

	// Width distributions per ladder level, plus the widest line per level.
	dists := make([]*widthDist, len(ladderLevels))
	for i := range dists {
		dists[i] = &widthDist{}
	}
	type extreme struct {
		width int
		text  string
		doc   int
	}
	extremes := make([]extreme, len(ladderLevels))

	for lo := 0; lo < len(m); lo += iter2PageSize {
		hi := lo + iter2PageSize
		if hi > len(m) {
			hi = len(m)
		}
		col := pageMnemCol(m, lo, hi)
		for i := lo; i < hi; i++ {
			l := m[i]
			if !isInstruction(l) {
				continue
			}
			for li, lv := range ladderLevels {
				wd := widthAt(l, lv.level, col, lv.truncN)
				dists[li].add(wd)
				if wd > extremes[li].width {
					ops := opsAt(l, lv.level, lv.truncN)
					gap := " "
					if lv.level >= 1 {
						gap = strings.Repeat(" ", col-len(l.mnemonic))
					}
					text := l.mnemonic
					if ops != "" {
						text += gap + ops
					}
					extremes[li] = extreme{wd, text, l.doc}
				}
			}
		}
	}
	for _, d := range dists {
		d.sort()
	}

	fmt.Fprintf(w, "## Code-line width by ladder level (cumulative)\n\n")
	fmt.Fprintf(w, "| level | min | p50 | p90 | p99 | max | <=20 | <=24 | <=28 | <=32 | comment cols @p50 | @p90 |\n")
	fmt.Fprintf(w, "|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for li, lv := range ladderLevels {
		d := dists[li]
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %d | %.1f%% | %.1f%% | %.1f%% | %.1f%% | %d | %d |\n",
			lv.label, d.min(), d.pct(0.50), d.pct(0.90), d.pct(0.99), d.max(),
			d.pctLE(20), d.pctLE(24), d.pctLE(28), d.pctLE(32),
			64-2-d.pct(0.50), 64-2-d.pct(0.90))
	}
	fmt.Fprintf(w, "\n(\"comment cols\" = 64 − 2-space gap − width: the same-line comment room next to a p50/p90 code line.)\n\n")

	// Histogram: bucketed widths, all levels side by side.
	buckets := []struct {
		lo, hi int
		label  string
	}{
		{0, 8, "<=8"}, {9, 12, "9-12"}, {13, 16, "13-16"}, {17, 20, "17-20"},
		{21, 24, "21-24"}, {25, 28, "25-28"}, {29, 32, "29-32"}, {33, 40, "33-40"},
		{41, 56, "41-56"}, {57, 1 << 30, "57+"},
	}
	fmt.Fprintf(w, "## Width histogram (%% of instruction lines)\n\n| width |")
	for _, lv := range ladderLevels {
		fmt.Fprintf(w, " %s |", lv.label)
	}
	fmt.Fprintf(w, "\n|---|")
	for range ladderLevels {
		fmt.Fprintf(w, "---|")
	}
	fmt.Fprintf(w, "\n")
	for _, b := range buckets {
		fmt.Fprintf(w, "| %s |", b.label)
		for li := range ladderLevels {
			d := dists[li]
			fmt.Fprintf(w, " %.1f |", 100*float64(d.countIn(b.lo, b.hi))/float64(d.n()))
		}
		fmt.Fprintf(w, "\n")
	}

	fmt.Fprintf(w, "\n## Longest instruction per level\n\n")
	for li, lv := range ladderLevels {
		e := extremes[li]
		fmt.Fprintf(w, "- %s (%d cells, doc line %d): `%s`\n",
			lv.label, e.width, e.doc+1, strings.ReplaceAll(e.text, string([]byte{glyphEllipsis}), "…"))
	}

	// Label-length distribution (definition sites).
	var names []string
	for _, l := range m {
		if l.kind != lkLabel {
			continue
		}
		name := strings.TrimSuffix(l.label, ":")
		if reSymbolOp.MatchString(name) { // skip numeric locals (1:, 2:)
			names = append(names, name)
		}
	}
	dl := &widthDist{}
	for _, n := range names {
		dl.add(len(n))
	}
	dl.sort()
	sort.Slice(names, func(a, b int) bool { return len(names[a]) > len(names[b]) })
	fmt.Fprintf(w, "\n## Label lengths (definition sites, %d labels)\n\n", len(names))
	fmt.Fprintf(w, "min %d / p50 %d / p90 %d / p99 %d / max %d; %.1f%% <=12 chars, %.1f%% <=16 chars.\n\n",
		dl.min(), dl.pct(0.5), dl.pct(0.9), dl.pct(0.99), dl.max(), dl.pctLE(12), dl.pctLE(16))
	fmt.Fprintf(w, "Longest labels:\n\n")
	for i := 0; i < 10 && i < len(names); i++ {
		fmt.Fprintf(w, "- %2d  `%s`\n", len(names[i]), names[i])
	}

	// Use-site: how often does R5 truncation actually bite?
	var nTargets, bite12, bite16 int
	for _, l := range m {
		if !isInstruction(l) || !isBranchy(l.mnemonic) {
			continue
		}
		parts := strings.Split(tightenSeps(l.operands), ",")
		last := parts[len(parts)-1]
		if !reSymbolOp.MatchString(last) {
			continue
		}
		nTargets++
		if len(last) > 12 {
			bite12++
		}
		if len(last) > 16 {
			bite16++
		}
	}
	fmt.Fprintf(w, "\nBranch/adr label operands (use sites): %d total; truncation bites %d (%.1f%%) at N=12, %d (%.1f%%) at N=16.\n",
		nTargets, bite12, 100*float64(bite12)/float64(nTargets), bite16, 100*float64(bite16)/float64(nTargets))

	// Comment budget: same-line (trailing) comments on instruction lines.
	fmt.Fprintf(w, "\n## Same-line comments vs budget (64 cols, 2-space gap)\n\n")
	type fitStat struct{ total, fitPageAll, fitPageCommented, fitLine int }
	measure := func(level, truncN int) fitStat {
		var s fitStat
		for lo := 0; lo < len(m); lo += iter2PageSize {
			hi := lo + iter2PageSize
			if hi > len(m) {
				hi = len(m)
			}
			col := pageMnemCol(m, lo, hi)
			colAll := pageMaxWidth(m, lo, hi, level, truncN, false) + 2
			colCommented := pageMaxWidth(m, lo, hi, level, truncN, true) + 2
			for i := lo; i < hi; i++ {
				l := m[i]
				if !isInstruction(l) || l.trailing == "" {
					continue
				}
				s.total++
				if colAll+len(l.trailing) <= 64 {
					s.fitPageAll++
				}
				if colCommented+len(l.trailing) <= 64 {
					s.fitPageCommented++
				}
				if widthAt(l, level, col, truncN)+2+len(l.trailing) <= 64 {
					s.fitLine++
				}
			}
		}
		return s
	}
	r0 := measure(0, 0)
	r5 := measure(5, 16)
	fmt.Fprintf(w, "| code level | comments | fit (column clears ALL code) | fit (column clears commented code) | fit (per-line cram) |\n|---|---|---|---|---|\n")
	for _, row := range []struct {
		label string
		s     fitStat
	}{{"R0", r0}, {"R5 (N=16)", r5}} {
		fmt.Fprintf(w, "| %s | %d | %.1f%% | %.1f%% | %.1f%% |\n",
			row.label, row.s.total,
			100*float64(row.s.fitPageAll)/float64(row.s.total),
			100*float64(row.s.fitPageCommented)/float64(row.s.total),
			100*float64(row.s.fitLine)/float64(row.s.total))
	}
	fmt.Fprintf(w, "\n(Column variants are page-adaptive over the 24-line page: \"clears ALL code\" = widest code line + 2; \"clears commented code\" = widest comment-carrying code line + 2 — a wide uncommented line just runs past the column (the mockups draw this rule); per-line cram: each comment starts 2 after its own line's code.)\n")

	// Comment length distribution for context.
	cl := &widthDist{}
	for _, l := range m {
		if isInstruction(l) && l.trailing != "" {
			cl.add(len(l.trailing))
		}
	}
	cl.sort()
	fmt.Fprintf(w, "\nTrailing-comment lengths: min %d / p50 %d / p90 %d / max %d; %.1f%% of instruction lines carry one (%d of %d).\n",
		cl.min(), cl.pct(0.5), cl.pct(0.9), cl.max(), 100*float64(cl.n())/float64(nInst), cl.n(), nInst)
	return nil
}

// ---------------------------------------------------------------------------
// Part B — mockups
// ---------------------------------------------------------------------------

// regStyle is one register-marking treatment: ink/paper for x-width and
// w-width register cells.
type regStyle struct {
	xInk, xPaper uint8
	wInk, wPaper uint8
}

var (
	// (a) fg-coloured digits; x vs w by fg-vs-bg within the one accent colour.
	styleFG = regStyle{i2Mark, i2Paper, i2Paper, i2Mark}
	// (b) inverse-video cells; x vs w by which pen forms the block.
	styleInverse = regStyle{i2Paper, i2Code, i2Paper, i2Comment}
	// (c) bg-tinted cells on the comment grey; x vs w by fg.
	styleTint = regStyle{i2Code, i2Comment, i2Paper, i2Comment}
)

// iter2TruncN is the mockups' label truncation length (stats cover 12 and 16;
// see REPORT.md for the choice).
const iter2TruncN = 16

// i2cell is one virtual-row cell; rows are built unclipped, then blitted to
// the screen at a horizontal offset (the viewport mockups).
type i2cell struct {
	ch         byte
	ink, paper uint8
}

// i2VirtWidth bounds the virtual row (longest corpus line + slack).
const i2VirtWidth = 256

func i2text(row []i2cell, x int, s string, ink, paper uint8) int {
	for i := 0; i < len(s); i++ {
		if x+i >= len(row) {
			break
		}
		ch := s[i]
		if (ch < 0x20 || ch > 0x7e) && ch != glyphEllipsis && ch != glyphArrowLeft {
			ch = ' '
		}
		row[x+i] = i2cell{ch, ink, paper}
	}
	return x + len(s)
}

// i2Window is a fixed document-line window for the mockups.
type i2Window struct {
	name   string
	lo, hi int
}

// findWindow locates a window by a content needle so the mockups survive
// corpus drift; rows is the visible text-row count.
func findWindow(doc *viewer.Document, needle string, rows int) (i2Window, error) {
	for i, ln := range doc.Lines {
		if strings.Contains(ln.Text, needle) {
			hi := i + rows
			if hi > len(doc.Lines) {
				hi = len(doc.Lines)
			}
			return i2Window{needle, i, hi}, nil
		}
	}
	return i2Window{}, fmt.Errorf("window needle %q not found in corpus", needle)
}

// i2Renderer paints one window in one style.
type i2Renderer struct {
	doc        *viewer.Document
	m          []srcLine
	win        i2Window
	baseline   bool     // R0 cram instead of the R5 ladder
	semicolon  bool     // prefix comments with ';' (era convention: no space)
	style      regStyle // register marking (R5 modes)
	offset     int      // horizontal viewport offset
	mnemCol    int      // operand column (R5 modes)
	commentCol int      // comment column (R5 modes)
}

func newI2Renderer(doc *viewer.Document, m []srcLine, win i2Window) *i2Renderer {
	r := &i2Renderer{doc: doc, m: m, win: win, style: styleFG}
	r.mnemCol = pageMnemCol(m, win.lo, win.hi)
	// The comment column clears the page's comment-carrying code lines; a
	// wide uncommented line may run past it (see the Part-A fit table).
	r.commentCol = 2 + pageMaxWidth(m, win.lo, win.hi, 5, iter2TruncN, true) + 2
	return r
}

// commentBody returns a standalone comment row's text (the document line
// with its "//" marker stripped).
func (r *i2Renderer) commentBody(l srcLine) string {
	text := strings.TrimSpace(r.doc.Lines[l.doc].Text)
	text = strings.TrimPrefix(text, "//")
	return strings.TrimPrefix(text, " ")
}

// row builds the full virtual row for model line l.
func (r *i2Renderer) row(l srcLine) []i2cell {
	row := make([]i2cell, i2VirtWidth)
	for i := range row {
		row[i] = i2cell{' ', i2Code, i2Paper}
	}
	comment := func(x int, text string) {
		if r.semicolon {
			text = ";" + text
		}
		i2text(row, x, text, i2Comment, i2Paper)
	}
	switch l.kind {
	case lkComment:
		if r.baseline {
			i2text(row, 0, strings.TrimSpace(r.doc.Lines[l.doc].Text), i2Comment, i2Paper)
		} else {
			comment(0, r.commentBody(l))
		}
	case lkLabel:
		i2text(row, 0, l.label, i2Code, i2Paper)
	case lkInst:
		stmt := l.mnemonic
		if l.operands != "" {
			stmt += " " + l.operands
		}
		if r.baseline {
			x := i2text(row, 2, stmt, i2Code, i2Paper)
			if l.trailing != "" {
				i2text(row, x, " // "+l.trailing, i2Comment, i2Paper)
			}
			break
		}
		if !isInstruction(l) {
			// Directives keep their R0 shape (they are not code-column
			// material) but still get the comment column.
			i2text(row, 2, stmt, i2Code, i2Paper)
		} else {
			i2text(row, 2, l.mnemonic, i2Code, i2Paper)
			if ops := opsAtDisplay(l, iter2TruncN); ops != "" {
				r.paintOperands(row, 2+r.mnemCol, ops)
			}
		}
		if l.trailing != "" {
			comment(r.commentCol, l.trailing)
		}
	}
	return row
}

// paintOperands writes operand text (R2/R3/R5 already applied, register
// prefixes still present) dropping xN/wN prefixes and painting register cells
// in the active marking style — the display form of R4.
func (r *i2Renderer) paintOperands(row []i2cell, x int, ops string) {
	rest := ops
	for rest != "" {
		locNum := reRegNum.FindStringSubmatchIndex(rest)
		locName := reRegName.FindStringIndex(rest)
		// Pick the earlier match.
		var lo, hi int
		var cellText string
		var wReg bool
		switch {
		case locNum != nil && (locName == nil || locNum[0] <= locName[0]):
			lo, hi = locNum[0], locNum[1]
			cellText = rest[locNum[4]:locNum[5]] // digits, prefix dropped
			wReg = rest[locNum[2]:locNum[3]] == "w"
		case locName != nil:
			lo, hi = locName[0], locName[1]
			cellText = rest[lo:hi]
			wReg = strings.HasPrefix(cellText, "w")
		default:
			i2text(row, x, rest, i2Code, i2Paper)
			return
		}
		x = i2text(row, x, rest[:lo], i2Code, i2Paper)
		ink, paper := r.style.xInk, r.style.xPaper
		if wReg {
			ink, paper = r.style.wInk, r.style.wPaper
		}
		x = i2text(row, x, cellText, ink, paper)
		rest = rest[hi:]
	}
}

// opsAtDisplay is opsAt for the display path: R2+R3+R5 textual transforms but
// NOT the R4 prefix strip (paintOperands does that per-token, with colour).
func opsAtDisplay(l srcLine, truncN int) string {
	ops := nativizeImms(l.operands)
	ops = tightenSeps(ops)
	return truncLabelOp(l.mnemonic, ops, truncN)
}

// render paints the window to scr: text rows then the status bar.
func (r *i2Renderer) render(scr samscreen.Screen, statusLeft, statusRight string) {
	scr.Clear(samscreen.Cell{Ch: ' ', Ink: i2Code, Paper: i2Paper})
	cols, rows := scr.Size()
	y := 0
	for i := r.win.lo; i < r.win.hi && y < rows-1; i++ {
		row := r.row(r.m[i])
		for x := 0; x < cols; x++ {
			vx := x + r.offset
			if vx >= len(row) {
				break
			}
			c := row[vx]
			scr.Set(x, y, samscreen.Cell{Ch: c.ch, Ink: c.ink, Paper: c.paper})
		}
		y++
	}
	// Status bar: black on white, same CLUT quadruple as the code rows (no
	// line interrupt needed for this design).
	for x := 0; x < cols; x++ {
		scr.Set(x, rows-1, samscreen.Cell{Ch: ' ', Ink: i2Paper, Paper: i2Code})
	}
	putStatus(scr, 1, rows-1, statusLeft)
	putStatus(scr, cols-1-len(statusRight), rows-1, statusRight)
}

// putStatus writes status-bar text (black on white), allowing custom glyphs.
func putStatus(scr samscreen.Screen, x, y int, s string) {
	cols, _ := scr.Size()
	for i := 0; i < len(s); i++ {
		if x+i < 0 || x+i >= cols {
			continue
		}
		ch := s[i]
		if (ch < 0x20 || ch > 0x7e) && ch != glyphEllipsis && ch != glyphArrowLeft {
			ch = ' '
		}
		scr.Set(x+i, y, samscreen.Cell{Ch: ch, Ink: i2Paper, Paper: i2Code})
	}
}

// renderRegisterStyles paints the register-marking comparison screen: the
// same R5-compressed lines under each of the three treatments.
func renderRegisterStyles(scr samscreen.Screen, doc *viewer.Document, m []srcLine, samples []int) {
	scr.Clear(samscreen.Cell{Ch: ' ', Ink: i2Code, Paper: i2Paper})
	cols, rows := scr.Size()
	mnemCol := 0
	for _, i := range samples {
		if len(m[i].mnemonic) > mnemCol {
			mnemCol = len(m[i].mnemonic)
		}
	}
	mnemCol++
	groups := []struct {
		title string
		style regStyle
	}{
		{"(a) fg-coloured digits: x=yellow ink, w=inverse yellow", styleFG},
		{"(b) inverse-video cells: x=white block, w=grey block", styleInverse},
		{"(c) bg-tinted cells: x=white-on-grey, w=black-on-grey", styleTint},
	}
	y := 0
	for _, g := range groups {
		r := &i2Renderer{doc: doc, m: m, style: g.style, mnemCol: mnemCol}
		row := make([]i2cell, i2VirtWidth)
		for i := range row {
			row[i] = i2cell{' ', i2Code, i2Paper}
		}
		i2text(row, 0, g.title, i2Comment, i2Paper)
		blitRow(scr, row, y, cols)
		y++
		for _, idx := range samples {
			l := m[idx]
			row := make([]i2cell, i2VirtWidth)
			for i := range row {
				row[i] = i2cell{' ', i2Code, i2Paper}
			}
			i2text(row, 2, l.mnemonic, i2Code, i2Paper)
			if ops := opsAtDisplay(l, iter2TruncN); ops != "" {
				r.paintOperands(row, 2+mnemCol, ops)
			}
			blitRow(scr, row, y, cols)
			y++
		}
		y++ // group gap
	}
	for x := 0; x < cols; x++ {
		scr.Set(x, rows-1, samscreen.Cell{Ch: ' ', Ink: i2Paper, Paper: i2Code})
	}
	putStatus(scr, 1, rows-1, "register marking styles  MODE 3 64x24 8x8")
}

func blitRow(scr samscreen.Screen, row []i2cell, y, cols int) {
	for x := 0; x < cols && x < len(row); x++ {
		scr.Set(x, y, samscreen.Cell{Ch: row[x].ch, Ink: row[x].ink, Paper: row[x].paper})
	}
}

// runIter2Mockups renders the Part-B PNG set into outDir.
func runIter2Mockups(doc *viewer.Document, outDir, fontDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	m := parseDesignDoc(doc)

	geom, err := samscreen.LookupGeometry("mode3-8x8")
	if err != nil {
		return err
	}
	font, err := loadDesignFont(geom.Font, fontDir)
	if err != nil {
		return err
	}
	font, err = withGlyphOverrides(font, iter2Glyphs)
	if err != nil {
		return err
	}

	textRows := geom.Rows - 1
	winA, err := findWindow(doc, "handle_irq_bcm283x:", textRows)
	if err != nil {
		return err
	}
	winA.name = "irq dispatch"
	winB, err := findWindow(doc, "x21, x20, x14, x11", textRows)
	if err != nil {
		return err
	}
	winB.name = "paint pixel"

	write := func(name string, paint func(scr samscreen.Screen)) error {
		scr := mockup.New(geom, font, mockup.WithPalette(iter2Palette()))
		paint(scr)
		path := filepath.Join(outDir, name)
		if err := scr.WritePNGFile(path); err != nil {
			return err
		}
		fmt.Println(path)
		return nil
	}
	status := func(win i2Window) string {
		return fmt.Sprintf("release.s  L%d-%d  %s", win.lo+1, win.hi, win.name)
	}

	shots := []struct {
		file      string
		win       i2Window
		baseline  bool
		semicolon bool
		offset    int
		right     string
	}{
		{"iter2-r0-baseline.png", winA, true, false, 0, "R0 binutils"},
		{"iter2-r0-baseline-dense.png", winB, true, false, 0, "R0 binutils"},
		{"iter2-r5-compressed.png", winA, false, false, 0, "R5"},
		{"iter2-r5-compressed-dense.png", winB, false, false, 0, "R5"},
		{"iter2-r5-semicolon.png", winA, false, true, 0, "R5 ;"},
		{"iter2-viewport.png", winB, false, false, 0, "R5 \x820"},
		{"iter2-viewport-shifted.png", winB, false, false, 14, "R5 \x8214"},
	}
	for _, s := range shots {
		s := s
		err := write(s.file, func(scr samscreen.Screen) {
			r := newI2Renderer(doc, m, s.win)
			r.baseline = s.baseline
			r.semicolon = s.semicolon
			r.offset = s.offset
			r.render(scr, status(s.win), s.right)
		})
		if err != nil {
			return err
		}
	}

	// Register styles: pick register-rich lines from the dense window.
	samples := pickRegisterSamples(m, winB, 5)
	return write("iter2-register-styles.png", func(scr samscreen.Screen) {
		renderRegisterStyles(scr, doc, m, samples)
	})
}

// pickRegisterSamples returns up to n instruction lines from win with the most
// register occurrences (variety for the style comparison).
func pickRegisterSamples(m []srcLine, win i2Window, n int) []int {
	type cand struct{ idx, score int }
	var cands []cand
	for i := win.lo; i < win.hi && i < len(m); i++ {
		if !isInstruction(m[i]) {
			continue
		}
		score := len(reRegNum.FindAllString(m[i].operands, -1)) +
			len(reRegName.FindAllString(m[i].operands, -1))
		if score > 0 {
			cands = append(cands, cand{i, score})
		}
	}
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].score > cands[b].score })
	if len(cands) > n {
		cands = cands[:n]
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].idx < cands[b].idx })
	out := make([]int, len(cands))
	for i, c := range cands {
		out[i] = c.idx
	}
	return out
}
