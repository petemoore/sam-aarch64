package main

// Tests for i275 (never silently fail) and i279 (self-explanatory priority).
//
// i275: every validation failure must (a) print an explicit reason to STDERR so it
// survives stdout piping, and (b) exit NON-ZERO so && chains and scripts stop.
//
// i279: after `add` and `prioritize`, the CLI reports the resulting ready-queue
// position + reasons so agents never have to read the source to understand why an
// item isn't at position 1.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireBinary returns the path to the built registry binary, failing the test
// if it does not exist (i253: a missing artifact must fail, not skip).
func requireBinary(t *testing.T) string {
	t.Helper()
	repoRoot := findRepoRootRegistry(t)
	bin := filepath.Join(repoRoot, "build", "registry")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("registry binary not found at %s; run `make registry-gen` first (i253: missing artifact must fail not skip)", bin)
	}
	return bin
}

// runRegistry runs the registry binary with the given args and returns stdout,
// stderr, and the exit code. It never uses t.Fatal itself so callers can assert.
func runRegistry(bin string, env []string, args ...string) (stdout, stderr string, exitCode int) {
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// baseEnv returns an env suitable for the test binary: it points REGISTRY_ITEMS,
// REGISTRY_QUESTIONS, REGISTRY_PRIORITY, and REGISTRY_DIR at the given tempDir so
// the live registry is never touched. The templates are taken from the real source.
func baseEnv(t *testing.T, tempDir string) []string {
	t.Helper()
	repoRoot := findRepoRootRegistry(t)
	templateDir := filepath.Join(repoRoot, "tools", "registry", "templates")

	// Copy the fixture files into the tempDir so mutations work.
	copyFile(t, filepath.Join(repoRoot, "tools", "registry", "testdata", "items.yaml"),
		filepath.Join(tempDir, "items.yaml"))
	copyFile(t, filepath.Join(repoRoot, "tools", "registry", "testdata", "questions.yaml"),
		filepath.Join(tempDir, "questions.yaml"))

	// Write a minimal valid priority.yaml with the pullable items from the fixture.
	// The fixture has i1b and i2 as the two pullable items.
	if err := writePriority(filepath.Join(tempDir, "priority.yaml"), []string{"i1b", "i2"}); err != nil {
		t.Fatalf("baseEnv: writePriority: %v", err)
	}

	env := os.Environ()
	env = append(env,
		"REGISTRY_ITEMS="+filepath.Join(tempDir, "items.yaml"),
		"REGISTRY_QUESTIONS="+filepath.Join(tempDir, "questions.yaml"),
		"REGISTRY_PRIORITY="+filepath.Join(tempDir, "priority.yaml"),
		"REGISTRY_DIR="+tempDir,
		"REGISTRY_TEMPLATES="+templateDir,
	)
	return env
}

// --- i275: never-silent-fail tests ---

// TestNeverSilentFail_AddMissingTitle asserts that `add --owner agent` (missing
// --title) exits non-zero and prints a reason to stderr.
func TestNeverSilentFail_AddMissingTitle(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)
	_, stderr, code := runRegistry(bin, env, "add", "--owner", "agent")
	if code == 0 {
		t.Fatalf("expected non-zero exit for add missing --title; got exit 0")
	}
	if !strings.Contains(stderr, "--title") {
		t.Errorf("expected stderr to mention --title; got: %q", stderr)
	}
}

// TestNeverSilentFail_AddMissingOwner asserts that `add --title "…"` (missing
// --owner) exits non-zero and prints a reason to stderr.
func TestNeverSilentFail_AddMissingOwner(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)
	_, stderr, code := runRegistry(bin, env, "add", "--title", "something", "--status", "OPEN")
	if code == 0 {
		t.Fatalf("expected non-zero exit for add missing --owner; got exit 0")
	}
	if !strings.Contains(stderr, "--owner") {
		t.Errorf("expected stderr to mention --owner; got: %q", stderr)
	}
}

// TestNeverSilentFail_SetDescOverLimit asserts that `set-desc --desc <too long>`
// exits non-zero and prints a reason to stderr (i276 + i275 together).
func TestNeverSilentFail_SetDescOverLimit(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)
	overLimit := strings.Repeat("x", 4097)
	_, stderr, code := runRegistry(bin, env, "set-desc", "--id", "i1b", "--desc", overLimit)
	if code == 0 {
		t.Fatalf("expected non-zero exit for set-desc over 4096 chars; got exit 0")
	}
	if !strings.Contains(stderr, "4096") {
		t.Errorf("expected stderr to mention 4096-char limit; got: %q", stderr)
	}
}

// TestNeverSilentFail_SetDescJustUnderLimit asserts that a 4096-char desc
// succeeds (boundary: new limit is 4096, not 2000 — i276).
func TestNeverSilentFail_SetDescJustUnderLimit(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)
	atLimit := strings.Repeat("x", 4096)
	_, stderr, code := runRegistry(bin, env, "set-desc", "--id", "i1b", "--desc", atLimit)
	if code != 0 {
		t.Fatalf("expected exit 0 for set-desc at the 4096-char limit; got %d\nstderr:\n%s", code, stderr)
	}
}

// TestNeverSilentFail_PrioritizeUnknownID asserts that `prioritize --id iXXX
// --to-top` on an id not in the priority queue exits non-zero and prints to stderr.
func TestNeverSilentFail_PrioritizeUnknownID(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)
	_, stderr, code := runRegistry(bin, env, "prioritize", "--id", "i9999", "--to-top")
	if code == 0 {
		t.Fatalf("expected non-zero exit for prioritize with unknown id; got exit 0")
	}
	if !strings.Contains(stderr, "i9999") {
		t.Errorf("expected stderr to mention the unknown id i9999; got: %q", stderr)
	}
}

// TestNeverSilentFail_SetStatusUnknownID asserts that `set-status --id iXXX`
// exits non-zero and prints a reason to stderr.
func TestNeverSilentFail_SetStatusUnknownID(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)
	_, stderr, code := runRegistry(bin, env, "set-status", "--id", "i9999", "--status", "DONE")
	if code == 0 {
		t.Fatalf("expected non-zero exit for set-status with unknown id; got exit 0")
	}
	if !strings.Contains(stderr, "i9999") || !strings.Contains(stderr, "not found") {
		t.Errorf("expected stderr to mention i9999 not found; got: %q", stderr)
	}
}

// TestNeverSilentFail_DepAddUnknownID asserts that `dep add --id iXXX --on i1b`
// exits non-zero and prints a reason to stderr.
func TestNeverSilentFail_DepAddUnknownID(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)
	_, stderr, code := runRegistry(bin, env, "dep", "add", "--id", "i9999", "--on", "i1b")
	if code == 0 {
		t.Fatalf("expected non-zero exit for dep add with unknown --id; got exit 0")
	}
	if !strings.Contains(stderr, "i9999") {
		t.Errorf("expected stderr to mention i9999; got: %q", stderr)
	}
}

// TestNeverSilentFail_AnswerUnknownQuestion asserts that `answer --id qXXX`
// exits non-zero and prints a reason to stderr.
func TestNeverSilentFail_AnswerUnknownQuestion(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)
	_, stderr, code := runRegistry(bin, env, "answer", "--id", "q9999")
	if code == 0 {
		t.Fatalf("expected non-zero exit for answer with unknown question id; got exit 0")
	}
	if !strings.Contains(stderr, "q9999") {
		t.Errorf("expected stderr to mention q9999; got: %q", stderr)
	}
}

// TestNeverSilentFail_UnknownSubcommand asserts that an unknown subcommand exits
// non-zero (exit 2) and prints a usage message to stderr.
func TestNeverSilentFail_UnknownSubcommand(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)
	_, stderr, code := runRegistry(bin, env, "xyzzy-no-such-command")
	if code == 0 {
		t.Fatalf("expected non-zero exit for unknown subcommand; got exit 0")
	}
	if !strings.Contains(stderr, "unknown subcommand") {
		t.Errorf("expected stderr to mention unknown subcommand; got: %q", stderr)
	}
}

// --- i279: self-explanatory priority tests ---

// TestReportReadyPosition_TopAgentItem asserts that after prioritize --to-top of
// the first agent-actionable item, the CLI reports "ready-position 1 of N".
func TestReportReadyPosition_TopAgentItem(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)
	// In the fixture: i1b is unblocked (i1a is DONE), i2 is blocked (open q1).
	// So i1b should be the only ready agent item.
	stdout, _, code := runRegistry(bin, env, "prioritize", "--id", "i1b", "--to-top")
	if code != 0 {
		t.Fatalf("prioritize --to-top exited non-zero: %d\nstdout: %s", code, stdout)
	}
	// The position report line should appear in stdout.
	if !strings.Contains(stdout, "ready-position") {
		t.Errorf("expected stdout to contain 'ready-position' after prioritize; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "i1b") {
		t.Errorf("expected stdout to mention i1b in the position report; got:\n%s", stdout)
	}
}

// TestReportReadyPosition_Add asserts that after `add`, the CLI reports the
// resulting ready-position of the newly added item.
func TestReportReadyPosition_Add(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)
	stdout, _, code := runRegistry(bin, env, "add",
		"--title", "Test self-explanatory item",
		"--status", "OPEN",
		"--owner", "agent",
	)
	if code != 0 {
		t.Fatalf("add exited non-zero: %d\nstdout: %s", code, stdout)
	}
	if !strings.Contains(stdout, "ready-position") {
		t.Errorf("expected stdout to contain 'ready-position' after add; got:\n%s", stdout)
	}
}

// TestReportReadyPosition_BlockedItem asserts that for an item that is blocked,
// the CLI reports it is dependency-blocked (not "ready-position N of M").
func TestReportReadyPosition_BlockedItem(t *testing.T) {
	// i2 depends on open question q1 — it is blocked and should not appear in ready.
	reg := &Registry{
		Items: []Item{
			{
				ID:     "i10",
				Title:  "Free item",
				Status: StatusOpen,
				Kind:   "leaf",
				Owner:  "agent",
			},
			{
				ID:        "i20",
				Title:     "Blocked item",
				Status:    StatusOpen,
				Kind:      "leaf",
				Owner:     "agent",
				DependsOn: []string{"q1"},
				Refs:      []string{"q1"},
			},
		},
		Questions: []Question{
			{ID: "q1", Body: "Which approach?", Owner: "pete"},
		},
		Priority: []string{"i20", "i10"},
	}

	// i20 is dep-blocked; reportReadyPosition should say "dependency-blocked".
	// Capture stdout to assert.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	reportReadyPosition(reg, "i20")
	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if !strings.Contains(out, "dependency-blocked") && !strings.Contains(out, "blocked") {
		t.Errorf("expected blocked item to be described as dependency-blocked; got: %q", out)
	}
	if strings.Contains(out, "ready-position") {
		t.Errorf("expected blocked item NOT to report ready-position; got: %q", out)
	}
}

// TestReportReadyPosition_PeteItemExplained asserts that when an owner:pete item
// is in the ready list and the target is an agent item behind it, the explanation
// mentions "owner:pete" in the output.
func TestReportReadyPosition_PeteItemExplained(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{
				ID:     "i10",
				Title:  "Pete item",
				Status: StatusOpen,
				Kind:   "leaf",
				Owner:  "pete",
			},
			{
				ID:     "i20",
				Title:  "Agent item",
				Status: StatusOpen,
				Kind:   "leaf",
				Owner:  "agent",
			},
		},
		Priority: []string{"i10", "i20"},
	}

	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	reportReadyPosition(reg, "i20")
	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if !strings.Contains(out, "ready-position") {
		t.Errorf("expected position report for agent item i20; got: %q", out)
	}
	// The explanation should mention owner:pete items above.
	if !strings.Contains(out, "pete") {
		t.Errorf("expected explanation to mention owner:pete items above; got: %q", out)
	}
}
