// release_paged_test.go — drives the full ~88 KB spectrum4 release .tbn (a
// 6-page paged-IN load, physical pages 7..12) through the prod assembler in
// the harness.  This is the regression guard for the "Go-harness paged-path
// trap" root-caused in https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-29-go-harness-paged-trap-rootcause.md.
//
// Two assertions:
//
//   - WITH build/sysreg_data.bin served as "sd13" on page 13, the harness runs
//     the full 6-page IN load to completion (OK banner) and the OUT bytes
//     byte-match the vendored tests/release/release.img (21752 B).  This
//     proves the multi-page paged-IN HLOAD emulation is faithful — there is no
//     6-page paging fidelity bug.
//
//   - WITHOUT sd13, the run traps at &0038 (the page-13 sysreg matcher is
//     empty), and the harness's trap diagnostic now names "sd13" as an
//     unserved HGTHD file — so the cryptic trap is self-explaining.
//
// Requires (all produced by `make assembler-prod enctab sam-aarch64 sysreg-data`):
//
//	build/assembler-prod.bin
//	build/enctab.enc
//	build/sysreg_data.bin
//	build/sam-aarch64
//	tests/release/release.s   (vendored; always present)
//	tests/release/release.img (vendored oracle; always present)
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
	d15Path := filepath.Join(root, "build", "disasm.bin")
	samPath := filepath.Join(root, "build", "sam-aarch64")
	releaseSrc := filepath.Join(root, "tests", "release", "release.s")
	releaseImg := filepath.Join(root, "tests", "release", "release.img")

	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, samPath, releaseSrc, releaseImg} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("prerequisite missing: %s\n  run `make assembler-prod enctab sam-aarch64 sysreg-data disasm-payload`", p)
		}
	}

	// Build the flattened, comment-stripped release binary + compact .tbn at
	// the release origin (matches `make release-stripped-tbn`) via
	// sam-aarch64 --emit-tbn.  The SAM assembler consumes the compact v2 .tbn
	// (INSN_RUN decoder).
	tmp := t.TempDir()
	tbnPath := filepath.Join(tmp, "release.compact.tbn")
	goImgPath := filepath.Join(tmp, "release.go.img")
	cmd := exec.Command(samPath,
		"-flatten", "-strip-comments",
		"-origin", "0xfffffff000000000",
		"-o", goImgPath, "-emit-tbn", tbnPath, releaseSrc)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sam-aarch64 failed: %v\n%s", err, out)
	}

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	in, err := os.ReadFile(tbnPath)
	if err != nil {
		t.Fatalf("read release.compact.tbn: %v", err)
	}
	wantImg, _ := os.ReadFile(releaseImg)

	// The compact .tbn collapses literal runs, so the ~88 KB symbolic source
	// shrinks to ~47 KB → 3 IN pages (vs 6 for the symbolic form).  The
	// regression this guards is multi-page paged-IN HLOAD fidelity, so assert
	// >1 page; the byte-match below is the real correctness check.
	wantPages := (len(in) + pageSize - 1) / pageSize
	t.Logf("release.compact.tbn: %d bytes (%d IN pages)", len(in), wantPages)
	if wantPages < 2 {
		t.Fatalf("release.compact.tbn is only %d page; expected >=2 for the paged-IN regression", wantPages)
	}

	// (1) WITH sd13 + d15 → completes + byte-matches the vendored oracle.
	// d15 (the disassembler) is a production feature HLOAD'd at boot by
	// load_page15_payload in BOTH variants, so the prod boot pages it into
	// physical page 15; without it served the boot traps naming d15.
	res := RunWithFiles(asm, enc, in,
		[]NamedFile{
			{Name: "sd13", Content: sd13, TargetPage: 13},
			{Name: "d15", Content: d15, TargetPage: 15},
		},
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

	// (2) WITHOUT sd13 (d15 still served) → the compact `.tbn` still assembles
	// and byte-matches.  The v2 overlay folds every sysreg instruction into its
	// literal 4-byte word at compaction time, so the SAM no longer does the
	// runtime page-13 sysreg-name lookup — sd13 is requested at boot (and shows
	// up unserved) but is not a hard dependency for assembling compact input.
	resNo := RunWithFiles(asm, enc, in,
		[]NamedFile{{Name: "d15", Content: d15, TargetPage: 15}},
		30*time.Second)
	if !resNo.Passed {
		t.Fatalf("compact run FAILED without sd13 (expected pass — sysreg insts are pre-folded): exit=%q printer=%q regs=%s",
			resNo.ExitReason, resNo.PrinterCapture, resNo.FaultRegs)
	}
	if !bytes.Equal(resNo.OutBytes, wantImg) {
		t.Errorf("OUT without sd13 does not byte-match release.img (got %d B, want %d B)",
			len(resNo.OutBytes), len(wantImg))
	}
	found := false
	for _, n := range resNo.UnservedFiles {
		if n == "sd13" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'sd13' reported unserved when -sysreg-data omitted; got %v",
			resNo.UnservedFiles)
	} else {
		t.Logf("compact assembles without sd13; it is reported unserved as expected")
	}
}
