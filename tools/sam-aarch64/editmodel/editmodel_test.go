package editmodel

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// mustInvariants calls checkInvariants and fails the test on any violation.
func mustInvariants(t *testing.T, d *Document) {
	t.Helper()
	if err := d.checkInvariants(); err != nil {
		t.Fatalf("invariant violation: %v", err)
	}
}

func TestEmptyDocument(t *testing.T) {
	d := New()
	if d.LineCount() != 0 {
		t.Fatalf("empty doc: want LineCount 0, got %d", d.LineCount())
	}
	rendered := d.Render(0, 10)
	if len(rendered) != 0 {
		t.Fatalf("empty doc: Render should return empty, got %d lines", len(rendered))
	}
	mustInvariants(t, d)
}

func TestEmptyDocumentSerializeLoad(t *testing.T) {
	d := New()
	var buf bytes.Buffer
	if err := d.Serialize(&buf); err != nil {
		t.Fatalf("Serialize empty: %v", err)
	}
	// Snapshot bytes before Load consumes the reader.
	serialized1 := buf.Bytes()
	d2, err := Load(bytes.NewReader(serialized1))
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if d2.LineCount() != 0 {
		t.Fatalf("Load empty: want 0 lines, got %d", d2.LineCount())
	}
	mustInvariants(t, d2)

	// Second serialize must be byte-identical.
	var buf2 bytes.Buffer
	if err := d2.Serialize(&buf2); err != nil {
		t.Fatalf("Serialize reloaded empty: %v", err)
	}
	if !bytes.Equal(serialized1, buf2.Bytes()) {
		t.Fatalf("empty doc: serialize→load→serialize not byte-stable")
	}
}

func TestSingleInsertLineAtIndexOf(t *testing.T) {
	d := New()
	id := d.InsertLine(0, []byte("hello world"))
	if d.LineCount() != 1 {
		t.Fatalf("want 1 line, got %d", d.LineCount())
	}
	gotID, gotText := d.LineAt(0)
	if gotID != id {
		t.Fatalf("LineAt(0): want id %d, got %d", id, gotID)
	}
	if string(gotText) != "hello world" {
		t.Fatalf("LineAt(0): want text %q, got %q", "hello world", gotText)
	}
	idx, ok := d.IndexOf(id)
	if !ok || idx != 0 {
		t.Fatalf("IndexOf(%d): want (0, true), got (%d, %v)", id, idx, ok)
	}
	mustInvariants(t, d)
}

func TestInsertOrder(t *testing.T) {
	d := New()
	lines := []string{"alpha", "beta", "gamma", "delta"}

	// Insert at top repeatedly — each insert prepends, so the last inserted
	// line ends up at index 0. Final order: delta, gamma, beta, alpha.
	for _, s := range lines {
		d.InsertLine(0, []byte(s))
	}
	expected := []string{"delta", "gamma", "beta", "alpha"}
	if d.LineCount() != len(expected) {
		t.Fatalf("want %d lines, got %d", len(expected), d.LineCount())
	}
	for i, want := range expected {
		_, got := d.LineAt(i)
		if string(got) != want {
			t.Fatalf("line %d: want %q, got %q", i, want, got)
		}
	}
	mustInvariants(t, d)
}

func TestInsertAtMiddleAndEnd(t *testing.T) {
	d := New()
	d.InsertLine(0, []byte("A"))
	d.InsertLine(1, []byte("C"))
	d.InsertLine(1, []byte("B")) // insert before C

	want := []string{"A", "B", "C"}
	for i, w := range want {
		_, got := d.LineAt(i)
		if string(got) != w {
			t.Fatalf("line %d: want %q, got %q", i, w, got)
		}
	}
	mustInvariants(t, d)
}

func TestDeleteLine(t *testing.T) {
	d := New()
	id0 := d.InsertLine(0, []byte("first"))
	id1 := d.InsertLine(1, []byte("second"))
	id2 := d.InsertLine(2, []byte("third"))

	deleted := d.DeleteLine(1) // delete "second"
	if deleted != id1 {
		t.Fatalf("DeleteLine: want id %d, got %d", id1, deleted)
	}
	if d.LineCount() != 2 {
		t.Fatalf("want 2 lines, got %d", d.LineCount())
	}
	_, t0 := d.LineAt(0)
	_, t1 := d.LineAt(1)
	if string(t0) != "first" || string(t1) != "third" {
		t.Fatalf("after delete: want [first, third], got [%s, %s]", t0, t1)
	}
	// Deleted id should not be findable.
	if _, ok := d.IndexOf(id1); ok {
		t.Fatalf("deleted id %d should not be found by IndexOf", id1)
	}
	// Surviving ids still work.
	for _, id := range []RecordID{id0, id2} {
		if _, ok := d.IndexOf(id); !ok {
			t.Fatalf("live id %d not found by IndexOf", id)
		}
	}
	mustInvariants(t, d)
}

func TestDeleteRemovesBlock(t *testing.T) {
	d := New()
	id := d.InsertLine(0, []byte("only"))
	if len(d.blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(d.blocks))
	}
	d.DeleteLine(0)
	if d.LineCount() != 0 {
		t.Fatalf("want 0 lines, got %d", d.LineCount())
	}
	if len(d.blocks) != 0 {
		t.Fatalf("want 0 blocks after last line deleted, got %d", len(d.blocks))
	}
	if _, ok := d.IndexOf(id); ok {
		t.Fatalf("deleted id %d should not be found", id)
	}
	mustInvariants(t, d)
}

// makeLine generates a line of approximately size bytes for split/merge tests.
func makeLine(i, size int) []byte {
	s := fmt.Sprintf("line%04d:", i)
	pad := size - len(s)
	if pad < 0 {
		pad = 0
	}
	return []byte(s + strings.Repeat("x", pad))
}

func TestForcedSplit(t *testing.T) {
	d := New()
	// Insert enough ~40-byte lines to exceed 8 KB in one block, forcing a split.
	// Each line costs ~40+5=45 bytes; 8192/45 ≈ 182 lines to fill one block.
	// Insert 250 lines to be sure.
	const n = 250
	for i := 0; i < n; i++ {
		d.InsertLine(d.LineCount(), makeLine(i, 40))
	}
	if len(d.blocks) < 2 {
		t.Fatalf("expected ≥2 blocks after forced split, got %d", len(d.blocks))
	}
	if d.LineCount() != n {
		t.Fatalf("want %d lines, got %d", n, d.LineCount())
	}
	// Verify order is preserved.
	for i := 0; i < n; i++ {
		_, got := d.LineAt(i)
		want := makeLine(i, 40)
		if !bytes.Equal(got, want) {
			t.Fatalf("line %d: want %q, got %q", i, want, got)
		}
	}
	mustInvariants(t, d)
}

func TestForcedMerge(t *testing.T) {
	d := New()
	// Fill up to cause a split (250 lines).
	const n = 250
	for i := 0; i < n; i++ {
		d.InsertLine(d.LineCount(), makeLine(i, 40))
	}
	blocksBefore := len(d.blocks)
	if blocksBefore < 2 {
		t.Fatalf("setup: expected ≥2 blocks, got %d", blocksBefore)
	}

	// Delete most lines from the last block, leaving it underflowing.
	lastBlockLines := len(d.blocks[len(d.blocks)-1].lines)
	keepInLast := 2
	for i := 0; i < lastBlockLines-keepInLast; i++ {
		d.DeleteLine(d.LineCount() - 1)
	}

	blocksAfter := len(d.blocks)
	if blocksAfter >= blocksBefore {
		t.Fatalf("expected block count to drop after merges, before=%d after=%d", blocksBefore, blocksAfter)
	}
	mustInvariants(t, d)
}

func TestGotoByIDStableAfterInserts(t *testing.T) {
	d := New()
	id0 := d.InsertLine(0, []byte("base"))
	// Insert many lines before id0's position.
	for i := 0; i < 50; i++ {
		d.InsertLine(0, []byte("prefix"))
	}
	idx, ok := d.IndexOf(id0)
	if !ok {
		t.Fatalf("id0 not found after inserts")
	}
	if idx != 50 {
		t.Fatalf("id0 should be at index 50 after 50 prepends, got %d", idx)
	}
	mustInvariants(t, d)
}

func TestIndexOfDeletedID(t *testing.T) {
	d := New()
	id := d.InsertLine(0, []byte("bye"))
	d.DeleteLine(0)
	if _, ok := d.IndexOf(id); ok {
		t.Fatalf("deleted id %d should not be found", id)
	}
}

func TestRenderAcrossBlockBoundary(t *testing.T) {
	d := New()
	// Build enough lines to guarantee a split.
	const n = 250
	for i := 0; i < n; i++ {
		d.InsertLine(d.LineCount(), makeLine(i, 40))
	}
	if len(d.blocks) < 2 {
		t.Fatalf("precondition: need ≥2 blocks")
	}
	// Find the last line of block 0 and render 3 lines spanning the boundary.
	boundary := len(d.blocks[0].lines) - 1
	rendered := d.Render(boundary, 3)
	if len(rendered) != 3 {
		t.Fatalf("Render across boundary: want 3 lines, got %d", len(rendered))
	}
	for i, rl := range rendered {
		want := makeLine(boundary+i, 40)
		if !bytes.Equal(rl.Text, want) {
			t.Fatalf("Render line %d: want %q, got %q", i, want, rl.Text)
		}
	}
}

func TestSerializeLoadByteStability(t *testing.T) {
	d := New()
	lines := []string{"mov x0, #1", "add x1, x0, x2", "bl foo", "ret"}
	for _, l := range lines {
		d.InsertLine(d.LineCount(), []byte(l))
	}

	var buf1 bytes.Buffer
	if err := d.Serialize(&buf1); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	d2, err := Load(bytes.NewReader(buf1.Bytes()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	mustInvariants(t, d2)

	var buf2 bytes.Buffer
	if err := d2.Serialize(&buf2); err != nil {
		t.Fatalf("Serialize reloaded: %v", err)
	}
	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Fatalf("serialize→load→serialize not byte-stable")
	}
	// All lines must match.
	for i, want := range lines {
		_, got := d2.LineAt(i)
		if string(got) != want {
			t.Fatalf("reloaded line %d: want %q, got %q", i, want, got)
		}
	}
}

func TestSerializeLineTooLong(t *testing.T) {
	d := New()
	// Insert a line longer than 65535 bytes.
	longLine := make([]byte, 65536)
	d.InsertLine(0, longLine)
	var buf bytes.Buffer
	err := d.Serialize(&buf)
	if err == nil {
		t.Fatalf("expected error for line >65535 bytes, got nil")
	}
}

func TestCheckInvariantsHasTeeth(t *testing.T) {
	d := New()
	id := d.InsertLine(0, []byte("line"))
	mustInvariants(t, d)

	// Corrupt loc: point the id at a freshly-allocated block not in d.blocks.
	d.loc[id] = &block{}
	err := d.checkInvariants()
	if err == nil {
		t.Fatalf("checkInvariants should catch loc pointing to a block not in d.blocks")
	}

	// Restore and add a bogus stale entry pointing at an existing block.
	d.loc[id] = d.blocks[0]
	d.loc[RecordID(9999)] = d.blocks[0] // stale entry for a non-existent id
	err = d.checkInvariants()
	if err == nil {
		t.Fatalf("checkInvariants should catch stale loc entry")
	}
}
