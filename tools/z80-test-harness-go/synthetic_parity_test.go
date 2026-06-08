// synthetic_parity_test.go — synthetic-fixture Z80↔Go disassembler parity
// sweep (item i9, "parity robustness seeds").
//
// # What this certifies
//
// The release-corpus oracle (disasm_oracle_test.go::TestDisasmOracle) reaches
// 100% but is *corpus-bounded*: it only ever compares the instruction families
// that actually appear in tests/m6/release/release.img.  The i10 capability
// report (docs/notes/2026-06-08-go-vs-z80-disasm-capability-parity.md) found
// ~two dozen families that src/disasm.asm *handles* but the oracle never
// exercises, so their Z80 correctness was asserted only *structurally* (the
// handler exists, mirrors the Go source) — never *empirically tested*.
//
// This sweep closes that gap for the families the i10 report ranked
// highest-value, by generating synthetic instruction words (assembled with
// GNU `as`, the ground-truth encoder) and asserting, for EVERY word, that the
// on-SAM Z80 disassembler (build/disasm.bin) decodes it byte-for-byte
// identically to the Go authority (aarch64dec.DecodeAt) — including the
// ".inst 0x…" fallback when Go declines.  Go is the authority: a word Go
// declines MUST decode to ".inst" on the Z80 too, and vice-versa.
//
// Families certified here (structural → empirical):
//   - conditional select base forms: csel, csinc
//   - conditional select aliases:    cset, csetm, cinc, cinv, cneg
//   - extended-register add/sub:      add/sub/adds/subs Xd, Xn, Wm, {u,s}xt{b,h,w}/sxtx [#sh]
//   - signed multiply long/high:      smull, smaddl, smsubl, smnegl, smulh
//
// `sdiv` is deliberately excluded (known-missing across the whole stack —
// encoder + decoder both lack it; tracked separately as item i35).  The
// conditional-COMPARE family (ccmp/ccmn) and the *non-alias* base csinv/csneg
// forms are ALSO excluded here, because they are genuine Z80↔Go DISAGREEMENTS
// found while building this sweep — see TestSyntheticParity_KnownDisagreements
// below and the report in docs/notes/2026-06-08-z80-go-disasm-parity-i9.md.
// Per the prime directive those are reported, not worked around.
//
// Not a CI gate — same philosophy as the oracle (SimCoupé is the only gate).
// Requires GNU `as` (aarch64-none-elf-as or aarch64-linux-gnu-as) and a built
// build/disasm.bin.  Skips cleanly if either prerequisite is missing.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// gnuAssembleWords assembles each source line with GNU `as`, links flat at
// base 0, and returns the resulting little-endian 32-bit words in source
// order.  Skips the calling test if no GNU toolchain is present.
func gnuAssembleWords(t *testing.T, lines []string) []uint32 {
	t.Helper()

	as, ld, objcopy := "aarch64-none-elf-as", "aarch64-none-elf-ld", "aarch64-none-elf-objcopy"
	if _, e := exec.LookPath(as); e != nil {
		as, ld, objcopy = "aarch64-linux-gnu-as", "aarch64-linux-gnu-ld", "aarch64-linux-gnu-objcopy"
		if _, e2 := exec.LookPath(as); e2 != nil {
			t.Skip("no GNU aarch64 assembler (aarch64-none-elf-as / aarch64-linux-gnu-as) on PATH")
		}
	}

	tmp := t.TempDir()
	src := tmp + "/sweep.s"
	var body []byte
	for _, l := range lines {
		body = append(body, []byte(l)...)
		body = append(body, '\n')
	}
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatalf("write synthetic source: %v", err)
	}

	oPath, elfPath, binPath := tmp+"/sweep.o", tmp+"/sweep.elf", tmp+"/sweep.bin"
	if out, e := exec.Command(as, src, "-o", oPath).CombinedOutput(); e != nil {
		t.Fatalf("GNU as failed (a source line is not valid aarch64):\n%s", out)
	}
	if out, e := exec.Command(ld, "-Ttext=0", "-o", elfPath, oPath).CombinedOutput(); e != nil {
		t.Fatalf("GNU ld failed:\n%s", out)
	}
	if out, e := exec.Command(objcopy, "-O", "binary", elfPath, binPath).CombinedOutput(); e != nil {
		t.Fatalf("GNU objcopy failed:\n%s", out)
	}
	raw, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read GNU binary: %v", err)
	}
	if len(raw)%4 != 0 {
		t.Fatalf("GNU output length %d not a multiple of 4", len(raw))
	}
	words := make([]uint32, len(raw)/4)
	for i := range words {
		o := i * 4
		words[i] = uint32(raw[o]) | uint32(raw[o+1])<<8 | uint32(raw[o+2])<<16 | uint32(raw[o+3])<<24
	}
	return words
}

// assertZ80MatchesGo decodes each word with both the Z80 disassembler and the
// Go authority and fails on any divergence.  Go is the authority: identical
// (mnemonic, operands) — including the ".inst" fallback — is required.
func assertZ80MatchesGo(t *testing.T, disasmBin []byte, srcLines []string, words []uint32) {
	t.Helper()
	for i, w := range words {
		zm, zo, err := runZ80Disasm(disasmBin, w, 0)
		if err != nil {
			t.Fatalf("[%s] Z80 run failed (word=%#08x): %v", srcLines[i], w, err)
		}
		gm, go_ := goOracle(0, w)
		if zm != gm || zo != go_ {
			t.Errorf("PARITY MISMATCH on %q (word=%#08x):\n  Go : [%s|%s]\n  Z80: [%s|%s]",
				srcLines[i], w, gm, go_, zm, zo)
		}
	}
}

// loadProdDisasm reads the shipped PROD decoder (build/disasm.bin); skips the
// test if it has not been built.
func loadProdDisasm(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../build/disasm.bin")
	if err != nil {
		t.Skipf("build/disasm.bin not built (run `make build/disasm.bin`): %v", err)
	}
	return b
}

// TestSyntheticParity_CondSel certifies the conditional-select family that Go
// decodes (base csel/csinc + every cset/csetm/cinc/cinv/cneg alias) against
// the Z80 port.  Covers both the alias-firing operand shapes (Rn==Rm, Rn==Rm==31)
// and the non-alias base shapes, across 32- and 64-bit, and several conditions.
func TestSyntheticParity_CondSel(t *testing.T) {
	lines := []string{
		// Base csel / csinc (Go has forms for these; Rn != Rm so no alias).
		"csel x0, x1, x2, eq", "csel w3, w4, w5, ne", "csel x6, x7, x8, ge",
		"csinc x9, x10, x11, lt", "csinc w12, w13, w14, gt", "csinc x15, x16, x17, le",
		// cset / csetm (csinc/csinv with Rn==Rm==zr, invertable cond).
		"cset x0, eq", "cset w1, ne", "cset x2, cc",
		"csetm x3, hi", "csetm w4, ls", "csetm x5, mi",
		// cinc / cinv / cneg (csinc/csinv/csneg with Rn==Rm != zr).
		"cinc x6, x7, eq", "cinc w8, w9, ne",
		"cinv x10, x11, ge", "cinv w12, w13, lt",
		"cneg x14, x15, gt", "cneg w16, w17, le",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_ExtendedReg certifies extended-register add/sub —
// address arithmetic the i10 report ranked HIGH (e.g. `add x0, x1, w2, uxtw`).
// release.img exercises only the shifted-register base, never the extended
// operand path (dpreg.go:decodeExtendedReg / src/disasm.asm extended-reg region).
func TestSyntheticParity_ExtendedReg(t *testing.T) {
	lines := []string{
		"add x0, x1, w2, uxtb", "add x3, x4, w5, uxth", "add x6, x7, w8, uxtw",
		"add x9, x10, x11, uxtx", "add x12, x13, w14, sxtb", "add x15, x16, w17, sxth",
		"add x18, x19, w20, sxtw", "add x21, x22, x23, sxtx",
		"add x0, x1, w2, uxtw #2", "add x3, x4, x5, sxtx #3",
		"sub x6, x7, w8, uxth #1", "sub x9, x10, w11, sxtb #4",
		"adds x12, x13, w14, uxtw", "adds x15, x16, w17, sxth #2",
		"subs x18, x19, w20, uxtb", "subs x21, x22, x23, sxtx #1",
		// 32-bit (Wd) extended forms.
		"add w0, w1, w2, uxtb", "sub w3, w4, w5, sxth #2", "adds w6, w7, w8, uxtw",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_SignedMul certifies the signed multiply-long / high
// family.  release.img has the *unsigned* longs (umull/umulh/umaddl/umsubl)
// but zero signed ones despite identical decode structure — the i10 report's
// canonical "structurally proven, never run" case (aliases.go:decodeMul3Source).
func TestSyntheticParity_SignedMul(t *testing.T) {
	lines := []string{
		"smull x0, w1, w2", "smull x3, w4, w5",
		"smaddl x6, w7, w8, x9", "smaddl x10, w11, w12, x13",
		"smsubl x14, w15, w16, x17", "smsubl x18, w19, w20, x21",
		"smnegl x22, w23, w24", "smnegl x25, w26, w27",
		"smulh x0, x1, x2", "smulh x3, x4, x5",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_ExtendedSysName certifies the item-i9 ENCODER fix: the
// PSTATE / DC / TLBI named-table extension (src/sysreg_tables.inc, now at full
// parity with the Go authority tools/sam-aarch64-format/sysregs.go for those
// three families).  Before the fix the SAM assembler `jp fail`ed (hard halt)
// on any pstate/dc/tlbi name outside the M5/M6 subset — names Go encodes
// correctly (Go's ParsePState/ParseDC/ParseTLBI have NO generic fallback, so
// "extend the subset" is the faithful fix; see sysname.asm lookup-miss paths).
//
// This drives a fixture of previously-failing names through the REAL prod SAM
// assembler (build/assembler-prod.bin, the same one M6 byte-matches) and
// asserts byte-identical output to GNU `as`.  It exercises the fixed encoder
// path end-to-end, not just the table bytes (the sync guard
// tools/sam-aarch64-format/sysregs_z80sync_test.go already checks the bytes).
func TestSyntheticParity_ExtendedSysName(t *testing.T) {
	// Names absent from the pre-i9 SAM subset but present in the Go tables.
	// Each line is valid for the base aarch64 target (cvap/cvadp need a newer
	// -march so are omitted here; the byte-level table parity is guarded
	// separately by the sysreg sync test).
	lines := []string{
		"msr spsel, #1", "msr pan, #1", "msr uao, #0",
		"msr dit, #1", "msr tco, #1", "msr ssbs, #1",
		"dc zva, x0", "dc isw, x1", "dc csw, x2", "dc cisw, x3", "dc cvau, x4",
		"tlbi alle1", "tlbi alle2", "tlbi alle3is", "tlbi vmalls12e1",
		"tlbi vae1, x0", "tlbi aside1is, x1", "tlbi ipas2e1is, x2", "tlbi vale3, x3",
	}
	gnu := gnuRawBytes(t, lines)

	root := repoRoot(t)
	enc, err := os.ReadFile(filepath.Join(root, "build", "enctab.enc"))
	if err != nil {
		t.Skipf("build/enctab.enc not built: %v", err)
	}
	asm, err := os.ReadFile(filepath.Join(root, "build", "assembler-prod.bin"))
	if err != nil {
		t.Skipf("build/assembler-prod.bin not built: %v", err)
	}
	sd13, err := os.ReadFile(filepath.Join(root, "build", "sysreg_data.bin"))
	if err != nil {
		t.Skipf("build/sysreg_data.bin not built (the page-13 sysname tables): %v", err)
	}
	// The prod assembler always HLOADs sd13 (page 13) and d15 (page 15) at
	// boot; serve both so the HGTHD requests resolve.
	d15, _ := os.ReadFile(filepath.Join(root, "build", "disasm.bin"))

	text2bin := filepath.Join(root, "build", "text2bin")
	if _, e := os.Stat(text2bin); e != nil {
		t.Skipf("build/text2bin not built: %v", e)
	}
	tmp := t.TempDir()
	srcPath := filepath.Join(tmp, "sysn.s")
	var body []byte
	for _, l := range lines {
		body = append(body, []byte(l+"\n")...)
	}
	if err := os.WriteFile(srcPath, body, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	tbnPath := filepath.Join(tmp, "sysn.tbn")
	if out, e := exec.Command(text2bin, "-o", tbnPath, srcPath).CombinedOutput(); e != nil {
		t.Fatalf("text2bin failed on the extended sysname fixture:\n%s", out)
	}
	tbn, _ := os.ReadFile(tbnPath)

	res := RunWithFiles(asm, enc, tbn, []NamedFile{
		{Name: "sd13", Content: sd13, TargetPage: 13},
		{Name: "d15", Content: d15, TargetPage: 15},
	}, 15*time.Second)

	if !res.Passed {
		t.Fatalf("SAM assembler did not reach a clean assemble on the extended "+
			"sysname fixture (a previously-failing pstate/dc/tlbi name still "+
			"`jp fail`s?): printer=%q exit=%q", res.PrinterCapture, res.ExitReason)
	}
	if !bytes.Equal(res.OutBytes, gnu) {
		t.Fatalf("extended sysname encode mismatch vs GNU:\n  gnu=% x\n  sam=% x", gnu, res.OutBytes)
	}
	t.Logf("extended sysname encode: %d bytes byte-match GNU (%d names certified)", len(gnu), len(lines))
}

// gnuRawBytes assembles lines with GNU `as` and returns the flat binary bytes
// (not split into words).  Skips if no GNU toolchain.
func gnuRawBytes(t *testing.T, lines []string) []byte {
	t.Helper()
	as, ld, objcopy := "aarch64-none-elf-as", "aarch64-none-elf-ld", "aarch64-none-elf-objcopy"
	if _, e := exec.LookPath(as); e != nil {
		as, ld, objcopy = "aarch64-linux-gnu-as", "aarch64-linux-gnu-ld", "aarch64-linux-gnu-objcopy"
		if _, e2 := exec.LookPath(as); e2 != nil {
			t.Skip("no GNU aarch64 assembler on PATH")
		}
	}
	tmp := t.TempDir()
	src := tmp + "/g.s"
	var body []byte
	for _, l := range lines {
		body = append(body, []byte(l+"\n")...)
	}
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatalf("write GNU source: %v", err)
	}
	o, elf, bin := tmp+"/g.o", tmp+"/g.elf", tmp+"/g.bin"
	if out, e := exec.Command(as, src, "-o", o).CombinedOutput(); e != nil {
		t.Fatalf("GNU as failed:\n%s", out)
	}
	if out, e := exec.Command(ld, "-Ttext=0", "-o", elf, o).CombinedOutput(); e != nil {
		t.Fatalf("GNU ld failed:\n%s", out)
	}
	if out, e := exec.Command(objcopy, "-O", "binary", elf, bin).CombinedOutput(); e != nil {
		t.Fatalf("GNU objcopy failed:\n%s", out)
	}
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("read GNU binary: %v", err)
	}
	return raw
}

// TestSyntheticParity_KnownDisagreements documents the genuine Z80↔Go
// DECODE disagreements this sweep surfaced.  These are NOT worked around: the
// test is SKIPPED by default with a pointer to the report so they cannot be
// silently forgotten, and (when run with PARITY_DISAGREEMENTS=1) it asserts
// the *current* divergent behaviour so a future fix that resolves a row trips
// the test and prompts moving that family up into the certified sweeps above.
//
// The two disagreement classes (full analysis in
// docs/notes/2026-06-08-z80-go-disasm-parity-i9.md):
//
//  1. ccmp / ccmn (conditional compare): Go decodes (ccmp has a form;
//     binutils agrees), but src/disasm.asm has NO ccmp/ccmn handler, so the
//     Z80 emits ".inst".  A Z80 *decoder* port gap (the *encoder* already has
//     full parity — the SAM assembler byte-matches GNU on ccmp).
//
//  2. non-alias base csinv / csneg (Rn != Rm): src/disasm.asm decodes the
//     base csinv/csneg forms, but Go's DecodeAt declines them (no csinv/csneg
//     form in aarch64enc.AllForms(); decodeCondSelAlias only fires for the
//     Rn==Rm alias shapes), so Go emits ".inst" while the Z80 emits the base
//     mnemonic.  Here the Z80 is MORE capable than the Go authority — which
//     itself is less capable than binutils.  Resolution is a Go-authority
//     decision (extend Go's decoder to match binutils, then the Z80 already
//     agrees), so it is reported, not changed in this PR.
func TestSyntheticParity_KnownDisagreements(t *testing.T) {
	if os.Getenv("PARITY_DISAGREEMENTS") != "1" {
		t.Skip("known Z80↔Go disasm disagreements (ccmp/ccmn decode, base csinv/csneg decode) — " +
			"see docs/notes/2026-06-08-z80-go-disasm-parity-i9.md; set PARITY_DISAGREEMENTS=1 to assert them")
	}
	disasmBin := loadProdDisasm(t)

	// (word, gnu-text, expected-Go-mnem, expected-Z80-mnem) for each
	// documented divergence.  Asserting the *current* split state means a
	// future fix that closes a gap will fail here, flagging the family for
	// promotion into a certified sweep.
	type diverge struct {
		word            uint32
		text            string
		goMnem, z80Mnem string
	}
	cases := []diverge{
		{0xfa410002, "ccmp x0, x1, #2, eq", "ccmp", ".inst"},    // Go decodes; Z80 has no handler
		{0xfa4318a4, "ccmp x5, #3, #4, ne", "ccmp", ".inst"},    // immediate form, same gap
		{0x3a441065, "ccmn w3, w4, #5, ne", ".inst", ".inst"},   // ccmn unimplemented BOTH sides → agree
		{0xda87b0c5, "csinv x5, x6, x7, lt", ".inst", "csinv"},  // Z80 decodes base; Go declines
		{0xda8ac528, "csneg x8, x9, x10, gt", ".inst", "csneg"}, // Z80 decodes base; Go declines
	}
	for _, c := range cases {
		zm, _, err := runZ80Disasm(disasmBin, c.word, 0)
		if err != nil {
			t.Fatalf("[%s] Z80 run failed: %v", c.text, err)
		}
		gm, _ := goOracle(0, c.word)
		if gm != c.goMnem {
			t.Errorf("[%s] Go mnemonic changed: got %q want documented %q — re-triage this row", c.text, gm, c.goMnem)
		}
		if zm != c.z80Mnem {
			t.Errorf("[%s] Z80 mnemonic changed: got %q want documented %q — a fix may have landed; "+
				"promote this family into a certified sweep", c.text, zm, c.z80Mnem)
		}
	}
}
