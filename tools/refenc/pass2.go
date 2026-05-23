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

	emitFlush := func(preFlushPC int64) error {
		entries, ok := p1.PoolFlushEntries[preFlushPC]
		if !ok {
			return nil
		}
		afterPC := p1.PoolFlushAtPC[preFlushPC]
		var fours, eights []int
		for _, i := range entries {
			if p1.PoolEntries[i].Width == 4 {
				fours = append(fours, i)
			} else {
				eights = append(eights, i)
			}
		}
		// Pad to 4 if needed before 4-byte literals.
		if len(fours) > 0 && pc%4 != 0 {
			padN := 4 - (pc % 4)
			out = append(out, make([]byte, padN)...)
			pc += padN
		}
		for _, i := range fours {
			e := p1.PoolEntries[i]
			ctx := makeCtx(e.EvalPC, p1, f)
			v, err := enc.Eval(e.Expr, ctx)
			if err != nil {
				return fmt.Errorf("pool entry @ pc=0x%x: %w", e.PC, err)
			}
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], uint32(v))
			out = append(out, buf[:]...)
			pc += 4
		}
		if len(eights) > 0 && pc%8 != 0 {
			padN := 8 - (pc % 8)
			out = append(out, make([]byte, padN)...)
			pc += padN
		}
		for _, i := range eights {
			e := p1.PoolEntries[i]
			ctx := makeCtx(e.EvalPC, p1, f)
			v, err := enc.Eval(e.Expr, ctx)
			if err != nil {
				return fmt.Errorf("pool entry @ pc=0x%x: %w", e.PC, err)
			}
			var buf [8]byte
			binary.LittleEndian.PutUint64(buf[:], uint64(v))
			out = append(out, buf[:]...)
			pc += 8
		}
		if pc != afterPC {
			return fmt.Errorf("pool flush mismatch: pc=0x%x, after=0x%x", pc, afterPC)
		}
		return nil
	}

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
			name := format.DirectiveName(rec.DirectiveID)
			if name == ".ltorg" {
				if err := emitFlush(pc); err != nil {
					return nil, err
				}
				continue
			}
			bytes, err := encodeDirective(rec, pc, p1, f)
			if err != nil {
				return nil, err
			}
			out = append(out, bytes...)
			pc += int64(len(bytes))
		}
	}
	// Implicit pool flush at end of input.
	if err := emitFlush(pc); err != nil {
		return nil, err
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
		if k == format.OpLitPool {
			return encodeLdrLitPoolInst(operands, pc, p1)
		}
	}

	// Mnemonic-specific intercepts before the generic form table:
	// lsl/lsr use UBFM with computed immr/imms; bfi/bfxil/ubfx use
	// BFM/UBFM with alias-specific computations.
	switch rec.MnemonicID {
	case 17, 18: // lsl, lsr
		return encodeLSLSR(rec.MnemonicID, operands, pc, p1, f)
	case 49, 50, 51: // bfi, bfxil, ubfx
		return encodeBitfieldInst(rec.MnemonicID, operands, pc, p1, f)
	case 47: // bic — immediate form: negate the immediate before LogicalImm
		if len(kinds) >= 3 && kinds[2] == format.OpImmExpr {
			return encodeBicImm(operands, pc, p1, f)
		}
	case 52: // csetm — invert the condition code before encoding
		return encodeCsetm(operands, pc, p1, f)
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
				case enc.AdrImm:
					// ADR uses raw byte offset from current PC.
					v = v - pc
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
// LDR literal-pool pseudo-instruction
// ---------------------------------------------------------------------------

// encodeLdrLitPoolInst encodes `ldr Xn|Wn, =expr` as a PC-relative
// load whose target is the literal-pool slot allocated for this
// instruction during pass1.
//
//	X-form (size=01): bits[31:24] = 0x58 — 0x58000000 + (imm19 << 5) + Rt
//	W-form (size=00): bits[31:24] = 0x18 — 0x18000000 + (imm19 << 5) + Rt
//
// imm19 = (target_pc - pc) / 4. The 19-bit signed range is ±1 MiB.
func encodeLdrLitPoolInst(operands []format.Operand, pc int64, p1 *Pass1Result) (uint32, error) {
	if len(operands) < 2 {
		return 0, fmt.Errorf("ldr litpool: need 2 operands, got %d", len(operands))
	}
	rt := operands[0]
	lit := operands[1]
	if lit.Kind != format.OpLitPool {
		// Defensive: dispatch should have ensured this.
		return 0, fmt.Errorf("ldr litpool: operand 1 kind = %v", lit.Kind)
	}

	idx, ok := p1.LdrPoolIdx[pc]
	if !ok {
		return 0, fmt.Errorf("ldr litpool @ pc=0x%x: no pool index recorded", pc)
	}
	targetPC := p1.PoolEntries[idx].PC
	off := targetPC - pc
	if off%4 != 0 {
		return 0, fmt.Errorf("ldr litpool @ pc=0x%x: target pc=0x%x not 4-byte aligned", pc, targetPC)
	}
	imm19 := off / 4
	if imm19 < -(1<<18) || imm19 >= (1<<18) {
		return 0, fmt.Errorf("ldr litpool @ pc=0x%x: offset %d out of ±1MiB range", pc, off)
	}

	var base uint32
	if lit.Width == 8 {
		base = 0x58000000 // 64-bit LDR (literal)
	} else {
		base = 0x18000000 // 32-bit LDR (literal)
	}
	word := base | ((uint32(imm19) & 0x7ffff) << 5) | uint32(rt.Reg)
	return word, nil
}

// ---------------------------------------------------------------------------
// Memory operand encoding
// ---------------------------------------------------------------------------

// mnemonicIsLoad returns true when mnemonicID is ldr or similar load.
// Returns false for str/stp etc. Used to set the L bit in memory encodings.
func isLoadMnemonic(mnemonicID uint16) bool {
	// ldr=5, ldp=7, ldrb=54, ldrh=56
	return mnemonicID == 5 || mnemonicID == 7 || mnemonicID == 54 || mnemonicID == 56
}

// memInstSize returns the AArch64 "size" field (bits 31:30) and the byte
// scale factor for a given load/store mnemonic.
// ldr/str: size=11 (64-bit), scale=8
// ldrb/strb: size=00 (byte), scale=1
// ldrh/strh: size=01 (halfword), scale=2
func memInstSize(mnemonicID uint16) (sizeBits uint32, scale int64) {
	switch mnemonicID {
	case 54, 55: // ldrb, strb
		return 0b00, 1
	case 56, 57: // ldrh, strh
		return 0b01, 2
	default: // ldr(5), str(6), ldp(7), stp(8)
		return 0b11, 8
	}
}

// encodeMemInst encodes instructions that have an OpMem operand.
// Handles ldr/str/ldrb/strb/ldrh/strh for all memory addressing modes,
// and ldp/stp (pair) instructions with 3 operands (Rt1, Rt2, Mem).
func encodeMemInst(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	// Pair instructions (ldp=7, stp=8) have 3 operands: Rt1, Rt2, Mem.
	if len(operands) >= 3 && operands[2].Kind == format.OpMem {
		return encodePairInst(mnemonicID, operands, pc, p1, f)
	}

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
	sizeBits, scale := memInstSize(mnemonicID)

	// AArch64 load/store encoding:
	// bits[31:30] = size (00=byte, 01=halfword, 10=word, 11=doubleword)
	// bits[29:27] = 111 (VFP=0 is 0b111_0; V=0 → 0b111, bit26=0)
	// bits[25:24] = addressing mode (01=unsigned-offset, 00=pre/post/unscaled)
	// bits[23:22] = opc (01=load, 00=store for integer)
	const vfpMid = uint32(0b111) << 27

	switch mem.MemShape {
	case format.MemBase, format.MemBaseOff:
		// LDR/STR (unsigned offset): size(2)|111|01|opc(2)|imm12|Rn|Rt
		// bits 25:24 = 01 (unsigned offset encoding)
		// bits 23:22 = 01 (load) or 00 (store)
		opc := uint32(0) // store
		if isLoad {
			opc = uint32(1)
		}
		base := (sizeBits << 30) | vfpMid | (uint32(1) << 24) | (opc << 22)
		// imm12 is a scaled (unsigned) offset: byte_offset / scale
		var byteOffset int64
		if mem.MemShape == format.MemBaseOff {
			ctx := makeCtx(pc, p1, f)
			v, err := enc.Eval(mem.Expr, ctx)
			if err != nil {
				return 0, err
			}
			byteOffset = v
		}
		if byteOffset < 0 || byteOffset%scale != 0 || byteOffset/scale >= (1<<12) {
			return 0, fmt.Errorf("LDR/STR unsigned offset: byte offset %d not representable as scaled imm12 (must be 0..%d, multiple of %d)", byteOffset, scale*(1<<12-1), scale)
		}
		imm12 := uint32(byteOffset / scale)
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
		base := (sizeBits << 30) | vfpMid | (opc << 22) | (uint32(3) << 10)
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
		base := (sizeBits << 30) | vfpMid | (opc << 22) | (uint32(1) << 10)
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
		base := (sizeBits << 30) | vfpMid | (opc << 22) | (uint32(1) << 21) |
			(optionLSL << 13) | (uint32(0b10) << 10)
		word := base | (uint32(mem.Idx) << 16) | (uint32(mem.Base) << 5) | uint32(rt)
		return word, nil

	case format.MemBaseIdxShifted:
		// LDR/STR (register, LSL): Xt, [Xn, Xm, LSL #N]
		// Same as MemBaseIdx but S=1; S lives at bit 12.
		opc := uint32(0)
		if isLoad {
			opc = uint32(1)
		}
		const optionLSL = uint32(0b011)
		s := uint32(0)
		if mem.ShiftAmt != 0 {
			s = 1
		}
		base := (sizeBits << 30) | vfpMid | (opc << 22) | (uint32(1) << 21) |
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
		base := (sizeBits << 30) | vfpMid | (opc << 22) | (uint32(1) << 21) |
			(option << 13) | (s << 12) | (uint32(0b10) << 10)
		word := base | (uint32(mem.Idx) << 16) | (uint32(mem.Base) << 5) | uint32(rt)
		return word, nil

	default:
		return 0, fmt.Errorf("encodeMemInst: unsupported MemShape %v", mem.MemShape)
	}
}

// encodePairInst encodes LDP/STP (load/store pair) instructions.
// Operands: Rt1, Rt2, Mem where Mem carries the base register and offset.
// AArch64 encoding: opc(2)|101|0|mode(2)|L|imm7(7)|Rt2(5)|Rn(5)|Rt1(5)
func encodePairInst(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 3 {
		return 0, fmt.Errorf("encodePairInst: need 3 operands, got %d", len(operands))
	}
	rt1 := operands[0]
	rt2 := operands[1]
	mem := operands[2]

	// Determine 64-bit vs 32-bit from the register kind.
	is64 := rt1.Kind == format.OpRegX
	scale := int64(8)
	opc := uint32(0b10) // 64-bit
	if !is64 {
		scale = 4
		opc = uint32(0b00) // 32-bit
	}

	l := uint32(0) // store
	if mnemonicID == 7 { // ldp
		l = 1
	}

	var byteOffset int64
	var modeBits uint32
	switch mem.MemShape {
	case format.MemBase:
		// Signed offset of 0.
		modeBits = 0b10 // signed offset
	case format.MemBaseOff:
		ctx := makeCtx(pc, p1, f)
		v, err := enc.Eval(mem.Expr, ctx)
		if err != nil {
			return 0, err
		}
		byteOffset = v
		modeBits = 0b10 // signed offset
	case format.MemBaseOffPre:
		ctx := makeCtx(pc, p1, f)
		v, err := enc.Eval(mem.Expr, ctx)
		if err != nil {
			return 0, err
		}
		byteOffset = v
		modeBits = 0b11 // pre-index
	case format.MemBaseOffPost:
		ctx := makeCtx(pc, p1, f)
		v, err := enc.Eval(mem.Expr, ctx)
		if err != nil {
			return 0, err
		}
		byteOffset = v
		modeBits = 0b01 // post-index
	default:
		return 0, fmt.Errorf("encodePairInst: unsupported MemShape %v", mem.MemShape)
	}

	if byteOffset%scale != 0 {
		return 0, fmt.Errorf("encodePairInst: byte offset %d not a multiple of %d", byteOffset, scale)
	}
	scaledOff := byteOffset / scale
	if scaledOff < -64 || scaledOff > 63 {
		return 0, fmt.Errorf("encodePairInst: scaled offset %d out of range [-64,63]", scaledOff)
	}
	imm7 := uint32(scaledOff) & 0x7F

	// opc(2)|101|0|mode(2)|L|imm7(7)|Rt2(5)|Rn(5)|Rt1(5)
	word := (opc << 30) | (0b101 << 27) | (modeBits << 23) | (l << 22) |
		(imm7 << 15) | (uint32(rt2.Reg) << 10) | (uint32(mem.Base) << 5) | uint32(rt1.Reg)
	return word, nil
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

// encodeShiftedRegInst encodes shifted-register instructions.
// Operands: Rd, Rn, OpShiftedReg{Rm, shift, amount}
// For tst: Rn, OpShiftedReg (Rd is implicitly xzr=31, baked into pattern).
func encodeShiftedRegInst(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	// tst (46) shifted-reg: only 2 operands (Rn, Rm{,shift}).
	// bic (47) with ShiftedReg uses 3 operands (Rd, Rn, Rm{,shift}).
	// subs (45) uses 3 operands (Rd, Rn, Rm{,shift}).
	// and/orr/eor (14/15/16) shifted-reg: 3 operands.
	// add/sub (1/2): 3 operands.
	isTst := mnemonicID == 46
	minOps := 3
	if isTst {
		minOps = 2
	}
	if len(operands) < minOps {
		return 0, fmt.Errorf("encodeShiftedRegInst: need %d operands, got %d", minOps, len(operands))
	}

	var rd, rn byte
	var sr format.Operand
	if isTst {
		rd = 31 // xzr baked
		rn = operands[0].Reg
		sr = operands[1]
	} else {
		rd = operands[0].Reg
		rn = operands[1].Reg
		sr = operands[2]
	}
	if sr.Kind != format.OpShiftedReg {
		return 0, fmt.Errorf("encodeShiftedRegInst: shifted-reg operand is not OpShiftedReg")
	}

	ctx := makeCtx(pc, p1, f)
	amt, err := enc.Eval(sr.AmtExpr, ctx)
	if err != nil {
		return 0, fmt.Errorf("shift amount: %w", err)
	}
	if amt < 0 || amt > 63 {
		return 0, fmt.Errorf("shift amount %d out of range [0,63]", amt)
	}

	// Determine sf (64-bit flag), opc, and N bit from mnemonic.
	sf, opc, nBit, err := shiftedRegMnemonicFields(mnemonicID, sr.Width == 1)
	if err != nil {
		return 0, err
	}

	// Encoding: sf(1)|opc(2)|01011|shift(2)|N(1)|Rm(5)|imm6(6)|Rn(5)|Rd(5)
	shiftEnc := uint32(sr.ShiftKind)
	word := (sf << 31) | (opc << 29) | uint32(0b01011)<<24 |
		(shiftEnc << 22) | (nBit << 21) | (uint32(sr.Reg) << 16) |
		(uint32(amt) << 10) | (uint32(rn) << 5) | uint32(rd)
	return word, nil
}

// shiftedRegMnemonicFields returns sf, opc, N-bit for shifted-register encoding.
// is64 is true when the operands are X registers.
// N=1 distinguishes BIC/ORN/EON from AND/ORR/EOR in the shifted-reg space.
func shiftedRegMnemonicFields(mnemonicID uint16, is64 bool) (sf, opc, nBit uint32, err error) {
	sf = 0
	if is64 {
		sf = 1
	}
	switch mnemonicID {
	case 1: // add
		return sf, 0b00, 0, nil
	case 2: // sub
		return sf, 0b10, 0, nil
	case 14: // and (shifted-reg, N=0)
		return sf, 0b00, 0, nil
	case 15: // orr (shifted-reg, N=0)
		return sf, 0b01, 0, nil
	case 16: // eor (shifted-reg, N=0)
		return sf, 0b10, 0, nil
	case 45: // subs (shifted-reg): opc=11
		return sf, 0b11, 0, nil
	case 46: // tst = ands (shifted-reg, N=0): opc=11 (ANDS discards result)
		return sf, 0b11, 0, nil
	case 47: // bic (shifted-reg, N=1): AND NOT
		return sf, 0b00, 1, nil
	default:
		return 0, 0, 0, fmt.Errorf("shiftedReg: unsupported mnemonic id %d", mnemonicID)
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

// ---------------------------------------------------------------------------
// LSL / LSR via UBFM
// ---------------------------------------------------------------------------

// encodeLSLSR encodes lsl and lsr as UBFM aliases.
// Syntax: lsl/lsr Rd, Rn, #shift
//
// LSL: immr = (-shift) mod regsize, imms = regsize - 1 - shift
// LSR: immr = shift, imms = regsize - 1
func encodeLSLSR(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 3 {
		return 0, fmt.Errorf("lsl/lsr: need 3 operands, got %d", len(operands))
	}
	rd := operands[0]
	rn := operands[1]
	immOp := operands[2]
	if immOp.Kind != format.OpImmExpr {
		return 0, fmt.Errorf("lsl/lsr: operand 2 must be an immediate")
	}

	ctx := makeCtx(pc, p1, f)
	shift, err := enc.Eval(immOp.Expr, ctx)
	if err != nil {
		return 0, fmt.Errorf("lsl/lsr shift: %w", err)
	}

	// Determine register size from operand kind.
	is64 := rd.Kind == format.OpRegX
	regsize := int64(64)
	if !is64 {
		regsize = 32
	}

	if shift < 0 || shift >= regsize {
		return 0, fmt.Errorf("lsl/lsr: shift %d out of range [0,%d)", shift, regsize)
	}

	var immr, imms uint32
	if mnemonicID == 17 { // lsl
		immr = uint32((-shift)&(regsize-1)) & 0x3F
		imms = uint32(regsize-1-shift) & 0x3F
	} else { // lsr (18)
		immr = uint32(shift) & 0x3F
		imms = uint32(regsize-1) & 0x3F
	}

	// UBFM pattern: sf=is64, opc=10, 100110, N=is64, immr, imms, Rn, Rd
	// 64-bit: 0xd3400000, 32-bit: 0x53000000
	var base uint32
	if is64 {
		base = 0xd3400000
	} else {
		base = 0x53000000
	}
	word := base | (immr << 16) | (imms << 10) | (uint32(rn.Reg) << 5) | uint32(rd.Reg)
	return word, nil
}

// ---------------------------------------------------------------------------
// Bitfield instructions: bfi, bfxil, ubfx
// ---------------------------------------------------------------------------

// encodeBitfieldInst encodes bfi / bfxil / ubfx.
// All three use BFM or UBFM base with computed immr/imms.
//
// BFI (49):   Rd, Rn, #lsb, #width → BFM immr=(-lsb)%regsize, imms=width-1
// BFXIL (50): Rd, Rn, #lsb, #width → BFM immr=lsb, imms=lsb+width-1
// UBFX (51):  Rd, Rn, #lsb, #width → UBFM immr=lsb, imms=lsb+width-1
func encodeBitfieldInst(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 4 {
		return 0, fmt.Errorf("bitfield: need 4 operands, got %d", len(operands))
	}
	rd := operands[0]
	rn := operands[1]
	lsbOp := operands[2]
	widthOp := operands[3]

	if lsbOp.Kind != format.OpImmExpr || widthOp.Kind != format.OpImmExpr {
		return 0, fmt.Errorf("bitfield: operands 2 and 3 must be immediates")
	}

	ctx := makeCtx(pc, p1, f)
	lsb, err := enc.Eval(lsbOp.Expr, ctx)
	if err != nil {
		return 0, fmt.Errorf("bitfield lsb: %w", err)
	}
	width, err := enc.Eval(widthOp.Expr, ctx)
	if err != nil {
		return 0, fmt.Errorf("bitfield width: %w", err)
	}

	is64 := rd.Kind == format.OpRegX
	regsize := int64(64)
	if !is64 {
		regsize = 32
	}

	if lsb < 0 || lsb >= regsize {
		return 0, fmt.Errorf("bitfield: lsb %d out of range", lsb)
	}
	if width < 1 || width > regsize-lsb {
		return 0, fmt.Errorf("bitfield: width %d out of range", width)
	}

	var immr, imms uint32
	var base uint32
	switch mnemonicID {
	case 49: // bfi: BFM alias — immr=(-lsb)%regsize, imms=width-1
		immr = uint32((-lsb)&(regsize-1)) & 0x3F
		imms = uint32(width-1) & 0x3F
		if is64 {
			base = 0xb3400000 // BFM 64-bit
		} else {
			base = 0x33000000 // BFM 32-bit
		}
	case 50: // bfxil: BFM alias — immr=lsb, imms=lsb+width-1
		immr = uint32(lsb) & 0x3F
		imms = uint32(lsb+width-1) & 0x3F
		if is64 {
			base = 0xb3400000
		} else {
			base = 0x33000000
		}
	case 51: // ubfx: UBFM alias — immr=lsb, imms=lsb+width-1
		immr = uint32(lsb) & 0x3F
		imms = uint32(lsb+width-1) & 0x3F
		if is64 {
			base = 0xd3400000 // UBFM 64-bit
		} else {
			base = 0x53000000 // UBFM 32-bit
		}
	}

	word := base | (immr << 16) | (imms << 10) | (uint32(rn.Reg) << 5) | uint32(rd.Reg)
	return word, nil
}

// ---------------------------------------------------------------------------
// BIC immediate — negate the logical immediate then use AND-imm encoding
// ---------------------------------------------------------------------------

// encodeBicImm encodes `bic Rd, Rn, #imm` as `and Rd, Rn, #~imm`.
// The bitwise NOT of the operand produces the AND mask to clear the target bits.
func encodeBicImm(operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 3 {
		return 0, fmt.Errorf("bic-imm: need 3 operands, got %d", len(operands))
	}
	rd := operands[0]
	rn := operands[1]
	immOp := operands[2]

	ctx := makeCtx(pc, p1, f)
	imm, err := enc.Eval(immOp.Expr, ctx)
	if err != nil {
		return 0, fmt.Errorf("bic-imm: %w", err)
	}

	// BIC clears the bits given; AND with ~imm achieves the same.
	negImm := ^imm

	is64 := rd.Kind == format.OpRegX
	// Build fake OperandSlot to call encodeLogicalImm.
	slot := enc.OperandSlot{SlotKind: enc.LogicalImm, BitPosition: 10, BitWidth: 13}
	bits, err := enc.EncodeLogicalImmPub(slot, negImm, is64)
	if err != nil {
		return 0, fmt.Errorf("bic-imm: cannot encode ~%d as logical immediate: %w", imm, err)
	}

	// AND-imm base pattern: 32-bit=0x12000000, 64-bit=0x92000000
	var base uint32
	if is64 {
		base = 0x92000000
	} else {
		base = 0x12000000
	}
	word := base | bits | (uint32(rn.Reg) << 5) | uint32(rd.Reg)
	return word, nil
}

// ---------------------------------------------------------------------------
// CSETM — invert condition before encoding CSINV
// ---------------------------------------------------------------------------

// encodeCsetm encodes `csetm Rd, cond` as `csinv Rd, xzr, xzr, !cond`.
// The condition must be inverted: the canonical condition bit XOR 1 selects the inverse.
func encodeCsetm(operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 2 {
		return 0, fmt.Errorf("csetm: need 2 operands, got %d", len(operands))
	}
	rd := operands[0]
	condOp := operands[1]
	if condOp.Kind != format.OpCond {
		return 0, fmt.Errorf("csetm: operand 1 must be a condition code")
	}

	// Invert condition: cond XOR 1 (e.g. EQ→NE, CS→CC, etc.)
	invertedCond := uint32(condOp.Cond) ^ 1

	is64 := rd.Kind == format.OpRegX
	// CSINV pattern: 32-bit=0x5a9f03e0, 64-bit=0xda9f03e0
	// Rn=Rm=xzr (11111) baked, cond at bits [15:12].
	var base uint32
	if is64 {
		base = 0xda9f03e0
	} else {
		base = 0x5a9f03e0
	}
	word := base | (invertedCond << 12) | uint32(rd.Reg)
	return word, nil
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
	case ".section":
		// .section is a no-op in the current flat-layout pipeline. See
		// docs/notes/m2-status.md for the multi-section gap this leaves.
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
	case ".align":
		// aarch64 GNU as convention: `.align N` aligns to 2^N bytes.
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		n, ok := format.EvalConst(o.Expr)
		if !ok {
			return nil, fmt.Errorf(".align: non-constant exponent")
		}
		if n <= 0 {
			return nil, nil
		}
		align := int64(1) << uint64(n)
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
