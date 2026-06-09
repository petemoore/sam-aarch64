package format

import (
	"bytes"
	"testing"
)

func TestReadFileRoundtrip(t *testing.T) {
	st := NewSymbolTable()
	st.Intern("loop")
	st.Intern("exit")

	// The on-disk record stream carries overlay-format records only.
	var rw RecordWriter
	rw.WriteComment(0, []byte("loop body"))
	rw.WriteInsnRun(0, []InsnElement{{BaseWord: 0xd503201f}})

	var buf bytes.Buffer
	if err := WriteFile(&buf, st, nil, nil, rw.Bytes()); err != nil {
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
	if len(f.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(f.Records))
	}
	if f.Records[0].Kind != KindComment || string(f.Records[0].Body) != "loop body" {
		t.Errorf("rec0 = %+v", f.Records[0])
	}
	if f.Records[1].Kind != KindInsnRun || len(f.Records[1].Elements) != 1 ||
		f.Records[1].Elements[0].BaseWord != 0xd503201f {
		t.Errorf("rec1 = %+v", f.Records[1])
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
