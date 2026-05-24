package format

import "testing"

func TestExprOpcodeValues(t *testing.T) {
	cases := map[ExprOp]byte{
		OpPushImm8: 0x01, OpPushImm16: 0x02, OpPushImm32: 0x03, OpPushImm64: 0x04,
		OpPushSym: 0x05, OpPushLocal: 0x06, OpPushPC: 0x07,
		OpAdd: 0x10, OpSub: 0x11, OpMul: 0x12, OpDiv: 0x13,
		OpAnd: 0x14, OpOr: 0x15, OpXor: 0x16, OpShl: 0x17, OpShr: 0x18,
		OpNeg: 0x20, OpNot: 0x21,
		OpRelLo12: 0x30, OpRelHi12: 0x31,
		OpRelAbsG0: 0x32, OpRelAbsG0NC: 0x33,
		OpRelAbsG1: 0x34, OpRelAbsG1NC: 0x35,
		OpRelAbsG2: 0x36, OpRelAbsG2NC: 0x37,
		OpRelAbsG3: 0x38,
	}
	for op, want := range cases {
		if byte(op) != want {
			t.Errorf("%s = 0x%02x, want 0x%02x", op.Name(), byte(op), want)
		}
	}
}

func TestExprOpcodeName(t *testing.T) {
	if OpAdd.Name() != "ADD" {
		t.Errorf("OpAdd.Name() = %q, want %q", OpAdd.Name(), "ADD")
	}
	if ExprOp(0xFF).Name() != "UNKNOWN" {
		t.Errorf("unknown opcode name should be UNKNOWN")
	}
}
