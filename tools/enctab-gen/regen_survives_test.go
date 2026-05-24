package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRegenProducesCanonicalDataGo asserts that running
// `enctab-gen -mra reference/arm-mra -gopkg <tmp>` against the
// committed MRA snapshot produces a file byte-identical to the
// checked-in tools/aarch64enc/data.go.  This is the guarantee that
// makes `make enctab-regen-source` safe to run: regenerating data.go
// never introduces a phantom diff because data.go is purely the MRA
// projection.  All hand-curated forms live in manual_forms.go, which
// this tool never touches.
//
// The test invokes the locally built `enctab-gen` binary instead of
// calling `main()` directly so that the same code path the Makefile
// exercises is exercised here.
func TestRegenProducesCanonicalDataGo(t *testing.T) {
	repoRoot := findRepoRoot(t)
	bin := filepath.Join(repoRoot, "build", "enctab-gen")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("enctab-gen binary not found at %s; run `make enctab-gen` first", bin)
	}

	tmp := t.TempDir()
	outGo := filepath.Join(tmp, "data.go")
	outEnc := filepath.Join(tmp, "enctab.enc")

	cmd := exec.Command(bin,
		"-mra", filepath.Join(repoRoot, "reference", "arm-mra"),
		"-gopkg", outGo,
		"-out", outEnc,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("enctab-gen failed: %v\nstderr:\n%s", err, stderr.String())
	}

	got, err := os.ReadFile(outGo)
	if err != nil {
		t.Fatalf("read regen output: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(repoRoot, "tools", "aarch64enc", "data.go"))
	if err != nil {
		t.Fatalf("read canonical data.go: %v", err)
	}
	if !bytes.Equal(got, want) {
		// Print only the first difference for brevity.
		for i := 0; i < len(got) && i < len(want); i++ {
			if got[i] != want[i] {
				start := i - 40
				if start < 0 {
					start = 0
				}
				endGot := i + 80
				if endGot > len(got) {
					endGot = len(got)
				}
				endWant := i + 80
				if endWant > len(want) {
					endWant = len(want)
				}
				t.Fatalf("regenerated data.go differs from canonical at byte %d.\nwant: %q\ngot:  %q\nHint: `make enctab-regen-source` should produce a byte-identical file; if you intentionally bumped the MRA snapshot, regenerate and commit the new data.go.",
					i, want[start:endWant], got[start:endGot])
			}
		}
		t.Fatalf("regenerated data.go differs in length: got %d bytes, want %d bytes", len(got), len(want))
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	// We're in tools/enctab-gen; walk up until we see a Makefile.
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
