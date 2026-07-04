// release_demo_assemble_save_test.go — the i365b emulation gate.
//
// Sibling of release_full_comments_test.go (the prod-assembler gate): it drives
// the SAME full, unstripped release .tbn through the DEMO_ASM assembler variant
// (build/assembler-demo.bin) and proves the callable-exit demo path the i365
// demo driver will `call`:
//
//	(a) it assembles + saves correctly — res.OutBytes byte-match release.img,
//	    exactly as the prod path does (the DEMO_ASM changes are confined to the
//	    exit mechanism and the output filename); and
//	(b) control RETURNED rather than halting — the demo restores the caller's SP
//	    and `ret`s into the harness-planted StopPC trap (res.ReachedStop), the
//	    behaviour that distinguishes it from prod's terminal di/halt; and
//	(c) the image was saved under the demo name "RELEASEIMG" (res.SavedName,
//	    read from UIFA[1..10] on the HSAVE hook), not prod's "OUT".
//
// Requires build/assembler-demo.bin (`make assembler-demo`) plus the same
// payloads the prod gate needs.  Any absent prerequisite is a hard failure
// (t.Fatal), never a skip (CLAUDE.md testing policy).
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// demoRetTrap is the sentinel return address the harness plants at the boot SP
// (&FFFE) for this run.  The DEMO_ASM assembler saves that SP at start:,
// restores it at its clean exit, and `ret`s — landing PC here.  It is a high
// section-D address the assembler never executes as code, so PC==demoRetTrap is
// unambiguously "the demo returned".
const demoRetTrap = 0xFFDD

func TestReleaseDemoAssembleSave(t *testing.T) {
	root := repoRoot(t)

	demoPath := filepath.Join(root, "build", "assembler-demo.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	sd13Path := filepath.Join(root, "build", "sysreg_data.bin")
	d15Path := filepath.Join(root, "build", "disasm.bin")
	zx0Path := filepath.Join(root, "build", "zx0.bin")
	samPath := filepath.Join(root, "build", "sam-aarch64")
	releaseSrc := filepath.Join(root, "tests", "release", "release.s")
	releaseImg := filepath.Join(root, "tests", "release", "release.img")

	for _, p := range []string{demoPath, encPath, sd13Path, d15Path, zx0Path, samPath, releaseSrc, releaseImg} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("prerequisite missing: %s\n  run `make assembler-demo enctab sam-aarch64 sysreg-data disasm-payload zx0-payload`", p)
		}
	}

	tmp := t.TempDir()
	tbnPath := filepath.Join(tmp, "release.full-comments.tbn")
	goImgPath := filepath.Join(tmp, "release.go.img")

	// Build the compact .tbn with ALL comments retained (mirrors the prod gate
	// and the `release-unstripped-tbn` Makefile target).  The assembler loads
	// only the .tbn prefix, so the full-comment form assembles identically.
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

	demo, _ := os.ReadFile(demoPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	zx0, _ := os.ReadFile(zx0Path)
	wantImg, _ := os.ReadFile(releaseImg)

	res, _, _ := RunConfig(Config{
		AssemblerBin: demo,
		EnctabData:   enc,
		InData:       in,
		Files: []NamedFile{
			{Name: "sd13", Content: sd13, TargetPage: 13},
			{Name: "zx013", Content: zx0, TargetPage: 13, LoadOffset: 0x0400},
			{Name: "d15", Content: d15, TargetPage: 15},
		},
		Timeout: 30 * time.Second,
		StopPC:  demoRetTrap,
	})

	if !res.Passed {
		t.Fatalf("demo assemble+save FAILED: exit=%q printer=%q regs=%s",
			res.ExitReason, res.PrinterCapture, res.FaultRegs)
	}
	if len(res.UnservedFiles) != 0 {
		t.Errorf("unexpected unserved files: %v", res.UnservedFiles)
	}

	// (a) assemble + save is byte-correct under the demo path.
	if !bytes.Equal(res.OutBytes, wantImg) {
		t.Errorf("OUT bytes do not byte-match release.img (got %d B, want %d B)",
			len(res.OutBytes), len(wantImg))
	} else {
		t.Logf("OUT byte-matches release.img (%d B) via the demo assembler", len(wantImg))
	}

	// (b) control RETURNED — the demo's ld sp,(saved_caller_sp)/ret landed on
	// the planted StopPC, proving a callable exit rather than prod's di/halt.
	if !res.ReachedStop {
		t.Errorf("demo did not RET to the planted trap &%04X (exit=%q PC=&%04X) — "+
			"the callable exit did not fire", demoRetTrap, res.ExitReason, res.FaultRegs.PC)
	} else {
		t.Logf("demo returned cleanly to planted trap &%04X (ret-exit confirmed)", demoRetTrap)
	}

	// (c) saved under the demo name, not prod's "OUT".
	if res.SavedName != "RELEASEIMG" {
		t.Errorf("HSAVE name = %q, want %q", res.SavedName, "RELEASEIMG")
	} else {
		t.Logf("HSAVE name = %q", res.SavedName)
	}
}
