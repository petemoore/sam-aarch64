package aarch64enc

import "fmt"

// encodeReg packs a 5-bit register index into a register slot.
// All four register kinds (Xreg, Wreg, XregOrSp, WregOrSp) share
// this body; the SP-vs-ZR distinction is text2bin's job via the
// slot's ExpectedKind. At encode time, value 31 is the same 5 bits.
func encodeReg(slot OperandSlot, reg byte) (uint32, error) {
	if reg > 31 {
		return 0, fmt.Errorf("register index %d > 31", reg)
	}
	return uint32(reg) << slot.BitPosition, nil
}

// encodeImmN packs an unsigned N-bit integer into a slot, where N
// is slot.BitWidth. Used by Imm5, Imm6.
func encodeImmN(slot OperandSlot, v int64) (uint32, error) {
	limit := int64(1) << slot.BitWidth
	if v < 0 || v >= limit {
		return 0, fmt.Errorf("immediate %d does not fit in %d bits", v, slot.BitWidth)
	}
	return uint32(v) << slot.BitPosition, nil
}

// encodeCond packs a 4-bit condition code.
func encodeCond(slot OperandSlot, cc byte) (uint32, error) {
	if cc > 15 {
		return 0, fmt.Errorf("cond code %d > 15", cc)
	}
	return uint32(cc) << slot.BitPosition, nil
}
