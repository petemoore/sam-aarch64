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

// immOps builds an operand stream of constant IMM_EXPR operands.
func immOps(vals ...int64) (byte, []byte) {
	var ow format.OperandWriter
	for _, v := range vals {
		var ew format.ExprWriter
		ew.WriteImm(v)
		ow.WriteImmExpr(ew.Bytes())
	}
	return byte(len(vals)), ow.Bytes()
}

// buildDataTBN has constant data runs (collapsible), a symbol-bearing
// .quad (must stay symbolic), and a literal inst, so Compact exercises
// both LIT_DATA and LIT_INSTS plus pass-through.
func buildDataTBN(t *testing.T) *format.File {
	t.Helper()
	st := format.NewSymbolTable()
	startID := st.Intern("start")
	nopID, _ := format.MnemonicID("nop")
	wordID, _ := format.DirectiveID(".word")
	quadID, _ := format.DirectiveID(".quad")
	byteID, _ := format.DirectiveID(".byte")

	var rw format.RecordWriter
	rw.WriteLabelDef(startID)
	n, ops := immOps(1, 2, 3)
	rw.WriteDirective(wordID, n, ops) // .word 1,2,3
	n, ops = immOps(4)
	rw.WriteDirective(wordID, n, ops) // .word 4  (same dir → merges)
	rw.WriteInst(nopID, 0, nil)       // nop (literal inst run)
	// .quad start — symbol-bearing, stays symbolic.
	var sew format.ExprWriter
	sew.WriteSym(startID)
	var sow format.OperandWriter
	sow.WriteImmExpr(sew.Bytes())
	rw.WriteDirective(quadID, 1, sow.Bytes())
	n, ops = immOps(0xaa, 0xbb)
	rw.WriteDirective(byteID, n, ops) // .byte 0xaa,0xbb (different dir → own run)

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

func TestCompact_DataRoundTripIdentical(t *testing.T) {
	f := buildDataTBN(t)
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
		t.Errorf("compact data assembly differs:\n got % X\nwant % X", got, want)
	}
}

func TestCompact_CollapsesDataRuns(t *testing.T) {
	f := buildDataTBN(t)
	p1, err := Pass1(f)
	if err != nil {
		t.Fatalf("Pass1: %v", err)
	}
	compacted, err := Compact(f, p1)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	wordID, _ := format.DirectiveID(".word")
	byteID, _ := format.DirectiveID(".byte")
	quadID, _ := format.DirectiveID(".quad")

	var litData, symDirs int
	var dirIDs []byte
	rr := format.NewRecordReader(compacted)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			t.Fatal(err)
		}
		switch rec.Kind {
		case format.KindLitData:
			litData++
			dirIDs = append(dirIDs, rec.LitDataDirID)
		case format.KindDirective:
			symDirs++
			if rec.DirectiveID != quadID {
				t.Errorf("unexpected symbolic directive id %d (want only the .quad)", rec.DirectiveID)
			}
		}
	}
	// The two .word records merge into one LIT_DATA; the .byte is a
	// second LIT_DATA (different directive); the .quad stays symbolic.
	if litData != 2 {
		t.Errorf("LIT_DATA records = %d, want 2", litData)
	}
	if symDirs != 1 {
		t.Errorf("symbolic DIRECTIVE records = %d, want 1 (.quad start)", symDirs)
	}
	if len(dirIDs) == 2 && !(dirIDs[0] == wordID && dirIDs[1] == byteID) {
		t.Errorf("LIT_DATA dir ids = %v, want [.word=%d, .byte=%d]", dirIDs, wordID, byteID)
	}
}
