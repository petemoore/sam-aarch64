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

func TestParseInstBarrierZeroOperand(t *testing.T) {
	// eret / wfi are 0-operand mnemonics.
	f := parseHelper(t, "eret\nwfi\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	eretID, _ := format.MnemonicID("eret")
	if rec.MnemonicID != eretID || rec.OperandCount != 0 {
		t.Errorf("eret: %+v", rec)
	}
	rec, _ = r.Next()
	wfiID, _ := format.MnemonicID("wfi")
	if rec.MnemonicID != wfiID || rec.OperandCount != 0 {
		t.Errorf("wfi: %+v", rec)
	}
}

func TestParseInstISBDefaultArg(t *testing.T) {
	// isb with no argument defaults to sy (CRm=0xf).
	f := parseHelper(t, "isb\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	isbID, _ := format.MnemonicID("isb")
	if rec.MnemonicID != isbID || rec.OperandCount != 1 {
		t.Fatalf("isb: %+v", rec)
	}
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpImmExpr {
		t.Fatalf("isb op0 kind = %v", o.Kind)
	}
	v, ok := format.EvalConst(o.Expr)
	if !ok || v != 0xf {
		t.Errorf("isb default crm = (%d, %v), want (15, true)", v, ok)
	}
}

func TestParseInstDSBWithBarrierArgs(t *testing.T) {
	cases := []struct {
		src string
		crm int64
	}{
		{"dsb sy\n", 0xf},
		{"dsb st\n", 0xe},
		{"dsb ld\n", 0xd},
		{"dsb ish\n", 0xb},
		{"dsb ishst\n", 0xa},
		{"dsb ishld\n", 0x9},
		{"dsb nsh\n", 0x7},
		{"dsb nshst\n", 0x6},
		{"dsb nshld\n", 0x5},
		{"dsb osh\n", 0x3},
		{"dsb oshst\n", 0x2},
		{"dsb oshld\n", 0x1},
	}
	dsbID, _ := format.MnemonicID("dsb")
	for _, c := range cases {
		f := parseHelper(t, c.src)
		r := format.NewRecordReader(f.Records)
		rec, _ := r.Next()
		if rec.MnemonicID != dsbID || rec.OperandCount != 1 {
			t.Errorf("%s: rec = %+v", c.src, rec)
			continue
		}
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		v, ok := format.EvalConst(o.Expr)
		if !ok || v != c.crm {
			t.Errorf("%s: crm = (%d, %v), want %d", c.src, v, ok, c.crm)
		}
	}
}

func TestParseInstDMBMandatoryArg(t *testing.T) {
	dmbID, _ := format.MnemonicID("dmb")
	f := parseHelper(t, "dmb ish\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.MnemonicID != dmbID || rec.OperandCount != 1 {
		t.Fatalf("dmb: %+v", rec)
	}
	// Missing arg → parse error.
	_, err := Translate([]byte("dmb\n"), "t.s")
	if err == nil {
		t.Error("expected error for dmb with no arg")
	}
	// Unknown arg → parse error.
	_, err = Translate([]byte("dmb bogus\n"), "t.s")
	if err == nil {
		t.Error("expected error for dmb with unknown arg")
	}
}

func TestParseInstRorImm(t *testing.T) {
	f := parseHelper(t, "ror x0, x1, #5\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	rorID, _ := format.MnemonicID("ror")
	if rec.MnemonicID != rorID || rec.OperandCount != 3 {
		t.Fatalf("ror: %+v", rec)
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
		t.Fatalf("op2 kind = %v", o.Kind)
	}
	v, ok := format.EvalConst(o.Expr)
	if !ok || v != 5 {
		t.Errorf("op2 expr = (%d, %v)", v, ok)
	}
}

func TestParseInstRorReg(t *testing.T) {
	f := parseHelper(t, "ror w0, w1, w2\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.OperandCount != 3 {
		t.Fatalf("ror reg: %+v", rec)
	}
	or := format.NewOperandReader(rec.Operands)
	for i := 0; i < 3; i++ {
		o, _ := or.Next()
		if o.Kind != format.OpRegW || o.Reg != byte(i) {
			t.Errorf("op%d = %+v", i, o)
		}
	}
}

func TestParseInstMulUdivClsSxtw(t *testing.T) {
	f := parseHelper(t, "mul x0, x1, x2\nudiv x0, x1, x2\ncls x0, x1\nsxtw x0, w1\n")
	r := format.NewRecordReader(f.Records)
	mulID, _ := format.MnemonicID("mul")
	udivID, _ := format.MnemonicID("udiv")
	clsID, _ := format.MnemonicID("cls")
	sxtwID, _ := format.MnemonicID("sxtw")
	rec, _ := r.Next()
	if rec.MnemonicID != mulID || rec.OperandCount != 3 {
		t.Errorf("mul: %+v", rec)
	}
	rec, _ = r.Next()
	if rec.MnemonicID != udivID || rec.OperandCount != 3 {
		t.Errorf("udiv: %+v", rec)
	}
	rec, _ = r.Next()
	if rec.MnemonicID != clsID || rec.OperandCount != 2 {
		t.Errorf("cls: %+v", rec)
	}
	rec, _ = r.Next()
	if rec.MnemonicID != sxtwID || rec.OperandCount != 2 {
		t.Errorf("sxtw: %+v", rec)
	}
}

func TestParseInstSturLdur(t *testing.T) {
	cases := []struct {
		src     string
		mnem    string
		regKind format.OperandKind
		regNum  byte
		base    byte
		hasOff  bool
		off     int64
	}{
		{"stur w0, [x1]\n", "stur", format.OpRegW, 0, 1, false, 0},
		{"stur x0, [x1, #0]\n", "stur", format.OpRegX, 0, 1, true, 0},
		{"stur w0, [x1, #4]\n", "stur", format.OpRegW, 0, 1, true, 4},
		{"stur x0, [x1, #-8]\n", "stur", format.OpRegX, 0, 1, true, -8},
		{"ldur w0, [x1]\n", "ldur", format.OpRegW, 0, 1, false, 0},
		{"ldur x0, [x1, #8]\n", "ldur", format.OpRegX, 0, 1, true, 8},
	}
	for _, c := range cases {
		f := parseHelper(t, c.src)
		r := format.NewRecordReader(f.Records)
		rec, _ := r.Next()
		mid, _ := format.MnemonicID(c.mnem)
		if rec.MnemonicID != mid {
			t.Errorf("%s: mnemonic_id=%d want %d", c.src, rec.MnemonicID, mid)
		}
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		if o.Kind != c.regKind || o.Reg != c.regNum {
			t.Errorf("%s: rt = %+v", c.src, o)
		}
		o, _ = or.Next()
		if o.Kind != format.OpMem {
			t.Errorf("%s: mem kind = %v", c.src, o.Kind)
		}
		if o.Base != c.base {
			t.Errorf("%s: base = %d, want %d", c.src, o.Base, c.base)
		}
		if c.hasOff {
			if o.MemShape != format.MemBaseOff {
				t.Errorf("%s: memshape = %v, want MemBaseOff", c.src, o.MemShape)
			}
			v, _ := format.EvalConst(o.Expr)
			if v != c.off {
				t.Errorf("%s: off = %d, want %d", c.src, v, c.off)
			}
		} else if o.MemShape != format.MemBase {
			t.Errorf("%s: memshape = %v, want MemBase", c.src, o.MemShape)
		}
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
