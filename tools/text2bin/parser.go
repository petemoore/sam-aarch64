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
	return newErr(t.Pos, "instruction parsing arrives in Task 19")
}
