package format

import (
	"bytes"
	"testing"
)

func TestKindLitDataValue(t *testing.T) {
	if byte(KindLitData) != 0x08 {
		t.Errorf("KindLitData = 0x%02x, want 0x08", byte(KindLitData))
	}
	if KindLitData.Name() != "LIT_DATA" {
		t.Errorf("KindLitData.Name() = %q, want %q", KindLitData.Name(), "LIT_DATA")
	}
	if !KindLitData.IsKnown() {
		t.Errorf("KindLitData.IsKnown() = false, want true")
	}
}

func TestLitDataRoundTrip(t *testing.T) {
	dirID, _ := DirectiveID(".word")
	raw := []byte{0x78, 0x56, 0x34, 0x12, 0x21, 0x43, 0x65, 0x87} // two .word LE
	var rw RecordWriter
	rw.WriteLitData(dirID, raw)

	rr := NewRecordReader(rw.Bytes())
	rec, err := rr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if rec.Kind != KindLitData {
		t.Fatalf("Kind = %s, want LIT_DATA", rec.Kind.Name())
	}
	if rec.LitDataDirID != dirID {
		t.Errorf("LitDataDirID = %d, want %d (.word)", rec.LitDataDirID, dirID)
	}
	if !bytes.Equal(rec.LitData, raw) {
		t.Errorf("LitData = % X, want % X", rec.LitData, raw)
	}
	if !rr.AtEnd() {
		t.Errorf("reader not at end")
	}
}

// dataRec builds a numeric data DIRECTIVE record with the given
// directive name and per-element constant expressions.
func dataRec(t *testing.T, dirName string, vals ...int64) Record {
	t.Helper()
	id, ok := DirectiveID(dirName)
	if !ok {
		t.Fatalf("directive %s unknown", dirName)
	}
	var ow OperandWriter
	for _, v := range vals {
		var ew ExprWriter
		ew.WriteImm(v)
		ow.WriteImmExpr(ew.Bytes())
	}
	var rw RecordWriter
	rw.WriteDirective(id, byte(len(vals)), ow.Bytes())
	rr := NewRecordReader(rw.Bytes())
	rec, err := rr.Next()
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestConstDataWidth(t *testing.T) {
	cases := []struct {
		name     string
		dir      string
		wantW    int
		wantOK   bool
	}{
		{"word", ".word", 4, true},
		{"quad", ".quad", 8, true},
		{"byte", ".byte", 1, true},
		{"short", ".short", 2, true},
		{"hword", ".hword", 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, ok := ConstDataWidth(dataRec(t, c.dir, 1, 2, 3))
			if ok != c.wantOK || w != c.wantW {
				t.Errorf("%s: got (%d,%v), want (%d,%v)", c.dir, w, ok, c.wantW, c.wantOK)
			}
		})
	}
}

func TestConstDataWidth_SymbolOperand(t *testing.T) {
	// .quad some_symbol — not constant, must not collapse.
	var ew ExprWriter
	ew.WriteSym(0)
	var ow OperandWriter
	ow.WriteImmExpr(ew.Bytes())
	id, _ := DirectiveID(".quad")
	var rw RecordWriter
	rw.WriteDirective(id, 1, ow.Bytes())
	rr := NewRecordReader(rw.Bytes())
	rec, _ := rr.Next()
	if _, ok := ConstDataWidth(rec); ok {
		t.Errorf("symbol-bearing .quad: ConstDataWidth ok=true, want false")
	}
}

func TestConstDataWidth_NotNumericData(t *testing.T) {
	id, _ := DirectiveID(".text")
	var rw RecordWriter
	rw.WriteDirective(id, 0, nil)
	rr := NewRecordReader(rw.Bytes())
	rec, _ := rr.Next()
	if _, ok := ConstDataWidth(rec); ok {
		t.Errorf(".text: ConstDataWidth ok=true, want false")
	}
	// A non-directive record (INST) is never const data.
	irec := Record{Kind: KindInst, MnemonicID: 0, OperandCount: 0}
	if _, ok := ConstDataWidth(irec); ok {
		t.Errorf("INST: ConstDataWidth ok=true, want false")
	}
}

func TestConstDataWidth_Empty(t *testing.T) {
	// A .word with no operands collapses to nothing — not collapsible.
	if _, ok := ConstDataWidth(dataRec(t, ".word")); ok {
		t.Errorf("empty .word: ConstDataWidth ok=true, want false")
	}
}
