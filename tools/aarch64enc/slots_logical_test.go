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
