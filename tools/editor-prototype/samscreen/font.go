package samscreen

import "fmt"

// Font is a 1-bpp bitmap font for one cell geometry (spec §2.1: glyphs are our
// own definition, any W×H 1-bpp bit pattern). The mockup backend rasterises
// glyphs through this; the terminal backend ignores it (the terminal draws its
// own font), but it still picks a Font so the two backends agree on cell size.
type Font interface {
	// Size is the glyph cell geometry. It must match the geometry the screen
	// renders.
	Size() FontSize
	// Row returns the bit pattern for one pixel row of glyph ch: bit (W-1) is
	// the leftmost pixel, bit 0 the rightmost. row is 0..H-1.
	Row(ch byte, row int) uint8
}

// BitmapFont is a Font backed by a flat per-glyph row table. Glyphs is indexed
// [ch][row]; each entry is the row's bit pattern with bit (W-1) leftmost. This
// is exactly the SAM's row-padded software-font format (one byte per row),
// so a SAM port loads the same bytes (spec §5 P1 step 2 — the converter emits
// these bytes for the Z80 routine).
type BitmapFont struct {
	W, H   int
	Glyphs [256][]uint8
}

// Size returns the font cell geometry.
func (f *BitmapFont) Size() FontSize { return FontSize{f.W, f.H} }

// Row returns glyph ch's bit pattern for pixel row r (0 if out of range).
func (f *BitmapFont) Row(ch byte, r int) uint8 {
	rows := f.Glyphs[ch]
	if r < 0 || r >= len(rows) {
		return 0
	}
	return rows[r]
}

// NewBitmapFont builds a BitmapFont from a [][]uint8 glyph table. Each glyph
// must have exactly h rows; a glyph row's bit (w-1) is the leftmost pixel. The
// table may cover fewer than 256 glyphs (the rest render blank).
func NewBitmapFont(w, h int, glyphs [][]uint8) (*BitmapFont, error) {
	f := &BitmapFont{W: w, H: h}
	for ch, rows := range glyphs {
		if ch >= 256 {
			break
		}
		if len(rows) != h {
			return nil, fmt.Errorf("font glyph %d has %d rows, want %d", ch, len(rows), h)
		}
		cp := make([]uint8, h)
		copy(cp, rows)
		f.Glyphs[ch] = cp
	}
	return f, nil
}
