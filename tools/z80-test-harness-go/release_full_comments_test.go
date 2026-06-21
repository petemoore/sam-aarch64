// release_full_comments_test.go — at-scale proof for i40 load-path + i51.
//
// Drives the full, unstripped release .tbn (ALL 7,502 comment lines retained,
// no -strip-comments / -thin-comments) through the prod assembler in the
// harness and asserts the OUT bytes byte-match the vendored release.img.
//
// BEFORE the two-phase prefix-only load (i40 load-path), this .tbn exceeds
// the 6-page / 96 KB IN ceiling, so the assembler would fail with FAIL03
// (in_file_pages > 6).  AFTER the fix, only the assembler-facing prefix
// (~38,584 B / 3 pages) enters the IN buffer; the editor region never loads.
//
// The assertion that the .tbn is > 98304 bytes (6 × 16384) confirms this test
// exercises the i40 mechanism rather than inadvertently passing because the
// file happens to fit before the fix.
//
// Requires (all produced by `make assembler-prod enctab sam-aarch64
// sysreg-data disasm-payload zx0-payload`):
//
//	build/assembler-prod.bin
//	build/enctab.enc
//	build/sysreg_data.bin
//	build/disasm.bin
//	build/zx0.bin
//	build/sam-aarch64
//	tests/release/release.s   (vendored; always present)
//	tests/release/release.img (vendored oracle; always present)
//
// Skipped automatically if any build artefact is absent.  SimCoupé remains
// the sole CI gate; this is an inner-loop check.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReleaseFullCommentsTbn(t *testing.T) {
	root := repoRoot(t)

	asmPath := filepath.Join(root, "build", "assembler-prod.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	sd13Path := filepath.Join(root, "build", "sysreg_data.bin")
	d15Path := filepath.Join(root, "build", "disasm.bin")
	zx0Path := filepath.Join(root, "build", "zx0.bin")
	samPath := filepath.Join(root, "build", "sam-aarch64")
	releaseSrc := filepath.Join(root, "tests", "release", "release.s")
	releaseImg := filepath.Join(root, "tests", "release", "release.img")

	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, zx0Path, samPath, releaseSrc, releaseImg} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("prerequisite missing: %s\n  run `make assembler-prod enctab sam-aarch64 sysreg-data disasm-payload zx0-payload`", p)
		}
	}

	tmp := t.TempDir()
	tbnPath := filepath.Join(tmp, "release.full-comments.tbn")
	goImgPath := filepath.Join(tmp, "release.go.img")

	// Build the compact .tbn with ALL comments retained (no -strip-comments /
	// -thin-comments).  This mirrors the `release-unstripped-tbn` Makefile
	// target (Makefile:455-461).  The resulting file includes the full
	// comment sidecar in the editor region, making it substantially larger
	// than the 6-page / 96 KB IN ceiling.
	cmd := exec.Command(samPath,
		"-flatten",
		"-origin", "0xfffffff000000000",
		"-o", goImgPath, "-emit-tbn", tbnPath, releaseSrc)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sam-aarch64 failed: %v\n%s", err, out)
	}

	in, err := os.ReadFile(tbnPath)
	if err != nil {
		t.Fatalf("read release.full-comments.tbn: %v", err)
	}

	// Assert the .tbn is larger than the 6-page / 96 KB IN ceiling — this
	// proves the test exercises the i40 prefix-only load mechanism rather
	// than passing trivially because the file already fits.
	const inCeiling = 6 * pageSize // 98304 bytes
	t.Logf("release.full-comments.tbn: %d bytes (IN ceiling: %d bytes)", len(in), inCeiling)
	if len(in) <= inCeiling {
		t.Fatalf("full-comments .tbn is %d bytes — not larger than the %d-byte IN ceiling; "+
			"test does not exercise the i40 prefix-only load (the file fits without it)",
			len(in), inCeiling)
	}

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	zx0, _ := os.ReadFile(zx0Path)
	wantImg, _ := os.ReadFile(releaseImg)

	res := RunWithFiles(asm, enc, in,
		[]NamedFile{
			{Name: "sd13", Content: sd13, TargetPage: 13},
			{Name: "zx013", Content: zx0, TargetPage: 13, LoadOffset: 0x0400},
			{Name: "d15", Content: d15, TargetPage: 15},
		},
		30*time.Second)
	if !res.Passed {
		t.Fatalf("full-comments .tbn run FAILED: exit=%q printer=%q regs=%s",
			res.ExitReason, res.PrinterCapture, res.FaultRegs)
	}
	if len(res.UnservedFiles) != 0 {
		t.Errorf("unexpected unserved files: %v", res.UnservedFiles)
	}
	if !bytes.Equal(res.OutBytes, wantImg) {
		t.Errorf("OUT bytes do not byte-match release.img (got %d B, want %d B)",
			len(res.OutBytes), len(wantImg))
	} else {
		t.Logf("OUT byte-matches release.img (%d B) from full-comments .tbn (%d B)",
			len(wantImg), len(in))
	}
}
