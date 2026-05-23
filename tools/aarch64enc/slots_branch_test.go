package aarch64enc

import "testing"

func TestEncodeBranchImm26(t *testing.T) {
	slot := OperandSlot{SlotKind: BranchImm26, BitPosition: 0, BitWidth: 26}
	got, err := encodeBranchImm(slot, 0)
	if err != nil || got != 0 {
		t.Errorf("BranchImm26(0) = (0x%08x, %v)", got, err)
	}
	got, err = encodeBranchImm(slot, 4)
	if err != nil || got != 1 {
		t.Errorf("BranchImm26(+4) = (0x%08x, %v)", got, err)
	}
	got, err = encodeBranchImm(slot, -4)
	if err != nil || got != 0x03FFFFFF {
		t.Errorf("BranchImm26(-4) = (0x%08x, %v)", got, err)
	}
	max := int64(1<<27) - 4
	got, err = encodeBranchImm(slot, max)
	if err != nil || got != (1<<25)-1 {
		t.Errorf("BranchImm26(max) = (0x%08x, %v)", got, err)
	}
	if _, err := encodeBranchImm(slot, 1<<28); err == nil {
		t.Errorf("BranchImm26 too-large positive should error")
	}
	if _, err := encodeBranchImm(slot, 6); err == nil {
		t.Errorf("BranchImm26(6) misalignment should error")
	}
}

func TestEncodeBranchImm19(t *testing.T) {
	slot := OperandSlot{SlotKind: BranchImm19, BitPosition: 5, BitWidth: 19}
	got, err := encodeBranchImm(slot, 8)
	if err != nil || got != 2<<5 {
		t.Errorf("BranchImm19(+8) = (0x%08x, %v)", got, err)
	}
	got, err = encodeBranchImm(slot, -4)
	want := uint32((1<<19)-1) << 5
	if err != nil || got != want {
		t.Errorf("BranchImm19(-4) = (0x%08x, %v), want 0x%08x", got, err, want)
	}
}

func TestEncodeBranchImm14(t *testing.T) {
	slot := OperandSlot{SlotKind: BranchImm14, BitPosition: 5, BitWidth: 14}
	got, err := encodeBranchImm(slot, 4)
	if err != nil || got != 1<<5 {
		t.Errorf("BranchImm14(+4) = (0x%08x, %v)", got, err)
	}
}
