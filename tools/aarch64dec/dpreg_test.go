package aarch64dec

import "testing"

// TestDecodeShiftedReg covers the shifted-register data-processing forms
// (add/sub/adds/subs + logical and/orr/eor/ands/bic/orn/eon).  Each word
// + expected text was cross-checked against aarch64-none-elf-objdump.
func TestDecodeShiftedReg(t *testing.T) {
	tests := []struct {
		name     string
		pc       uint64
		word     uint32
		mnem     string
		operands string
	}{
		// add x1, x0, x1 — no shift (imm6=0 → shift suppressed).
		{"add_x1_x0_x1", 0, 0x8b010001, "add", "x1, x0, x1"},
		// add x16, x16, x16, lsl #2.
		{"add_lsl2", 0, 0x8b100a10, "add", "x16, x16, x16, lsl #2"},
		// 32-bit add w15, w15, w14, lsl #8.
		{"add_w_lsl8", 0, 0x0b0e21ef, "add", "w15, w15, w14, lsl #8"},
		// sub x2, x2, x1, lsl #1.
		{"sub_lsl1", 0, 0xcb010442, "sub", "x2, x2, x1, lsl #1"},
		// subs x11, x0, x9 — real subs (Rd != xzr), no shift.
		{"subs_x11", 0, 0xeb09000b, "subs", "x11, x0, x9"},
		// orr w16, w17, w17, lsr #4.
		{"orr_lsr4", 0, 0x2a511230, "orr", "w16, w17, w17, lsr #4"},
		// and x11, x11, x24, lsr #32.
		{"and_lsr32", 0, 0x8a58816b, "and", "x11, x11, x24, lsr #32"},
		// orr x27, x9, x12, lsr #1.
		{"orr_x_lsr1", 0, 0xaa4c053b, "orr", "x27, x9, x12, lsr #1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mnem, ops, ok := DecodeAt(tc.pc, tc.word)
			if !ok {
				t.Fatalf("DecodeAt(0x%08x): ok=false, want %s %s", tc.word, tc.mnem, tc.operands)
			}
			if mnem != tc.mnem || ops != tc.operands {
				t.Errorf("DecodeAt(0x%08x) = %q %q, want %q %q", tc.word, mnem, ops, tc.mnem, tc.operands)
			}
		})
	}
}

// TestDecodeExtendedReg covers the extended-register add/sub forms,
// including the lsl-vs-uxtx rendering nuance and the sp-register cases.
func TestDecodeExtendedReg(t *testing.T) {
	tests := []struct {
		name     string
		word     uint32
		mnem     string
		operands string
	}{
		// add x1, x2, w3, uxtw (amt 0 → keyword shown, no #amt).
		{"add_uxtw", 0x8b234041, "add", "x1, x2, w3, uxtw"},
		// add x1, x2, w3, sxtw #2.
		{"add_sxtw2", 0x8b23c841, "add", "x1, x2, w3, sxtw #2"},
		// add x0, x1, x2, uxtx (amt 0, no sp → uxtx shown).
		{"add_uxtx", 0x8b226020, "add", "x0, x1, x2, uxtx"},
		// add x0, x1, x2, uxtx #3.
		{"add_uxtx3", 0x8b226c20, "add", "x0, x1, x2, uxtx #3"},
		// add x0, sp, x1 — uxtx #0 with sp involved → extend omitted.
		{"add_sp_omit", 0x8b2163e0, "add", "x0, sp, x1"},
		// add x0, sp, x1, lsl #2 — uxtx option + sp → rendered lsl.
		{"add_sp_lsl2", 0x8b216be0, "add", "x0, sp, x1, lsl #2"},
		// add sp, x0, x1 — Rd=sp, uxtx #0 → omitted.
		{"add_rd_sp", 0x8b21601f, "add", "sp, x0, x1"},
		// 32-bit add wsp, w0, w1 — uxtw option(010) + sp → omitted.
		{"add_wsp", 0x0b21401f, "add", "wsp, w0, w1"},
		// 32-bit add wsp, wsp, w1, lsl #2 — uxtw + sp → lsl.
		{"add_wsp_lsl2", 0x0b214bff, "add", "wsp, wsp, w1, lsl #2"},
		// subs x0, x1, w2, uxtb — extended subs.
		{"subs_uxtb", 0xeb220020, "subs", "x0, x1, w2, uxtb"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mnem, ops, ok := Decode(tc.word)
			if !ok {
				t.Fatalf("Decode(0x%08x): ok=false, want %s %s", tc.word, tc.mnem, tc.operands)
			}
			if mnem != tc.mnem || ops != tc.operands {
				t.Errorf("Decode(0x%08x) = %q %q, want %q %q", tc.word, mnem, ops, tc.mnem, tc.operands)
			}
		})
	}
}

// TestDecodeDPRegRejects ensures decodeDPReg declines encodings outside
// its space and unallocated sub-encodings (so they fall back to .inst,
// matching objdump's `undefined`).
func TestDecodeDPRegRejects(t *testing.T) {
	// 6b6a6968: arithmetic class, bit21=1 (extended), but bits[23:22]=01
	// (opt != 0) → unallocated.  objdump renders it `.inst`.
	if _, _, ok := decodeDPReg(0x6b6a6968); ok {
		t.Errorf("decodeDPReg(0x6b6a6968): ok=true, want false (opt!=0 is unallocated)")
	}
}
