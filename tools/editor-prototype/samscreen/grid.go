package samscreen

// Grid is the shared cell buffer both backends embed. It owns the geometry,
// enforces the §2.1 bounds + colour constraints on every Set, and exposes the
// buffer for a backend to present (the terminal copies it to tcell; the mockup
// rasterises it to pixels). It is the single place the abstraction's invariants
// live, so neither backend can bypass them.
type Grid struct {
	geom  Geometry
	cells []Cell // row-major, len == Cols*Rows
	// offSAM lifts the mode's colour ceiling for the explicitly off-SAM
	// "dreaming" path only (the lab's relax_palette: unlimited terminal
	// colours, can never render a SAM PNG). A normal SAM grid stays bounded —
	// the invariant is not weakened for it; only a grid built with
	// NewOffSAMGrid opts out, and it is plainly labelled as off-SAM.
	offSAM bool
}

// NewGrid builds a cleared grid for geom (every cell SpaceCell).
func NewGrid(geom Geometry) *Grid {
	g := &Grid{
		geom:  geom,
		cells: make([]Cell, geom.Cols*geom.Rows),
	}
	g.Clear(SpaceCell)
	return g
}

// NewOffSAMGrid builds a grid that keeps geom's layout (cell count, font) but
// lifts the colour ceiling to the full 16-entry CLUT — for the lab's
// relax_palette dreaming mode, which is explicitly not constrained to a SAM
// quadruple and refuses SAM PNG export. Cells still index a 16-entry Palette;
// values 0–15 are accepted.
func NewOffSAMGrid(geom Geometry) *Grid {
	g := NewGrid(geom)
	g.offSAM = true
	return g
}

// Size returns the fixed grid dimensions.
func (g *Grid) Size() (w, h int) { return g.geom.Cols, g.geom.Rows }

// Geometry returns the grid's geometry.
func (g *Grid) Geometry() Geometry { return g.geom }

// Set writes one cell, enforcing the abstraction's constraints (panics on an
// out-of-range cell or colour — spec §2.1).
func (g *Grid) Set(x, y int, c Cell) {
	checkBounds(g.geom, x, y)
	g.checkColour(c.Ink, c.Paper)
	g.cells[y*g.geom.Cols+x] = c
}

// Clear fills every cell with c (colour-checked once).
func (g *Grid) Clear(c Cell) {
	g.checkColour(c.Ink, c.Paper)
	for i := range g.cells {
		g.cells[i] = c
	}
}

// checkColour enforces the colour ceiling: the mode's MaxColour for a SAM grid,
// or the full 16-entry CLUT (0–15) for an off-SAM grid.
func (g *Grid) checkColour(ink, paper uint8) {
	if g.offSAM {
		if ink > 15 || paper > 15 {
			panic("samscreen: off-SAM colour index exceeds the 16-entry CLUT")
		}
		return
	}
	checkColour(g.geom, ink, paper)
}

// At returns the cell at (x,y) for a backend to present. Panics out of range.
func (g *Grid) At(x, y int) Cell {
	checkBounds(g.geom, x, y)
	return g.cells[y*g.geom.Cols+x]
}

// SetString writes s starting at (x,y) on one row, clipping at the right edge.
// Bytes are font indices; no Unicode decoding (spec §2.1). It is a convenience
// for the viewer's row painting and applies the same per-cell constraints.
func (g *Grid) SetString(x, y int, s []byte, ink, paper uint8) {
	for i := 0; i < len(s) && x+i < g.geom.Cols; i++ {
		g.Set(x+i, y, Cell{Ch: s[i], Ink: ink, Paper: paper})
	}
}
