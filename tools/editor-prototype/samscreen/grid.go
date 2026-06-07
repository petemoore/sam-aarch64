package samscreen

// Grid is the shared cell buffer both backends embed. It owns the geometry,
// enforces the §2.1 bounds + colour constraints on every Set, and exposes the
// buffer for a backend to present (the terminal copies it to tcell; the mockup
// rasterises it to pixels). It is the single place the abstraction's invariants
// live, so neither backend can bypass them.
type Grid struct {
	geom  Geometry
	cells []Cell // row-major, len == Cols*Rows
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

// Size returns the fixed grid dimensions.
func (g *Grid) Size() (w, h int) { return g.geom.Cols, g.geom.Rows }

// Geometry returns the grid's geometry.
func (g *Grid) Geometry() Geometry { return g.geom }

// Set writes one cell, enforcing the abstraction's constraints (panics on an
// out-of-range cell or colour — spec §2.1).
func (g *Grid) Set(x, y int, c Cell) {
	checkBounds(g.geom, x, y)
	checkColour(g.geom, c.Ink, c.Paper)
	g.cells[y*g.geom.Cols+x] = c
}

// Clear fills every cell with c (colour-checked once).
func (g *Grid) Clear(c Cell) {
	checkColour(g.geom, c.Ink, c.Paper)
	for i := range g.cells {
		g.cells[i] = c
	}
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
