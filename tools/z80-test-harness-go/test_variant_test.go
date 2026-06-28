// test_variant_test.go — drives the BUILD_TESTS variant assembler.bin
// through the harness, exercising the boot-time self-test suites (including
// run_reader_paged_self_tests).  This is the harness-side cousin of the
// SimCoupé ci-core test-variant job.  SimCoupé remains the sole CI gate; this
// test is a fast inner-loop check that the test variant boots clean.
//
// Requires the BUILD_TESTS artefacts:
//
//	build/assembler.bin                  (make assembler)
//	build/test_mem.bin                   (make test-mem-offaxis)
//	build/paged_call_test_payload.bin    (make paged-call-payload)
//	build/enctab.enc                     (make enctab)
//	build/sam-aarch64                     (make sam-aarch64)
//
// Skipped automatically if build/assembler.bin is absent.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVariantBootSelfTests(t *testing.T) {
	root := repoRoot(t)

	asmPath := filepath.Join(root, "build", "assembler.bin")
	if _, err := os.Stat(asmPath); err != nil {
		t.Fatal("build/assembler.bin absent — run `make assembler test-mem-offaxis paged-call-payload enctab sam-aarch64`")
	}
	tmPath := filepath.Join(root, "build", "test_mem.bin")
	clusterPath := filepath.Join(root, "build", "test_cluster.bin")
	p14Path := filepath.Join(root, "build", "paged_call_test_payload.bin")
	sd13Path := filepath.Join(root, "build", "sysreg_data.bin")
	// BUILD_TESTS assembler boot runs the disasm self-test → TEST disasm.
	d15Path := filepath.Join(root, "build", "disasm-test.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	samPath := filepath.Join(root, "build", "sam-aarch64")
	fixturePath := filepath.Join(root, "tests", "format", "sources", "inst_nop_ret.s")
	for _, p := range []string{tmPath, clusterPath, p14Path, sd13Path, d15Path, encPath, samPath, fixturePath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("prerequisite missing: %s", p)
		}
	}

	// Assemble the fixture to a binary + compact .tbn (sam-aarch64 --emit-tbn):
	// the SAM assembler consumes the compact v2 .tbn (INSN_RUN decoder).
	tmp := t.TempDir()
	tbnPath := filepath.Join(tmp, "inst_nop_ret.compact.tbn")
	goImgPath := filepath.Join(tmp, "inst_nop_ret.go.img")
	cmd := exec.Command(samPath, "-o", goImgPath, "-emit-tbn", tbnPath, fixturePath)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sam-aarch64 failed: %v\n%s", err, out)
	}

	asm, _ := os.ReadFile(asmPath)
	tm, _ := os.ReadFile(tmPath)
	cluster, _ := os.ReadFile(clusterPath)
	p14, _ := os.ReadFile(p14Path)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	enc, _ := os.ReadFile(encPath)
	in, _ := os.ReadFile(tbnPath)

	// Use RunConfig to also exercise the windowed-trace and trigger-PC
	// diagnostics added for the PR-6 reader-self-test investigation.  We
	// trace the reader self-test address window and assert SP never
	// descends into that code region (the original PR #42 failure mode was
	// the stack at &C100 overwriting the test function's own opcodes when
	// it spilled above &C000 — which must not recur).
	//
	// The window is derived from build/assembler.sym rather than hardcoded:
	// the routine's address shifts with every layout change, and a stale
	// fixed window ends up tracing whatever code drifts into it — code that
	// may legitimately run on the paged-call trampoline stack (&7EFE, see
	// TRAMP_SAFE_SP in src/trampoline.asm) and so trips the SP guard
	// spuriously.  The window spans run_reader_paged_self_tests up to the
	// first following symbol outside the routine's reader_paged_* block.
	syms, err := loadSAMSymbols(filepath.Join(root, "build", "assembler.sym"))
	if err != nil {
		t.Fatalf("build/assembler.sym unreadable: %v", err)
	}
	winLo, ok := syms["run_reader_paged_self_tests"]
	if !ok {
		t.Fatal("run_reader_paged_self_tests missing from build/assembler.sym")
	}
	winHi := uint32(0)
	for name, addr := range syms {
		if addr > winLo && (winHi == 0 || addr < winHi) && !strings.HasPrefix(name, "reader_paged_") {
			winHi = addr
		}
	}
	if winHi == 0 {
		t.Fatal("no symbol found after run_reader_paged_self_tests — cannot bound the trace window")
	}
	t.Logf("reader-test window: [%04X,%04X)", winLo, winHi)

	res, trace, _ := RunConfig(Config{
		AssemblerBin: asm, EnctabData: enc, InData: in,
		Files: []NamedFile{
			// sd13 listed before test_mem so test_mem wins the initial
			// page-13 pre-deposit (run_mem_self_tests runs first); the boot
			// later HGTHD+HLOADs "sd13" over page 13 via load_page13_payload
			// (PR-2) before run_sysreg_paged_self_tests reads the tables.
			{Name: "sd13", Content: sd13, TargetPage: 13},
			{Name: "test_mem", Content: tm, TargetPage: 13},
			// The off-axis "M5 + misc encoder" cluster on page 12 (M6
			// budget-relief PR).  load_offaxis_cluster HGTHD+HLOADs it at
			// boot; cluster_dispatch runs it via one LMPR swap.
			{Name: "cluster", Content: cluster, TargetPage: 12},
			{Name: "p14", Content: p14, TargetPage: 14},
			// The disassembler payload, HLOAD'd by load_page15_payload
			// (src/loader.asm) as a DOS CODE file "d15" into physical
			// page 15.  The BUILD_TESTS boot does a paged_call to
			// DISASM_SELF_TEST_ENTRY (&8003) on that page; without it
			// served, page 15 is empty and the paged_call jumps into a
			// zero page (trap → &0038).
			{Name: "d15", Content: d15, TargetPage: 15},
		},
		Timeout: 10 * time.Second,
		TraceLo: uint16(winLo), TraceHi: uint16(winHi),
	})

	t.Logf("Exit: %s", res.ExitReason)
	t.Logf("Printer: %q", res.PrinterCapture)
	t.Logf("FaultRegs: %s", res.FaultRegs)
	t.Logf("reader-window trace entries: %d", len(trace))

	if !res.Passed {
		t.Fatalf("test variant boot self-tests did not pass: printer=%q exit=%q regs=%s",
			res.PrinterCapture, res.ExitReason, res.FaultRegs)
	}
	// The trace window filters on logical PC only, so it also captures
	// off-axis code (pages 12/13/15 paged into section C, running on the
	// trampoline stack at &7EFE) whose logical addresses happen to overlap
	// the window.  The routine under guard executes from the production
	// code page (HMPR = hmprDefault; bits 5-7 of HMPR are CLUT, so mask),
	// so only those steps are the guard's business.
	prodSteps := 0
	for _, s := range trace {
		if s.HMPR&0x1F == hmprDefault {
			prodSteps++
		}
	}
	// The regression guard below is only meaningful if execution actually
	// entered the reader-test routine on the production page; a trace of
	// off-axis pass-through entries alone would let it pass vacuously.
	// Fail loudly rather than rot into a no-op.
	if prodSteps == 0 {
		t.Fatalf("reader-test window [%04X,%04X) never entered on the production page — regression guard is not exercising the target routine", winLo, winHi)
	}
	// Regression guard for the PR #42 failure mode: the boot stack at
	// &C100 must never collide with code executing in the reader-test
	// region; flag if SP ever dips into the reader-test window itself.
	for _, s := range trace {
		if s.HMPR&0x1F == hmprDefault && uint32(s.SP) < winHi {
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
