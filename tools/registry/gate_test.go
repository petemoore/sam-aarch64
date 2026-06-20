package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn while redirecting os.Stdout to a pipe, and returns
// everything fn printed. The query commands (ready, in-progress) write their
// results to stdout via fmt.Println, so a test must capture it to assert.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()

	defer func() {
		os.Stdout = orig
	}()
	fn()
	w.Close()
	out := <-done
	r.Close()
	return out
}

// nonEmptyLines splits s into lines, dropping empty ones.
func nonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

// --- runInProgress ---

// TestInProgress_ListsInProgressItems asserts that `in-progress` prints exactly
// the IN_PROGRESS items, in priority order. We build a queue [i2, i1b] and mark
// both IN_PROGRESS (so order is observable) while a third item stays OPEN.
func TestInProgress_ListsInProgressItems(t *testing.T) {
	// Queue order [i2, i1b] (both pullable leaves in the fixture).
	paths := setupMutatorFixtureWithPriority(t, []string{"i2", "i1b"})

	// Mark i2 and i1b IN_PROGRESS; leave the OPEN/closed others alone.
	runSetStatus([]string{"--id", "i2", "--status", "IN_PROGRESS"}, paths)
	runSetStatus([]string{"--id", "i1b", "--status", "IN_PROGRESS"}, paths)

	out := captureStdout(t, func() { runInProgress(nil, paths) })
	got := nonEmptyLines(out)

	// Priority order is [i2, i1b]; both are IN_PROGRESS → that exact order.
	want := []string{"i2", "i1b"}
	if !equalStringSlice(got, want) {
		t.Errorf("in-progress: got %v, want %v (priority order)", got, want)
	}
}

// TestInProgress_ExcludesNonInProgress asserts that `in-progress` lists ONLY the
// IN_PROGRESS items — an OPEN sibling does not appear.
func TestInProgress_ExcludesNonInProgress(t *testing.T) {
	paths := setupMutatorFixtureWithPriority(t, []string{"i2", "i1b"})

	// Only i1b is IN_PROGRESS; i2 stays OPEN.
	runSetStatus([]string{"--id", "i1b", "--status", "IN_PROGRESS"}, paths)

	out := captureStdout(t, func() { runInProgress(nil, paths) })
	got := nonEmptyLines(out)

	want := []string{"i1b"}
	if !equalStringSlice(got, want) {
		t.Errorf("in-progress: got %v, want %v (only the IN_PROGRESS item)", got, want)
	}
}

// --- runReady excludes IN_PROGRESS ---

// TestReady_ExcludesInProgress asserts that `ready` returns only not-yet-started
// (OPEN) pullable items: an IN_PROGRESS item is absent while an OPEN, fully-satisfied
// sibling is present.
//
// Fixture: i1b is OPEN and depends_on i1a (DONE) → ready. We mark i1b IN_PROGRESS
// and add a fresh OPEN item with no deps; ready must list the OPEN item but NOT i1b.
func TestReady_ExcludesInProgress(t *testing.T) {
	paths := setupMutatorFixtureWithPriority(t, []string{"i1b", "i2"})

	// Add a fresh OPEN, dep-free item (auto-allocated id). It auto-joins the queue.
	openID := expectedNextItemID(t, paths)
	runAdd([]string{
		"--title", "Open dep-free sibling",
		"--desc", "A ready item that should appear in `ready`.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	// Mark i1b IN_PROGRESS — it must drop out of `ready` (already being worked).
	runSetStatus([]string{"--id", "i1b", "--status", "IN_PROGRESS"}, paths)

	out := captureStdout(t, func() { runReady(nil, paths) })
	got := nonEmptyLines(out)

	// The OPEN sibling must be present.
	if !containsStr(got, openID) {
		t.Errorf("ready: expected OPEN item %s to be present; got %v", openID, got)
	}
	// The IN_PROGRESS item must be absent.
	if containsStr(got, "i1b") {
		t.Errorf("ready: IN_PROGRESS item i1b must be excluded; got %v", got)
	}
	// i2 depends on the open question q1 → still not ready.
	if containsStr(got, "i2") {
		t.Errorf("ready: i2 (gated on open question q1) must be excluded; got %v", got)
	}
}

// --- computeGates ---

// TestComputeGates_OpenQuestionLocksTransitively builds a registry where an item
// directly depends on an open question, and a second item depends on the first.
// Both must show the 🔒 qN gate (the lock propagates transitively to dependents).
func TestComputeGates_OpenQuestionLocksTransitively(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			// i1 directly gated on q1.
			{ID: "i1", Title: "gated on q1", Status: StatusOpen, Kind: "leaf", Owner: "agent",
				DependsOn: []string{"q1"}, Refs: []string{"q1"}},
			// i2 gated on i1 (open) → transitively rooted in q1.
			{ID: "i2", Title: "gated on i1", Status: StatusOpen, Kind: "leaf", Owner: "agent",
				DependsOn: []string{"i1"}},
		},
		Questions: []Question{{ID: "q1", Body: "decide?", Owner: "pete"}},
	}
	gates := computeGates(reg)

	if gates["i1"] != "🔒 q1" {
		t.Errorf("i1 gate: got %q, want %q", gates["i1"], "🔒 q1")
	}
	if gates["i2"] != "🔒 q1" {
		t.Errorf("i2 gate (transitive lock): got %q, want %q", gates["i2"], "🔒 q1")
	}
}

// TestComputeGates_MultipleQuestionsSorted asserts that an item rooted in more
// than one open question lists them sorted, comma-separated.
func TestComputeGates_MultipleQuestionsSorted(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "gated on q24 and q31", Status: StatusOpen, Kind: "leaf", Owner: "agent",
				DependsOn: []string{"q31", "q24"}, Refs: []string{"q24", "q31"}},
		},
		Questions: []Question{
			{ID: "q24", Body: "a?", Owner: "pete"},
			{ID: "q31", Body: "b?", Owner: "pete"},
		},
	}
	gates := computeGates(reg)
	if gates["i1"] != "🔒 q24, q31" {
		t.Errorf("i1 gate: got %q, want %q (sorted)", gates["i1"], "🔒 q24, q31")
	}
}

// TestComputeGates_InProgress asserts an IN_PROGRESS item shows the 🛠 gate.
func TestComputeGates_InProgress(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "being worked", Status: StatusInProgress, Kind: "leaf", Owner: "agent"},
		},
	}
	gates := computeGates(reg)
	if gates["i1"] != "🛠 in-progress" {
		t.Errorf("i1 gate: got %q, want %q", gates["i1"], "🛠 in-progress")
	}
}

// TestComputeGates_Ready asserts that an item whose deps are all DONE shows "ready".
func TestComputeGates_Ready(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "done dep", Status: StatusDone, Kind: "leaf", Owner: "agent",
				PRs: []PRRef{{Num: 1, Role: RoleCompleting}}},
			{ID: "i2", Title: "gated on DONE i1", Status: StatusOpen, Kind: "leaf", Owner: "agent",
				DependsOn: []string{"i1"}},
		},
	}
	gates := computeGates(reg)
	if gates["i2"] != "ready" {
		t.Errorf("i2 gate: got %q, want %q", gates["i2"], "ready")
	}
}

// TestComputeGates_BlockedOnOpenWork asserts that an item blocked only on other
// OPEN work (no open question at the root) shows the ⏳ <id> gate.
func TestComputeGates_BlockedOnOpenWork(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "open prerequisite", Status: StatusOpen, Kind: "leaf", Owner: "agent"},
			{ID: "i2", Title: "gated on open i1", Status: StatusOpen, Kind: "leaf", Owner: "agent",
				DependsOn: []string{"i1"}},
		},
	}
	gates := computeGates(reg)
	if gates["i2"] != "⏳ i1" {
		t.Errorf("i2 gate: got %q, want %q", gates["i2"], "⏳ i1")
	}
	// i1 itself is unblocked → ready.
	if gates["i1"] != "ready" {
		t.Errorf("i1 gate: got %q, want %q", gates["i1"], "ready")
	}
}
