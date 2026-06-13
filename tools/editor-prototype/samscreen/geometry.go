package samscreen

import (
	"fmt"
	"sort"
	"strings"
)

// FontSize is a cell pixel geometry: width × height. The SAM renders all text
// in software, so any of these is a legitimate cell size — the constraint is
// only that a glyph be definable as a W×H 1-bpp bit pattern (spec §2.1).
type FontSize struct {
	W, H int
}

func (f FontSize) String() string { return fmt.Sprintf("%dx%d", f.W, f.H) }

// Geometry is one mode×font choice: it fixes the cell grid (Cols×Rows) the
// abstraction presents, derived purely from the mode's pixel resolution
// divided by the font cell size (spec §3 — "every layout decision reads
// Size(); nothing hardcodes a width"). Construction is the single place mode
// and font are paired; the grid follows by arithmetic.
type Geometry struct {
	Name string
	Mode Mode
	Font FontSize
	Cols int
	Rows int
}

// geometries is the full §3 mode×font matrix as -geometry options. MODE 2 / 1
// rows of the spec table are not candidate editor modes and are not offered as
// flags (spec §3 †‡); the prototype targets MODES 3 and 4.
var geometries = func() map[string]Geometry {
	defs := []struct {
		mode Mode
		font FontSize
	}{
		{Mode3, FontSize{8, 8}}, // 64×24 — default (spec §8 D2)
		{Mode3, FontSize{6, 8}}, // 85×24
		{Mode3, FontSize{6, 6}}, // 85×32
		{Mode4, FontSize{8, 8}}, // 32×24
		{Mode4, FontSize{6, 8}}, // 42×24
		{Mode4, FontSize{6, 6}}, // 42×32
	}
	m := make(map[string]Geometry, len(defs))
	for _, d := range defs {
		g := newGeometry(d.mode, d.font)
		m[g.Name] = g
	}
	return m
}()

// DefaultGeometry is the P1 default: MODE 3 64×24 with the 8×8 font (spec §8
// R2 / D2).
const DefaultGeometry = "mode3-8x8"

func newGeometry(mode Mode, font FontSize) Geometry {
	return Geometry{
		Name: fmt.Sprintf("%s-%s", mode.String(), font.String()),
		Mode: mode,
		Font: font,
		Cols: mode.PixelWidth() / font.W,
		Rows: mode.PixelHeight() / font.H,
	}
}

// LookupGeometry resolves a -geometry flag value (e.g. "mode3-8x8") to its
// Geometry, or returns an error listing the valid options.
func LookupGeometry(name string) (Geometry, error) {
	if g, ok := geometries[name]; ok {
		return g, nil
	}
	return Geometry{}, fmt.Errorf("unknown geometry %q; valid options: %s", name, strings.Join(GeometryNames(), ", "))
}

// GeometryNames lists every -geometry option, in a stable order.
func GeometryNames() []string {
	names := make([]string, 0, len(geometries))
	for n := range geometries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// AllGeometries returns every Geometry in stable name order (used by the
// mockup backend to render the full matrix).
func AllGeometries() []Geometry {
	gs := make([]Geometry, 0, len(geometries))
	for _, n := range GeometryNames() {
		gs = append(gs, geometries[n])
	}
	return gs
}
