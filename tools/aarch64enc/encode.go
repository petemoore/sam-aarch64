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
		// hw (shift selector) is an internal field. For MOV (= MOVZ),
		// hw is fixed to 0 in the pattern but a single-arg `mov Rd, #imm`
		// should still work for any constant that fits in one of the
		// movz slots (lsl #0 / #16 / #32 / #48). MOVK / MOVZ / MOVN
		// callers encode hw into bits [17:16] of the immediate so the
		// extraction below selects the requested slot.
		//
		// If bits [17:16] are zero (the default for an un-annotated
		// `mov`) and the value's lower 16 bits don't cover the full
		// constant, try each higher-shift slot to find a single-movz
		// fit. This mirrors GNU as's behaviour of auto-selecting the
		// shift for one-instruction `mov` aliases.
		hw := byte((v.Imm >> 16) & 0x3)
		imm16 := v.Imm & 0xffff
		if hw == 0 && (v.Imm < 0 || v.Imm > 0xffff) {
			u := uint64(v.Imm)
			for shift := uint(0); shift < 64; shift += 16 {
				chunk := (u >> shift) & 0xffff
				if chunk<<shift == u {
					hw = byte(shift / 16)
					imm16 = int64(chunk)
					break
				}
			}
		}
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
