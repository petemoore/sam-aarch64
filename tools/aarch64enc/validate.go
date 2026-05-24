package aarch64enc

import (
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// ValidateOperandKinds picks the form whose expected_kind tuple
// matches `kinds`, by linear scan in form-id order. Returns the
// matching form and ok=true, or a diagnostic + ok=false.
func ValidateOperandKinds(mnemonicID uint16, kinds []format.OperandKind) (Form, bool, string) {
	candidates := FormsForMnemonic(mnemonicID)
	if len(candidates) == 0 {
		return Form{}, false, fmt.Sprintf("no encoder forms for mnemonic id %d", mnemonicID)
	}
	if form, ok := matchOperandKinds(candidates, kinds); ok {
		return form, true, ""
	}
	return Form{}, false, formMismatchDiag(candidates, kinds)
}

func matchOperandKinds(candidates []Form, kinds []format.OperandKind) (Form, bool) {
	for _, form := range candidates {
		if len(form.Slots) != len(kinds) {
			continue
		}
		ok := true
		for i, slot := range form.Slots {
			if slot.ExpectedKind != kinds[i] {
				ok = false
				break
			}
		}
		if ok {
			return form, true
		}
	}
	return Form{}, false
}

// FormsForMnemonic is overridden by the generated data.go to return
// real forms. The default returns nil so unit tests can use
// matchOperandKinds directly with synthetic candidates.
var FormsForMnemonic = func(id uint16) []Form {
	return nil
}

func formMismatchDiag(candidates []Form, got []format.OperandKind) string {
	gotStr := "("
	for i, k := range got {
		if i > 0 {
			gotStr += ", "
		}
		gotStr += k.Name()
	}
	gotStr += ")"
	out := fmt.Sprintf("invalid operand kinds %s; expected one of:", gotStr)
	for _, f := range candidates {
		out += "\n  ("
		for i, slot := range f.Slots {
			if i > 0 {
				out += ", "
			}
			out += slot.ExpectedKind.Name()
		}
		out += ")"
	}
	return out
}
