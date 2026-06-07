package format

import "testing"

// TestRecordReaderRoundtrip exercises the on-disk (overlay) record stream:
// COMMENT, DIRECTIVE, LIT_DATA, and INSN_RUN are the only kinds the reader
// decodes now that the symbolic kinds live only in the in-memory IR.
func TestRecordReaderRoundtrip(t *testing.T) {
	var rw RecordWriter
	rw.WriteComment(0, []byte("howdy"))
	dirID, _ := DirectiveID(".text")
	rw.WriteDirective(dirID, 0, nil)
	wordID, _ := DirectiveID(".word")
	rw.WriteLitData(wordID, []byte{0x78, 0x56, 0x34, 0x12})
	rw.WriteInsnRun(0, []InsnElement{{BaseWord: 0xd503201f}})

	r := NewRecordReader(rw.Bytes())

	rec, err := r.Next()
	if err != nil || rec.Kind != KindComment || rec.Placement != 0 || string(rec.Body) != "howdy" {
		t.Fatalf("rec0: %+v err=%v", rec, err)
	}
	rec, err = r.Next()
	if err != nil || rec.Kind != KindDirective || rec.DirectiveID != dirID || rec.OperandCount != 0 {
		t.Fatalf("rec1: %+v err=%v", rec, err)
	}
	rec, err = r.Next()
	if err != nil || rec.Kind != KindLitData || rec.LitDataDirID != wordID {
		t.Fatalf("rec2: %+v err=%v", rec, err)
	}
	rec, err = r.Next()
	if err != nil || rec.Kind != KindInsnRun || len(rec.Elements) != 1 || rec.Elements[0].BaseWord != 0xd503201f {
		t.Fatalf("rec3: %+v err=%v", rec, err)
	}
	if !r.AtEnd() {
		t.Errorf("reader not at end")
	}
}

func TestRecordReaderUnknownKindSurfaced(t *testing.T) {
	buf := []byte{0xFE, 3, 0, 'a', 'b', 'c'}
	r := NewRecordReader(buf)
	rec, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Kind != 0xFE {
		t.Errorf("kind = 0x%02x, want 0xFE", byte(rec.Kind))
	}
	if string(rec.Raw) != "abc" {
		t.Errorf("raw payload = %q, want %q", string(rec.Raw), "abc")
	}
}
