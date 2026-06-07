package main

import (
	"strings"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/viewer"
	"github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
)

// labGrid is a minimal samscreen.Screen for render unit tests (no terminal, no
// raster).
type labGrid struct{ *samscreen.Grid }

func (labGrid) Flush() {}

func newLabGrid(t *testing.T) labGrid {
	t.Helper()
	g, err := samscreen.LookupGeometry(labGeometry)
	if err != nil {
		t.Fatal(err)
	}
	return labGrid{samscreen.NewGrid(g)}
}

// gridRow returns row y's characters with trailing spaces trimmed.
func gridRow(s labGrid, y int) string {
	cols, _ := s.Size()
	var b strings.Builder
	for x := 0; x < cols; x++ {
		ch := s.At(x, y).Ch
		if ch < 0x20 || ch > 0x7e {
			ch = '.' // custom glyphs (ellipsis/and/marker) shown as '.'
		}
		b.WriteByte(ch)
	}
	return strings.TrimRight(b.String(), " ")
}

// model builds a srcLine model + docLines from inline instructions, so render
// tests need no .tbn. Each entry is "mnem|operands|trailing".
func model(specs ...[3]string) ([]srcLine, *docLines) {
	var m []srcLine
	texts := map[int]string{}
	for i, s := range specs {
		m = append(m, srcLine{doc: i, kind: lkInst, mnemonic: s[0], operands: s[1], trailing: s[2]})
		texts[i] = s[0] + " " + s[1]
	}
	dl := &docLines{text: func(i int) string { return texts[i] }}
	return m, dl
}

// renderAt renders cfg over model m at cursor and returns the grid rows.
func renderAt(t *testing.T, cfg LabConfig, m []srcLine, dl *docLines, cursor int) (labGrid, []string) {
	t.Helper()
	s := newLabGrid(t)
	r := newLabRenderer(cfg, m, dl)
	r.renderScreen(s, cursor, 0, labStatus{file: "t", line: cursor + 1, total: len(m), mode: "MODE 3"})
	_, rows := s.Size()
	out := make([]string, rows)
	for y := 0; y < rows; y++ {
		out[y] = gridRow(s, y)
	}
	return s, out
}

// joined returns all rows concatenated for substring checks.
func joined(rows []string) string { return strings.Join(rows, "\n") }

// TestBaselineRendersOperands confirms R0 paints "mnem ops" with one space.
func TestBaselineRendersOperands(t *testing.T) {
	cfg := defaultConfig()
	m, dl := model([3]string{"add", "x0, x1, x2", ""})
	_, rows := renderAt(t, cfg, m, dl, 0)
	if !strings.Contains(joined(rows), "add x0, x1, x2") {
		t.Errorf("baseline missing 'add x0, x1, x2'\n%s", joined(rows))
	}
}

// TestImmHexAmp converts #0x1f -> &1F.
func TestImmHexAmp(t *testing.T) {
	cfg := defaultConfig()
	cfg.ImmHexAmp = true
	m, dl := model([3]string{"and", "x0, x0, #0x1f", ""})
	_, rows := renderAt(t, cfg, m, dl, 0)
	if !strings.Contains(joined(rows), "&1F") {
		t.Errorf("imm_hex_amp did not produce &1F\n%s", joined(rows))
	}
}

// TestImmDropHash drops the # on a decimal immediate.
func TestImmDropHash(t *testing.T) {
	cfg := defaultConfig()
	cfg.ImmDropHash = true
	m, dl := model([3]string{"add", "x0, x0, #16", ""})
	_, rows := renderAt(t, cfg, m, dl, 0)
	if !strings.Contains(joined(rows), "x0, x0, 16") {
		t.Errorf("imm_drop_hash did not drop '#'\n%s", joined(rows))
	}
}

// TestTightCommas removes the space after commas.
func TestTightCommas(t *testing.T) {
	cfg := defaultConfig()
	cfg.TightCommas = true
	m, dl := model([3]string{"add", "x0, x1, x2", ""})
	_, rows := renderAt(t, cfg, m, dl, 0)
	if !strings.Contains(joined(rows), "x0,x1,x2") {
		t.Errorf("tight_commas did not tighten\n%s", joined(rows))
	}
}

// TestRegStrip drops the x/w prefix.
func TestRegStrip(t *testing.T) {
	cfg := defaultConfig()
	cfg.RegStrip = true
	cfg.TightCommas = true
	m, dl := model([3]string{"add", "x0, x1, x2", ""})
	_, rows := renderAt(t, cfg, m, dl, 0)
	if !strings.Contains(joined(rows), "add 0,1,2") {
		t.Errorf("reg_style strip did not strip prefixes\n%s", joined(rows))
	}
}

// TestLabelTruncate truncates a long branch target.
func TestLabelTruncate(t *testing.T) {
	cfg := defaultConfig()
	cfg.LabelTruncate = 8
	cfg.LabelEllipsis = false
	m, dl := model([3]string{"b", "a_very_long_label_name", ""})
	_, rows := renderAt(t, cfg, m, dl, 0)
	j := joined(rows)
	if !strings.Contains(j, "a_very_l") || strings.Contains(j, "a_very_long_label_name") {
		t.Errorf("label_truncate did not cut to 8\n%s", j)
	}
}

// TestSamelineComment places the trailing comment on the code row.
func TestSamelineComment(t *testing.T) {
	cfg := defaultConfig()
	m, dl := model([3]string{"nop", "", "the note"})
	_, rows := renderAt(t, cfg, m, dl, 0)
	// The comment shares a row with "nop".
	for _, row := range rows {
		if strings.Contains(row, "nop") && strings.Contains(row, "the note") {
			return
		}
	}
	t.Errorf("sameline comment not on the code row\n%s", joined(rows))
}

// TestAboveComment places the comment on its own row, above the code.
func TestAboveComment(t *testing.T) {
	cfg := defaultConfig()
	cfg.CommentLayout = clAbove
	m, dl := model([3]string{"nop", "", "the note"})
	_, rows := renderAt(t, cfg, m, dl, 0)
	codeRow, noteRow := -1, -1
	for y, row := range rows {
		if strings.Contains(row, "nop") {
			codeRow = y
		}
		if strings.Contains(row, "the note") && !strings.Contains(row, "nop") {
			noteRow = y
		}
	}
	if noteRow < 0 || codeRow < 0 || noteRow >= codeRow {
		t.Errorf("above layout: note row %d not above code row %d\n%s", noteRow, codeRow, joined(rows))
	}
}

// TestCurrentLineHighlight floods the cursor row's background with the band pen.
func TestCurrentLineHighlight(t *testing.T) {
	cfg := defaultConfig()
	cfg.CurrentLineHighlight = true
	m, dl := model(
		[3]string{"nop", "", ""},
		[3]string{"ret", "", ""},
	)
	s, _ := renderAt(t, cfg, m, dl, 1) // cursor on "ret"
	_, rows := s.Size()
	found := false
	for y := 0; y < rows-1; y++ {
		if strings.TrimSpace(gridRow(s, y)) == "ret" {
			if s.At(0, y).Paper == uint8(cfg.RoleCurrentBg) {
				found = true
			}
		}
	}
	if !found {
		t.Error("current-line band paper not applied to the cursor row")
	}
}

// TestStatusTemplate fills {file}/{line}/{total}.
func TestStatusTemplate(t *testing.T) {
	cfg := defaultConfig()
	cfg.StatusTemplate = "{file} {line}/{total} {pct}"
	m, dl := model([3]string{"nop", "", ""}, [3]string{"ret", "", ""})
	s, _ := renderAt(t, cfg, m, dl, 1)
	_, rows := s.Size()
	status := gridRow(s, rows-1)
	if !strings.Contains(status, "t 2/2 100%") {
		t.Errorf("status = %q, want it to contain 't 2/2 100%%'", status)
	}
}

// TestSecondStatusLine reserves a second status row for the message.
func TestSecondStatusLine(t *testing.T) {
	cfg := defaultConfig()
	cfg.StatusMessage = "hello lab"
	m, dl := model([3]string{"nop", "", ""})
	s, _ := renderAt(t, cfg, m, dl, 0)
	_, rows := s.Size()
	if !strings.Contains(gridRow(s, rows-1), "hello lab") {
		t.Errorf("message line missing\n%q", gridRow(s, rows-1))
	}
}

// fakeViewerDoc builds a viewer.Document from plain source lines (no .tbn), so
// the PNG-render path can be exercised in a unit test.
func fakeViewerDoc(lines ...string) *viewer.Document {
	d := &viewer.Document{Path: "fake.tbn"}
	for _, s := range lines {
		d.Lines = append(d.Lines, render.Line{RecordIdx: -1, Text: s})
	}
	return d
}

// TestLabFrameImageDimensions confirms the PNG-export render path produces the
// aspect-correct 512x384 SAM frame for a config.
func TestLabFrameImageDimensions(t *testing.T) {
	doc := fakeViewerDoc("\tadd x0, x1, x2 // sum", "\tret")
	img, err := labFrameImage(doc, defaultConfig(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 512 || img.Bounds().Dy() != 384 {
		t.Errorf("frame = %dx%d, want 512x384", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

// TestRelaxPaletteRendersOffSAMColours confirms a relaxed config can paint a
// pen beyond the MODE 3 quad without the abstraction panicking (off-SAM grid).
func TestRelaxPaletteRendersOffSAMColours(t *testing.T) {
	cfg := defaultConfig()
	cfg.RelaxPalette = true
	// Fine-syntax roles must also be set beyond the quad to exercise off-SAM rendering.
	cfg.RoleCode = 7        // beyond the 0-3 quad
	cfg.RoleMnemonic.Pen = 7 // mnemonic uses role_mnemonic, not role_code
	m, dl := model([3]string{"nop", "", ""})
	g, err := samscreen.LookupGeometry(labGeometry)
	if err != nil {
		t.Fatal(err)
	}
	s := labGrid{samscreen.NewOffSAMGrid(g)}
	r := newLabRenderer(cfg, m, dl)
	r.renderScreen(s, 0, 0, labStatus{file: "t", total: 1})
	// "nop" painted in pen 7 — no panic is the assertion; spot-check the ink.
	_, rows := s.Size()
	for y := 0; y < rows; y++ {
		if strings.TrimSpace(gridRow(s, y)) == "nop" {
			if s.At(2, y).Ink != 7 {
				t.Errorf("code ink = %d, want 7 (off-SAM)", s.At(2, y).Ink)
			}
			return
		}
	}
	t.Fatal("nop row not found")
}

// TestMaxInstructionWidthTruncatesOffCursor confirms that a non-cursor line
// whose code width exceeds MaxInstructionWidth is truncated at N−1 + ellipsis,
// while the cursor line is always shown in full.
func TestMaxInstructionWidthTruncatesOffCursor(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxInstructionWidth = 10
	// A wide instruction that definitely exceeds 10 columns (2 indent + 8 = 10
	// minimum; "ldrb x12, [x13, x14]" with indent is well over that).
	m, dl := model(
		[3]string{"ldrb", "x12, [x13, x14]", ""},
		[3]string{"ret", "", ""},
	)
	// cursor=1 ("ret"): off-cursor line is the ldrb row (line 0).
	_, rows := renderAt(t, cfg, m, dl, 1)
	j := joined(rows)
	// The truncated row must not contain the full operands.
	if strings.Contains(j, "x13, x14") {
		t.Errorf("off-cursor line was not truncated: %s", j)
	}
	// The truncated row must contain the ellipsis placeholder '.'.
	foundEllipsis := false
	for _, row := range rows {
		if strings.Contains(row, "ldrb") && strings.Contains(row, ".") {
			foundEllipsis = true
			break
		}
	}
	if !foundEllipsis {
		t.Errorf("truncated row missing ellipsis glyph\n%s", j)
	}
}

// TestMaxInstructionWidthCursorFull confirms the cursor line is never truncated
// even when MaxInstructionWidth is set.
func TestMaxInstructionWidthCursorFull(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxInstructionWidth = 10
	m, dl := model(
		[3]string{"ldrb", "x12, [x13, x14]", ""},
	)
	// cursor=0: the single line is the cursor line, never truncated.
	_, rows := renderAt(t, cfg, m, dl, 0)
	j := joined(rows)
	if !strings.Contains(j, "x13, x14") {
		t.Errorf("cursor line was truncated but must be full\n%s", j)
	}
}

// TestRenderConstantsHex forces hex rendering of a decimal constant.
func TestRenderConstantsHex(t *testing.T) {
	cfg := defaultConfig()
	cfg.ImmHexAmp = true
	cfg.RenderConstants = rcHex
	// A decimal immediate — should become &HEX.
	m, dl := model([3]string{"mov", "x0, #255", ""})
	_, rows := renderAt(t, cfg, m, dl, 0)
	j := joined(rows)
	if !strings.Contains(j, "&FF") {
		t.Errorf("render_constants=hex did not produce &FF\n%s", j)
	}
}

// TestRenderConstantsDec forces decimal rendering of a hex constant.
func TestRenderConstantsDec(t *testing.T) {
	cfg := defaultConfig()
	cfg.RenderConstants = rcDec
	// A hex immediate — should become decimal.
	m, dl := model([3]string{"mov", "x0, #0xff", ""})
	_, rows := renderAt(t, cfg, m, dl, 0)
	j := joined(rows)
	if !strings.Contains(j, "255") || strings.Contains(j, "0xff") {
		t.Errorf("render_constants=dec did not produce 255\n%s", j)
	}
}

// TestMaxInstructionWidthCommentSuppressed confirms that a truncated off-cursor
// line has its same-line comment suppressed (the comment would float against a
// fake column on the truncated row).
func TestMaxInstructionWidthCommentSuppressed(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxInstructionWidth = 10
	// A wide instruction with a trailing comment.
	m, dl := model(
		[3]string{"ldrb", "x12, [x13, x14]", "load byte"},
		[3]string{"ret", "", ""},
	)
	// cursor=1: off-cursor line (line 0) should be truncated and its comment suppressed.
	_, rows := renderAt(t, cfg, m, dl, 1)
	j := joined(rows)
	// The truncated row must not show the comment.
	if strings.Contains(j, "load byte") {
		t.Errorf("truncated off-cursor line should not show its comment\n%s", j)
	}
}

// --- v1.2 feature tests ------------------------------------------------------

// TestExpandOnlyIfNeededSkipsExpansion confirms that when expand_only_if_needed
// is on and the cursor line's code+comment fits in one row, no expansion rows
// appear below the cursor (the comment stays inline).
func TestExpandOnlyIfNeededSkipsExpansion(t *testing.T) {
	cfg := defaultConfig()
	cfg.ExpandCursorLine = expandWrap
	cfg.ExpandK = 3
	cfg.ExpandOnlyIfNeeded = true
	// A short instruction + short comment: "nop" + "ok" easily fits on 64 cols.
	m, dl := model([3]string{"nop", "", "ok"})
	_, rows := renderAt(t, cfg, m, dl, 0)
	j := joined(rows)
	// The comment must appear on the same row as "nop", not beneath it.
	for _, row := range rows {
		if strings.Contains(row, "nop") && strings.Contains(row, "ok") {
			return // found on same row: correct
		}
	}
	t.Errorf("expand_only_if_needed=on: short comment should stay inline\n%s", j)
}

// TestExpandOnlyIfNeededFiresWhenNeeded confirms expansion fires when the
// cursor line's code+comment would exceed the screen width.
func TestExpandOnlyIfNeededFiresWhenNeeded(t *testing.T) {
	cfg := defaultConfig()
	cfg.ExpandCursorLine = expandWrap
	cfg.ExpandK = 3
	cfg.ExpandOnlyIfNeeded = true
	// "nop" + a 70-char comment: total exceeds 64 cols, so expansion fires.
	longComment := strings.Repeat("x", 70)
	m, dl := model([3]string{"nop", "", longComment})
	_, rows := renderAt(t, cfg, m, dl, 0)
	j := joined(rows)
	// The long comment text must appear somewhere in the output.
	if !strings.Contains(j, "xxxxxxxx") {
		t.Errorf("expand_only_if_needed=on: long comment should expand\n%s", j)
	}
	// And it must NOT appear on the same row as "nop" in full.
	for _, row := range rows {
		if strings.Contains(row, "nop") && strings.Contains(row, longComment) {
			t.Errorf("long comment should have expanded, not stayed inline\n%s", j)
		}
	}
}

// TestCursorBlockStyleBracket confirms the bracket glyph appears on expanded rows.
func TestCursorBlockStyleBracket(t *testing.T) {
	cfg := defaultConfig()
	cfg.ExpandCursorLine = expandWrap
	cfg.ExpandK = 3
	cfg.ExpandOnlyIfNeeded = false
	cfg.CursorBlockStyle = cblBracket
	longComment := strings.Repeat("z", 70)
	m, dl := model([3]string{"nop", "", longComment})
	s, _ := renderAt(t, cfg, m, dl, 0)
	_, rows := s.Size()
	// At least one expanded row must have the bracket glyph at column 0.
	found := false
	for y := 0; y < rows; y++ {
		if s.At(0, y).Ch == labGlyphBracket {
			found = true
			break
		}
	}
	if !found {
		t.Error("cursor_block_style=bracket: no bracket glyph found at column 0")
	}
}

// TestCursorBlockStyleBand confirms the band paper colour appears on expanded rows.
func TestCursorBlockStyleBand(t *testing.T) {
	cfg := defaultConfig()
	cfg.ExpandCursorLine = expandWrap
	cfg.ExpandK = 3
	cfg.ExpandOnlyIfNeeded = false
	cfg.CursorBlockStyle = cblBand
	longComment := strings.Repeat("y", 70)
	m, dl := model(
		[3]string{"nop", "", longComment},
		[3]string{"ret", "", ""},
	)
	s, _ := renderAt(t, cfg, m, dl, 0)
	_, rows := s.Size()
	// Find an expansion row (below the cursor row, containing the repeated char).
	bandFound := false
	for y := 0; y < rows; y++ {
		if strings.Contains(gridRow(s, y), "yyy") {
			// This is an expansion row; its paper should be currentBg.
			if s.At(2, y).Paper == uint8(cfg.RoleCurrentBg) {
				bandFound = true
			}
		}
	}
	if !bandFound {
		t.Error("cursor_block_style=band: expansion row not painted in band colour")
	}
}

// TestSyntaxRoleMnemonicColour confirms role_mnemonic colouring applies to the
// mnemonic token when a non-default pen is set.
func TestSyntaxRoleMnemonicColour(t *testing.T) {
	cfg := defaultConfig()
	cfg.RoleMnemonic = roleSpec{Pen: 1} // comment pen (grey), distinct from code pen 3
	m, dl := model([3]string{"nop", "", ""})
	s, _ := renderAt(t, cfg, m, dl, 0)
	_, rows := s.Size()
	// Find the "nop" row and check the mnemonic ink.
	for y := 0; y < rows; y++ {
		if strings.TrimSpace(gridRow(s, y)) == "nop" {
			if s.At(2, y).Ink != 1 {
				t.Errorf("role_mnemonic pen=1: mnemonic ink = %d, want 1", s.At(2, y).Ink)
			}
			return
		}
	}
	t.Fatal("nop row not found")
}

// TestSyntaxRoleImmediateColour confirms role_immediate colouring applies to
// immediate tokens when mark_immediates is off.
func TestSyntaxRoleImmediateColour(t *testing.T) {
	cfg := defaultConfig()
	cfg.RoleImmediate = roleSpec{Pen: 2} // accent pen
	cfg.MarkImmediates = false           // role_immediate active (mark_immediates not overriding)
	m, dl := model([3]string{"mov", "x0, #16", ""})
	cfg.ImmDropHash = true // so "#16" becomes "16" — simpler token match
	s, _ := renderAt(t, cfg, m, dl, 0)
	_, rows := s.Size()
	// Find the row with "mov" and look for a cell with ink=2 (the accent pen).
	for y := 0; y < rows-1; y++ {
		row := gridRow(s, y)
		if strings.Contains(row, "mov") && strings.Contains(row, "16") {
			// Scan cells for ink=2.
			cols, _ := s.Size()
			for x := 0; x < cols; x++ {
				if s.At(x, y).Ink == 2 {
					return // found: correct
				}
			}
			t.Errorf("role_immediate pen=2: no cell with ink=2 on row %q", row)
			return
		}
	}
	t.Fatal("mov row not found")
}

// TestSyntaxRoleInverseModifier confirms the inverse flag swaps ink/paper for
// a token class.
func TestSyntaxRoleInverseModifier(t *testing.T) {
	cfg := defaultConfig()
	// Set mnemonic to inverse: ink=paper(0), paper=pen(3). The mnemonic cell
	// should read (ink=0, paper=3) rather than (ink=3, paper=0).
	cfg.RoleMnemonic = roleSpec{Pen: 3, Inverse: true}
	m, dl := model([3]string{"nop", "", ""})
	s, _ := renderAt(t, cfg, m, dl, 0)
	_, rows := s.Size()
	for y := 0; y < rows; y++ {
		if strings.TrimSpace(gridRow(s, y)) == "nop" {
			cell := s.At(2, y)
			if cell.Ink != uint8(cfg.RolePaper) || cell.Paper != 3 {
				t.Errorf("role_mnemonic inverse: ink=%d paper=%d, want ink=%d paper=3",
					cell.Ink, cell.Paper, cfg.RolePaper)
			}
			return
		}
	}
	t.Fatal("nop row not found")
}

// TestSnapshotRoundTripV12 confirms that the v1.2 new keys survive a snapshot
// round-trip (serialise, parse, compare).
func TestSnapshotRoundTripV12(t *testing.T) {
	orig := defaultConfig()
	orig.ExpandCursorLine = expandWrap
	orig.ExpandK = 3
	orig.ExpandOnlyIfNeeded = true
	orig.CursorBlockStyle = cblBracket
	orig.RoleMnemonic = roleSpec{Pen: 2, Inverse: false}
	orig.RoleExpression = roleSpec{Pen: 3, Inverse: false}
	orig.RoleRegister = roleSpec{Pen: 1, Inverse: true}
	orig.RoleImmediate = roleSpec{Pen: 2, Inverse: false}

	p := writeConfig(t, orig.Snapshot())
	got, err := parseLabConfigFile(p)
	if err != nil {
		t.Fatalf("reparse v1.2 snapshot: %v", err)
	}
	if got != orig {
		t.Errorf("v1.2 snapshot round-trip mismatch:\n got  %+v\n want %+v", got, orig)
	}
}
