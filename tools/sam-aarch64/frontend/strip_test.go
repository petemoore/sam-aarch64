package frontend

import (
	"reflect"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestStripCommentRecords_RemovesOnlyComments(t *testing.T) {
	// A record stream containing: a label, an inst, a comment, a
	// directive, another comment, a local def.  After stripping we
	// expect only label + inst + directive + local.
	in := []format.Record{
		labelRec(0),
		instRec(1, 0, nil),
		commentRec(0, []byte("a discarded comment")),
		dirRec(2, 0, nil),
		commentRec(1, []byte("trailing comment also discarded")),
		localRec(3),
	}

	out := StripCommentRecords(in)

	want := []format.RecordKind{
		format.KindLabelDef,
		format.KindInst,
		format.KindDirective,
		format.KindLocalDef,
	}
	if got := recordKinds(out); !reflect.DeepEqual(got, want) {
		t.Errorf("got kinds %v, want %v", got, want)
	}
}

func TestStripCommentRecords_NoCommentsIsIdempotent(t *testing.T) {
	// Stripping a record stream that has no comments must produce the
	// same records back (sanity: we don't mangle non-comment records).
	in := []format.Record{
		labelRec(0),
		instRec(1, 0, nil),
		dirRec(2, 0, nil),
	}
	out := StripCommentRecords(in)
	if !reflect.DeepEqual(in, out) {
		t.Errorf("strip of comment-free records should be a no-op:\n  in = %+v\n out = %+v", in, out)
	}
}

func TestStripCommentRecords_RemovesAllWhenAllAreComments(t *testing.T) {
	in := []format.Record{
		commentRec(0, []byte("one")),
		commentRec(0, []byte("two")),
		commentRec(1, []byte("three")),
	}
	out := StripCommentRecords(in)
	if len(out) != 0 {
		t.Errorf("expected empty record stream, got %d records", len(out))
	}
}

// — StripDataRecords tests —

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
	ow.WriteReg(format.OpRegX, 0)          // x0
	ow.WriteLitPool(8, []byte{0x01, 0x00}) // =1 (8-byte pool entry)
	return ow.Bytes()
}

func TestStripDataRecords_RemovesDataDirectivesAndLitPool(t *testing.T) {
	wordID := dirID(t, ".word")
	quadID := dirID(t, ".quad")
	ltorgID := dirID(t, ".ltorg")

	in := []format.Record{
		labelRec(0),                          // keep: label
		instRec(1, 0, nil),                   // keep: plain instruction
		dirRec(wordID, 0, nil),               // strip: .word
		dirRec(quadID, 0, nil),               // strip: .quad
		dirRec(ltorgID, 0, nil),              // strip: .ltorg
		instRec(2, 2, makeLitPoolOperands()), // strip: ldr x0, =expr
		localRec(1),                          // keep: local label
	}

	out := StripDataRecords(in)

	want := []format.RecordKind{
		format.KindLabelDef,
		format.KindInst,
		format.KindLocalDef,
	}
	if got := recordKinds(out); !reflect.DeepEqual(got, want) {
		t.Errorf("got kinds %v, want %v", got, want)
	}
}

func TestStripDataRecords_PreservesNonDataDirectives(t *testing.T) {
	globalID := dirID(t, ".global")
	equID := dirID(t, ".equ")
	wordID := dirID(t, ".word")

	in := []format.Record{
		dirRec(globalID, 0, nil), // keep: .global
		dirRec(equID, 0, nil),    // keep: .equ
		dirRec(wordID, 0, nil),   // strip: .word
	}

	out := StripDataRecords(in)

	var dirNames []string
	for _, rec := range out {
		if rec.Kind == format.KindDirective {
			dirNames = append(dirNames, format.DirectiveName(rec.DirectiveID))
		}
	}
	if len(dirNames) != 2 || dirNames[0] != ".global" || dirNames[1] != ".equ" {
		t.Errorf("got directives %v, want [.global .equ]", dirNames)
	}
}

func TestStripDataRecords_NoDataIsIdempotent(t *testing.T) {
	in := []format.Record{
		labelRec(0),
		instRec(1, 0, nil),
	}
	out := StripDataRecords(in)
	if !reflect.DeepEqual(in, out) {
		t.Errorf("strip of data-free records should be a no-op")
	}
}
