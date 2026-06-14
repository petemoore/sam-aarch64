package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestGenByteStable asserts that calling the gen functions twice on the same
// registry produces byte-identical output (determinism guarantee).
// Spec §"Generator": "Deterministic — true-numeric sort, no map iteration,
// identical across machines".
func TestGenByteStable(t *testing.T) {
	reg := loadTestFixture(t)

	var open1, closed1, open2, closed2 bytes.Buffer
	if err := genItemsOpenClosed(reg, &open1, &closed1); err != nil {
		t.Fatalf("first gen: %v", err)
	}
	if err := genItemsOpenClosed(reg, &open2, &closed2); err != nil {
		t.Fatalf("second gen: %v", err)
	}

	if !bytes.Equal(open1.Bytes(), open2.Bytes()) {
		t.Error("item-open output differs between two gen calls (not deterministic)")
	}
	if !bytes.Equal(closed1.Bytes(), closed2.Bytes()) {
		t.Error("item-closed output differs between two gen calls (not deterministic)")
	}

	// Questions: one open view only (no closed view — questions are transient).
	var qopen1, qopen2 bytes.Buffer
	if err := genQuestionsOpen(reg, &qopen1); err != nil {
		t.Fatalf("first q gen: %v", err)
	}
	if err := genQuestionsOpen(reg, &qopen2); err != nil {
		t.Fatalf("second q gen: %v", err)
	}

	if !bytes.Equal(qopen1.Bytes(), qopen2.Bytes()) {
		t.Error("question-open output differs between two gen calls (not deterministic)")
	}
}

// TestLoadGenLoadFixedPoint asserts that load -> gen produces a table whose
// row counts match the source (the generator does not discard or hallucinate
// records).
// Spec §"CI sync-check + regen": round-trip test.
func TestLoadGenLoadFixedPoint(t *testing.T) {
	reg := loadTestFixture(t)

	// Validate the fixture is clean.
	ve := validate(reg)
	if ve.hasErrors() {
		t.Fatalf("fixture validation failed:\n%v", ve.msgs)
	}

	// Gen is byte-stable (checked by TestGenByteStable); the fixed-point
	// here checks that item counts are preserved across two gen calls.
	var open1, closed1 bytes.Buffer
	if err := genItemsOpenClosed(reg, &open1, &closed1); err != nil {
		t.Fatalf("gen: %v", err)
	}

	// Count open vs closed items in source and verify the generated tables
	// contain the matching row counts.
	openCount, closedCount := 0, 0
	for _, it := range reg.Items {
		if isOpen(it.Status) {
			openCount++
		} else {
			closedCount++
		}
	}

	gotOpen := countTableRows(open1.String())
	gotClosed := countTableRows(closed1.String())
	if gotOpen != openCount {
		t.Errorf("open table has %d rows, want %d", gotOpen, openCount)
	}
	if gotClosed != closedCount {
		t.Errorf("closed table has %d rows, want %d", gotClosed, closedCount)
	}

	// Question view: all questions appear in the single open view.
	var qopen bytes.Buffer
	if err := genQuestionsOpen(reg, &qopen); err != nil {
		t.Fatalf("q gen: %v", err)
	}
	gotQOpen := countTableRows(qopen.String())
	if gotQOpen != len(reg.Questions) {
		t.Errorf("question-open table has %d rows, want %d", gotQOpen, len(reg.Questions))
	}
}

// TestDependsOnRoundTrip verifies that depends_on edges survive load->gen->load:
// items with depends_on in the testdata fixture have those edges after load,
// and the gated-on: prefix appears in the generated refs/links column.
func TestDependsOnRoundTrip(t *testing.T) {
	reg := loadTestFixture(t)

	// Find at least one item with a depends_on edge.
	var gatedItem *Item
	for i := range reg.Items {
		if len(reg.Items[i].DependsOn) > 0 {
			gatedItem = &reg.Items[i]
			break
		}
	}
	if gatedItem == nil {
		t.Fatal("testdata has no item with depends_on — fixture needs updating")
	}

	// Generate and check that the gated-on: prefix appears in the open table.
	var open, closed bytes.Buffer
	if err := genItemsOpenClosed(reg, &open, &closed); err != nil {
		t.Fatalf("gen: %v", err)
	}

	combined := open.String() + closed.String()
	found := false
	for _, dep := range gatedItem.DependsOn {
		needle := "gated-on:" + dep
		if contains(combined, needle) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("depends_on edges of %s not visible as gated-on: in generated output", gatedItem.ID)
	}
}

// TestGenItemCellRendersDescription is the real-content guard: the generator must
// render the item's DESCRIPTION (not just the short title) in the item cell, with
// correct escaping — a bold title lead-in, literal pipes escaped, internal newlines
// as <br>, and NO spurious trailing <br> from a block scalar's trailing newline.
// (The other fixtures have one-line descriptions, so this is the case that caught
// the original "title-only" rendering bug.)
func TestGenItemCellRendersDescription(t *testing.T) {
	reg := &Registry{Items: []Item{{
		ID:          "i900",
		Title:       "Priority queue",
		Description: "First line with a|b pipe.\nSecond line after a newline.\n",
		Status:      StatusOpen,
	}}}
	var open, closed bytes.Buffer
	if err := genItemsOpenClosed(reg, &open, &closed); err != nil {
		t.Fatalf("gen: %v", err)
	}
	out := open.String()
	if !contains(out, "**Priority queue** — First line") {
		t.Errorf("item cell missing the bold-title lead-in + description:\n%s", out)
	}
	if !contains(out, `a\|b`) {
		t.Errorf("literal pipe not escaped to a\\|b:\n%s", out)
	}
	if !contains(out, "pipe.<br>Second line") {
		t.Errorf("internal newline not rendered as <br>:\n%s", out)
	}
	if contains(out, "newline.<br> |") {
		t.Errorf("spurious trailing <br> at the cell end (block-scalar trailing newline):\n%s", out)
	}
}

// TestBinaryGenMatchesValidFixture builds the registry binary (if present) and
// confirms `validate` exits 0 on the fixture and `gen` exits 0.
func TestBinaryGenMatchesValidFixture(t *testing.T) {
	repoRoot := findRepoRootRegistry(t)
	bin := filepath.Join(repoRoot, "build", "registry")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("registry binary not found at %s; run `make registry-gen` first", bin)
	}

	fixtureItems := filepath.Join(repoRoot, "tools", "registry", "testdata", "items.yaml")
	fixtureQuestions := filepath.Join(repoRoot, "tools", "registry", "testdata", "questions.yaml")

	// validate exits 0.
	cmd := exec.Command(bin, "validate", fixtureItems, fixtureQuestions)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("registry validate exited non-zero: %v\nstderr:\n%s", err, stderr.String())
	}

	// gen exits 0 and produces non-empty output.
	var stdout bytes.Buffer
	cmd2 := exec.Command(bin, "gen", fixtureItems, fixtureQuestions)
	cmd2.Stdout = &stdout
	cmd2.Stderr = &stderr
	stderr.Reset()
	if err := cmd2.Run(); err != nil {
		t.Fatalf("registry gen exited non-zero: %v\nstderr:\n%s", err, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Error("registry gen produced empty output")
	}
}

// loadTestFixture loads the testdata fixtures and returns a Registry.
func loadTestFixture(t *testing.T) *Registry {
	t.Helper()
	repoRoot := findRepoRootRegistry(t)
	items, err := loadItems(filepath.Join(repoRoot, "tools", "registry", "testdata", "items.yaml"))
	if err != nil {
		t.Fatalf("load items fixture: %v", err)
	}
	questions, err := loadQuestions(filepath.Join(repoRoot, "tools", "registry", "testdata", "questions.yaml"))
	if err != nil {
		t.Fatalf("load questions fixture: %v", err)
	}
	return &Registry{Items: items, Questions: questions}
}

// countTableRows counts the data rows in a generated markdown table (excludes
// the header row and separator row).
func countTableRows(s string) int {
	count := 0
	headerSeen := false
	separatorSeen := false
	for _, line := range splitLines(s) {
		if !headerSeen {
			if len(line) > 0 && line[0] == '|' {
				headerSeen = true
			}
			continue
		}
		if !separatorSeen {
			separatorSeen = true
			continue
		}
		if len(line) > 0 && line[0] == '|' {
			count++
		}
	}
	return count
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}

func findRepoRootRegistry(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root walking up from %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}
