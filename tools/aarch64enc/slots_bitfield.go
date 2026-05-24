package aarch64enc

import "fmt"

// encodeBitfieldBFI translates (lsb, width) to (immr, imms) per
// the BFI alias rule: immr = (-lsb) mod regsize, imms = width - 1.
func encodeBitfieldBFI(immrSlot, immsSlot OperandSlot, regsize int, lsb, width int64) (uint32, error) {
	if regsize != 32 && regsize != 64 {
		return 0, fmt.Errorf("BitfieldBFI: regsize must be 32 or 64, got %d", regsize)
	}
	if lsb < 0 || lsb >= int64(regsize) {
		return 0, fmt.Errorf("BitfieldBFI: lsb %d out of range", lsb)
	}
	if width < 1 || width > int64(regsize)-lsb {
		return 0, fmt.Errorf("BitfieldBFI: width %d out of range", width)
	}
	immr := uint32((-lsb)&int64(regsize-1)) & 0x3F
	imms := uint32(width-1) & 0x3F
	return immr<<immrSlot.BitPosition | imms<<immsSlot.BitPosition, nil
}

// encodeBitfieldUBFX: immr = lsb, imms = lsb + width - 1.
func encodeBitfieldUBFX(immrSlot, immsSlot OperandSlot, lsb, width int64) (uint32, error) {
	if lsb < 0 || lsb >= 64 {
		return 0, fmt.Errorf("BitfieldUBFX: lsb %d out of range", lsb)
	}
	if width < 1 || lsb+width > 64 {
		return 0, fmt.Errorf("BitfieldUBFX: width %d out of range", width)
	}
	immr := uint32(lsb) & 0x3F
	imms := uint32(lsb+width-1) & 0x3F
	return immr<<immrSlot.BitPosition | imms<<immsSlot.BitPosition, nil
}
