package mockup

import (
	"testing"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/fonts"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
)

func geom(t *testing.T, name string) samscreen.Geometry {
	t.Helper()
	g, err := samscreen.LookupGeometry(name)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestImageDimensions asserts every geometry exports at the aspect-correct
// 512×384 (spec §2.4), both for a real-font and a placeholder screen.
func TestImageDimensions(t *testing.T) {
	f8, err := fonts.Font8x8()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range samscreen.GeometryNames() {
		g := geom(t, name)
		var s *Screen
		if g.Font.W == 8 && g.Font.H == 8 {
			s = New(g, f8)
		} else {
			s = New(g, nil, WithPlaceholder("pending"))
		}
		img := s.Image()
		if img.Bounds().Dx() != 512 || img.Bounds().Dy() != 384 {
			t.Errorf("%s image = %dx%d, want 512x384", name, img.Bounds().Dx(), img.Bounds().Dy())
		}
	}
}

// TestGlyphRastersToPixels is the renderer golden test (deterministic, no
// binary fixture): draw 'A' (ink 3, paper 0) at cell (0,0) in MODE 3 8×8 and
// assert the top-left pixel block matches the glyph's MSB-leftmost rows. MODE 3
// exports 1×2, so each glyph pixel row maps to two image rows.
func TestGlyphRastersToPixels(t *testing.T) {
	f8, err := fonts.Font8x8()
	if err != nil {
		t.Fatal(err)
	}
	g := geom(t, "mode3-8x8")
	s := New(g, f8)
	s.Set(0, 0, samscreen.Cell{Ch: 'A', Ink: 3, Paper: 0})
	img := s.Image()

	pal := samscreen.DefaultPalette()
	ink := pal.RGBA(3)
	paper := pal.RGBA(0)
	for gy := 0; gy < 8; gy++ {
		bits := f8.Row('A', gy)
		for gx := 0; gx < 8; gx++ {
			on := (bits>>(uint(7-gx)))&1 == 1
			want := paper
			if on {
				want = ink
			}
			// MODE 3 export scale is 1×2: image (gx, gy*2).
			got := img.RGBAAt(gx, gy*2)
			if got != want {
				t.Fatalf("pixel (%d,%d) = %v, want %v (glyph bit on=%v)", gx, gy*2, got, want, on)
			}
		}
	}
}

// TestScreenBytesLength asserts the SCREEN$ payload is the 24 KB display +
// 16-byte PALTAB (spec §2.4).
func TestScreenBytesLength(t *testing.T) {
	f8, err := fonts.Font8x8()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mode3-8x8", "mode4-8x8"} {
		s := New(geom(t, name), f8)
		s.Set(0, 0, samscreen.Cell{Ch: 'X', Ink: 3, Paper: 0})
		b, err := s.ScreenBytes()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(b) != displayBytes+16 {
			t.Errorf("%s SCREEN$ = %d bytes, want %d", name, len(b), displayBytes+16)
		}
	}
}

// TestColourConstraintEnforcedViaScreen confirms the abstraction's colour
// guard reaches through the mockup backend: a MODE 3 ink of 4 panics.
func TestColourConstraintEnforcedViaScreen(t *testing.T) {
	f8, _ := fonts.Font8x8()
	s := New(geom(t, "mode3-8x8"), f8)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on out-of-range MODE 3 colour")
		}
	}()
	s.Set(0, 0, samscreen.Cell{Ch: 'x', Ink: 4})
}
