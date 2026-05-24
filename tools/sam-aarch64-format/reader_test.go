package format

import "testing"

func TestRecordReaderRoundtrip(t *testing.T) {
	var rw RecordWriter
	rw.WriteLabelDef(7)
	rw.WriteLocalDef(2)
	rw.WriteComment(0, []byte("howdy"))
	rw.WriteInst(0, 0, nil)

	r := NewRecordReader(rw.Bytes())

	rec, err := r.Next()
	if err != nil || rec.Kind != KindLabelDef || rec.SymbolID != 7 {
		t.Fatalf("rec0: %+v err=%v", rec, err)
	}
	rec, err = r.Next()
	if err != nil || rec.Kind != KindLocalDef || rec.Digit != 2 {
		t.Fatalf("rec1: %+v err=%v", rec, err)
	}
	rec, err = r.Next()
	if err != nil || rec.Kind != KindComment || rec.Placement != 0 || string(rec.Body) != "howdy" {
		t.Fatalf("rec2: %+v err=%v", rec, err)
	}
	rec, err = r.Next()
	if err != nil || rec.Kind != KindInst || rec.MnemonicID != 0 || rec.OperandCount != 0 {
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
