package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"

	"github.com/petemoore/sam-aarch64/tools/tables-gen/emit"
	"github.com/petemoore/sam-aarch64/tools/tables-gen/mra"
)

func main() {
	var mraDir, outEnc, outGo, outSysregInc string
	flag.StringVar(&mraDir, "mra", "reference/arm-mra", "MRA snapshot dir")
	flag.StringVar(&outEnc, "out", "", "binary .enc output (optional)")
	flag.StringVar(&outGo, "gopkg", "", "regenerate the MRA-derived Go source at this path.  Only emits MRA-derived forms; hand-curated forms live in tools/aarch64enc/manual_forms.go and are not touched by this tool.")
	flag.StringVar(&outSysregInc, "sysreg-inc", "", "regenerate the Z80 sysreg/pstate/dc/tlbi tables (src/sysreg_tables.inc) from tools/sam-aarch64-format/sysregs.go.")
	flag.Parse()

	// The sysreg .inc is projected straight from the Go format package; it
	// needs no MRA parse, so handle it before the (slower) XML walk.
	if outSysregInc != "" {
		out, err := os.Create(outSysregInc)
		if err != nil {
			fail(err)
		}
		if err := emit.RenderSysregInc(out); err != nil {
			out.Close()
			fail(err)
		}
		out.Close()
		fmt.Printf("Wrote %s (sysreg/pstate/dc/tlbi tables from sysregs.go).\n", outSysregInc)
	}

	// The MRA walk only feeds the -out / -gopkg emitters. When only the
	// sysreg .inc was requested, skip it (and the MRA snapshot dependency).
	if outEnc == "" && outGo == "" {
		return
	}

	xmls, err := filepath.Glob(filepath.Join(mraDir, "*.xml"))
	if err != nil {
		fail(err)
	}
	var allForms []enc.Form
	for _, p := range xmls {
		base := filepath.Base(p)
		if base == "shared_pseudocode.xml" {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			fail(err)
		}
		parsed, err := mra.ParseInstructionXML(f)
		f.Close()
		if err != nil {
			fail(fmt.Errorf("%s: %w", p, err))
		}
		for _, pf := range parsed {
			mn := strings.ToLower(pf.Mnemonic)
			mnID, ok := format.MnemonicID(mn)
			if !ok {
				continue
			}
			ctx := mra.FieldContext{
				Is64: detectIs64(pf),
			}
			slots, err := convertSlots(pf.RawOperands, ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s (%s): skipping form: %v\n", p, pf.Mnemonic, err)
				continue
			}
			allForms = append(allForms, enc.Form{
				MnemonicID: mnID,
				Pattern:    pf.Pattern,
				Mask:       pf.Mask,
				Slots:      slots,
			})
		}
	}

	// Expand b.cond forms: the MRA represents all conditional branches as a
	// single B encoding with a CondCode slot (under the "B" mnemonic). The
	// text2bin format uses separate mnemonics b.eq, b.ne, b.lt etc. (IDs 27-42).
	// For each conditional-branch form found, emit one form per b.cc mnemonic
	// with the condition code baked into pattern/mask and the CondCode slot removed.
	allForms = expandBCondForms(allForms)

	if outGo != "" {
		out, err := os.Create(outGo)
		if err != nil {
			fail(err)
		}
		if err := emit.RenderGoPackage(out, allForms); err != nil {
			out.Close()
			fail(err)
		}
		out.Close()
		fmt.Printf("Wrote %s with %d forms.\n", outGo, len(allForms))
	}

	if outEnc != "" {
		// The Go library's runtime form table is the union of
		// manualForms (hand-curated) and the MRA-derived allForms.
		// The binary enctab.enc must mirror that union or M3's Z80
		// loader would be using a strict subset of the encodings the
		// Go side relies on.  Manual entries come first to match the
		// runtime lookup order — see manual_forms.go for why.
		combined := make([]enc.Form, 0, len(enc.ManualForms())+len(allForms))
		combined = append(combined, enc.ManualForms()...)
		combined = append(combined, allForms...)
		encOut, err := os.Create(outEnc)
		if err != nil {
			fail(err)
		}
		if err := emit.RenderEnc(encOut, combined); err != nil {
			encOut.Close()
			fail(err)
		}
		encOut.Close()
		fmt.Printf("Wrote %s (%d forms: %d manual + %d MRA-derived)\n",
			outEnc, len(combined), len(enc.ManualForms()), len(allForms))
	}
}

// slotSortOrder returns a sort key for a raw operand slot name so that
// the generated slot list matches assembler syntax order:
//
//	1. Destination registers (Rd, Rt, Rt2)
//	2. Source registers (Rn, Rm, Ra, Rs)
//	3. Immediates and everything else
//
// Within each group, slots are ordered by descending bit position (MSB
// of their field) so that ties fall in the same order the MRA regdiagram
// lists them (which is typically the correct operand order when two
// slots share the same group, e.g. Rn before Rm in a 3-register form).
func slotSortOrder(name string) int {
	switch name {
	case "Rd", "Rt", "Rt2":
		return 0
	case "Rn", "Rm", "Ra", "Rs":
		return 1
	default:
		return 2
	}
}

func convertSlots(raws []mra.RawOperandSlot, ctx mra.FieldContext) ([]enc.OperandSlot, error) {
	// Filter and map first.
	type rawMapped struct {
		raw  mra.RawOperandSlot
		kind enc.SlotKind
	}
	var mapped []rawMapped
	for _, r := range raws {
		// Internal fields are handled implicitly by their parent SlotKind encoder
		// (e.g. "sh" is encoded inside Imm12Shifted). Skip them as operand slots.
		if mra.IsInternalField(r.Name) {
			continue
		}
		kind, ok := mra.MapField(r.Name, ctx)
		if !ok {
			return nil, fmt.Errorf("unmapped MRA field %q", r.Name)
		}
		mapped = append(mapped, rawMapped{raw: r, kind: kind})
	}

	// Sort so that slot order matches assembler syntax: dest regs, source
	// regs, then immediates. Within a group, sort by descending bit position
	// (the regdiagram lists fields MSB→LSB, and ties within a group should
	// preserve that order since it is usually the assembler order too).
	sort.SliceStable(mapped, func(i, j int) bool {
		oi := slotSortOrder(mapped[i].raw.Name)
		oj := slotSortOrder(mapped[j].raw.Name)
		if oi != oj {
			return oi < oj
		}
		// Same group: preserve regdiagram order (higher bit positions come first
		// in the XML, which is the intended assembler order for that group).
		return mapped[i].raw.BitPosition > mapped[j].raw.BitPosition
	})

	var out []enc.OperandSlot
	for _, m := range mapped {
		out = append(out, enc.OperandSlot{
			SlotKind:     m.kind,
			ExpectedKind: expectedKindForSlot(m.kind),
			BitPosition:  m.raw.BitPosition,
			BitWidth:     m.raw.BitWidth,
		})
	}
	return out, nil
}

func expectedKindForSlot(k enc.SlotKind) format.OperandKind {
	switch k {
	case enc.Xreg:
		return format.OpRegX
	case enc.Wreg:
		return format.OpRegW
	case enc.XregOrSp:
		return format.OpRegXSP
	case enc.WregOrSp:
		return format.OpRegWSP
	case enc.CondCode:
		return format.OpCond
	default:
		return format.OpImmExpr
	}
}

func detectIs64(pf mra.ParsedForm) bool {
	return (pf.Pattern>>31)&1 == 1
}

// expandBCondForms finds conditional-branch forms (forms for mnemonic "b" that
// contain a CondCode slot) and expands them into 16 per-condition forms for
// the b.eq, b.ne, ..., b.nv mnemonics (condition codes 0-15). The expanded
// forms have the condition code baked into pattern/mask and no CondCode slot.
//
// It also emits alias forms for b.hs (=CS, cond=2) and b.lo (=CC, cond=3).
//
// The MRA represents all 16 conditional branches as a single encoding with a
// variable cond field; text2bin uses separate mnemonic IDs for each condition.
func expandBCondForms(forms []enc.Form) []enc.Form {
	bID, _ := format.MnemonicID("b")
	// b.eq is the first conditional mnemonic; we rely on it being the start
	// of a contiguous run of 16 entries in MnemonicTable.
	bEqID, ok := format.MnemonicID("b.eq")
	if !ok {
		return forms // b.eq not in table; nothing to expand
	}

	// Alias mnemonics: map condition-code value → additional mnemonic IDs
	// that share the same encoding. b.hs=CS=cond2, b.lo=CC=cond3.
	type condAlias struct {
		cond uint32
		id   uint16
	}
	var aliases []condAlias
	if id, ok := format.MnemonicID("b.hs"); ok {
		aliases = append(aliases, condAlias{2, id}) // CS = HS
	}
	if id, ok := format.MnemonicID("b.lo"); ok {
		aliases = append(aliases, condAlias{3, id}) // CC = LO
	}

	var out []enc.Form
	for _, f := range forms {
		if f.MnemonicID != bID || !hasCondCodeSlot(f) {
			out = append(out, f)
			continue
		}
		// Find the CondCode slot to determine its bit position.
		var condSlot enc.OperandSlot
		var otherSlots []enc.OperandSlot
		for _, s := range f.Slots {
			if s.SlotKind == enc.CondCode {
				condSlot = s
			} else {
				otherSlots = append(otherSlots, s)
			}
		}
		// Emit one form per condition code (0=EQ .. 15=NV).
		for cond := uint32(0); cond <= 15; cond++ {
			condBits := cond << uint32(condSlot.BitPosition)
			condMask := uint32((1<<condSlot.BitWidth)-1) << uint32(condSlot.BitPosition)
			out = append(out, enc.Form{
				MnemonicID: bEqID + uint16(cond),
				Pattern:    f.Pattern | condBits,
				Mask:       f.Mask | condMask,
				Slots:      otherSlots,
			})
			// Emit alias forms for any alias with this condition code.
			for _, a := range aliases {
				if a.cond == cond {
					out = append(out, enc.Form{
						MnemonicID: a.id,
						Pattern:    f.Pattern | condBits,
						Mask:       f.Mask | condMask,
						Slots:      otherSlots,
					})
				}
			}
		}
	}
	return out
}

func hasCondCodeSlot(f enc.Form) bool {
	for _, s := range f.Slots {
		if s.SlotKind == enc.CondCode {
			return true
		}
	}
	return false
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
