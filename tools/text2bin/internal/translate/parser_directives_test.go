package translate

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
