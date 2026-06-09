package format

import (
	"bytes"
	"testing"
)

func TestReadFileRoundtrip(t *testing.T) {
	st := NewSymbolTable()
	st.Intern("loop")
	st.Intern("exit")

	// The on-disk record stream carries overlay records only; comments and
	// `.global` live in the editor region (M8 / i39b-2).
	var rw RecordWriter
	rw.WriteInsnRun(0, []InsnElement{{BaseWord: 0xd503201f}})

	comments := []CommentRow{{Anchor: 0, Placement: 0, Body: []byte("loop body")}}
	globals := []uint16{1} // "exit" is .global

	var buf bytes.Buffer
	if err := WriteFile(&buf, st, nil, nil, rw.Bytes(), globals, comments); err != nil {
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
	if len(f.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(f.Records))
	}
	if f.Records[0].Kind != KindInsnRun || len(f.Records[0].Elements) != 1 ||
		f.Records[0].Elements[0].BaseWord != 0xd503201f {
		t.Errorf("rec0 = %+v", f.Records[0])
	}
	if len(f.Comments) != 1 || f.Comments[0].Anchor != 0 || string(f.Comments[0].Body) != "loop body" {
		t.Errorf("comments = %+v", f.Comments)
	}
	if len(f.GlobalNameIDs) != 1 || f.GlobalNameIDs[0] != 1 {
		t.Errorf("globals = %+v", f.GlobalNameIDs)
	}
}

func TestReadFileWrongMagic(t *testing.T) {
	// 12-byte header (magic+version+flags+editor_region_offset) with bad magic.
	buf := []byte{'B', 'A', 'D', '!', 2, 0, 0, 0, 12, 0, 0, 0}
	if _, err := ReadFile(buf); err == nil {
		t.Errorf("expected error on bad magic")
	}
}

func TestReadFileWrongVersion(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(Magic[:])
	buf.Write([]byte{99, 0, 0, 0, 12, 0, 0, 0}) // version=99, flags=0, editor_region_offset=12
	if _, err := ReadFile(buf.Bytes()); err == nil {
		t.Errorf("expected error on unknown version")
	}
}
