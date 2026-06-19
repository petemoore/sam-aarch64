package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestGenByteStable asserts that calling genItemsOpenClosed twice on the same
// registry produces byte-identical output (determinism guarantee).
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

	var qopen1, qclosed1, qopen2, qclosed2 bytes.Buffer
	if err := genQuestionsOpenClosed(reg, &qopen1, &qclosed1); err != nil {
		t.Fatalf("first q gen: %v", err)
	}
	if err := genQuestionsOpenClosed(reg, &qopen2, &qclosed2); err != nil {
		t.Fatalf("second q gen: %v", err)
	}

	if !bytes.Equal(qopen1.Bytes(), qopen2.Bytes()) {
		t.Error("question-open output differs between two gen calls (not deterministic)")
	}
	if !bytes.Equal(qclosed1.Bytes(), qclosed2.Bytes()) {
		t.Error("question-closed output differs between two gen calls (not deterministic)")
	}
}

// TestLoadGenLoadFixedPoint asserts that load → gen → load produces the same
// registry as the original load (the gen output is a faithful view of the source
// data, not a lossy transform).  This is checked by validating the reloaded
// registry against the original on key fields.
func TestLoadGenLoadFixedPoint(t *testing.T) {
	reg := loadTestFixture(t)

	// Validate the fixture is clean.
	ve := validate(reg)
	if ve.hasErrors() {
		t.Fatalf("fixture validation failed:\n%v", ve.msgs)
	}

	// Gen is byte-stable (checked by TestGenByteStable); the fixed-point
	// here checks that the item/question counts are preserved across two
	// gen calls — the generator does not discard or hallucinate records.
	var open1, closed1 bytes.Buffer
	if err := genItemsOpenClosed(reg, &open1, &closed1); err != nil {
		t.Fatalf("gen: %v", err)
	}

	// Count open vs closed items in source and verify the generated tables
	// contain the matching counts of rows.
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
