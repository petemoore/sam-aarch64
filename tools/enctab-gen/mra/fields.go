package mra

import enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"

// FieldContext carries information about the surrounding form that
// affects how a field name maps to a slot kind.
type FieldContext struct {
	// Is64 is true for X-register forms, false for W. Determined by
	// the form's regdiagram bit 31 (the 'sf' field, conventionally).
	Is64 bool
	// AcceptsSP is true for forms where the encoding allows SP/WSP
	// for the named register (vs XZR/WZR). Determined per-form.
	AcceptsSP bool
}

// IsInternalField returns true for fields that appear as variable bits in the
// iclass regdiagram but are NOT separate user-supplied operands. They are
// either:
//   - Handled implicitly by another SlotKind's encoder (e.g. "sh" is encoded
//     inside Imm12Shifted at BitPosition+12), or
//   - ISA-level selectors that are fixed by context but left variable in the
//     shared iclass diagram (and should have been fixed by encoding-level
//     bitdiffs but occasionally are not).
//
// The generator skips forms that contain an internal field rather than
// reporting an error, because the field is an implementation detail, not
// a user-visible operand.
func IsInternalField(name string) bool {
	switch name {
	case "sh": // shift selector for imm12; encoded inside Imm12Shifted at +12
		return true
	case "hw": // shift selector for imm16 (movz/movk); encoded inside Imm16Shifted at +16
		return true
	}
	return false
}

// MapField returns the SlotKind for an MRA operand-box name in the
// given context. ok=false means the name is unrecognised; the
// generator hard-errors in that case.
func MapField(name string, ctx FieldContext) (enc.SlotKind, bool) {
	switch name {
	case "Rd", "Rt", "Rt2":
		if ctx.AcceptsSP {
			if ctx.Is64 {
				return enc.XregOrSp, true
			}
			return enc.WregOrSp, true
		}
		if ctx.Is64 {
			return enc.Xreg, true
		}
		return enc.Wreg, true
	case "Rn":
		if ctx.AcceptsSP {
			if ctx.Is64 {
				return enc.XregOrSp, true
			}
			return enc.WregOrSp, true
		}
		if ctx.Is64 {
			return enc.Xreg, true
		}
		return enc.Wreg, true
	case "Rm", "Ra", "Rs":
		if ctx.Is64 {
			return enc.Xreg, true
		}
		return enc.Wreg, true
	case "imm12":
		return enc.Imm12Shifted, true
	case "imm16":
		return enc.Imm16Shifted, true
	case "imm26":
		return enc.BranchImm26, true
	case "imm19":
		return enc.BranchImm19, true
	case "imm14":
		return enc.BranchImm14, true
	case "imm5":
		return enc.Imm5, true
	case "imm6":
		return enc.Imm6, true
	case "imms", "immr":
		return enc.BitfieldImm, true
	case "immhi", "immlo":
		return enc.AdrpImm, true
	case "cond":
		return enc.CondCode, true
	case "shift", "shamt":
		return enc.ShiftAmount, true
	case "option":
		return enc.ExtendOp, true
	case "hw":
		return enc.Imm16Shifted, true
	case "N":
		return enc.LogicalImm, true
	}
	return 0, false
}
