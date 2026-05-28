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
	if err := format.WriteFile(&buf, st, recordBytes); err != nil {
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
