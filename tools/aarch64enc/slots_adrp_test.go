package aarch64enc

import "testing"

func TestEncodeAdrpImm(t *testing.T) {
	slot := OperandSlot{SlotKind: AdrpImm, BitPosition: 0, BitWidth: 21}

	// +4096 (one page forward) → imm21 = 1; immlo = 1 at bits 29..30.
	got, err := encodeAdrpImm(slot, 4096)
	want := uint32(1) << 29
	if err != nil || got != want {
		t.Errorf("AdrpImm(+4096) = (0x%08x, %v), want 0x%08x", got, err, want)
	}

	// +4096*3 → imm21 = 3 = 0b11; immlo = 0b11 at bits 29..30.
	got, err = encodeAdrpImm(slot, 4096*3)
	want = uint32(3) << 29
	if err != nil || got != want {
		t.Errorf("AdrpImm(+12288) = (0x%08x, %v), want 0x%08x", got, err, want)
	}

	// +4096*5 → imm21 = 5 = 0b101; immlo = 0b01 (bit 29), immhi = 0b1 (bit 5).
	got, err = encodeAdrpImm(slot, 4096*5)
	want = (uint32(1) << 29) | (uint32(1) << 5)
	if err != nil || got != want {
		t.Errorf("AdrpImm(+20480) = (0x%08x, %v), want 0x%08x", got, err, want)
	}

	// -4096 → imm21 = -1 = 0x1FFFFF; immlo = 0b11, immhi = 0x7FFFF.
	got, err = encodeAdrpImm(slot, -4096)
	want = (uint32(3) << 29) | (uint32(0x7FFFF) << 5)
	if err != nil || got != want {
		t.Errorf("AdrpImm(-4096) = (0x%08x, %v), want 0x%08x", got, err, want)
	}

	if _, err := encodeAdrpImm(slot, 1); err == nil {
		t.Errorf("AdrpImm(1) misalignment should error")
	}

	if _, err := encodeAdrpImm(slot, int64(1)<<33); err == nil {
		t.Errorf("AdrpImm out of range should error")
	}
}
