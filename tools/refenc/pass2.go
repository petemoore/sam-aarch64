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

	// Dispatch compound-operand forms before ValidateOperandKinds.
	// Memory, shifted-register, and extended-register operands carry
	// richer structure than a flat kind-list can express, so they are
	// encoded directly rather than through the form table.
	for _, k := range kinds {
		if k == format.OpMem {
			return encodeMemInst(rec.MnemonicID, operands, pc, p1, f)
		}
		if k == format.OpShiftedReg {
			return encodeShiftedRegInst(rec.MnemonicID, operands, pc, p1, f)
		}
		if k == format.OpExtendedReg {
			return encodeExtendedRegInst(rec.MnemonicID, operands, pc, p1, f)
		}
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

// ---------------------------------------------------------------------------
// Memory operand encoding
// ---------------------------------------------------------------------------

// mnemonicIsLoad returns true when mnemonicID is ldr or similar load.
// Returns false for str/stp etc. Used to set the L bit in memory encodings.
func isLoadMnemonic(mnemonicID uint16) bool {
	// ldr=5, ldp=7; str=6, stp=8
	return mnemonicID == 5 || mnemonicID == 7
}

// encodeMemInst encodes instructions that have an OpMem operand.
// Handles ldr and str for all memory addressing modes.
func encodeMemInst(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	// Operand 0 is the destination/source register (Rt).
	if len(operands) < 2 {
		return 0, fmt.Errorf("encodeMemInst: need at least 2 operands, got %d", len(operands))
	}
	rt := operands[0].Reg
	mem := operands[1]
	if mem.Kind != format.OpMem {
		return 0, fmt.Errorf("encodeMemInst: operand 1 is not OpMem")
	}

	isLoad := isLoadMnemonic(mnemonicID)
	// ldr/str Xt, [...]: 64-bit (size=11, V=0).
	// Base pattern bits [31:30]=11, [29:27]=111, [26]=0
	// = 0xf8000000 with bits 25:24 and 23:22 filled in by mode.
	const size64 = uint32(0b11) << 30
	const vfpMid = uint32(0b111) << 27

	switch mem.MemShape {
	case format.MemBase, format.MemBaseOff:
		// LDR/STR (unsigned offset): size(2)|111|01|opc(2)|imm12|Rn|Rt
		// For 64-bit: pattern base = 0xf9000000 (str) or 0xf9400000 (ldr)
		// bits 25:24 = 01 (unsigned offset encoding)
		// bits 23:22 = 01 (load) or 00 (store)
		opc := uint32(0) // store
		if isLoad {
			opc = uint32(1)
		}
		base := size64 | vfpMid | (uint32(1) << 24) | (opc << 22)
		// imm12 is a scaled (unsigned) offset: byte_offset / 8 for 64-bit
		var byteOffset int64
		if mem.MemShape == format.MemBaseOff {
			ctx := makeCtx(pc, p1, f)
			v, err := enc.Eval(mem.Expr, ctx)
			if err != nil {
				return 0, err
			}
			byteOffset = v
		}
		if byteOffset < 0 || byteOffset%8 != 0 || byteOffset/8 >= (1<<12) {
			return 0, fmt.Errorf("LDR/STR unsigned offset: byte offset %d not representable as scaled imm12 (must be 0..32760, multiple of 8)", byteOffset)
		}
		imm12 := uint32(byteOffset / 8)
		word := base | (imm12 << 10) | (uint32(mem.Base) << 5) | uint32(rt)
		return word, nil

	case format.MemBaseOffPre:
		// LDR/STR (immediate, pre-index): size(2)|111|00|opc(2)|0|imm9|11|Rn|Rt
		// bits 25:24 = 00 (unscaled/pre/post)
		// bits 23:22 = 01 (load) or 00 (store)
		// bit  21    = 0
		// bits 20:12 = imm9 (signed)
		// bits 11:10 = 11 (pre-index)
		opc := uint32(0)
		if isLoad {
			opc = uint32(1)
		}
		base := size64 | vfpMid | (opc << 22) | (uint32(3) << 10)
		ctx := makeCtx(pc, p1, f)
		v, err := enc.Eval(mem.Expr, ctx)
		if err != nil {
			return 0, err
		}
		imm9, err := encodeSignedImm9(v)
		if err != nil {
			return 0, fmt.Errorf("LDR/STR pre-index: %w", err)
		}
		word := base | (imm9 << 12) | (uint32(mem.Base) << 5) | uint32(rt)
		return word, nil

	case format.MemBaseOffPost:
		// LDR/STR (immediate, post-index): bits 11:10 = 01
		opc := uint32(0)
		if isLoad {
			opc = uint32(1)
		}
		base := size64 | vfpMid | (opc << 22) | (uint32(1) << 10)
		ctx := makeCtx(pc, p1, f)
		v, err := enc.Eval(mem.Expr, ctx)
		if err != nil {
			return 0, err
		}
		imm9, err := encodeSignedImm9(v)
		if err != nil {
			return 0, fmt.Errorf("LDR/STR post-index: %w", err)
		}
		word := base | (imm9 << 12) | (uint32(mem.Base) << 5) | uint32(rt)
		return word, nil

	case format.MemBaseIdx:
		// LDR/STR (register, no shift): Xt, [Xn, Xm]
		// size(2)|111|00|opc(2)|1|Rm|option|S|10|Rn|Rt
		// For Xm: option=011 (LSL), S=0
		opc := uint32(0)
		if isLoad {
			opc = uint32(1)
		}
		const optionLSL = uint32(0b011)
		base := size64 | vfpMid | (opc << 22) | (uint32(1) << 21) |
			(optionLSL << 13) | (uint32(0b10) << 10)
		word := base | (uint32(mem.Idx) << 16) | (uint32(mem.Base) << 5) | uint32(rt)
		return word, nil

	case format.MemBaseIdxShifted:
		// LDR/STR (register, LSL): Xt, [Xn, Xm, LSL #N]
		// Same as MemBaseIdx but S=1; S lives at bit 12.
		// ShiftAmt must be 0 or the log2 of size (3 for 64-bit).
		opc := uint32(0)
		if isLoad {
			opc = uint32(1)
		}
		const optionLSL = uint32(0b011)
		s := uint32(0)
		if mem.ShiftAmt != 0 {
			s = 1
		}
		base := size64 | vfpMid | (opc << 22) | (uint32(1) << 21) |
			(optionLSL << 13) | (s << 12) | (uint32(0b10) << 10)
		word := base | (uint32(mem.Idx) << 16) | (uint32(mem.Base) << 5) | uint32(rt)
		return word, nil

	case format.MemBaseIdxExtended:
		// LDR/STR (register, extend): Xt, [Xn, Wm, UXTW/SXTW {#N}]
		// size(2)|111|00|opc(2)|1|Rm|option|S|10|Rn|Rt
		// option = extend kind (UXTW=010, SXTW=110, etc.)
		// S = shift-applied (0 or 1)
		opc := uint32(0)
		if isLoad {
			opc = uint32(1)
		}
		option := uint32(mem.Extend)
		s := uint32(0)
		if mem.ShiftAmt != 0 {
			s = 1
		}
		base := size64 | vfpMid | (opc << 22) | (uint32(1) << 21) |
			(option << 13) | (s << 12) | (uint32(0b10) << 10)
		word := base | (uint32(mem.Idx) << 16) | (uint32(mem.Base) << 5) | uint32(rt)
		return word, nil

	default:
		return 0, fmt.Errorf("encodeMemInst: unsupported MemShape %v", mem.MemShape)
	}
}

// encodeSignedImm9 packs a signed byte offset into a 9-bit field.
func encodeSignedImm9(v int64) (uint32, error) {
	if v < -256 || v > 255 {
		return 0, fmt.Errorf("signed imm9: value %d out of range [-256,255]", v)
	}
	return uint32(v) & 0x1ff, nil
}

// ---------------------------------------------------------------------------
// Shifted-register operand encoding
// ---------------------------------------------------------------------------

// encodeShiftedRegInst encodes ADD/SUB (shifted register) instructions.
// Operands: Rd, Rn, OpShiftedReg{Rm, shift, amount}
func encodeShiftedRegInst(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 3 {
		return 0, fmt.Errorf("encodeShiftedRegInst: need 3 operands")
	}
	rd := operands[0].Reg
	rn := operands[1].Reg
	sr := operands[2]
	if sr.Kind != format.OpShiftedReg {
		return 0, fmt.Errorf("encodeShiftedRegInst: operand 2 is not OpShiftedReg")
	}

	ctx := makeCtx(pc, p1, f)
	amt, err := enc.Eval(sr.AmtExpr, ctx)
	if err != nil {
		return 0, fmt.Errorf("shift amount: %w", err)
	}
	if amt < 0 || amt > 63 {
		return 0, fmt.Errorf("shift amount %d out of range [0,63]", amt)
	}

	// Determine sf (64-bit) and opc from mnemonic.
	// add=1, sub=2. 64-bit form: sf=1.
	sf, opc, err := shiftedRegMnemonicFields(mnemonicID)
	if err != nil {
		return 0, err
	}

	// Encoding: sf(1)|opc(2)|01011|shift(2)|0|Rm(5)|imm6(6)|Rn(5)|Rd(5)
	shiftEnc := uint32(sr.ShiftKind)
	word := (sf << 31) | (opc << 29) | uint32(0b01011)<<24 |
		(shiftEnc << 22) | (uint32(sr.Reg) << 16) |
		(uint32(amt) << 10) | (uint32(rn) << 5) | uint32(rd)
	return word, nil
}

func shiftedRegMnemonicFields(mnemonicID uint16) (sf, opc uint32, err error) {
	// add=1, sub=2. Only 64-bit (X-register) forms for now.
	switch mnemonicID {
	case 1: // add
		return 1, 0b00, nil
	case 2: // sub
		return 1, 0b10, nil
	default:
		return 0, 0, fmt.Errorf("shiftedReg: unsupported mnemonic id %d", mnemonicID)
	}
}

// ---------------------------------------------------------------------------
// Extended-register operand encoding
// ---------------------------------------------------------------------------

// encodeExtendedRegInst encodes ADD/SUB (extended register) instructions.
// Operands: Rd, Rn, OpExtendedReg{Rm, extend, amount}
func encodeExtendedRegInst(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 3 {
		return 0, fmt.Errorf("encodeExtendedRegInst: need 3 operands")
	}
	rd := operands[0].Reg
	rn := operands[1].Reg
	er := operands[2]
	if er.Kind != format.OpExtendedReg {
		return 0, fmt.Errorf("encodeExtendedRegInst: operand 2 is not OpExtendedReg")
	}

	ctx := makeCtx(pc, p1, f)
	var amt int64
	if len(er.AmtExpr) > 0 {
		var err error
		amt, err = enc.Eval(er.AmtExpr, ctx)
		if err != nil {
			return 0, fmt.Errorf("extend amount: %w", err)
		}
	}
	if amt < 0 || amt > 4 {
		return 0, fmt.Errorf("extend shift amount %d out of range [0,4]", amt)
	}

	sf, opc, err := extendedRegMnemonicFields(mnemonicID)
	if err != nil {
		return 0, err
	}

	// Encoding: sf(1)|opc(2)|01011|00|1|Rm(5)|option(3)|imm3(3)|Rn(5)|Rd(5)
	// bit 21 = 1 (distinguishes extended from shifted)
	option := uint32(er.Extend)
	word := (sf << 31) | (opc << 29) | uint32(0b01011)<<24 |
		(uint32(1) << 21) |
		(uint32(er.Reg) << 16) | (option << 13) |
		(uint32(amt) << 10) | (uint32(rn) << 5) | uint32(rd)
	return word, nil
}

func extendedRegMnemonicFields(mnemonicID uint16) (sf, opc uint32, err error) {
	switch mnemonicID {
	case 1: // add
		return 1, 0b00, nil
	case 2: // sub
		return 1, 0b10, nil
	default:
		return 0, 0, fmt.Errorf("extendedReg: unsupported mnemonic id %d", mnemonicID)
	}
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
	case ".balign":
		// Round PC up to next multiple of alignment, emitting zero bytes.
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		align, ok := format.EvalConst(o.Expr)
		if !ok {
			return nil, fmt.Errorf(".balign: non-constant alignment")
		}
		if align <= 1 {
			return nil, nil
		}
		pad := (align - (pc % align)) % align
		return make([]byte, pad), nil
	case ".skip", ".space":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		v, ok := format.EvalConst(o.Expr)
		if !ok {
			return nil, fmt.Errorf(".skip: non-constant size")
		}
		return make([]byte, v), nil
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
