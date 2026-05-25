package aarch64enc

import (
	"fmt"
)

// Encode produces the 32-bit instruction word for a (form, values)
// pair. Walks form.Slots and dispatches to per-slot-kind encoders.
func Encode(form Form, values []OperandValue) (uint32, error) {
	if len(values) < len(form.Slots) {
		return 0, fmt.Errorf("encode: not enough operand values (need %d, got %d)",
			len(form.Slots), len(values))
	}
	// is64 is derived from the sf bit (bit 31) of the pattern. This lets
	// size-sensitive encoders (e.g. LogicalImm) pick the correct N-bit value
	// without needing a per-slot is64 parameter.
	is64 := (form.Pattern>>31)&1 == 1
	out := form.Pattern
	for i, slot := range form.Slots {
		v := values[i]
		bits, err := encodeSlot(slot, v, is64)
		if err != nil {
			return 0, fmt.Errorf("slot %d (%s): %v", i, slot.SlotKind.Name(), err)
		}
		out |= bits
	}
	return out, nil
}

func encodeSlot(slot OperandSlot, v OperandValue, is64 bool) (uint32, error) {
	switch slot.SlotKind {
	case Xreg, Wreg, XregOrSp, WregOrSp:
		return encodeReg(slot, v.Reg)
	case Imm5, Imm6:
		return encodeImmN(slot, v.Imm)
	case CondCode:
		return encodeCond(slot, byte(v.Cond))
	case Imm12Shifted:
		return encodeImm12Shifted(slot, v.Imm)
	case Imm16Shifted:
		// hw (shift selector) is an internal field. For MOV (= MOVZ), hw is
		// fixed to 0 in the pattern. For MOVK, the parser encodes the hw value
		// into bits [17:16] of the immediate constant (see parseMovk in text2bin).
		// Extract hw from bits [17:16] and imm16 from bits [15:0].
		hw := byte((v.Imm >> 16) & 0x3)
		imm16 := v.Imm & 0xffff
		return encodeImm16Shifted(slot, imm16, hw)
	case ShiftAmount:
		return encodeShiftAmount(slot, v.Imm)
	case ExtendOp:
		return encodeExtendOp(slot, v.ExtendKind, byte(v.Imm))
	case BranchImm26, BranchImm19, BranchImm14:
		return encodeBranchImm(slot, v.Imm)
	case AdrpImm:
		return encodeAdrpImm(slot, v.Imm)
	case AdrImm:
		return encodeAdrImm(slot, v.Imm)
	case LogicalImm:
		// is64 is passed from Encode, derived from sf bit (bit 31) of the pattern.
		return encodeLogicalImm(slot, v.Imm, is64)
	case BitfieldImm:
		return 0, fmt.Errorf("BitfieldImm dispatched via two-slot pairs, not single-slot")
	}
	return 0, fmt.Errorf("encode: unsupported slot kind %v", slot.SlotKind)
}
