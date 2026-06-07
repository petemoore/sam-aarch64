package fonts

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
)

// ErrFontNotVendored signals that a geometry's font has no vendored bitmap yet
// — specifically the 6-pixel fonts, which the parallel font-proof leg vendors
// (spec §5 P1). Callers render a placeholder note for that geometry until the
// font drops in by path; see FontFor.
type ErrFontNotVendored struct {
	Size samscreen.FontSize
	Path string
}

func (e *ErrFontNotVendored) Error() string {
	return fmt.Sprintf("no vendored %s font (expected row-padded binary at %s) — the 6-px font-proof leg vendors it (spec §5 P1)", e.Size, e.Path)
}

// FontFor returns the bitmap font for a geometry's cell size. The 8×8 font is
// the vendored public-domain table (always available). The 6×8 / 6×6 fonts are
// loaded from a row-padded binary under fontDir (font6x8.sam / font6x6.sam) —
// the format the §5 P1 converter emits and a SAM port consumes — so the
// parallel font-proof leg's font drops in by path with no code change. When a
// 6-px binary is absent, FontFor returns an *ErrFontNotVendored so the caller
// can render a placeholder note rather than failing.
func FontFor(size samscreen.FontSize, fontDir string) (samscreen.Font, error) {
	if size.W == 8 && size.H == 8 {
		return Font8x8()
	}
	name := fmt.Sprintf("font%dx%d.sam", size.W, size.H)
	path := filepath.Join(fontDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &ErrFontNotVendored{Size: size, Path: path}
	}
	return LoadRowPadded(size.W, size.H, data)
}

// LoadRowPadded builds a Font from a row-padded binary: one byte per glyph row,
// h rows per glyph, glyph 0 first; bit (w-1) is the leftmost pixel. This is the
// SAM software-font layout the converter emits and the Z80 routine loads (spec
// §5 P1 step 2). The binary may cover up to 256 glyphs; a short binary leaves
// the remaining glyphs blank.
func LoadRowPadded(w, h int, data []byte) (samscreen.Font, error) {
	if h <= 0 || w <= 0 || w > 8 {
		return nil, fmt.Errorf("LoadRowPadded: bad cell %dx%d", w, h)
	}
	if len(data)%h != 0 {
		return nil, fmt.Errorf("LoadRowPadded: %d bytes not a multiple of %d rows/glyph", len(data), h)
	}
	n := len(data) / h
	if n > 256 {
		n = 256
	}
	glyphs := make([][]uint8, 256)
	for ch := 0; ch < 256; ch++ {
		rows := make([]uint8, h)
		if ch < n {
			copy(rows, data[ch*h:ch*h+h])
		}
		glyphs[ch] = rows
	}
	return samscreen.NewBitmapFont(w, h, glyphs)
}

// RowPaddedBytes is the inverse of LoadRowPadded: it serialises a Font to the
// row-padded SAM binary (the converter's output — spec §5 P1 step 2). It emits
// glyphs 0..upTo-1.
func RowPaddedBytes(f samscreen.Font, upTo int) []byte {
	h := f.Size().H
	out := make([]byte, 0, upTo*h)
	for ch := 0; ch < upTo; ch++ {
		for r := 0; r < h; r++ {
			out = append(out, f.Row(byte(ch), r))
		}
	}
	return out
}
