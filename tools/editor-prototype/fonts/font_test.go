package fonts

import (
	"testing"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
)

// TestFont8x8Loads confirms the vendored 8×8 font loads with the right cell
// size and a non-blank uppercase glyph.
func TestFont8x8Loads(t *testing.T) {
	f, err := Font8x8()
	if err != nil {
		t.Fatal(err)
	}
	if f.Size() != (samscreen.FontSize{W: 8, H: 8}) {
		t.Fatalf("size = %v, want 8x8", f.Size())
	}
	// 'A' (0x41) must have set pixels.
	any := false
	for r := 0; r < 8; r++ {
		if f.Row('A', r) != 0 {
			any = true
		}
	}
	if !any {
		t.Error("glyph 'A' is blank")
	}
	// Space (0x20) must be blank.
	for r := 0; r < 8; r++ {
		if f.Row(' ', r) != 0 {
			t.Errorf("space glyph row %d non-blank (%#02x)", r, f.Row(' ', r))
		}
	}
}

// TestFontGlyphAShape pins the exact 'A' bitmap (MSB-leftmost), guarding the
// LSB→MSB bit-reversal done at vendor time.
func TestFontGlyphAShape(t *testing.T) {
	f, err := Font8x8()
	if err != nil {
		t.Fatal(err)
	}
	want := [8]uint8{0x30, 0x78, 0xcc, 0xcc, 0xfc, 0xcc, 0xcc, 0x00}
	for r := 0; r < 8; r++ {
		if got := f.Row('A', r); got != want[r] {
			t.Errorf("'A' row %d = %#02x, want %#02x", r, got, want[r])
		}
	}
}

// TestFontForUnvendored6px asserts FontFor reports ErrFontNotVendored for a
// 6-px geometry whose binary is absent (so callers render a placeholder).
func TestFontForUnvendored6px(t *testing.T) {
	_, err := FontFor(samscreen.FontSize{W: 6, H: 6}, t.TempDir())
	if err == nil {
		t.Fatal("expected ErrFontNotVendored")
	}
	if _, ok := err.(*ErrFontNotVendored); !ok {
		t.Fatalf("got %T, want *ErrFontNotVendored", err)
	}
}

// TestRowPaddedRoundTrip confirms RowPaddedBytes ↔ LoadRowPadded round-trips —
// the SAM software-font binary the §5 P1 converter emits and the Z80 routine
// loads.
func TestRowPaddedRoundTrip(t *testing.T) {
	f, err := Font8x8()
	if err != nil {
		t.Fatal(err)
	}
	bin := RowPaddedBytes(f, 128)
	if len(bin) != 128*8 {
		t.Fatalf("serialised %d bytes, want %d", len(bin), 128*8)
	}
	f2, err := LoadRowPadded(8, 8, bin)
	if err != nil {
		t.Fatal(err)
	}
	for ch := 0; ch < 128; ch++ {
		for r := 0; r < 8; r++ {
			if f.Row(byte(ch), r) != f2.Row(byte(ch), r) {
				t.Fatalf("round-trip mismatch glyph %d row %d", ch, r)
			}
		}
	}
}
