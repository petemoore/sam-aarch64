package main

import (
	"bytes"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestPass2_Nop(t *testing.T) {
	var rw format.RecordWriter
	nopID, _ := format.MnemonicID("nop")
	rw.WriteInst(nopID, 0, nil)
	var buf bytes.Buffer
	format.WriteFile(&buf, format.NewSymbolTable(), rw.Bytes())
	f, _ := format.ReadFile(buf.Bytes())

	res, _ := Pass1(f)
	out, err := Pass2(f, res)
	if err != nil {
		t.Fatal(err)
	}
	// NOP = 0xD503201F (little-endian: 1F 20 03 D5)
	want := []byte{0x1f, 0x20, 0x03, 0xd5}
	if !bytes.Equal(out, want) {
		t.Errorf("got % X, want % X", out, want)
	}
}

func TestPass2_DirectiveBytes(t *testing.T) {
	st := format.NewSymbolTable()
	var rw format.RecordWriter
	var ow format.OperandWriter
	for _, v := range []int64{1, 2, 3} {
		var ew format.ExprWriter
		ew.WriteImm(v)
		ow.WriteImmExpr(ew.Bytes())
	}
	id, _ := format.DirectiveID(".byte")
	rw.WriteDirective(id, 3, ow.Bytes())

	var buf bytes.Buffer
	format.WriteFile(&buf, st, rw.Bytes())
	f, _ := format.ReadFile(buf.Bytes())

	res, _ := Pass1(f)
	out, err := Pass2(f, res)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 2, 3}
	if !bytes.Equal(out, want) {
		t.Errorf("got % X, want % X", out, want)
	}
}
