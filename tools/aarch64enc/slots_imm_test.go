package aarch64enc

import "testing"

func TestEncodeImm12Shifted_NoShift(t *testing.T) {
	slot := OperandSlot{SlotKind: Imm12Shifted, BitPosition: 10, BitWidth: 12}
	got, err := encodeImm12Shifted(slot, 0xABC)
	if err != nil || got != 0xABC<<10 {
		t.Errorf("Imm12Shifted(0xABC) = (0x%08x, %v)", got, err)
	}
}

func TestEncodeImm12Shifted_LSL12(t *testing.T) {
	slot := OperandSlot{SlotKind: Imm12Shifted, BitPosition: 10, BitWidth: 12}
	got, err := encodeImm12Shifted(slot, 0x1000)
	want := uint32(1)<<10 | uint32(1)<<22
	if err != nil || got != want {
		t.Errorf("Imm12Shifted(0x1000) = (0x%08x, %v), want 0x%08x", got, err, want)
	}
}

func TestEncodeImm12Shifted_OutOfRange(t *testing.T) {
	slot := OperandSlot{SlotKind: Imm12Shifted, BitPosition: 10, BitWidth: 12}
	if _, err := encodeImm12Shifted(slot, 0xFFFFFF); err == nil {
		t.Errorf("Imm12Shifted(0xFFFFFF) should overflow")
	}
	if _, err := encodeImm12Shifted(slot, 0x100001); err == nil {
		t.Errorf("Imm12Shifted(0x100001) should reject (not LSL12-aligned)")
	}
}

func TestEncodeImm16Shifted(t *testing.T) {
	slot := OperandSlot{SlotKind: Imm16Shifted, BitPosition: 5, BitWidth: 16}
	got, err := encodeImm16Shifted(slot, 0x42, 0)
	if err != nil || got != 0x42<<5 {
		t.Errorf("Imm16Shifted(0x42, hw=0) = (0x%08x, %v)", got, err)
	}
	got, err = encodeImm16Shifted(slot, 0x42, 2)
	want := uint32(0x42)<<5 | uint32(2)<<21
	if err != nil || got != want {
		t.Errorf("Imm16Shifted(0x42, hw=2) = (0x%08x, %v), want 0x%08x", got, err, want)
	}
}

func TestEncodeImm16Shifted_BadHw(t *testing.T) {
	slot := OperandSlot{SlotKind: Imm16Shifted, BitPosition: 5, BitWidth: 16}
	if _, err := encodeImm16Shifted(slot, 0, 4); err == nil {
		t.Errorf("Imm16Shifted(0, hw=4) should reject")
	}
}
