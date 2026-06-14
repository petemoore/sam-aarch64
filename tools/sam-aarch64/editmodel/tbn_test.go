package editmodel

import (
	"bytes"
	"testing"
)

// validAsmSource is a small, complete aarch64 source that frontend.Translate
// handles without flatten. The canonical rendered form re-emits hex literals
// and tab-separated operands (e.g. "mov\tx0, #0x0"), so the first
// text→.tbn serialize canonicalizes whitespace / literal formatting, but the
// subsequent .tbn→text→.tbn round-trip is byte-stable (the real invariant).
const validAsmSource = `main:
  mov x0, #0
  add x0, x0, #1
  ret
`

// TestSerializeTBNRoundTrip verifies the .tbn-level round-trip invariant:
// encode → decode → encode produces byte-identical output. This mirrors the
// project's disasm-roundtrip gate. The first serialize may canonicalize the
// source text; the subsequent round-trip must be byte-stable.
func TestSerializeTBNRoundTrip(t *testing.T) {
	// Build doc from the valid source.
	d := New()
	lines := splitLines(validAsmSource)
	for _, l := range lines {
		d.InsertLine(d.LineCount(), []byte(l))
	}

	// Step 1: SerializeTBN → bytes1.
	var buf1 bytes.Buffer
	if err := d.SerializeTBN(&buf1); err != nil {
		t.Fatalf("SerializeTBN (first): %v", err)
	}
	bytes1 := buf1.Bytes()
	if len(bytes1) == 0 {
		t.Fatalf("SerializeTBN produced empty output")
	}

	// Step 2: LoadTBN → doc2.
	doc2, err := LoadTBN(bytes.NewReader(bytes1))
	if err != nil {
		t.Fatalf("LoadTBN: %v", err)
	}
	if doc2.LineCount() == 0 {
		t.Fatalf("LoadTBN returned empty document")
	}

	// Step 3: doc2.SerializeTBN → bytes2.
	var buf2 bytes.Buffer
	if err := doc2.SerializeTBN(&buf2); err != nil {
		t.Fatalf("SerializeTBN (second): %v", err)
	}
	bytes2 := buf2.Bytes()

	// Step 4: bytes1 == bytes2 — the .tbn is byte-stable across the
	// canonical-text round-trip.
	if !bytes.Equal(bytes1, bytes2) {
		t.Fatalf(".tbn round-trip not byte-stable:\n  len(bytes1)=%d\n  len(bytes2)=%d",
			len(bytes1), len(bytes2))
	}

	// Step 5: re-loading bytes2 gives the same line texts as doc2.
	doc3, err := LoadTBN(bytes.NewReader(bytes2))
	if err != nil {
		t.Fatalf("LoadTBN (bytes2): %v", err)
	}
	if doc3.LineCount() != doc2.LineCount() {
		t.Fatalf("re-loaded doc has %d lines, want %d", doc3.LineCount(), doc2.LineCount())
	}
	for i := 0; i < doc2.LineCount(); i++ {
		_, text2 := doc2.LineAt(i)
		_, text3 := doc3.LineAt(i)
		if !bytes.Equal(text2, text3) {
			t.Fatalf("re-loaded line %d mismatch:\n  doc2: %q\n  doc3: %q", i, text2, text3)
		}
	}
}

// TestSerializeTBNInvalidFailsLoud confirms SerializeTBN returns a non-nil
// error for syntactically-invalid assembly rather than silently producing
// garbage bytes. Failing loud is the correct behaviour (design §7.3).
func TestSerializeTBNInvalidFailsLoud(t *testing.T) {
	d := New()
	d.InsertLine(0, []byte("this is not valid asm @@@"))
	var buf bytes.Buffer
	err := d.SerializeTBN(&buf)
	if err == nil {
		t.Fatalf("SerializeTBN should return an error for invalid assembly, got nil")
	}
}

// TestLoadTBNFromSerialize verifies the round-trip: Document → SerializeTBN →
// LoadTBN → SerializeTBN produces byte-identical output to the first serialize.
func TestLoadTBNFromSerialize(t *testing.T) {
	d := New()
	lines := splitLines(validAsmSource)
	for _, l := range lines {
		d.InsertLine(d.LineCount(), []byte(l))
	}

	var buf1 bytes.Buffer
	if err := d.SerializeTBN(&buf1); err != nil {
		t.Fatalf("SerializeTBN: %v", err)
	}

	// Load, re-serialize and compare.
	d2, err := LoadTBN(bytes.NewReader(buf1.Bytes()))
	if err != nil {
		t.Fatalf("LoadTBN: %v", err)
	}
	if d2.LineCount() == 0 {
		t.Fatalf("LoadTBN returned empty document")
	}

	var buf2 bytes.Buffer
	if err := d2.SerializeTBN(&buf2); err != nil {
		t.Fatalf("SerializeTBN (reloaded): %v", err)
	}
	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Fatalf("LoadTBN→SerializeTBN not byte-stable with the input")
	}
}

// splitLines splits a source string into individual lines, stripping the
// trailing newline from each, and skipping the final empty string that results
// from a trailing newline.
func splitLines(src string) []string {
	var out []string
	start := 0
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			out = append(out, src[start:i])
			start = i + 1
		}
	}
	// Don't append a final empty line if the source ends with '\n'.
	if start < len(src) {
		out = append(out, src[start:])
	}
	return out
}
