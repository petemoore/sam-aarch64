package aarch64enc

import "testing"

// Hand-computed fold vectors for every overlay slot the m6-release fixture
// exercises (plus the two non-exercised-but-structural slots, Logical and
// MemImm9). Each `want` is derived by hand from the corresponding pass2.go
// conversion + aarch64enc slot encoder, so a one-bit drift in Fold fails
// here deterministically — the byte-match guard at unit granularity.
func TestFold(t *testing.T) {
	cases := []struct {
		name     string
		slot     FoldSlot
		value    int64
		pc       int64
		baseWord uint32
		want     uint32
	}{
		// PC-dependent: off = value - pc, /4, placed at the slot's bits.
		{"branch26 +1024 instr", FoldBranch26, 0x2000, 0x1000, 0x94000000, 0x400},
		{"branch19 +16 instr", FoldBranch19, 0x1040, 0x1000, 0x54000000, 0x10 << 5},
		{"branch14 -16 instr", FoldBranch14, 0xFC0, 0x1000, 0x36000000, (0x3FF0) << 5},
		// ADR: raw byte offset split into immlo:2 @29, immhi:19 @5.
		{"adr +2 bytes", FoldAdr, 0x1002, 0x1000, 0x10000000, 0x2 << 29},
		// ADRP: page diff /4096 split into immlo:2 @29, immhi:19 @5.
		{"adrp +4 pages", FoldAdrp, 0x5000, 0x1000, 0x90000000, 0x1 << 5},
		// PC-invariant symbol-bearing slots (value used directly).
		{"add/sub imm12 lo12", FoldAddSubImm12, 0x123, 0, 0x91000000, 0x123 << 10},
		{"mem imm12 X-scale8", FoldMemImm12, 0x40, 0, 0xF9400000, (0x40 / 8) << 10},
		{"mem imm9 -8 unscaled", FoldMemImm9, -8, 0, 0xF8400000, (0x1F8) << 12},
		{"movk imm16 hw=1", FoldMovkImm16, 0x12340000, 0, 0x52a00000, 0x1234 << 5},
		{"logical #0xFFF w-reg", FoldLogical, 0xFFF, 0, 0x32000000, 0xB << 10},
		{"pair imm7 X-scale8", FoldPairImm7, 0x40, 0, 0xA9000000, 0x8 << 15},
		// Litpool: value is the pool-entry PC; fold like branch19.
		{"litpool imm19 +32 instr", FoldLitpool19, 0x1080, 0x1000, 0x58000000, 0x20 << 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Fold(c.slot, c.value, c.pc, c.baseWord)
			if err != nil {
				t.Fatalf("Fold(%s) error: %v", c.name, err)
			}
			if got != c.want {
				t.Errorf("Fold(%s) = %#08x, want %#08x", c.name, got, c.want)
			}
		})
	}
}

// ZeroSlot must clear exactly the bits Fold sets: for every slot, a base
// word ORed with a folded value, then ZeroSlot'd, equals the base word
// ZeroSlot'd. This is the invariant the compactor relies on to recover a
// patch-free base word.
func TestZeroSlotClearsFoldBits(t *testing.T) {
	cases := []struct {
		slot     FoldSlot
		value    int64
		pc       int64
		baseWord uint32
	}{
		{FoldBranch26, 0x2000, 0x1000, 0x94000000},
		{FoldBranch19, 0x1040, 0x1000, 0x54000000},
		{FoldBranch14, 0xFC0, 0x1000, 0x36000000},
		{FoldAdr, 0x1002, 0x1000, 0x10000000},
		{FoldAdrp, 0x5000, 0x1000, 0x90000000},
		{FoldAddSubImm12, 0x123, 0, 0x91000000},
		{FoldMemImm12, 0x40, 0, 0xF9400000},
		{FoldMemImm9, -8, 0, 0xF8400000},
		{FoldMovkImm16, 0x12340000, 0, 0x52a00000},
		{FoldLogical, 0xFFF, 0, 0x32000000},
		{FoldPairImm7, 0x40, 0, 0xA9000000},
		{FoldLitpool19, 0x1080, 0x1000, 0x58000000},
	}
	for _, c := range cases {
		bits, err := Fold(c.slot, c.value, c.pc, c.baseWord)
		if err != nil {
			t.Fatalf("Fold(slot %d) error: %v", c.slot, err)
		}
		full := c.baseWord | bits
		if got, want := ZeroSlot(full, c.slot), ZeroSlot(c.baseWord, c.slot); got != want {
			t.Errorf("slot %d: ZeroSlot(base|fold)=%#08x, ZeroSlot(base)=%#08x", c.slot, got, want)
		}
		// And the zeroed base must, ORed with the fold, reproduce `full`.
		if got := ZeroSlot(full, c.slot) | bits; got != full {
			t.Errorf("slot %d: ZeroSlot(full)|fold=%#08x, want full=%#08x", c.slot, got, full)
		}
	}
}
