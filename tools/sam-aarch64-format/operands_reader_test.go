package format

import (
	"bytes"
	"testing"
)

func TestOperandReadRoundtrip(t *testing.T) {
	var ew ExprWriter
	ew.WriteImm(0x42)
	var ow OperandWriter
	ow.WriteReg(OpRegX, 7)
	ow.WriteImmExpr(ew.Bytes())
	ow.WriteCond(CondGT)
	ow.WriteString([]byte("hello"))

	r := NewOperandReader(ow.Bytes())

	o, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != OpRegX || o.Reg != 7 {
		t.Errorf("op0: %+v", o)
	}

	o, err = r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != OpImmExpr || !bytes.Equal(o.Expr, ew.Bytes()) {
		t.Errorf("op1: %+v", o)
	}

	o, err = r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != OpCond || o.Cond != CondGT {
		t.Errorf("op2: %+v", o)
	}

	o, err = r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != OpString || string(o.Str) != "hello" {
		t.Errorf("op3: %+v", o)
	}

	if !r.AtEnd() {
		t.Errorf("reader not at end")
	}
}

func TestOperandReadMemShapes(t *testing.T) {
	var ow OperandWriter
	ow.WriteMemBase(1)
	ow.WriteMemBaseIdx(1, 2, 1)
	ow.WriteMemBaseIdxShifted(1, 2, 1, 3)
	ow.WriteMemBaseIdxExtended(1, 2, 0, ExtUXTW, 0)

	r := NewOperandReader(ow.Bytes())

	o, _ := r.Next()
	if o.Kind != OpMem || o.MemShape != MemBase || o.Base != 1 {
		t.Errorf("MemBase decoded wrong: %+v", o)
	}
	o, _ = r.Next()
	if o.Kind != OpMem || o.MemShape != MemBaseIdx || o.Base != 1 || o.Idx != 2 || o.IdxWidth != 1 {
		t.Errorf("MemBaseIdx decoded wrong: %+v", o)
	}
	o, _ = r.Next()
	if o.MemShape != MemBaseIdxShifted || o.ShiftAmt != 3 {
		t.Errorf("MemBaseIdxShifted decoded wrong: %+v", o)
	}
	o, _ = r.Next()
	if o.MemShape != MemBaseIdxExtended || o.Extend != ExtUXTW {
		t.Errorf("MemBaseIdxExtended decoded wrong: %+v", o)
	}
}
