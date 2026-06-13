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
	cfg.RoleCode = 7 // beyond the 0-3 quad
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
