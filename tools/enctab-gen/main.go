package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"

	"github.com/petemoore/sam-aarch64/tools/enctab-gen/emit"
	"github.com/petemoore/sam-aarch64/tools/enctab-gen/mra"
)

func main() {
	var mraDir, outEnc, outGo string
	flag.StringVar(&mraDir, "mra", "reference/arm-mra", "MRA snapshot dir")
	flag.StringVar(&outEnc, "out", "", "binary .enc output (optional)")
	flag.StringVar(&outGo, "gopkg", "tools/aarch64enc/data.go", "generated Go source output")
	flag.Parse()

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
		encOut, err := os.Create(outEnc)
		if err != nil {
			fail(err)
		}
		if err := emit.RenderEnc(encOut, allForms); err != nil {
			encOut.Close()
			fail(err)
		}
		encOut.Close()
		fmt.Printf("Wrote %s\n", outEnc)
	}
}

func convertSlots(raws []mra.RawOperandSlot, ctx mra.FieldContext) ([]enc.OperandSlot, error) {
	var out []enc.OperandSlot
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
		out = append(out, enc.OperandSlot{
			SlotKind:     kind,
			ExpectedKind: expectedKindForSlot(kind),
			BitPosition:  r.BitPosition,
			BitWidth:     r.BitWidth,
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

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
