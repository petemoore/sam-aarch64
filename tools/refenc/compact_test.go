package main

import (
	"bytes"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// buildMixedTBN constructs a .tbn with a mix of fully-literal
// instructions and one symbol-bearing branch, plus a label, so the
// compaction pass has both run-collapsible and pass-through records.
func buildMixedTBN(t *testing.T) *format.File {
	t.Helper()
	st := format.NewSymbolTable()
	startID := st.Intern("start")

	nopID, ok := format.MnemonicID("nop")
	if !ok {
		t.Fatal("nop not in mnemonic table")
	}
	bID, ok := format.MnemonicID("b")
	if !ok {
		t.Fatal("b not in mnemonic table")
	}

	var rw format.RecordWriter
	rw.WriteLabelDef(startID) // start:
	rw.WriteInst(nopID, 0, nil)
	rw.WriteInst(nopID, 0, nil)
	// b start — symbol-bearing, must stay symbolic.
	var ew format.ExprWriter
	ew.WriteSym(startID)
	var ow format.OperandWriter
	ow.WriteImmExpr(ew.Bytes())
	rw.WriteInst(bID, 1, ow.Bytes())
	rw.WriteInst(nopID, 0, nil)

	var buf bytes.Buffer
	if err := format.WriteFile(&buf, st, rw.Bytes()); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	f, err := format.ReadFile(buf.Bytes())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return f
}

func assemble(t *testing.T, f *format.File) []byte {
	t.Helper()
	p1, err := Pass1(f)
	if err != nil {
		t.Fatalf("Pass1: %v", err)
	}
	out, err := Pass2(f, p1)
	if err != nil {
		t.Fatalf("Pass2: %v", err)
	}
	return out
}

func TestCompact_RoundTripIdentical(t *testing.T) {
	f := buildMixedTBN(t)
	want := assemble(t, f)

	p1, err := Pass1(f)
	if err != nil {
		t.Fatalf("Pass1: %v", err)
	}
	compacted, err := Compact(f, p1)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	cf := &format.File{Version: f.Version, Flags: f.Flags, Names: f.Names, Records: compacted}
	got := assemble(t, cf)

	if !bytes.Equal(got, want) {
		t.Errorf("compact assembly differs:\n got % X\nwant % X", got, want)
	}
}

func TestCompact_CollapsesLiteralRun(t *testing.T) {
	f := buildMixedTBN(t)
	p1, err := Pass1(f)
	if err != nil {
		t.Fatalf("Pass1: %v", err)
	}
	compacted, err := Compact(f, p1)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	var litRuns, litInsts, symInsts int
	rr := format.NewRecordReader(compacted)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			t.Fatalf("reader: %v", err)
		}
		switch rec.Kind {
		case format.KindLitInsts:
			litRuns++
			litInsts += int(rec.LitCount)
		case format.KindInst:
			symInsts++
		}
	}
	// The two leading nops collapse into one run; the trailing nop is a
	// second run of one. The `b start` stays a symbolic INST.
	if litRuns != 2 {
		t.Errorf("lit runs = %d, want 2", litRuns)
	}
	if litInsts != 3 {
		t.Errorf("collapsed insts = %d, want 3 (three nops)", litInsts)
	}
	if symInsts != 1 {
		t.Errorf("symbolic insts = %d, want 1 (b start)", symInsts)
	}
}
