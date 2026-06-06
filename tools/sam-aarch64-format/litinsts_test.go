package format

import (
	"encoding/binary"
	"testing"
)

func TestKindLitInstsValue(t *testing.T) {
	if byte(KindLitInsts) != 0x07 {
		t.Errorf("KindLitInsts = 0x%02x, want 0x07", byte(KindLitInsts))
	}
	if KindLitInsts.Name() != "LIT_INSTS" {
		t.Errorf("KindLitInsts.Name() = %q, want %q", KindLitInsts.Name(), "LIT_INSTS")
	}
	if !KindLitInsts.IsKnown() {
		t.Errorf("KindLitInsts.IsKnown() = false, want true")
	}
}

func TestLitInstsRoundTrip(t *testing.T) {
	words := []uint32{0xd503201f, 0xd65f03c0, 0x910003fd} // nop, ret, mov x29,sp
	var rw RecordWriter
	rw.WriteLitInsts(words)

	rr := NewRecordReader(rw.Bytes())
	rec, err := rr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if rec.Kind != KindLitInsts {
		t.Fatalf("Kind = %s, want LIT_INSTS", rec.Kind.Name())
	}
	if int(rec.LitCount) != len(words) {
		t.Fatalf("LitCount = %d, want %d", rec.LitCount, len(words))
	}
	if len(rec.LitWords) != 4*len(words) {
		t.Fatalf("LitWords len = %d, want %d", len(rec.LitWords), 4*len(words))
	}
	for i, want := range words {
		got := binary.LittleEndian.Uint32(rec.LitWords[4*i:])
		if got != want {
			t.Errorf("word %d = 0x%08x, want 0x%08x", i, got, want)
		}
	}
	if !rr.AtEnd() {
		t.Errorf("reader not at end after single record")
	}
}

// readSingleRecord serialises one record via the writer and reads it
// back as a decoded Record, so IsFullyLiteral can be tested against the
// same shape the reader produces in refenc.
func readSingleRecord(t *testing.T, rw *RecordWriter) Record {
	t.Helper()
	rr := NewRecordReader(rw.Bytes())
	rec, err := rr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return rec
}

func TestIsFullyLiteral_RegisterOnly(t *testing.T) {
	// add x0, x1, x2 — three plain registers, no expressions.
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteReg(OpRegX, 1)
	ow.WriteReg(OpRegX, 2)
	var rw RecordWriter
	rw.WriteInst(1, 3, ow.Bytes())
	if !IsFullyLiteral(readSingleRecord(t, &rw)) {
		t.Errorf("register-only inst: IsFullyLiteral = false, want true")
	}
}

func TestIsFullyLiteral_ConstImm(t *testing.T) {
	// movz x0, #5 — an immediate that is a pure constant.
	var ew ExprWriter
	ew.WriteImm(5)
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteImmExpr(ew.Bytes())
	var rw RecordWriter
	rw.WriteInst(40, 2, ow.Bytes())
	if !IsFullyLiteral(readSingleRecord(t, &rw)) {
		t.Errorf("const-immediate inst: IsFullyLiteral = false, want true")
	}
}

func TestIsFullyLiteral_SymbolRef(t *testing.T) {
	// An immediate that references a symbol depends on the symbol table.
	var ew ExprWriter
	ew.WriteSym(0)
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteImmExpr(ew.Bytes())
	var rw RecordWriter
	rw.WriteInst(40, 2, ow.Bytes())
	if IsFullyLiteral(readSingleRecord(t, &rw)) {
		t.Errorf("symbol-ref inst: IsFullyLiteral = true, want false")
	}
}

func TestIsFullyLiteral_LocalRef(t *testing.T) {
	var ew ExprWriter
	ew.WriteLocal(1, 0)
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteImmExpr(ew.Bytes())
	var rw RecordWriter
	rw.WriteInst(40, 2, ow.Bytes())
	if IsFullyLiteral(readSingleRecord(t, &rw)) {
		t.Errorf("local-ref inst: IsFullyLiteral = true, want false")
	}
}

func TestIsFullyLiteral_PCRef(t *testing.T) {
	var ew ExprWriter
	ew.WritePC()
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteImmExpr(ew.Bytes())
	var rw RecordWriter
	rw.WriteInst(40, 2, ow.Bytes())
	if IsFullyLiteral(readSingleRecord(t, &rw)) {
		t.Errorf("PC-ref inst: IsFullyLiteral = true, want false")
	}
}

func TestIsFullyLiteral_Reloc(t *testing.T) {
	// :lo12:sym — a relocation operator over a symbol push.
	var ew ExprWriter
	ew.WriteSym(0)
	ew.WriteOp(OpRelLo12)
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteImmExpr(ew.Bytes())
	var rw RecordWriter
	rw.WriteInst(40, 2, ow.Bytes())
	if IsFullyLiteral(readSingleRecord(t, &rw)) {
		t.Errorf("reloc inst: IsFullyLiteral = true, want false")
	}
}

func TestIsFullyLiteral_LitPool(t *testing.T) {
	// ldr x0, =0x1234 — literal-pool ref is PC-dependent (pool slot).
	var ew ExprWriter
	ew.WriteImm(0x1234)
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteLitPool(8, ew.Bytes())
	var rw RecordWriter
	rw.WriteInst(5, 2, ow.Bytes())
	if IsFullyLiteral(readSingleRecord(t, &rw)) {
		t.Errorf("litpool inst: IsFullyLiteral = true, want false")
	}
}

func TestIsFullyLiteral_MemConstOffset(t *testing.T) {
	// ldr x0, [x1, #8] — constant memory offset is literal.
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	var off ExprWriter
	off.WriteImm(8)
	ow.WriteMemBaseOff(MemBaseOff, 1, off.Bytes())
	var rw RecordWriter
	rw.WriteInst(5, 2, ow.Bytes())
	if !IsFullyLiteral(readSingleRecord(t, &rw)) {
		t.Errorf("const-offset mem inst: IsFullyLiteral = false, want true")
	}
}

func TestIsFullyLiteral_MemSymbolOffset(t *testing.T) {
	// ldr x0, [x1, #:lo12:sym] — symbol-bearing memory offset.
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	var off ExprWriter
	off.WriteSym(0)
	off.WriteOp(OpRelLo12)
	ow.WriteMemBaseOff(MemBaseOff, 1, off.Bytes())
	var rw RecordWriter
	rw.WriteInst(5, 2, ow.Bytes())
	if IsFullyLiteral(readSingleRecord(t, &rw)) {
		t.Errorf("symbol-offset mem inst: IsFullyLiteral = true, want false")
	}
}

func TestIsFullyLiteral_NotAnInst(t *testing.T) {
	var rw RecordWriter
	rw.WriteLabelDef(0)
	if IsFullyLiteral(readSingleRecord(t, &rw)) {
		t.Errorf("label-def record: IsFullyLiteral = true, want false")
	}
}
