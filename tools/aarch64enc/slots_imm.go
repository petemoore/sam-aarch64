package aarch64enc

import (
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// encodeImm12Shifted handles the (sh:1, imm12:12) pair used by
// add/sub immediate. The 'sh' bit is at BitPosition+12; imm12 at
// BitPosition. Either v fits in 12 bits directly (sh=0), or v is a
// multiple of 4096 whose top portion fits in 12 bits (sh=1).
func encodeImm12Shifted(slot OperandSlot, v int64) (uint32, error) {
	if v >= 0 && v < (1<<12) {
		return uint32(v) << slot.BitPosition, nil
	}
	if v >= (1<<12) && v < (1<<24) && (v&0xFFF) == 0 {
		shiftBit := uint32(1) << (slot.BitPosition + 12)
		return uint32(v>>12)<<slot.BitPosition | shiftBit, nil
	}
	return 0, fmt.Errorf("Imm12Shifted: %d cannot be encoded as imm12 or imm12 lsl #12", v)
}

// encodeImm16Shifted handles the (hw:2, imm16:16) pair used by movz/movk.
// hw selects the shift: 0 → no shift, 1 → lsl #16, 2 → lsl #32, 3 → lsl #48.
// hw lives at BitPosition+16.
func encodeImm16Shifted(slot OperandSlot, imm int64, hw byte) (uint32, error) {
	if imm < 0 || imm >= (1<<16) {
		return 0, fmt.Errorf("Imm16Shifted: imm %d does not fit in 16 bits", imm)
	}
	if hw > 3 {
		return 0, fmt.Errorf("Imm16Shifted: hw %d > 3", hw)
	}
	bits := uint32(imm)<<slot.BitPosition | uint32(hw)<<(slot.BitPosition+16)
	return bits, nil
}

// encodeShiftAmount handles the shift-amount field inside shifted-
// register operand forms. It is a plain unsigned N-bit immediate.
func encodeShiftAmount(slot OperandSlot, amt int64) (uint32, error) {
	return encodeImmN(slot, amt)
}

// encodeExtendOp packs the (option:3, imm3:3) pair used by
// extended-register forms. option encodes the extend kind. imm3 is
// the optional shift amount (0–4).
func encodeExtendOp(slot OperandSlot, ext format.ExtendKind, shift byte) (uint32, error) {
	if shift > 4 {
		return 0, fmt.Errorf("ExtendOp: shift %d > 4", shift)
	}
	option := uint32(ext)
	bits := option<<(slot.BitPosition+3) | uint32(shift)<<slot.BitPosition
	return bits, nil
}
