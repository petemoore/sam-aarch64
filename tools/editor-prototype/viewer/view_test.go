package viewer

import (
	"strings"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
)

// fakeDoc builds a Document from plain text lines (no .tbn needed), each as a
// synthetic render.Line, for unit-testing scroll/wrap independent of the format
// libs. RecordIdx is -1 (sidecar) so RecordKindAt reports "comment" — fine for
// these layout tests, which never assert the record kind.
func fakeDoc(lines ...string) *Document {
	d := &Document{Path: "fake.tbn"}
	for _, s := range lines {
		d.Lines = append(d.Lines, render.Line{RecordIdx: -1, Text: s})
	}
	return d
}

// readGrid returns the text of every grid row as a string slice (trailing
// spaces trimmed), for asserting what the viewer painted.
func readGrid(s testGrid) []string {
	cols, rows := s.Size()
	out := make([]string, rows)
	for y := 0; y < rows; y++ {
		var b strings.Builder
		for x := 0; x < cols; x++ {
			b.WriteByte(s.At(x, y).Ch)
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

// testGrid is a minimal samscreen.Screen for unit tests (no terminal, no
// raster): the shared Grid with a no-op Flush, plus At for inspection.
type testGrid struct{ *samscreen.Grid }

func (testGrid) Flush() {}

func newTestGrid(t *testing.T, name string) testGrid {
	t.Helper()
	g, err := samscreen.LookupGeometry(name)
	if err != nil {
		t.Fatal(err)
	}
	return testGrid{samscreen.NewGrid(g)}
}

// TestCursorStartsAtTop confirms the viewer opens at line 1 with the document
// top visible (centre-lock pins to line 0 near the top, spec §5).
func TestCursorStartsAtTop(t *testing.T) {
	doc := fakeDoc("alpha", "beta", "gamma")
	v := NewView(doc)
	s := newTestGrid(t, "mode3-8x8")
	v.Render(s)
	grid := readGrid(s)
	if grid[0] != "alpha" {
		t.Errorf("row 0 = %q, want first line at top", grid[0])
	}
	if v.Cursor() != 0 {
		t.Errorf("cursor = %d, want 0", v.Cursor())
	}
}

// TestCentreLockScroll confirms that once the cursor is past the centre, the
// cursor row sits at the vertical centre and the document scrolls under it.
func TestCentreLockScroll(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, lineLabel(i))
	}
	doc := fakeDoc(lines...)
	v := NewView(doc)
	s := newTestGrid(t, "mode3-8x8") // 24 rows → 23 text rows, centre = 11
	v.SetCursor(50)
	v.Render(s)
	grid := readGrid(s)
	// The cursor's line (50) should be at the centre text row (11).
	centre := 23 / 2
	if grid[centre] != lineLabel(50) {
		t.Errorf("centre row %d = %q, want %q", centre, grid[centre], lineLabel(50))
	}
}

// TestBottomClamp confirms Bottom lands on the last line and does not scroll
// past the end.
func TestBottomClamp(t *testing.T) {
	doc := fakeDoc("a", "b", "c")
	v := NewView(doc)
	v.Bottom()
	if v.Cursor() != 2 {
		t.Errorf("cursor = %d, want 2", v.Cursor())
	}
	v.Move(10)
	if v.Cursor() != 2 {
		t.Errorf("cursor over-ran to %d, want clamp at 2", v.Cursor())
	}
}

// TestWrapVsTruncate confirms a long line wraps into multiple display rows when
// Wrap is on, and occupies a single (clipped) row when off.
func TestWrapVsTruncate(t *testing.T) {
	long := strings.Repeat("x", 200)
	doc := fakeDoc(long, "next")
	s := newTestGrid(t, "mode3-8x8") // 64 cols
	v := NewView(doc)

	v.Wrap = true
	v.Render(s)
	wrapGrid := readGrid(s)
	// Row 0 is the cursor line (first 64 'x'); a wrapped row follows with a
	// continuation marker '\'.
	if len(wrapGrid[0]) != 64 {
		t.Errorf("wrapped row 0 len = %d, want 64", len(wrapGrid[0]))
	}
	if !strings.HasPrefix(wrapGrid[1], "\\") {
		t.Errorf("wrapped row 1 = %q, want continuation marker", wrapGrid[1])
	}

	v.Wrap = false
	s2 := newTestGrid(t, "mode3-8x8")
	v.Render(s2)
	truncGrid := readGrid(s2)
	// In truncate mode the long line occupies one row; the next source line
	// follows immediately on row 1.
	if truncGrid[1] != "next" {
		t.Errorf("truncate row 1 = %q, want 'next' (no wrap rows)", truncGrid[1])
	}
}

// TestStatusLine confirms the status line shows file · record kind · line/total
// · geometry · mode (spec §5).
func TestStatusLine(t *testing.T) {
	doc := fakeDoc("a", "b", "c")
	v := NewView(doc)
	s := newTestGrid(t, "mode3-8x8")
	v.Render(s)
	_, h := s.Size()
	var b strings.Builder
	cols, _ := s.Size()
	for x := 0; x < cols; x++ {
		b.WriteByte(s.At(x, h-1).Ch)
	}
	status := b.String()
	for _, want := range []string{"fake.tbn", "1/3", "mode3-8x8", "wrap"} {
		if !strings.Contains(status, want) {
			t.Errorf("status %q missing %q", status, want)
		}
	}
}

func lineLabel(i int) string {
	return "L" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
