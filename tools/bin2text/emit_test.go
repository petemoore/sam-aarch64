package main

import (
	"bytes"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestEmitEmpty(t *testing.T) {
	var buf bytes.Buffer
	format.WriteFile(&buf, format.NewSymbolTable(), nil)
	out, err := Emit(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("emit of empty file = %q, want empty", string(out))
	}
}

func TestEmitLabelDef(t *testing.T) {
	st := format.NewSymbolTable()
	st.Intern("loop")
	var rw format.RecordWriter
	rw.WriteLabelDef(0)
	var buf bytes.Buffer
	format.WriteFile(&buf, st, rw.Bytes())
	out, err := Emit(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	want := "loop:\n"
	if string(out) != want {
		t.Errorf("emit = %q, want %q", string(out), want)
	}
}

func TestEmitLocalDef(t *testing.T) {
	var rw format.RecordWriter
	rw.WriteLocalDef(3)
	var buf bytes.Buffer
	format.WriteFile(&buf, format.NewSymbolTable(), rw.Bytes())
	out, _ := Emit(buf.Bytes())
	if string(out) != "3:\n" {
		t.Errorf("emit = %q, want %q", string(out), "3:\n")
	}
}

func TestEmitCommentPlacement(t *testing.T) {
	var rw format.RecordWriter
	rw.WriteComment(0, []byte(" standalone"))
	rw.WriteLabelDef(0)
	rw.WriteComment(1, []byte(" trailing"))
	st := format.NewSymbolTable()
	st.Intern("x")
	var buf bytes.Buffer
	format.WriteFile(&buf, st, rw.Bytes())
	out, _ := Emit(buf.Bytes())
	want := "// standalone\nx: // trailing\n"
	if string(out) != want {
		t.Errorf("got %q, want %q", string(out), want)
	}
}
