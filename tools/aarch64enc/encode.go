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
	out := form.Pattern
	for i, slot := range form.Slots {
		v := values[i]
		bits, err := encodeSlot(slot, v)
		if err != nil {
			return 0, fmt.Errorf("slot %d (%s): %v", i, slot.SlotKind.Name(), err)
		}
		out |= bits
	}
	return out, nil
}

func encodeSlot(slot OperandSlot, v OperandValue) (uint32, error) {
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
		return 0, fmt.Errorf("Imm16Shifted: dispatcher path not yet wired")
	case ShiftAmount:
		return encodeShiftAmount(slot, v.Imm)
	case ExtendOp:
		return encodeExtendOp(slot, v.ExtendKind, byte(v.Imm))
	case BranchImm26, BranchImm19, BranchImm14:
		return encodeBranchImm(slot, v.Imm)
	case AdrpImm:
		return encodeAdrpImm(slot, v.Imm)
	case LogicalImm:
		return encodeLogicalImm(slot, v.Imm, true)
	case BitfieldImm:
		return 0, fmt.Errorf("BitfieldImm dispatched via two-slot pairs, not single-slot")
	}
	return 0, fmt.Errorf("encode: unsupported slot kind %v", slot.SlotKind)
}
