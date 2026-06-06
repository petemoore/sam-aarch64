// boot_self_test_fail_probe_test.go — negative control for TestBootSelfTestsPass.
//
// Proves the boot-self-test gate actually FAILS when the disasm self-test is
// broken, rather than passing vacuously.  It corrupts the d15 payload's
// run_disasm_self_test so the test returns a non-zero fail tag in BC; the
// boot's `ld a,b; or c; jr z,...; ld a,c; jp fail_with_tag` then halts with a
// FAIL banner — exactly what a real regression (e.g. a DISASM_COMM_PC slot
// collision) would produce.
//
// This test EXPECTS the boot to fail and asserts the harness detects it; it
// passes precisely when the gate is working.  Skipped if artefacts are absent.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootSelfTestsFailProbe(t *testing.T) {
	root := repoRoot(t)

	asmPath := filepath.Join(root, "build", "assembler.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	sd13Path := filepath.Join(root, "build", "sysreg_data.bin")
	d15Path := filepath.Join(root, "build", "disasm.bin")
	tmPath := filepath.Join(root, "build", "test_mem.bin")
	clusterPath := filepath.Join(root, "build", "test_cluster.bin")
	p14Path := filepath.Join(root, "build", "paged_call_test_payload.bin")

	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, tmPath, clusterPath, p14Path} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("prerequisite missing: %s", p)
		}
	}

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	tm, _ := os.ReadFile(tmPath)
	cluster, _ := os.ReadFile(clusterPath)
	p14, _ := os.ReadFile(p14Path)

	// No IN .tbn needed: the disasm self-test runs long before main_assemble,
	// so the boot fails before it would consume IN.  An empty IN is fine.
	var in []byte

	// Corrupt the disasm self-test entry.  DISASM_SELF_TEST_ENTRY (&8003) is
	// the second jp in disasm.bin's header (offset 3): `jp run_disasm_self_test`.
	// Overwrite it so the routine immediately loads a non-zero fail tag into
	// BC and returns, instead of running the real self-test:
	//   01 EE 00   ld bc, &00EE   ; C = 0xEE fail tag, B = 0
	//   C9         ret
	// On return the boot sees BC != 0 → `ld a,c; jp fail_with_tag` with tag EE.
	broken := append([]byte(nil), d15...)
	copy(broken[3:], []byte{0x01, 0xEE, 0x00, 0xC9})

	res := runBootSelfTests(asm, enc, in, sd13, broken, tm, cluster, p14)

	t.Logf("Exit: %s", res.ExitReason)
	t.Logf("Printer: %q", res.PrinterCapture)

	if res.Passed {
		t.Fatalf("BROKEN disasm self-test still produced a passing boot — the gate is vacuous!")
	}
	if !strings.HasPrefix(res.PrinterCapture, "FAIL") {
		t.Fatalf("expected a FAIL banner from the broken disasm self-test, got printer=%q exit=%q",
			res.PrinterCapture, res.ExitReason)
	}
	tag := strings.TrimSpace(strings.TrimPrefix(res.PrinterCapture, "FAIL"))
	if !strings.EqualFold(tag, "ee") {
		t.Errorf("expected fail tag EE from the injected fault, got %q (printer=%q)", tag, res.PrinterCapture)
	} else {
		t.Logf("gate correctly caught the broken disasm self-test with fail tag %q", tag)
	}
}
