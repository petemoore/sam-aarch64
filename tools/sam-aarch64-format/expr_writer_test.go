package format

import (
	"bytes"
	"testing"
)

func TestExprWriteImmShortestFit(t *testing.T) {
	cases := []struct {
		v    int64
		want []byte
	}{
		{0, []byte{byte(OpPushImm8), 0x00}},
		{127, []byte{byte(OpPushImm8), 0x7F}},
		{-128, []byte{byte(OpPushImm8), 0x80}},
		{128, []byte{byte(OpPushImm16), 0x80, 0x00}},
		{-129, []byte{byte(OpPushImm16), 0x7F, 0xFF}},
		{32767, []byte{byte(OpPushImm16), 0xFF, 0x7F}},
		{32768, []byte{byte(OpPushImm32), 0x00, 0x80, 0x00, 0x00}},
		{int64(1) << 31, []byte{byte(OpPushImm64),
			0x00, 0x00, 0x00, 0x80, 0x00, 0x00, 0x00, 0x00}},
	}
	for _, c := range cases {
		var w ExprWriter
		w.WriteImm(c.v)
		if !bytes.Equal(w.Bytes(), c.want) {
			t.Errorf("WriteImm(%d) = % X, want % X", c.v, w.Bytes(), c.want)
		}
	}
}

func TestExprWriteSymAndLocal(t *testing.T) {
	var w ExprWriter
	w.WriteSym(0x1234)
	w.WriteLocal(3, 0)
	want := []byte{
		byte(OpPushSym), 0x34, 0x12,
		byte(OpPushLocal), 0x03, 0x00,
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}

func TestExprWriteOps(t *testing.T) {
	var w ExprWriter
	w.WriteSym(7)
	w.WriteImm(4)
	w.WriteOp(OpAdd)
	want := []byte{
		byte(OpPushSym), 0x07, 0x00,
		byte(OpPushImm8), 0x04,
		byte(OpAdd),
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}
