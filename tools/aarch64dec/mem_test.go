package aarch64dec

import "testing"

// TestDecodeMem exercises the load/store (memory) decoders in mem.go.
//
// Every word + expected text below was cross-checked against
// `aarch64-none-elf-objdump -D -z -b binary -m aarch64` on the raw
// little-endian word (the same oracle the CI comparison uses).  The
// objdump rendering is the ground truth: decimal immediate offsets,
// omission of a zero offset (`[x0]` not `[x0, #0]`), `lsl #N`/extend
// keywords on register offsets, and x/w/sp/xzr register naming.
func TestDecodeMem(t *testing.T) {
	tests := []struct {
		name     string
		pc       uint64
		word     uint32
		mnem     string
		operands string
	}{
		// --- scalar unsigned-offset (mode=01) -----------------------------
		{"ldr_x_base", 0, 0xf9400000, "ldr", "x0, [x0]"},
		{"ldr_x_off8", 0, 0xf9400400, "ldr", "x0, [x0, #8]"},
		{"ldr_x_off512", 0, 0xf9410000, "ldr", "x0, [x0, #512]"},
		{"ldr_w_base", 0, 0xb9400000, "ldr", "w0, [x0]"},
		{"str_w_base", 0, 0xb9000020, "str", "w0, [x1]"},
		{"str_w_off4", 0, 0xb9000420, "str", "w0, [x1, #4]"},
		{"ldr_w_off112", 0, 0xb940700b, "ldr", "w11, [x0, #112]"},
		{"ldrb_w_base", 0, 0x39400000, "ldrb", "w0, [x0]"},
		{"ldrh_w_base", 0, 0x79400000, "ldrh", "w0, [x0]"},
		{"ldrsb_x", 0, 0x39800000, "ldrsb", "x0, [x0]"},
		{"ldrsb_w", 0, 0x39c00000, "ldrsb", "w0, [x0]"},
		{"ldrsh_x", 0, 0x79800000, "ldrsh", "x0, [x0]"},
		{"ldrsh_w", 0, 0x79c00000, "ldrsh", "w0, [x0]"},
		{"ldrsw_x", 0, 0xb9800000, "ldrsw", "x0, [x0]"},

		// --- scalar pre/post index (mode=00, bits[11:10]=11/01) -----------
		{"ldr_x_post8", 0, 0xf8408400, "ldr", "x0, [x0], #8"},
		{"ldr_x_pre8", 0, 0xf8408c00, "ldr", "x0, [x0, #8]!"},
		{"str_x_post8", 0, 0xf8008401, "str", "x1, [x0], #8"},

		// --- unscaled stur/ldur (mode=00, bits[11:10]=00) -----------------
		{"stur_x_neg16", 0, 0xf81f0000, "stur", "x0, [x0, #-16]"},
		{"stur_x_neg256", 0, 0xf8100000, "stur", "x0, [x0, #-256]"},
		{"stur_x_255", 0, 0xf80ff000, "stur", "x0, [x0, #255]"},
		{"stur_x_base", 0, 0xf8000000, "stur", "x0, [x0]"},
		{"stur_w_neg16", 0, 0xb81f0000, "stur", "w0, [x0, #-16]"},

		// --- scalar register offset (mode=00, bits[11:10]=10, bit21=1) ----
		{"ldr_x_regoff", 0, 0xf8616800, "ldr", "x0, [x0, x1]"},
		{"ldr_x_regoff_lsl3", 0, 0xf8617800, "ldr", "x0, [x0, x1, lsl #3]"},

		// --- pair signed-offset (mode=10) ---------------------------------
		{"ldp_x_base", 0, 0xa9400400, "ldp", "x0, x1, [x0]"},
		{"ldp_x_off8", 0, 0xa9408800, "ldp", "x0, x2, [x0, #8]"},
		{"ldp_x_negoff", 0, 0xa97f0000, "ldp", "x0, x0, [x0, #-16]"},
		{"ldp_w_base", 0, 0x29400400, "ldp", "w0, w1, [x0]"},

		// --- pair pre/post index (mode=11/01) -----------------------------
		{"stp_x_pre_neg16", 0, 0xa9bf7bfd, "stp", "x29, x30, [sp, #-16]!"},
		{"ldp_x_post16", 0, 0xa8c103e3, "ldp", "x3, x0, [sp], #16"},

		// --- LDR literal (PC-relative, bits[29:26]=0110) ------------------
		// target = pc + (sext(imm19) << 2); rendered as hex.
		{"ldr_lit_x", 0x34, 0x5802a0e0, "ldr", "x0, 0x5450"},
		{"ldr_lit_w", 0x130, 0x18000e42, "ldr", "w2, 0x2f8"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mnem, ops, ok := DecodeAt(tc.pc, tc.word)
			if !ok {
				t.Fatalf("DecodeAt(%#x, %#08x) returned ok=false; want %q %q",
					tc.pc, tc.word, tc.mnem, tc.operands)
			}
			if mnem != tc.mnem || ops != tc.operands {
				t.Errorf("DecodeAt(%#x, %#08x) = %q %q; want %q %q",
					tc.pc, tc.word, mnem, ops, tc.mnem, tc.operands)
			}
		})
	}
}

// TestDecodeMemRejectsSIMD confirms the integer load/store decoder does
// NOT claim the SIMD/FP load-store space (V bit26=1).  These bit patterns
// occur in release.img's data regions; objdump renders them as SIMD
// (`str d7, ...`), but the spectrum4 encoder never emits them, so we
// leave them to the form-walk / .inst fallback rather than mis-decoding
// them as integer ops.
func TestDecodeMemRejectsSIMD(t *testing.T) {
	for _, w := range []uint32{0xfc0ffc07 /*str d7*/, 0x1c3f0c1e /*ldr s30*/, 0x3c0f0c0c /*str b12*/} {
		if _, _, ok := decodeMem(0, w); ok {
			t.Errorf("decodeMem(%#08x) claimed a SIMD word; want ok=false", w)
		}
	}
}
