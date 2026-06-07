package samscreen

import "testing"

func mode3Grid(t *testing.T) *Grid {
	t.Helper()
	g, err := LookupGeometry("mode3-8x8")
	if err != nil {
		t.Fatal(err)
	}
	return NewGrid(g)
}

// TestColourConstraintPanics asserts the abstraction enforces the mode's
// colour ceiling — a MODE 3 cell with colour 4 (only 0–3 valid) panics
// (spec §2.1: a constraint violation is a prototype bug, not a rendering
// choice).
func TestColourConstraintPanics(t *testing.T) {
	g := mode3Grid(t)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on out-of-range MODE 3 colour")
		}
	}()
	g.Set(0, 0, Cell{Ch: 'x', Ink: 4})
}

// TestColourInRangeOK confirms valid MODE 3 colours (0–3) are accepted.
func TestColourInRangeOK(t *testing.T) {
	g := mode3Grid(t)
	for c := uint8(0); c <= 3; c++ {
		g.Set(0, 0, Cell{Ch: 'x', Ink: c, Paper: 3 - c})
	}
}

// TestMode4AllowsHigherColour confirms MODE 4 accepts colours up to 15 that
// MODE 3 would reject.
func TestMode4AllowsHigherColour(t *testing.T) {
	g, err := LookupGeometry("mode4-8x8")
	if err != nil {
		t.Fatal(err)
	}
	grid := NewGrid(g)
	grid.Set(0, 0, Cell{Ch: 'x', Ink: 15, Paper: 0})
}

// TestBoundsPanic asserts an out-of-range cell index panics (fixed grid,
// spec §2.1).
func TestBoundsPanic(t *testing.T) {
	g := mode3Grid(t)
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {64, 0}, {0, 24}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("expected panic for cell (%d,%d)", p[0], p[1])
				}
			}()
			g.Set(p[0], p[1], SpaceCell)
		}()
	}
}

// TestSizeFixed asserts Size matches the geometry and does not change.
func TestSizeFixed(t *testing.T) {
	g := mode3Grid(t)
	w, h := g.Size()
	if w != 64 || h != 24 {
		t.Fatalf("size = %dx%d, want 64x24", w, h)
	}
}

// TestSetStringClips confirms SetString clips at the right edge rather than
// wrapping or extending the grid.
func TestSetStringClips(t *testing.T) {
	g := mode3Grid(t)
	long := make([]byte, 100)
	for i := range long {
		long[i] = 'A'
	}
	g.SetString(60, 0, long, 3, 0) // 60+ at width 64 → only 4 cells fit
	if g.At(63, 0).Ch != 'A' {
		t.Errorf("cell (63,0) = %q, want 'A'", g.At(63, 0).Ch)
	}
	// No panic, and column 64 would be out of range (clipped).
}
