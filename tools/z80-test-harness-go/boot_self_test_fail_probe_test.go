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
	// Test disk: corrupts the &8003 self-test, which only exists in the
	// TEST disasm binary (disasm-test.bin).
	d15Path := filepath.Join(root, "build", "disasm-test.bin")
	tmPath := filepath.Join(root, "build", "test_mem.bin")
	clusterPath := filepath.Join(root, "build", "test_cluster.bin")
	encFixPath := filepath.Join(root, "build", "enc_fix_payload.bin")
	p14Path := filepath.Join(root, "build", "paged_call_test_payload.bin")
	zx0Path := filepath.Join(root, "build", "zx0-test.bin")

	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, tmPath, clusterPath, encFixPath, p14Path, zx0Path} {
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
	encFix, _ := os.ReadFile(encFixPath)
	p14, _ := os.ReadFile(p14Path)
	zx0, _ := os.ReadFile(zx0Path)

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

	res := runBootSelfTests(asm, enc, in, sd13, broken, tm, cluster, encFix, p14, zx0)

	t.Logf("Exit: %s", res.ExitReason)
	t.Logf("Printer: %q", res.PrinterCapture)

	if res.Passed {
		t.Fatalf("BROKEN disasm self-test still produced a passing boot — the gate is vacuous!")
	}
	tag, pc, ok := parseFailBanner(res.PrinterCapture)
	if !ok {
		t.Fatalf("expected a FAIL banner from the broken disasm self-test, got printer=%q exit=%q",
			res.PrinterCapture, res.ExitReason)
	}
	if !strings.EqualFold(tag, "ee") {
		t.Errorf("expected fail tag EE from the injected fault, got %q (printer=%q)", tag, res.PrinterCapture)
	}
	// The disasm self-test fails via `jp fail_with_tag` (a direct site), so the
	// banner's call-site PC field must be zero — proving fail_with_tag does not
	// leak a stale LAST_FAIL_PC from an earlier (passing) assert helper.
	if pc != 0 {
		t.Errorf("direct fail_with_tag site should report pc=0000, got %04X (printer=%q)", pc, res.PrinterCapture)
	}
	if !t.Failed() {
		t.Logf("gate correctly caught the broken disasm self-test with fail tag %q pc=%04X", tag, pc)
	}
}

// TestBootSelfTestsZX0FailProbe is the same negative control for the i68
// zx0 boot self-test: it corrupts run_zx0_self_test in the zx0-test.bin
// payload so the paged_call returns a non-zero fail tag, and asserts the
// boot halts with that tag's FAIL banner.  Proves the zx0 gate fails
// loudly rather than passing vacuously — the page-13 time-multiplex seam
// the i60c design review flagged as the riskiest part of the wiring.
func TestBootSelfTestsZX0FailProbe(t *testing.T) {
	root := repoRoot(t)

	asmPath := filepath.Join(root, "build", "assembler.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	sd13Path := filepath.Join(root, "build", "sysreg_data.bin")
	d15Path := filepath.Join(root, "build", "disasm-test.bin")
	tmPath := filepath.Join(root, "build", "test_mem.bin")
	clusterPath := filepath.Join(root, "build", "test_cluster.bin")
	encFixPath := filepath.Join(root, "build", "enc_fix_payload.bin")
	p14Path := filepath.Join(root, "build", "paged_call_test_payload.bin")
	zx0Path := filepath.Join(root, "build", "zx0-test.bin")

	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, tmPath, clusterPath, encFixPath, p14Path, zx0Path} {
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
	encFix, _ := os.ReadFile(encFixPath)
	p14, _ := os.ReadFile(p14Path)
	zx0, _ := os.ReadFile(zx0Path)

	var in []byte

	// Corrupt run_zx0_self_test.  The zx0-test payload loads at &8400 and
	// the self-test entry is ZX0_SELF_TEST_ENTRY = &AFA0 (src/zx0_comm.inc),
	// i.e. file offset &AFA0 - &8400.  Overwrite the entry so it returns a
	// non-zero fail tag immediately:
	//   01 E9 00   ld bc, &00E9   ; C = 0xE9 probe tag, B = 0
	//   C9         ret
	// On return the boot sees BC != 0 → `ld a,c; jp fail_with_tag`.
	const zx0SelfTestFileOff = 0xAFA0 - 0x8400
	if len(zx0) <= zx0SelfTestFileOff {
		t.Fatalf("zx0-test.bin too short (%d B) to contain the &AFA0 self-test entry — payload layout drifted?", len(zx0))
	}
	broken := append([]byte(nil), zx0...)
	copy(broken[zx0SelfTestFileOff:], []byte{0x01, 0xE9, 0x00, 0xC9})

	res := runBootSelfTests(asm, enc, in, sd13, d15, tm, cluster, encFix, p14, broken)

	t.Logf("Exit: %s", res.ExitReason)
	t.Logf("Printer: %q", res.PrinterCapture)

	if res.Passed {
		t.Fatalf("BROKEN zx0 self-test still produced a passing boot — the gate is vacuous!")
	}
	tag, pc, ok := parseFailBanner(res.PrinterCapture)
	if !ok {
		t.Fatalf("expected a FAIL banner from the broken zx0 self-test, got printer=%q exit=%q",
			res.PrinterCapture, res.ExitReason)
	}
	if !strings.EqualFold(tag, "e9") {
		t.Errorf("expected fail tag E9 from the injected fault, got %q (printer=%q)", tag, res.PrinterCapture)
	}
	// Direct fail_with_tag site → call-site PC field must be zero.
	if pc != 0 {
		t.Errorf("direct fail_with_tag site should report pc=0000, got %04X (printer=%q)", pc, res.PrinterCapture)
	}
	if !t.Failed() {
		t.Logf("gate correctly caught the broken zx0 self-test with fail tag %q pc=%04X", tag, pc)
	}
}
