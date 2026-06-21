// synthetic_parity_test.go — synthetic-fixture Z80↔Go disassembler parity
// sweep (item i9, "parity robustness seeds").
//
// # What this certifies
//
// The release-corpus oracle (disasm_oracle_test.go::TestDisasmOracle) reaches
// 100% but is *corpus-bounded*: it only ever compares the instruction families
// that actually appear in tests/release/release.img.  The i10 capability
// report (https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-06-08-go-vs-z80-disasm-capability-parity.md) found
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
//   - conditional select base forms: csel, csinc, csinv, csneg
//   - conditional select aliases:    cset, csetm, cinc, cinv, cneg
//   - conditional compare:           ccmp, ccmn (immediate + register forms)
//   - extended-register add/sub:      add/sub/adds/subs Xd, Xn, Wm, {u,s}xt{b,h,w}/sxtx [#sh]
//   - signed multiply long/high:      smull, smaddl, smsubl, smnegl, smulh
//
// `sdiv` is deliberately excluded (known-missing across the whole stack —
// encoder + decoder both lack it; tracked separately as item i35).
//
// The conditional-COMPARE family (ccmp/ccmn) and the *non-alias* base
// csinv/csneg forms were genuine Z80↔Go disagreements surfaced by an earlier
// run of this sweep; items i36 + i37 closed them at source (encoder + both
// decoders + binutils now all agree), so they are certified here rather than
// pinned.  See https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-06-08-z80-go-disasm-parity-i9.md.
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
		t.Fatalf("build/disasm.bin not built (run `make build/disasm.bin`): %v", err)
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
		// Base csinv / csneg (Rn != Rm so no alias).  Added by items i36+i37:
		// Go previously declined these (no form) while the Z80 decoded them —
		// now both decode the base mnemonic, matching binutils.
		"csinv x5, x6, x7, lt", "csinv w0, w1, w2, ne", "csinv x18, x19, x20, ge",
		"csneg x8, x9, x10, gt", "csneg w3, w4, w5, le", "csneg x21, x22, x23, mi",
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

// TestSyntheticParity_CondCmp certifies the conditional-compare family
// (ccmp/ccmn, immediate and register forms, 32- and 64-bit, several
// conditions and nzcv/imm5 values).  Items i36+i37 added ccmn to the encoder
// + Go decoder, ported ccmp/ccmn decode to src/disasm.asm, and aligned the
// Go Imm5 rendering with binutils (`#0xN` hex).  Before that the Z80 emitted
// `.inst` for the whole family and ccmn was unimplemented on every side.
func TestSyntheticParity_CondCmp(t *testing.T) {
	lines := []string{
		// ccmp register form.
		"ccmp x0, x1, #2, eq", "ccmp w3, w4, #0, ne", "ccmp x6, x7, #15, ge",
		// ccmp immediate form.
		"ccmp x5, #3, #4, ne", "ccmp w8, #0, #0, eq", "ccmp x9, #31, #7, lt",
		// ccmn register form.
		"ccmn w3, w4, #5, ne", "ccmn x10, x11, #1, gt", "ccmn w12, w13, #8, le",
		// ccmn immediate form.
		"ccmn w5, #3, #4, eq", "ccmn x14, #17, #2, cc", "ccmn w15, #31, #15, hi",
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
		t.Fatalf("build/enctab.enc not built: %v", err)
	}
	asm, err := os.ReadFile(filepath.Join(root, "build", "assembler-prod.bin"))
	if err != nil {
		t.Fatalf("build/assembler-prod.bin not built: %v", err)
	}
	sd13, err := os.ReadFile(filepath.Join(root, "build", "sysreg_data.bin"))
	if err != nil {
		t.Fatalf("build/sysreg_data.bin not built (the page-13 sysname tables): %v", err)
	}
	// The prod assembler always HLOADs sd13 (page 13) and d15 (page 15) at
	// boot; serve both so the HGTHD requests resolve.
	d15, _ := os.ReadFile(filepath.Join(root, "build", "disasm.bin"))

	sam := filepath.Join(root, "build", "sam-aarch64")
	if _, e := os.Stat(sam); e != nil {
		t.Fatalf("build/sam-aarch64 not built: %v", e)
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
	// Assemble to a binary + compact .tbn (sam-aarch64 --emit-tbn): the SAM
	// assembler consumes the compact v2 .tbn (INSN_RUN decoder).
	tbnPath := filepath.Join(tmp, "sysn.compact.tbn")
	goImgPath := filepath.Join(tmp, "sysn.go.img")
	if out, e := exec.Command(sam, "-o", goImgPath, "-emit-tbn", tbnPath, srcPath).CombinedOutput(); e != nil {
		t.Fatalf("sam-aarch64 failed on the extended sysname fixture:\n%s", out)
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

// TestSyntheticParity_DPRegAliases certifies the data-processing-register
// aliases that release.img exercises only partially: neg/negs (sub/subs with
// Rn=zr), cmn (adds with Rd=zr), plus the logical-shifted-register mnemonics
// that the corpus never hits: eon, orn, bics.
// These all route through decodeDPRegAlias / decodeShiftedReg (dpreg.go).
func TestSyntheticParity_DPRegAliases(t *testing.T) {
	lines := []string{
		// neg (sub with Rn=zr, shifted-reg base).
		"neg x0, x1", "neg w0, w1", "neg x2, x3, lsl #4",
		// negs (subs with Rn=zr).
		"negs x2, x3", "negs w2, w3, lsl #4", "negs x4, x5, lsr #2",
		// cmn (adds with Rd=zr).
		"cmn x4, x5", "cmn x6, x7, lsr #8", "cmn w8, w9",
		// eon (eor with N=1).
		"eon x0, x1, x2", "eon w0, w1, w2, lsl #3", "eon x3, x4, x5, lsr #6",
		// orn (orr with N=1).
		"orn x3, x4, x5", "orn w3, w4, w5", "orn x6, x7, x8, asr #12",
		// bics (ands with N=1).
		"bics x6, x7, x8", "bics w6, w7, w8, lsr #2", "bics x9, x10, x11",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_ExtrRor certifies the EXTR/ROR-immediate family.
// EXTR is absent from AllForms (it's hand-rolled) and absent from
// release.img, so the Z80 handler (aliases.go:tryDecodeExtr) was never
// exercised by the oracle.  When Rm==Rn objdump renders the ROR alias.
func TestSyntheticParity_ExtrRor(t *testing.T) {
	lines := []string{
		// ror Rd, Rn, #imm (EXTR with Rm==Rn).
		"ror x0, x1, #5", "ror w0, w1, #7", "ror x6, x7, #12", "ror w8, w9, #31",
		// extr Rd, Rn, Rm, #imm (Rm!=Rn).
		"extr x0, x1, x2, #5", "extr w3, w4, w5, #10", "extr x6, x7, x8, #63",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_VarShift certifies the variable-shift family (LSLV/
// LSRV/ASRV/RORV), decoded by aliases.go:decodeShiftVarAlias. release.img
// contains only immediate shifts; the variable-register forms are entirely
// absent from the corpus.
func TestSyntheticParity_VarShift(t *testing.T) {
	lines := []string{
		"lsl x0, x1, x2", "lsr x3, x4, x5", "asr x6, x7, x8", "ror x9, x10, x11",
		"lsl w0, w1, w2", "lsr w3, w4, w5", "asr w6, w7, w8", "ror w9, w10, w11",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_BitfieldAliases certifies the bitfield alias forms not
// exercised by release.img: sbfiz / ubfiz (insert-with-zero-fill), asr-imm
// (SBFM with imms==d-1), and the sign/zero-extend shortcuts sxtb/sxth/sxtw/
// uxtb/uxth.  All route through decodeBitfieldAlias (aliases.go:186).
func TestSyntheticParity_BitfieldAliases(t *testing.T) {
	lines := []string{
		// sbfiz (SBFM with imms < immr, Rn!=zr).
		"sbfiz x0, x1, #4, #8", "sbfiz w6, w7, #3, #5", "sbfiz x2, x3, #32, #16",
		// ubfiz (UBFM with imms < immr).
		"ubfiz x2, x3, #12, #16", "ubfiz w8, w9, #7, #12", "ubfiz x4, x5, #20, #4",
		// asr-imm (SBFM with imms==d-1).
		"asr x4, x5, #2", "asr w10, w11, #1", "asr x6, x7, #63", "asr w8, w9, #31",
		// sxtb/sxth/sxtw (SBFM with immr=0, imms=7/15/31).
		"sxtb x0, w1", "sxth x2, w3", "sxtw x4, w5",
		"sxtb w0, w1", "sxth w2, w3",
		// uxtb/uxth (UBFM with immr=0, imms=7/15, 32-bit).
		"uxtb w6, w7", "uxth w8, w9",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_SignedLoads certifies the signed-extend load family and
// the unscaled-offset (ldur/stur) forms that release.img does not exercise:
// ldrsb/ldrsh/ldrsw with unsigned-scaled offset, ldurb/ldurh/ldursh/ldursb/
// ldursw with unscaled signed offset.  These all route through
// mem.go:scalarMnem / sturMnem (decodeScalarMem).
func TestSyntheticParity_SignedLoads(t *testing.T) {
	lines := []string{
		// Unsigned-offset signed loads (mode=01, scaled imm12).
		"ldrsb x0, [x1, #8]", "ldrsb w6, [x7, #4]",
		"ldrsh x2, [x3, #16]", "ldrsh w4, [x5, #2]",
		"ldrsw x4, [x5, #32]",
		// Unscaled-offset forms (mode=00, bits11:10=00, imm9 signed).
		"ldurb w0, [x1, #-4]", "ldurh w2, [x3, #-8]",
		"ldursh w4, [x5, #-12]", "ldursh x6, [x7, #-16]",
		"ldursb w6, [x7, #-4]", "ldursb x8, [x9, #-8]",
		"ldursw x8, [x9, #-20]",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_LdrLiteral certifies the LDR/LDRSW PC-relative literal
// form (decodeLiteralMem in mem.go), distinct from the base+offset form.
// The oracle feeds pc=0 for these so the target address equals the signed
// byte-offset encoded in imm19 (same as the standalone mode GoOracle uses).
func TestSyntheticParity_LdrLiteral(t *testing.T) {
	lines := []string{
		"ldr x0, .+8",    // ldr 64-bit literal
		"ldr w1, .+16",   // ldr 32-bit literal
		"ldrsw x2, .+24", // ldrsw literal
		"ldr x3, .+64",   // larger forward offset
		"ldr w4, .+4",    // minimal offset
	}
	words := gnuAssembleWords(t, lines)
	// For literal loads the pc matters (the target is pc+offset).  The
	// assembled words are at offsets 0,4,8,12,16 (base 0); pass each pc.
	disasmBin := loadProdDisasm(t)
	for i, w := range words {
		pc := uint64(i * 4)
		zm, zo, err := runZ80Disasm(disasmBin, w, pc)
		if err != nil {
			t.Fatalf("[%s] Z80 run failed (word=%#08x pc=%#x): %v", lines[i], w, pc, err)
		}
		gm, go_ := goOracle(pc, w)
		if zm != gm || zo != go_ {
			t.Errorf("PARITY MISMATCH on %q (word=%#08x pc=%#x):\n  Go : [%s|%s]\n  Z80: [%s|%s]",
				lines[i], w, pc, gm, go_, zm, zo)
		}
	}
}

// TestSyntheticParity_RegOffsetMem certifies the register-offset
// load/store family ([Rn, Rm, ext #amt]), decoded by decodeRegOffset
// (mem.go:352).  release.img never exercises this path — it uses only
// the immediate-offset and unscaled (stur/ldur) forms.
func TestSyntheticParity_RegOffsetMem(t *testing.T) {
	lines := []string{
		// LSL option (Xm, S=0 / S=1).
		"ldr x0, [x1, x2]",
		"ldr x0, [x1, x2, lsl #3]",
		"str x0, [x1, x2, lsl #3]",
		// UXTW (Wm extend, S=0 / S=1).
		"ldr w0, [x1, w2, uxtw]",
		"ldr x3, [x4, w5, uxtw #3]",
		// SXTW (Wm extend, S=0 / S=1).
		"ldr x0, [x1, w2, sxtw]",
		"ldr x0, [x1, w2, sxtw #3]",
		// SXTX (Xm extend).
		"ldr x0, [x1, x2, sxtx]",
		"str x3, [x4, x5, sxtx #3]",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_MsrPstate certifies the MSR-immediate PSTATE form
// (decodeMsrImm in sys.go): msr <pstatefield>, #<imm>.  release.img only
// exercises the register form (msr <sysreg>, Xt); the immediate PSTATE
// variants (daifset/daifclr/spsel/pan/uao/…) never appear.
func TestSyntheticParity_MsrPstate(t *testing.T) {
	lines := []string{
		"msr daifset, #3", "msr daifset, #0xf",
		"msr daifclr, #2", "msr daifclr, #0",
		"msr spsel, #1", "msr spsel, #0",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_MovBitmaskImm certifies the mov-via-bitmask-immediate
// alias (decodeLogicalImmAlias in aliases.go): ORR Rd, zr, #bitmask where
// the immediate is NOT movz/movn-encodable (MoveWidePreferred==false).
// release.img exercises `mov` only via movz; this path (the bitmask ORR
// alias) is entirely absent from the corpus.
func TestSyntheticParity_MovBitmaskImm(t *testing.T) {
	lines := []string{
		"mov x0, #0xcccccccccccccccc", // alternating 8-bit pattern
		"mov x1, #0xffffffff",         // 32-bit all-ones in 64-bit reg
		"mov w2, #0x5555",             // 16-bit alternating
		"mov x3, #0xaaaaaaaaaaaaaaaa", // another repeating pattern
		"mov w4, #0xff00ff00",         // repeating byte pattern
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_MovzMovnKept certifies the "kept base form" cases of
// movz and movn (decodeMovWideAlias in aliases.go): specifically movz with
// imm16=0 and hw!=0, and movn with imm16=0 and hw!=0 or sf=0 and imm16=0xffff.
// These are the words for which objdump retains the movz/movn mnemonic rather
// than collapsing to mov.  Also certifies movk with non-zero hw (decodeMovk)
// which the encoder's loose Form mask misses.
func TestSyntheticParity_MovzMovnKept(t *testing.T) {
	lines := []string{
		// movz kept: imm16=0, hw!=0.
		"movz x0, #0, lsl #16", "movz w1, #0, lsl #16",
		"movz x2, #0, lsl #32", "movz x3, #0, lsl #48",
		// movn kept: imm16=0, hw!=0 (non-alias zone).
		"movn x2, #0, lsl #16", "movn x4, #0, lsl #32",
		// movn kept: sf=0, imm16=0xffff (32-bit movn with max imm).
		"movn w3, #0xffff",
		// movk with non-zero hw (the Form-table gap decodeMovk fills).
		"movk x0, #0x5, lsl #16", "movk w1, #0x6, lsl #16",
		"movk x2, #0xabcd, lsl #32", "movk x3, #0x1, lsl #48",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_Mneg certifies the mneg alias (madd/msub with Ra=xzr,
// o0=1), decoded by decodeMul3Source (aliases.go).  release.img exercises
// mul/madd/msub/umull etc. but never mneg.
func TestSyntheticParity_Mneg(t *testing.T) {
	lines := []string{
		"mneg x0, x1, x2", "mneg w3, w4, w5",
		"mneg x6, x7, x8", "mneg w9, w10, w11",
	}
	words := gnuAssembleWords(t, lines)
	assertZ80MatchesGo(t, loadProdDisasm(t), lines, words)
}

// TestSyntheticParity_KnownDisagreementsResolved re-asserts, as a regression
// guard, that the two former Z80↔Go disagreements this sweep once pinned are
// now fully resolved (items i36 + i37): every word decodes to the same
// (mnemonic, operands) on the Go authority AND the Z80 port, matching
// binutils.  The families are exercised more broadly by
// TestSyntheticParity_CondCmp and TestSyntheticParity_CondSel above; this is
// a focused guard on the exact words from the original report.
func TestSyntheticParity_KnownDisagreementsResolved(t *testing.T) {
	disasmBin := loadProdDisasm(t)
	type fixed struct {
		word uint32
		want string // canonical objdump operands (mnem in wantMnem)
		mnem string
	}
	cases := []fixed{
		{0xfa410002, "x0, x1, #0x2, eq", "ccmp"},
		{0xfa4318a4, "x5, #0x3, #0x4, ne", "ccmp"},
		{0x3a441065, "w3, w4, #0x5, ne", "ccmn"},
		{0xda87b0c5, "x5, x6, x7, lt", "csinv"},
		{0xda8ac528, "x8, x9, x10, gt", "csneg"},
	}
	for _, c := range cases {
		zm, zo, err := runZ80Disasm(disasmBin, c.word, 0)
		if err != nil {
			t.Fatalf("[%#08x] Z80 run failed: %v", c.word, err)
		}
		gm, go_ := goOracle(0, c.word)
		if gm != c.mnem || go_ != c.want {
			t.Errorf("[%#08x] Go decode: got [%s|%s] want [%s|%s]", c.word, gm, go_, c.mnem, c.want)
		}
		if zm != c.mnem || zo != c.want {
			t.Errorf("[%#08x] Z80 decode: got [%s|%s] want [%s|%s]", c.word, zm, zo, c.mnem, c.want)
		}
	}
}
