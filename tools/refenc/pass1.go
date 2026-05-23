package main

import (
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Pass1Result holds the symbol table and total program size produced
// by the first pass.
type Pass1Result struct {
	Symbols   map[string]int64
	LocalDefs map[byte][]int64
	TotalSize int64
}

// Pass1 walks records and assigns PC to each instruction / data
// directive, populating the symbol table.
func Pass1(f *format.File) (*Pass1Result, error) {
	res := &Pass1Result{
		Symbols:   make(map[string]int64),
		LocalDefs: make(map[byte][]int64),
	}
	var pc int64

	rr := format.NewRecordReader(f.Records)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			return nil, err
		}
		switch rec.Kind {
		case format.KindLabelDef:
			res.Symbols[f.Names[rec.SymbolID]] = pc
		case format.KindLocalDef:
			res.LocalDefs[rec.Digit] = append(res.LocalDefs[rec.Digit], pc)
		case format.KindInst:
			pc += 4
		case format.KindDirective:
			n, err := directiveSize(rec)
			if err != nil {
				return nil, err
			}
			pc += n
		}
	}
	res.TotalSize = pc
	return res, nil
}

func directiveSize(rec format.Record) (int64, error) {
	name := format.DirectiveName(rec.DirectiveID)
	switch name {
	case ".byte":
		return int64(rec.OperandCount), nil
	case ".short":
		return int64(rec.OperandCount) * 2, nil
	case ".word":
		return int64(rec.OperandCount) * 4, nil
	case ".quad":
		return int64(rec.OperandCount) * 8, nil
	case ".ascii", ".asciz":
		or := format.NewOperandReader(rec.Operands)
		o, err := or.Next()
		if err != nil {
			return 0, err
		}
		n := int64(len(o.Str))
		if name == ".asciz" {
			n++
		}
		return n, nil
	case ".skip", ".space":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		v, ok := format.EvalConst(o.Expr)
		if !ok {
			return 0, fmt.Errorf(".skip with non-constant operand")
		}
		return v, nil
	case ".inst":
		return 4, nil
	case ".text", ".data", ".global", ".equ", ".set":
		return 0, nil
	case ".balign", ".org":
		// Approximate: 0 size. Real implementation rounds PC up
		// or sets it — deferred.
		return 0, nil
	}
	return 0, fmt.Errorf("unknown directive %q in pass1", name)
}
