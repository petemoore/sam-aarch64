package emit

import (
	"bytes"
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Emit reads .tbn bytes and returns canonically-formatted text.
func Emit(in []byte) ([]byte, error) {
	f, err := format.ReadFile(in)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	rr := format.NewRecordReader(f.Records)
	var prevWasStatement bool
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			return nil, err
		}
		switch rec.Kind {
		case format.KindLabelDef:
			if prevWasStatement {
				out.WriteByte('\n')
			}
			fmt.Fprintf(&out, "%s:", f.Names[rec.SymbolID])
			prevWasStatement = true
		case format.KindLocalDef:
			if prevWasStatement {
				out.WriteByte('\n')
			}
			fmt.Fprintf(&out, "%d:", rec.Digit)
			prevWasStatement = true
		case format.KindComment:
			if rec.Placement == 1 && prevWasStatement {
				out.WriteByte(' ')
				fmt.Fprintf(&out, "//%s", string(rec.Body))
				out.WriteByte('\n')
				prevWasStatement = false
				continue
			}
			if prevWasStatement {
				out.WriteByte('\n')
			}
			fmt.Fprintf(&out, "//%s\n", string(rec.Body))
			prevWasStatement = false
		case format.KindInst, format.KindDirective:
			if prevWasStatement {
				out.WriteByte('\n')
			}
			if err := emitStatement(&out, f, rec); err != nil {
				return nil, err
			}
			prevWasStatement = true
		default:
			fmt.Fprintf(&out, "// [skipped unknown record kind 0x%02x, %d bytes]\n",
				byte(rec.Kind), len(rec.Raw))
			prevWasStatement = false
		}
	}
	if prevWasStatement {
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func emitStatement(out *bytes.Buffer, f *format.File, rec format.Record) error {
	out.WriteString("  ")
	isDirective := false
	switch rec.Kind {
	case format.KindInst:
		name := format.MnemonicName(rec.MnemonicID)
		if name == "" {
			return fmt.Errorf("unknown mnemonic_id %d", rec.MnemonicID)
		}
		out.WriteString(name)
	case format.KindDirective:
		isDirective = true
		name := format.DirectiveName(rec.DirectiveID)
		if name == "" {
			return fmt.Errorf("unknown directive_id %d", rec.DirectiveID)
		}
		out.WriteString(name)
	}
	if rec.OperandCount == 0 {
		return nil
	}
	out.WriteByte(' ')
	or := format.NewOperandReader(rec.Operands)
	first := true
	for !or.AtEnd() {
		o, err := or.Next()
		if err != nil {
			return err
		}
		if !first {
			out.WriteString(", ")
		}
		first = false
		if err := emitOperandWithContext(out, f, o, isDirective); err != nil {
			return err
		}
	}
	return nil
}

func emitOperandWithContext(out *bytes.Buffer, f *format.File, o format.Operand, isDirective bool) error {
	switch o.Kind {
	case format.OpRegX:
		writeRegX(out, o.Reg)
	case format.OpRegW:
		writeRegW(out, o.Reg)
	case format.OpRegXSP:
		writeRegXSP(out, o.Reg)
	case format.OpRegWSP:
		writeRegWSP(out, o.Reg)
	case format.OpImmExpr:
		return emitExprAsImmediateWithContext(out, f, o.Expr, isDirective)
	case format.OpShiftedReg:
		if o.Width == 1 {
			writeRegX(out, o.Reg)
		} else {
			writeRegW(out, o.Reg)
		}
		fmt.Fprintf(out, ", %s ", o.ShiftKind.Name())
		return emitExprAsImmediateWithContext(out, f, o.AmtExpr, false)
	case format.OpExtendedReg:
		if o.Width == 1 {
			writeRegX(out, o.Reg)
		} else {
			writeRegW(out, o.Reg)
		}
		fmt.Fprintf(out, ", %s", o.Extend.Name())
		if len(o.AmtExpr) > 0 {
			out.WriteString(" ")
			return emitExprAsImmediateWithContext(out, f, o.AmtExpr, false)
		}
	case format.OpMem:
		return emitMem(out, f, o)
	case format.OpString:
		out.WriteByte('"')
		writeEscapedString(out, o.Str)
		out.WriteByte('"')
	case format.OpCond:
		out.WriteString(o.Cond.Name())
	case format.OpSysName:
		out.Write(o.Str)
	default:
		return fmt.Errorf("emitOperand: unsupported kind %v", o.Kind)
	}
	return nil
}

func writeRegX(out *bytes.Buffer, r byte) {
	switch r {
	case 29:
		out.WriteString("fp")
	case 30:
		out.WriteString("lr")
	case 31:
		out.WriteString("xzr")
	default:
		fmt.Fprintf(out, "x%d", r)
	}
}

func writeRegW(out *bytes.Buffer, r byte) {
	if r == 31 {
		out.WriteString("wzr")
		return
	}
	fmt.Fprintf(out, "w%d", r)
}

func writeRegXSP(out *bytes.Buffer, r byte) {
	if r == 31 {
		out.WriteString("sp")
		return
	}
	writeRegX(out, r)
}

func writeRegWSP(out *bytes.Buffer, r byte) {
	if r == 31 {
		out.WriteString("wsp")
		return
	}
	writeRegW(out, r)
}

func emitMem(out *bytes.Buffer, f *format.File, o format.Operand) error {
	out.WriteByte('[')
	writeRegXSP(out, o.Base)
	switch o.MemShape {
	case format.MemBase:
		out.WriteByte(']')
	case format.MemBaseOff:
		out.WriteString(", ")
		if err := emitExprAsImmediateWithContext(out, f, o.Expr, false); err != nil {
			return err
		}
		out.WriteByte(']')
	case format.MemBaseOffPre:
		out.WriteString(", ")
		if err := emitExprAsImmediateWithContext(out, f, o.Expr, false); err != nil {
			return err
		}
		out.WriteByte(']')
		out.WriteByte('!')
	case format.MemBaseOffPost:
		out.WriteByte(']')
		out.WriteString(", ")
		if err := emitExprAsImmediateWithContext(out, f, o.Expr, false); err != nil {
			return err
		}
	case format.MemBaseIdx:
		out.WriteString(", ")
		if o.IdxWidth == 1 {
			writeRegX(out, o.Idx)
		} else {
			writeRegW(out, o.Idx)
		}
		out.WriteByte(']')
	case format.MemBaseIdxShifted:
		out.WriteString(", ")
		writeRegX(out, o.Idx)
		fmt.Fprintf(out, ", lsl #%d]", o.ShiftAmt)
	case format.MemBaseIdxExtended:
		out.WriteString(", ")
		if o.IdxWidth == 1 {
			writeRegX(out, o.Idx)
		} else {
			writeRegW(out, o.Idx)
		}
		fmt.Fprintf(out, ", %s", o.Extend.Name())
		if o.ShiftAmt != 0 {
			fmt.Fprintf(out, " #%d", o.ShiftAmt)
		}
		out.WriteByte(']')
	}
	return nil
}

// emitExprAsImmediateWithContext prints an expression in immediate context.
// If isDirective is true, omit the leading '#' for simple immediates.
// If the expression is a single PUSH_SYM or PUSH_LOCAL or a REL_*
// chain whose root operand is a symbol, print without the '#'.
func emitExprAsImmediateWithContext(out *bytes.Buffer, f *format.File, expr []byte, isDirective bool) error {
	if v, ok := format.EvalConst(expr); ok {
		if isDirective {
			// In directive context (.byte, .word, etc.), no leading #
			if v >= 0 && v < 256 {
				fmt.Fprintf(out, "%d", v)
			} else if v < 0 && v > -256 {
				fmt.Fprintf(out, "%d", v)
			} else {
				fmt.Fprintf(out, "0x%x", v)
			}
		} else {
			// In instruction context, include leading #
			if v >= 0 && v < 256 {
				fmt.Fprintf(out, "#%d", v)
			} else if v < 0 && v > -256 {
				fmt.Fprintf(out, "#%d", v)
			} else {
				fmt.Fprintf(out, "#0x%x", v)
			}
		}
		return nil
	}
	if _, text, ok := simpleSymRef(expr, f); ok {
		out.WriteString(text)
		return nil
	}
	out.WriteByte('(')
	if err := printExpr(out, f, expr); err != nil {
		return err
	}
	out.WriteByte(')')
	return nil
}

// simpleSymRef recognises:
//   PUSH_SYM N                         → "<name>"
//   PUSH_SYM N ; REL_LO12              → ":lo12:<name>"
//   PUSH_LOCAL d, dir                  → "<d>f" or "<d>b"
func simpleSymRef(expr []byte, f *format.File) (bool, string, bool) {
	r := format.NewExprReader(expr)
	op, operand, err := r.Next()
	if err != nil {
		return false, "", false
	}
	switch op {
	case format.OpPushSym:
		id := uint16(operand[0]) | uint16(operand[1])<<8
		name := f.Names[id]
		if r.AtEnd() {
			return true, name, true
		}
		op2, _, _ := r.Next()
		if !r.AtEnd() {
			return false, "", false
		}
		switch op2 {
		case format.OpRelLo12:
			return true, ":lo12:" + name, true
		case format.OpRelHi12:
			return true, ":hi12:" + name, true
		}
	case format.OpPushLocal:
		d := operand[0]
		dir := byte('f')
		if operand[1] == 1 {
			dir = 'b'
		}
		if r.AtEnd() {
			return true, fmt.Sprintf("%d%c", d, dir), true
		}
	}
	return false, "", false
}

// printExpr renders an arbitrary bytecode stream as infix text.
func printExpr(out *bytes.Buffer, f *format.File, expr []byte) error {
	r := format.NewExprReader(expr)
	stack := make([]string, 0, 4)
	for !r.AtEnd() {
		op, operand, err := r.Next()
		if err != nil {
			return err
		}
		switch op {
		case format.OpPushImm8:
			stack = append(stack, fmt.Sprintf("%d", int8(operand[0])))
		case format.OpPushImm16:
			v := int16(uint16(operand[0]) | uint16(operand[1])<<8)
			stack = append(stack, fmt.Sprintf("%d", v))
		case format.OpPushImm32:
			v := int32(uint32(operand[0]) | uint32(operand[1])<<8 | uint32(operand[2])<<16 | uint32(operand[3])<<24)
			stack = append(stack, fmt.Sprintf("%d", v))
		case format.OpPushSym:
			id := uint16(operand[0]) | uint16(operand[1])<<8
			stack = append(stack, f.Names[id])
		case format.OpPushLocal:
			dir := 'f'
			if operand[1] == 1 {
				dir = 'b'
			}
			stack = append(stack, fmt.Sprintf("%d%c", operand[0], dir))
		case format.OpPushPC:
			stack = append(stack, ".")
		case format.OpAdd, format.OpSub, format.OpMul, format.OpDiv,
			format.OpAnd, format.OpOr, format.OpXor, format.OpShl, format.OpShr:
			if len(stack) < 2 {
				return fmt.Errorf("printExpr: stack underflow at %v", op)
			}
			b := stack[len(stack)-1]
			a := stack[len(stack)-2]
			stack = stack[:len(stack)-2]
			stack = append(stack, fmt.Sprintf("%s %s %s", a, opSym(op), b))
		case format.OpNeg:
			if len(stack) < 1 {
				return fmt.Errorf("printExpr: stack underflow at NEG")
			}
			stack[len(stack)-1] = "-" + stack[len(stack)-1]
		case format.OpNot:
			if len(stack) < 1 {
				return fmt.Errorf("printExpr: stack underflow at NOT")
			}
			stack[len(stack)-1] = "~" + stack[len(stack)-1]
		case format.OpRelLo12, format.OpRelHi12,
			format.OpRelAbsG0, format.OpRelAbsG0NC,
			format.OpRelAbsG1, format.OpRelAbsG1NC,
			format.OpRelAbsG2, format.OpRelAbsG2NC, format.OpRelAbsG3:
			if len(stack) < 1 {
				return fmt.Errorf("printExpr: stack underflow at %v", op)
			}
			stack[len(stack)-1] = ":" + relName(op) + ":" + stack[len(stack)-1]
		default:
			return fmt.Errorf("printExpr: unknown opcode %v", op)
		}
	}
	if len(stack) != 1 {
		return fmt.Errorf("printExpr: stack ended with %d values", len(stack))
	}
	out.WriteString(stack[0])
	return nil
}

func opSym(op format.ExprOp) string {
	switch op {
	case format.OpAdd:
		return "+"
	case format.OpSub:
		return "-"
	case format.OpMul:
		return "*"
	case format.OpDiv:
		return "/"
	case format.OpAnd:
		return "&"
	case format.OpOr:
		return "|"
	case format.OpXor:
		return "^"
	case format.OpShl:
		return "<<"
	case format.OpShr:
		return ">>"
	}
	return "?"
}

func relName(op format.ExprOp) string {
	switch op {
	case format.OpRelLo12:
		return "lo12"
	case format.OpRelHi12:
		return "hi12"
	case format.OpRelAbsG0:
		return "abs_g0"
	case format.OpRelAbsG0NC:
		return "abs_g0_nc"
	case format.OpRelAbsG1:
		return "abs_g1"
	case format.OpRelAbsG1NC:
		return "abs_g1_nc"
	case format.OpRelAbsG2:
		return "abs_g2"
	case format.OpRelAbsG2NC:
		return "abs_g2_nc"
	case format.OpRelAbsG3:
		return "abs_g3"
	}
	return "?"
}

func writeEscapedString(out *bytes.Buffer, body []byte) {
	for _, b := range body {
		switch b {
		case '\\':
			out.WriteString(`\\`)
		case '"':
			out.WriteString(`\"`)
		case '\n':
			out.WriteString(`\n`)
		case '\t':
			out.WriteString(`\t`)
		case 0:
			out.WriteString(`\0`)
		default:
			if b < 0x20 || b >= 0x7F {
				fmt.Fprintf(out, "\\x%02x", b)
			} else {
				out.WriteByte(b)
			}
		}
	}
}
