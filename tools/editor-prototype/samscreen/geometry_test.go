package samscreen

import "testing"

// TestGeometryMatrix asserts the §3 mode×font matrix arithmetic: cols = pixel
// width / font width, rows = 192 / font height, for every offered geometry.
func TestGeometryMatrix(t *testing.T) {
	want := map[string]struct{ cols, rows int }{
		"mode3-8x8": {64, 24},
		"mode3-6x8": {85, 24},
		"mode3-6x6": {85, 32},
		"mode4-8x8": {32, 24},
		"mode4-6x8": {42, 24},
		"mode4-6x6": {42, 32},
	}
	if len(want) != len(geometries) {
		t.Fatalf("offered %d geometries, spec table has %d", len(geometries), len(want))
	}
	for name, exp := range want {
		g, err := LookupGeometry(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if g.Cols != exp.cols || g.Rows != exp.rows {
			t.Errorf("%s: got %dx%d, want %dx%d (spec §3 table)", name, g.Cols, g.Rows, exp.cols, exp.rows)
		}
	}
}

// TestDefaultGeometry pins the P1 default to MODE 3 64×24 (spec §8 R2/D2).
func TestDefaultGeometry(t *testing.T) {
	g, err := LookupGeometry(DefaultGeometry)
	if err != nil {
		t.Fatal(err)
	}
	if g.Mode != Mode3 || g.Cols != 64 || g.Rows != 24 {
		t.Fatalf("default geometry = %s (%dx%d, mode %v); want mode3 64x24", g.Name, g.Cols, g.Rows, g.Mode)
	}
}

// TestExportAspect asserts both modes export to 512×384 with the right
// per-pixel scale (spec §2.4: MODE 4 2×2, MODE 3 1×2).
func TestExportAspect(t *testing.T) {
	for _, tc := range []struct {
		mode     Mode
		sx, sy   int
		exW, exH int
	}{
		{Mode4, 2, 2, 512, 384},
		{Mode3, 1, 2, 512, 384},
	} {
		sx, sy := tc.mode.ExportPixelScale()
		if sx != tc.sx || sy != tc.sy {
			t.Errorf("%v scale = %dx%d, want %dx%d", tc.mode, sx, sy, tc.sx, tc.sy)
		}
		w := tc.mode.PixelWidth() * sx
		h := tc.mode.PixelHeight() * sy
		if w != tc.exW || h != tc.exH {
			t.Errorf("%v export = %dx%d, want %dx%d", tc.mode, w, h, tc.exW, tc.exH)
		}
	}
}

// TestLookupGeometryUnknown errors on a bad -geometry value.
func TestLookupGeometryUnknown(t *testing.T) {
	if _, err := LookupGeometry("mode5-9x9"); err == nil {
		t.Fatal("expected error for unknown geometry")
	}
}

// TestMaxColour pins the per-mode colour ceiling (spec §2.1).
func TestMaxColour(t *testing.T) {
	if Mode4.MaxColour() != 15 {
		t.Errorf("MODE 4 max colour = %d, want 15", Mode4.MaxColour())
	}
	if Mode3.MaxColour() != 3 {
		t.Errorf("MODE 3 max colour = %d, want 3", Mode3.MaxColour())
	}
}
