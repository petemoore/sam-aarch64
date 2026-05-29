package aarch64dec

import "testing"

// TestDecodeSys covers the System instruction group: mrs / msr (register
// and PSTATE-immediate), the SYS family (dc / tlbi), and the barriers
// (dsb / dmb / isb).  Words + expected text cross-checked against
// aarch64-none-elf-objdump on tests/m6/release/release.img.
func TestDecodeSys(t *testing.T) {
	tests := []struct {
		name     string
		word     uint32
		mnem     string
		operands string
	}{
		// --- mrs / msr (register) ---
		{"mrs_currentel", 0xd5384240, "mrs", "x0, currentel"},
		{"mrs_mpidr", 0xd53800a0, "mrs", "x0, mpidr_el1"},
		{"mrs_generic", 0xd539b040, "mrs", "x0, s3_1_c11_c0_2"},
		{"msr_generic", 0xd519b040, "msr", "s3_1_c11_c0_2, x0"},
		{"msr_generic2", 0xd519f220, "msr", "s3_1_c15_c2_1, x0"},
		{"msr_sctlr", 0xd5181000, "msr", "sctlr_el1, x0"},
		{"msr_hcr", 0xd51c1100, "msr", "hcr_el2, x0"},
		{"msr_ttbr0_xzr", 0xd518201f, "msr", "ttbr0_el1, xzr"},
		{"mrs_cntpct", 0xd53be021, "mrs", "x1, cntpct_el0"},

		// --- msr (PSTATE immediate) ---
		{"msr_daifset3", 0xd50343df, "msr", "daifset, #0x3"},
		{"msr_daifclr2", 0xd50342ff, "msr", "daifclr, #0x2"},

		// --- SYS family (dc / tlbi) ---
		{"dc_civac", 0xd50b7e2a, "dc", "civac, x10"},
		{"dc_ivac", 0xd5087623, "dc", "ivac, x3"},
		{"dc_cvac", 0xd50b7a31, "dc", "cvac, x17"},
		{"tlbi_vmalle1", 0xd508871f, "tlbi", "vmalle1"},

		// --- barriers ---
		{"dsb_sy", 0xd5033f9f, "dsb", "sy"},
		{"dsb_ishst", 0xd5033a9f, "dsb", "ishst"},
		{"dsb_ish", 0xd5033b9f, "dsb", "ish"},
		{"dmb_sy", 0xd5033fbf, "dmb", "sy"},
		{"isb_sy", 0xd5033fdf, "isb", ""},
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

// TestDecodeSysDeclinesHints ensures decodeSys returns ok=false for the
// hint sub-space (nop/wfi), which is decoded by the form walk instead.
// (A false positive here would shadow those forms.)
func TestDecodeSysDeclinesHints(t *testing.T) {
	for _, w := range []uint32{0xd503201f /* nop */, 0xd503207f /* wfi */} {
		if _, _, ok := decodeSys(w); ok {
			t.Errorf("decodeSys(0x%08x): ok=true, want false (hint belongs to form walk)", w)
		}
	}
	// And confirm the full decode still names them (via the form walk).
	if mnem, _, ok := Decode(0xd503201f); !ok || mnem != "nop" {
		t.Errorf("Decode(nop) = %q ok=%v, want nop", mnem, ok)
	}
}
