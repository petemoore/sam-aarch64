// test_variant_test.go — drives the BUILD_TESTS variant assembler.bin
// through the harness, exercising the boot-time self-test suites (including
// run_reader_paged_self_tests).  This is the harness-side cousin of the
// SimCoupé ci-m3 test-variant job.  SimCoupé remains the sole CI gate; this
// test is a fast inner-loop check that the test variant boots clean.
//
// Requires the BUILD_TESTS artefacts:
//
//	build/assembler.bin                  (make m3-asm)
//	build/test_mem.bin                   (make test-mem-offaxis)
//	build/paged_call_test_payload.bin    (make paged-call-payload)
//	build/enctab.enc                     (make enctab)
//	build/text2bin                       (make text2bin)
//
// Skipped automatically if build/assembler.bin is absent.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestVariantBootSelfTests(t *testing.T) {
	root := repoRoot(t)

	asmPath := filepath.Join(root, "build", "assembler.bin")
	if _, err := os.Stat(asmPath); err != nil {
		t.Skip("build/assembler.bin absent — run `make m3-asm test-mem-offaxis paged-call-payload enctab text2bin`")
	}
	tmPath := filepath.Join(root, "build", "test_mem.bin")
	p14Path := filepath.Join(root, "build", "paged_call_test_payload.bin")
	sd13Path := filepath.Join(root, "build", "sysreg_data.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	text2binPath := filepath.Join(root, "build", "text2bin")
	fixturePath := filepath.Join(root, "tests", "m3", "sources", "inst_nop_ret.s")
	for _, p := range []string{tmPath, p14Path, sd13Path, encPath, text2binPath, fixturePath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("prerequisite missing: %s", p)
		}
	}

	tbnPath := filepath.Join(t.TempDir(), "inst_nop_ret.tbn")
	cmd := exec.Command(text2binPath, "-o", tbnPath, fixturePath)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("text2bin failed: %v\n%s", err, out)
	}

	asm, _ := os.ReadFile(asmPath)
	tm, _ := os.ReadFile(tmPath)
	p14, _ := os.ReadFile(p14Path)
	sd13, _ := os.ReadFile(sd13Path)
	enc, _ := os.ReadFile(encPath)
	in, _ := os.ReadFile(tbnPath)

	// Use RunConfig to also exercise the windowed-trace and trigger-PC
	// diagnostics added for the PR-6 reader-self-test investigation.  We
	// trace the reader self-test address window and assert SP never
	// descends into that code region (the original PR #42 failure mode was
	// the stack at &C100 overwriting the test function's own opcodes when
	// it spilled above &C000 — which must not recur).
	res, trace, _ := RunConfig(Config{
		AssemblerBin: asm, EnctabData: enc, InData: in,
		Files: []NamedFile{
			// sd13 listed before test_mem so test_mem wins the initial
			// page-13 pre-deposit (run_mem_self_tests runs first); the boot
			// later HGTHD+HLOADs "sd13" over page 13 via load_page13_payload
			// (PR-2) before run_sysreg_paged_self_tests reads the tables.
			{Name: "sd13", Content: sd13, TargetPage: 13},
			{Name: "test_mem", Content: tm, TargetPage: 13},
			{Name: "p14", Content: p14, TargetPage: 14},
		},
		Timeout: 10 * time.Second,
		TraceLo: 0xBE00, TraceHi: 0xC000,
	})

	t.Logf("Exit: %s", res.ExitReason)
	t.Logf("Printer: %q", res.PrinterCapture)
	t.Logf("FaultRegs: %s", res.FaultRegs)
	t.Logf("reader-window trace entries: %d", len(trace))

	if !res.Passed {
		t.Fatalf("test variant boot self-tests did not pass: printer=%q exit=%q regs=%s",
			res.PrinterCapture, res.ExitReason, res.FaultRegs)
	}
	// The regression guard below is only meaningful if execution actually
	// entered the reader-test window; an empty trace would let it pass
	// vacuously (e.g. if a future layout moved the routine out of the
	// window).  Fail loudly rather than rot into a no-op.
	if len(trace) == 0 {
		t.Fatal("reader-test window [BE00,C000) never entered — regression guard is not exercising the target routine")
	}
	// Regression guard for the PR #42 failure mode: the boot stack at
	// &C100 must never collide with code executing below &C000.
	for _, s := range trace {
		if s.PC >= 0xBE00 && s.PC < 0xC000 && s.SP < 0xC080 {
			t.Errorf("SP descended dangerously close to reader-test code: PC=%04X SP=%04X", s.PC, s.SP)
			break
		}
	}
	// nop; ret → 1F 20 03 D5  C0 03 5F D6
	wantOut := []byte{0x1F, 0x20, 0x03, 0xD5, 0xC0, 0x03, 0x5F, 0xD6}
	if !bytes.Equal(res.OutBytes, wantOut) {
		t.Errorf("OUT mismatch: got %X want %X", res.OutBytes, wantOut)
	}
}
