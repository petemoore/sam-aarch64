package frontend

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestParseDirectiveByte(t *testing.T) {
	f := parseHelper(t, ".byte 1, 2, 3\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindDirective {
		t.Fatalf("kind = %v", rec.Kind)
	}
	id, _ := format.DirectiveID(".byte")
	if rec.DirectiveID != id || rec.OperandCount != 3 {
		t.Errorf("rec = %+v", rec)
	}
}

func TestParseDirectiveHword(t *testing.T) {
	f := parseHelper(t, ".hword 0x1234, 0x5678, 0xabcd\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindDirective {
		t.Fatalf("kind = %v", rec.Kind)
	}
	id, _ := format.DirectiveID(".hword")
	if rec.DirectiveID != id || rec.OperandCount != 3 {
		t.Errorf("rec = %+v", rec)
	}
}

func TestParseDirectiveAscii(t *testing.T) {
	f := parseHelper(t, ".ascii \"hi\"\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpString || string(o.Str) != "hi" {
		t.Errorf("op0 = %+v", o)
	}
}

func TestParseBCondOperand(t *testing.T) {
	f := parseHelper(t, "b.eq target\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	id, _ := format.MnemonicID("b.eq")
	if rec.MnemonicID != id {
		t.Errorf("mnemonic_id = %d, want %d", rec.MnemonicID, id)
	}
}

func TestParseCselWithCond(t *testing.T) {
	f := parseHelper(t, "csel x0, x1, x2, ne\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	or.Next()
	or.Next()
	or.Next()
	o, _ := or.Next()
	if o.Kind != format.OpCond || o.Cond != format.CondNE {
		t.Errorf("op3 = %+v", o)
	}
}

// TestParseDirectiveSectionFull covers the three-operand form:
//
//	.section bss_kernel, "aw", %nobits
//
// The directive must parse to KindDirective with directive_id = .section
// and three operands: section name (OpSysName), flags (OpString),
// type keyword (OpSysName) — with the leading '%' preserved in the
// stored text so bin2text can round-trip it.
func TestParseDirectiveSectionFull(t *testing.T) {
	f := parseHelper(t, ".section bss_kernel, \"aw\", %nobits\n")
	r := format.NewRecordReader(f.Records)
	rec, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Kind != format.KindDirective {
		t.Fatalf("kind = %v", rec.Kind)
	}
	wantID, _ := format.DirectiveID(".section")
	if rec.DirectiveID != wantID {
		t.Errorf("directive_id = %d, want %d", rec.DirectiveID, wantID)
	}
	if rec.OperandCount != 3 {
		t.Fatalf("operand_count = %d, want 3", rec.OperandCount)
	}
	or := format.NewOperandReader(rec.Operands)
	o0, _ := or.Next()
	if o0.Kind != format.OpSysName || string(o0.Str) != "bss_kernel" {
		t.Errorf("op0 = %+v, want OpSysName bss_kernel", o0)
	}
	o1, _ := or.Next()
	if o1.Kind != format.OpString || string(o1.Str) != "aw" {
		t.Errorf("op1 = %+v, want OpString \"aw\"", o1)
	}
	o2, _ := or.Next()
	if o2.Kind != format.OpSysName || string(o2.Str) != "%nobits" {
		t.Errorf("op2 = %+v, want OpSysName %%nobits", o2)
	}
}

// TestParseDirectiveSectionFlagsOnly covers the two-operand form:
//
//	.section text_tests, "ax"
func TestParseDirectiveSectionFlagsOnly(t *testing.T) {
	f := parseHelper(t, ".section text_tests, \"ax\"\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindDirective || rec.OperandCount != 2 {
		t.Fatalf("rec = %+v", rec)
	}
	or := format.NewOperandReader(rec.Operands)
	o0, _ := or.Next()
	if o0.Kind != format.OpSysName || string(o0.Str) != "text_tests" {
		t.Errorf("op0 = %+v", o0)
	}
	o1, _ := or.Next()
	if o1.Kind != format.OpString || string(o1.Str) != "ax" {
		t.Errorf("op1 = %+v", o1)
	}
}

// TestParseDirectiveSectionBareName covers the permissive single-operand form:
//
//	.section .rodata
func TestParseDirectiveSectionBareName(t *testing.T) {
	f := parseHelper(t, ".section .rodata\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindDirective || rec.OperandCount != 1 {
		t.Fatalf("rec = %+v", rec)
	}
	or := format.NewOperandReader(rec.Operands)
	o0, _ := or.Next()
	if o0.Kind != format.OpSysName || string(o0.Str) != ".rodata" {
		t.Errorf("op0 = %+v", o0)
	}
}

// TestParseDirectiveArch covers `.arch armv8-a` — a no-op directive whose
// argument is an opaque architecture name. The lexer splits `armv8-a` into
// three tokens (TokIdent, TokMinus, TokIdent), and the parser must reassemble
// them into a single OpSysName "armv8-a".
func TestParseDirectiveArch(t *testing.T) {
	f := parseHelper(t, ".arch armv8-a\n")
	r := format.NewRecordReader(f.Records)
	rec, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Kind != format.KindDirective {
		t.Fatalf("kind = %v", rec.Kind)
	}
	wantID, _ := format.DirectiveID(".arch")
	if rec.DirectiveID != wantID {
		t.Errorf("directive_id = %d, want %d", rec.DirectiveID, wantID)
	}
	if rec.OperandCount != 1 {
		t.Fatalf("operand_count = %d, want 1", rec.OperandCount)
	}
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpSysName || string(o.Str) != "armv8-a" {
		t.Errorf("op0 = %+v, want OpSysName armv8-a", o)
	}
}

// TestParseDirectiveCpu covers `.cpu cortex-a53` — same shape as .arch.
func TestParseDirectiveCpu(t *testing.T) {
	f := parseHelper(t, ".cpu cortex-a53\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	wantID, _ := format.DirectiveID(".cpu")
	if rec.DirectiveID != wantID || rec.OperandCount != 1 {
		t.Fatalf("rec = %+v", rec)
	}
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpSysName || string(o.Str) != "cortex-a53" {
		t.Errorf("op0 = %+v, want OpSysName cortex-a53", o)
	}
}

func TestParseLo12(t *testing.T) {
	f := parseHelper(t, "add x0, x1, :lo12:msg\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	or.Next()
	or.Next()
	o, _ := or.Next()
	if o.Kind != format.OpImmExpr {
		t.Errorf("op2 kind = %v", o.Kind)
	}
	er := format.NewExprReader(o.Expr)
	op, _, _ := er.Next()
	if op != format.OpPushSym {
		t.Errorf("first op = %v", op)
	}
	op, _, _ = er.Next()
	if op != format.OpRelLo12 {
		t.Errorf("second op = %v", op)
	}
}
