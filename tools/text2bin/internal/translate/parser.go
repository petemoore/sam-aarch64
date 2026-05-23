package translate

import (
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

type parser struct {
	toks []Tok
	pos  int
	st   *format.SymbolTable
	rw   format.RecordWriter
}

// Parse turns a token stream into a record stream and the populated
// symbol table.
func Parse(toks []Tok) ([]byte, *format.SymbolTable, error) {
	p := &parser{toks: toks, st: format.NewSymbolTable()}
	for !p.atEOF() {
		if err := p.parseLine(); err != nil {
			return nil, nil, err
		}
	}
	return p.rw.Bytes(), p.st, nil
}

func (p *parser) atEOF() bool { return p.toks[p.pos].Kind == TokEOF }
func (p *parser) cur() Tok    { return p.toks[p.pos] }

func (p *parser) parseLine() error {
	for p.cur().Kind == TokEOL {
		p.pos++
	}
	if p.atEOF() {
		return nil
	}

	emittedStatement := false

	for {
		t := p.cur()
		switch t.Kind {
		case TokEOL:
			p.pos++
			return nil
		case TokEOF:
			return nil
		case TokLineComment, TokBlockComment:
			placement := byte(0)
			if emittedStatement {
				placement = 1
			}
			p.rw.WriteComment(placement, t.Bytes)
			p.pos++
		case TokInt:
			if p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == TokColon && t.Int >= 1 && t.Int <= 99 {
				p.rw.WriteLocalDef(byte(t.Int))
				p.pos += 2
				emittedStatement = true
				continue
			}
			return newErr(t.Pos, "unexpected number at start of statement")
		case TokIdent:
			if p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == TokColon {
				id := p.st.Intern(t.Text)
				p.rw.WriteLabelDef(id)
				p.pos += 2
				emittedStatement = true
				continue
			}
			if err := p.parseInstOrDirective(t); err != nil {
				return err
			}
			emittedStatement = true
		default:
			return newErr(t.Pos, "unexpected token kind %d", t.Kind)
		}
	}
}

func (p *parser) parseInstOrDirective(t Tok) error {
	if len(t.Text) > 0 && t.Text[0] == '.' {
		return p.parseDirective(t)
	}
	return p.parseInst(t)
}

func (p *parser) parseDirective(t Tok) error {
	id, ok := format.DirectiveID(t.Text)
	if !ok {
		return newErr(t.Pos, "unknown directive %q", t.Text)
	}
	p.pos++
	var ow format.OperandWriter
	count := byte(0)
	for {
		switch p.cur().Kind {
		case TokEOL, TokEOF, TokLineComment, TokBlockComment:
			p.rw.WriteDirective(id, count, ow.Bytes())
			return nil
		case TokComma:
			if count == 0 {
				return newErr(p.cur().Pos, "unexpected ','")
			}
			p.pos++
			continue
		}
		if p.cur().Kind == TokString {
			ow.WriteString(p.cur().Bytes)
			p.pos++
			count++
			continue
		}
		if err := p.parseOperand(&ow); err != nil {
			return err
		}
		count++
	}
}

func (p *parser) parseInst(t Tok) error {
	id, ok := format.MnemonicID(t.Text)
	if !ok {
		return newErr(t.Pos, "unknown mnemonic %q", t.Text)
	}
	p.pos++

	// MOVK has a special immediate syntax: `movk Rd, #imm16 [, lsl #N]`.
	// The lsl #N suffix selects which 16-bit slot to fill (hw=N/16).
	// We encode the hw into bits [17:16] of the immediate so the refenc
	// encoder can extract it without needing a new operand kind.
	movkID, _ := format.MnemonicID("movk")
	if id == movkID {
		return p.parseMovk(id)
	}

	// MOVL is a spectrum4 pseudo-instruction: `movl Rd, #imm32` loads a
	// 32-bit (or 64-bit) constant into a register. It expands to:
	//   movz Rd, #lo16          (always emitted)
	//   movk Rd, #hi16, lsl #16 (only if hi16 != 0)
	// Both are emitted as separate instruction records.
	movlID, _ := format.MnemonicID("movl")
	if id == movlID {
		return p.parseMovl()
	}

	// LDR with `=value` is a literal-pool pseudo-instruction. It is
	// always shaped `ldr <Xn|Wn>, =<expr>` and emits a single record
	// carrying [OpReg{X|W}, OpLitPool{width, expr}].
	ldrID, _ := format.MnemonicID("ldr")
	if id == ldrID {
		if handled, err := p.tryParseLdrLitPool(id); handled || err != nil {
			return err
		}
	}

	var ow format.OperandWriter
	count := byte(0)
	for {
		switch p.cur().Kind {
		case TokEOL, TokEOF, TokLineComment, TokBlockComment:
			p.rw.WriteInst(id, count, ow.Bytes())
			return nil
		case TokComma:
			if count == 0 {
				return newErr(p.cur().Pos, "unexpected ','")
			}
			p.pos++
			continue
		}
		if err := p.parseOperand(&ow); err != nil {
			return err
		}
		count++
	}
}

// parseMovk handles the MOVK instruction which has the syntax:
//
//	movk <Rd>, #<imm16> [, lsl #N]
//
// The lsl #N suffix selects which 16-bit slot within the register to write
// (hw = N/16 where N ∈ {0, 16, 32, 48}). We encode hw into bits [17:16] of
// the immediate constant so the refenc encoder can extract it. For operands
// without lsl, hw defaults to 0.
func (p *parser) parseMovk(id uint16) error {
	var ow format.OperandWriter

	// Operand 1: destination register (Xd or Wd).
	if err := p.parseOperand(&ow); err != nil {
		return err
	}

	// Expect comma.
	if p.cur().Kind != TokComma {
		return newErr(p.cur().Pos, "movk: expected ',' after register")
	}
	p.pos++

	// Operand 2: immediate (#imm16).
	immExpr, err := p.parseExpression()
	if err != nil {
		return err
	}
	imm16, ok := format.EvalConst(immExpr)
	if !ok {
		return newErr(p.cur().Pos, "movk: immediate must be a constant")
	}
	if imm16 < 0 || imm16 > 0xffff {
		return newErr(p.cur().Pos, "movk: immediate %d out of range [0, 65535]", imm16)
	}

	// Optional: `, lsl #N` suffix.
	hw := int64(0)
	if p.cur().Kind == TokComma {
		p.pos++
		if p.cur().Kind == TokIdent && p.cur().Text == "lsl" {
			p.pos++
			if p.cur().Kind != TokHash {
				return newErr(p.cur().Pos, "movk: expected '#' after lsl")
			}
			p.pos++
			shiftExpr, err := p.parseExpression()
			if err != nil {
				return err
			}
			shiftAmt, ok := format.EvalConst(shiftExpr)
			if !ok {
				return newErr(p.cur().Pos, "movk: shift amount must be a constant")
			}
			if shiftAmt < 0 || shiftAmt > 48 || shiftAmt%16 != 0 {
				return newErr(p.cur().Pos, "movk: lsl shift %d not in {0, 16, 32, 48}", shiftAmt)
			}
			hw = shiftAmt / 16
		} else {
			return newErr(p.cur().Pos, "movk: expected 'lsl' after ','")
		}
	}

	// Encode hw into bits [17:16] of the immediate so the encoder can
	// distinguish hw=0 from hw=1/2/3 while keeping the format generic.
	encoded := (hw << 16) | imm16
	var folded format.ExprWriter
	folded.WriteImm(encoded)
	ow.WriteImmExpr(folded.Bytes())

	p.rw.WriteInst(id, 2, ow.Bytes())
	return nil
}

// parseMovl handles the spectrum4 `movl Rd, imm32` pseudo-instruction.
// It expands to two real instructions using :abs_g0_nc: and :abs_g1: relocation ops:
//
//	mov  Rd, :abs_g0_nc:expr     (MOVZ with low 16 bits, no-carry)
//	movk Rd, :abs_g1:expr        (MOVK with bits 31:16)
//
// When the expression is a constant, we use constant-folded lo/hi literals instead.
func (p *parser) parseMovl() error {
	movzID, _ := format.MnemonicID("mov")
	movkID, _ := format.MnemonicID("movk")

	// Operand 1: destination register (Xd or Wd).
	var ow format.OperandWriter
	if err := p.parseOperand(&ow); err != nil {
		return err
	}
	// Extract register kind and index from what we just wrote.
	rdBytes := ow.Bytes()
	if len(rdBytes) < 2 {
		return newErr(p.cur().Pos, "movl: expected register")
	}
	rdKind := format.OperandKind(rdBytes[0])
	rdReg := rdBytes[1]

	// Expect comma.
	if p.cur().Kind != TokComma {
		return newErr(p.cur().Pos, "movl: expected ',' after register")
	}
	p.pos++

	// Operand 2: value expression (may be a symbol or a constant).
	immExpr, err := p.parseExpression()
	if err != nil {
		return err
	}

	if imm, ok := format.EvalConst(immExpr); ok {
		// Constant case: fold directly.
		lo16 := imm & 0xffff
		hi16 := (imm >> 16) & 0xffff

		if lo16 == 0 && hi16 != 0 {
			// Emit MOVZ Rd, #hi16, lsl #16 (hw=1 encoded in bits [17:16]).
			var ow1 format.OperandWriter
			ow1.WriteReg(rdKind, rdReg)
			var e1 format.ExprWriter
			e1.WriteImm((int64(1) << 16) | hi16)
			ow1.WriteImmExpr(e1.Bytes())
			p.rw.WriteInst(uint16(movzID), 2, ow1.Bytes())
			return nil
		}
		// Emit MOVZ Rd, #lo16.
		{
			var ow1 format.OperandWriter
			ow1.WriteReg(rdKind, rdReg)
			var e1 format.ExprWriter
			e1.WriteImm(lo16)
			ow1.WriteImmExpr(e1.Bytes())
			p.rw.WriteInst(uint16(movzID), 2, ow1.Bytes())
		}
		// If hi16 != 0, emit MOVK Rd, #hi16, lsl #16.
		if hi16 != 0 {
			var ow2 format.OperandWriter
			ow2.WriteReg(rdKind, rdReg)
			var e2 format.ExprWriter
			e2.WriteImm((int64(1) << 16) | hi16)
			ow2.WriteImmExpr(e2.Bytes())
			p.rw.WriteInst(movkID, 2, ow2.Bytes())
		}
		return nil
	}

	// Symbolic case: expand to:
	//   mov  Rd, :abs_g0_nc:expr   — MOVZ with bits[15:0], no-carry
	//   movk Rd, :abs_g1:expr      — MOVK with bits[31:16], lsl #16
	//
	// immExpr is an already-encoded expression bytecode (e.g. PUSH_SYM id).
	// We append the relocation op to produce :rel:sym form.
	{
		var ow1 format.OperandWriter
		ow1.WriteReg(rdKind, rdReg)
		var e1 format.ExprWriter
		// Append immExpr bytes then the relocation op.
		e1.AppendRaw(immExpr)
		e1.WriteOp(format.OpRelAbsG0NC)
		ow1.WriteImmExpr(e1.Bytes())
		p.rw.WriteInst(uint16(movzID), 2, ow1.Bytes())
	}
	{
		var ow2 format.OperandWriter
		ow2.WriteReg(rdKind, rdReg)
		// For MOVK hw=1 (lsl #16): encode as (1<<16) | :abs_g1:sym.
		// The Imm16Shifted slot extracts hw from bits[17:16] of the imm value.
		var e2 format.ExprWriter
		e2.WriteImm(1 << 16)   // hw=1 marker
		e2.AppendRaw(immExpr)  // push the symbol/expr
		e2.WriteOp(format.OpRelAbsG1)
		e2.WriteOp(format.OpOr) // (1<<16) | :abs_g1:sym
		ow2.WriteImmExpr(e2.Bytes())
		p.rw.WriteInst(movkID, 2, ow2.Bytes())
	}
	return nil
}

// tryParseLdrLitPool checks for the `ldr <Xn|Wn>, =<expr>` form. If the
// shape matches it parses and emits the instruction and returns
// (true, nil) (or (true, err) on a parse error). Otherwise it returns
// (false, nil) and leaves p.pos at the start of the operand list so
// the generic parseInst flow can handle this `ldr` like any other.
//
// We require the syntax to be exactly `ldr <reg>, =<expr>` — i.e. the
// second operand starts with `=`. Any other shape (memory addressing,
// PC-relative literal with a numeric offset etc.) falls through.
func (p *parser) tryParseLdrLitPool(id uint16) (bool, error) {
	// Peek without consuming.
	startPos := p.pos

	// Op0: register name.
	t0 := p.cur()
	if t0.Kind != TokIdent {
		return false, nil
	}
	regKind, reg, ok := matchReg(t0.Text)
	if !ok {
		return false, nil
	}
	if regKind != format.OpRegX && regKind != format.OpRegW {
		return false, nil
	}

	// Comma.
	if p.pos+1 >= len(p.toks) || p.toks[p.pos+1].Kind != TokComma {
		return false, nil
	}
	// Equals at operand 1 head.
	if p.pos+2 >= len(p.toks) || p.toks[p.pos+2].Kind != TokEquals {
		return false, nil
	}

	// Commit: consume `<reg>` `,` `=`.
	p.pos += 3

	expr, err := p.parseExpression()
	if err != nil {
		// Restore and let the caller bubble the error.
		p.pos = startPos
		return true, err
	}

	width := byte(8)
	if regKind == format.OpRegW {
		width = 4
	}

	var ow format.OperandWriter
	ow.WriteReg(regKind, reg)
	ow.WriteLitPool(width, expr)
	p.rw.WriteInst(id, 2, ow.Bytes())
	return true, nil
}

func (p *parser) parseOperand(ow *format.OperandWriter) error {
	t := p.cur()
	switch t.Kind {
	case TokIdent:
		if c, ok := matchCond(t.Text); ok {
			ow.WriteCond(c)
			p.pos++
			return nil
		}
		if kind, reg, ok := matchReg(t.Text); ok {
			p.pos++
			if p.cur().Kind == TokComma && p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == TokIdent {
				next := p.toks[p.pos+1].Text
				if sk, ok := matchShiftKind(next); ok && (kind == format.OpRegX || kind == format.OpRegW) {
					p.pos += 2
					if p.cur().Kind != TokHash {
						return newErr(p.cur().Pos, "expected '#' after shift")
					}
					p.pos++
					amt, err := p.parseExpression()
					if err != nil {
						return err
					}
					width := byte(0)
					if kind == format.OpRegX {
						width = 1
					}
					ow.WriteShiftedReg(width, reg, sk, amt)
					return nil
				}
				if ek, ok := matchExtend(next); ok && (kind == format.OpRegX || kind == format.OpRegW) {
					p.pos += 2
					var amt []byte
					if p.cur().Kind == TokHash {
						p.pos++
						a, err := p.parseExpression()
						if err != nil {
							return err
						}
						amt = a
					}
					width := byte(0)
					if kind == format.OpRegX {
						width = 1
					}
					ow.WriteExtendedReg(width, reg, ek, amt)
					return nil
				}
			}
			ow.WriteReg(kind, reg)
			return nil
		}
		expr, err := p.parseExpression()
		if err != nil {
			return err
		}
		ow.WriteImmExpr(expr)
		return nil
	case TokHash, TokInt, TokMinus, TokTilde, TokLParen, TokDot, TokLocalRef, TokColon:
		expr, err := p.parseExpression()
		if err != nil {
			return err
		}
		ow.WriteImmExpr(expr)
		return nil
	case TokLBracket:
		return p.parseMem(ow)
	}
	return newErr(t.Pos, "unexpected token in operand")
}

// matchReg returns the operand kind and register index for a textual
// register name, or ok=false if it is not a register.
func matchReg(name string) (format.OperandKind, byte, bool) {
	switch name {
	case "sp":
		return format.OpRegXSP, 31, true
	case "wsp":
		return format.OpRegWSP, 31, true
	case "xzr":
		return format.OpRegX, 31, true
	case "wzr":
		return format.OpRegW, 31, true
	case "fp":
		return format.OpRegX, 29, true
	case "lr":
		return format.OpRegX, 30, true
	}
	if len(name) < 2 {
		return 0, 0, false
	}
	prefix := name[0]
	if prefix != 'x' && prefix != 'w' {
		return 0, 0, false
	}
	num := 0
	for _, c := range []byte(name[1:]) {
		if c < '0' || c > '9' {
			return 0, 0, false
		}
		num = num*10 + int(c-'0')
		if num > 30 {
			return 0, 0, false
		}
	}
	if prefix == 'x' {
		return format.OpRegX, byte(num), true
	}
	return format.OpRegW, byte(num), true
}

// parseExpression consumes tokens until it hits a comma, EOL, EOF, or
// a closing bracket. It returns the bytecode for the expression.
func (p *parser) parseExpression() ([]byte, error) {
	var w format.ExprWriter
	if err := p.parseExprPrec(&w, 0); err != nil {
		return nil, err
	}
	if v, ok := format.EvalConst(w.Bytes()); ok {
		var folded format.ExprWriter
		folded.WriteImm(v)
		return folded.Bytes(), nil
	}
	return w.Bytes(), nil
}

func tokPrec(k TokKind) int {
	switch k {
	case TokPipe, TokCaret:
		return 0
	case TokAmp:
		return 1
	case TokShl, TokShr:
		return 2
	case TokPlus, TokMinus:
		return 3
	case TokStar, TokSlash:
		return 4
	}
	return -1
}

func (p *parser) parseExprPrec(w *format.ExprWriter, minPrec int) error {
	if err := p.parseExprPrimary(w); err != nil {
		return err
	}
	for {
		k := p.cur().Kind
		prec := tokPrec(k)
		if prec < minPrec {
			return nil
		}
		opTok := p.cur()
		p.pos++
		if err := p.parseExprPrec(w, prec+1); err != nil {
			return err
		}
		switch opTok.Kind {
		case TokPlus:
			w.WriteOp(format.OpAdd)
		case TokMinus:
			w.WriteOp(format.OpSub)
		case TokStar:
			w.WriteOp(format.OpMul)
		case TokSlash:
			w.WriteOp(format.OpDiv)
		case TokAmp:
			w.WriteOp(format.OpAnd)
		case TokPipe:
			w.WriteOp(format.OpOr)
		case TokCaret:
			w.WriteOp(format.OpXor)
		case TokShl:
			w.WriteOp(format.OpShl)
		case TokShr:
			w.WriteOp(format.OpShr)
		}
	}
}

func (p *parser) parseExprPrimary(w *format.ExprWriter) error {
	t := p.cur()
	switch t.Kind {
	case TokHash:
		p.pos++
		return p.parseExprPrimary(w)
	case TokInt:
		w.WriteImm(t.Int)
		p.pos++
		return nil
	case TokIdent:
		id := p.st.Intern(t.Text)
		w.WriteSym(id)
		p.pos++
		return nil
	case TokDot:
		w.WritePC()
		p.pos++
		return nil
	case TokLocalRef:
		dir := byte(0)
		if t.LocalDir == 'b' {
			dir = 1
		}
		w.WriteLocal(t.Digit, dir)
		p.pos++
		return nil
	case TokMinus:
		p.pos++
		if err := p.parseExprPrimary(w); err != nil {
			return err
		}
		w.WriteOp(format.OpNeg)
		return nil
	case TokTilde:
		p.pos++
		if err := p.parseExprPrimary(w); err != nil {
			return err
		}
		w.WriteOp(format.OpNot)
		return nil
	case TokLParen:
		p.pos++
		if err := p.parseExprPrec(w, 0); err != nil {
			return err
		}
		if p.cur().Kind != TokRParen {
			return newErr(p.cur().Pos, "missing ')'")
		}
		p.pos++
		return nil
	case TokColon:
		p.pos++
		if p.cur().Kind != TokIdent {
			return newErr(p.cur().Pos, "expected relocation name after ':'")
		}
		name := p.cur().Text
		p.pos++
		if p.cur().Kind != TokColon {
			return newErr(p.cur().Pos, "expected ':' after relocation name")
		}
		p.pos++
		if err := p.parseExprPrimary(w); err != nil {
			return err
		}
		op, ok := relocOp(name)
		if !ok {
			return newErr(t.Pos, "unknown relocation %q", name)
		}
		w.WriteOp(op)
		return nil
	}
	return newErr(t.Pos, "unexpected token in expression")
}

func (p *parser) parseMem(ow *format.OperandWriter) error {
	p.pos++ // consume '['
	baseTok := p.cur()
	if baseTok.Kind != TokIdent {
		return newErr(baseTok.Pos, "expected register after '['")
	}
	baseKind, base, ok := matchReg(baseTok.Text)
	if !ok || (baseKind != format.OpRegX && baseKind != format.OpRegXSP) {
		return newErr(baseTok.Pos, "expected X register after '['")
	}
	p.pos++

	if p.cur().Kind == TokRBracket {
		p.pos++
		// Post-index? `[base], #imm`
		if p.cur().Kind == TokComma && p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == TokHash {
			p.pos++ // ,
			expr, err := p.parseExpression()
			if err != nil {
				return err
			}
			ow.WriteMemBaseOff(format.MemBaseOffPost, base, expr)
			return nil
		}
		ow.WriteMemBase(base)
		return nil
	}

	if p.cur().Kind != TokComma {
		return newErr(p.cur().Pos, "expected ',' or ']'")
	}
	p.pos++

	if p.cur().Kind == TokIdent {
		idxKind, idx, ok := matchReg(p.cur().Text)
		if ok && (idxKind == format.OpRegX || idxKind == format.OpRegW) {
			width := byte(0)
			if idxKind == format.OpRegX {
				width = 1
			}
			p.pos++
			if p.cur().Kind == TokComma {
				p.pos++
				modTok := p.cur()
				if modTok.Kind != TokIdent {
					return newErr(modTok.Pos, "expected shift/extend keyword")
				}
				if modTok.Text == "lsl" {
					p.pos++
					if p.cur().Kind != TokHash {
						return newErr(p.cur().Pos, "expected '#'")
					}
					p.pos++
					if p.cur().Kind != TokInt {
						return newErr(p.cur().Pos, "shift amount must be literal")
					}
					amt := byte(p.cur().Int)
					p.pos++
					if err := p.expect(TokRBracket); err != nil {
						return err
					}
					ow.WriteMemBaseIdxShifted(base, idx, width, amt)
					return nil
				}
				ext, ok := matchExtend(modTok.Text)
				if !ok {
					return newErr(modTok.Pos, "unknown extend %q", modTok.Text)
				}
				p.pos++
				amt := byte(0)
				if p.cur().Kind == TokHash {
					p.pos++
					if p.cur().Kind != TokInt {
						return newErr(p.cur().Pos, "extend amount must be literal")
					}
					amt = byte(p.cur().Int)
					p.pos++
				}
				if err := p.expect(TokRBracket); err != nil {
					return err
				}
				ow.WriteMemBaseIdxExtended(base, idx, width, ext, amt)
				return nil
			}
			if err := p.expect(TokRBracket); err != nil {
				return err
			}
			ow.WriteMemBaseIdx(base, idx, width)
			return nil
		}
	}

	expr, err := p.parseExpression()
	if err != nil {
		return err
	}
	if err := p.expect(TokRBracket); err != nil {
		return err
	}
	if p.cur().Kind == TokBang {
		p.pos++
		ow.WriteMemBaseOff(format.MemBaseOffPre, base, expr)
		return nil
	}
	ow.WriteMemBaseOff(format.MemBaseOff, base, expr)
	return nil
}

func (p *parser) expect(k TokKind) error {
	if p.cur().Kind != k {
		return newErr(p.cur().Pos, "expected token %d, got %d", k, p.cur().Kind)
	}
	p.pos++
	return nil
}

func matchExtend(name string) (format.ExtendKind, bool) {
	for i := 0; i < 8; i++ {
		if format.ExtendKind(i).Name() == name {
			return format.ExtendKind(i), true
		}
	}
	return 0, false
}

func matchShiftKind(name string) (format.ShiftKind, bool) {
	for i := 0; i < 4; i++ {
		if format.ShiftKind(i).Name() == name {
			return format.ShiftKind(i), true
		}
	}
	return 0, false
}

func matchCond(name string) (format.CondCode, bool) {
	for i := 0; i < 16; i++ {
		if format.CondCode(i).Name() == name {
			return format.CondCode(i), true
		}
	}
	return 0, false
}

func relocOp(name string) (format.ExprOp, bool) {
	switch name {
	case "lo12":
		return format.OpRelLo12, true
	case "hi12":
		return format.OpRelHi12, true
	case "abs_g0":
		return format.OpRelAbsG0, true
	case "abs_g0_nc":
		return format.OpRelAbsG0NC, true
	case "abs_g1":
		return format.OpRelAbsG1, true
	case "abs_g1_nc":
		return format.OpRelAbsG1NC, true
	case "abs_g2":
		return format.OpRelAbsG2, true
	case "abs_g2_nc":
		return format.OpRelAbsG2NC, true
	case "abs_g3":
		return format.OpRelAbsG3, true
	}
	return 0, false
}
