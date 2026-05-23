package main

import (
	"bytes"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func emitFile(t *testing.T, st *format.SymbolTable, rw format.RecordWriter) string {
	t.Helper()
	var buf bytes.Buffer
	format.WriteFile(&buf, st, rw.Bytes())
	out, err := Emit(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestEmitNop(t *testing.T) {
	var rw format.RecordWriter
	rw.WriteInst(0, 0, nil)
	got := emitFile(t, format.NewSymbolTable(), rw)
	if got != "  nop\n" {
		t.Errorf("got %q, want %q", got, "  nop\n")
	}
}

func TestEmitAddRegImm(t *testing.T) {
	var ow format.OperandWriter
	ow.WriteReg(format.OpRegX, 0)
	ow.WriteReg(format.OpRegX, 1)
	var ew format.ExprWriter
	ew.WriteImm(4)
	ow.WriteImmExpr(ew.Bytes())
	var rw format.RecordWriter
	id, _ := format.MnemonicID("add")
	rw.WriteInst(id, 3, ow.Bytes())
	got := emitFile(t, format.NewSymbolTable(), rw)
	if got != "  add x0, x1, #4\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitSymbolBranch(t *testing.T) {
	st := format.NewSymbolTable()
	id := st.Intern("target")
	var ew format.ExprWriter
	ew.WriteSym(id)
	var ow format.OperandWriter
	ow.WriteImmExpr(ew.Bytes())
	bid, _ := format.MnemonicID("b")
	var rw format.RecordWriter
	rw.WriteInst(bid, 1, ow.Bytes())
	got := emitFile(t, st, rw)
	if got != "  b target\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitDirectiveByte(t *testing.T) {
	var ow format.OperandWriter
	for _, v := range []int64{1, 2, 3} {
		var ew format.ExprWriter
		ew.WriteImm(v)
		ow.WriteImmExpr(ew.Bytes())
	}
	id, _ := format.DirectiveID(".byte")
	var rw format.RecordWriter
	rw.WriteDirective(id, 3, ow.Bytes())
	got := emitFile(t, format.NewSymbolTable(), rw)
	if got != "  .byte 1, 2, 3\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitShiftedReg(t *testing.T) {
	var ow format.OperandWriter
	ow.WriteReg(format.OpRegX, 0)
	ow.WriteReg(format.OpRegX, 1)
	var ew format.ExprWriter
	ew.WriteImm(4)
	ow.WriteShiftedReg(1, 2, format.ShiftLSL, ew.Bytes())
	var rw format.RecordWriter
	id, _ := format.MnemonicID("add")
	rw.WriteInst(id, 3, ow.Bytes())
	got := emitFile(t, format.NewSymbolTable(), rw)
	if got != "  add x0, x1, x2, lsl #4\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitMemShapes(t *testing.T) {
	cases := []struct {
		build func(*format.OperandWriter)
		want  string
	}{
		{func(ow *format.OperandWriter) { ow.WriteMemBase(1) }, "[x1]"},
		{
			func(ow *format.OperandWriter) {
				var ew format.ExprWriter
				ew.WriteImm(8)
				ow.WriteMemBaseOff(format.MemBaseOff, 1, ew.Bytes())
			},
			"[x1, #8]",
		},
		{
			func(ow *format.OperandWriter) {
				var ew format.ExprWriter
				ew.WriteImm(8)
				ow.WriteMemBaseOff(format.MemBaseOffPre, 1, ew.Bytes())
			},
			"[x1, #8]!",
		},
		{
			func(ow *format.OperandWriter) {
				var ew format.ExprWriter
				ew.WriteImm(8)
				ow.WriteMemBaseOff(format.MemBaseOffPost, 1, ew.Bytes())
			},
			"[x1], #8",
		},
		{func(ow *format.OperandWriter) { ow.WriteMemBaseIdx(1, 2, 1) }, "[x1, x2]"},
		{
			func(ow *format.OperandWriter) { ow.WriteMemBaseIdxShifted(1, 2, 1, 3) },
			"[x1, x2, lsl #3]",
		},
		{
			func(ow *format.OperandWriter) {
				ow.WriteMemBaseIdxExtended(1, 2, 0, format.ExtUXTW, 2)
			},
			"[x1, w2, uxtw #2]",
		},
	}
	for _, c := range cases {
		var ow format.OperandWriter
		ow.WriteReg(format.OpRegX, 0)
		c.build(&ow)
		var rw format.RecordWriter
		id, _ := format.MnemonicID("ldr")
		rw.WriteInst(id, 2, ow.Bytes())
		got := emitFile(t, format.NewSymbolTable(), rw)
		want := "  ldr x0, " + c.want + "\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestEmitImmDecimalVsHex(t *testing.T) {
	cases := []struct {
		v    int64
		want string
	}{
		{42, "#42"},
		{255, "#255"},
		{256, "#0x100"},
		{-1, "#-1"},
	}
	for _, c := range cases {
		var ew format.ExprWriter
		ew.WriteImm(c.v)
		var ow format.OperandWriter
		ow.WriteReg(format.OpRegX, 0)
		ow.WriteImmExpr(ew.Bytes())
		var rw format.RecordWriter
		id, _ := format.MnemonicID("mov")
		rw.WriteInst(id, 2, ow.Bytes())
		got := emitFile(t, format.NewSymbolTable(), rw)
		want := "  mov x0, " + c.want + "\n"
		if got != want {
			t.Errorf("v=%d: got %q, want %q", c.v, got, want)
		}
	}
}

func TestEmitExpressionWithSymbol(t *testing.T) {
	st := format.NewSymbolTable()
	id := st.Intern("msg")
	var ew format.ExprWriter
	ew.WriteSym(id)
	ew.WriteImm(8)
	ew.WriteOp(format.OpAdd)
	var ow format.OperandWriter
	ow.WriteReg(format.OpRegX, 0)
	ow.WriteImmExpr(ew.Bytes())
	var rw format.RecordWriter
	mid, _ := format.MnemonicID("mov")
	rw.WriteInst(mid, 2, ow.Bytes())
	got := emitFile(t, st, rw)
	if got != "  mov x0, (msg + 8)\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitLo12(t *testing.T) {
	st := format.NewSymbolTable()
	id := st.Intern("msg")
	var ew format.ExprWriter
	ew.WriteSym(id)
	ew.WriteOp(format.OpRelLo12)
	var ow format.OperandWriter
	ow.WriteReg(format.OpRegX, 0)
	ow.WriteReg(format.OpRegX, 1)
	ow.WriteImmExpr(ew.Bytes())
	var rw format.RecordWriter
	aid, _ := format.MnemonicID("add")
	rw.WriteInst(aid, 3, ow.Bytes())
	got := emitFile(t, st, rw)
	if got != "  add x0, x1, :lo12:msg\n" {
		t.Errorf("got %q", got)
	}
}
