package samscreen

import "fmt"

// Screen is the output abstraction (spec §2.1): a fixed W×H cell grid a SAM
// port can implement. The interactive terminal backend and the file-rendering
// mockup backend both satisfy it; the viewer is written against this interface
// alone.
//
// Colours are CLUT indices, not RGB. ink and paper must each be ≤ the mode's
// MaxColour — an out-of-range colour is a constraint violation and panics
// (spec §2.1: "a constraint violation is a prototype bug, not a rendering
// choice").
//
// ch indexes a bitmap font of our own definition, not any fixed SAM character
// set (spec §2.1) — any glyph definable as a W×H 1-bpp pattern in the active
// cell geometry is permitted; the only exclusion is anything outside a
// byte-indexed bitmap font (no Unicode passthrough).
type Screen interface {
	// Size returns the fixed grid dimensions, set at construction from the
	// mode×font choice. The grid never resizes (spec §2.1).
	Size() (w, h int)

	// Geometry returns the mode×font geometry this screen renders.
	Geometry() Geometry

	// Set writes one cell. ch is the font index; ink/paper are CLUT indices
	// (≤ mode MaxColour). It panics on an out-of-range cell or colour.
	Set(x, y int, c Cell)

	// Clear fills the whole grid with one cell (typically a space in the
	// editor's default ink/paper).
	Clear(c Cell)

	// Flush presents the frame (draws to the terminal, or no-ops for the
	// mockup backend, which exports explicitly).
	Flush()
}

// Cell is one character cell: a font glyph index, ink/paper CLUT indices, an
// optional alternate font-bank index for styling, and an optional whole-pen
// blink flag (spec §2.1).
type Cell struct {
	// Ch is the font glyph index (0–255), into the active font bank.
	Ch byte
	// Ink / Paper are CLUT colour indices (≤ mode MaxColour).
	Ink, Paper uint8
	// Bank is the software font bank for styling (spec §2.1: styling is
	// expressible ONLY as an alternate software font bank, never as a terminal
	// attribute). 0 = the default (and only) bank; the prototype ships no
	// styled variant, so Bank stays 0 unless a future variant earns it. A SAM
	// port honours Bank by switching font base address.
	Bank uint8
	// Blink requests the one optional whole-pen attention cue (spec §2.1:
	// palette-swap blink, mode-agnostic, nothing per-cell). It marks the cell's
	// pen as belonging to the blinking CLUT entry; the backend alternates that
	// entry between two colours at frame rate.
	Blink bool
}

// SpaceCell is the editor's default empty cell: a space glyph, ink/paper 0.
var SpaceCell = Cell{Ch: ' '}

// checkColour panics if c exceeds the mode's MaxColour (spec §2.1).
func checkColour(g Geometry, ink, paper uint8) {
	max := g.Mode.MaxColour()
	if ink > max {
		panic(fmt.Sprintf("samscreen: ink %d out of range for %s (max %d)", ink, g.Name, max))
	}
	if paper > max {
		panic(fmt.Sprintf("samscreen: paper %d out of range for %s (max %d)", paper, g.Name, max))
	}
}

// checkBounds panics if (x,y) is outside the grid (spec §2.1: a fixed grid; the
// terminal being bigger leaves a border, it does not extend the grid).
func checkBounds(g Geometry, x, y int) {
	if x < 0 || x >= g.Cols || y < 0 || y >= g.Rows {
		panic(fmt.Sprintf("samscreen: cell (%d,%d) out of range for %s (%dx%d)", x, y, g.Name, g.Cols, g.Rows))
	}
}
