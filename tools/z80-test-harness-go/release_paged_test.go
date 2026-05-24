// release_paged_test.go — drives the full ~88 KB spectrum4 release .tbn (a
// 6-page paged-IN load, physical pages 7..12) through the prod assembler in
// the harness.  This is the regression guard for the "Go-harness paged-path
// trap" root-caused in docs/notes/2026-05-29-go-harness-paged-trap-rootcause.md.
//
// Two assertions:
//
//   - WITH build/sysreg_data.bin served as "sd13" on page 13, the harness runs
//     the full 6-page IN load to completion (OK banner) and the OUT bytes
//     byte-match the vendored tests/m6/release/release.img (21752 B).  This
//     proves the multi-page paged-IN HLOAD emulation is faithful — there is no
//     6-page paging fidelity bug.
//
//   - WITHOUT sd13, the run traps at &0038 (the page-13 sysreg matcher is
//     empty), and the harness's trap diagnostic now names "sd13" as an
//     unserved HGTHD file — so the cryptic trap is self-explaining.
//
// Requires (all produced by `make m3-asm-prod enctab text2bin sysreg-data`):
//
//	build/assembler-prod.bin
//	build/enctab.enc
//	build/sysreg_data.bin
//	build/text2bin
//	tests/m6/release/release.s   (vendored; always present)
//	tests/m6/release/release.img (vendored oracle; always present)
//
// Skipped automatically if any build artefact is absent.  SimCoupé remains the
// sole CI gate; this is an inner-loop check.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReleasePagedInLoad(t *testing.T) {
	root := repoRoot(t)

	asmPath := filepath.Join(root, "build", "assembler-prod.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	sd13Path := filepath.Join(root, "build", "sysreg_data.bin")
	text2binPath := filepath.Join(root, "build", "text2bin")
	releaseSrc := filepath.Join(root, "tests", "m6", "release", "release.s")
	releaseImg := filepath.Join(root, "tests", "m6", "release", "release.img")

	for _, p := range []string{asmPath, encPath, sd13Path, text2binPath, releaseSrc, releaseImg} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("prerequisite missing: %s\n  run `make m3-asm-prod enctab text2bin sysreg-data`", p)
		}
	}

	// Build the flattened, comment-stripped release .tbn at the release origin
	// (matches `make release-stripped-tbn`).  ~88 KB → 6 IN pages (7..12).
	tbnPath := filepath.Join(t.TempDir(), "release.tbn")
	cmd := exec.Command(text2binPath,
		"-flatten", "-strip-comments",
		"-origin", "0xfffffff000000000",
		"-o", tbnPath, releaseSrc)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("text2bin failed: %v\n%s", err, out)
	}

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	in, err := os.ReadFile(tbnPath)
	if err != nil {
		t.Fatalf("read release.tbn: %v", err)
	}
	wantImg, _ := os.ReadFile(releaseImg)

	wantPages := (len(in) + pageSize - 1) / pageSize
	t.Logf("release.tbn: %d bytes (%d IN pages)", len(in), wantPages)
	if wantPages < 6 {
		t.Fatalf("release.tbn is only %d pages; expected >=6 for the paged-IN regression", wantPages)
	}

	// (1) WITH sd13 → completes + byte-matches the vendored oracle.
	res := RunWithFiles(asm, enc, in,
		[]NamedFile{{Name: "sd13", Content: sd13, TargetPage: 13}},
		30*time.Second)
	if !res.Passed {
		t.Fatalf("release paged-IN run FAILED with sd13 supplied: exit=%q printer=%q regs=%s",
			res.ExitReason, res.PrinterCapture, res.FaultRegs)
	}
	if len(res.UnservedFiles) != 0 {
		t.Errorf("unexpected unserved files with sd13 supplied: %v", res.UnservedFiles)
	}
	if !bytes.Equal(res.OutBytes, wantImg) {
		t.Errorf("OUT bytes do not byte-match release.img (got %d B, want %d B)",
			len(res.OutBytes), len(wantImg))
	} else {
		t.Logf("OUT byte-matches release.img (%d B)", len(wantImg))
	}

	// (2) WITHOUT sd13 → traps, and the diagnostic names "sd13" as unserved.
	resNo := RunWithFiles(asm, enc, in, nil, 30*time.Second)
	if resNo.Passed {
		t.Fatalf("expected a trap without sd13, but the run PASSED")
	}
	found := false
	for _, n := range resNo.UnservedFiles {
		if n == "sd13" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'sd13' in UnservedFiles when -sysreg-data omitted; got %v (exit=%q)",
			resNo.UnservedFiles, resNo.ExitReason)
	} else {
		t.Logf("trap diagnostic correctly names sd13: %q", resNo.ExitReason)
	}
}
