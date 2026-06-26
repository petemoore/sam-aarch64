// compact_tbn_test.go — drives the COMPACT release .tbn (i1: runs of
// fully-literal instructions collapsed to KindLitInsts / 0x07 records,
// produced by `sam-aarch64 --emit-tbn`) through the prod assembler in
// the harness and asserts the OUT bytes still byte-match the vendored
// release.img.  This is the inner-loop proof for the Z80
// REC_KIND_LIT_INSTS decode (i1 PR2): if the SAM assembler memcpys the
// stored literal words correctly and keeps PASS_PC in lockstep, the
// compact source assembles to the identical 21752-byte release binary.
//
// Requires (make assembler-prod enctab sam-aarch64 sysreg-data disasm-payload):
//
//	build/assembler-prod.bin build/enctab.enc build/sysreg_data.bin
//	build/disasm.bin build/sam-aarch64
//	tests/release/release.{s,img}
//
// Skipped automatically if any build artefact is absent.  SimCoupé
// remains the sole CI gate; this is an inner-loop check.
package main

import (
	"bytes"
	"encoding/binary"
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
	releaseSrc := filepath.Join(root, "tests", "release", "release.s")
	releaseImg := filepath.Join(root, "tests", "release", "release.img")

	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, samPath, releaseSrc, releaseImg} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("prerequisite missing: %s\n  run `make assembler-prod enctab sam-aarch64 sysreg-data disasm-payload`", p)
		}
	}

	tmp := t.TempDir()
	compactTbn := filepath.Join(tmp, "release.compact.tbn")
	goImg := filepath.Join(tmp, "release.go.img")

	// release.s → binary + compact .tbn (flatten + thin-comments=20, release
	// origin). thin-comments keeps a bounded subset of release.s's ~335 KB of
	// comments (M8 / i39b-2) so a populated editor region rides the full Z80
	// round-trip while the .tbn stays under the 96 KB IN ceiling — mirrors the
	// release-gate SimCoupé gate. Comments are assembly no-ops, so the OUT must
	// still byte-match release.img.
	cmd := exec.Command(samPath, "-flatten", "-thin-comments=20",
		"-origin", "0xfffffff000000000", "-o", goImg, "-emit-tbn", compactTbn, releaseSrc)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sam-aarch64 failed: %v\n%s", err, out)
	}

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	in, err := os.ReadFile(compactTbn)
	if err != nil {
		t.Fatalf("read compact .tbn: %v", err)
	}
	wantImg, _ := os.ReadFile(releaseImg)

	// The section index (bytes 8..12) is the editor_region_offset: the
	// assembler-facing region the SAM walks. Everything past it (front-coded
	// name table, .global flags, comment sidecar) is the editor region the
	// assembler never reads — the bytes a future build (i40) could reclaim.
	pages := (len(in) + pageSize - 1) / pageSize
	editorOff := int(binary.LittleEndian.Uint32(in[8:12]))
	t.Logf("compact .tbn: %d bytes (%d IN pages); assembler-facing %d B, "+
		"editor region (reclaimable) %d B", len(in), pages, editorOff, len(in)-editorOff)
	// The IN buffer is 6 physical pages (96 KB). A thinned-comment .tbn must
	// still fit; if this trips, lower the m6/harness thin-comments ratio.
	if pages > 6 {
		t.Errorf("compact .tbn is %d IN pages, exceeds the 6-page / 96 KB ceiling", pages)
	}

	// Serve the COMPLETE prod-boot disk (sd13 + zx013 + d15) with
	// StrictFileNotFound (i184): the prod assembler HLOADs all three at boot, so
	// a missing one now fails loudly instead of silently no-opping.
	res := runProdComplete(t, asm, enc, in, 30*time.Second)
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
