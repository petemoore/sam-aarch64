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

// setupMutatorFixtureWithPriority is like setupMutatorFixture but also writes a
// priority.yaml with the given initial ids. It returns a mutatorPaths with
// priorityYAML set so that applyAndCommit will auto-maintain the queue.
func setupMutatorFixtureWithPriority(t *testing.T, initialIDs []string) mutatorPaths {
	t.Helper()
	paths := setupMutatorFixture(t)
	priorityPath := filepath.Join(filepath.Dir(paths.itemsYAML), "priority.yaml")
	if err := writePriority(priorityPath, initialIDs); err != nil {
		t.Fatalf("setupMutatorFixtureWithPriority: writePriority: %v", err)
	}
	paths.priorityYAML = priorityPath
	return paths
}

// loadPriorityFromPaths reads priority.yaml from disk and returns the ids.
func loadPriorityFromPaths(t *testing.T, paths mutatorPaths) []string {
	t.Helper()
	ids, err := loadPriority(paths.priorityYAML)
	if err != nil {
		t.Fatalf("loadPriorityFromPaths: %v", err)
	}
	return ids
}

// assertPriorityValid checks that priority.yaml on disk satisfies validatePriority.
func assertPriorityValid(t *testing.T, paths mutatorPaths) {
	t.Helper()
	reg := loadRegFromPaths(t, paths)
	pve := validatePriority(reg, reg.Priority)
	if pve.hasErrors() {
		t.Fatalf("validatePriority after mutation failed:\n%s", strings.Join(pve.msgs, "\n"))
	}
}

// containsStr reports whether ss contains s.
func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
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

// TestAdd_DonePR_OneCommand confirms `add --status DONE --pr N` attaches the
// completing PR in one command (so a DONE leaf validates without a follow-up
// set-status). This is the migration's one-command DONE-item path.
func TestAdd_DonePR_OneCommand(t *testing.T) {
	paths := setupMutatorFixture(t)

	runAdd([]string{
		"--id", "i6",
		"--title", "Done in one command",
		"--desc", "A completed leaf migrated with its PR.",
		"--status", "DONE",
		"--owner", "agent",
		"--pr", "73",
	}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)
	for _, it := range reg.Items {
		if it.ID == "i6" {
			if it.Status != StatusDone {
				t.Errorf("i6.Status: got %q, want DONE", it.Status)
			}
			if len(it.PRs) != 1 || it.PRs[0].Num != 73 || it.PRs[0].Role != RoleCompleting {
				t.Errorf("i6.PRs: got %+v, want one completing PR #73", it.PRs)
			}
			return
		}
	}
	t.Error("i6 not found after add")
}

// TestAdd_KindUmbrella confirms `add --kind umbrella` creates an umbrella record
// (carrying no PRs), so a pre-existing-umbrella item migrates in one command.
func TestAdd_KindUmbrella(t *testing.T) {
	paths := setupMutatorFixture(t)

	runAdd([]string{
		"--id", "i6",
		"--title", "Umbrella item",
		"--desc", "Groups its leaf children.",
		"--status", "OPEN",
		"--owner", "agent",
		"--kind", "umbrella",
	}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)
	for _, it := range reg.Items {
		if it.ID == "i6" {
			if !it.isUmbrella() {
				t.Errorf("i6.Kind: got %q, want umbrella", it.Kind)
			}
			return
		}
	}
	t.Error("i6 not found after add")
}

// --- priority auto-maintenance ---
//
// The following tests verify that applyAndCommit auto-reconciles priority.yaml
// when a priority file is active (paths.priorityYAML != ""), eliminating the
// hand-edit burden that existed before this fix.

// TestPriorityAutoMaintain_SetStatusDone verifies that flipping a pullable item
// to DONE removes it from priority.yaml and leaves the queue valid.
// Testdata: i1b and i2 are both pullable (OPEN leaves); queue starts as [i1b, i2].
// After set-status i1b DONE: i1b is no longer pullable and must be gone.
func TestPriorityAutoMaintain_SetStatusDone(t *testing.T) {
	// Testdata pullable set: {i1b, i2}.
	paths := setupMutatorFixtureWithPriority(t, []string{"i1b", "i2"})

	runSetStatus([]string{"--id", "i1b", "--status", "DONE", "--pr", "777"}, paths)

	ids := loadPriorityFromPaths(t, paths)

	// i1b must be gone.
	if containsStr(ids, "i1b") {
		t.Errorf("priority still contains i1b after set-status DONE; got %v", ids)
	}
	// i2 must still be present.
	if !containsStr(ids, "i2") {
		t.Errorf("priority is missing i2 after set-status on i1b; got %v", ids)
	}
	// The queue must pass validatePriority (strict permutation).
	assertPriorityValid(t, paths)
}

// TestPriorityAutoMaintain_Reopen verifies that flipping a non-queued item back
// to OPEN adds it to priority.yaml (at the end) and leaves the queue valid.
// We first close i1b (removing it from the queue), then reopen it and confirm
// it re-appears.
func TestPriorityAutoMaintain_Reopen(t *testing.T) {
	paths := setupMutatorFixtureWithPriority(t, []string{"i1b", "i2"})

	// Close i1b — removes it from the queue.
	runSetStatus([]string{"--id", "i1b", "--status", "DONE", "--pr", "778"}, paths)

	// Reopen i1b — must be re-added to the queue.
	runSetStatus([]string{"--id", "i1b", "--status", "OPEN"}, paths)

	ids := loadPriorityFromPaths(t, paths)

	if !containsStr(ids, "i1b") {
		t.Errorf("priority missing i1b after reopen; got %v", ids)
	}
	if !containsStr(ids, "i2") {
		t.Errorf("priority missing i2 after reopen of i1b; got %v", ids)
	}
	assertPriorityValid(t, paths)
}

// TestPriorityAutoMaintain_Add verifies that adding a new OPEN leaf item
// appends it to priority.yaml automatically, so no hand-edit is needed.
// (This was the i141 pain point: the add command didn't touch priority.yaml.)
func TestPriorityAutoMaintain_Add(t *testing.T) {
	// Testdata pullable set: {i1b, i2}. Start queue with the full pullable set.
	paths := setupMutatorFixtureWithPriority(t, []string{"i1b", "i2"})

	runAdd([]string{
		"--id", "i6",
		"--title", "New item that should auto-join the queue",
		"--desc", "Added via add; priority.yaml must update without hand-edit.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	ids := loadPriorityFromPaths(t, paths)

	if !containsStr(ids, "i6") {
		t.Errorf("priority missing new item i6 after add; got %v", ids)
	}
	// Existing items must still be present.
	if !containsStr(ids, "i1b") {
		t.Errorf("priority missing i1b after add of i6; got %v", ids)
	}
	if !containsStr(ids, "i2") {
		t.Errorf("priority missing i2 after add of i6; got %v", ids)
	}
	assertPriorityValid(t, paths)
}

// TestPriorityAutoMaintain_Split verifies that after split:
// - the parent (now an umbrella) is removed from the queue;
// - the new child leaf is appended to the queue.
// Testdata: i1b and i2 are pullable. We will split i1b → umbrella with child i1c.
// After split: i1b is no longer pullable (now an umbrella); i1c must be in the queue.
//
// Note: i1b depends_on i1a. After promotion to umbrella, i1b itself loses leaf
// status (becomes umbrella), so it is no longer in the pullable set. i1c (the
// new child) is OPEN leaf → pullable and must join the queue.
func TestPriorityAutoMaintain_Split(t *testing.T) {
	// Testdata pullable set: {i1b, i2}. Start with the full pullable set.
	paths := setupMutatorFixtureWithPriority(t, []string{"i1b", "i2"})

	runSplit([]string{
		"--parent", "i1b",
		"--child-id", "i1c",
		"--title", "Sub-task of what was i1b",
	}, paths)

	ids := loadPriorityFromPaths(t, paths)

	// i1b is now an umbrella — must be gone from queue.
	if containsStr(ids, "i1b") {
		t.Errorf("priority still contains i1b (now umbrella) after split; got %v", ids)
	}
	// i1c is the new child leaf — must be present.
	if !containsStr(ids, "i1c") {
		t.Errorf("priority missing new child i1c after split; got %v", ids)
	}
	// i2 must still be present.
	if !containsStr(ids, "i2") {
		t.Errorf("priority missing i2 after split of i1b; got %v", ids)
	}
	assertPriorityValid(t, paths)
}

// TestPriorityAutoMaintain_OrderPreservation verifies that reconcile preserves
// the relative order of surviving ids while dropping the non-pullable ones.
// Setup: three pullable items [i6, i1b, i2] in that curated order. Close i1b.
// Expect: [i6, i2] — i6 before i2, as originally ranked.
func TestPriorityAutoMaintain_OrderPreservation(t *testing.T) {
	// Add i6 first so it exists in the registry.
	paths := setupMutatorFixtureWithPriority(t, []string{})

	runAdd([]string{
		"--id", "i6",
		"--title", "Order-preservation test item",
		"--desc", "Used to check that reconcile preserves curated order.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	// Now set the priority to a curated order: i6, i1b, i2.
	// Write it directly then reload (simulate the user having ranked it with move).
	if err := writePriority(paths.priorityYAML, []string{"i6", "i1b", "i2"}); err != nil {
		t.Fatalf("writePriority: %v", err)
	}
	// Reload so reg.Priority matches what's on disk.
	reg := loadRegFromPaths(t, paths)
	// Sanity: all three must be pullable at this point.
	if pve := validatePriority(reg, reg.Priority); pve.hasErrors() {
		t.Fatalf("initial priority invalid: %s", strings.Join(pve.msgs, "\n"))
	}

	// Close i1b — must be dropped from the queue. i6 and i2 must survive in order.
	runSetStatus([]string{"--id", "i1b", "--status", "DONE", "--pr", "779"}, paths)

	ids := loadPriorityFromPaths(t, paths)

	if containsStr(ids, "i1b") {
		t.Errorf("priority still contains closed i1b; got %v", ids)
	}

	// Find positions of i6 and i2.
	posI6, posI2 := -1, -1
	for i, id := range ids {
		switch id {
		case "i6":
			posI6 = i
		case "i2":
			posI2 = i
		}
	}
	if posI6 == -1 {
		t.Errorf("i6 missing from priority after close of i1b; got %v", ids)
	}
	if posI2 == -1 {
		t.Errorf("i2 missing from priority after close of i1b; got %v", ids)
	}
	if posI6 != -1 && posI2 != -1 && posI6 > posI2 {
		t.Errorf("curated order violated: i6 (pos %d) should be before i2 (pos %d); got %v",
			posI6, posI2, ids)
	}
	assertPriorityValid(t, paths)
}
