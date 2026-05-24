package aarch64enc

import "testing"

func TestEncodeLogicalImm_Valid64(t *testing.T) {
	slot := OperandSlot{SlotKind: LogicalImm, BitPosition: 10, BitWidth: 13}
	got, err := encodeLogicalImm(slot, 0x00ff00ff00ff00ff, true)
	if err != nil {
		t.Fatalf("LogicalImm(0x00ff…) err = %v", err)
	}
	// For a replicating 16-bit pattern 0x00ff (8 ones in low byte),
	// the encoding is N=0, immr=0, imms encodes (size, ones).
	// Just check that the result is non-zero and is placed at BitPosition.
	if got == 0 {
		t.Errorf("LogicalImm(0x00ff…) returned zero")
	}
	if got&((1<<10)-1) != 0 {
		t.Errorf("LogicalImm: bits below BitPosition should be zero, got 0x%08x", got)
	}
}

func TestEncodeLogicalImm_Invalid(t *testing.T) {
	slot := OperandSlot{SlotKind: LogicalImm, BitPosition: 10, BitWidth: 13}
	if _, err := encodeLogicalImm(slot, 0, true); err == nil {
		t.Errorf("LogicalImm(0) should fail")
	}
	if _, err := encodeLogicalImm(slot, -1, true); err == nil {
		t.Errorf("LogicalImm(-1) should fail")
	}
	if _, err := encodeLogicalImm(slot, 0x12345, true); err == nil {
		t.Errorf("LogicalImm(0x12345) should fail (not encodable)")
	}
}

func TestEncodeLogicalImm_32bit(t *testing.T) {
	slot := OperandSlot{SlotKind: LogicalImm, BitPosition: 10, BitWidth: 13}
	got, err := encodeLogicalImm(slot, 0x000f000f, false)
	if err != nil {
		t.Fatalf("LogicalImm 32-bit err = %v", err)
	}
	if got == 0 {
		t.Errorf("LogicalImm 32-bit returned zero")
	}
}

// TestEncodeLogicalImm_AgainstGNUAs pins exact encodings cross-checked
// against `aarch64-none-elf-as`. If these drift, the algorithm has
// regressed.
func TestEncodeLogicalImm_AgainstGNUAs(t *testing.T) {
	slot := OperandSlot{SlotKind: LogicalImm, BitPosition: 10, BitWidth: 13}
	cases := []struct {
		imm      int64
		is64     bool
		wantImm13 uint32 // bare imm13 value (will be shifted to BitPosition)
		desc     string
	}{
		// `and x0, x0, #0xff` → 0x92401C00. imm13 = 0x1007 (N=1, immr=0, imms=7).
		{0xff, true, 0x1007, "0xff (64): N=1, immr=0, imms=7"},
		// `and x0, x0, #0xffff` → 0x92403C00. imm13 = 0x100F.
		{0xffff, true, 0x100F, "0xffff (64): N=1, immr=0, imms=15"},
		// `orr x0, x1, #0x00ff00ff00ff00ff` replicates at 16 bits with 8 ones.
		// ARM imms encoding for size=16 (len=4): imms = 0b10_0000 | (8-1) = 0x27.
		// Verified: aarch64-none-elf-as gives 0xb2009c20 → N=0, immr=0, imms=0x27.
		{0x00ff00ff00ff00ff, true, 0x27, "0x00ff... (16-bit replicating, 8 ones): N=0, imms=0x27"},
	}
	for _, c := range cases {
		got, err := encodeLogicalImm(slot, c.imm, c.is64)
		if err != nil {
			t.Errorf("%s: err = %v", c.desc, err)
			continue
		}
		want := c.wantImm13 << 10
		if got != want {
			t.Errorf("%s: got 0x%08x (imm13=0x%x), want 0x%08x (imm13=0x%x)",
				c.desc, got, got>>10, want, c.wantImm13)
		}
	}
}
