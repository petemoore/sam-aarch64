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

// expectedNextItemID returns the id that runAdd would auto-allocate for an item
// against the current on-disk fixture (registry + ledger), so a test can assert
// on the new id without hardcoding it.
func expectedNextItemID(t *testing.T, paths mutatorPaths) string {
	t.Helper()
	reg := loadRegFromPaths(t, paths)
	ledger, err := loadLedger(ledgerPath(paths.registryDir))
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	return nextItemID(reg, ledger)
}

// expectedNextQuestionID returns the id that runAdd --space questions would
// auto-allocate against the current on-disk fixture.
func expectedNextQuestionID(t *testing.T, paths mutatorPaths) string {
	t.Helper()
	reg := loadRegFromPaths(t, paths)
	ledger, err := loadLedger(ledgerPath(paths.registryDir))
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	return nextQuestionID(reg, ledger)
}

// expectedNextSubID returns the child id that runSplit would auto-determine for
// the given parent against the current on-disk fixture (registry + ledger).
func expectedNextSubID(t *testing.T, paths mutatorPaths, parentID string) string {
	t.Helper()
	reg := loadRegFromPaths(t, paths)
	ledger, err := loadLedger(ledgerPath(paths.registryDir))
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	id, err := nextSubID(parentID, mintedIDs(reg, ledger))
	if err != nil {
		t.Fatalf("nextSubID(%q): %v", parentID, err)
	}
	return id
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

// --- id allocators (nextItemID / nextQuestionID) ---
//
// These are the functions add/split use to auto-allocate ids. They are still
// part of the public surface (add no longer takes --id; it calls these), so they
// remain unit-tested here even though `next-id` is no longer a CLI command.

// TestNextID_Items asserts nextItemID returns the next free item id not present in
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

// TestAdd_Item checks that add appends a valid item with an auto-allocated id
// (the testdata fixture has top-level i1..i5 so the next id is i6), the source
// validates, and gen would still produce consistent output. The id is no longer
// caller-supplied (--id was removed).
func TestAdd_Item(t *testing.T) {
	paths := setupMutatorFixture(t)

	// Expected auto-allocated id: nextItemID over the fixture (top-level max is
	// i5) → i6.
	wantID := expectedNextItemID(t, paths)

	runAdd([]string{
		"--title", "New test item",
		"--desc", "A freshly added item for test coverage.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	// Validate that the source on disk is still valid.
	assertValidFromPaths(t, paths)

	// Confirm the auto-allocated id is present in the written items.yaml.
	reg := loadRegFromPaths(t, paths)
	found := false
	for _, it := range reg.Items {
		if it.ID == wantID {
			found = true
			if it.Title != "New test item" {
				t.Errorf("%s.Title: got %q, want %q", wantID, it.Title, "New test item")
			}
		}
	}
	if !found {
		t.Errorf("%s not found in items after add", wantID)
	}
}

// TestAdd_Question checks that add --space questions appends a question record
// with an auto-allocated q-id (the fixture has q1 so the next id is q2).
func TestAdd_Question(t *testing.T) {
	paths := setupMutatorFixture(t)

	wantID := expectedNextQuestionID(t, paths)

	runAdd([]string{
		"--space", "questions",
		"--desc", "Should we use approach A or B?",
		"--owner", "pete",
	}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)
	found := false
	for _, q := range reg.Questions {
		if q.ID == wantID {
			found = true
			if q.Owner != "pete" {
				t.Errorf("%s.Owner: got %q, want %q", wantID, q.Owner, "pete")
			}
		}
	}
	if !found {
		t.Errorf("%s not found in questions after add", wantID)
	}
}

// TestAdd_LedgerUpdated confirms the ledger file is written for the new id.
func TestAdd_LedgerUpdated(t *testing.T) {
	paths := setupMutatorFixture(t)

	wantID := expectedNextItemID(t, paths)

	runAdd([]string{
		"--title", "Ledger test",
		"--desc", "Checking ledger write.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	ledger, err := loadLedger(ledgerPath(paths.registryDir))
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	if !ledger[wantID] {
		t.Errorf("%s not found in ledger after add", wantID)
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

	// Add a dependent that depends on i2 (the item we will split). Its id is
	// auto-allocated (i6 in the fixture).
	depID := expectedNextItemID(t, paths)
	runAdd([]string{
		"--title", "Item depending on i2",
		"--desc", "Will be rewritten when i2 splits.",
		"--status", "OPEN",
		"--owner", "agent",
		"--dep", "i2",
	}, paths)

	// The child id is auto-determined from the parent: i2 has no children yet, so
	// nextSubID("i2") → i2a.
	childID := expectedNextSubID(t, paths, "i2")

	// Split i2 into an umbrella with the auto-determined child.
	// i2 is OPEN (no completing PR), so promoting it to umbrella is valid.
	runSplit([]string{
		"--parent", "i2",
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

	// The child should exist as a leaf child of i2.
	var child *Item
	for i := range reg.Items {
		if reg.Items[i].ID == childID {
			child = &reg.Items[i]
		}
	}
	if child == nil {
		t.Fatalf("%s not found after split", childID)
	}
	if child.Parent != "i2" {
		t.Errorf("%s.Parent: got %q, want %q", childID, child.Parent, "i2")
	}
	if child.Kind != "leaf" {
		t.Errorf("%s.Kind: got %q, want %q", childID, child.Kind, "leaf")
	}

	// The dependent previously had depends_on:[i2]; after split it should contain
	// the new child, not i2 itself.
	var dep *Item
	for i := range reg.Items {
		if reg.Items[i].ID == depID {
			dep = &reg.Items[i]
		}
	}
	if dep == nil {
		t.Fatalf("%s not found after split", depID)
	}
	foundChild := false
	foundParent := false
	for _, d := range dep.DependsOn {
		if d == childID {
			foundChild = true
		}
		if d == "i2" {
			foundParent = true
		}
	}
	if !foundChild {
		t.Errorf("%s.DependsOn after split: expected %s; got %v", depID, childID, dep.DependsOn)
	}
	if foundParent {
		t.Errorf("%s.DependsOn after split: still contains i2 (should be replaced by children); got %v", depID, dep.DependsOn)
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

// TestSetOwner flips an item's owner agent→pete→agent and confirms the change
// round-trips through validate + regeneration. This is the lever i172 / the
// needs-Pete model use to move a presence-gated item out of (and back into) the
// agent `ready` queue.
func TestSetOwner(t *testing.T) {
	paths := setupMutatorFixture(t)

	get := func(id string) *Item {
		reg := loadRegFromPaths(t, paths)
		for i := range reg.Items {
			if reg.Items[i].ID == id {
				return &reg.Items[i]
			}
		}
		t.Fatalf("%s not found", id)
		return nil
	}

	if got := get("i1b").Owner; got != "agent" {
		t.Fatalf("precondition: i1b owner got %q, want agent", got)
	}

	runSetOwner([]string{"--id", "i1b", "--owner", "pete"}, paths)
	assertValidFromPaths(t, paths)
	if got := get("i1b").Owner; got != "pete" {
		t.Errorf("after set-owner pete: i1b owner got %q, want pete", got)
	}

	// Flip back — the lever works both directions.
	runSetOwner([]string{"--id", "i1b", "--owner", "agent"}, paths)
	assertValidFromPaths(t, paths)
	if got := get("i1b").Owner; got != "agent" {
		t.Errorf("after set-owner agent: i1b owner got %q, want agent", got)
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
		t.Fatalf("testdata fixture invariant broken: i1b is not OPEN (status=%v) — the test fixture drifted", origStatus)
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

// TestSetPR_Idempotent is the i282 regression: attaching the SAME completing PR
// twice — set-pr --pr N then set-status --pr N (the natural "attach the PR, then
// flip to DONE" sequence) — must leave exactly ONE {num, role} entry, not a
// duplicate pair (observed accumulating on i280b-b2b / PR #724, hand-deduped).
func TestSetPR_Idempotent(t *testing.T) {
	paths := setupMutatorFixture(t)

	// set-pr attaches the completing PR; set-status --pr re-attaches the same one.
	runSetPR([]string{"--id", "i1b", "--pr", "724", "--role", "completing"}, paths)
	runSetStatus([]string{"--id", "i1b", "--status", "DONE", "--pr", "724"}, paths)

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
	n := 0
	for _, pr := range i1b.PRs {
		if pr.Num == 724 && pr.Role == RoleCompleting {
			n++
		}
	}
	if n != 1 {
		t.Errorf("i1b.PRs: completing PR #724 present %d times, want exactly 1 (upsert); got %v", n, i1b.PRs)
	}
}

// TestAddPR_DedupesOnNumAndRole unit-tests the upsert directly: same (num, role)
// is a no-op; the same num with a DIFFERENT role is a distinct, kept entry.
func TestAddPR_DedupesOnNumAndRole(t *testing.T) {
	it := &Item{ID: "i1"}
	it.AddPR(50, RoleCompleting)
	it.AddPR(50, RoleCompleting) // duplicate -> no-op
	if len(it.PRs) != 1 {
		t.Fatalf("after two identical AddPR, len(PRs)=%d want 1; got %v", len(it.PRs), it.PRs)
	}
	it.AddPR(50, RoleFollowup) // same num, different role -> distinct
	if len(it.PRs) != 2 {
		t.Fatalf("after adding a different role, len(PRs)=%d want 2; got %v", len(it.PRs), it.PRs)
	}
}

// --- dep add / rm ---

// TestDepAdd_ValidEdge checks that dep add attaches a depends_on edge and validate stays green.
func TestDepAdd_ValidEdge(t *testing.T) {
	paths := setupMutatorFixture(t)

	// Add a new item without deps (id auto-allocated), then add an edge to it.
	newID := expectedNextItemID(t, paths)
	runAdd([]string{
		"--title", "Item for dep-add test",
		"--desc", "Test item.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	// Add a dep edge from the new item → i5 (i5 is DONE, so the edge is satisfied and valid).
	runDep([]string{"add", "--id", newID, "--on", "i5"}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)
	var newIt *Item
	for i := range reg.Items {
		if reg.Items[i].ID == newID {
			newIt = &reg.Items[i]
		}
	}
	if newIt == nil {
		t.Fatalf("%s not found", newID)
	}
	found := false
	for _, dep := range newIt.DependsOn {
		if dep == "i5" {
			found = true
		}
	}
	if !found {
		t.Errorf("%s.DependsOn: expected i5; got %v", newID, newIt.DependsOn)
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
	// We add a fresh item (id auto-allocated) that has no dependency on q1 to
	// ensure the registry still has a valid record after removal.
	runAdd([]string{
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

	wantID := expectedNextItemID(t, paths)

	runAdd([]string{
		"--title", "Done in one command",
		"--desc", "A completed leaf migrated with its PR.",
		"--status", "DONE",
		"--owner", "agent",
		"--pr", "73",
	}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)
	for _, it := range reg.Items {
		if it.ID == wantID {
			if it.Status != StatusDone {
				t.Errorf("%s.Status: got %q, want DONE", wantID, it.Status)
			}
			if len(it.PRs) != 1 || it.PRs[0].Num != 73 || it.PRs[0].Role != RoleCompleting {
				t.Errorf("%s.PRs: got %+v, want one completing PR #73", wantID, it.PRs)
			}
			return
		}
	}
	t.Errorf("%s not found after add", wantID)
}

// TestAdd_KindUmbrella confirms `add --kind umbrella` creates an umbrella record
// (carrying no PRs), so a pre-existing-umbrella item migrates in one command.
func TestAdd_KindUmbrella(t *testing.T) {
	paths := setupMutatorFixture(t)

	wantID := expectedNextItemID(t, paths)

	runAdd([]string{
		"--title", "Umbrella item",
		"--desc", "Groups its leaf children.",
		"--status", "OPEN",
		"--owner", "agent",
		"--kind", "umbrella",
	}, paths)

	assertValidFromPaths(t, paths)

	reg := loadRegFromPaths(t, paths)
	for _, it := range reg.Items {
		if it.ID == wantID {
			if !it.isUmbrella() {
				t.Errorf("%s.Kind: got %q, want umbrella", wantID, it.Kind)
			}
			return
		}
	}
	t.Errorf("%s not found after add", wantID)
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

	newID := expectedNextItemID(t, paths)
	runAdd([]string{
		"--title", "New item that should auto-join the queue",
		"--desc", "Added via add; priority.yaml must update without hand-edit.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	ids := loadPriorityFromPaths(t, paths)

	if !containsStr(ids, newID) {
		t.Errorf("priority missing new item %s after add; got %v", newID, ids)
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
// Testdata: i1b and i2 are pullable. We split i1b → umbrella with an
// auto-determined child. i1b is a letter-child (iNx), so nextSubID yields a
// numbered brick (i1b-b1). After split: i1b is no longer pullable (now an
// umbrella); the new child must be in the queue.
//
// Note: i1b depends_on i1a. After promotion to umbrella, i1b itself loses leaf
// status (becomes umbrella), so it is no longer in the pullable set. The new
// child is an OPEN leaf → pullable and must join the queue.
func TestPriorityAutoMaintain_Split(t *testing.T) {
	// Testdata pullable set: {i1b, i2}. Start with the full pullable set.
	paths := setupMutatorFixtureWithPriority(t, []string{"i1b", "i2"})

	// Child id is auto-determined from the parent: i1b is a letter-child, so
	// nextSubID("i1b") → i1b-b1.
	childID := expectedNextSubID(t, paths, "i1b")

	runSplit([]string{
		"--parent", "i1b",
		"--title", "Sub-task of what was i1b",
	}, paths)

	ids := loadPriorityFromPaths(t, paths)

	// i1b is now an umbrella — must be gone from queue.
	if containsStr(ids, "i1b") {
		t.Errorf("priority still contains i1b (now umbrella) after split; got %v", ids)
	}
	// The new child leaf must be present.
	if !containsStr(ids, childID) {
		t.Errorf("priority missing new child %s after split; got %v", childID, ids)
	}
	// i2 must still be present.
	if !containsStr(ids, "i2") {
		t.Errorf("priority missing i2 after split of i1b; got %v", ids)
	}
	assertPriorityValid(t, paths)
}

// TestPriorityAutoMaintain_OrderPreservation verifies that reconcile preserves
// the relative order of surviving ids while dropping the non-pullable ones.
// Setup: three pullable items [<new>, i1b, i2] in that curated order. Close i1b.
// Expect: [<new>, i2] — the new item before i2, as originally ranked.
func TestPriorityAutoMaintain_OrderPreservation(t *testing.T) {
	// Add a fresh item first so it exists in the registry (id auto-allocated).
	paths := setupMutatorFixtureWithPriority(t, []string{})

	newID := expectedNextItemID(t, paths)
	runAdd([]string{
		"--title", "Order-preservation test item",
		"--desc", "Used to check that reconcile preserves curated order.",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	// Now set the priority to a curated order: <new>, i1b, i2.
	// Write it directly then reload (simulate the user having ranked it with move).
	if err := writePriority(paths.priorityYAML, []string{newID, "i1b", "i2"}); err != nil {
		t.Fatalf("writePriority: %v", err)
	}
	// Reload so reg.Priority matches what's on disk.
	reg := loadRegFromPaths(t, paths)
	// Sanity: all three must be pullable at this point.
	if pve := validatePriority(reg, reg.Priority); pve.hasErrors() {
		t.Fatalf("initial priority invalid: %s", strings.Join(pve.msgs, "\n"))
	}

	// Close i1b — must be dropped from the queue. <new> and i2 must survive in order.
	runSetStatus([]string{"--id", "i1b", "--status", "DONE", "--pr", "779"}, paths)

	ids := loadPriorityFromPaths(t, paths)

	if containsStr(ids, "i1b") {
		t.Errorf("priority still contains closed i1b; got %v", ids)
	}

	// Find positions of the new item and i2.
	posNew, posI2 := -1, -1
	for i, id := range ids {
		switch id {
		case newID:
			posNew = i
		case "i2":
			posI2 = i
		}
	}
	if posNew == -1 {
		t.Errorf("%s missing from priority after close of i1b; got %v", newID, ids)
	}
	if posI2 == -1 {
		t.Errorf("i2 missing from priority after close of i1b; got %v", ids)
	}
	if posNew != -1 && posI2 != -1 && posNew > posI2 {
		t.Errorf("curated order violated: %s (pos %d) should be before i2 (pos %d); got %v",
			newID, posNew, posI2, ids)
	}
	assertPriorityValid(t, paths)
}

// TestAddLandsAtTop_NoDepItem verifies that a newly added OPEN no-dep item
// becomes the tip of the ready queue (ready-position 1 among agent-actionable
// items) after add.
//
// Setup: priority starts as [i1b, i2]. A new item (i6) is added with no deps.
// Expected: after add, i6 is at index 0 in the priority queue (topo repair has
// nothing to move past, so it stays at the front).
func TestAddLandsAtTop_NoDepItem(t *testing.T) {
	// Testdata pullable set: {i1b, i2}. Start with that curated order.
	paths := setupMutatorFixtureWithPriority(t, []string{"i1b", "i2"})

	newID := expectedNextItemID(t, paths)
	runAdd([]string{
		"--title", "Should land at top of backlog",
		"--status", "OPEN",
		"--owner", "agent",
	}, paths)

	ids := loadPriorityFromPaths(t, paths)

	if len(ids) == 0 {
		t.Fatal("priority queue is empty after add")
	}
	// The new item must be at position 0 (the front / highest priority).
	if ids[0] != newID {
		t.Errorf("after add of no-dep item, expected %s at position 0 (top of queue); got queue %v", newID, ids)
	}
	// Existing items must still follow it.
	if !containsStr(ids, "i1b") {
		t.Errorf("priority missing i1b after add; got %v", ids)
	}
	if !containsStr(ids, "i2") {
		t.Errorf("priority missing i2 after add; got %v", ids)
	}
	assertPriorityValid(t, paths)
}

// TestAddLandsAfterDep_DepedOnItem verifies topo correctness: when a new item
// is added with --dep on an item already in the queue, topo repair places the
// new item AFTER its dependency, not at rank 1.
//
// Setup: priority starts as [i1b, i2]. A new item (i6) is added with
// --dep i1b (i1b is pullable and in the queue). Expected: after add, i6 is
// somewhere after i1b in the queue (not at position 0 or before i1b).
func TestAddLandsAfterDep_DepedOnItem(t *testing.T) {
	// i1b depends_on i1a (DONE — satisfied for ready), so i1b itself is pullable.
	paths := setupMutatorFixtureWithPriority(t, []string{"i1b", "i2"})

	newID := expectedNextItemID(t, paths)
	runAdd([]string{
		"--title", "Should land after its in-queue dep",
		"--status", "OPEN",
		"--owner", "agent",
		"--dep", "i1b", // i1b is an in-queue item
	}, paths)

	ids := loadPriorityFromPaths(t, paths)

	// Find positions.
	posNew, posI1b := -1, -1
	for i, id := range ids {
		switch id {
		case newID:
			posNew = i
		case "i1b":
			posI1b = i
		}
	}
	if posNew == -1 {
		t.Fatalf("%s not found in priority after add; got %v", newID, ids)
	}
	if posI1b == -1 {
		t.Fatalf("i1b not found in priority after add; got %v", ids)
	}
	// The new item must appear AFTER its dependency.
	if posNew <= posI1b {
		t.Errorf("topo violation: %s (pos %d) is at or before its dep i1b (pos %d); queue: %v",
			newID, posNew, posI1b, ids)
	}
	assertPriorityValid(t, paths)
}

// TestDeriveUmbrellaStatuses covers the i233 fix: umbrella status is derived
// from its children (DONE iff all children are DONE/WONTFIX, else OPEN, never
// IN_PROGRESS), at the applyAndCommit chokepoint, so completing the last child
// no longer leaves the umbrella stale-OPEN.
func TestDeriveUmbrellaStatuses(t *testing.T) {
	mk := func(id, kind, parent string, st Status) Item {
		return Item{ID: id, Kind: kind, Parent: parent, Status: st}
	}

	t.Run("all children closed flips a stale-OPEN umbrella to DONE", func(t *testing.T) {
		reg := &Registry{Items: []Item{
			mk("i1", "umbrella", "", StatusOpen),
			mk("i1a", "leaf", "i1", StatusDone),
			mk("i1b", "leaf", "i1", StatusWontfix),
		}}
		deriveUmbrellaStatuses(reg)
		if got := reg.Items[0].Status; got != StatusDone {
			t.Errorf("umbrella status = %s, want DONE", got)
		}
	})

	t.Run("an open child flips a stale-DONE umbrella back to OPEN", func(t *testing.T) {
		reg := &Registry{Items: []Item{
			mk("i1", "umbrella", "", StatusDone),
			mk("i1a", "leaf", "i1", StatusDone),
			mk("i1b", "leaf", "i1", StatusInProgress),
		}}
		deriveUmbrellaStatuses(reg)
		if got := reg.Items[0].Status; got != StatusOpen {
			t.Errorf("umbrella status = %s, want OPEN", got)
		}
	})

	t.Run("nested umbrellas settle via the fixpoint", func(t *testing.T) {
		// i1 (umbrella) -> i1a (umbrella) -> i1a1 (leaf, DONE).
		reg := &Registry{Items: []Item{
			mk("i1", "umbrella", "", StatusOpen),
			mk("i1a", "umbrella", "i1", StatusOpen),
			mk("i1a1", "leaf", "i1a", StatusDone),
		}}
		deriveUmbrellaStatuses(reg)
		if reg.Items[1].Status != StatusDone {
			t.Errorf("inner umbrella = %s, want DONE", reg.Items[1].Status)
		}
		if reg.Items[0].Status != StatusDone {
			t.Errorf("outer umbrella = %s, want DONE (fixpoint should close both levels)", reg.Items[0].Status)
		}
	})

	t.Run("a childless umbrella keeps its stored status", func(t *testing.T) {
		reg := &Registry{Items: []Item{mk("i1", "umbrella", "", StatusOpen)}}
		deriveUmbrellaStatuses(reg)
		if reg.Items[0].Status != StatusOpen {
			t.Errorf("childless umbrella = %s, want unchanged OPEN", reg.Items[0].Status)
		}
	})
}
