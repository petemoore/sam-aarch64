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
			name := format.DirectiveName(rec.DirectiveID)
			if name == ".equ" || name == ".set" {
				// .equ NAME, value — add to symbol table as a constant.
				// Operand 1 is a PUSH_SYM expr with the symbol index.
				// Operand 2 is a constant expression for the value.
				if err := resolveEquDirective(rec, f, res); err != nil {
					return nil, fmt.Errorf(".equ: %w", err)
				}
				// No PC contribution.
			} else {
				n, err := directiveSizeAtPC(rec, pc)
				if err != nil {
					return nil, err
				}
				pc += n
			}
		}
	}
	res.TotalSize = pc
	return res, nil
}

// resolveEquDirective handles .equ/.set directives by evaluating the value
// expression and adding the symbol to the symbol table. The first operand
// of .equ is a symbol-reference expression (PUSH_SYM nameID); the second
// is the constant value expression.
func resolveEquDirective(rec format.Record, f *format.File, res *Pass1Result) error {
	or := format.NewOperandReader(rec.Operands)
	// Operand 1: the symbol being defined.
	symOp, err := or.Next()
	if err != nil {
		return fmt.Errorf("missing symbol operand: %w", err)
	}
	// Evaluate the symbol-ref expression; it should resolve to its own ID
	// via PUSH_SYM. We just need the name, so use EvalConst on the expr:
	// If PUSH_SYM evaluates to 0 (because the symbol isn't in the table yet),
	// we use the expr bytes directly to extract the name ID.
	nameID, ok := extractSymID(symOp.Expr)
	if !ok {
		return fmt.Errorf("first operand of .equ must be a symbol reference")
	}
	name := f.Names[nameID]
	// Operand 2: the value.
	valOp, err := or.Next()
	if err != nil {
		return fmt.Errorf("missing value operand: %w", err)
	}
	v, ok := format.EvalConst(valOp.Expr)
	if !ok {
		return fmt.Errorf(".equ %s: value is not a constant expression", name)
	}
	res.Symbols[name] = v
	return nil
}

// extractSymID returns the symbol ID from an expression that is exactly a
// PUSH_SYM instruction (opcode 0x05 followed by 2-byte LE ID). Returns false
// if the expression doesn't match that shape.
func extractSymID(expr []byte) (uint16, bool) {
	// A PUSH_SYM expr is [0x05, lo, hi] (3 bytes).
	if len(expr) != 3 || format.ExprOp(expr[0]) != format.OpPushSym {
		return 0, false
	}
	return uint16(expr[1]) | uint16(expr[2])<<8, true
}

func directiveSize(rec format.Record) (int64, error) {
	return directiveSizeAtPC(rec, 0)
}

// directiveSizeAtPC computes the byte contribution of a directive
// record at the given PC. For most directives pc is ignored; for
// .balign it is needed to compute the padding.
func directiveSizeAtPC(rec format.Record, pc int64) (int64, error) {
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
	case ".balign":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		align, ok := format.EvalConst(o.Expr)
		if !ok {
			return 0, fmt.Errorf(".balign with non-constant operand")
		}
		if align <= 1 {
			return 0, nil
		}
		pad := (align - (pc % align)) % align
		return pad, nil
	case ".org":
		return 0, nil
	}
	return 0, fmt.Errorf("unknown directive %q in pass1", name)
}
