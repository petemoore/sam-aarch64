package format

import (
	"bytes"
	"testing"
)

func TestReadFileRoundtrip(t *testing.T) {
	st := NewSymbolTable()
	st.Intern("loop")
	st.Intern("exit")

	var rw RecordWriter
	rw.WriteLabelDef(0)
	rw.WriteLabelDef(1)

	var buf bytes.Buffer
	if err := WriteFile(&buf, st, rw.Bytes()); err != nil {
		t.Fatal(err)
	}

	f, err := ReadFile(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 2 {
		t.Errorf("version = %d", f.Version)
	}
	if len(f.Names) != 2 || f.Names[0] != "loop" || f.Names[1] != "exit" {
		t.Errorf("names = %v", f.Names)
	}
	if !bytes.Equal(f.Records, rw.Bytes()) {
		t.Errorf("records mismatch:\n got: % X\nwant: % X", f.Records, rw.Bytes())
	}
}

func TestReadFileWrongMagic(t *testing.T) {
	buf := []byte{'B', 'A', 'D', '!', 1, 0, 0, 0, 0, 0}
	if _, err := ReadFile(buf); err == nil {
		t.Errorf("expected error on bad magic")
	}
}

func TestReadFileWrongVersion(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(Magic[:])
	buf.Write([]byte{99, 0, 0, 0, 0, 0})
	if _, err := ReadFile(buf.Bytes()); err == nil {
		t.Errorf("expected error on unknown version")
	}
}
