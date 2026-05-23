package translate

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

func TestParseInstLdrLitPoolXInt(t *testing.T) {
	f := parseHelper(t, "ldr x0, =0x30d0088a\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindInst {
		t.Fatalf("rec.Kind = %v", rec.Kind)
	}
	id, _ := format.MnemonicID("ldr")
	if rec.MnemonicID != id {
		t.Errorf("mnemonic_id = %d, want %d", rec.MnemonicID, id)
	}
	if rec.OperandCount != 2 {
		t.Errorf("operand_count = %d, want 2", rec.OperandCount)
	}
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpRegX || o.Reg != 0 {
		t.Errorf("op0 = %+v", o)
	}
	o, _ = or.Next()
	if o.Kind != format.OpLitPool {
		t.Fatalf("op1 kind = %v, want OpLitPool", o.Kind)
	}
	if o.Width != 8 {
		t.Errorf("op1 width = %d, want 8", o.Width)
	}
	v, ok := format.EvalConst(o.Expr)
	if !ok || v != 0x30d0088a {
		t.Errorf("op1 expr = (%#x, %v), want (0x30d0088a, true)", v, ok)
	}
}

func TestParseInstLdrLitPoolWInt(t *testing.T) {
	f := parseHelper(t, "ldr w2, =0xdeadbeef\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpRegW || o.Reg != 2 {
		t.Errorf("op0 = %+v", o)
	}
	o, _ = or.Next()
	if o.Kind != format.OpLitPool {
		t.Fatalf("op1 kind = %v, want OpLitPool", o.Kind)
	}
	if o.Width != 4 {
		t.Errorf("op1 width = %d, want 4", o.Width)
	}
	v, ok := format.EvalConst(o.Expr)
	// 0xdeadbeef as int64 (sign-extended from int32 in folder).
	if !ok || uint32(v) != 0xdeadbeef {
		t.Errorf("op1 expr = (%#x, %v), want 0xdeadbeef", uint32(v), ok)
	}
}

func TestParseInstLdrLitPoolSym(t *testing.T) {
	f := parseHelper(t, "ldr x1, =msg\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	_, _ = or.Next() // x1
	o, _ := or.Next()
	if o.Kind != format.OpLitPool {
		t.Fatalf("op1 kind = %v, want OpLitPool", o.Kind)
	}
	if o.Width != 8 {
		t.Errorf("op1 width = %d", o.Width)
	}
	er := format.NewExprReader(o.Expr)
	op, _, _ := er.Next()
	if op != format.OpPushSym {
		t.Errorf("expr op = %v, want PUSH_SYM", op)
	}
}

func TestParseInstLdrRegular(t *testing.T) {
	// Ensure the literal-pool intercept doesn't break ordinary
	// `ldr Xt, [Xn, #off]` syntax.
	f := parseHelper(t, "ldr x0, [x1, #8]\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	_, _ = or.Next() // x0
	o, _ := or.Next()
	if o.Kind != format.OpMem {
		t.Errorf("op1 kind = %v, want OpMem", o.Kind)
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
