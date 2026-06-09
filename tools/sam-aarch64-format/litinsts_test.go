package format

import (
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

// instRecord builds an in-memory INST record directly (the form the
// front-end produces), so IsFullyLiteral can be tested against the same
// shape the assembler sees.
func instRecord(mnemonicID uint16, operandCount byte, operands []byte) Record {
	return Record{Kind: KindInst, MnemonicID: mnemonicID, OperandCount: operandCount, Operands: operands}
}

func TestIsFullyLiteral_RegisterOnly(t *testing.T) {
	// add x0, x1, x2 — three plain registers, no expressions.
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteReg(OpRegX, 1)
	ow.WriteReg(OpRegX, 2)
	if !IsFullyLiteral(instRecord(1, 3, ow.Bytes())) {
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
	if !IsFullyLiteral(instRecord(40, 2, ow.Bytes())) {
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
	if IsFullyLiteral(instRecord(40, 2, ow.Bytes())) {
		t.Errorf("symbol-ref inst: IsFullyLiteral = true, want false")
	}
}

func TestIsFullyLiteral_LocalRef(t *testing.T) {
	var ew ExprWriter
	ew.WriteLocal(1, 0)
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteImmExpr(ew.Bytes())
	if IsFullyLiteral(instRecord(40, 2, ow.Bytes())) {
		t.Errorf("local-ref inst: IsFullyLiteral = true, want false")
	}
}

func TestIsFullyLiteral_PCRef(t *testing.T) {
	var ew ExprWriter
	ew.WritePC()
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteImmExpr(ew.Bytes())
	if IsFullyLiteral(instRecord(40, 2, ow.Bytes())) {
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
	if IsFullyLiteral(instRecord(40, 2, ow.Bytes())) {
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
	if IsFullyLiteral(instRecord(5, 2, ow.Bytes())) {
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
	if !IsFullyLiteral(instRecord(5, 2, ow.Bytes())) {
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
	if IsFullyLiteral(instRecord(5, 2, ow.Bytes())) {
		t.Errorf("symbol-offset mem inst: IsFullyLiteral = true, want false")
	}
}

func TestIsFullyLiteral_NotAnInst(t *testing.T) {
	if IsFullyLiteral(Record{Kind: KindLabelDef}) {
		t.Errorf("label-def record: IsFullyLiteral = true, want false")
	}
}
