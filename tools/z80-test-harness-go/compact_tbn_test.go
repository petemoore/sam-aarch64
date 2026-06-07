// compact_tbn_test.go — drives the COMPACT release .tbn (i1: runs of
// fully-literal instructions collapsed to KindLitInsts / 0x07 records,
// produced by `sam-aarch64 --emit-tbn`) through the prod assembler in
// the harness and asserts the OUT bytes still byte-match the vendored
// release.img.  This is the inner-loop proof for the Z80
// REC_KIND_LIT_INSTS decode (i1 PR2): if the SAM assembler memcpys the
// stored literal words correctly and keeps PASS_PC in lockstep, the
// compact source assembles to the identical 21752-byte release binary.
//
// Requires (make m3-asm-prod enctab sam-aarch64 sysreg-data disasm-payload):
//
//	build/assembler-prod.bin build/enctab.enc build/sysreg_data.bin
//	build/disasm.bin build/sam-aarch64
//	tests/m6/release/release.{s,img}
//
// Skipped automatically if any build artefact is absent.  SimCoupé
// remains the sole CI gate; this is an inner-loop check.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCompactTbnAssembly(t *testing.T) {
	root := repoRoot(t)

	asmPath := filepath.Join(root, "build", "assembler-prod.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	sd13Path := filepath.Join(root, "build", "sysreg_data.bin")
	d15Path := filepath.Join(root, "build", "disasm.bin")
	samPath := filepath.Join(root, "build", "sam-aarch64")
	releaseSrc := filepath.Join(root, "tests", "m6", "release", "release.s")
	releaseImg := filepath.Join(root, "tests", "m6", "release", "release.img")

	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, samPath, releaseSrc, releaseImg} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("prerequisite missing: %s\n  run `make m3-asm-prod enctab sam-aarch64 sysreg-data disasm-payload`", p)
		}
	}

	tmp := t.TempDir()
	compactTbn := filepath.Join(tmp, "release.compact.tbn")
	goImg := filepath.Join(tmp, "release.go.img")

	// release.s → binary + compact .tbn (flatten + strip-comments, release
	// origin; literal runs collapsed to 0x07 in the compact .tbn).
	cmd := exec.Command(samPath, "-flatten", "-strip-comments",
		"-origin", "0xfffffff000000000", "-o", goImg, "-emit-tbn", compactTbn, releaseSrc)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sam-aarch64 failed: %v\n%s", err, out)
	}

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	in, err := os.ReadFile(compactTbn)
	if err != nil {
		t.Fatalf("read compact .tbn: %v", err)
	}
	wantImg, _ := os.ReadFile(releaseImg)

	t.Logf("compact .tbn: %d bytes (%d IN pages)",
		len(in), (len(in)+pageSize-1)/pageSize)

	res := RunWithFiles(asm, enc, in,
		[]NamedFile{
			{Name: "sd13", Content: sd13, TargetPage: 13},
			{Name: "d15", Content: d15, TargetPage: 15},
		},
		30*time.Second)
	if !res.Passed {
		t.Fatalf("compact .tbn run FAILED: exit=%q printer=%q regs=%s",
			res.ExitReason, res.PrinterCapture, res.FaultRegs)
	}
	if !bytes.Equal(res.OutBytes, wantImg) {
		t.Errorf("OUT bytes do not byte-match release.img (got %d B, want %d B)",
			len(res.OutBytes), len(wantImg))
	} else {
		t.Logf("compact .tbn OUT byte-matches release.img (%d B)", len(wantImg))
	}
}
