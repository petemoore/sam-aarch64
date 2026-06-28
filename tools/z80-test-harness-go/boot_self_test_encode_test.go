// boot_self_test_encode_test.go — fast LOCAL verification that the
// BUILD_TESTS_ENCODE variant (build/assembler-enc-tests.bin, i234) boots
// clean through the encode_inst self-test family.
//
// # Why this exists
//
// The encode_inst family (insn_encode.asm + test_encode_inst.asm + the
// page-11 enc_fix fixture payload) is ENCTAB-coupled and must run inline in
// section C, so it lives in its own boot variant rather than sharing the
// BUILD_TESTS variant's section-C test budget — section-C test memory is
// time-multiplexed across two boot runs.  This test drives the enc-tests
// boot in the koron-go/z80 harness in well under a second; the enc-tests CI
// job (SimCoupé) is the gate.
//
// How it works
//
//   - Loads assembler-enc-tests.bin plus every payload its boot HLOADs:
//     enctab.enc (page 4), the unconditional production payloads —
//     sysreg_data.bin as "sd13" (page 13), zx0.bin as "zx013" (page 13 at
//     offset &0400), disasm.bin as "d15" (page 15), all PROD flavours since
//     no disasm/zx0/sysreg self-test runs in this variant — and the
//     encode_inst fixture payload as "enc_fix" (page 11).
//   - Boots with a trivial fixture (nop; ret).  The boot runs ONLY the
//     encode_inst self-test family (after load_enctab) before
//     main_assemble, so a clean OK + the known OUT bytes proves the encode
//     suite passed.
//   - TestBootSelfTestsEncodeFailProbe is the negative control: it corrupts
//     the first fixture row's expected word in the enc_fix payload and
//     asserts the boot halts with the suite's FAIL banner (tag 00 — the
//     enc_fix_fail path is an untagged `jp fail` — and pc = the failing row
//     pointer, ENC_FIX_TABLE_RAM = &E100), proving the gate fails loudly
//     rather than passing vacuously.
//
// Requires (all from `make assembler-enc-tests enctab sysreg-data
// disasm-payload zx0-payload enc-fix-payload sam-aarch64`):
//
//	build/assembler-enc-tests.bin  build/enctab.enc  build/sysreg_data.bin
//	build/disasm.bin  build/zx0.bin  build/enc_fix_payload.bin
//	build/sam-aarch64
//
// SimCoupé remains the sole CI gate; this is a fast inner-loop check.
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

// encFixTableRAM mirrors ENC_FIX_TABLE_RAM (src/trampoline.asm): the org of
// the enc_fix payload, and therefore the address of its first fixture row —
// the row pointer the fail banner reports when row 0's compare fails.
const encFixTableRAM = 0xE100

// encTestsArtifacts resolves and stat-checks every artifact the enc-tests
// boot needs, returning the paths keyed for the reads below.
func encTestsArtifacts(t *testing.T, root string) (asmPath, encPath, sd13Path, d15Path, zx0Path, encFixPath string) {
	t.Helper()
	asmPath = filepath.Join(root, "build", "assembler-enc-tests.bin")
	encPath = filepath.Join(root, "build", "enctab.enc")
	sd13Path = filepath.Join(root, "build", "sysreg_data.bin")
	// No disasm/zx0 self-test runs in this variant, so the PROD payloads
	// (the ones its disk ships) are the right flavours.
	d15Path = filepath.Join(root, "build", "disasm.bin")
	zx0Path = filepath.Join(root, "build", "zx0.bin")
	encFixPath = filepath.Join(root, "build", "enc_fix_payload.bin")
	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, zx0Path, encFixPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("prerequisite missing: %s\n  run `make assembler-enc-tests enctab sysreg-data disasm-payload zx0-payload enc-fix-payload sam-aarch64`", p)
		}
	}
	return
}

func TestBootSelfTestsEncodePass(t *testing.T) {
	root := repoRoot(t)

	asmPath, encPath, sd13Path, d15Path, zx0Path, encFixPath := encTestsArtifacts(t, root)
	samPath := filepath.Join(root, "build", "sam-aarch64")
	fixturePath := filepath.Join(root, "tests", "core", "sources", "inst_nop_ret.s")
	for _, p := range []string{samPath, fixturePath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("prerequisite missing: %s", p)
		}
	}

	// Assemble the trivial fixture to a compact .tbn so main_assemble has
	// something to chew on after the encode self-tests; reaching its "OK"
	// proves the boot got all the way through them.
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
	zx0, _ := os.ReadFile(zx0Path)
	encFix, _ := os.ReadFile(encFixPath)
	in, _ := os.ReadFile(tbnPath)

	res := runBootEncodeSelfTests(asm, enc, in, sd13, d15, zx0, encFix)

	t.Logf("Exit: %s", res.ExitReason)
	t.Logf("Printer: %q", res.PrinterCapture)
	t.Logf("Steps: %d", res.Steps)
	if len(res.UnservedFiles) != 0 {
		t.Errorf("boot HGTHD'd file(s) the harness did not serve: %v "+
			"(an unserved enc_fix means the encode self-test ran against an empty page 11)",
			res.UnservedFiles)
	}

	if strings.HasPrefix(res.PrinterCapture, "FAIL") {
		symPath := filepath.Join(root, "build", "assembler-enc-tests.sym")
		t.Fatalf("an encode boot self-test FAILED [%s]: printer=%q exit=%q regs=%s",
			describeFailBanner(res.PrinterCapture, symPath), res.PrinterCapture, res.ExitReason, res.FaultRegs)
	}

	if !res.Passed {
		t.Fatalf("boot did not reach a clean OK assemble (the encode self-test halted, "+
			"trapped, or timed out before main_assemble): printer=%q exit=%q regs=%s",
			res.PrinterCapture, res.ExitReason, res.FaultRegs)
	}

	// Sanity: the OK we reached is a real assemble, not a stray banner —
	// nop; ret → 1F 20 03 D5  C0 03 5F D6 (same guard as TestBootSelfTestsPass).
	wantOut := []byte{0x1F, 0x20, 0x03, 0xD5, 0xC0, 0x03, 0x5F, 0xD6}
	if !bytes.Equal(res.OutBytes, wantOut) {
		t.Errorf("OUT mismatch after a passing boot: got %X want %X", res.OutBytes, wantOut)
	}
}

// TestBootSelfTestsEncodeFailProbe is the negative control: it corrupts the
// enc_fix payload so the very first fixture's expected-word compare fails,
// and asserts the harness sees the suite's FAIL banner.  Proves the encode
// gate fails loudly rather than passing vacuously.
func TestBootSelfTestsEncodeFailProbe(t *testing.T) {
	root := repoRoot(t)

	asmPath, encPath, sd13Path, d15Path, zx0Path, encFixPath := encTestsArtifacts(t, root)

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	zx0, _ := os.ReadFile(zx0Path)
	encFix, _ := os.ReadFile(encFixPath)

	// No IN .tbn needed: the encode self-test runs before main_assemble,
	// so the boot fails before it would consume IN.
	var in []byte

	// Corrupt row 0's expected word.  The enc_fix payload starts with
	// enc_fix_table at its org (ENC_FIX_TABLE_RAM); each 11-byte row is
	// {pc_lo16, fixture ptr, opcount, mnemonic id, expected word at +7}
	// (src/test_encode_inst.asm), so payload offset 7 is the first
	// expected byte.  Flipping it guarantees a mismatch on fixture 0 →
	// enc_fix_fail records the row pointer (&E100) in LAST_FAIL_PC and
	// does an untagged `jp fail` → banner FAIL00E100.
	const rowExpectedOff = 7
	if len(encFix) <= rowExpectedOff {
		t.Fatalf("enc_fix_payload.bin too short (%d B) to contain a fixture row — payload layout drifted?", len(encFix))
	}
	broken := append([]byte(nil), encFix...)
	broken[rowExpectedOff] ^= 0xFF

	res := runBootEncodeSelfTests(asm, enc, in, sd13, d15, zx0, broken)

	t.Logf("Exit: %s", res.ExitReason)
	t.Logf("Printer: %q", res.PrinterCapture)

	if res.Passed {
		t.Fatalf("BROKEN encode fixture still produced a passing boot — the gate is vacuous!")
	}
	tag, pc, ok := parseFailBanner(res.PrinterCapture)
	if !ok {
		t.Fatalf("expected a FAIL banner from the corrupted encode fixture, got printer=%q exit=%q",
			res.PrinterCapture, res.ExitReason)
	}
	if !strings.EqualFold(tag, "00") {
		t.Errorf("expected the untagged enc_fix_fail tag 00, got %q (printer=%q)", tag, res.PrinterCapture)
	}
	// enc_fix_fail records the failing row pointer; row 0 sits at the
	// payload org, so the banner PC must be exactly ENC_FIX_TABLE_RAM.
	if pc != encFixTableRAM {
		t.Errorf("expected the failing row pointer %04X in the banner, got %04X (printer=%q)",
			encFixTableRAM, pc, res.PrinterCapture)
	}
	if !t.Failed() {
		t.Logf("gate correctly caught the corrupted encode fixture with tag %q pc=%04X", tag, pc)
	}
}

// runBootEncodeSelfTests wires the payload set the enc-tests boot HLOADs (in
// the same page assignments it expects) and runs assembler-enc-tests.bin to
// completion.  Counterpart of runBootSelfTests for the BUILD_TESTS_ENCODE
// variant (i234).
func runBootEncodeSelfTests(asm, enc, in, sd13, d15, zx0, encFix []byte) Result {
	return RunWithFiles(asm, enc, in, []NamedFile{
		// The unconditional production payload loads (load_page13_payload,
		// load_zx0_payload, load_page15_payload) run in every variant.
		{Name: "sd13", Content: sd13, TargetPage: 13},
		{Name: "zx013", Content: zx0, TargetPage: 13, LoadOffset: 0x0400},
		{Name: "d15", Content: d15, TargetPage: 15},
		// The encode_inst fixture payload on physical page 11: HLOAD'd by
		// load_enc_fix_payload as "enc_fix", then bulk-copied to section-D
		// RAM at ENC_FIX_TABLE_RAM by run_encode_inst_self_tests before
		// enctab_map_in.
		{Name: "enc_fix", Content: encFix, TargetPage: 11},
	}, 10*time.Second)
}
