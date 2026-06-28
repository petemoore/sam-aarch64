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
//     no disasm/zx0/sysreg self-test runs in this variant — the
//     encode_inst fixture payload as "enc_fix" (page 11), and the i204b
//     overlay_classify suite code payload as "ovl12" (page 12).
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
//   - TestBootSelfTestsOverlayFailProbe is the same negative control for
//     the i204b overlay suite: it corrupts the first toc_ci_table row's
//     expected base word (offset located via enc_fix_payload.sym) and
//     asserts the FAIL banner carries that row's pointer.
//
// Requires (all from `make assembler-enc-tests enctab sysreg-data
// disasm-payload zx0-payload enc-fix-payload overlay-suite sam-aarch64`):
//
//	build/assembler-enc-tests.bin  build/enctab.enc  build/sysreg_data.bin
//	build/disasm.bin  build/zx0.bin  build/enc_fix_payload.bin
//	build/overlay_suite.bin  build/sam-aarch64
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
func encTestsArtifacts(t *testing.T, root string) (asmPath, encPath, sd13Path, d15Path, zx0Path, encFixPath, ovlSuitePath string) {
	t.Helper()
	asmPath = filepath.Join(root, "build", "assembler-enc-tests.bin")
	encPath = filepath.Join(root, "build", "enctab.enc")
	sd13Path = filepath.Join(root, "build", "sysreg_data.bin")
	// No disasm/zx0 self-test runs in this variant, so the PROD payloads
	// (the ones its disk ships) are the right flavours.
	d15Path = filepath.Join(root, "build", "disasm.bin")
	zx0Path = filepath.Join(root, "build", "zx0.bin")
	encFixPath = filepath.Join(root, "build", "enc_fix_payload.bin")
	ovlSuitePath = filepath.Join(root, "build", "overlay_suite.bin")
	for _, p := range []string{asmPath, encPath, sd13Path, d15Path, zx0Path, encFixPath, ovlSuitePath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("prerequisite missing: %s\n  run `make assembler-enc-tests enctab sysreg-data disasm-payload zx0-payload enc-fix-payload overlay-suite sam-aarch64`", p)
		}
	}
	return
}

func TestBootSelfTestsEncodePass(t *testing.T) {
	root := repoRoot(t)

	asmPath, encPath, sd13Path, d15Path, zx0Path, encFixPath, ovlSuitePath := encTestsArtifacts(t, root)
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
	ovlSuite, _ := os.ReadFile(ovlSuitePath)
	in, _ := os.ReadFile(tbnPath)

	res := runBootEncodeSelfTests(asm, enc, in, sd13, d15, zx0, encFix, ovlSuite)

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

	asmPath, encPath, sd13Path, d15Path, zx0Path, encFixPath, ovlSuitePath := encTestsArtifacts(t, root)

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	zx0, _ := os.ReadFile(zx0Path)
	encFix, _ := os.ReadFile(encFixPath)
	ovlSuite, _ := os.ReadFile(ovlSuitePath)

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

	res := runBootEncodeSelfTests(asm, enc, in, sd13, d15, zx0, broken, ovlSuite)

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

// TestBootSelfTestsOverlayFailProbe is the negative control for the i204b
// overlay_classify suite: it corrupts the expected base word of the first
// toc_ci_table row in the enc_fix payload (the overlay fixtures ride the
// tail of that payload) and asserts the boot halts with the suite's FAIL
// banner carrying that row's pointer.  Proves the overlay gate fails
// loudly rather than passing vacuously — and, because the encode suite
// runs first and must PASS for the boot to even reach the overlay suite,
// it also proves the overlay suite genuinely executes.
func TestBootSelfTestsOverlayFailProbe(t *testing.T) {
	root := repoRoot(t)

	asmPath, encPath, sd13Path, d15Path, zx0Path, encFixPath, ovlSuitePath := encTestsArtifacts(t, root)

	// Locate toc_ci_table inside the payload from its sym export.  The
	// payload is org'd at ENC_FIX_TABLE_RAM, so file offset = addr - org.
	syms, err := loadSAMSymbols(filepath.Join(root, "build", "enc_fix_payload.sym"))
	if err != nil {
		t.Fatalf("build/enc_fix_payload.sym unreadable: %v", err)
	}
	tocCiTable, ok := syms["toc_ci_table"]
	if !ok {
		t.Fatal("toc_ci_table missing from build/enc_fix_payload.sym — overlay fixture block gone?")
	}

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	zx0, _ := os.ReadFile(zx0Path)
	encFix, _ := os.ReadFile(encFixPath)
	ovlSuite, _ := os.ReadFile(ovlSuitePath)

	// No IN .tbn needed: the overlay self-test runs before main_assemble.
	var in []byte

	// Corrupt row 0's expected base word.  toc_ci_table rows are 12 bytes:
	// {pc_lo16, fixture ptr, opcount, mnemonic id, is_literal, base word
	// at +8} (src/test_encode_inst_payload.asm), so +8 is the first
	// expected base byte.  Flipping it guarantees a base-word mismatch on
	// fixture 0 → toc_fail records the row pointer (= toc_ci_table) in
	// LAST_FAIL_PC and the assertion tag TOC_TAG_CI_BASE (&d2) in
	// LAST_FAIL_TAG → banner FAILd2<toc_ci_table>.
	const rowBaseOff = 8
	off := int(tocCiTable) - encFixTableRAM + rowBaseOff
	if off <= 0 || off >= len(encFix) {
		t.Fatalf("computed corruption offset %d outside enc_fix payload (%d B) — payload layout drifted?", off, len(encFix))
	}
	broken := append([]byte(nil), encFix...)
	broken[off] ^= 0xFF

	res := runBootEncodeSelfTests(asm, enc, in, sd13, d15, zx0, broken, ovlSuite)

	t.Logf("Exit: %s", res.ExitReason)
	t.Logf("Printer: %q", res.PrinterCapture)

	if res.Passed {
		t.Fatalf("BROKEN overlay fixture still produced a passing boot — the gate is vacuous!")
	}
	tag, pc, ok := parseFailBanner(res.PrinterCapture)
	if !ok {
		t.Fatalf("expected a FAIL banner from the corrupted overlay fixture, got printer=%q exit=%q",
			res.PrinterCapture, res.ExitReason)
	}
	if !strings.EqualFold(tag, "d2") {
		t.Errorf("expected the TOC_TAG_CI_BASE tag d2, got %q (printer=%q)", tag, res.PrinterCapture)
	}
	// toc_fail records the failing row pointer; row 0 sits at toc_ci_table.
	if pc != uint16(tocCiTable) {
		t.Errorf("expected the failing row pointer %04X in the banner, got %04X (printer=%q)",
			tocCiTable, pc, res.PrinterCapture)
	}
	if !t.Failed() {
		t.Logf("gate correctly caught the corrupted overlay fixture with tag %q pc=%04X", tag, pc)
	}
}

// runBootEncodeSelfTests wires the payload set the enc-tests boot HLOADs (in
// the same page assignments it expects) and runs assembler-enc-tests.bin to
// completion.  Counterpart of runBootSelfTests for the BUILD_TESTS_ENCODE
// variant (i234).
func runBootEncodeSelfTests(asm, enc, in, sd13, d15, zx0, encFix, ovlSuite []byte) Result {
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
		// The i204b overlay-suite CODE payload on physical page 12:
		// HLOAD'd by load_overlay_suite as "ovl12", then LDIR'd by the
		// boot stub in assembler.asm to section-D RAM at
		// OVERLAY_SUITE_RAM and executed there.
		{Name: "ovl12", Content: ovlSuite, TargetPage: 12},
	}, 10*time.Second)
}
