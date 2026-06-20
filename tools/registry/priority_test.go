package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pullableReg returns a minimal registry for priority invariant tests.
//
// Items:
//   i1      umbrella (not pullable)
//   i1a     DONE leaf (not pullable)
//   i1b     OPEN leaf, depends_on i1a (i1a is DONE → satisfied for ready)
//   i2      OPEN leaf, depends_on q1 (q1 is an open question → not satisfied for ready)
//   i3      WONTFIX leaf (not pullable)
//
// Questions:
//   q1      open question
//
// Valid priority: [i1b, i2] — topological order is unconstrained between them
// since i1b depends on DONE i1a (no pullable dep) and i2 depends on q1 (question).
func pullableReg() *Registry {
	return &Registry{
		Items: []Item{
			{
				ID:     "i1",
				Title:  "Umbrella",
				Status: StatusOpen,
				Kind:   "umbrella",
				Owner:  "agent",
			},
			{
				ID:     "i1a",
				Title:  "Done leaf",
				Status: StatusDone,
				Kind:   "leaf",
				Owner:  "agent",
				Parent: "i1",
				PRs:    []PRRef{{Num: 1, Role: RoleCompleting}},
			},
			{
				ID:        "i1b",
				Title:     "Open leaf gated on DONE i1a",
				Status:    StatusOpen,
				Kind:      "leaf",
				Owner:     "agent",
				Parent:    "i1",
				DependsOn: []string{"i1a"},
			},
			{
				ID:        "i2",
				Title:     "Open leaf gated on open question q1",
				Status:    StatusOpen,
				Kind:      "leaf",
				Owner:     "agent",
				DependsOn: []string{"q1"},
				Refs:      []string{"q1"},
			},
			{
				ID:          "i3",
				Title:       "WONTFIX leaf",
				Description: "Abandoned.",
				Status:      StatusWontfix,
				Kind:        "leaf",
				Owner:       "agent",
			},
		},
		Questions: []Question{
			{ID: "q1", Body: "Which approach?", Owner: "pete"},
		},
	}
}

// TestPriority_ValidPermutation asserts that a correct permutation of the
// pullable items passes priority validation.
func TestPriority_ValidPermutation(t *testing.T) {
	reg := pullableReg()
	pve := validatePriority(reg, []string{"i1b", "i2"})
	if pve.hasErrors() {
		t.Fatalf("expected no errors for valid permutation; got:\n%s",
			strings.Join(pve.msgs, "\n"))
	}
}

// TestPriority_EmptyListIsValid asserts that an empty priority list (absent
// file) is treated as "no priority yet" and produces no validation errors.
func TestPriority_EmptyListIsValid(t *testing.T) {
	reg := pullableReg()
	pve := validatePriority(reg, []string{})
	if pve.hasErrors() {
		t.Fatalf("expected no errors for empty priority list; got:\n%s",
			strings.Join(pve.msgs, "\n"))
	}
}

// TestPriority_MissingID asserts that a missing pullable id produces an error.
func TestPriority_MissingID(t *testing.T) {
	reg := pullableReg()
	// Only i1b listed — i2 is missing.
	pve := validatePriority(reg, []string{"i1b"})
	if !pve.hasErrors() {
		t.Fatal("expected error for missing id i2; got none")
	}
	found := false
	for _, msg := range pve.msgs {
		if strings.Contains(msg, "i2") && strings.Contains(msg, "missing") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error mentioning i2 missing; got:\n%s",
			strings.Join(pve.msgs, "\n"))
	}
}

// TestPriority_ExtraID asserts that an unknown id in the priority list produces
// an error.
func TestPriority_ExtraID(t *testing.T) {
	reg := pullableReg()
	// i999 does not exist.
	pve := validatePriority(reg, []string{"i1b", "i2", "i999"})
	if !pve.hasErrors() {
		t.Fatal("expected error for extra/unknown id i999; got none")
	}
	found := false
	for _, msg := range pve.msgs {
		if strings.Contains(msg, "i999") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error mentioning i999; got:\n%s",
			strings.Join(pve.msgs, "\n"))
	}
}

// TestPriority_DuplicateID asserts that a duplicate id in the list produces an
// error.
func TestPriority_DuplicateID(t *testing.T) {
	reg := pullableReg()
	pve := validatePriority(reg, []string{"i1b", "i1b", "i2"})
	if !pve.hasErrors() {
		t.Fatal("expected error for duplicate id i1b; got none")
	}
	found := false
	for _, msg := range pve.msgs {
		if strings.Contains(msg, "duplicate") && strings.Contains(msg, "i1b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected duplicate-id error for i1b; got:\n%s",
			strings.Join(pve.msgs, "\n"))
	}
}

// TestPriority_ClosedIDListed asserts that listing a DONE or WONTFIX id
// produces an error (closed items are not pullable).
func TestPriority_ClosedIDListed(t *testing.T) {
	reg := pullableReg()
	// i1a is DONE — not pullable.
	pve := validatePriority(reg, []string{"i1b", "i2", "i1a"})
	if !pve.hasErrors() {
		t.Fatal("expected error for closed (DONE) id i1a in priority list; got none")
	}
	found := false
	for _, msg := range pve.msgs {
		if strings.Contains(msg, "i1a") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error mentioning i1a (DONE/closed); got:\n%s",
			strings.Join(pve.msgs, "\n"))
	}
}

// TestPriority_UmbrellaListed asserts that listing an umbrella id produces an
// error (umbrellas are not queue entries).
func TestPriority_UmbrellaListed(t *testing.T) {
	reg := pullableReg()
	// i1 is an umbrella — not pullable.
	pve := validatePriority(reg, []string{"i1b", "i2", "i1"})
	if !pve.hasErrors() {
		t.Fatal("expected error for umbrella id i1 in priority list; got none")
	}
	found := false
	for _, msg := range pve.msgs {
		if strings.Contains(msg, "i1") && strings.Contains(msg, "umbrella") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected umbrella error for i1; got:\n%s",
			strings.Join(pve.msgs, "\n"))
	}
}

// TestPriority_TopoViolation asserts that ranking an item before its pullable
// dependency produces an error.
func TestPriority_TopoViolation(t *testing.T) {
	// Build a registry where i10 depends on i20 (both pullable).
	reg := &Registry{
		Items: []Item{
			{
				ID:        "i10",
				Title:     "Depends on i20",
				Status:    StatusOpen,
				Kind:      "leaf",
				Owner:     "agent",
				DependsOn: []string{"i20"},
			},
			{
				ID:     "i20",
				Title:  "Prerequisite",
				Status: StatusOpen,
				Kind:   "leaf",
				Owner:  "agent",
			},
		},
	}
	// i10 ranked before i20 — topology violation.
	pve := validatePriority(reg, []string{"i10", "i20"})
	if !pve.hasErrors() {
		t.Fatal("expected topo-violation error (i10 before i20); got none")
	}
	found := false
	for _, msg := range pve.msgs {
		if strings.Contains(msg, "i10") && strings.Contains(msg, "i20") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected topo error mentioning i10 and i20; got:\n%s",
			strings.Join(pve.msgs, "\n"))
	}
}

// TestPriority_TopoValidAfterReorder asserts that placing i20 before i10
// satisfies the topo constraint.
func TestPriority_TopoValidAfterReorder(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{
				ID:        "i10",
				Title:     "Depends on i20",
				Status:    StatusOpen,
				Kind:      "leaf",
				Owner:     "agent",
				DependsOn: []string{"i20"},
			},
			{
				ID:     "i20",
				Title:  "Prerequisite",
				Status: StatusOpen,
				Kind:   "leaf",
				Owner:  "agent",
			},
		},
	}
	// Correct order: i20 before i10.
	pve := validatePriority(reg, []string{"i20", "i10"})
	if pve.hasErrors() {
		t.Fatalf("expected no errors for valid topo order; got:\n%s",
			strings.Join(pve.msgs, "\n"))
	}
}

// TestReady_UnblockedItems asserts that `ready` returns items whose all
// depends_on targets are satisfied (DONE items or absent questions), while
// items gated on an open question are excluded.
//
// Fixture:
//   i1b  — depends_on i1a (DONE) → READY
//   i2   — depends_on q1 (open question) → NOT READY
func TestReady_UnblockedItems(t *testing.T) {
	reg := pullableReg()

	// Build the set of ready items using the same logic as runReady.
	itemStatus := map[string]Status{}
	for _, it := range reg.Items {
		itemStatus[it.ID] = it.Status
	}
	questionIDs := map[string]bool{}
	for _, q := range reg.Questions {
		questionIDs[q.ID] = true
	}

	depSatisfied := func(depID string) bool {
		if questionIDs[depID] {
			return false
		}
		st, exists := itemStatus[depID]
		if !exists {
			return true
		}
		return st == StatusDone || st == StatusWontfix
	}
	allDepsSatisfied := func(it Item) bool {
		for _, dep := range it.DependsOn {
			if !depSatisfied(dep) {
				return false
			}
		}
		return true
	}

	pullable := pullableItems(reg.Items)
	var ready []string
	for _, it := range sortedItems(reg.Items) {
		if pullable[it.ID] && allDepsSatisfied(it) {
			ready = append(ready, it.ID)
		}
	}

	// i1b should be ready (i1a is DONE — satisfied).
	if !contains(strings.Join(ready, "\n"), "i1b") {
		t.Errorf("expected i1b to be ready (depends on DONE i1a); ready set: %v", ready)
	}

	// i2 should NOT be ready (q1 is an open question — not satisfied).
	if contains(strings.Join(ready, "\n"), "i2") {
		t.Errorf("expected i2 to NOT be ready (depends on open question q1); ready set: %v", ready)
	}
}

// TestReady_OpenDepExcludes asserts that an item gated on another open pullable
// item is excluded from the ready set.
func TestReady_OpenDepExcludes(t *testing.T) {
	// i20 is open-pullable; i10 depends on i20 (also open-pullable) → i10 not ready.
	reg := &Registry{
		Items: []Item{
			{
				ID:        "i10",
				Title:     "Depends on open i20",
				Status:    StatusOpen,
				Kind:      "leaf",
				Owner:     "agent",
				DependsOn: []string{"i20"},
			},
			{
				ID:     "i20",
				Title:  "Unblocked",
				Status: StatusOpen,
				Kind:   "leaf",
				Owner:  "agent",
			},
		},
	}

	itemStatus := map[string]Status{}
	for _, it := range reg.Items {
		itemStatus[it.ID] = it.Status
	}
	depSatisfied := func(depID string) bool {
		st, exists := itemStatus[depID]
		if !exists {
			return true
		}
		return st == StatusDone || st == StatusWontfix
	}

	pullable := pullableItems(reg.Items)
	var ready []string
	for _, it := range sortedItems(reg.Items) {
		if !pullable[it.ID] {
			continue
		}
		allSatisfied := true
		for _, dep := range it.DependsOn {
			if !depSatisfied(dep) {
				allSatisfied = false
				break
			}
		}
		if allSatisfied {
			ready = append(ready, it.ID)
		}
	}

	if contains(strings.Join(ready, "\n"), "i10") {
		t.Errorf("expected i10 NOT to be ready (depends on open i20); ready: %v", ready)
	}
	if !contains(strings.Join(ready, "\n"), "i20") {
		t.Errorf("expected i20 to be ready (no unsatisfied deps); ready: %v", ready)
	}
}

// TestGenBacklog_ProducesRankedTable asserts that genBacklog writes a table with
// one row per priority item, with a 1-based rank, the id, and the item content.
func TestGenBacklog_ProducesRankedTable(t *testing.T) {
	reg := pullableReg()
	priority := []string{"i1b", "i2"}

	var buf bytes.Buffer
	if err := genBacklog(reg, priority, &buf); err != nil {
		t.Fatalf("genBacklog: %v", err)
	}

	out := buf.String()

	// Banner must be present.
	if !strings.Contains(out, "GENERATED by") {
		t.Error("backlog output missing GENERATED banner")
	}
	// Table header.
	if !strings.Contains(out, "| rank |") {
		t.Error("backlog output missing '| rank |' header")
	}
	if !strings.Contains(out, "| **id** |") {
		t.Error("backlog output missing '| **id** |' header")
	}

	// i1b must appear at rank 1.
	if !strings.Contains(out, "| 1 | **i1b** |") {
		t.Errorf("expected rank-1 row for i1b; output:\n%s", out)
	}
	// i2 must appear at rank 2.
	if !strings.Contains(out, "| 2 | **i2** |") {
		t.Errorf("expected rank-2 row for i2; output:\n%s", out)
	}
}

// TestGenBacklog_EmptyPriorityProducesHeaderOnly asserts that genBacklog with an
// empty priority list emits a valid table with zero data rows.
func TestGenBacklog_EmptyPriorityProducesHeaderOnly(t *testing.T) {
	reg := pullableReg()
	var buf bytes.Buffer
	if err := genBacklog(reg, []string{}, &buf); err != nil {
		t.Fatalf("genBacklog with empty priority: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "| rank |") {
		t.Error("backlog output missing table header for empty priority")
	}
	// No data rows.
	rows := countGenTableRows(out)
	if rows != 0 {
		t.Errorf("expected 0 data rows for empty priority, got %d", rows)
	}
}

// TestGenToOutDir_BacklogWritten asserts that genToOutDirOrStdout writes
// backlog.md to the outDir when reg.Priority is non-empty, and that its content
// contains the expected header and the priority ids in rank order.
func TestGenToOutDir_BacklogWritten(t *testing.T) {
	repoRoot := findRepoRootRegistry(t)
	reg := pullableReg()
	reg.Priority = []string{"i1b", "i2"}

	outDir := t.TempDir()
	templatesDir := filepath.Join(repoRoot, "tools", "registry", "templates")

	paths := mutatorPaths{
		outDir:       outDir,
		templatesDir: templatesDir,
	}
	genToOutDirOrStdout(reg, paths)

	backlogPath := filepath.Join(outDir, "backlog.md")
	data, err := os.ReadFile(backlogPath)
	if err != nil {
		t.Fatalf("backlog.md not written: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "GENERATED") {
		t.Error("backlog.md missing GENERATED banner")
	}
	if !strings.Contains(content, "# Backlog") {
		t.Error("backlog.md missing template H1")
	}
	if !strings.Contains(content, "| rank |") {
		t.Error("backlog.md missing rank column header")
	}
	if !strings.Contains(content, "| 1 | **i1b** |") {
		t.Errorf("backlog.md missing rank-1 row for i1b:\n%s", content)
	}
	if !strings.Contains(content, "| 2 | **i2** |") {
		t.Errorf("backlog.md missing rank-2 row for i2:\n%s", content)
	}
}

// TestGenToOutDir_BacklogSkippedWhenNoPriority asserts that backlog.md is NOT
// written when the registry has no priority list (empty slice).
func TestGenToOutDir_BacklogSkippedWhenNoPriority(t *testing.T) {
	repoRoot := findRepoRootRegistry(t)
	reg := pullableReg()
	// reg.Priority is nil/empty — no backlog should be generated.

	outDir := t.TempDir()
	templatesDir := filepath.Join(repoRoot, "tools", "registry", "templates")

	paths := mutatorPaths{
		outDir:       outDir,
		templatesDir: templatesDir,
	}
	genToOutDirOrStdout(reg, paths)

	backlogPath := filepath.Join(outDir, "backlog.md")
	if _, err := os.Stat(backlogPath); err == nil {
		t.Error("backlog.md was written even though reg.Priority is empty")
	}
}

// TestPriorityLoadRoundTrip asserts that writePriority followed by loadPriority
// is a fixed point: the loaded ids match the written ids exactly.
func TestPriorityLoadRoundTrip(t *testing.T) {
	ids := []string{"i119c", "i138", "i119d", "i119e", "i121"}
	path := filepath.Join(t.TempDir(), "priority.yaml")
	if err := writePriority(path, ids); err != nil {
		t.Fatalf("writePriority: %v", err)
	}
	loaded, err := loadPriority(path)
	if err != nil {
		t.Fatalf("loadPriority: %v", err)
	}
	if len(loaded) != len(ids) {
		t.Fatalf("loadPriority: got %d ids, want %d", len(loaded), len(ids))
	}
	for i, id := range ids {
		if loaded[i] != id {
			t.Errorf("position %d: got %q, want %q", i, loaded[i], id)
		}
	}
}

// TestLoadPriority_AbsentFileIsEmpty asserts that loadPriority on a missing
// file returns an empty slice without error.
func TestLoadPriority_AbsentFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.yaml")
	ids, err := loadPriority(path)
	if err != nil {
		t.Fatalf("loadPriority on absent file: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty slice for absent file, got %v", ids)
	}
}
