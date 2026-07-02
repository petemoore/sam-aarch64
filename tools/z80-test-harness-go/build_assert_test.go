// build_assert_test.go — structural guard against stale-artifact false passes.
//
// The measurement / byte-match / oracle tests in this package read pre-built
// SAM-side artifacts from build/ (assembler.bin, disasm.bin, enctab.enc, …).
// Historically each test only checked that the file *existed* (os.Stat) before
// reading it — never that it was *fresh*. A failed `pyz80`/`make` build leaves
// the previous .bin in place, so a byte-identical or T-state check reads the
// STALE artifact and reports a false PASS. This bit the project across four
// sessions (sha256-fastest-z80, i88-tls-go-authority, i48c-expr-parser-bricks,
// and a timestamp-keyed variant). See registry item i116.
//
// The fix is structural, not advisory: TestMain rebuilds the full artifact set
// through `make` BEFORE any test runs, and ASSERTS the build exited 0. The
// Makefile's own prerequisite graph (build/*.bin : src/*.asm) rebuilds anything
// stale and no-ops when fresh, so every subsequent read in the suite sees an
// artifact that is current with its sources. If the build fails (a pyz80 error,
// a missing toolchain, a syntax error in a src/*.asm), TestMain aborts the
// whole suite with a non-zero exit — the tests never run against a stale .bin,
// so the false-pass mode is impossible.
//
// Override: set HARNESS_SKIP_BUILD=1 to bypass the pre-build (for environments
// without make/pyz80 on PATH, or to test a hand-placed artifact). This is an
// explicit, visible opt-out — not a silent default — and it prints a warning.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// harnessArtifactTargets invokes the Makefile's harness-artifacts aggregate —
// the single source of truth for the artifact set the Go test suite reads
// from build/. A new build/* artifact read by a test must be added to that
// aggregate's prerequisites in the Makefile; nothing is listed here (a
// duplicated per-target list drifted out of sync with the Makefile — i309).
var harnessArtifactTargets = []string{"harness-artifacts"}

// findRepoRoot walks up from the current working directory to the repo root,
// identified by the presence of a Makefile. Unlike repoRoot(t) it takes no
// *testing.T, so it is usable from TestMain (where no *T exists yet).
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find repo root (no Makefile walking up from %s)", dir)
		}
		dir = parent
	}
}

// buildArtifacts runs `make <targets...>` from the repo root and returns an
// error if the build exits non-zero. Because the Makefile declares each
// build/*.bin's src/*.asm prerequisites, make rebuilds anything stale and
// no-ops when everything is fresh (~0.5s warm). On a build failure the stale
// .bin is left on disk — but this returns the error, so the caller (TestMain)
// aborts before any test can read it.
func buildArtifacts(root string, targets ...string) error {
	args := append([]string{"--"}, targets...)
	cmd := exec.Command("make", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("`make %v` failed: %v\n%s", targets, err, out)
	}
	return nil
}

// TestMain is the structural stale-artifact guard. It rebuilds the full
// harness artifact set before any test runs and aborts the suite if the build
// fails, so no test in this package can ever read a stale build/*.bin.
func TestMain(m *testing.M) {
	if os.Getenv("HARNESS_SKIP_BUILD") == "1" {
		fmt.Fprintln(os.Stderr, "WARNING: HARNESS_SKIP_BUILD=1 — skipping the pre-test artifact rebuild.")
		fmt.Fprintln(os.Stderr, "         Tests may read STALE build/*.bin; results are NOT trustworthy.")
		os.Exit(m.Run())
	}

	root, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build guard: %v\n", err)
		os.Exit(1)
	}

	if err := buildArtifacts(root, harnessArtifactTargets...); err != nil {
		fmt.Fprintf(os.Stderr, "\n=== ABORTING harness test suite: artifact build failed ===\n")
		fmt.Fprintf(os.Stderr, "Refusing to run tests against a possibly-stale build/ directory.\n")
		fmt.Fprintf(os.Stderr, "%v\n", err)
		fmt.Fprintf(os.Stderr, "Fix the build (or set HARNESS_SKIP_BUILD=1 to bypass at your own risk).\n")
		os.Exit(1)
	}

	os.Exit(m.Run())
}
