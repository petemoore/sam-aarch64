package format

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The name table is front-coded (i39b-1): each entry stores the length of
// the prefix it shares with the PREVIOUS name (in encounter / intern order)
// plus the remaining suffix, both as LEB128 uvarints. Decode reconstructs a
// name by copying `shared` bytes from the prior name and appending the suffix.

// nameTableBytes returns the slice of NAME-TABLE entry bytes (the front-coded
// entries only — after the name count, before the global-flags/comment
// sidecars). The name table now lives in the editor region (M8 / i39b-2),
// located via the section index at bytes 8..12. With no labels/locals/records
// the editor region is name-table + empty global-flags(2) + empty comment
// sidecar(2), so the entries run up to the last 4 bytes.
func nameTableBytes(t *testing.T, names ...string) []byte {
	t.Helper()
	st := NewSymbolTable()
	for _, n := range names {
		st.Intern(n)
	}
	var buf bytes.Buffer
	if err := WriteFile(&buf, st, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	editorOff := int(binary.LittleEndian.Uint32(b[8:12]))
	// editor region: [name count u16][entries…][global count u16=0][comment count u16=0]
	return b[editorOff+2 : len(b)-4]
}

func TestNameTableFrontCodeGolden(t *testing.T) {
	// "loop" shares nothing with "" -> shared=0, suffix="loop".
	// "loops" shares "loop" with the prior name -> shared=4, suffix="s".
	got := nameTableBytes(t, "loop", "loops")
	want := []byte{
		0x00, 0x04, 'l', 'o', 'o', 'p', // shared=0, suffix_len=4, "loop"
		0x04, 0x01, 's', //               shared=4, suffix_len=1, "s"
	}
	if !bytes.Equal(got, want) {
		t.Errorf("front-coded name table\n got = % x\nwant = % x", got, want)
	}
}

func TestNameTableFrontCodeRoundtrip(t *testing.T) {
	// A prefix-sharing set spanning a few common spectrum4 stems.
	names := []string{
		"spectrum4_init", "spectrum4_io", "spectrum4_iomap",
		"__bss_start", "__bss_end", "handle_irq", "handle_irq_tail", "x",
	}
	st := NewSymbolTable()
	for _, n := range names {
		st.Intern(n)
	}
	var buf bytes.Buffer
	if err := WriteFile(&buf, st, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFile(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Names) != len(names) {
		t.Fatalf("recovered %d names, want %d", len(f.Names), len(names))
	}
	for i, n := range names {
		if f.Names[i] != n {
			t.Errorf("name[%d] = %q, want %q", i, f.Names[i], n)
		}
	}
}

// Front-coding must be strictly smaller than the old [len u16][bytes] layout
// for a prefix-sharing set (the whole point of i39b-1).
func TestNameTableFrontCodeShrinks(t *testing.T) {
	names := []string{"spectrum4_a", "spectrum4_b", "spectrum4_c", "spectrum4_dd"}
	got := len(nameTableBytes(t, names...))
	old := 0
	for _, n := range names {
		old += 2 + len(n) // [len u16][bytes]
	}
	if got >= old {
		t.Errorf("front-coded table = %d bytes, not smaller than old layout %d", got, old)
	}
}
