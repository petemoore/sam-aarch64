package format

// IsFullyLiteral reports whether an instruction record encodes to a
// fixed machine-code word independent of the symbol table — i.e. none
// of its operands reference a symbol, local label, the PC, a relocation
// operator, or the literal pool. Such instructions can be stored as
// their assembled bytes in a compact `.tbn` (KindLitInsts) and copied
// straight to OUT without re-encoding.
//
// This is a structural (operand/expression) check only. It does NOT by
// itself guarantee PC-independence: an instruction with a constant
// operand fed to a PC-relative form (e.g. `b 0x1000`) is structurally
// literal yet its encoding still varies with PC. The compaction pass
// adds a PC-invariance guard (encode at two PCs, require equal) on top
// of this predicate before collapsing — see tools/refenc/compact.go.
func IsFullyLiteral(rec Record) bool {
	if rec.Kind != KindInst {
		return false
	}
	or := NewOperandReader(rec.Operands)
	for !or.AtEnd() {
		o, err := or.Next()
		if err != nil {
			return false
		}
		switch o.Kind {
		case OpLitPool:
			// Literal-pool ref resolves to a PC-relative load of a
			// pool slot — depends on layout, never literal.
			return false
		case OpImmExpr, OpMem:
			if exprHasContextDep(o.Expr) {
				return false
			}
		case OpShiftedReg, OpExtendedReg:
			if exprHasContextDep(o.AmtExpr) {
				return false
			}
		case OpRegX, OpRegW, OpRegXSP, OpRegWSP, OpString, OpSysName, OpCond:
			// No expression, or context-independent payload.
		default:
			// Unknown operand kind: classify conservatively as not
			// literal so it falls back to the symbolic path.
			return false
		}
	}
	return true
}

// ConstDataWidth reports whether rec is a numeric data directive
// (.byte/.short/.hword/.word/.quad) whose every operand is a pure
// constant expression — i.e. a run-collapsible constant data record —
// and returns its per-element output width (1/2/4/8). ok=false for any
// other record, a data directive with a symbol/local/PC/reloc operand
// (must stay symbolic for relocation), or an empty operand list.
func ConstDataWidth(rec Record) (width int, ok bool) {
	if rec.Kind != KindDirective {
		return 0, false
	}
	switch DirectiveName(rec.DirectiveID) {
	case ".byte":
		width = 1
	case ".short", ".hword":
		width = 2
	case ".word":
		width = 4
	case ".quad":
		width = 8
	default:
		return 0, false
	}
	or := NewOperandReader(rec.Operands)
	n := 0
	for !or.AtEnd() {
		o, err := or.Next()
		if err != nil {
			return 0, false
		}
		n++
		if o.Kind != OpImmExpr {
			return 0, false
		}
		if _, isConst := EvalConst(o.Expr); !isConst {
			return 0, false
		}
	}
	if n == 0 {
		return 0, false
	}
	return width, true
}

// exprHasContextDep reports whether an expression bytecode stream
// contains any opcode whose value depends on the symbol table, a local
// label, the PC, or a relocation — anything that makes the enclosing
// instruction's encoding non-constant. A malformed stream is treated as
// context-dependent (conservative).
func exprHasContextDep(buf []byte) bool {
	r := NewExprReader(buf)
	for !r.AtEnd() {
		op, _, err := r.Next()
		if err != nil {
			return true
		}
		switch op {
		case OpPushSym, OpPushLocal, OpPushPC,
			OpRelLo12, OpRelHi12,
			OpRelAbsG0, OpRelAbsG0NC,
			OpRelAbsG1, OpRelAbsG1NC,
			OpRelAbsG2, OpRelAbsG2NC,
			OpRelAbsG3:
			return true
		}
	}
	return false
}
