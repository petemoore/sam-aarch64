package aarch64enc

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestEvalWithSymbols(t *testing.T) {
	var w format.ExprWriter
	w.WriteSym(0)
	w.WriteImm(4)
	w.WriteOp(format.OpAdd)

	ctx := EvalContext{
		PC: 0x1000,
		Symbol: func(id uint16) (int64, bool) {
			if id == 0 {
				return 0x2000, true
			}
			return 0, false
		},
		LocalLabel: func(digit, dir byte) (int64, bool) {
			return 0, false
		},
	}
	v, err := Eval(w.Bytes(), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0x2004 {
		t.Errorf("eval = 0x%x, want 0x2004", v)
	}
}

func TestEvalUndefinedSymbol(t *testing.T) {
	var w format.ExprWriter
	w.WriteSym(99)
	ctx := EvalContext{Symbol: func(uint16) (int64, bool) { return 0, false }}
	if _, err := Eval(w.Bytes(), ctx); err == nil {
		t.Errorf("undefined symbol should error")
	}
}

func TestEvalPC(t *testing.T) {
	var w format.ExprWriter
	w.WritePC()
	w.WriteImm(4)
	w.WriteOp(format.OpSub)
	v, err := Eval(w.Bytes(), EvalContext{PC: 0x1000})
	if err != nil || v != 0xFFC {
		t.Errorf("PC - 4 = 0x%x", v)
	}
}

func TestEvalRelLo12(t *testing.T) {
	var w format.ExprWriter
	w.WriteImm(0x12345)
	w.WriteOp(format.OpRelLo12)
	v, err := Eval(w.Bytes(), EvalContext{})
	if err != nil || v != 0x345 {
		t.Errorf("REL_LO12(0x12345) = 0x%x, want 0x345", v)
	}
}
