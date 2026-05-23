package format

// OperandKind tags each operand with its on-disk shape (§4).
type OperandKind byte

const (
	OpRegX        OperandKind = 0x01
	OpRegW        OperandKind = 0x02
	OpRegXSP      OperandKind = 0x03
	OpRegWSP      OperandKind = 0x04
	OpImmExpr     OperandKind = 0x05
	OpShiftedReg  OperandKind = 0x06
	OpExtendedReg OperandKind = 0x07
	OpMem         OperandKind = 0x08
	OpString      OperandKind = 0x09
	OpCond        OperandKind = 0x0A
	OpSysName     OperandKind = 0x0B
)

func (k OperandKind) Name() string {
	switch k {
	case OpRegX:
		return "REG_X"
	case OpRegW:
		return "REG_W"
	case OpRegXSP:
		return "REG_X_SP"
	case OpRegWSP:
		return "REG_W_SP"
	case OpImmExpr:
		return "IMM_EXPR"
	case OpShiftedReg:
		return "SHIFTED_REG"
	case OpExtendedReg:
		return "EXTENDED_REG"
	case OpMem:
		return "MEM"
	case OpString:
		return "STRING"
	case OpCond:
		return "COND"
	case OpSysName:
		return "SYS_NAME"
	}
	return "UNKNOWN"
}

// MemShape sub-codes for the MEM operand (§4).
type MemShape byte

const (
	MemBase            MemShape = 0
	MemBaseOff         MemShape = 1
	MemBaseOffPre      MemShape = 2
	MemBaseOffPost     MemShape = 3
	MemBaseIdx         MemShape = 4
	MemBaseIdxShifted  MemShape = 5
	MemBaseIdxExtended MemShape = 6
)

// ShiftKind for SHIFTED_REG operands.
type ShiftKind byte

const (
	ShiftLSL ShiftKind = 0
	ShiftLSR ShiftKind = 1
	ShiftASR ShiftKind = 2
	ShiftROR ShiftKind = 3
)

func (s ShiftKind) Name() string {
	return [...]string{"lsl", "lsr", "asr", "ror"}[s]
}

// ExtendKind for EXTENDED_REG operands.
type ExtendKind byte

const (
	ExtUXTB ExtendKind = 0
	ExtUXTH ExtendKind = 1
	ExtUXTW ExtendKind = 2
	ExtUXTX ExtendKind = 3
	ExtSXTB ExtendKind = 4
	ExtSXTH ExtendKind = 5
	ExtSXTW ExtendKind = 6
	ExtSXTX ExtendKind = 7
)

func (e ExtendKind) Name() string {
	return [...]string{"uxtb", "uxth", "uxtw", "uxtx", "sxtb", "sxth", "sxtw", "sxtx"}[e]
}

// CondCode for COND operands. Values match the aarch64 encoding.
type CondCode byte

const (
	CondEQ CondCode = 0
	CondNE CondCode = 1
	CondCS CondCode = 2
	CondCC CondCode = 3
	CondMI CondCode = 4
	CondPL CondCode = 5
	CondVS CondCode = 6
	CondVC CondCode = 7
	CondHI CondCode = 8
	CondLS CondCode = 9
	CondGE CondCode = 10
	CondLT CondCode = 11
	CondGT CondCode = 12
	CondLE CondCode = 13
	CondAL CondCode = 14
	CondNV CondCode = 15
)

func (c CondCode) Name() string {
	return [...]string{
		"eq", "ne", "cs", "cc", "mi", "pl", "vs", "vc",
		"hi", "ls", "ge", "lt", "gt", "le", "al", "nv",
	}[c]
}
