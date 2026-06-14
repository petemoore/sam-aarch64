package editmodel

import (
	"bytes"
	"math/rand"
	"testing"
)

// logicalLine is a snapshot of a single line's identity and content used
// by undo/redo tests to compare document state against an oracle.
type logicalLine struct {
	id   RecordID
	text []byte
}

// snapshotDoc captures the current (id, text) sequence of the document.
func snapshotDoc(d *Document) []logicalLine {
	n := d.LineCount()
	snap := make([]logicalLine, n)
	for i := 0; i < n; i++ {
		id, text := d.LineAt(i)
		snap[i] = logicalLine{id: id, text: copyBytes(text)}
	}
	return snap
}

// assertSnapshotEqual fails t if the document's current logical sequence
// differs from snap (by id, text, or count).
func assertSnapshotEqual(t *testing.T, label string, d *Document, snap []logicalLine) {
	t.Helper()
	if d.LineCount() != len(snap) {
		t.Fatalf("%s: LineCount %d != snapshot len %d", label, d.LineCount(), len(snap))
	}
	for i, want := range snap {
		gotID, gotText := d.LineAt(i)
		if gotID != want.id {
			t.Fatalf("%s: line %d id mismatch: want %d got %d", label, i, want.id, gotID)
		}
		if !bytes.Equal(gotText, want.text) {
			t.Fatalf("%s: line %d text mismatch: want %q got %q", label, i, want.text, gotText)
		}
	}
}

// TestUndoInsert verifies that undoing an insert removes the inserted line and
// that CanUndo/CanRedo transition correctly.
func TestUndoInsert(t *testing.T) {
	d := New()
	if d.CanUndo() {
		t.Fatal("fresh document: CanUndo should be false")
	}
	if d.CanRedo() {
		t.Fatal("fresh document: CanRedo should be false")
	}

	before := snapshotDoc(d)

	id := d.InsertLine(0, []byte("hello"))
	if !d.CanUndo() {
		t.Fatal("after insert: CanUndo should be true")
	}
	if d.CanRedo() {
		t.Fatal("after insert: CanRedo should be false")
	}
	if d.LineCount() != 1 {
		t.Fatalf("want 1 line, got %d", d.LineCount())
	}
	gotID, gotText := d.LineAt(0)
	if gotID != id {
		t.Fatalf("LineAt(0) id mismatch: want %d got %d", id, gotID)
	}
	if string(gotText) != "hello" {
		t.Fatalf("LineAt(0) text mismatch: want %q got %q", "hello", gotText)
	}

	ok := d.Undo()
	if !ok {
		t.Fatal("Undo returned false; expected true")
	}
	if d.CanUndo() {
		t.Fatal("after undo of only edit: CanUndo should be false")
	}
	if !d.CanRedo() {
		t.Fatal("after undo: CanRedo should be true")
	}

	// Document should be back to empty.
	assertSnapshotEqual(t, "after undo of insert", d, before)
	mustInvariants(t, d)
}

// TestUndoDeleteRestoresIDAndPosition verifies that undoing a delete restores
// the line with the same RecordID (id-stability, design §3) at the same position
// with the same text.
func TestUndoDeleteRestoresIDAndPosition(t *testing.T) {
	d := New()
	d.InsertLine(0, []byte("alpha"))
	targetID := d.InsertLine(1, []byte("bravo"))
	d.InsertLine(2, []byte("charlie"))

	before := snapshotDoc(d)

	deleted := d.DeleteLine(1) // delete "bravo"
	if deleted != targetID {
		t.Fatalf("DeleteLine returned id %d; want %d", deleted, targetID)
	}
	if d.LineCount() != 2 {
		t.Fatalf("after delete: want 2 lines, got %d", d.LineCount())
	}
	// targetID must not be live.
	if _, ok := d.IndexOf(targetID); ok {
		t.Fatalf("deleted id %d still found after delete", targetID)
	}

	ok := d.Undo()
	if !ok {
		t.Fatal("Undo returned false")
	}

	// Restored line must carry the original id.
	if d.LineCount() != 3 {
		t.Fatalf("after undo of delete: want 3 lines, got %d", d.LineCount())
	}
	idx, found := d.IndexOf(targetID)
	if !found {
		t.Fatalf("after undo: id %d should be live again", targetID)
	}
	if idx != 1 {
		t.Fatalf("after undo: id %d should be at index 1, got %d", targetID, idx)
	}
	_, text := d.LineAt(1)
	if string(text) != "bravo" {
		t.Fatalf("after undo: line 1 text want %q got %q", "bravo", text)
	}

	assertSnapshotEqual(t, "after undo of delete", d, before)
	mustInvariants(t, d)
}

// TestRedoInsert verifies that Redo re-applies an undone insert.
func TestRedoInsert(t *testing.T) {
	d := New()
	id := d.InsertLine(0, []byte("redo-me"))
	afterInsert := snapshotDoc(d)

	d.Undo()
	if d.LineCount() != 0 {
		t.Fatalf("after undo: want 0 lines, got %d", d.LineCount())
	}

	ok := d.Redo()
	if !ok {
		t.Fatal("Redo returned false")
	}
	if d.LineCount() != 1 {
		t.Fatalf("after redo: want 1 line, got %d", d.LineCount())
	}
	// The re-inserted line must have the original id.
	idx, found := d.IndexOf(id)
	if !found {
		t.Fatalf("after redo: id %d not found", id)
	}
	if idx != 0 {
		t.Fatalf("after redo: id %d should be at 0, got %d", id, idx)
	}
	assertSnapshotEqual(t, "after redo of insert", d, afterInsert)
	mustInvariants(t, d)
}

// TestRedoDelete verifies that Redo re-applies an undone delete.
func TestRedoDelete(t *testing.T) {
	d := New()
	id := d.InsertLine(0, []byte("delete-me"))
	d.DeleteLine(0)
	afterDelete := snapshotDoc(d) // empty

	d.Undo() // restore the line
	if d.LineCount() != 1 {
		t.Fatalf("after undo of delete: want 1 line, got %d", d.LineCount())
	}
	if _, ok := d.IndexOf(id); !ok {
		t.Fatalf("after undo of delete: id %d not found", id)
	}

	ok := d.Redo()
	if !ok {
		t.Fatal("Redo returned false")
	}
	assertSnapshotEqual(t, "after redo of delete", d, afterDelete)
	if _, ok := d.IndexOf(id); ok {
		t.Fatalf("after redo of delete: id %d should be dead", id)
	}
	mustInvariants(t, d)
}

// TestUndoPastBeginning verifies that Undo returns false when the stack is empty.
func TestUndoPastBeginning(t *testing.T) {
	d := New()
	d.InsertLine(0, []byte("one"))
	d.Undo()
	if d.Undo() {
		t.Fatal("Undo past empty stack should return false")
	}
	if d.CanUndo() {
		t.Fatal("CanUndo should be false after exhausting stack")
	}
	mustInvariants(t, d)
}

// TestRedoPastTop verifies that Redo returns false when the redo stack is empty.
func TestRedoPastTop(t *testing.T) {
	d := New()
	d.InsertLine(0, []byte("one"))
	if d.Redo() {
		t.Fatal("Redo with empty redo stack should return false")
	}
	d.Undo()
	d.Redo()
	if d.Redo() {
		t.Fatal("Redo past top should return false")
	}
	if d.CanRedo() {
		t.Fatal("CanRedo should be false after exhausting redo stack")
	}
	mustInvariants(t, d)
}

// TestFreshEditClearsRedo verifies that making a new public edit after some
// undos clears the redo stack.
func TestFreshEditClearsRedo(t *testing.T) {
	d := New()
	d.InsertLine(0, []byte("a"))
	d.InsertLine(1, []byte("b"))
	d.InsertLine(2, []byte("c"))

	// Undo twice — redo stack should have 2 entries.
	d.Undo()
	d.Undo()
	if !d.CanRedo() {
		t.Fatal("expected CanRedo true after two undos")
	}

	// Fresh insert clears redo.
	d.InsertLine(1, []byte("new"))
	if d.CanRedo() {
		t.Fatal("CanRedo should be false after a fresh insert")
	}
	if d.Redo() {
		t.Fatal("Redo should return false after fresh insert cleared the stack")
	}
	mustInvariants(t, d)
}

// TestUndoRedoRingCap verifies bounded ring semantics: after maxUndoDepth+10
// inserts, Undo is possible exactly maxUndoDepth times then returns false, and
// the resulting state equals the state after the first 10 inserts.
func TestUndoRedoRingCap(t *testing.T) {
	d := New()
	const extra = 10
	total := maxUndoDepth + extra

	// Snapshot after the first `extra` inserts (the oldest entries that will be
	// dropped once the cap is exceeded).
	var snapAfterFirst10 []logicalLine
	for i := 0; i < total; i++ {
		text := []byte{byte('a' + i%26), byte('0' + i%10)}
		d.InsertLine(d.LineCount(), text)
		if i == extra-1 {
			snapAfterFirst10 = snapshotDoc(d)
		}
	}

	if d.LineCount() != total {
		t.Fatalf("want %d lines, got %d", total, d.LineCount())
	}

	// Undo exactly maxUndoDepth times — all must succeed.
	for i := 0; i < maxUndoDepth; i++ {
		if !d.Undo() {
			t.Fatalf("Undo %d of %d returned false (expected true)", i+1, maxUndoDepth)
		}
	}

	// The next Undo must fail (cap dropped the oldest 10).
	if d.Undo() {
		t.Fatal("Undo returned true past cap; expected false")
	}
	if d.CanUndo() {
		t.Fatal("CanUndo should be false after exhausting capped stack")
	}

	// The document must now equal the state after the first 10 inserts.
	assertSnapshotEqual(t, "after ring-cap undos", d, snapAfterFirst10)
	mustInvariants(t, d)
}

// TestInvariantsHoldAfterUndoRedo verifies that checkInvariants passes after a
// sequence of insert/delete/undo/redo operations.
func TestInvariantsHoldAfterUndoRedo(t *testing.T) {
	d := New()

	// Insert several lines.
	d.InsertLine(0, []byte("line0"))
	d.InsertLine(1, []byte("line1"))
	d.InsertLine(2, []byte("line2"))
	mustInvariants(t, d)

	// Undo all.
	d.Undo()
	mustInvariants(t, d)
	d.Undo()
	mustInvariants(t, d)
	d.Undo()
	mustInvariants(t, d)

	// Redo all.
	d.Redo()
	mustInvariants(t, d)
	d.Redo()
	mustInvariants(t, d)
	d.Redo()
	mustInvariants(t, d)

	// Delete the middle line, undo, redo.
	d.DeleteLine(1)
	mustInvariants(t, d)
	d.Undo()
	mustInvariants(t, d)
	d.Redo()
	mustInvariants(t, d)
}

// TestPropertyUndoRedo is a property test that builds a reference state
// history of M random edits (M ≤ maxUndoDepth, so no entries are dropped)
// then performs a random walk of Undo/Redo and asserts the document exactly
// matches the corresponding snapshot at each step.
//
// The test also verifies:
//   - Undo returns true iff cursor > 0 (non-empty undo stack).
//   - Redo returns true iff cursor < M (non-empty redo stack).
//   - Fully undoing to cursor 0 yields an empty document.
//   - Fully redoing back to cursor M restores the final state.
//   - checkInvariants passes at every step.
//   - RecordID stability: after undo-of-delete the restored id equals the
//     oracle-recorded id.
func TestPropertyUndoRedo(t *testing.T) {
	seeds := []int64{1001, 2002, 3003, 4004, 5005}
	for _, seed := range seeds {
		seed := seed
		t.Run("seed"+itoa(seed), func(t *testing.T) {
			runUndoRedoSeed(t, seed)
		})
	}
}

func runUndoRedoSeed(t *testing.T, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))

	// Choose M edits, capped at maxUndoDepth so no history is dropped.
	M := maxUndoDepth/2 + rng.Intn(maxUndoDepth/2+1)

	d := New()

	// states[i] is the logical sequence after the i-th edit (states[0] = empty).
	states := make([][]logicalLine, M+1)
	states[0] = snapshotDoc(d)

	for i := 1; i <= M; i++ {
		lc := d.LineCount()
		if lc == 0 || rng.Intn(10) < 6 {
			// Insert.
			at := rng.Intn(lc + 1)
			text := randomLine(rng, 5, 30)
			d.InsertLine(at, text)
		} else {
			// Delete.
			at := rng.Intn(lc)
			d.DeleteLine(at)
		}
		states[i] = snapshotDoc(d)
	}

	// Random walk of Undo/Redo. cursor tracks which state the document should
	// be in.
	cursor := M
	const walkSteps = 3000
	for step := 0; step < walkSteps; step++ {
		var ok bool
		if rng.Intn(2) == 0 {
			// Try Undo.
			ok = d.Undo()
			wantOk := cursor > 0
			if ok != wantOk {
				t.Fatalf("seed %d step %d: Undo returned %v; cursor=%d wantOk=%v",
					seed, step, ok, cursor, wantOk)
			}
			if ok {
				cursor--
			}
		} else {
			// Try Redo.
			ok = d.Redo()
			wantOk := cursor < M
			if ok != wantOk {
				t.Fatalf("seed %d step %d: Redo returned %v; cursor=%d wantOk=%v",
					seed, step, ok, cursor, wantOk)
			}
			if ok {
				cursor++
			}
		}

		// Assert document matches oracle state.
		assertSnapshotEqual(t,
			"seed"+itoa(seed)+" step"+itoa(int64(step)),
			d, states[cursor])

		// Assert structural invariants.
		if err := d.checkInvariants(); err != nil {
			t.Fatalf("seed %d step %d: invariant violation: %v", seed, step, err)
		}
	}

	// Fully undo to the beginning.
	for cursor > 0 {
		if !d.Undo() {
			t.Fatalf("seed %d: Undo returned false at cursor %d (expected true)", seed, cursor)
		}
		cursor--
	}
	// cursor == 0 → empty document.
	if d.LineCount() != 0 {
		t.Fatalf("seed %d: after full undo: want 0 lines, got %d", seed, d.LineCount())
	}
	assertSnapshotEqual(t, "seed"+itoa(seed)+" full-undo", d, states[0])

	// Fully redo back to states[M].
	for cursor < M {
		if !d.Redo() {
			t.Fatalf("seed %d: Redo returned false at cursor %d (expected true)", seed, cursor)
		}
		cursor++
	}
	assertSnapshotEqual(t, "seed"+itoa(seed)+" full-redo", d, states[M])

	if err := d.checkInvariants(); err != nil {
		t.Fatalf("seed %d: final invariant violation: %v", seed, err)
	}
}
