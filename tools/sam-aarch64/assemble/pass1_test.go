package assemble

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestPass1_PCAssignment(t *testing.T) {
	st := format.NewSymbolTable()
	mainID := st.Intern("main")
	nopID, _ := format.MnemonicID("nop")
	f := fileFromRecords(st.Names(), []format.Record{
		labelRec(mainID),
		instRec(nopID, 0, nil),
		instRec(nopID, 0, nil),
	})

	res, err := Pass1(f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Symbols["main"] != 0 {
		t.Errorf("main = %x, want 0", res.Symbols["main"])
	}
	if res.TotalSize != 8 {
		t.Errorf("total size = %d, want 8", res.TotalSize)
	}
}

func TestPass1_DirectiveBytes(t *testing.T) {
	var ow format.OperandWriter
	for _, v := range []int64{1, 2, 3} {
		var ew format.ExprWriter
		ew.WriteImm(v)
		ow.WriteImmExpr(ew.Bytes())
	}
	id, _ := format.DirectiveID(".byte")
	f := fileFromRecords(nil, []format.Record{dirRec(id, 3, ow.Bytes())})

	res, err := Pass1(f)
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalSize != 3 {
		t.Errorf("total size for .byte 1,2,3 = %d, want 3", res.TotalSize)
	}
}

// equRec builds a `.equ`/`.set NAME, value` DIRECTIVE record: operand 1 is the
// symbol-ref expr (PUSH_SYM nameID), operand 2 is the constant value.
func equRec(t *testing.T, directive string, nameID uint16, value int64) format.Record {
	t.Helper()
	dirID, ok := format.DirectiveID(directive)
	if !ok {
		t.Fatalf("unknown directive %q", directive)
	}
	var ow format.OperandWriter
	var sym format.ExprWriter
	sym.WriteSym(nameID)
	ow.WriteImmExpr(sym.Bytes())
	var val format.ExprWriter
	val.WriteImm(value)
	ow.WriteImmExpr(val.Bytes())
	return dirRec(dirID, 2, ow.Bytes())
}

// TestPass1_EquRedefinitionHardFails asserts that redefining a symbol with
// .set/.equ is a HARD FAIL — a deliberate divergence from GNU as (which
// silently overwrites), matching the Z80 assembler's symbol_insert jp-fail
// (i73/q49; documented in docs/ARCHITECTURE.md §3). .set and .equ are synonyms,
// so every ordering of the two rejects the redefinition.
func TestPass1_EquRedefinitionHardFails(t *testing.T) {
	for _, tc := range []struct{ first, second string }{
		{".equ", ".equ"},
		{".set", ".set"},
		{".equ", ".set"},
		{".set", ".equ"},
	} {
		t.Run(tc.first[1:]+"_then_"+tc.second[1:], func(t *testing.T) {
			f := fileFromRecords([]string{"FOO"}, []format.Record{
				equRec(t, tc.first, 0, 1),
				equRec(t, tc.second, 0, 2),
			})
			if _, err := Pass1(f); err == nil {
				t.Fatalf("Pass1 accepted %s then %s redefinition of FOO; want a hard-fail error", tc.first, tc.second)
			}
		})
	}
}

// TestPass1_EquDistinctNamesOK is the negative control: distinct .equ/.set
// names still resolve (the hard-fail must not fire on non-redefinitions).
func TestPass1_EquDistinctNamesOK(t *testing.T) {
	f := fileFromRecords([]string{"FOO", "BAR"}, []format.Record{
		equRec(t, ".equ", 0, 0x10),
		equRec(t, ".set", 1, 0x20),
	})
	res, err := Pass1(f)
	if err != nil {
		t.Fatalf("Pass1 rejected distinct .equ/.set names: %v", err)
	}
	if res.Symbols["FOO"] != 0x10 || res.Symbols["BAR"] != 0x20 {
		t.Errorf("symbols = FOO:%#x BAR:%#x, want FOO:0x10 BAR:0x20", res.Symbols["FOO"], res.Symbols["BAR"])
	}
}

func TestPass1_LocalLabels(t *testing.T) {
	nopID, _ := format.MnemonicID("nop")
	f := fileFromRecords(nil, []format.Record{
		instRec(nopID, 0, nil),
		localRec(1),
		instRec(nopID, 0, nil),
		localRec(1),
	})

	res, err := Pass1(f)
	if err != nil {
		t.Fatal(err)
	}
	pcs := res.LocalDefs[1]
	if len(pcs) != 2 {
		t.Fatalf("expected 2 local defs for digit 1, got %d", len(pcs))
	}
	if pcs[0] != 4 || pcs[1] != 8 {
		t.Errorf("local PCs = %v, want [4, 8]", pcs)
	}
}
