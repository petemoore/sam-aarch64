package format

import (
	"bytes"
	"testing"
)

func TestRecordWriterLabelDef(t *testing.T) {
	var rw RecordWriter
	rw.WriteLabelDef(42)
	want := []byte{
		byte(KindLabelDef),
		2, 0,
		42, 0,
	}
	if !bytes.Equal(rw.Bytes(), want) {
		t.Errorf("got % X, want % X", rw.Bytes(), want)
	}
}

func TestRecordWriterLocalDef(t *testing.T) {
	var rw RecordWriter
	rw.WriteLocalDef(3)
	want := []byte{byte(KindLocalDef), 1, 0, 3}
	if !bytes.Equal(rw.Bytes(), want) {
		t.Errorf("got % X, want % X", rw.Bytes(), want)
	}
}

func TestRecordWriterComment(t *testing.T) {
	var rw RecordWriter
	rw.WriteComment(1, []byte("hi"))
	want := []byte{byte(KindComment), 3, 0, 1, 'h', 'i'}
	if !bytes.Equal(rw.Bytes(), want) {
		t.Errorf("got % X, want % X", rw.Bytes(), want)
	}
}

func TestRecordWriterInst(t *testing.T) {
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteReg(OpRegX, 1)
	var ew ExprWriter
	ew.WriteImm(4)
	ow.WriteImmExpr(ew.Bytes())

	var rw RecordWriter
	id, _ := MnemonicID("add")
	rw.WriteInst(id, 3, ow.Bytes())

	got := rw.Bytes()
	if got[0] != byte(KindInst) {
		t.Errorf("kind = 0x%02x, want 0x%02x", got[0], byte(KindInst))
	}
	wantLen := uint16(3 + len(ow.Bytes()))
	gotLen := uint16(got[1]) | uint16(got[2])<<8
	if gotLen != wantLen {
		t.Errorf("len = %d, want %d", gotLen, wantLen)
	}
}
