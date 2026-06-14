package render

import (
	"strings"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// nop is mnemonic id 0 (see TestEmitNop). Render emits "  nop\n".

// TestEmitBlankRunSymbolic exercises the symbolic-IR render path: a BLANK_RUN
// record between two statements emits that many empty lines, in position.
func TestEmitBlankRunSymbolic(t *testing.T) {
	cases := []struct {
		name string
		recs []format.Record
		want string
	}{
		{
			name: "single blank between two nops",
			recs: []format.Record{instRec(0, 0, nil), blankRunRec(1), instRec(0, 0, nil)},
			want: "  nop\n\n  nop\n",
		},
		{
			name: "run of three blanks",
			recs: []format.Record{instRec(0, 0, nil), blankRunRec(3), instRec(0, 0, nil)},
			want: "  nop\n\n\n\n  nop\n",
		},
		{
			name: "leading blank run",
			recs: []format.Record{blankRunRec(2), instRec(0, 0, nil)},
			want: "\n\n  nop\n",
		},
		{
			name: "trailing blank run",
			recs: []format.Record{instRec(0, 0, nil), blankRunRec(2)},
			want: "  nop\n\n\n",
		},
		{
			name: "blank between comment paragraphs (disambiguated from textless //)",
			recs: []format.Record{
				commentRec(0, []byte(" para one")),
				commentRec(0, []byte("")), // textless // — a comment, NOT a blank
				commentRec(0, []byte(" para two")),
				blankRunRec(1), // a real blank line
				instRec(0, 0, nil),
			},
			want: "// para one\n//\n// para two\n\n  nop\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := emitRecords(t, nil, c.recs)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestEmitBlankRunCompact exercises the compact render path: blank runs live in
// f.Sidecar (anchored to output PC), interleaved with comments, and the PC walk
// emits them in stored (source) order at their anchor.
func TestEmitBlankRunCompact(t *testing.T) {
	// One instruction run of two nops (8 bytes). A comment + a blank run anchor
	// at PC 0 (before the first nop); a blank run anchors at PC 4 (between the
	// two nops). Build the File the way ReadFile would hand it to the renderer.
	var rw format.RecordWriter
	rw.WriteInsnRun(0, []format.InsnElement{{BaseWord: 0xd503201f}, {BaseWord: 0xd503201f}})
	rr := format.NewRecordReader(rw.Bytes())
	var recs []format.Record
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}

	f := &format.File{
		Version: format.Version,
		Flags:   format.Flags,
		Records: recs,
		Sidecar: []format.SidecarRow{
			{Kind: format.SidecarComment, Comment: format.CommentRow{Anchor: 0, Placement: 0, Body: []byte(" header")}},
			{Kind: format.SidecarBlank, Blank: format.BlankRun{Anchor: 0, RunLen: 1}},
			{Kind: format.SidecarBlank, Blank: format.BlankRun{Anchor: 4, RunLen: 2}},
		},
	}
	out, err := EmitFile(f)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	// header comment, one blank, first nop, two blanks, second nop.
	want := "// header\n\n  nop\n\n\n  nop\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// EmitLines must agree byte-for-byte (the two renderers share emit logic).
	lines, err := EmitLines(f)
	if err != nil {
		t.Fatal(err)
	}
	if joined := JoinLines(lines); joined != want {
		t.Errorf("EmitLines join = %q, want %q", joined, want)
	}
}

// TestEmitBlankRunSharedAnchorOrder verifies that at one anchor, stored order is
// source order: comment-then-blank renders differently from blank-then-comment.
func TestEmitBlankRunSharedAnchorOrder(t *testing.T) {
	mk := func(rows []format.SidecarRow) string {
		var rw format.RecordWriter
		rw.WriteInsnRun(0, []format.InsnElement{{BaseWord: 0xd503201f}})
		rr := format.NewRecordReader(rw.Bytes())
		var recs []format.Record
		for !rr.AtEnd() {
			rec, _ := rr.Next()
			recs = append(recs, rec)
		}
		out, err := EmitFile(&format.File{Version: format.Version, Flags: format.Flags, Records: recs, Sidecar: rows})
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	commentFirst := mk([]format.SidecarRow{
		{Kind: format.SidecarComment, Comment: format.CommentRow{Anchor: 0, Body: []byte(" c")}},
		{Kind: format.SidecarBlank, Blank: format.BlankRun{Anchor: 0, RunLen: 1}},
	})
	blankFirst := mk([]format.SidecarRow{
		{Kind: format.SidecarBlank, Blank: format.BlankRun{Anchor: 0, RunLen: 1}},
		{Kind: format.SidecarComment, Comment: format.CommentRow{Anchor: 0, Body: []byte(" c")}},
	})
	if commentFirst == blankFirst {
		t.Errorf("shared-anchor order not preserved: both render as %q", commentFirst)
	}
	if !strings.HasPrefix(commentFirst, "// c\n\n") {
		t.Errorf("comment-first = %q, want comment then blank", commentFirst)
	}
	if !strings.HasPrefix(blankFirst, "\n// c\n") {
		t.Errorf("blank-first = %q, want blank then comment", blankFirst)
	}
}
