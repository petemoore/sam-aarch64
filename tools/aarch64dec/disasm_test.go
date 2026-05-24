package aarch64dec

import (
	"testing"
)

// TestDecodeKnownEncodings exercises the (mnem, operands) output for
// a small set of hand-picked aarch64 words drawn from the M3 fixture
// corpus and the ARM ARM examples.  Each entry's `word` and expected
// text was cross-checked against `objdump -d -b binary -m aarch64`.
//
// Task 1 scope is intentionally narrow — the core SlotKinds
// (Xreg/Wreg/XregOrSp/WregOrSp, CondCode, Imm12Shifted) plus the
// zero-slot `nop` case.  Wider coverage lands in Task 2.
func TestDecodeKnownEncodings(t *testing.T) {
	tests := []struct {
		name     string
		word     uint32
		mnem     string
		operands string
	}{
		// nop — zero-slot form, MnemonicID 0, Pattern 0xd503201f.
		{"nop", 0xd503201f, "nop", ""},

		// add Xd, Xn, #imm — 64-bit ADD immediate.
		// Pattern 0x91000000 mask 0xff800000.  Rd=0, Rn=1, imm=5.
		// 0x91000000 | (5<<10) | (1<<5) | (0<<0)
		{"add_x0_x1_5", 0x91001420, "add", "x0, x1, #0x5"},

		// add Wd, Wn, #imm12 lsl #12 — sh bit set.  Rd=2, Rn=3, imm=1.
		// Pattern 0x11000000 | (1<<22 = sh) | (1<<10 = imm12) | (3<<5) | 2.
		// objdump renders this as two operands: `#0x1, lsl #12`.
		{"add_w2_w3_lsl12", 0x11400462, "add", "w2, w3, #0x1, lsl #12"},

		// mov Xd, Xn — register form via manualForms (ORR Xd, XZR, Xm).
		// Pattern 0xaa0003e0 | (Xm<<16) | Xd; here Xm=1, Xd=0.
		{"mov_x0_x1", 0xaa0103e0, "mov", "x0, x1"},

		// mov Wd, Wn — 32-bit register form.  Pattern 0x2a0003e0.
		{"mov_w2_w3", 0x2a0303e2, "mov", "w2, w3"},

		// sub Wd, Wn, #imm — 32-bit SUB immediate.  Rd=4, Rn=5, imm=7.
		// 0x51000000 | (7<<10) | (5<<5) | 4 = 0x51001ca4.
		{"sub_w4_w5_7", 0x51001ca4, "sub", "w4, w5, #0x7"},

		// ret — bare form (= ret x30).  The manualForms 0-operand
		// `ret` entry matches first and renders without operands, which
		// is what objdump emits for 0xd65f03c0.
		{"ret", 0xd65f03c0, "ret", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mnem, ops, ok := Decode(tc.word)
			if !ok {
				t.Fatalf("Decode(0x%08x): ok=false, expected mnem=%q ops=%q",
					tc.word, tc.mnem, tc.operands)
			}
			if mnem != tc.mnem {
				t.Errorf("Decode(0x%08x): mnem=%q, want %q",
					tc.word, mnem, tc.mnem)
			}
			if ops != tc.operands {
				t.Errorf("Decode(0x%08x): operands=%q, want %q",
					tc.word, ops, tc.operands)
			}
		})
	}
}

func TestDecodeUnknownReturnsFalse(t *testing.T) {
	// 0x00000000 is `udf #0` which is in the encoder table; pick a
	// bit pattern that no Form should match.  All-ones is reserved
	// and not in our table.
	if _, _, ok := Decode(0xffffffff); ok {
		t.Errorf("Decode(0xffffffff) returned ok=true; want false")
	}
}

func TestFormat(t *testing.T) {
	cases := []struct{ mnem, ops, want string }{
		{"nop", "", "nop"},
		{"add", "x0, x1, #0x5", "add\tx0, x1, #0x5"},
	}
	for _, tc := range cases {
		got := Format(tc.mnem, tc.ops)
		if got != tc.want {
			t.Errorf("Format(%q, %q) = %q, want %q", tc.mnem, tc.ops, got, tc.want)
		}
	}
}
