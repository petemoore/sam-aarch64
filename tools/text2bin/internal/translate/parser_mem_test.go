package translate

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func firstInst(t *testing.T, src string) format.Record {
	t.Helper()
	f := parseHelper(t, src)
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindInst {
		t.Fatalf("not INST: %+v", rec)
	}
	return rec
}

func TestParseShiftedReg(t *testing.T) {
	rec := firstInst(t, "add x0, x1, x2, lsl #4\n")
	or := format.NewOperandReader(rec.Operands)
	or.Next()
	or.Next()
	o, _ := or.Next()
	if o.Kind != format.OpShiftedReg || o.Width != 1 || o.Reg != 2 || o.ShiftKind != format.ShiftLSL {
		t.Errorf("op2 = %+v", o)
	}
	v, ok := format.EvalConst(o.AmtExpr)
	if !ok || v != 4 {
		t.Errorf("shift amt = (%d,%v)", v, ok)
	}
}

func TestParseExtendedReg(t *testing.T) {
	rec := firstInst(t, "add x0, x1, w2, uxtw #2\n")
	or := format.NewOperandReader(rec.Operands)
	or.Next()
	or.Next()
	o, _ := or.Next()
	if o.Kind != format.OpExtendedReg || o.Width != 0 || o.Reg != 2 || o.Extend != format.ExtUXTW {
		t.Errorf("op2 = %+v", o)
	}
}

func TestParseMemShapes(t *testing.T) {
	cases := []struct {
		src   string
		shape format.MemShape
	}{
		{"ldr x0, [x1]\n", format.MemBase},
		{"ldr x0, [x1, #8]\n", format.MemBaseOff},
		{"ldr x0, [x1, #8]!\n", format.MemBaseOffPre},
		{"ldr x0, [x1], #8\n", format.MemBaseOffPost},
		{"ldr x0, [x1, x2]\n", format.MemBaseIdx},
		{"ldr x0, [x1, x2, lsl #3]\n", format.MemBaseIdxShifted},
		{"ldr x0, [x1, w2, uxtw #2]\n", format.MemBaseIdxExtended},
	}
	for _, c := range cases {
		rec := firstInst(t, c.src)
		or := format.NewOperandReader(rec.Operands)
		or.Next()
		o, _ := or.Next()
		if o.Kind != format.OpMem || o.MemShape != c.shape {
			t.Errorf("%q: got %+v, want shape %v", c.src, o, c.shape)
		}
	}
}
