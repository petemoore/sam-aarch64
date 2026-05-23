package aarch64enc

import "fmt"

// encodeBranchImm encodes a PC-relative branch offset. The input is
// a byte offset; the encoder divides by 4, sign-extends to the
// slot's bit width, range-checks, and alignment-checks.
func encodeBranchImm(slot OperandSlot, byteOffset int64) (uint32, error) {
	if byteOffset%4 != 0 {
		return 0, fmt.Errorf("%s: offset %d not 4-byte aligned",
			slot.SlotKind.Name(), byteOffset)
	}
	instrOffset := byteOffset / 4
	half := int64(1) << (slot.BitWidth - 1)
	if instrOffset >= half || instrOffset < -half {
		return 0, fmt.Errorf("%s: instruction offset %d out of range ±%d",
			slot.SlotKind.Name(), instrOffset, half)
	}
	mask := (uint32(1) << slot.BitWidth) - 1
	bits := uint32(instrOffset) & mask
	return bits << slot.BitPosition, nil
}
