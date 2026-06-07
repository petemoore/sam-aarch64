package format

import (
	"bytes"
	"testing"
)

func TestRecordWriterComment(t *testing.T) {
	var rw RecordWriter
	rw.WriteComment(1, []byte("hi"))
	want := []byte{byte(KindComment), 3, 0, 1, 'h', 'i'}
	if !bytes.Equal(rw.Bytes(), want) {
		t.Errorf("got % X, want % X", rw.Bytes(), want)
	}
}

func TestRecordWriterDirective(t *testing.T) {
	var ow OperandWriter
	var ew ExprWriter
	ew.WriteImm(4)
	ow.WriteImmExpr(ew.Bytes())

	var rw RecordWriter
	id, _ := DirectiveID(".byte")
	rw.WriteDirective(id, 1, ow.Bytes())

	got := rw.Bytes()
	if got[0] != byte(KindDirective) {
		t.Errorf("kind = 0x%02x, want 0x%02x", got[0], byte(KindDirective))
	}
	wantLen := uint16(2 + len(ow.Bytes()))
	gotLen := uint16(got[1]) | uint16(got[2])<<8
	if gotLen != wantLen {
		t.Errorf("len = %d, want %d", gotLen, wantLen)
	}
}
