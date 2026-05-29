package aarch64dec

import "testing"

// TestDecodeTestBranch covers tbz / tbnz, including the 6-bit bit number
// split across b5/b40 and the PC-relative target.  Words + expected text
// cross-checked against aarch64-none-elf-objdump.
func TestDecodeTestBranch(t *testing.T) {
	tests := []struct {
		name     string
		pc       uint64
		word     uint32
		mnem     string
		operands string
	}{
		// @0x314: tbnz w10, #31, 0x310 (imm14 = -1 word → pc-4).
		{"tbnz_w10_31", 0x314, 0x37ffffea, "tbnz", "w10, #31, 0x310"},
		// @0x900: tbz w0, #4, 0x8ec.
		{"tbz_w0_4", 0x900, 0x3627ff60, "tbz", "w0, #4, 0x8ec"},
		// @0x930: tbnz w1, #31, 0x988.
		{"tbnz_w1_31", 0x930, 0x37f802c1, "tbnz", "w1, #31, 0x988"},
		// @0x940: tbz w1, #31, 0x988.
		{"tbz_w1_31", 0x940, 0x36f80241, "tbz", "w1, #31, 0x988"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mnem, ops, ok := DecodeAt(tc.pc, tc.word)
			if !ok {
				t.Fatalf("DecodeAt(0x%x, 0x%08x): ok=false, want %s %s", tc.pc, tc.word, tc.mnem, tc.operands)
			}
			if mnem != tc.mnem || ops != tc.operands {
				t.Errorf("DecodeAt(0x%x, 0x%08x) = %q %q, want %q %q", tc.pc, tc.word, mnem, ops, tc.mnem, tc.operands)
			}
		})
	}
}
