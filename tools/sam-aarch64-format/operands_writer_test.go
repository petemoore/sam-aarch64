package format

import (
	"bytes"
	"testing"
)

func TestOperandWriteRegX(t *testing.T) {
	var w OperandWriter
	w.WriteReg(OpRegX, 5)
	want := []byte{byte(OpRegX), 5}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}

func TestOperandWriteImmExpr(t *testing.T) {
	var ew ExprWriter
	ew.WriteImm(0x42)
	var ow OperandWriter
	ow.WriteImmExpr(ew.Bytes())
	want := []byte{
		byte(OpImmExpr),
		2, 0,
		byte(OpPushImm8), 0x42,
	}
	if !bytes.Equal(ow.Bytes(), want) {
		t.Errorf("got % X, want % X", ow.Bytes(), want)
	}
}

func TestOperandWriteShiftedReg(t *testing.T) {
	var ew ExprWriter
	ew.WriteImm(4)
	var w OperandWriter
	w.WriteShiftedReg(1, 2, ShiftLSL, ew.Bytes())
	want := []byte{
		byte(OpShiftedReg),
		1, 2, byte(ShiftLSL),
		2, 0,
		byte(OpPushImm8), 4,
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}

func TestOperandWriteMemBaseOff(t *testing.T) {
	var ew ExprWriter
	ew.WriteImm(8)
	var w OperandWriter
	w.WriteMemBaseOff(MemBaseOff, 1, ew.Bytes())
	want := []byte{
		byte(OpMem),
		byte(MemBaseOff),
		1,
		2, 0,
		byte(OpPushImm8), 8,
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}

func TestOperandWriteString(t *testing.T) {
	var w OperandWriter
	w.WriteString([]byte("hi"))
	want := []byte{byte(OpString), 2, 0, 'h', 'i'}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}

func TestOperandWriteLitPool(t *testing.T) {
	var w OperandWriter
	// width=8, expr = PUSH_IMM8(0x42)
	expr := []byte{byte(OpPushImm8), 0x42}
	w.WriteLitPool(8, expr)
	want := []byte{
		byte(OpLitPool), 8, byte(len(expr)), 0,
		byte(OpPushImm8), 0x42,
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}

func TestOperandWriteCondSysName(t *testing.T) {
	var w OperandWriter
	w.WriteCond(CondNE)
	w.WriteSysName("sctlr_el1")
	want := []byte{
		byte(OpCond), byte(CondNE),
		byte(OpSysName), 9, 0, 's', 'c', 't', 'l', 'r', '_', 'e', 'l', '1',
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}
