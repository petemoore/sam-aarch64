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

	// Header-table label/local definitions (compact `.tbn` v2). Inert for a
	// symbolic `.tbn` (empty tables); there the LABEL_DEF/LOCAL_DEF records
	// below render inline.
	hd := newHeaderDefs(f)
	// PC tracking exists solely to place header-table definitions. A symbolic
	// `.tbn` has none, so PC bookkeeping (which can fail on a non-constant
	// directive size like `.skip SYM`) is skipped entirely — labels/locals
	// render from their inline LABEL_DEF/LOCAL_DEF records instead.
	trackPC := !hd.empty()
	var pc int64        // running offset from origin
	var originVMA int64 // set by the leading injected `.org`

	// emitDef renders one header definition (a label name or a numeric local)
	// exactly as the inline LABEL_DEF/LOCAL_DEF records do.
	emitDef := func(d headerDef) {
		if prevWasStatement {
			out.WriteByte('\n')
		}
		if d.isLocal {
			fmt.Fprintf(&out, "%d:", d.digit)
		} else {
			fmt.Fprintf(&out, "%s:", d.name)
		}
		prevWasStatement = true
	}
	flush := func() { hd.flushAt(pc, emitDef) }

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
			// Comments occupy no PC; a label at this PC still belongs before
			// the next byte-emitting statement, so don't flush here.
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
		case format.KindInst:
			flush()
			if prevWasStatement {
				out.WriteByte('\n')
			}
			if err := emitStatement(&out, f, rec); err != nil {
				return nil, err
			}
			prevWasStatement = true
			pc += 4
		case format.KindDirective:
			// Flush header labels/locals before every directive EXCEPT .org.
			// A label at a directive's start PC belongs before it: `foo: .quad
			// 5` (data), or `end:` then `.align 4` (the label marks the
			// pre-pad PC). .org is the sole exception: it remaps PC↔VMA, so a
			// label coincident with the leading `.org` (e.g. `.org N` then
			// `_start:`) must render AFTER the .org — otherwise _start would
			// resolve to PC 0 instead of the origin VMA on re-assembly. A
			// label at the post-.org PC is flushed by the next byte-producer.
			if format.DirectiveName(rec.DirectiveID) != ".org" {
				flush()
			}
			if prevWasStatement {
				out.WriteByte('\n')
			}
			if err := emitStatement(&out, f, rec); err != nil {
				return nil, err
			}
			prevWasStatement = true
			// Advance PC by the directive's byte contribution, handling the
			// leading `.org` that establishes the origin VMA. Only needed when
			// tracking header-table definitions.
			if trackPC {
				if format.DirectiveName(rec.DirectiveID) == ".org" {
					or := format.NewOperandReader(rec.Operands)
					o, oerr := or.Next()
					if oerr != nil {
						return nil, oerr
					}
					target, ok := format.EvalConst(o.Expr)
					if !ok {
						return nil, fmt.Errorf(".org: non-constant target in compact stream")
					}
					if pc == 0 && originVMA == 0 {
						originVMA = target // first .org sets the origin
					} else {
						pc = target - originVMA // later .org advances within origin space
					}
				} else {
					n, derr := directiveByteSize(rec, pc, originVMA)
					if derr != nil {
						return nil, derr
					}
					pc += n
				}
			}
		case format.KindInsnRun:
			if err := emitInsnRun(&out, f, rec, &prevWasStatement, &pc, flush); err != nil {
				return nil, err
			}
		case format.KindLitInsts:
			emitLitInsts(&out, rec, &prevWasStatement, &pc, flush)
		case format.KindLitData:
			flush()
			if err := emitLitData(&out, rec, &prevWasStatement); err != nil {
				return nil, err
			}
			pc += int64(len(rec.LitData))
		default:
			fmt.Fprintf(&out, "// [skipped unknown record kind 0x%02x, %d bytes]\n",
				byte(rec.Kind), len(rec.Raw))
			prevWasStatement = false
		}
	}
	// Flush any definition at the final PC (an end-of-stream label).
	flush()
	if leftover := hd.remaining(); len(leftover) > 0 {
		return nil, fmt.Errorf("bin2text: %d header definition(s) not placed by PC walk (first at offset %#x) — PC accounting bug or corrupt header table",
			len(leftover), leftover[0].offset)
	}
	if prevWasStatement {
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func emitStatement(out *bytes.Buffer, f *format.File, rec format.Record) error {
	out.WriteString("  ")
	isDirective := false
	isBarrier := false
	switch rec.Kind {
	case format.KindInst:
		name := format.MnemonicName(rec.MnemonicID)
		if name == "" {
			return fmt.Errorf("unknown mnemonic_id %d", rec.MnemonicID)
		}
		out.WriteString(name)
		// isb / dsb / dmb single-operand form: print the barrier-arg
		// keyword (sy, st, ld, ish, ...) rather than the CRm immediate.
		if name == "isb" || name == "dsb" || name == "dmb" {
			isBarrier = true
		}
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
	if isBarrier {
		// Barrier mnemonics carry exactly one OpImmExpr operand (the CRm
		// value). Emit the keyword form.
		or := format.NewOperandReader(rec.Operands)
		o, err := or.Next()
		if err != nil {
			return err
		}
		if o.Kind != format.OpImmExpr {
			return fmt.Errorf("barrier mnemonic operand kind %v", o.Kind)
		}
		v, ok := format.EvalConst(o.Expr)
		if !ok {
			return fmt.Errorf("barrier mnemonic with non-constant CRm")
		}
		name := barrierName(byte(v))
		if name == "" {
			return fmt.Errorf("barrier mnemonic with unknown CRm %d", v)
		}
		out.WriteByte(' ')
		out.WriteString(name)
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

// barrierName is the inverse of parser.barrierCRm — it maps a CRm value
// (bits 11:8 of dsb/dmb/isb encodings) to the canonical barrier-arg
// keyword per ARM ARM C6.2.74 / C6.2.73 / C6.2.99.
func barrierName(crm byte) string {
	switch crm {
	case 0xf:
		return "sy"
	case 0xe:
		return "st"
	case 0xd:
		return "ld"
	case 0xb:
		return "ish"
	case 0xa:
		return "ishst"
	case 0x9:
		return "ishld"
	case 0x7:
		return "nsh"
	case 0x6:
		return "nshst"
	case 0x5:
		return "nshld"
	case 0x3:
		return "osh"
	case 0x2:
		return "oshst"
	case 0x1:
		return "oshld"
	}
	return ""
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
	case format.OpLitPool:
		// `ldr Xn, =expr` — emit the value expression without a
		// leading '#'. GNU as accepts both forms (the parser strips
		// `#` in primary-expression context), but the canonical
		// spelling has no `#` after `=`.
		out.WriteByte('=')
		return emitExprAsImmediateWithContext(out, f, o.Expr, true)
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
				// %#x places the sign before the 0x (a plain "0x%x" of a
				// negative renders the malformed "0x-…").
				fmt.Fprintf(out, "%#x", v)
			}
		} else {
			// In instruction context, include leading #
			if v >= 0 && v < 256 {
				fmt.Fprintf(out, "#%d", v)
			} else if v < 0 && v > -256 {
				fmt.Fprintf(out, "#%d", v)
			} else {
				fmt.Fprintf(out, "#%#x", v)
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
//
//	PUSH_SYM N                         → "<name>"
//	PUSH_SYM N ; REL_LO12              → ":lo12:<name>"
//	PUSH_LOCAL d, dir                  → "<d>f" or "<d>b"
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

// exprTerm is one operand on printExpr's evaluation stack: its rendered text
// and whether it is "atomic" — a primitive (number/symbol/local/PC/`:reloc:`)
// or already parenthesised — and so safe to drop into a larger expression
// without parentheses. A bare binary expression is non-atomic and must be
// wrapped when it becomes an operand, preserving the bytecode's grouping
// independent of the re-parser's operator precedence.
type exprTerm struct {
	text string
	atom bool
}

// asOperand renders a term for use inside a larger expression, parenthesising
// it if it is a bare binary/unary expression.
func (t exprTerm) asOperand() string {
	if t.atom {
		return t.text
	}
	return "(" + t.text + ")"
}

// printExpr renders an arbitrary bytecode stream as infix text. Sub-expressions
// are parenthesised so the rendered text re-parses to the identical value
// regardless of the parser's precedence rules; the top-level result is left
// unparenthesised (its caller adds the single enclosing pair).
func printExpr(out *bytes.Buffer, f *format.File, expr []byte) error {
	r := format.NewExprReader(expr)
	stack := make([]exprTerm, 0, 4)
	push := func(s string, atom bool) { stack = append(stack, exprTerm{s, atom}) }
	for !r.AtEnd() {
		op, operand, err := r.Next()
		if err != nil {
			return err
		}
		switch op {
		case format.OpPushImm8:
			push(fmt.Sprintf("%d", int8(operand[0])), true)
		case format.OpPushImm16:
			push(fmt.Sprintf("%d", int16(uint16(operand[0])|uint16(operand[1])<<8)), true)
		case format.OpPushImm32:
			v := int32(uint32(operand[0]) | uint32(operand[1])<<8 | uint32(operand[2])<<16 | uint32(operand[3])<<24)
			push(fmt.Sprintf("%d", v), true)
		case format.OpPushImm64:
			var v uint64
			for i := 0; i < 8; i++ {
				v |= uint64(operand[i]) << (8 * i)
			}
			push(fmt.Sprintf("%d", int64(v)), true)
		case format.OpPushSym:
			id := uint16(operand[0]) | uint16(operand[1])<<8
			push(f.Names[id], true)
		case format.OpPushLocal:
			dir := 'f'
			if operand[1] == 1 {
				dir = 'b'
			}
			push(fmt.Sprintf("%d%c", operand[0], dir), true)
		case format.OpPushPC:
			push(".", true)
		case format.OpAdd, format.OpSub, format.OpMul, format.OpDiv,
			format.OpAnd, format.OpOr, format.OpXor, format.OpShl, format.OpShr:
			if len(stack) < 2 {
				return fmt.Errorf("printExpr: stack underflow at %v", op)
			}
			b := stack[len(stack)-1]
			a := stack[len(stack)-2]
			stack = stack[:len(stack)-2]
			push(a.asOperand()+" "+opSym(op)+" "+b.asOperand(), false)
		case format.OpNeg:
			if len(stack) < 1 {
				return fmt.Errorf("printExpr: stack underflow at NEG")
			}
			t := stack[len(stack)-1]
			stack[len(stack)-1] = exprTerm{"-" + t.asOperand(), false}
		case format.OpNot:
			if len(stack) < 1 {
				return fmt.Errorf("printExpr: stack underflow at NOT")
			}
			t := stack[len(stack)-1]
			stack[len(stack)-1] = exprTerm{"~" + t.asOperand(), false}
		case format.OpRelLo12, format.OpRelHi12,
			format.OpRelAbsG0, format.OpRelAbsG0NC,
			format.OpRelAbsG1, format.OpRelAbsG1NC,
			format.OpRelAbsG2, format.OpRelAbsG2NC, format.OpRelAbsG3:
			if len(stack) < 1 {
				return fmt.Errorf("printExpr: stack underflow at %v", op)
			}
			t := stack[len(stack)-1]
			stack[len(stack)-1] = exprTerm{":" + relName(op) + ":" + t.asOperand(), true}
		default:
			return fmt.Errorf("printExpr: unknown opcode %v", op)
		}
	}
	if len(stack) != 1 {
		return fmt.Errorf("printExpr: stack ended with %d values", len(stack))
	}
	out.WriteString(stack[0].text)
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
