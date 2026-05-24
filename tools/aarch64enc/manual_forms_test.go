package aarch64enc

import "testing"

// TestManualFormsNonEmpty guards against an accidental wipe of the
// hand-curated form table (e.g. someone replacing manual_forms.go
// while regenerating data.go).
func TestManualFormsNonEmpty(t *testing.T) {
	if len(manualForms) == 0 {
		t.Fatal("manualForms is empty; hand-curated form table must not be lost")
	}
	if len(manualForms) < 50 {
		t.Errorf("manualForms has only %d entries; expected ~80+ (mvn, lsl, lsr, csel, csinc, csetm, bfi, bfxil, ubfx, sbfx, tst, bic, adr, eor, orr, ands, movk, movn, madd, msub, umull, umulh, umaddl, umsubl, eret, wfi, ror, mul, udiv, sxtw, bfc, …)", len(manualForms))
	}
}

// TestManualFormsKeyEntries pins down well-known hand-curated forms
// so that an accidental deletion is caught immediately rather than
// surfacing later as a byte-match regression.
func TestManualFormsKeyEntries(t *testing.T) {
	// (mnemonicID, pattern) tuples that must appear in manualForms.
	wanted := []struct {
		name    string
		id      uint16
		pattern uint32
	}{
		{"ret (0-operand, = ret x30)", 12, 0xd65f03c0},
		{"mov Xd, Xn via ORR (64-bit)", 3, 0xaa0003e0},
		{"mov Wd, Wn via ORR (32-bit)", 3, 0x2a0003e0},
		{"mvn 64-bit (ORN XZR)", 4, 0xaa2003e0},
		{"br Xn", 11, 0xd61f0000},
		{"blr Xn (manual half)", 44, 0xd63f0000},
		{"subs Xd, Xn, #imm12", 45, 0xf1000000},
		{"cmp Xn, Xm shifted-register", 19, 0xeb00001f},
		{"csel Xd, Xn, Xm, cond", 24, 0x9a800000},
		{"csinc Xd, Xn, Xm, cond", 25, 0x9a800400},
		{"madd Xd, Xn, Xm, Xa", 58, 0x9b000000},
		{"umulh Xd, Xn, Xm", 61, 0x9bc07c00},
		{"eret", 65, 0xd69f03e0},
		{"wfi", 69, 0xd503207f},
	}
	for _, w := range wanted {
		found := false
		for _, f := range manualForms {
			if f.MnemonicID == w.id && f.Pattern == w.pattern {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("manualForms missing %s: id=%d pattern=0x%08x", w.name, w.id, w.pattern)
		}
	}
}

// TestAllFormsUnion verifies that AllForms() returns manualForms first
// then generatedForms, preserving order.  matchOperandKinds depends on
// this so that hand-curated entries shadow MRA-derived ones with the
// same operand-kind shape (notably ORR-based mov vs MRA's ADD-imm-#0).
func TestAllFormsUnion(t *testing.T) {
	all := AllForms()
	if len(all) != len(manualForms)+len(generatedForms) {
		t.Fatalf("AllForms()=%d, expected manualForms(%d)+generatedForms(%d)=%d",
			len(all), len(manualForms), len(generatedForms),
			len(manualForms)+len(generatedForms))
	}
	// Form contains a slice, so use field-level equality on Pattern
	// + Mask + MnemonicID to spot order regressions.
	for i, m := range manualForms {
		if all[i].MnemonicID != m.MnemonicID || all[i].Pattern != m.Pattern || all[i].Mask != m.Mask {
			t.Errorf("AllForms()[%d]={id=%d,pat=0x%08x}, want manualForms[%d]={id=%d,pat=0x%08x}",
				i, all[i].MnemonicID, all[i].Pattern, i, m.MnemonicID, m.Pattern)
		}
	}
	for i, g := range generatedForms {
		j := len(manualForms) + i
		if all[j].MnemonicID != g.MnemonicID || all[j].Pattern != g.Pattern || all[j].Mask != g.Mask {
			t.Errorf("AllForms()[%d]={id=%d,pat=0x%08x}, want generatedForms[%d]={id=%d,pat=0x%08x}",
				j, all[j].MnemonicID, all[j].Pattern, i, g.MnemonicID, g.Pattern)
		}
	}
}

// TestFormsForMnemonicMovIsORR verifies the crucial encoder-shadowing
// invariant: looking up mov (ID 3) with two register operands returns
// the ORR-based encoding before the MRA's ADD-imm-#0 alias.  This is
// the load-bearing check that prevents byte-match regressions when
// `make enctab-regen-source` reintroduces the ADD-imm-#0 form.
func TestFormsForMnemonicMovIsORR(t *testing.T) {
	movID := uint16(3)
	forms := FormsForMnemonic(movID)
	if len(forms) == 0 {
		t.Fatal("FormsForMnemonic(mov) returned no forms")
	}
	// The first two-register form returned must be ORR-based
	// (pattern 0x2a0003e0 or 0xaa0003e0), not ADD-imm-#0
	// (0x11000000 or 0x91000000 with mask 0xfffffc00).
	for _, f := range forms {
		if len(f.Slots) == 2 &&
			f.Slots[0].SlotKind != XregOrSp && f.Slots[1].SlotKind != XregOrSp {
			// Plain (Wreg/Xreg, Wreg/Xreg) — first one of this shape wins.
			switch f.Pattern {
			case 0x2a0003e0, 0xaa0003e0:
				return // ORR form found first; OK.
			case 0x11000000, 0x91000000:
				t.Fatalf("mov 2-reg lookup returned ADD-imm-#0 alias (pattern 0x%08x) before ORR — byte-match would regress",
					f.Pattern)
			}
		}
	}
	t.Fatal("mov 2-reg lookup returned no plain-register form")
}
