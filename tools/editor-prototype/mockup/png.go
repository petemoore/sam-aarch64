// Package mockup is the file-rendering backend (spec §2.4, the q1 deliverable):
// the same samscreen.Screen interface rendering to PNG at exact mode geometry
// and to a SAM SCREEN$ file, instead of to a terminal. It is how the §3
// geometry options get compared as images and put on a real SAM screen.
package mockup

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
)

// Screen is the mockup backend: a cell grid that rasterises to pixels through a
// bitmap font and a CLUT palette. It satisfies samscreen.Screen; Flush is a
// no-op (export is explicit via WritePNG / WriteSCREEN). When Font is nil (a
// 6-px geometry whose font is not yet vendored), it renders a placeholder note
// instead of glyphs (spec §6 — "6-px options may render with a placeholder
// note until P1b's font lands").
type Screen struct {
	*samscreen.Grid
	font    samscreen.Font
	pal     samscreen.Palette
	crt     bool   // scanline-CRT darkening (spec §2.4 R3 — off by default)
	noFont  bool   // true when no glyph font is available (placeholder mode)
	noteMsg string // placeholder note, drawn when noFont
}

// Option configures a mockup Screen.
type Option func(*Screen)

// WithCRT enables the optional scanline-CRT darkening variant (spec §2.4 R3).
func WithCRT() Option { return func(s *Screen) { s.crt = true } }

// WithPlaceholder marks the screen as font-less: WritePNG draws note centred on
// a plain field instead of glyphs (for 6-px geometries pending the font-proof
// leg's vendored font).
func WithPlaceholder(note string) Option {
	return func(s *Screen) { s.noFont = true; s.noteMsg = note }
}

// New builds a mockup Screen for geom with font (may be nil with
// WithPlaceholder) and the default power-on CLUT.
func New(geom samscreen.Geometry, font samscreen.Font, opts ...Option) *Screen {
	s := &Screen{
		Grid: samscreen.NewGrid(geom),
		font: font,
		pal:  samscreen.DefaultPalette(),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Flush is a no-op: the mockup backend exports explicitly.
func (s *Screen) Flush() {}

// Image rasterises the current grid to the mode's true pixel grid, then scales
// per the mode's export aspect (spec §2.4: MODE 4 → 2×2, MODE 3 → 1×2, both
// 512×384). With WithPlaceholder set, it draws the note instead of glyphs.
func (s *Screen) Image() *image.RGBA {
	geom := s.Geometry()
	pw, ph := geom.Mode.PixelWidth(), geom.Mode.PixelHeight()
	base := image.NewRGBA(image.Rect(0, 0, pw, ph))

	if s.noFont {
		s.drawPlaceholder(base)
	} else {
		s.drawGlyphs(base)
	}

	// Aspect-correct export scaling.
	sx, sy := geom.Mode.ExportPixelScale()
	out := image.NewRGBA(image.Rect(0, 0, pw*sx, ph*sy))
	for y := 0; y < ph; y++ {
		for x := 0; x < pw; x++ {
			c := base.RGBAAt(x, y)
			if s.crt && (y%2 == 1) {
				c = darken(c)
			}
			for dy := 0; dy < sy; dy++ {
				for dx := 0; dx < sx; dx++ {
					out.SetRGBA(x*sx+dx, y*sy+dy, c)
				}
			}
		}
	}
	return out
}

func (s *Screen) drawGlyphs(base *image.RGBA) {
	geom := s.Geometry()
	fw, fh := geom.Font.W, geom.Font.H
	cols, rows := geom.Cols, geom.Rows
	for cy := 0; cy < rows; cy++ {
		for cx := 0; cx < cols; cx++ {
			cell := s.At(cx, cy)
			ink := s.pal.RGBA(cell.Ink)
			paper := s.pal.RGBA(cell.Paper)
			for ry := 0; ry < fh; ry++ {
				bits := s.font.Row(cell.Ch, ry)
				for rx := 0; rx < fw; rx++ {
					px := cx*fw + rx
					py := cy*fh + ry
					on := (bits>>(uint(fw-1-rx)))&1 == 1
					if on {
						base.SetRGBA(px, py, ink)
					} else {
						base.SetRGBA(px, py, paper)
					}
				}
			}
		}
	}
}

// drawPlaceholder fills the field with paper colour and writes the note using
// the always-available 8×8 vendored font, centred, so a 6-px-geometry sheet
// still carries an explanatory image (spec §6).
func (s *Screen) drawPlaceholder(base *image.RGBA) {
	geom := s.Geometry()
	paper := s.pal.RGBA(0)
	ink := s.pal.RGBA(7 & geom.Mode.MaxColour())
	for y := base.Rect.Min.Y; y < base.Rect.Max.Y; y++ {
		for x := base.Rect.Min.X; x < base.Rect.Max.X; x++ {
			base.SetRGBA(x, y, paper)
		}
	}
	// Draw the note with the 8×8 font at the field's vertical centre.
	f, err := placeholderFont()
	if err != nil {
		return
	}
	msg := s.noteMsg
	maxChars := geom.Mode.PixelWidth() / 8
	if len(msg) > maxChars {
		msg = msg[:maxChars]
	}
	x0 := (geom.Mode.PixelWidth() - len(msg)*8) / 2
	y0 := (geom.Mode.PixelHeight() - 8) / 2
	for i := 0; i < len(msg); i++ {
		for ry := 0; ry < 8; ry++ {
			bits := f.Row(msg[i], ry)
			for rx := 0; rx < 8; rx++ {
				if (bits>>(uint(7-rx)))&1 == 1 {
					base.SetRGBA(x0+i*8+rx, y0+ry, ink)
				}
			}
		}
	}
}

func darken(c color.RGBA) color.RGBA {
	const num, den = 7, 10
	return color.RGBA{
		R: uint8(int(c.R) * num / den),
		G: uint8(int(c.G) * num / den),
		B: uint8(int(c.B) * num / den),
		A: c.A,
	}
}

// WritePNG encodes the current frame to w.
func (s *Screen) WritePNG(w io.Writer) error {
	return png.Encode(w, s.Image())
}

// WritePNGFile writes the current frame to path.
func (s *Screen) WritePNGFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := s.WritePNG(f); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return f.Close()
}
