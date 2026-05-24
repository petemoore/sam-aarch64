package aarch64dec

import "testing"

// aliasCase is one (word → expected mnemonic + operands) row.  Every
// expected value was produced by aarch64-none-elf-objdump on the word
// (objdump is the oracle the disassembler matches); the byte for byte
// confirmation is captured in the per-group comments.
type aliasCase struct {
	name     string
	word     uint32
	mnem     string
	operands string
}

func runAliasCases(t *testing.T, cases []aliasCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mnem, ops, ok := DecodeAt(0, tc.word)
			if !ok {
				t.Fatalf("DecodeAt(0x%08x): ok=false, want %q %q", tc.word, tc.mnem, tc.operands)
			}
			if mnem != tc.mnem || ops != tc.operands {
				t.Errorf("DecodeAt(0x%08x) = %q %q, want %q %q", tc.word, mnem, ops, tc.mnem, tc.operands)
			}
		})
	}
}

// TestAlias_MoveWide covers movz/movn → mov #<full-imm>, plus the cases
// where objdump keeps the base movz/movn.
func TestAlias_MoveWide(t *testing.T) {
	runAliasCases(t, []aliasCase{
		// movz → mov with the pre-shifted full immediate.
		{"movz_x0_8000_lsl16", 0xd2b00000, "mov", "x0, #0x80000000"},
		{"movz_x2_6_lsl32", 0xd2c000c2, "mov", "x2, #0x600000000"},
		{"movz_x9_123", 0xd2802469, "mov", "x9, #0x123"},
		{"movz_x9_123_lsl16", 0xd2a02469, "mov", "x9, #0x1230000"},
		{"movz_w0_8000_lsl16", 0x52b00000, "mov", "w0, #0x80000000"},
		// movn → mov with the inverted (width-masked) immediate.
		{"movn_w1_0", 0x12800001, "mov", "w1, #0xffffffff"},
		{"movn_x1_0", 0x92800001, "mov", "x1, #0xffffffffffffffff"},
		{"movn_x1_8000_lsl16", 0x92b00001, "mov", "x1, #0xffffffff7fffffff"},
		// movz/movn kept (alias not preferred).
		{"movz_x0_0_lsl16_kept", 0xd2a00000, "movz", "x0, #0x0, lsl #16"},
		{"movn_w1_0_lsl16_kept", 0x12a00001, "movn", "w1, #0x0, lsl #16"},
		{"movn_w1_ffff_kept", 0x129fffe1, "movn", "w1, #0xffff"},
	})
}

// TestAlias_Movk exercises movk with non-zero hw (the encoder Form misses
// these; the dedicated decoder renders them).
func TestAlias_Movk(t *testing.T) {
	runAliasCases(t, []aliasCase{
		{"movk_w0_ffe0_lsl16", 0x72bffc00, "movk", "w0, #0xffe0, lsl #16"},
		{"movk_x13_12f_lsl48", 0xf2e025ed, "movk", "x13, #0x12f, lsl #48"},
		{"movk_w0_cc_lsl16", 0x72a01980, "movk", "w0, #0xcc, lsl #16"},
	})
}

// TestAlias_Bitfield covers UBFM/SBFM/BFM → their canonical aliases,
// including the bfi lsb/width fix (b35c6d89) and the uxt/sxt sub-aliases.
func TestAlias_Bitfield(t *testing.T) {
	runAliasCases(t, []aliasCase{
		// UBFM
		{"ubfm_lsr_x", 0xd35efc62, "lsr", "x2, x3, #30"},
		{"ubfm_lsr_x36", 0xd364fc0c, "lsr", "x12, x0, #36"},
		{"ubfm_lsl_x32", 0xd3607c42, "lsl", "x2, x2, #32"},
		{"ubfm_lsl_w1", 0x531f7821, "lsl", "w1, w1, #1"},
		{"ubfm_ubfx_x", 0xd34c3e53, "ubfx", "x19, x18, #12, #4"},
		{"ubfm_ubfiz_x", 0xd3647c62, "ubfiz", "x2, x3, #28, #32"},
		{"ubfm_uxtb_w", 0x53001c62, "uxtb", "w2, w3"},
		{"ubfm_uxth_w", 0x53003c62, "uxth", "w2, w3"},
		// SBFM
		{"sbfm_asr_x", 0x9340fc62, "asr", "x2, x3, #0"},
		{"sbfm_sxtb_x", 0x93401c62, "sxtb", "x2, w3"},
		{"sbfm_sxth_w", 0x13003c62, "sxth", "w2, w3"},
		{"sbfm_sxtw_x", 0x93407c62, "sxtw", "x2, w3"},
		{"sbfm_sbfx_x", 0x93441c62, "sbfx", "x2, x3, #4, #4"},
		{"sbfm_sbfiz_x", 0x935e1c62, "sbfiz", "x2, x3, #34, #8"},
		// BFM
		{"bfm_bfi_x36_28", 0xb35c6d89, "bfi", "x9, x12, #36, #28"}, // the documented bug
		{"bfm_bfi_x32_32", 0xb3607c40, "bfi", "x0, x2, #32, #32"},
		{"bfm_bfi_w10_9", 0x33162041, "bfi", "w1, w2, #10, #9"},
		{"bfm_bfxil_w0_8", 0x33001d00, "bfxil", "w0, w8, #0, #8"},
		{"bfm_bfxil_w4_1", 0x33041043, "bfxil", "w3, w2, #4, #1"},
	})
}

// TestAlias_AddSubImm covers cmp/cmn (subs/adds zr), mov Rd,sp (add #0
// with SP), and the base add/sub-imm rendering this decoder now owns.
func TestAlias_AddSubImm(t *testing.T) {
	runAliasCases(t, []aliasCase{
		{"cmp_x0_c", 0xf100301f, "cmp", "x0, #0xc"},
		{"cmp_x11_870", 0xf121c17f, "cmp", "x11, #0x870"},
		{"cmp_w0_10", 0x7100401f, "cmp", "w0, #0x10"},
		{"cmn_w3_5", 0x3100147f, "cmn", "w3, #0x5"},
		{"cmn_x3_5", 0xb100147f, "cmn", "x3, #0x5"},
		{"mov_x29_sp", 0x910003fd, "mov", "x29, sp"},
		{"mov_x0_sp", 0x910003e0, "mov", "x0, sp"},
		{"mov_sp_sp", 0x910003ff, "mov", "sp, sp"},
		// base forms (no SP, no zr) stay add/sub/adds/subs.
		{"add_x10_x10_0", 0x9100014a, "add", "x10, x10, #0x0"},
		{"add_x5_x3_16", 0x91004065, "add", "x5, x3, #0x10"},
		{"add_x5_x3_16_lsl12", 0x91404065, "add", "x5, x3, #0x10, lsl #12"},
		{"sub_x5_x3_16", 0xd1004065, "sub", "x5, x3, #0x10"},
		{"add_sp_sp_16", 0x910043ff, "add", "sp, sp, #0x10"},
		{"adds_w5_w3_16", 0x31004065, "adds", "w5, w3, #0x10"},
		{"subs_w5_w3_16_lsl12", 0x71404065, "subs", "w5, w3, #0x10, lsl #12"},
	})
}

// TestAlias_LogicalImmMov covers orr (immediate) Rn=zr → mov #<bitmask>,
// and the cases objdump keeps as orr (movz-encodable immediates).
func TestAlias_LogicalImmMov(t *testing.T) {
	runAliasCases(t, []aliasCase{
		{"mov_x15_cc", 0xb202e7ef, "mov", "x15, #0xcccccccccccccccc"},
		{"mov_w1_c000c000", 0x320287e1, "mov", "w1, #0xc000c000"},
		{"mov_w6_1010101", 0x3200c3e6, "mov", "w6, #0x1010101"},
		{"mov_x0_ffffffff", 0xb2407fe0, "mov", "x0, #0xffffffff"},
		// movz-encodable bitmasks stay orr.
		{"orr_x0_1_kept", 0xb24003e0, "orr", "x0, xzr, #0x1"},
		{"orr_x0_ffff_kept", 0xb2403fe0, "orr", "x0, xzr, #0xffff"},
	})
}

// TestAlias_DPRegister covers the shifted-register aliases:
// cmp/cmn/tst (zr-dest), neg/negs (zr-source), mov/mvn (orr/orn zr).
func TestAlias_DPRegister(t *testing.T) {
	runAliasCases(t, []aliasCase{
		{"cmp_w0_w3", 0x6b03001f, "cmp", "w0, w3"},
		{"cmp_x0_x3", 0xeb03001f, "cmp", "x0, x3"},
		{"cmn_w3_w?", 0x2b03001f, "cmn", "w0, w3"},
		{"tst_w0_w3", 0x6a03001f, "tst", "w0, w3"},
		{"tst_x0_x3", 0xea03001f, "tst", "x0, x3"},
		{"neg_w5_w3", 0x4b0303e5, "neg", "w5, w3"},
		{"neg_x5_x3", 0xcb0303e5, "neg", "x5, x3"},
		{"negs_w5_w3", 0x6b0303e5, "negs", "w5, w3"},
		{"negs_x5_x3", 0xeb0303e5, "negs", "x5, x3"},
		// mov (orr zr, unshifted) and mvn (orn zr).
		{"mov_x1_x0", 0xaa0003e1, "mov", "x1, x0"},
		{"mvn_x0_x1", 0xaa2103e0, "mvn", "x0, x1"},
		// shifted cmp keeps the shift suffix.
		{"cmp_x0_x3_lsl3", 0xeb030c1f, "cmp", "x0, x3, lsl #3"},
		// shifted orr-zr stays orr (mov alias only covers unshifted).
		{"orr_shifted_kept", 0xaa150fe2, "orr", "x2, xzr, x21, lsl #3"},
	})
}

// TestAlias_CondSelect covers csinc/csinv/csneg → cset/csetm/cinc/cinv/cneg.
func TestAlias_CondSelect(t *testing.T) {
	runAliasCases(t, []aliasCase{
		{"cinc_w5_w3_ne", 0x1a830465, "cinc", "w5, w3, ne"},
		{"cset_w5_ne", 0x1a9f07e5, "cset", "w5, ne"},
		{"csetm_w5_ne", 0x5a9f03e5, "csetm", "w5, ne"},
		{"cinv_w5_w3_ne", 0x5a830065, "cinv", "w5, w3, ne"},
		{"cneg_w5_w3_ne", 0x5a830465, "cneg", "w5, w3, ne"},
	})
}

// TestAlias_ShiftVar covers the data-processing 2-source variable-shift
// aliases (lslv/lsrv/asrv/rorv → lsl/lsr/asr/ror).
func TestAlias_ShiftVar(t *testing.T) {
	runAliasCases(t, []aliasCase{
		{"lsr_w12_w14", 0x1ace258c, "lsr", "w12, w12, w14"},
		{"lsl_w5", 0x1ac42065, "lsl", "w5, w3, w4"},
		{"lsr_w5", 0x1ac42465, "lsr", "w5, w3, w4"},
		{"asr_w5", 0x1ac42865, "asr", "w5, w3, w4"},
		{"ror_w5", 0x1ac42c65, "ror", "w5, w3, w4"},
		{"lsr_x5", 0x9ac42465, "lsr", "x5, x3, x4"},
	})
}

// TestAlias_BitfieldUndefined covers bitfield-class words that are
// architecturally undefined (N != sf), which objdump renders `.inst`.
func TestAlias_BitfieldUndefined(t *testing.T) {
	runAliasCases(t, []aliasCase{
		{"sf0_N1_undef", 0x5355004e, ".inst", "0x5355004e"},
		{"sf0_N1_undef2", 0x53524556, ".inst", "0x53524556"},
	})
}

// TestAlias_Mul3Source covers the data-processing 3-source multiply
// family — distinct instructions the encoder's loose madd Form would
// otherwise all decode as madd.
func TestAlias_Mul3Source(t *testing.T) {
	runAliasCases(t, []aliasCase{
		{"madd_x16", 0x9b0c2a10, "madd", "x16, x16, x12, x10"},
		{"madd_w23", 0x1b1a19d7, "madd", "w23, w14, w26, w6"},
		{"msub_x5", 0x9b049865, "msub", "x5, x3, x4, x6"},
		{"mul_x5", 0x9b047c65, "mul", "x5, x3, x4"},
		{"mneg_x5", 0x9b04fc65, "mneg", "x5, x3, x4"},
		{"smaddl_x5", 0x9b241865, "smaddl", "x5, w3, w4, x6"},
		{"smull_x5", 0x9b247c65, "smull", "x5, w3, w4"},
		{"smnegl_x5", 0x9b24fc65, "smnegl", "x5, w3, w4"},
		{"smulh_x5", 0x9b447c65, "smulh", "x5, x3, x4"},
		{"umaddl_x19", 0x9bac2a73, "umaddl", "x19, w19, w12, x10"},
		{"umull_x4", 0x9ba37c24, "umull", "x4, w1, w3"},
		{"umnegl_x5", 0x9ba4fc65, "umnegl", "x5, w3, w4"},
		{"umulh_x14", 0x9bcb7dae, "umulh", "x14, x13, x11"},
	})
}
