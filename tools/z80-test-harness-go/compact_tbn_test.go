// compact_tbn_test.go — drives the COMPACT release .tbn (i1: runs of
// fully-literal instructions collapsed to KindLitInsts / 0x07 records,
// produced by `refenc -emit-compact-tbn`) through the prod assembler in
// the harness and asserts the OUT bytes still byte-match the vendored
// release.img.  This is the inner-loop proof for the Z80
// REC_KIND_LIT_INSTS decode (i1 PR2): if the SAM assembler memcpys the
// stored literal words correctly and keeps PASS_PC in lockstep, the
// compact source assembles to the identical 21752-byte release binary.
//
// Requires (make m3-asm-prod enctab text2bin refenc sysreg-data disasm-payload):
//
//	build/assembler-prod.bin build/enctab.enc build/sysreg_data.bin
//	build/disasm.bin build/text2bin build/refenc
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
	text2binPath := filepath.Join(root, "build", "text2bin")
	refencPath := filepath.Join(root, "build", "refenc")
	releaseSrc := filepath.Join(root, "tests", "m6", "release", "release.s")
	releaseImg := filepath.Join(root, "tests", "m6", "release", "release.img")

	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, text2binPath, refencPath, releaseSrc, releaseImg} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("prerequisite missing: %s\n  run `make m3-asm-prod enctab text2bin refenc sysreg-data disasm-payload`", p)
		}
	}

	tmp := t.TempDir()
	symTbn := filepath.Join(tmp, "release.tbn")
	compactTbn := filepath.Join(tmp, "release.compact.tbn")
	goImg := filepath.Join(tmp, "release.go.img")

	// release.s → symbolic .tbn (flatten + strip-comments, release origin).
	cmd := exec.Command(text2binPath, "-flatten", "-strip-comments",
		"-origin", "0xfffffff000000000", "-o", symTbn, releaseSrc)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("text2bin failed: %v\n%s", err, out)
	}
	// symbolic .tbn → compact .tbn (literal runs collapsed to 0x07).
	cmd = exec.Command(refencPath, "-o", goImg, "-emit-compact-tbn", compactTbn, symTbn)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("refenc failed: %v\n%s", err, out)
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

	symInfo, _ := os.Stat(symTbn)
	t.Logf("compact .tbn: %d bytes (symbolic was %d; %d IN pages)",
		len(in), symInfo.Size(), (len(in)+pageSize-1)/pageSize)

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
