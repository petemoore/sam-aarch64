package aarch64enc

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

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

func TestEncodeShiftAmount(t *testing.T) {
	slot := OperandSlot{SlotKind: ShiftAmount, BitPosition: 10, BitWidth: 6}
	got, err := encodeShiftAmount(slot, 4)
	if err != nil || got != 4<<10 {
		t.Errorf("ShiftAmount(4) = (0x%08x, %v)", got, err)
	}
	if _, err := encodeShiftAmount(slot, 64); err == nil {
		t.Errorf("ShiftAmount(64) should overflow 6 bits")
	}
}

func TestEncodeExtendOp(t *testing.T) {
	slot := OperandSlot{SlotKind: ExtendOp, BitPosition: 10, BitWidth: 6}
	got, err := encodeExtendOp(slot, format.ExtUXTW, 0)
	want := uint32(2) << (10 + 3)
	if err != nil || got != want {
		t.Errorf("ExtendOp(uxtw, 0) = (0x%08x, %v), want 0x%08x", got, err, want)
	}
	got, err = encodeExtendOp(slot, format.ExtSXTW, 2)
	want = uint32(6)<<(10+3) | uint32(2)<<10
	if err != nil || got != want {
		t.Errorf("ExtendOp(sxtw, 2) = (0x%08x, %v), want 0x%08x", got, err, want)
	}
	if _, err := encodeExtendOp(slot, format.ExtSXTW, 5); err == nil {
		t.Errorf("ExtendOp shift 5 should overflow imm3")
	}
}
