// boot_self_test_test.go — fast LOCAL verification that the full BUILD_TESTS
// variant assembler boots clean through ALL of its boot-time self-test
// suites, INCLUDING the page-15 disassembler self-test.
//
// Why this exists
//
// The disasm boot path (load_page15_payload HLOADs "d15" into physical page
// 15; the BUILD_TESTS boot then paged_call's DISASM_SELF_TEST_ENTRY = &8003
// on that page and runs run_disasm_self_test) was, until now, only exercised
// by the SimCoupé CI matrix — minutes per push.  This test drives the same
// boot in the koron-go/z80 harness in well under a second, so disassembler
// iteration no longer needs a CI round-trip to confirm the boot path.
//
// How it works
//
//   - Loads the test-variant assembler.bin plus every payload the boot
//     HLOADs: enctab.enc (page 4), sysreg_data.bin as "sd13" (page 13),
//     test_mem.bin as "test_mem" (page 13), the off-axis cluster as "cluster"
//     (page 12), the paged_call payload as "p14" (page 14), and — the piece
//     this test is really about — disasm.bin as "d15" (page 15).
//   - Boots the assembler with a trivial fixture (nop; ret).  The boot runs
//     the five inline self-test suites + the off-axis suites + the disasm
//     paged-call self-test BEFORE it reaches load_enctab / main_assemble, so
//     reaching a successful assemble ("OK" on the printer channel + captured
//     OUT bytes) proves every boot self-test — disasm included — passed.
//   - Detects failure two ways: an explicit "FAIL<tag>" printer banner (any
//     self-test halts via fail / fail_with_tag, emitting FAIL plus two hex
//     digits of the fail tag), and a non-OK / trapped exit.  A broken disasm
//     self-test (or a section-B comm-buffer slot collision such as the new
//     DISASM_COMM_PC at &7EBD) surfaces here as a FAIL banner with its tag,
//     not as a CI failure minutes later.
//
// Requires (all from `make m3-asm enctab sysreg-data disasm-test-payload
// test-mem-offaxis cluster-offaxis paged-call-payload sam-aarch64`):
//
//	build/assembler.bin   build/enctab.enc   build/sysreg_data.bin
//	build/disasm-test.bin build/test_mem.bin build/test_cluster.bin
//	build/paged_call_test_payload.bin   build/sam-aarch64
//
// Skipped automatically if any artefact is absent.  SimCoupé remains the sole
// CI gate; this is a fast inner-loop check.
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

func TestBootSelfTestsPass(t *testing.T) {
	root := repoRoot(t)

	asmPath := filepath.Join(root, "build", "assembler.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	sd13Path := filepath.Join(root, "build", "sysreg_data.bin")
	// Test disk: the BUILD_TESTS assembler boot runs the disasm self-test,
	// so it needs the TEST disasm binary (disasm-test.bin) on page 15.
	d15Path := filepath.Join(root, "build", "disasm-test.bin")
	tmPath := filepath.Join(root, "build", "test_mem.bin")
	clusterPath := filepath.Join(root, "build", "test_cluster.bin")
	p14Path := filepath.Join(root, "build", "paged_call_test_payload.bin")
	samPath := filepath.Join(root, "build", "sam-aarch64")
	fixturePath := filepath.Join(root, "tests", "m3", "sources", "inst_nop_ret.s")

	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, tmPath, clusterPath, p14Path, samPath, fixturePath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("prerequisite missing: %s\n  run `make m3-asm enctab sysreg-data disasm-test-payload test-mem-offaxis cluster-offaxis paged-call-payload sam-aarch64`", p)
		}
	}

	// Assemble the trivial fixture to a binary + compact .tbn
	// (sam-aarch64 --emit-tbn) so main_assemble has something to chew on
	// after the self-test block; reaching its "OK" proves the boot got all
	// the way through the self-tests.  The SAM assembler consumes the compact
	// v2 .tbn (INSN_RUN decoder).
	tmp := t.TempDir()
	tbnPath := filepath.Join(tmp, "inst_nop_ret.compact.tbn")
	goImgPath := filepath.Join(tmp, "inst_nop_ret.go.img")
	cmd := exec.Command(samPath, "-o", goImgPath, "-emit-tbn", tbnPath, fixturePath)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sam-aarch64 failed: %v\n%s", err, out)
	}

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	tm, _ := os.ReadFile(tmPath)
	cluster, _ := os.ReadFile(clusterPath)
	p14, _ := os.ReadFile(p14Path)
	in, _ := os.ReadFile(tbnPath)

	res := runBootSelfTests(asm, enc, in, sd13, d15, tm, cluster, p14)

	t.Logf("Exit: %s", res.ExitReason)
	t.Logf("Printer: %q", res.PrinterCapture)
	t.Logf("Steps: %d", res.Steps)
	if len(res.UnservedFiles) != 0 {
		t.Errorf("boot HGTHD'd file(s) the harness did not serve: %v "+
			"(an unserved d15 means the disasm self-test ran against an empty page 15)",
			res.UnservedFiles)
	}

	// Explicit fail-tag detection: a failed self-test emits "FAIL<hh>\n" on
	// the printer channel (fail / fail_with_tag in src/assembler.asm), where
	// <hh> is two hex digits of LAST_FAIL_TAG.  The disasm self-test's
	// failure path is `ld a,c; jp fail_with_tag`, so a broken disasm
	// self-test shows up here with its tag.
	if strings.HasPrefix(res.PrinterCapture, "FAIL") {
		tag := strings.TrimSpace(strings.TrimPrefix(res.PrinterCapture, "FAIL"))
		t.Fatalf("a boot self-test FAILED (fail tag %q): printer=%q exit=%q regs=%s",
			tag, res.PrinterCapture, res.ExitReason, res.FaultRegs)
	}

	if !res.Passed {
		t.Fatalf("boot did not reach a clean OK assemble (a self-test halted, "+
			"trapped, or timed out before main_assemble): printer=%q exit=%q regs=%s",
			res.PrinterCapture, res.ExitReason, res.FaultRegs)
	}

	// Sanity: the OK we reached is a real assemble, not a stray banner — the
	// nop; ret fixture must round-trip to its known 8-byte encoding.  This
	// guards against a future change that prints OK without actually running
	// the pipeline (which would make the self-test gate vacuous).
	//   nop; ret → 1F 20 03 D5  C0 03 5F D6
	wantOut := []byte{0x1F, 0x20, 0x03, 0xD5, 0xC0, 0x03, 0x5F, 0xD6}
	if !bytes.Equal(res.OutBytes, wantOut) {
		t.Errorf("OUT mismatch after a passing boot: got %X want %X", res.OutBytes, wantOut)
	}
}

// runBootSelfTests wires every boot payload (in the same page assignments the
// boot expects) and runs the test-variant assembler to completion.  The d15
// disasm payload on page 15 is the piece the earlier harness lacked.
func runBootSelfTests(asm, enc, in, sd13, d15, tm, cluster, p14 []byte) Result {
	return RunWithFiles(asm, enc, in, []NamedFile{
		// sd13 before test_mem so test_mem wins the initial page-13
		// pre-deposit (run_mem_self_tests runs first); the boot later
		// HLOADs sd13 over page 13 before the sysreg suite reads it.
		{Name: "sd13", Content: sd13, TargetPage: 13},
		{Name: "test_mem", Content: tm, TargetPage: 13},
		{Name: "cluster", Content: cluster, TargetPage: 12},
		{Name: "p14", Content: p14, TargetPage: 14},
		// The disassembler payload on physical page 15: HLOAD'd by
		// load_page15_payload as "d15", then exercised by the BUILD_TESTS
		// paged_call to DISASM_SELF_TEST_ENTRY.
		{Name: "d15", Content: d15, TargetPage: 15},
	}, 10*time.Second)
}
