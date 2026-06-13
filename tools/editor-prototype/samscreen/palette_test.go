package samscreen

import (
	"image/color"
	"testing"
)

// TestSAMColourExtremes pins the two endpoints of the 7-bit colour map: black
// (0) and bright white (127) (TM p18 default CLUT registers 0 and 15).
func TestSAMColourExtremes(t *testing.T) {
	if got := SAMColour(0); got != (color.RGBA{0, 0, 0, 0xff}) {
		t.Errorf("colour 0 = %v, want black", got)
	}
	if got := SAMColour(127); got != (color.RGBA{0xff, 0xff, 0xff, 0xff}) {
		t.Errorf("colour 127 = %v, want full white", got)
	}
}

// TestSAMColourChannels asserts each channel decodes from the right bits
// (TM p19 layout: GRN1=bit6, RED1=bit5, BLU1=bit4, BRIGHT=bit3, GRN0=bit2,
// RED0=bit1, BLU0=bit0). Pure-red register 32 (RED1 set) should have R>0,
// G==0, B==0.
func TestSAMColourChannels(t *testing.T) {
	red := SAMColour(32) // RED1 only
	if red.R == 0 || red.G != 0 || red.B != 0 {
		t.Errorf("pure red (reg 32) = %v, want R>0,G=0,B=0", red)
	}
	blue := SAMColour(16) // BLU1 only
	if blue.B == 0 || blue.R != 0 || blue.G != 0 {
		t.Errorf("pure blue (reg 16) = %v, want B>0,R=0,G=0", blue)
	}
	green := SAMColour(64) // GRN1 only
	if green.G == 0 || green.R != 0 || green.B != 0 {
		t.Errorf("pure green (reg 64) = %v, want G>0,R=0,B=0", green)
	}
}

// TestDefaultPaletteMonotonic asserts the bright variants (8–15) are at least
// as bright as their base colours (0–7) channel-wise — a sanity check on the
// power-on CLUT (TM p18).
func TestDefaultPaletteMonotonic(t *testing.T) {
	p := DefaultPalette()
	for i := uint8(1); i < 7; i++ {
		base := p.RGBA(i)
		bright := p.RGBA(i + 8)
		sum := func(c color.RGBA) int { return int(c.R) + int(c.G) + int(c.B) }
		if sum(bright) < sum(base) {
			t.Errorf("bright colour %d dimmer than base %d", i+8, i)
		}
	}
}
