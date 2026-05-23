package format

import "testing"

func TestExprEvalConstFold(t *testing.T) {
	var w ExprWriter
	w.WriteImm(7)
	w.WriteImm(3)
	w.WriteOp(OpAdd)
	v, ok := EvalConst(w.Bytes())
	if !ok || v != 10 {
		t.Errorf("EvalConst(7+3) = (%d,%v), want (10,true)", v, ok)
	}
}

func TestExprEvalNotConst(t *testing.T) {
	var w ExprWriter
	w.WriteSym(0)
	w.WriteImm(1)
	w.WriteOp(OpAdd)
	if _, ok := EvalConst(w.Bytes()); ok {
		t.Errorf("EvalConst with PUSH_SYM should return ok=false")
	}
}

func TestExprEvalUnary(t *testing.T) {
	var w ExprWriter
	w.WriteImm(5)
	w.WriteOp(OpNeg)
	v, ok := EvalConst(w.Bytes())
	if !ok || v != -5 {
		t.Errorf("EvalConst(-5) = (%d,%v), want (-5,true)", v, ok)
	}
}

func TestExprEvalShift(t *testing.T) {
	var w ExprWriter
	w.WriteImm(1)
	w.WriteImm(8)
	w.WriteOp(OpShl)
	v, ok := EvalConst(w.Bytes())
	if !ok || v != 256 {
		t.Errorf("EvalConst(1<<8) = (%d,%v), want (256,true)", v, ok)
	}
}

func TestExprIterateOpcodes(t *testing.T) {
	var w ExprWriter
	w.WriteSym(0x1234)
	w.WriteImm(7)
	w.WriteOp(OpSub)
	r := NewExprReader(w.Bytes())
	op, _, err := r.Next()
	if err != nil || op != OpPushSym {
		t.Fatalf("first op = %v err=%v, want OpPushSym", op, err)
	}
	op, _, err = r.Next()
	if err != nil || op != OpPushImm8 {
		t.Fatalf("second op = %v, want OpPushImm8", op)
	}
	op, _, err = r.Next()
	if err != nil || op != OpSub {
		t.Fatalf("third op = %v, want OpSub", op)
	}
	if !r.AtEnd() {
		t.Errorf("reader not at end after 3 ops")
	}
}
