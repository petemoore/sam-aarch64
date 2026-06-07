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
