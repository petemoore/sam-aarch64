package main

// Tests for i341: mutation output is quiet (no full-view dump to stdout) and
// ends with the exact staging line, so an agent can confirm the operation and
// stage the complete file set without extra round-trips. Exec-based (like
// never_fail_silent_test.go) because the assertions are about the process's
// actual stdout.

import (
	"strings"
	"testing"
)

// TestMutationOutput_QuietWithStageHint asserts that a mutation run in stdout
// mode (no REGISTRY_OUTDIR) does NOT dump the generated views (a bare
// mutation used to spill all four full views — hundreds of KB — into the
// caller's output) and DOES end with the staging line.
func TestMutationOutput_QuietWithStageHint(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)

	stdout, _, code := runRegistry(bin, env, "set-status", "--id", "i2", "--status", "IN_PROGRESS")
	if code != 0 {
		t.Fatalf("set-status exited non-zero: %d\nstdout: %s", code, stdout)
	}
	if strings.Contains(stdout, "=== item-registry-open ===") {
		t.Errorf("mutation stdout contains the full view dump (should be quiet); got %d bytes", len(stdout))
	}
	if !strings.Contains(stdout, "registry: i2 status → IN_PROGRESS") {
		t.Errorf("expected the one-line mutation summary; got:\n%s", stdout)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "stage with: make registry && git add registry/items.yaml") {
		t.Errorf("expected the output to END with the staging line; last line: %q", last)
	}
}

// TestMutationOutput_StageHintOnAdd asserts add's output ends with the staging
// line too (after the ready-position report).
func TestMutationOutput_StageHintOnAdd(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)

	stdout, _, code := runRegistry(bin, env, "add",
		"--title", "Stage hint test item",
		"--status", "OPEN",
		"--owner", "agent",
	)
	if code != 0 {
		t.Fatalf("add exited non-zero: %d\nstdout: %s", code, stdout)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "stage with: make registry && git add") {
		t.Errorf("expected add output to END with the staging line; last line: %q", last)
	}
}

// TestGenStdoutDumpStillWorks asserts the explicit `gen` command (positional
// args, no REGISTRY_OUTDIR) still prints the four views to stdout — the quiet
// behavior applies to mutations only; gen's stdout dump is its documented
// contract.
func TestGenStdoutDumpStillWorks(t *testing.T) {
	bin := requireBinary(t)
	td := t.TempDir()
	env := baseEnv(t, td)

	stdout, stderr, code := runRegistry(bin, env, "gen", td+"/items.yaml", td+"/questions.yaml")
	if code != 0 {
		t.Fatalf("gen exited non-zero: %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "=== item-registry-open ===") {
		t.Errorf("expected gen (stdout mode) to dump the views; got:\n%.400s", stdout)
	}
}
