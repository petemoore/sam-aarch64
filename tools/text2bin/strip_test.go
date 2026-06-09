package main

import (
	"bytes"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// buildTbn constructs a synthetic .tbn with a name table containing
// `names` and a record stream containing the given Record values.  It
// uses the format library's writers so the output is canonical.
func buildTbn(t *testing.T, names []string, recordBytes []byte) []byte {
	t.Helper()
	st := format.NewSymbolTable()
	for _, n := range names {
		st.Intern(n)
	}
	var buf bytes.Buffer
	if err := format.WriteFile(&buf, st, nil, nil, recordBytes); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestStripCommentRecords_RemovesOnlyComments(t *testing.T) {
	// Build a record stream containing: a label, an inst, a comment, a
	// directive, another comment, a local def.  After stripping we
	// expect only label + inst + directive + local.
	var rw format.RecordWriter
	rw.WriteLabelDef(0)
	rw.WriteInst(1, 0, nil)
	rw.WriteComment(0, []byte("a discarded comment"))
	rw.WriteDirective(2, 0, nil)
	rw.WriteComment(1, []byte("trailing comment also discarded"))
	rw.WriteLocalDef(3)

	in := buildTbn(t, []string{"_start"}, rw.Bytes())

	out, err := stripCommentRecords(in)
	if err != nil {
		t.Fatal(err)
	}

	// Decode the result and assert the surviving record kinds.
	f, err := format.ReadFile(out)
	if err != nil {
		t.Fatalf("re-read after strip: %v", err)
	}
	r := format.NewRecordReader(f.Records)
	var kinds []format.RecordKind
	for !r.AtEnd() {
		rec, err := r.Next()
		if err != nil {
			t.Fatalf("read record: %v", err)
		}
		kinds = append(kinds, rec.Kind)
	}
	want := []format.RecordKind{
		format.KindLabelDef,
		format.KindInst,
		format.KindDirective,
		format.KindLocalDef,
	}
	if len(kinds) != len(want) {
		t.Fatalf("got kinds %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("kinds[%d] = %v, want %v", i, kinds[i], want[i])
		}
	}

	// Sanity: name table preserved verbatim.
	if len(f.Names) != 1 || f.Names[0] != "_start" {
		t.Errorf("name table = %v, want [_start]", f.Names)
	}
}

func TestStripCommentRecords_NoCommentsIsIdempotent(t *testing.T) {
	// Stripping a record stream that has no comments must produce the
	// same bytes back (sanity: we don't mangle non-comment records).
	var rw format.RecordWriter
	rw.WriteLabelDef(0)
	rw.WriteInst(1, 0, nil)
	rw.WriteDirective(2, 0, nil)
	in := buildTbn(t, []string{"_start"}, rw.Bytes())

	out, err := stripCommentRecords(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Errorf("strip of comment-free .tbn should be a no-op:\n  in = %x\n out = %x", in, out)
	}
}

func TestStripCommentRecords_RemovesAllWhenAllAreComments(t *testing.T) {
	var rw format.RecordWriter
	rw.WriteComment(0, []byte("one"))
	rw.WriteComment(0, []byte("two"))
	rw.WriteComment(1, []byte("three"))
	in := buildTbn(t, nil, rw.Bytes())

	out, err := stripCommentRecords(in)
	if err != nil {
		t.Fatal(err)
	}
	f, err := format.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Records) != 0 {
		t.Errorf("expected empty record stream, got %d bytes", len(f.Records))
	}
}

// — stripDataRecords tests —

func dirID(t *testing.T, name string) byte {
	t.Helper()
	id, ok := format.DirectiveID(name)
	if !ok {
		t.Fatalf("unknown directive %q", name)
	}
	return id
}

// makeLitPoolOperands returns the encoded operand bytes for an ldr Xn,=expr
// instruction: one OpRegX operand followed by one OpLitPool operand.
func makeLitPoolOperands() []byte {
	var ow format.OperandWriter
	ow.WriteReg(format.OpRegX, 0) // x0
	ow.WriteLitPool(8, []byte{0x01, 0x00}) // =1 (8-byte pool entry)
	return ow.Bytes()
}

func TestStripDataRecords_RemovesDataDirectivesAndLitPool(t *testing.T) {
	wordID := dirID(t, ".word")
	quadID := dirID(t, ".quad")
	ltorgID := dirID(t, ".ltorg")

	var rw format.RecordWriter
	rw.WriteLabelDef(0)                  // keep: label
	rw.WriteInst(1, 0, nil)              // keep: plain instruction
	rw.WriteDirective(wordID, 0, nil)    // strip: .word
	rw.WriteDirective(quadID, 0, nil)    // strip: .quad
	rw.WriteDirective(ltorgID, 0, nil)   // strip: .ltorg
	ops := makeLitPoolOperands()
	rw.WriteInst(2, 2, ops)             // strip: ldr x0, =expr
	rw.WriteLocalDef(1)                  // keep: local label

	in := buildTbn(t, []string{"_start"}, rw.Bytes())
	out, err := stripDataRecords(in)
	if err != nil {
		t.Fatal(err)
	}

	f, err := format.ReadFile(out)
	if err != nil {
		t.Fatalf("re-read after strip: %v", err)
	}
	r := format.NewRecordReader(f.Records)
	var kinds []format.RecordKind
	for !r.AtEnd() {
		rec, err := r.Next()
		if err != nil {
			t.Fatalf("read record: %v", err)
		}
		kinds = append(kinds, rec.Kind)
	}
	want := []format.RecordKind{
		format.KindLabelDef,
		format.KindInst,
		format.KindLocalDef,
	}
	if len(kinds) != len(want) {
		t.Fatalf("got kinds %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("kinds[%d] = %v, want %v", i, kinds[i], want[i])
		}
	}
}

func TestStripDataRecords_PreservesNonDataDirectives(t *testing.T) {
	globalID := dirID(t, ".global")
	equID := dirID(t, ".equ")
	wordID := dirID(t, ".word")

	var rw format.RecordWriter
	rw.WriteDirective(globalID, 0, nil) // keep: .global
	rw.WriteDirective(equID, 0, nil)    // keep: .equ
	rw.WriteDirective(wordID, 0, nil)   // strip: .word

	in := buildTbn(t, nil, rw.Bytes())
	out, err := stripDataRecords(in)
	if err != nil {
		t.Fatal(err)
	}

	f, err := format.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	r := format.NewRecordReader(f.Records)
	var dirNames []string
	for !r.AtEnd() {
		rec, err := r.Next()
		if err != nil {
			t.Fatalf("read record: %v", err)
		}
		if rec.Kind == format.KindDirective {
			dirNames = append(dirNames, format.DirectiveName(rec.DirectiveID))
		}
	}
	if len(dirNames) != 2 || dirNames[0] != ".global" || dirNames[1] != ".equ" {
		t.Errorf("got directives %v, want [.global .equ]", dirNames)
	}
}

func TestStripDataRecords_NoDataIsIdempotent(t *testing.T) {
	var rw format.RecordWriter
	rw.WriteLabelDef(0)
	rw.WriteInst(1, 0, nil)
	in := buildTbn(t, []string{"_start"}, rw.Bytes())

	out, err := stripDataRecords(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Errorf("strip of data-free .tbn should be a no-op")
	}
}
