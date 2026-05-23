package aarch64enc

import "testing"

func TestEncodeAdrpImm(t *testing.T) {
	slot := OperandSlot{SlotKind: AdrpImm, BitPosition: 0, BitWidth: 21}

	got, err := encodeAdrpImm(slot, 4096)
	want := uint32(1) << 5
	if err != nil || got != want {
		t.Errorf("AdrpImm(+4096) = (0x%08x, %v), want 0x%08x", got, err, want)
	}

	got, err = encodeAdrpImm(slot, 4096*3)
	want = uint32(3) << 29
	if err != nil || got != want {
		t.Errorf("AdrpImm(+12288) = (0x%08x, %v), want 0x%08x", got, err, want)
	}

	got, err = encodeAdrpImm(slot, 4096*5)
	want = (uint32(1) << 29) | (uint32(1) << 5)
	if err != nil || got != want {
		t.Errorf("AdrpImm(+20480) = (0x%08x, %v), want 0x%08x", got, err, want)
	}

	if _, err := encodeAdrpImm(slot, 1); err == nil {
		t.Errorf("AdrpImm(1) misalignment should error")
	}

	if _, err := encodeAdrpImm(slot, int64(1)<<33); err == nil {
		t.Errorf("AdrpImm out of range should error")
	}
}
