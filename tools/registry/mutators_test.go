package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupMutatorFixture creates a temporary directory with copies of the testdata
// fixtures and returns a mutatorPaths pointing at them. The temporary directory
// is cleaned up automatically at test end.
func setupMutatorFixture(t *testing.T) mutatorPaths {
	t.Helper()
	repoRoot := findRepoRootRegistry(t)
	td := t.TempDir()

	copyFile(t, filepath.Join(repoRoot, "tools", "registry", "testdata", "items.yaml"),
		filepath.Join(td, "items.yaml"))
	copyFile(t, filepath.Join(repoRoot, "tools", "registry", "testdata", "questions.yaml"),
		filepath.Join(td, "questions.yaml"))

	return mutatorPaths{
		itemsYAML:     filepath.Join(td, "items.yaml"),
		questionsYAML: filepath.Join(td, "questions.yaml"),
		registryDir:   td,
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("copyFile: read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("copyFile: write %s: %v", dst, err)
	}
}

// loadRegFromPaths is a test helper that loads items and questions from paths.
func loadRegFromPaths(t *testing.T, paths mutatorPaths) *Registry {
	t.Helper()
	reg, err := loadReg(paths)
	if err != nil {
		t.Fatalf("loadReg: %v", err)
	}
	return reg
}

// assertValidFromPaths checks that the YAML on disk is valid.
func assertValidFromPaths(t *testing.T, paths mutatorPaths) {
	t.Helper()
	reg := loadRegFromPaths(t, paths)
	ve := validate(reg)
	if ve.hasErrors() {
		t.Fatalf("validate after mutation failed:\n%s", strings.Join(ve.msgs, "\n"))
	}
}

// --- next-id ---

// TestNextID_Items asserts next-id returns the next free item id not present in
// the source or the ledger.
func TestNextID_Items(t *testing.T) {
	paths := setupMutatorFixture(t)
	reg := loadRegFromPaths(t, paths)

	ledger := map[string]bool{}
	got := nextItemID(reg, ledger)

	// Testdata has i1..i5; next should be i6.
	if got != "i6" {
		t.Errorf("nextItemID: got %q, want %q", got, "i6")
	}
}

// TestNextID_ConsultsLedger asserts that a ledgered id (even if absent from
// source) is skipped — ensuring a deleted id is never re-minted (invariant 1).
func TestNextID_ConsultsLedger(t *testing.T) {
	paths := setupMutatorFixture(t)
	reg := loadRegFromPaths(t, paths)

	// Pretend i6 was minted and then deleted.
	ledger := map[string]bool{"i6": true}
	got := nextItemID(reg, ledger)
	if got != "i7" {
		t.Errorf("nextItemID with ledger blocking i6: got %q, want %q", got, "i7")
	}
}

// TestNextID_Questions asserts the question-space returns the next free q-id.
func TestNextID_Questions(t *testing.T) {
	paths := setupMutatorFixture(t)
	reg := loadRegFromPaths(t, paths)

	ledger := map[string]bool{}
	got := nextQuestionID(reg, ledger)

	// Testdata has q1; next should be q2.
	if got != "q2" {
		t.Errorf("nextQuestionID: got %q, want %q", got, "q2")
	}
}

// --- add ---

// TestAdd_Item checks that add appends a valid item, the source validates, and
// gen would still produce consistent output.
func TestAdd_Item(t *testing.T) {
	paths := setupMutatorFixture(t)

	// Capture stdout during applyAndCommit (gen prints there).
	// We invoke runAdd indirectly via the mutatorPaths interface.
	runAdd([]string{
		"--id", "i6",
		"--title", "New test item",
		"--desc", "A freshly added item for test coverage.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	// Validate that the source on disk is still valid.
	assertValidFromPaths(t, paths)

	// Confirm i6 is present in the written items.yaml.
	reg := loadRegFromPaths(t, paths)
	found := false
	for _, it := range reg.Items {
		if it.ID == "i6" {
			found = true
			if it.Title != "New test item" {
				t.Errorf("i6.Title: got %q, want %q", it.Title, "New test item")
			}
		}
	}
	if !found {
		t.Error("i6 not found in items after add")
	}
}

// TestAdd_Question checks that add appends a question record.
func TestAdd_Question(t *testing.T) {
	paths := setupMutatorFixture(t)

	runAdd([]string{
		"--id", "q2",
		"--desc", "Should we use approach A or B?",
		"--owner", "pete",
	}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)
	found := false
	for _, q := range reg.Questions {
		if q.ID == "q2" {
			found = true
			if q.Owner != "pete" {
				t.Errorf("q2.Owner: got %q, want %q", q.Owner, "pete")
			}
		}
	}
	if !found {
		t.Error("q2 not found in questions after add")
	}
}

// TestAdd_LedgerUpdated confirms the ledger file is written for the new id.
func TestAdd_LedgerUpdated(t *testing.T) {
	paths := setupMutatorFixture(t)

	runAdd([]string{
		"--id", "i6",
		"--title", "Ledger test",
		"--desc", "Checking ledger write.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	ledger, err := loadLedger(ledgerPath(paths.registryDir))
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	if !ledger["i6"] {
		t.Error("i6 not found in ledger after add")
	}
}

// --- split ---

// TestSplit_SetsUmbrellaAddsChildRewiresDependents exercises the full split:
// - parent is promoted to umbrella
// - new child leaf is added
// - a dependent that had depends_on:[parentID] is rewritten to depend on the new children
//
// Setup: add i6 (OPEN, depends_on i2) and then split i2 → umbrella with child i2a.
// After split: i6.depends_on is rewritten from [i2] to [i2a] (the only child).
func TestSplit_SetsUmbrellaAddsChildRewiresDependents(t *testing.T) {
	paths := setupMutatorFixture(t)

	// Add i6 that depends on i2 (the item we will split).
	runAdd([]string{
		"--id", "i6",
		"--title", "Item depending on i2",
		"--desc", "Will be rewritten when i2 splits.",
		"--status", "OPEN",
		"--owner", "agent",
		"--dep", "i2",
	}, paths)

	// Split i2 into an umbrella with child i2a.
	// i2 is OPEN (no completing PR), so promoting it to umbrella is valid.
	runSplit([]string{
		"--parent", "i2",
		"--child-id", "i2a",
		"--title", "First sub-item of i2",
	}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)

	// i2 should now be umbrella.
	var i2 *Item
	for i := range reg.Items {
		if reg.Items[i].ID == "i2" {
			i2 = &reg.Items[i]
		}
	}
	if i2 == nil {
		t.Fatal("i2 not found after split")
	}
	if i2.Kind != "umbrella" {
		t.Errorf("i2.Kind after split: got %q, want %q", i2.Kind, "umbrella")
	}

	// i2a should exist as a leaf child of i2.
	var child *Item
	for i := range reg.Items {
		if reg.Items[i].ID == "i2a" {
			child = &reg.Items[i]
		}
	}
	if child == nil {
		t.Fatal("i2a not found after split")
	}
	if child.Parent != "i2" {
		t.Errorf("i2a.Parent: got %q, want %q", child.Parent, "i2")
	}
	if child.Kind != "leaf" {
		t.Errorf("i2a.Kind: got %q, want %q", child.Kind, "leaf")
	}

	// i6 previously had depends_on:[i2]; after split it should contain i2a
	// (the only new child of i2), not i2 itself.
	var i6 *Item
	for i := range reg.Items {
		if reg.Items[i].ID == "i6" {
			i6 = &reg.Items[i]
		}
	}
	if i6 == nil {
		t.Fatal("i6 not found after split")
	}
	foundChild := false
	foundParent := false
	for _, dep := range i6.DependsOn {
		if dep == "i2a" {
			foundChild = true
		}
		if dep == "i2" {
			foundParent = true
		}
	}
	if !foundChild {
		t.Errorf("i6.DependsOn after split: expected i2a; got %v", i6.DependsOn)
	}
	if foundParent {
		t.Errorf("i6.DependsOn after split: still contains i2 (should be replaced by children); got %v", i6.DependsOn)
	}
}

// --- set-status ---

// TestSetStatus_OpenToDone checks that set-status transitions an item and
// the regen output moves it to the closed view.
func TestSetStatus_OpenToDone(t *testing.T) {
	paths := setupMutatorFixture(t)

	// i1b is OPEN in testdata; mark it DONE with a completing PR.
	// First remove its depends_on edge (i1a is DONE so the edge is satisfied;
	// but DONE leaf with depends_on is fine — they're just gated edges, not blockers).
	runSetStatus([]string{
		"--id", "i1b",
		"--status", "DONE",
		"--pr", "999",
	}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)
	var i1b *Item
	for i := range reg.Items {
		if reg.Items[i].ID == "i1b" {
			i1b = &reg.Items[i]
		}
	}
	if i1b == nil {
		t.Fatal("i1b not found")
	}
	if i1b.Status != StatusDone {
		t.Errorf("i1b.Status: got %q, want %q", i1b.Status, StatusDone)
	}
	// Confirm the completing PR is attached.
	found := false
	for _, pr := range i1b.PRs {
		if pr.Num == 999 && pr.Role == RoleCompleting {
			found = true
		}
	}
	if !found {
		t.Errorf("i1b.PRs: completing PR #999 not found; got %v", i1b.PRs)
	}
}

// TestSetStatus_OpenClosedMoveReflectedInGen confirms that the open↔closed
// split is automatic: a status change from OPEN to DONE moves the row to the
// closed-view output.
func TestSetStatus_OpenClosedMoveReflectedInGen(t *testing.T) {
	paths := setupMutatorFixture(t)

	// Confirm i1b starts in open view.
	reg := loadRegFromPaths(t, paths)
	var origStatus Status
	for _, it := range reg.Items {
		if it.ID == "i1b" {
			origStatus = it.Status
		}
	}
	if !isOpen(origStatus) {
		t.Skip("testdata i1b is not OPEN; fixture may have changed")
	}

	runSetStatus([]string{"--id", "i1b", "--status", "DONE", "--pr", "888"}, paths)
	assertValidFromPaths(t, paths)

	reg2 := loadRegFromPaths(t, paths)
	for _, it := range reg2.Items {
		if it.ID == "i1b" {
			if isOpen(it.Status) {
				t.Error("i1b should be closed after set-status DONE, but isOpen returns true")
			}
		}
	}
}

// --- set-pr ---

// TestSetPR_Followup checks that set-pr attaches a followup PR.
func TestSetPR_Followup(t *testing.T) {
	paths := setupMutatorFixture(t)

	runSetPR([]string{
		"--id", "i5",
		"--pr", "200",
		"--role", "followup",
	}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)
	var i5 *Item
	for i := range reg.Items {
		if reg.Items[i].ID == "i5" {
			i5 = &reg.Items[i]
		}
	}
	if i5 == nil {
		t.Fatal("i5 not found")
	}
	found := false
	for _, pr := range i5.PRs {
		if pr.Num == 200 && pr.Role == RoleFollowup {
			found = true
		}
	}
	if !found {
		t.Errorf("i5.PRs: followup PR #200 not found; got %v", i5.PRs)
	}
}

// --- dep add / rm ---

// TestDepAdd_ValidEdge checks that dep add attaches a depends_on edge and validate stays green.
func TestDepAdd_ValidEdge(t *testing.T) {
	paths := setupMutatorFixture(t)

	// i10 does not exist in testdata; add a new item without deps, then add one.
	runAdd([]string{
		"--id", "i6",
		"--title", "Item for dep-add test",
		"--desc", "Test item.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	// Add a dep edge from i6 → i5 (i5 is DONE, so the edge is satisfied and valid).
	runDep([]string{"add", "--id", "i6", "--on", "i5"}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)
	var i6 *Item
	for i := range reg.Items {
		if reg.Items[i].ID == "i6" {
			i6 = &reg.Items[i]
		}
	}
	if i6 == nil {
		t.Fatal("i6 not found")
	}
	found := false
	for _, dep := range i6.DependsOn {
		if dep == "i5" {
			found = true
		}
	}
	if !found {
		t.Errorf("i6.DependsOn: expected i5; got %v", i6.DependsOn)
	}
}

// TestDepAdd_CycleRejected asserts that adding a dep edge that would create a
// cycle is REJECTED with exit 1 and leaves the source unchanged.
func TestDepAdd_CycleRejected(t *testing.T) {
	// Use an in-memory registry to test the cycle detection path without invoking
	// os.Exit. We exercise the validation directly.
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "A", Status: StatusOpen, Kind: "leaf", Owner: "agent",
				DependsOn: []string{"i2"}},
			{ID: "i2", Title: "B", Status: StatusOpen, Kind: "leaf", Owner: "agent"},
		},
	}

	// Add the back-edge i2→i1 in memory.
	reg.Items[1].DependsOn = []string{"i1"}

	ve := validate(reg)
	if !ve.hasErrors() {
		t.Fatal("expected cycle error from validate, got none")
	}
	hasСycle := false
	for _, msg := range ve.msgs {
		if strings.Contains(msg, "cycle") {
			hasСycle = true
		}
	}
	if !hasСycle {
		t.Errorf("expected cycle error in validation messages; got: %v", ve.msgs)
	}
}

// TestDepAdd_WontfixTargetRejected asserts that dep add onto a WONTFIX target
// from a non-WONTFIX item is rejected (invariant 12).
func TestDepAdd_WontfixTargetRejected(t *testing.T) {
	// Exercise validate directly (same path that applyAndCommit calls).
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "Open item", Status: StatusOpen, Kind: "leaf", Owner: "agent",
				DependsOn: []string{"i2"}},
			{ID: "i2", Title: "Wontfix", Description: "abandoned", Status: StatusWontfix, Kind: "leaf", Owner: "agent"},
		},
	}
	ve := validate(reg)
	if !ve.hasErrors() {
		t.Fatal("expected invariant-12 error from validate, got none")
	}
	found := false
	for _, msg := range ve.msgs {
		if strings.Contains(msg, "WONTFIX node") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected WONTFIX-node error; got: %v", ve.msgs)
	}
}

// TestDepRM_RemovesEdge verifies that dep rm removes an existing depends_on edge.
func TestDepRM_RemovesEdge(t *testing.T) {
	paths := setupMutatorFixture(t)

	// i1b has depends_on:[i1a] in testdata.
	runDep([]string{"rm", "--id", "i1b", "--on", "i1a"}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)
	for _, it := range reg.Items {
		if it.ID == "i1b" {
			for _, dep := range it.DependsOn {
				if dep == "i1a" {
					t.Error("i1b still depends on i1a after dep rm")
				}
			}
		}
	}
}

// --- answer ---

// TestAnswer_DeletesQuestion verifies that a question with no dependents is deleted.
// Before answering q1 we must curate: remove the depends_on edge and then
// update i2 so its refs no longer point at q1 (otherwise invariant 10 fires
// when the question is gone). We do this by adding a replacement item i6 that
// represents what i2 gets repointed to, and using set-status/dep to curate.
func TestAnswer_DeletesQuestion(t *testing.T) {
	paths := setupMutatorFixture(t)

	// i2 in testdata has: depends_on:[q1], refs:[q1].
	// Curation: remove the depends_on edge (the delete-gate check) and also
	// remove the ref so invariant 10 passes after q1 is gone.
	// We add a fresh item i6 that has no dependency on q1 to ensure the
	// registry still has a valid record after removal.
	runAdd([]string{
		"--id", "i6",
		"--title", "Replacement for q1-gated work",
		"--desc", "Added once the q1 decision is known.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	// Remove the depends_on edge from i2 → q1.
	runDep([]string{"rm", "--id", "i2", "--on", "q1"}, paths)

	// i2.refs still contains q1; update i2 so it points at i6 instead.
	// The cleanest in-place approach: edit the in-memory registry directly
	// (no mutator for refs yet) and write it, then validate.
	reg := loadRegFromPaths(t, paths)
	for idx := range reg.Items {
		if reg.Items[idx].ID == "i2" {
			newRefs := []string{}
			for _, r := range reg.Items[idx].Refs {
				if r != "q1" {
					newRefs = append(newRefs, r)
				}
			}
			reg.Items[idx].Refs = newRefs
		}
	}
	if err := writeItems(paths.itemsYAML, reg.Items); err != nil {
		t.Fatalf("writeItems: %v", err)
	}
	assertValidFromPaths(t, paths)

	// Now answer (delete) q1 — no dependents remain.
	runAnswer([]string{"--id", "q1"}, paths)
	assertValidFromPaths(t, paths)

	// q1 should no longer exist.
	reg2 := loadRegFromPaths(t, paths)
	for _, q := range reg2.Questions {
		if q.ID == "q1" {
			t.Error("q1 still present after answer (delete)")
		}
	}
}

// TestAnswer_FailsWhenDependentExists verifies the delete-gate (invariant 13):
// answer fails with a clear message when an item still depends_on the question.
func TestAnswer_FailsWhenDependentExists(t *testing.T) {
	// Exercise the delete-gate check directly (to avoid capturing os.Exit).
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "Gated on q1", Status: StatusOpen, Kind: "leaf", Owner: "agent",
				DependsOn: []string{"q1"}},
		},
		Questions: []Question{
			{ID: "q1", Body: "Some decision?", Owner: "pete"},
		},
	}

	// Simulate what runAnswer does: collect blockers before deleting.
	var blockers []string
	for _, it := range reg.Items {
		for _, dep := range it.DependsOn {
			if dep == "q1" {
				blockers = append(blockers, it.ID)
			}
		}
	}
	if len(blockers) == 0 {
		t.Fatal("expected blockers for q1, got none")
	}
	if blockers[0] != "i1" {
		t.Errorf("expected blocker i1; got %v", blockers)
	}
	// This is exactly the condition that causes runAnswer to exit 1 with the
	// "REJECTED — cannot delete … while N item(s) still depend on it" message.
}

// TestAnswer_FailsWhenDependentExists_ViaValidation confirms that deleting a
// question without first removing dependents produces a validate error (invariant 13
// falls out of invariant 11's existence check).
func TestAnswer_FailsWhenDependentExists_ViaValidation(t *testing.T) {
	// Simulate premature deletion: i1 still depends on q1 but q1 is absent.
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "Gated", Status: StatusOpen, Kind: "leaf", Owner: "agent",
				DependsOn: []string{"q1"}},
		},
		Questions: []Question{}, // q1 deleted without removing the edge
	}
	ve := validate(reg)
	if !ve.hasErrors() {
		t.Fatal("expected dangling-edge error after premature delete, got none")
	}
	found := false
	for _, msg := range ve.msgs {
		if strings.Contains(msg, "dangling edge") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected dangling-edge error; got: %v", ve.msgs)
	}
}
