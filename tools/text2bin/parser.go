package main

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
			if p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == TokColon && t.Int >= 1 && t.Int <= 9 {
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
		return newErr(t.Pos, "directive parsing arrives in Task 21")
	}
	return p.parseInst(t)
}

func (p *parser) parseInst(t Tok) error {
	id, ok := format.MnemonicID(t.Text)
	if !ok {
		return newErr(t.Pos, "unknown mnemonic %q", t.Text)
	}
	p.pos++
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

func (p *parser) parseOperand(ow *format.OperandWriter) error {
	t := p.cur()
	switch t.Kind {
	case TokIdent:
		if kind, reg, ok := matchReg(t.Text); ok {
			ow.WriteReg(kind, reg)
			p.pos++
			return nil
		}
		expr, err := p.parseExpression()
		if err != nil {
			return err
		}
		ow.WriteImmExpr(expr)
		return nil
	case TokHash, TokInt, TokMinus, TokTilde, TokLParen, TokDot, TokLocalRef:
		expr, err := p.parseExpression()
		if err != nil {
			return err
		}
		ow.WriteImmExpr(expr)
		return nil
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
	}
	return newErr(t.Pos, "unexpected token in expression")
}
