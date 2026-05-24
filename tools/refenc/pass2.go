package main

import (
	"encoding/binary"
	"fmt"

	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Pass2 emits the binary output by walking records again with the
// symbol table available from Pass1.
func Pass2(f *format.File, p1 *Pass1Result) ([]byte, error) {
	var out []byte
	var pc int64

	rr := format.NewRecordReader(f.Records)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			return nil, err
		}
		switch rec.Kind {
		case format.KindInst:
			word, err := encodeInst(rec, pc, p1, f)
			if err != nil {
				return nil, fmt.Errorf("pc=0x%x: %w", pc, err)
			}
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], word)
			out = append(out, buf[:]...)
			pc += 4
		case format.KindDirective:
			bytes, err := encodeDirective(rec, pc, p1, f)
			if err != nil {
				return nil, err
			}
			out = append(out, bytes...)
			pc += int64(len(bytes))
		}
	}
	return out, nil
}

func makeCtx(pc int64, p1 *Pass1Result, f *format.File) enc.EvalContext {
	return enc.EvalContext{
		PC: pc,
		Symbol: func(id uint16) (int64, bool) {
			v, ok := p1.Symbols[f.Names[id]]
			return v, ok
		},
		LocalLabel: func(digit, dir byte) (int64, bool) {
			positions := p1.LocalDefs[digit]
			if dir == 0 {
				for _, v := range positions {
					if v > pc {
						return v, true
					}
				}
				return 0, false
			}
			var best int64
			found := false
			for _, v := range positions {
				if v <= pc {
					best = v
					found = true
				}
			}
			return best, found
		},
	}
}

func encodeInst(rec format.Record, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	or := format.NewOperandReader(rec.Operands)
	var kinds []format.OperandKind
	var operands []format.Operand
	for !or.AtEnd() {
		o, err := or.Next()
		if err != nil {
			return 0, err
		}
		operands = append(operands, o)
		kinds = append(kinds, o.Kind)
	}
	form, ok, diag := enc.ValidateOperandKinds(rec.MnemonicID, kinds)
	if !ok {
		return 0, fmt.Errorf("%s", diag)
	}
	values, err := operandsToValues(operands, pc, p1, f, form)
	if err != nil {
		return 0, err
	}
	return enc.Encode(form, values)
}

func operandsToValues(ops []format.Operand, pc int64, p1 *Pass1Result, f *format.File, form enc.Form) ([]enc.OperandValue, error) {
	ctx := makeCtx(pc, p1, f)
	var out []enc.OperandValue
	for i, o := range ops {
		switch o.Kind {
		case format.OpRegX, format.OpRegW, format.OpRegXSP, format.OpRegWSP:
			out = append(out, enc.OperandValue{Reg: o.Reg})
		case format.OpImmExpr:
			v, err := enc.Eval(o.Expr, ctx)
			if err != nil {
				return nil, err
			}
			// For PC-relative slot kinds, convert absolute address
			// to byte offset from PC.
			if i < len(form.Slots) {
				switch form.Slots[i].SlotKind {
				case enc.BranchImm26, enc.BranchImm19, enc.BranchImm14:
					v = v - pc
				case enc.AdrpImm:
					// ADRP uses page-aligned PC; the offset is the
					// difference between target page and PC page.
					v = (v & ^int64(0xFFF)) - (pc & ^int64(0xFFF))
				}
			}
			out = append(out, enc.OperandValue{Imm: v})
		case format.OpCond:
			out = append(out, enc.OperandValue{Cond: o.Cond})
		default:
			return nil, fmt.Errorf("operandsToValues: unsupported kind %v", o.Kind)
		}
	}
	return out, nil
}

func encodeDirective(rec format.Record, pc int64, p1 *Pass1Result, f *format.File) ([]byte, error) {
	name := format.DirectiveName(rec.DirectiveID)
	ctx := makeCtx(pc, p1, f)
	switch name {
	case ".byte":
		return evalImmsAsBytes(rec, ctx, 1)
	case ".short":
		return evalImmsAsBytes(rec, ctx, 2)
	case ".word":
		return evalImmsAsBytes(rec, ctx, 4)
	case ".quad":
		return evalImmsAsBytes(rec, ctx, 8)
	case ".ascii":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		return o.Str, nil
	case ".asciz":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		return append(o.Str, 0), nil
	case ".text", ".data", ".global", ".equ", ".set":
		return nil, nil
	}
	return nil, fmt.Errorf("encodeDirective: %s not yet supported", name)
}

func evalImmsAsBytes(rec format.Record, ctx enc.EvalContext, size int) ([]byte, error) {
	or := format.NewOperandReader(rec.Operands)
	var out []byte
	for !or.AtEnd() {
		o, err := or.Next()
		if err != nil {
			return nil, err
		}
		v, err := enc.Eval(o.Expr, ctx)
		if err != nil {
			return nil, err
		}
		for i := 0; i < size; i++ {
			out = append(out, byte(v>>(8*i)))
		}
	}
	return out, nil
}
