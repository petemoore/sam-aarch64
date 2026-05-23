package format

// ExprOp is one byte of expression bytecode (§5).
type ExprOp byte

const (
	OpPushImm8  ExprOp = 0x01
	OpPushImm16 ExprOp = 0x02
	OpPushImm32 ExprOp = 0x03
	OpPushImm64 ExprOp = 0x04
	OpPushSym   ExprOp = 0x05
	OpPushLocal ExprOp = 0x06
	OpPushPC    ExprOp = 0x07

	OpAdd ExprOp = 0x10
	OpSub ExprOp = 0x11
	OpMul ExprOp = 0x12
	OpDiv ExprOp = 0x13
	OpAnd ExprOp = 0x14
	OpOr  ExprOp = 0x15
	OpXor ExprOp = 0x16
	OpShl ExprOp = 0x17
	OpShr ExprOp = 0x18

	OpNeg ExprOp = 0x20
	OpNot ExprOp = 0x21

	OpRelLo12    ExprOp = 0x30
	OpRelHi12    ExprOp = 0x31
	OpRelAbsG0   ExprOp = 0x32
	OpRelAbsG0NC ExprOp = 0x33
	OpRelAbsG1   ExprOp = 0x34
	OpRelAbsG1NC ExprOp = 0x35
	OpRelAbsG2   ExprOp = 0x36
	OpRelAbsG2NC ExprOp = 0x37
	OpRelAbsG3   ExprOp = 0x38
)

func (o ExprOp) Name() string {
	switch o {
	case OpPushImm8:
		return "PUSH_IMM8"
	case OpPushImm16:
		return "PUSH_IMM16"
	case OpPushImm32:
		return "PUSH_IMM32"
	case OpPushImm64:
		return "PUSH_IMM64"
	case OpPushSym:
		return "PUSH_SYM"
	case OpPushLocal:
		return "PUSH_LOCAL"
	case OpPushPC:
		return "PUSH_PC"
	case OpAdd:
		return "ADD"
	case OpSub:
		return "SUB"
	case OpMul:
		return "MUL"
	case OpDiv:
		return "DIV"
	case OpAnd:
		return "AND"
	case OpOr:
		return "OR"
	case OpXor:
		return "XOR"
	case OpShl:
		return "SHL"
	case OpShr:
		return "SHR"
	case OpNeg:
		return "NEG"
	case OpNot:
		return "NOT"
	case OpRelLo12:
		return "REL_LO12"
	case OpRelHi12:
		return "REL_HI12"
	case OpRelAbsG0:
		return "REL_ABS_G0"
	case OpRelAbsG0NC:
		return "REL_ABS_G0_NC"
	case OpRelAbsG1:
		return "REL_ABS_G1"
	case OpRelAbsG1NC:
		return "REL_ABS_G1_NC"
	case OpRelAbsG2:
		return "REL_ABS_G2"
	case OpRelAbsG2NC:
		return "REL_ABS_G2_NC"
	case OpRelAbsG3:
		return "REL_ABS_G3"
	}
	return "UNKNOWN"
}
