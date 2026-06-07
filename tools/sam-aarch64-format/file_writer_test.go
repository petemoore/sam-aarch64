package format

import (
	"bytes"
	"testing"
)

func TestWriteFileMinimal(t *testing.T) {
	st := NewSymbolTable()
	st.Intern("loop")
	st.Intern("exit")

	var rw RecordWriter
	rw.WriteComment(0, []byte("x"))

	var buf bytes.Buffer
	if err := WriteFile(&buf, st, nil, nil, rw.Bytes()); err != nil {
		t.Fatal(err)
	}

	got := buf.Bytes()
	if string(got[:4]) != "SA64" {
		t.Errorf("magic = %q", string(got[:4]))
	}
	if got[4] != 2 || got[5] != 0 {
		t.Errorf("version = %d %d, want 2 0", got[4], got[5])
	}
	if got[8] != 2 || got[9] != 0 {
		t.Errorf("name count = %d %d, want 2 0", got[8], got[9])
	}
}

func TestWriteFileEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFile(&buf, NewSymbolTable(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if buf.Len() < 10 {
		t.Errorf("min file should be 10 bytes (magic+ver+flags+count), got %d", buf.Len())
	}
}
