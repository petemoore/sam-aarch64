package aarch64enc

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestEncodeAddImmForm(t *testing.T) {
	mnID, _ := format.MnemonicID("add")
	form := Form{
		MnemonicID: mnID,
		Pattern:    0x91000000,
		Slots: []OperandSlot{
			{SlotKind: Xreg, ExpectedKind: format.OpRegX, BitPosition: 0, BitWidth: 5},
			{SlotKind: XregOrSp, ExpectedKind: format.OpRegXSP, BitPosition: 5, BitWidth: 5},
			{SlotKind: Imm12Shifted, ExpectedKind: format.OpImmExpr, BitPosition: 10, BitWidth: 12},
		},
	}
	vals := []OperandValue{
		{Reg: 0}, {Reg: 1}, {Imm: 4},
	}
	got, err := Encode(form, vals)
	if err != nil {
		t.Fatal(err)
	}
	want := uint32(0x91000000) | (0 << 0) | (1 << 5) | (4 << 10)
	if got != want {
		t.Errorf("Encode(add x0, x1, #4) = 0x%08x, want 0x%08x", got, want)
	}
}

func TestEncodeOperandCountMismatch(t *testing.T) {
	form := Form{Slots: []OperandSlot{{SlotKind: Xreg, BitWidth: 5}}}
	if _, err := Encode(form, nil); err == nil {
		t.Errorf("Encode with empty values should error")
	}
}
