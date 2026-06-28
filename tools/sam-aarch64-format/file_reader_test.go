package format

import (
	"bytes"
	"encoding/binary"
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

	sidecar := []SidecarRow{{Kind: SidecarComment, Comment: CommentRow{Anchor: 0, Placement: 0, Body: []byte("loop body")}}}
	globals := []uint16{1} // "exit" is .global

	var buf bytes.Buffer
	if err := WriteFile(&buf, st, nil, nil, rw.Bytes(), globals, sidecar); err != nil {
		t.Fatal(err)
	}

	f, err := ReadFile(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 3 {
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

func TestSidecarBlankRunRoundtrip(t *testing.T) {
	st := NewSymbolTable()
	var rw RecordWriter
	rw.WriteInsnRun(0, []InsnElement{{BaseWord: 0xd503201f}})

	// Interleaved comment + blank-run rows at assorted anchors, including two
	// rows sharing an anchor (source order must survive): a comment then a
	// 2-line blank run both at anchor 4.
	sidecar := []SidecarRow{
		{Kind: SidecarComment, Comment: CommentRow{Anchor: 0, Placement: 0, Body: []byte(" header")}},
		{Kind: SidecarBlank, Blank: BlankRun{Anchor: 0, RunLen: 1}},
		{Kind: SidecarComment, Comment: CommentRow{Anchor: 4, Placement: 0, Body: []byte(" mid")}},
		{Kind: SidecarBlank, Blank: BlankRun{Anchor: 4, RunLen: 2}},
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, st, nil, nil, rw.Bytes(), nil, sidecar); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFile(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if f.Flags&FlagTaggedSidecar == 0 {
		t.Errorf("expected FlagTaggedSidecar set, flags=%#x", f.Flags)
	}
	if len(f.Sidecar) != 4 {
		t.Fatalf("sidecar rows = %d, want 4: %+v", len(f.Sidecar), f.Sidecar)
	}
	// Row order: anchor-sorted, stable in source order at a tie. So:
	// [comment@0 "header"], [blank@0 x1], [comment@4 "mid"], [blank@4 x2].
	want := []struct {
		kind   SidecarKind
		anchor int64
		runLen uint32
		body   string
	}{
		{SidecarComment, 0, 0, " header"},
		{SidecarBlank, 0, 1, ""},
		{SidecarComment, 4, 0, " mid"},
		{SidecarBlank, 4, 2, ""},
	}
	for i, w := range want {
		r := f.Sidecar[i]
		if r.Kind != w.kind || r.Anchor() != w.anchor {
			t.Errorf("row %d kind/anchor = %d/%d, want %d/%d", i, r.Kind, r.Anchor(), w.kind, w.anchor)
		}
		if r.Kind == SidecarBlank && r.Blank.RunLen != w.runLen {
			t.Errorf("row %d run_len = %d, want %d", i, r.Blank.RunLen, w.runLen)
		}
		if r.Kind == SidecarComment && string(r.Comment.Body) != w.body {
			t.Errorf("row %d body = %q, want %q", i, r.Comment.Body, w.body)
		}
	}
	// The comment-only projection is the two comment rows.
	if len(f.Comments) != 2 || string(f.Comments[0].Body) != " header" || string(f.Comments[1].Body) != " mid" {
		t.Errorf("comments projection = %+v", f.Comments)
	}
}

func TestSidecarLegacyUntaggedReadsAsComments(t *testing.T) {
	// A file written WITHOUT FlagTaggedSidecar (the pre-i78 shape) must still
	// read: every row is an untagged comment. Hand-build such a file.
	st := NewSymbolTable()
	var rw RecordWriter
	rw.WriteInsnRun(0, []InsnElement{{BaseWord: 0xd503201f}})

	// Build the file then clear the tagged bit AND rewrite the sidecar untagged.
	// Easiest: serialise the untagged sidecar by hand into the editor region.
	records := rw.Bytes()
	tables := []byte{0, 0, 0, 0} // empty label + local tables (count u16 each)
	const headerLen = 4 + 2 + 2 + 4
	editorOffset := headerLen + len(tables) + len(records)

	var buf bytes.Buffer
	buf.Write(Magic[:])
	var u16 [2]byte
	binary.LittleEndian.PutUint16(u16[:], Version)
	buf.Write(u16[:])
	binary.LittleEndian.PutUint16(u16[:], 0) // flags = 0 → untagged
	buf.Write(u16[:])
	var u32 [4]byte
	binary.LittleEndian.PutUint32(u32[:], uint32(editorOffset))
	buf.Write(u32[:])
	buf.Write(tables)
	buf.Write(records)
	// Editor region: empty name table, empty globals, then an UNTAGGED sidecar
	// (no kind byte): one comment row [anchor_delta][placement][len][text].
	buf.Write([]byte{0, 0}) // name count 0
	buf.Write([]byte{0, 0}) // global count 0
	buf.Write([]byte{1, 0}) // sidecar count 1
	buf.WriteByte(0)        // anchor_delta = 0 (uvarint)
	buf.WriteByte(0)        // placement 0
	binary.LittleEndian.PutUint16(u16[:], uint16(len("legacy")))
	buf.Write(u16[:])
	buf.WriteString("legacy")

	f, err := ReadFile(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if f.Flags&FlagTaggedSidecar != 0 {
		t.Errorf("expected untagged flags, got %#x", f.Flags)
	}
	if len(f.Sidecar) != 1 || f.Sidecar[0].Kind != SidecarComment || string(f.Sidecar[0].Comment.Body) != "legacy" {
		t.Errorf("legacy sidecar = %+v", f.Sidecar)
	}
	_ = st
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
