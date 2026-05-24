package aarch64enc

import "testing"

func TestSlotKindValues(t *testing.T) {
	cases := map[SlotKind]byte{
		Xreg: 0x01, Wreg: 0x02, XregOrSp: 0x03, WregOrSp: 0x04,
		Imm5: 0x05, Imm6: 0x06, CondCode: 0x07,
		Imm12Shifted: 0x10, Imm16Shifted: 0x11, ShiftAmount: 0x12, ExtendOp: 0x13,
		BranchImm26: 0x20, BranchImm19: 0x21, BranchImm14: 0x22,
		AdrpImm: 0x23, LogicalImm: 0x24, BitfieldImm: 0x25,
	}
	for sk, want := range cases {
		if byte(sk) != want {
			t.Errorf("%v = 0x%02x, want 0x%02x", sk, byte(sk), want)
		}
	}
}

func TestSlotKindName(t *testing.T) {
	if Xreg.Name() != "Xreg" {
		t.Errorf("Xreg.Name() = %q, want %q", Xreg.Name(), "Xreg")
	}
	if SlotKind(0xFF).Name() != "Unknown" {
		t.Errorf("unknown slot kind name should be Unknown")
	}
}
