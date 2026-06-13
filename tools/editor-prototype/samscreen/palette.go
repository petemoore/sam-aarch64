package samscreen

import "image/color"

// SAM 7-bit colour → sRGB. Each CLUT register holds a 7-bit colour value
// (TM p19): bit 6 GRN1, bit 5 RED1, bit 4 BLU1, bit 3 BRIGHT, bit 2 GRN0,
// bit 1 RED0, bit 0 BLU0. Each channel is a 2-bit value (the high bit from
// {GRN,RED,BLU}1, the low bit from {GRN,RED,BLU}0) shared-LSB-extended by the
// single BRIGHT bit, giving a 3-bit intensity per channel.
//
// The 3-bit intensity → 8-bit sRGB map is SimCoupé's reference table
// (src/Video.cpp / SAM_PALETTE): the eight levels SimCoupé uses to render SAM
// colours on a modern display. Using SimCoupé's mapping is the spec's stated
// reference (§2.4), so the PNGs match what a SAM developer sees in the
// emulator.
var simcoupeLevels = [8]uint8{0x00, 0x24, 0x49, 0x6d, 0x92, 0xb6, 0xdb, 0xff}

// SAMColour converts a 7-bit CLUT register value to an sRGB colour using
// SimCoupé's level mapping (TM p19 bit layout, SimCoupé intensities).
func SAMColour(reg uint8) color.RGBA {
	bright := (reg >> 3) & 1
	level := func(hi, lo uint8) uint8 {
		// 3-bit intensity: high bit, low bit, then BRIGHT as the shared LSB.
		v := (hi << 2) | (lo << 1) | bright
		return simcoupeLevels[v]
	}
	r := level((reg>>5)&1, (reg>>1)&1)
	g := level((reg>>6)&1, (reg>>2)&1)
	b := level((reg>>4)&1, (reg>>0)&1)
	return color.RGBA{R: r, G: g, B: b, A: 0xff}
}

// DefaultCLUT is the 16-register power-on CLUT loaded by ROM (TM p18): the
// 7-bit colour value for each of the 16 colours 0–15. The mockup backend uses
// these register values both to colour pixels (via SAMColour) and to write the
// PALTAB into a SCREEN$ file.
var DefaultCLUT = [16]uint8{
	0:  0,   // black
	1:  16,  // blue
	2:  32,  // red
	3:  48,  // magenta
	4:  64,  // green
	5:  80,  // cyan
	6:  96,  // yellow
	7:  120, // white
	8:  0,   // black (bright variant of 0)
	9:  17,  // bright blue
	10: 34,  // bright red
	11: 51,  // bright magenta
	12: 68,  // bright green
	13: 85,  // bright cyan
	14: 102, // bright yellow
	15: 127, // bright white
}

// Palette is the active CLUT: per-colour register values. A MODE 3 screen sees
// only four of these at once (one CLUT quadruple, TM p20); the prototype's
// MODE 3 geometries use colours 0–3, which map to the first four DefaultCLUT
// registers.
type Palette [16]uint8

// DefaultPalette returns the power-on CLUT.
func DefaultPalette() Palette { return Palette(DefaultCLUT) }

// RGBA returns the sRGB colour for CLUT index i.
func (p Palette) RGBA(i uint8) color.RGBA { return SAMColour(p[i]) }
