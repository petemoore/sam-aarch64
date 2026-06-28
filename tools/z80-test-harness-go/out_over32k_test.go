// out_over32k_test.go — i24: the OUT-ceiling-lift end-to-end vehicle.
//
// Drives tests/paged/sources/inst_out_over32k.s (output 40,932 B — two run
// page boundaries, past the old two-page / 32 KB OUT ceiling) through the
// prod assembler in the harness and byte-compares the HSAVE'd OUT against
// the Go assembler (tools/sam-aarch64) for the same source.
//
// End-to-end this exercises the whole pool-run OUT path: reset_out_buffer
// sizing the run from the pass-1 total, pp_alloc_run(PP_OUT), the uniform
// LMPR-bracketed emit_byte with two out_advance_page crossings, the
// >32 KB 24-bit OUT_LEN, save_out_file's dynamic UIFA fill
// (start page = OUT_RUN_BASE, pages = 2, remainder = 8164), and the
// harness's multi-page UIFA reconstruction (harness.go HSAVE capture).
//
// The same committed fixture rides the SimCoupé ci-paged / ci-paged-prod
// matrix against GNU as — SimCoupé remains the sole CI gate; this is the
// fast inner-loop proof.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestOutOver32K(t *testing.T) {
	root := repoRoot(t)

	asmPath := filepath.Join(root, "build", "assembler-prod.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	sd13Path := filepath.Join(root, "build", "sysreg_data.bin")
	d15Path := filepath.Join(root, "build", "disasm.bin")
	zx0Path := filepath.Join(root, "build", "zx0.bin")
	samPath := filepath.Join(root, "build", "sam-aarch64")
	srcPath := filepath.Join(root, "tests", "paged", "sources", "inst_out_over32k.s")

	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, zx0Path, samPath, srcPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("prerequisite missing: %s\n  run `make assembler-prod enctab sam-aarch64 sysreg-data disasm-payload zx0-payload`", p)
		}
	}

	// Go-oracle image + the compact .tbn the SAM assembler consumes, from
	// the SAME source and flags (default origin, matching the fixture
	// sweeps' -Ttext=0 convention).
	tmp := t.TempDir()
	tbnPath := filepath.Join(tmp, "inst_out_over32k.compact.tbn")
	imgPath := filepath.Join(tmp, "inst_out_over32k.go.img")
	cmd := exec.Command(samPath, "-o", imgPath, "-emit-tbn", tbnPath, srcPath)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sam-aarch64 failed: %v\n%s", err, out)
	}

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	zx0, _ := os.ReadFile(zx0Path)
	in, err := os.ReadFile(tbnPath)
	if err != nil {
		t.Fatalf("read %s: %v", tbnPath, err)
	}
	wantImg, err := os.ReadFile(imgPath)
	if err != nil {
		t.Fatalf("read %s: %v", imgPath, err)
	}

	// The whole point: the oracle output itself must exceed the old 32 KB
	// ceiling, or this test proves nothing.
	if len(wantImg) <= 32*1024 {
		t.Fatalf("fixture emits only %d B — must exceed 32768 to exercise the ceiling lift", len(wantImg))
	}

	res := RunWithFiles(asm, enc, in,
		[]NamedFile{
			{Name: "sd13", Content: sd13, TargetPage: 13},
			{Name: "zx013", Content: zx0, TargetPage: 13, LoadOffset: 0x0400},
			{Name: "d15", Content: d15, TargetPage: 15},
		},
		60*time.Second)
	if !res.Passed {
		t.Fatalf(">32K OUT run FAILED: exit=%q printer=%q regs=%s",
			res.ExitReason, res.PrinterCapture, res.FaultRegs)
	}
	if len(res.UnservedFiles) != 0 {
		t.Errorf("unexpected unserved files: %v", res.UnservedFiles)
	}
	if len(res.OutBytes) != len(wantImg) {
		t.Fatalf("OUT length %d B != Go oracle %d B", len(res.OutBytes), len(wantImg))
	}
	if !bytes.Equal(res.OutBytes, wantImg) {
		for i := range wantImg {
			if res.OutBytes[i] != wantImg[i] {
				t.Fatalf("OUT diverges from the Go oracle at byte %d: got %02x want %02x",
					i, res.OutBytes[i], wantImg[i])
			}
		}
	}
	t.Logf(">32K OUT byte-match: %d B (%d full pages + %d remainder)",
		len(res.OutBytes), len(wantImg)/16384, len(wantImg)%16384)
}
