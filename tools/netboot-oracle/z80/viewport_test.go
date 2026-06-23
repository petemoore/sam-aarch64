// viewport_test.go — host-verification of src/viewport.asm (item i4a).
//
// Drives the Z80 viewport/scroll state machine under the flat-memory koron-go/z80
// harness and asserts it tracks a Go oracle mirroring the host authority
// tools/editor-prototype/viewer/view.go (non-wrapped / truncate mode) over random
// navigation sequences — the same Go-authority-+-Z80-port pattern as
// editmodel_test.go / pagepool_test.go.
//
// The oracle restates view.go's centre-locked-cursor math for the non-wrapped
// case (one display row per source line): cursor movement (SetCursor/Move/Top/
// Bottom/PageDown/PageUp) plus winTop = max(0, cursor - rows/2), cursorRow =
// cursor - winTop, visibleCount = min(rows, lineCount - winTop). Wrapping is
// render-coupled and belongs to the i4 rendering brick, not this one.
package z80_test

import (
	"fmt"
	"math/rand"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	vpBinPath = "../../../build/viewport.bin"
	vpMapPath = "../../../build/viewport.map"
)

func loadViewport(t *testing.T) *z80h.Machine {
	t.Helper()
	mac, err := z80h.Load(vpBinPath, vpMapPath)
	if err != nil {
		t.Skipf("viewport binary not built (%s); run `make viewport-z80`: %v", vpBinPath, err)
	}
	return mac
}

// vpOracle restates view.go's non-wrapped centre-locked-cursor behaviour.
type vpOracle struct {
	cursor    int
	lineCount int
	rows      int
}

func (o *vpOracle) clamp(i int) int {
	if o.lineCount == 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i > o.lineCount-1 {
		return o.lineCount - 1
	}
	return i
}
func (o *vpOracle) setCursor(i int) { o.cursor = o.clamp(i) }
func (o *vpOracle) lineDown()       { o.setCursor(o.cursor + 1) }
func (o *vpOracle) lineUp() {
	if o.cursor > 0 {
		o.cursor--
	}
}
func (o *vpOracle) pageDown() { o.setCursor(o.cursor + o.rows) }
func (o *vpOracle) pageUp() {
	o.cursor -= o.rows
	if o.cursor < 0 {
		o.cursor = 0
	}
}
func (o *vpOracle) top()    { o.cursor = 0 }
func (o *vpOracle) bottom() { o.cursor = o.clamp(o.lineCount - 1) }
func (o *vpOracle) winTop() int {
	w := o.cursor - o.rows/2
	if w < 0 {
		w = 0
	}
	return w
}
func (o *vpOracle) cursorRow() int { return o.cursor - o.winTop() }
func (o *vpOracle) visibleCount() int {
	v := o.lineCount - o.winTop()
	if v > o.rows {
		v = o.rows
	}
	return v
}

// Z80 driver helpers.
func vpInit(t *testing.T, mac *z80h.Machine, lineCount, rows int) {
	t.Helper()
	if _, err := mac.CallEntry("vp_init", z80h.Entry{BC: uint16(lineCount), A: uint8(rows)}); err != nil {
		t.Fatalf("vp_init: %v", err)
	}
}
func vpCallHL(t *testing.T, mac *z80h.Machine, entry string) int {
	t.Helper()
	res, err := mac.CallEntry(entry, z80h.Entry{})
	if err != nil {
		t.Fatalf("%s: %v", entry, err)
	}
	return int(res.HL)
}
func vpCallA(t *testing.T, mac *z80h.Machine, entry string) int {
	t.Helper()
	res, err := mac.CallEntry(entry, z80h.Entry{})
	if err != nil {
		t.Fatalf("%s: %v", entry, err)
	}
	return int(res.A)
}
func vpCursor(t *testing.T, mac *z80h.Machine) int  { return vpCallHL(t, mac, "vp_get_cursor") }
func vpWinTop(t *testing.T, mac *z80h.Machine) int  { return vpCallHL(t, mac, "vp_win_top") }
func vpCurRow(t *testing.T, mac *z80h.Machine) int  { return vpCallA(t, mac, "vp_cursor_row") }
func vpVisible(t *testing.T, mac *z80h.Machine) int { return vpCallA(t, mac, "vp_visible_count") }

// assertState checks the four read-back values against the oracle.
func assertState(t *testing.T, mac *z80h.Machine, o *vpOracle, ctx string) {
	t.Helper()
	if got := vpCursor(t, mac); got != o.cursor {
		t.Fatalf("%s: cursor = %d, want %d", ctx, got, o.cursor)
	}
	if got := vpWinTop(t, mac); got != o.winTop() {
		t.Fatalf("%s: winTop = %d, want %d (cursor %d rows %d)", ctx, got, o.winTop(), o.cursor, o.rows)
	}
	if got := vpCurRow(t, mac); got != o.cursorRow() {
		t.Fatalf("%s: cursorRow = %d, want %d", ctx, got, o.cursorRow())
	}
	if got := vpVisible(t, mac); got != o.visibleCount() {
		t.Fatalf("%s: visibleCount = %d, want %d", ctx, got, o.visibleCount())
	}
}

// TestViewportBasics pins the named behaviours the host view_test.go asserts:
// start-at-top, centre-lock past the middle, bottom clamp.
func TestViewportBasics(t *testing.T) {
	mac := loadViewport(t)
	const lineCount, rows = 100, 23 // mode3-8x8: 24 screen rows -> 23 text rows
	o := &vpOracle{lineCount: lineCount, rows: rows}
	vpInit(t, mac, lineCount, rows)

	// Start at top: cursor 0, winTop 0, cursorRow 0.
	assertState(t, mac, o, "init")
	if vpCursor(t, mac) != 0 {
		t.Fatalf("cursor not 0 at init")
	}

	// Centre-lock: cursor 50 -> cursorRow = centre = 11, winTop = 39.
	if _, err := mac.CallEntry("vp_set_cursor", z80h.Entry{BC: 50}); err != nil {
		t.Fatal(err)
	}
	o.setCursor(50)
	assertState(t, mac, o, "setCursor(50)")
	if got := vpCurRow(t, mac); got != rows/2 {
		t.Errorf("cursorRow at line 50 = %d, want centre %d", got, rows/2)
	}

	// Bottom clamp.
	if _, err := mac.CallEntry("vp_bottom", z80h.Entry{}); err != nil {
		t.Fatal(err)
	}
	o.bottom()
	assertState(t, mac, o, "bottom")
	if vpCursor(t, mac) != lineCount-1 {
		t.Errorf("bottom cursor = %d, want %d", vpCursor(t, mac), lineCount-1)
	}
	// Over-run past bottom stays clamped.
	for i := 0; i < 5; i++ {
		mac.CallEntry("vp_line_down", z80h.Entry{})
		o.lineDown()
	}
	assertState(t, mac, o, "lineDown past bottom")

	// Top clamp + line-up at 0.
	mac.CallEntry("vp_top", z80h.Entry{})
	o.top()
	for i := 0; i < 5; i++ {
		mac.CallEntry("vp_line_up", z80h.Entry{})
		o.lineUp()
	}
	assertState(t, mac, o, "lineUp past top")
}

// TestViewportRandomNav fuzzes the nav commands against the oracle over several
// (lineCount, rows) geometries, asserting all four read-backs after every step.
func TestViewportRandomNav(t *testing.T) {
	geometries := []struct{ lineCount, rows int }{
		{9000, 23}, // release.s scale, mode3-8x8
		{9000, 31}, // mode3-6x6 (32 rows -> 31 text)
		{40, 23},   // document shorter than the screen
		{1, 23},    // single line
		{0, 23},    // empty document (degenerate)
	}
	for _, g := range geometries {
		g := g
		t.Run(fmt.Sprintf("lc%d_r%d", g.lineCount, g.rows), func(t *testing.T) {
			mac := loadViewport(t)
			o := &vpOracle{lineCount: g.lineCount, rows: g.rows}
			vpInit(t, mac, g.lineCount, g.rows)
			rng := rand.New(rand.NewSource(int64(g.lineCount*100 + g.rows)))

			for step := 0; step < 1200; step++ {
				op := rng.Intn(7)
				var name string
				switch op {
				case 0:
					name = "vp_line_down"
					o.lineDown()
				case 1:
					name = "vp_line_up"
					o.lineUp()
				case 2:
					name = "vp_page_down"
					o.pageDown()
				case 3:
					name = "vp_page_up"
					o.pageUp()
				case 4:
					name = "vp_top"
					o.top()
				case 5:
					name = "vp_bottom"
					o.bottom()
				case 6:
					// set_cursor to a random (possibly out-of-range) target.
					target := rng.Intn(g.lineCount + 50)
					if _, err := mac.CallEntry("vp_set_cursor", z80h.Entry{BC: uint16(target)}); err != nil {
						t.Fatalf("step %d: vp_set_cursor: %v", step, err)
					}
					o.setCursor(target)
					assertState(t, mac, o, fmt.Sprintf("step %d set_cursor(%d)", step, target))
					continue
				}
				if _, err := mac.CallEntry(name, z80h.Entry{}); err != nil {
					t.Fatalf("step %d: %s: %v", step, name, err)
				}
				assertState(t, mac, o, fmt.Sprintf("step %d %s", step, name))
			}
		})
	}
}
