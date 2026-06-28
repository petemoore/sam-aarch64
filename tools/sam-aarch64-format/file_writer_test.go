package format

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestWriteFileMinimal(t *testing.T) {
	st := NewSymbolTable()
	st.Intern("loop")
	st.Intern("exit")

	var rw RecordWriter
	rw.WriteDirective(0, 0, nil) // a .text no-op record in the assembler stream

	var buf bytes.Buffer
	if err := WriteFile(&buf, st, nil, nil, rw.Bytes(), nil, nil); err != nil {
		t.Fatal(err)
	}

	got := buf.Bytes()
	if string(got[:4]) != "SA64" {
		t.Errorf("magic = %q", string(got[:4]))
	}
	if got[4] != 3 || got[5] != 0 {
		t.Errorf("version = %d %d, want 3 0", got[4], got[5])
	}
	// The name table moved to the editor region (M8 / i39b-2). The section
	// index at bytes 8..12 points at it; its first field is the name count.
	editorOff := binary.LittleEndian.Uint32(got[8:12])
	if int(editorOff) >= len(got)-1 {
		t.Fatalf("editor_region_offset %d out of range (file %d)", editorOff, len(got))
	}
	if got[editorOff] != 2 || got[editorOff+1] != 0 {
		t.Errorf("name count = %d %d, want 2 0", got[editorOff], got[editorOff+1])
	}
}

func TestWriteFileEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFile(&buf, NewSymbolTable(), nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	// magic(4)+ver(2)+flags(2)+editor_region_offset(4) + label-count(2) +
	// local-count(2) + 0 records + editor region (name-count(2)+global-count(2)
	// +comment-count(2)) = 22 bytes.
	if buf.Len() != 22 {
		t.Errorf("min empty v2 file should be 22 bytes, got %d", buf.Len())
	}
}
