package main

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestParseInstAddRegImm(t *testing.T) {
	f := parseHelper(t, "add x0, x1, #4\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindInst {
		t.Fatalf("rec.Kind = %v", rec.Kind)
	}
	id, _ := format.MnemonicID("add")
	if rec.MnemonicID != id {
		t.Errorf("mnemonic_id = %d, want %d", rec.MnemonicID, id)
	}
	if rec.OperandCount != 3 {
		t.Errorf("operand_count = %d, want 3", rec.OperandCount)
	}
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpRegX || o.Reg != 0 {
		t.Errorf("op0 = %+v", o)
	}
	o, _ = or.Next()
	if o.Kind != format.OpRegX || o.Reg != 1 {
		t.Errorf("op1 = %+v", o)
	}
	o, _ = or.Next()
	if o.Kind != format.OpImmExpr {
		t.Errorf("op2 = %+v", o)
	}
	v, ok := format.EvalConst(o.Expr)
	if !ok || v != 4 {
		t.Errorf("op2 expr = (%d, %v), want (4, true)", v, ok)
	}
}

func TestParseInstZeroOperand(t *testing.T) {
	f := parseHelper(t, "nop\nret\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.MnemonicID != 0 || rec.OperandCount != 0 {
		t.Errorf("nop: %+v", rec)
	}
	rec, _ = r.Next()
	retID, _ := format.MnemonicID("ret")
	if rec.MnemonicID != retID || rec.OperandCount != 0 {
		t.Errorf("ret: %+v", rec)
	}
}

func TestParseInstSPAndZR(t *testing.T) {
	f := parseHelper(t, "mov sp, x0\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpRegXSP || o.Reg != 31 {
		t.Errorf("op0 = %+v", o)
	}
	o, _ = or.Next()
	if o.Kind != format.OpRegX || o.Reg != 0 {
		t.Errorf("op1 = %+v", o)
	}
}

func TestParseInstSymbolRef(t *testing.T) {
	f := parseHelper(t, "b target\n")
	if len(f.Names) != 1 || f.Names[0] != "target" {
		t.Errorf("names = %v", f.Names)
	}
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpImmExpr {
		t.Errorf("op0 = %+v", o)
	}
	er := format.NewExprReader(o.Expr)
	op, _, _ := er.Next()
	if op != format.OpPushSym {
		t.Errorf("expr op = %v, want PUSH_SYM", op)
	}
}
