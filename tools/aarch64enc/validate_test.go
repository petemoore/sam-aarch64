package aarch64enc

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestMatchOperandKinds(t *testing.T) {
	mnID, _ := format.MnemonicID("add")
	candidates := []Form{
		{
			MnemonicID: mnID,
			Slots: []OperandSlot{
				{ExpectedKind: format.OpRegX},
				{ExpectedKind: format.OpRegXSP},
				{ExpectedKind: format.OpImmExpr},
			},
		},
		{
			MnemonicID: mnID,
			Slots: []OperandSlot{
				{ExpectedKind: format.OpRegX},
				{ExpectedKind: format.OpRegX},
				{ExpectedKind: format.OpShiftedReg},
			},
		},
	}
	form, ok := matchOperandKinds(candidates,
		[]format.OperandKind{format.OpRegX, format.OpRegXSP, format.OpImmExpr})
	if !ok || form.Slots[2].ExpectedKind != format.OpImmExpr {
		t.Errorf("did not match add immediate form")
	}
	form, ok = matchOperandKinds(candidates,
		[]format.OperandKind{format.OpRegX, format.OpRegX, format.OpShiftedReg})
	if !ok || form.Slots[2].ExpectedKind != format.OpShiftedReg {
		t.Errorf("did not match add shifted-reg form")
	}
	if _, ok := matchOperandKinds(candidates,
		[]format.OperandKind{format.OpRegX, format.OpRegX}); ok {
		t.Errorf("operand count mismatch should not match")
	}
}
