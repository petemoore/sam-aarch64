package main

import "testing"

// taken builds a set from a list of ids — convenience for nextSubID tests.
func taken(ids ...string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// TestNextSubID_PureBaseToLetter: a pure top-level iN gets the first free letter.
func TestNextSubID_PureBaseToLetter(t *testing.T) {
	got, err := nextSubID("i5", taken())
	if err != nil {
		t.Fatalf("nextSubID(i5): unexpected error: %v", err)
	}
	if got != "i5a" {
		t.Errorf("nextSubID(i5): got %q, want i5a", got)
	}
}

// TestNextSubID_LetterAlreadyTaken: with i5a taken, the next free letter is i5b.
func TestNextSubID_LetterAlreadyTaken(t *testing.T) {
	got, err := nextSubID("i5", taken("i5a"))
	if err != nil {
		t.Fatalf("nextSubID(i5) with i5a taken: unexpected error: %v", err)
	}
	if got != "i5b" {
		t.Errorf("nextSubID(i5) with i5a taken: got %q, want i5b", got)
	}
}

// TestNextSubID_LetterChildToBrick: a letter-child iNx descends to a numbered
// brick (iNx-b1), unbounded — this is the escape hatch under a sub-item.
func TestNextSubID_LetterChildToBrick(t *testing.T) {
	got, err := nextSubID("i5a", taken())
	if err != nil {
		t.Fatalf("nextSubID(i5a): unexpected error: %v", err)
	}
	if got != "i5a-b1" {
		t.Errorf("nextSubID(i5a): got %q, want i5a-b1", got)
	}

	// With i5a-b1 taken, the next brick is i5a-b2.
	got2, err := nextSubID("i5a", taken("i5a-b1"))
	if err != nil {
		t.Fatalf("nextSubID(i5a) with i5a-b1 taken: unexpected error: %v", err)
	}
	if got2 != "i5a-b2" {
		t.Errorf("nextSubID(i5a) with i5a-b1 taken: got %q, want i5a-b2", got2)
	}
}

// TestNextSubID_BrickAppendsLetter: a brick (iN-bM or iNx-bM) appends a letter.
func TestNextSubID_BrickAppendsLetter(t *testing.T) {
	// Brick under a top-level base: i5-b3 → i5-b3a.
	got, err := nextSubID("i5-b3", taken())
	if err != nil {
		t.Fatalf("nextSubID(i5-b3): unexpected error: %v", err)
	}
	if got != "i5-b3a" {
		t.Errorf("nextSubID(i5-b3): got %q, want i5-b3a", got)
	}

	// Brick under a letter-child: i5a-b3 → i5a-b3a.
	got2, err := nextSubID("i5a-b3", taken())
	if err != nil {
		t.Fatalf("nextSubID(i5a-b3): unexpected error: %v", err)
	}
	if got2 != "i5a-b3a" {
		t.Errorf("nextSubID(i5a-b3): got %q, want i5a-b3a", got2)
	}

	// With the first brick-letter taken, advance to the next.
	got3, err := nextSubID("i5-b3", taken("i5-b3a"))
	if err != nil {
		t.Fatalf("nextSubID(i5-b3) with i5-b3a taken: unexpected error: %v", err)
	}
	if got3 != "i5-b3b" {
		t.Errorf("nextSubID(i5-b3) with i5-b3a taken: got %q, want i5-b3b", got3)
	}
}

// TestNextSubID_DeepestLevelErrors: a brick-part iN-bMy is the deepest grammar
// level — there is no further level, so nextSubID returns an error.
func TestNextSubID_DeepestLevelErrors(t *testing.T) {
	if _, err := nextSubID("i5-b3a", taken()); err == nil {
		t.Error("nextSubID(i5-b3a): expected max-nesting error, got nil")
	}
	if _, err := nextSubID("i5a-b3a", taken()); err == nil {
		t.Error("nextSubID(i5a-b3a): expected max-nesting error, got nil")
	}
}

// TestNextSubID_All26LettersExhausted: when every a–z slot under a base is taken,
// nextSubID returns a loud error rather than overflowing.
func TestNextSubID_All26LettersExhausted(t *testing.T) {
	full := map[string]bool{}
	for c := byte('a'); c <= 'z'; c++ {
		full["i5"+string(c)] = true
	}
	if _, err := nextSubID("i5", full); err == nil {
		t.Error("nextSubID(i5) with all 26 letters taken: expected error, got nil")
	}
}

// TestNextSubID_RespectsTakenSet: a since-deleted id present only in the taken set
// (e.g. the ledger) is not re-minted — nextSubID skips it.
func TestNextSubID_RespectsTakenSet(t *testing.T) {
	// i5a was minted then deleted (still in the ledger); the next free letter is i5b.
	got, err := nextSubID("i5", taken("i5a"))
	if err != nil {
		t.Fatalf("nextSubID(i5) respecting taken: unexpected error: %v", err)
	}
	if got == "i5a" {
		t.Error("nextSubID re-minted a since-deleted id (i5a) that is in the taken set")
	}
	if got != "i5b" {
		t.Errorf("nextSubID(i5) with i5a in taken set: got %q, want i5b", got)
	}
}

// TestNextSubID_MalformedParentErrors: a parent id that does not match the item
// grammar is rejected.
func TestNextSubID_MalformedParentErrors(t *testing.T) {
	if _, err := nextSubID("x99", taken()); err == nil {
		t.Error("nextSubID(x99): expected grammar error for malformed parent, got nil")
	}
}
