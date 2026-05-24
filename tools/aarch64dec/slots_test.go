package aarch64dec

import (
	"testing"

	"github.com/petemoore/sam-aarch64/tools/aarch64enc"
)

// Each test in this file was cross-checked against
//
//	aarch64-elf-objdump -D -b binary -m aarch64 <bytes>
//
// before being committed.  Where the raw decoder text differs from
// objdump because of alias renaming (e.g. movz → mov, ubfm → ubfx/
// lsl/lsr) the test asserts the form-walk's choice with a comment
// pointing at Task 3 (alias recognition).

// ----------------------------------------------------------------
// Imm16Shifted: movz / movk / movn family (rendered as the `mov`
// alias because the MRA-derived form table maps the MOVZ pattern to
// the `mov` mnemonic — matches objdump).
//
// Plan-vs-reality discovery: the manual_forms.go masks for movk
// (`0xffe00000`) and movn (`0xffe00000`) include bit 21 (the low
// bit of the hw field), so movk/movn through the form-table walk
// only succeed for hw=0.  This is a real form-table asymmetry —
// encoder-side it's harmless because encodeImm16Shifted OR-s hw
// bits in directly without re-validating the mask, but decode-
// side it means full-Decode tests for movk-with-shift can't pass.
// Movz works for all hw values (its form masks leave hw free), and
// the AllForms ordering picks the MRA-derived `mov`-as-MOVZ entry
// before the dedicated `movz` Form, so 0xd2a02469 etc. decode as
// `mov` — matching objdump.
// ----------------------------------------------------------------

func TestDecode_Imm16Shifted_NoShift(t *testing.T) {
	// `mov x9, #0x123` — MOVZ X9, #0x123 (hw=00).  Word 0xd2802469.
	// objdump: `mov\tx9, #0x123` (after applying the MOVZ→mov alias).
	mnem, ops, ok := Decode(0xd2802469)
	if !ok {
		t.Fatalf("Decode(0xd2802469) not ok")
	}
	if mnem != "mov" || ops != "x9, #0x123" {
		t.Errorf("Decode(0xd2802469): got mnem=%q ops=%q; want mnem=\"mov\" ops=\"x9, #0x123\"", mnem, ops)
	}
}

func TestDecode_Imm16Shifted_LSL16(t *testing.T) {
	// MOVZ X9, #0x123, lsl #16 — hw=01.  Word 0xd2a02469.  objdump
	// pre-computes the shifted constant and emits `mov x9,
	// #0x1230000`; our raw decoder preserves the `#0x123, lsl #16`
	// form because that's what the slot produces.  Pre-computing
	// the constant for the MOV alias is alias-rewrite work and
	// belongs in Task 3.
	mnem, ops, ok := Decode(0xd2a02469)
	if !ok {
		t.Fatalf("Decode(0xd2a02469) not ok")
	}
	want := "x9, #0x123, lsl #16"
	if mnem != "mov" || ops != want {
		t.Errorf("Decode(0xd2a02469): got mnem=%q ops=%q; want mnem=\"mov\" ops=%q (objdump emits the pre-shifted hex; see comment)", mnem, ops, want)
	}
}

func TestDecode_Imm16Shifted_LSL32and48(t *testing.T) {
	// hw=10 → `, lsl #32`.  Word 0xd2c02469.
	// hw=11 → `, lsl #48`.  Word 0xd2e02469.
	for _, tc := range []struct {
		word uint32
		want string
	}{
		{0xd2c02469, "x9, #0x123, lsl #32"},
		{0xd2e02469, "x9, #0x123, lsl #48"},
	} {
		mnem, ops, ok := Decode(tc.word)
		if !ok || mnem != "mov" || ops != tc.want {
			t.Errorf("Decode(0x%08x): got mnem=%q ops=%q; want mnem=\"mov\" ops=%q", tc.word, mnem, ops, tc.want)
		}
	}
}

// ----------------------------------------------------------------
// ShiftAmount: unsigned N-bit immediate inside a shifted-reg
// operand.  No Form uses ShiftAmount yet — exercise the slot
// decoder directly.
// ----------------------------------------------------------------

func TestDecodeSlot_ShiftAmount(t *testing.T) {
	// `add x0, x1, x1, lsr #35` encodes as 0x8b418c20:
	//   bits[15:10] = 0x23 = 35.
	// The ShiftAmount slot for this form has BitPosition=10,
	// BitWidth=6.  We render `#35`.  Verified with:
	//
	//   echo -ne '\x20\x8c\x41\x8b' > /tmp/t.bin && \
	//     aarch64-elf-objdump -D -b binary -m aarch64 /tmp/t.bin
	//   → 0:  8b418c20  add  x0, x1, x1, lsr #35
	slot := aarch64enc.OperandSlot{
		SlotKind:    aarch64enc.ShiftAmount,
		BitPosition: 10,
		BitWidth:    6,
	}
	got, ok := decodeSlot(decodeCtx{word: 0x8b418c20}, slot)
	if !ok || got != "#35" {
		t.Errorf("ShiftAmount(0x8b418c20 @ bit10:6): got (%q, %v); want (\"#35\", true)", got, ok)
	}
}

// ----------------------------------------------------------------
// ExtendOp: (option:3, imm3:3) extend-kind + shift amount.  No Form
// uses ExtendOp yet — exercise the slot decoder directly.
// ----------------------------------------------------------------

func TestDecodeSlot_ExtendOp_NoShift(t *testing.T) {
	// option=2 (uxtw), imm3=0 → "uxtw".  Verified against the
	// extend-register field rendering used by `add x0, x1, w2,
	// uxtw` (objdump prints `uxtw` with no `#N` when imm3=0).
	word := uint32(2) << 13 // option=uxtw at bit 13, imm3=0 at bit 10
	slot := aarch64enc.OperandSlot{
		SlotKind:    aarch64enc.ExtendOp,
		BitPosition: 10,
		BitWidth:    6,
	}
	got, ok := decodeSlot(decodeCtx{word: word}, slot)
	if !ok || got != "uxtw" {
		t.Errorf("ExtendOp(uxtw, 0): got (%q, %v); want (\"uxtw\", true)", got, ok)
	}
}

func TestDecodeSlot_ExtendOp_WithShift(t *testing.T) {
	// option=6 (sxtw), imm3=2 → "sxtw #2".  Verified against
	// `add x0, x1, w2, sxtw #2` (objdump: `sxtw #2`).
	word := uint32(6)<<13 | uint32(2)<<10
	slot := aarch64enc.OperandSlot{
		SlotKind:    aarch64enc.ExtendOp,
		BitPosition: 10,
		BitWidth:    6,
	}
	got, ok := decodeSlot(decodeCtx{word: word}, slot)
	if !ok || got != "sxtw #2" {
		t.Errorf("ExtendOp(sxtw, 2): got (%q, %v); want (\"sxtw #2\", true)", got, ok)
	}
}

// ----------------------------------------------------------------
// Imm5 / Imm6: trivial N-bit unsigned immediates.  Imm5 is
// exercised by ccmp.
// ----------------------------------------------------------------

func TestDecode_Imm5_CCMP(t *testing.T) {
	// `ccmp x0, #5, #3, eq` — base pattern 0xfa400800, with
	// imm5=5 at bit 16, nzcv=3 at bit 0, cond=eq (0) at bit 12.
	// Word = 0xfa450803.  Verified:
	//
	//   echo -ne '\x03\x08\x45\xfa' > /tmp/t.bin && \
	//     aarch64-elf-objdump -D -b binary -m aarch64 /tmp/t.bin
	//   → 0:  fa450803  ccmp  x0, #0x5, #0x3, eq
	//
	// objdump emits `#0x5`/`#0x3` (hex) for ccmp's immediates; our
	// Imm5 decoder is generic across the 4-bit nzcv and 5-bit imm5
	// usages and emits `#<decimal>`.  Known formatting divergence
	// (`#5` vs `#0x5`); reconcile in a follow-up if/when we add a
	// per-Form hint for hex vs decimal rendering.
	mnem, ops, ok := Decode(0xfa450803)
	if !ok {
		t.Fatalf("Decode(0xfa450803) not ok")
	}
	want := "x0, #5, #3, eq"
	if mnem != "ccmp" || ops != want {
		t.Errorf("Decode(0xfa450803): got mnem=%q ops=%q; want mnem=\"ccmp\" ops=%q", mnem, ops, want)
	}
}

func TestDecodeSlot_Imm6(t *testing.T) {
	// Imm6 has no Form using it; exercise directly.  6-bit field at
	// BitPosition=10 with value 63 → "#63".
	slot := aarch64enc.OperandSlot{
		SlotKind:    aarch64enc.Imm6,
		BitPosition: 10,
		BitWidth:    6,
	}
	word := uint32(63) << 10
	got, ok := decodeSlot(decodeCtx{word: word}, slot)
	if !ok || got != "#63" {
		t.Errorf("Imm6(63): got (%q, %v); want (\"#63\", true)", got, ok)
	}
}

// ----------------------------------------------------------------
// Branch + PC-relative kinds: BranchImm14/19/26, AdrpImm, AdrImm.
// These render absolute target addresses (`0x<hex>`) computed from
// the current PC + the signed offset encoded in the instruction.
// ----------------------------------------------------------------

func TestDecode_BranchImm26_B_Positive(t *testing.T) {
	// 0x14000004 = `b 0x10` at pc=0 (instr-offset=+4 → byte +16).
	// objdump output (pc=0): `0:  14000004  b  0x10`.
	mnem, ops, ok := Decode(0x14000004)
	if !ok || mnem != "b" || ops != "0x10" {
		t.Errorf("Decode(0x14000004): got mnem=%q ops=%q; want mnem=\"b\" ops=\"0x10\"", mnem, ops)
	}
}

func TestDecode_BranchImm26_B_NegativeAtPC(t *testing.T) {
	// 0x17fffffe = `b 0x14` when pc=0x1c.  Instruction offset = -2
	// (sign-extended 26 bits) → byte -8 → target 0x14.
	mnem, ops, ok := DecodeAt(0x1c, 0x17fffffe)
	if !ok || mnem != "b" || ops != "0x14" {
		t.Errorf("DecodeAt(0x1c, 0x17fffffe): got mnem=%q ops=%q; want mnem=\"b\" ops=\"0x14\"", mnem, ops)
	}
}

func TestDecode_BranchImm26_BL_NegativeFromZero(t *testing.T) {
	// 0x97fffff1: bits[25:0] = 0x3fffff1 = -15 (sign-extended
	// 26 bits) → byte offset -60 (-0x3c).  At pc=0, target is
	// 0 - 0x3c = 0xffffffffffffffc4 (wraps in uint64 — exactly
	// what objdump prints).  Verified by running `objdump -d -b
	// binary -m aarch64` on the 4 bytes of 0x97fffff1.
	mnem, ops, ok := Decode(0x97fffff1)
	if !ok || mnem != "bl" || ops != "0xffffffffffffffc4" {
		t.Errorf("Decode(0x97fffff1): got mnem=%q ops=%q; want mnem=\"bl\" ops=\"0xffffffffffffffc4\"", mnem, ops)
	}
}

func TestDecode_BranchImm19_CBZ(t *testing.T) {
	// 0xb40000a0 = `cbz x0, 0x14` at pc=0 (instr-offset=+5 → byte +20).
	mnem, ops, ok := Decode(0xb40000a0)
	if !ok || mnem != "cbz" || ops != "x0, 0x14" {
		t.Errorf("Decode(0xb40000a0): got mnem=%q ops=%q; want mnem=\"cbz\" ops=\"x0, 0x14\"", mnem, ops)
	}
}

func TestDecode_BranchImm19_BEQ(t *testing.T) {
	// 0x54000060 = `b.eq 0xc` at pc=0 (instr-offset=+3 → byte +12).
	// objdump also appends `// b.none` as a cosmetic alias comment;
	// the trailing comment is *not* part of the operand text.
	mnem, ops, ok := Decode(0x54000060)
	if !ok || mnem != "b.eq" || ops != "0xc" {
		t.Errorf("Decode(0x54000060): got mnem=%q ops=%q; want mnem=\"b.eq\" ops=\"0xc\"", mnem, ops)
	}
}

func TestDecodeSlot_BranchImm14(t *testing.T) {
	// No Form uses BranchImm14 yet (tbz/tbnz are not in the table).
	// Exercise the slot decoder directly.
	//
	// tbz w0, #4, 0x10 at pc=0:
	//   word = 0x36200080.  bits[18:5] (imm14) = 4 → instr-offset
	//   +4 → byte +16 → target 0x10 at pc=0.
	//   Verified against objdump on the 4 bytes of 0x36200080.
	slot := aarch64enc.OperandSlot{
		SlotKind:    aarch64enc.BranchImm14,
		BitPosition: 5,
		BitWidth:    14,
	}
	ctx := decodeCtx{word: 0x36200080, pc: 0}
	got := decodeBranchImm(ctx, slot)
	if got != "0x10" {
		t.Errorf("decodeBranchImm tbz imm14: got %q, want %q", got, "0x10")
	}
}

func TestDecode_AdrpImm_PositivePage(t *testing.T) {
	// 0xb0000000 at pc=0 → `adrp x0, 0x1000`.
	// Verified against objdump on the 4 bytes of 0xb0000000.
	mnem, ops, ok := Decode(0xb0000000)
	if !ok || mnem != "adrp" || ops != "x0, 0x1000" {
		t.Errorf("Decode(0xb0000000): got mnem=%q ops=%q; want mnem=\"adrp\" ops=\"x0, 0x1000\"", mnem, ops)
	}
}

func TestDecode_AdrpImm_NegativePage(t *testing.T) {
	// 0xb0ffffe0 at pc=0 → `adrp x0, 0xffffffffffffd000`.
	// imm21 = -3 → -0x3000 byte offset.
	// Verified against objdump on the 4 bytes of 0xb0ffffe0.
	mnem, ops, ok := Decode(0xb0ffffe0)
	if !ok || mnem != "adrp" || ops != "x0, 0xffffffffffffd000" {
		t.Errorf("Decode(0xb0ffffe0): got mnem=%q ops=%q; want mnem=\"adrp\" ops=\"x0, 0xffffffffffffd000\"", mnem, ops)
	}
}

func TestDecode_AdrpImm_PageMaskingPC(t *testing.T) {
	// At pc=0x1234, PC & ~0xFFF = 0x1000, plus the same +0x1000
	// offset from 0xb0000000 → 0x2000.  Exercises that DecodeAt's
	// pc threads through to AdrpImm and that ADRP page-masks the
	// PC before adding the offset (per ARM ARM C6.2.10).
	mnem, ops, ok := DecodeAt(0x1234, 0xb0000000)
	if !ok || mnem != "adrp" || ops != "x0, 0x2000" {
		t.Errorf("DecodeAt(0x1234, 0xb0000000): got mnem=%q ops=%q; want mnem=\"adrp\" ops=\"x0, 0x2000\"", mnem, ops)
	}
}

func TestDecodeSlot_AdrImm(t *testing.T) {
	// No Form uses AdrImm yet (the generated form for adrp uses
	// AdrpImm).  Exercise the slot decoder directly.
	//
	// 0x10000020 = `adr x0, 0x4` at pc=0 — imm21=+1, byte +4.
	ctx := decodeCtx{word: 0x10000020, pc: 0}
	got := decodeAdrImm(ctx)
	if got != "0x4" {
		t.Errorf("decodeAdrImm 0x10000020 pc=0: got %q, want %q", got, "0x4")
	}
	// 0x10001020 = `adr x0, 0x214` at pc=0x10 — imm21=+0x81/4 → byte +0x204.
	ctx = decodeCtx{word: 0x10001020, pc: 0x10}
	got = decodeAdrImm(ctx)
	if got != "0x214" {
		t.Errorf("decodeAdrImm 0x10001020 pc=0x10: got %q, want %q", got, "0x214")
	}
}

// ----------------------------------------------------------------
// LogicalImm: ARM bitmask immediate (N:immr:imms → 32- or 64-bit
// bitmask via ARM ARM C4.1.64 "DecodeBitMasks").  Verified against
// objdump for each test case.
//
//   echo -ne '\x20\x04\x7c\x92' > /tmp/t.bin && \
//     aarch64-elf-objdump -D -b binary -m aarch64 /tmp/t.bin
//   → 0:  927c0420  and  x0, x1, #0x30
//
//   echo -ne '\x20\xec\x40\x92' ...   → and x0, x1, #0xfffffffffffffff
//   echo -ne '\x20\x04\x00\x12' ...   → and w0, w1, #0x3
// ----------------------------------------------------------------

func TestDecode_LogicalImm_X_Small(t *testing.T) {
	// `and x0, x1, #0x30` = 0x927c0420.  N=1, immr=0x3c (= 60), imms=1.
	// DecodeBitMasks: combined N:NOT(imms) = 1_111110 → length = 6.
	// esize=64.  S=imms&63 = 1 → welem = 0b11.  R=immr&63 = 60.
	// Rotate 0b11 right by 60 in 64 bits = 0b11 << 4 = 0x30.
	mnem, ops, ok := Decode(0x927c0420)
	if !ok || mnem != "and" || ops != "x0, x1, #0x30" {
		t.Errorf("Decode(0x927c0420): got mnem=%q ops=%q; want mnem=\"and\" ops=\"x0, x1, #0x30\"", mnem, ops)
	}
}

func TestDecode_LogicalImm_X_Replicated(t *testing.T) {
	// `and x0, x1, #0xfffffffffffffff` = 0x9240ec20.  N=1, immr=0,
	// imms=0x3b (=59).  welem = 0x0FFFFFFFFFFFFFFF (60 ones).
	// Rotate by 0 → unchanged.
	mnem, ops, ok := Decode(0x9240ec20)
	if !ok || mnem != "and" || ops != "x0, x1, #0xfffffffffffffff" {
		t.Errorf("Decode(0x9240ec20): got mnem=%q ops=%q; want mnem=\"and\" ops=\"x0, x1, #0xfffffffffffffff\"", mnem, ops)
	}
}

func TestDecode_LogicalImm_W(t *testing.T) {
	// `and w0, w1, #0x3` = 0x12000420.  sf=0 → regsize=32.
	// N=0 (forced by sf=0), immr=0, imms=1.  Combined N:NOT(imms)
	// = 0_111110 → length = 5 (highest set bit in 6 lower bits).
	// esize = 32, S = 1, welem = 0b11 → rotated = 0b11 → replicated
	// into 32 bits = 0x00000003.
	mnem, ops, ok := Decode(0x12000420)
	if !ok || mnem != "and" || ops != "w0, w1, #0x3" {
		t.Errorf("Decode(0x12000420): got mnem=%q ops=%q; want mnem=\"and\" ops=\"w0, w1, #0x3\"", mnem, ops)
	}
}

// ----------------------------------------------------------------
// BitfieldImm: pair of 6-bit immediates rendered as `#immr, #imms`.
//
// Plan-vs-reality discovery: the manualForms table lists `lsl`
// (ID 17) before `lsr`, `ubfx` (ID 51), `bfxil`, `bfi` etc. —
// ALL share the same Pattern + Mask (`0x53000000`/`0xd3400000` +
// `0xffc00000`/`0xff800000`).  The form-walk picks the first
// matching entry, so every UBFM-family word decodes through the
// `lsl` Form regardless of whether the (immr, imms) combination is
// the lsl-specific pair or some other pattern.  Alias
// disambiguation (picking the right alias name from (sf, immr,
// imms)) is Task 3.
//
// Test the slot decoder directly so we exercise BOTH the immr
// slot (BitPosition=16) and the imms slot (BitPosition=10) without
// being confused by which alias mnemonic the form-walk picks.
// ----------------------------------------------------------------

func TestDecodeSlot_BitfieldImm(t *testing.T) {
	// word 0xd341fc20 → bits[21:16] = immr = 1, bits[15:10] = imms = 0x3f.
	// objdump renders the WHOLE word as `lsr x0, x1, #1` (the lsr
	// alias of UBFM), but the BitfieldImm slot itself just emits
	// each 6-bit value as `#N`.
	word := uint32(0xd341fc20)
	ctx := decodeCtx{word: word}
	immrSlot := aarch64enc.OperandSlot{SlotKind: aarch64enc.BitfieldImm, BitPosition: 16, BitWidth: 6}
	immsSlot := aarch64enc.OperandSlot{SlotKind: aarch64enc.BitfieldImm, BitPosition: 10, BitWidth: 6}
	gotImmr, ok := decodeSlot(ctx, immrSlot)
	if !ok || gotImmr != "#1" {
		t.Errorf("BitfieldImm(immr): got (%q, %v); want (\"#1\", true)", gotImmr, ok)
	}
	gotImms, ok := decodeSlot(ctx, immsSlot)
	if !ok || gotImms != "#63" {
		t.Errorf("BitfieldImm(imms): got (%q, %v); want (\"#63\", true)", gotImms, ok)
	}
}
