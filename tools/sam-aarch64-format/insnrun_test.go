package format

import (
	"bytes"
	"reflect"
	"testing"
)

// A mode-1 INSN_RUN round-trips: writer → reader yields the same elements,
// and the wire bytes match the documented layout exactly.
func TestInsnRunMode1RoundTrip(t *testing.T) {
	elems := []InsnElement{
		// bl <sym 7>: base word with imm26 zeroed + one Branch26 patch.
		{BaseWord: 0x94000000, Patches: []InsnPatch{{Slot: 1, Expr: []byte{0x05, 0x07, 0x00}}}},
		// a literal nop carried inside the overlay run (patch_count == 0).
		{BaseWord: 0xd503201f},
	}

	var w RecordWriter
	w.WriteInsnRun(1, elems)

	wantPayload := []byte{
		0x01,                   // mode 1
		0x00, 0x00, 0x00, 0x94, // base 0x94000000
		0x01,             // patch_count 1
		0x13,             // packed header [slot:1|expr_len:3]
		0x05, 0x07, 0x00, // PUSH_SYM 0x0007
		0x1f, 0x20, 0x03, 0xd5, // base 0xd503201f
		0x00, // patch_count 0
	}
	want := append([]byte{byte(KindInsnRun), byte(len(wantPayload)), 0x00}, wantPayload...)
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("wire bytes:\n got %x\nwant %x", w.Bytes(), want)
	}

	rr := NewRecordReader(w.Bytes())
	rec, err := rr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if rec.Kind != KindInsnRun {
		t.Fatalf("kind = %#x, want INSN_RUN", byte(rec.Kind))
	}
	if rec.Mode != 1 {
		t.Fatalf("mode = %d, want 1", rec.Mode)
	}
	if !reflect.DeepEqual(rec.Elements, elems) {
		t.Errorf("elements = %+v, want %+v", rec.Elements, elems)
	}
	if !rr.AtEnd() {
		t.Errorf("reader not at end after one record")
	}
}

// A mode-1 patch whose expression is 15+ bytes takes the expr_len-nibble
// escape: header [slot:4|0xF] followed by the real length u8 (i39c).
func TestInsnRunMode1LenEscapeRoundTrip(t *testing.T) {
	longExpr := bytes.Repeat([]byte{0xAA}, 15)
	elems := []InsnElement{
		{BaseWord: 0x91000000, Patches: []InsnPatch{{Slot: 6, Expr: longExpr}}},
	}

	var w RecordWriter
	w.WriteInsnRun(1, elems)

	wantPayload := append([]byte{
		0x01,                   // mode 1
		0x00, 0x00, 0x00, 0x91, // base 0x91000000
		0x01, // patch_count 1
		0x6F, // packed header [slot:6|escape 0xF]
		0x0F, // real expr_len 15
	}, longExpr...)
	want := append([]byte{byte(KindInsnRun), byte(len(wantPayload)), 0x00}, wantPayload...)
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("wire bytes:\n got %x\nwant %x", w.Bytes(), want)
	}

	rr := NewRecordReader(w.Bytes())
	rec, err := rr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !reflect.DeepEqual(rec.Elements, elems) {
		t.Errorf("elements = %+v, want %+v", rec.Elements, elems)
	}
}

// A slot id that cannot fit the 4-bit packed header field is a compaction
// bug the writer refuses to encode.
func TestInsnRunSlotRangePanics(t *testing.T) {
	for _, slot := range []byte{0, 16} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("slot %d: want panic, got none", slot)
				}
			}()
			var w RecordWriter
			w.WriteInsnRun(1, []InsnElement{
				{BaseWord: 1, Patches: []InsnPatch{{Slot: slot, Expr: []byte{0x05, 0x00, 0x00}}}},
			})
		}()
	}
}

// A mode-0 INSN_RUN packs bare 4-byte words and reads back as patch-free
// elements (the LIT_INSTS floor, preserved).
func TestInsnRunMode0RoundTrip(t *testing.T) {
	words := []uint32{0x94000400, 0xd503201f, 0x52800000}
	var elems []InsnElement
	for _, word := range words {
		elems = append(elems, InsnElement{BaseWord: word})
	}

	var w RecordWriter
	w.WriteInsnRun(0, elems)

	wantPayload := []byte{
		0x00,                   // mode 0
		0x00, 0x04, 0x00, 0x94, // 0x94000400
		0x1f, 0x20, 0x03, 0xd5, // 0xd503201f
		0x00, 0x00, 0x80, 0x52, // 0x52800000
	}
	want := append([]byte{byte(KindInsnRun), byte(len(wantPayload)), 0x00}, wantPayload...)
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("wire bytes:\n got %x\nwant %x", w.Bytes(), want)
	}

	rr := NewRecordReader(w.Bytes())
	rec, err := rr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if rec.Kind != KindInsnRun || rec.Mode != 0 {
		t.Fatalf("kind=%#x mode=%d, want INSN_RUN mode 0", byte(rec.Kind), rec.Mode)
	}
	if !reflect.DeepEqual(rec.Elements, elems) {
		t.Errorf("elements = %+v, want %+v", rec.Elements, elems)
	}
}

func TestKindInsnRunName(t *testing.T) {
	if KindInsnRun.Name() != "INSN_RUN" {
		t.Errorf("Name = %q, want INSN_RUN", KindInsnRun.Name())
	}
	if !KindInsnRun.IsKnown() {
		t.Errorf("KindInsnRun should be known")
	}
}
