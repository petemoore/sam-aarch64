// Package aarch64enc holds the aarch64 encoder-table types and the
// reference encoder used by refenc.
//
// §N references point at https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m2-encoder-tables-design.md.
package aarch64enc

import (
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// SlotKind picks the operand encoder for one slot in a form. Values
// must match §3 of the spec — the on-disk .enc format depends on it.
type SlotKind byte

const (
	Xreg     SlotKind = 0x01
	Wreg     SlotKind = 0x02
	XregOrSp SlotKind = 0x03
	WregOrSp SlotKind = 0x04
	Imm5     SlotKind = 0x05
	Imm6     SlotKind = 0x06
	CondCode SlotKind = 0x07
	Imm16    SlotKind = 0x08

	Imm12Shifted SlotKind = 0x10
	Imm16Shifted SlotKind = 0x11
	ShiftAmount  SlotKind = 0x12
	ExtendOp     SlotKind = 0x13

	BranchImm26 SlotKind = 0x20
	BranchImm19 SlotKind = 0x21
	BranchImm14 SlotKind = 0x22
	AdrpImm     SlotKind = 0x23
	LogicalImm  SlotKind = 0x24
	BitfieldImm SlotKind = 0x25
	AdrImm      SlotKind = 0x26 // ADR: raw byte offset, same bit layout as AdrpImm
)

func (k SlotKind) Name() string {
	switch k {
	case Xreg:
		return "Xreg"
	case Wreg:
		return "Wreg"
	case XregOrSp:
		return "XregOrSp"
	case WregOrSp:
		return "WregOrSp"
	case Imm5:
		return "Imm5"
	case Imm6:
		return "Imm6"
	case CondCode:
		return "CondCode"
	case Imm16:
		return "Imm16"
	case Imm12Shifted:
		return "Imm12Shifted"
	case Imm16Shifted:
		return "Imm16Shifted"
	case ShiftAmount:
		return "ShiftAmount"
	case ExtendOp:
		return "ExtendOp"
	case BranchImm26:
		return "BranchImm26"
	case BranchImm19:
		return "BranchImm19"
	case BranchImm14:
		return "BranchImm14"
	case AdrpImm:
		return "AdrpImm"
	case LogicalImm:
		return "LogicalImm"
	case BitfieldImm:
		return "BitfieldImm"
	case AdrImm:
		return "AdrImm"
	}
	return "Unknown"
}

// OperandSlot describes one operand position within a form (§2).
type OperandSlot struct {
	SlotKind     SlotKind
	ExpectedKind format.OperandKind
	BitPosition  uint8
	BitWidth     uint8
}

// Form describes one (mnemonic, operand-kind-tuple) encoding (§2).
type Form struct {
	MnemonicID uint16
	Pattern    uint32
	Mask       uint32
	Slots      []OperandSlot
}

// OperandValue is the resolved value for one operand at encode time.
type OperandValue struct {
	Reg        byte
	Imm        int64
	ShiftKind  format.ShiftKind
	ExtendKind format.ExtendKind
	Cond       format.CondCode
}
