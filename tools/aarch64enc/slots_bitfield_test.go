package aarch64enc

import "testing"

func TestEncodeBitfieldBfi(t *testing.T) {
	immrSlot := OperandSlot{SlotKind: BitfieldImm, BitPosition: 16, BitWidth: 6}
	immsSlot := OperandSlot{SlotKind: BitfieldImm, BitPosition: 10, BitWidth: 6}
	regsize := 64

	got, err := encodeBitfieldBFI(immrSlot, immsSlot, regsize, 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	want := uint32(60)<<16 | uint32(7)<<10
	if got != want {
		t.Errorf("bfi(lsb=4, width=8) = 0x%08x, want 0x%08x", got, want)
	}
}

func TestEncodeBitfieldUbfx(t *testing.T) {
	immrSlot := OperandSlot{SlotKind: BitfieldImm, BitPosition: 16, BitWidth: 6}
	immsSlot := OperandSlot{SlotKind: BitfieldImm, BitPosition: 10, BitWidth: 6}

	got, err := encodeBitfieldUBFX(immrSlot, immsSlot, 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	want := uint32(4)<<16 | uint32(11)<<10
	if got != want {
		t.Errorf("ubfx(lsb=4, width=8) = 0x%08x, want 0x%08x", got, want)
	}
}
