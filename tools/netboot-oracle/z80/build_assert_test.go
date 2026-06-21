// build_assert_test.go — structural guard against stale-artifact false passes.
//
// The oracle / boot tests in this package load pre-built SAM-side artifacts
// from the repo-root build/ dir (netboot_*.bin/.map, asmlex.bin, editmodel.bin,
// …) via ../../../build/ paths. Each test only checks that the file exists
// before loading it — never that it is *fresh*. A failed `pyz80`/`make` build
// leaves the previous .bin in place, so an oracle comparison reads the STALE
// artifact and reports a false PASS. See registry item i116.
//
// The fix is structural: TestMain rebuilds the full artifact set through `make`
// BEFORE any test runs, and ASSERTS the build exited 0. The Makefile's
// prerequisite graph (build/*.bin : src/*.asm) rebuilds anything stale and
// no-ops when fresh (~0.06s warm), so every subsequent Load() sees an artifact
// current with its sources. A build failure aborts the whole suite with a
// non-zero exit — tests never run against a stale .bin.
//
// Override: HARNESS_SKIP_BUILD=1 bypasses the pre-build (no make/pyz80 on PATH,
// or to load a hand-placed artifact). It is an explicit, visible opt-out that
// prints a warning — not a silent default.
package z80_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// netbootArtifactTargets is the set of `make` targets that build every
// SAM-side artifact this package's tests load from build/. It mirrors the
// prerequisites of the `ci-netboot-z80` target in the Makefile. Keep the two
// in sync: a new build/*.bin loaded by a test must be added here AND to
// ci-netboot-z80.
var netbootArtifactTargets = []string{
	"netboot-z80-routines",
	"editmodel-z80",
	"editmodel-paged-z80",
	"pagepool-z80",
	"viewport-z80",
	"asmlex-z80",
	"asmparse-z80",
	"pass1-ir-z80",
	"compact-ir-z80",
}

// findRepoRoot walks up from the current working directory to the repo root,
// identified by the presence of a Makefile.
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
// no-ops when everything is fresh. On a build failure the stale .bin is left on
// disk — but this returns the error, so TestMain aborts before any test loads
// it.
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

// TestMain is the structural stale-artifact guard: it rebuilds the full
// netboot artifact set before any test runs and aborts the suite if the build
// fails, so no test can ever load a stale build/*.bin.
func TestMain(m *testing.M) {
	if os.Getenv("HARNESS_SKIP_BUILD") == "1" {
		fmt.Fprintln(os.Stderr, "WARNING: HARNESS_SKIP_BUILD=1 — skipping the pre-test artifact rebuild.")
		fmt.Fprintln(os.Stderr, "         Tests may load STALE build/*.bin; results are NOT trustworthy.")
		os.Exit(m.Run())
	}

	root, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build guard: %v\n", err)
		os.Exit(1)
	}

	if err := buildArtifacts(root, netbootArtifactTargets...); err != nil {
		fmt.Fprintf(os.Stderr, "\n=== ABORTING netboot-oracle z80 test suite: artifact build failed ===\n")
		fmt.Fprintf(os.Stderr, "Refusing to run tests against a possibly-stale build/ directory.\n")
		fmt.Fprintf(os.Stderr, "%v\n", err)
		fmt.Fprintf(os.Stderr, "Fix the build (or set HARNESS_SKIP_BUILD=1 to bypass at your own risk).\n")
		os.Exit(1)
	}

	os.Exit(m.Run())
}
