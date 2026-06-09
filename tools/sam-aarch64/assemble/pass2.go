package assemble

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
	// PC tracks the *VMA*: starts at OriginVMA (zero unless a leading
	// `.org N` shifted the origin). Output byte 0 always corresponds to
	// pc == OriginVMA, mirroring `objcopy -O binary` semantics.
	pc := p1.OriginVMA
	originVMA := p1.OriginVMA

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
		// Pad to 4 if needed before 4-byte literals. Use unsigned
		// modulus so high-VMA pc (negative as int64) produces the
		// correct remainder (see pass1.flushPool for the same fix).
		if len(fours) > 0 && uint64(pc)%4 != 0 {
			padN := int64(4 - (uint64(pc) % 4))
			out = append(out, make([]byte, padN)...)
			pc += padN
		}
		for _, i := range fours {
			e := p1.PoolEntries[i]
			ctx := makeCtx(e.EvalPC, p1, f)
			v, err := EvalUsage(e.Expr, ctx, usage)
			if err != nil {
				return fmt.Errorf("pool entry @ pc=0x%x: %w", e.PC, err)
			}
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], uint32(v))
			out = append(out, buf[:]...)
			pc += 4
		}
		if len(eights) > 0 && uint64(pc)%8 != 0 {
			padN := int64(8 - (uint64(pc) % 8))
			out = append(out, make([]byte, padN)...)
			pc += padN
		}
		for _, i := range eights {
			e := p1.PoolEntries[i]
			ctx := makeCtx(e.EvalPC, p1, f)
			v, err := EvalUsage(e.Expr, ctx, usage)
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
		case format.KindLitInsts:
			// Pre-assembled literal run: copy the stored little-endian
			// words straight to OUT — no encoding work.
			out = append(out, rec.LitWords...)
			pc += 4 * int64(rec.LitCount)
		case format.KindLitData:
			// Pre-assembled constant data run: copy the stored bytes
			// straight to OUT.
			out = append(out, rec.LitData...)
			pc += int64(len(rec.LitData))
		case format.KindInsnRun:
			// Overlay run: emit each element's base word ORed with every
			// patch's folded bits. A patch-free element (mode 0, or a
			// literal carried in a mode-1 run) emits its base word verbatim.
			for _, el := range rec.Elements {
				word := el.BaseWord
				for _, patch := range el.Patches {
					slot := enc.FoldSlot(patch.Slot)
					var value int64
					if slot == enc.FoldLitpool19 {
						idx, ok := p1.LdrPoolIdx[pc]
						if !ok {
							return nil, fmt.Errorf("pc=0x%x: INSN_RUN litpool patch with no pool index", uint64(pc))
						}
						value = p1.PoolEntries[idx].PC
					} else {
						v, err := EvalUsage(patch.Expr, makeCtx(pc, p1, f), usage)
						if err != nil {
							return nil, fmt.Errorf("pc=0x%x: INSN_RUN patch eval: %w", uint64(pc), err)
						}
						value = v
					}
					bits, err := enc.Fold(slot, value, pc, el.BaseWord)
					if err != nil {
						return nil, fmt.Errorf("pc=0x%x: INSN_RUN fold(slot %d): %w", uint64(pc), patch.Slot, err)
					}
					word |= bits
				}
				var buf [4]byte
				binary.LittleEndian.PutUint32(buf[:], word)
				out = append(out, buf[:]...)
				pc += 4
			}
		case format.KindDirective:
			name := format.DirectiveName(rec.DirectiveID)
			if name == ".ltorg" {
				if err := emitFlush(pc); err != nil {
					return nil, err
				}
				continue
			}
			if name == ".org" {
				// `.org expr` semantics: at the very start of input
				// (no bytes emitted, pc == OriginVMA == 0, no pool
				// entries) it sets the origin VMA. Otherwise it
				// advances pc, emitting zero-fill bytes if expr > pc.
				// Use unsigned arithmetic so high VMAs (>= 2^63) work.
				or := format.NewOperandReader(rec.Operands)
				o, _ := or.Next()
				ctx := makeCtx(pc, p1, f)
				target, err := EvalUsage(o.Expr, ctx, usage)
				if err != nil {
					return nil, fmt.Errorf(".org: %w", err)
				}
				if len(out) == 0 && pc == originVMA && originVMA == 0 && target != 0 {
					// Leading origin set — match pass1's decision.
					pc = target
					originVMA = target
					continue
				}
				if uint64(target) < uint64(pc) {
					return nil, fmt.Errorf(".org: target 0x%x < current pc 0x%x (backward .org not allowed)", uint64(target), uint64(pc))
				}
				pad := int64(uint64(target) - uint64(pc))
				if pad > 0 {
					out = append(out, make([]byte, pad)...)
					pc = target
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

	// Shifted-register coercion: when an arithmetic/logical mnemonic
	// is parsed with three plain register operands (Rd, Rn, Rm) and
	// no explicit shift, treat it as the no-shift case of the shifted
	// -register variant (LSL #0). GNU as accepts this canonical form
	// for ADD/SUB/AND/ORR/EOR/BIC/SUBS/ANDS — encoding identical to
	// `add Xd, Xn, Xm, lsl #0`.
	if len(operands) == 3 && isShiftedRegMnemonic(rec.MnemonicID) {
		k0, k1, k2 := operands[0].Kind, operands[1].Kind, operands[2].Kind
		if isPlainGPR(k0) && isPlainGPR(k1) && isPlainGPR(k2) {
			synth := operands[2]
			synth.Kind = format.OpShiftedReg
			synth.Reg = operands[2].Reg
			synth.ShiftKind = format.ShiftLSL
			if k0 == format.OpRegX {
				synth.Width = 1
			} else {
				synth.Width = 0
			}
			// Zero shift amount expression.
			var ew format.ExprWriter
			ew.WriteImm(0)
			synth.AmtExpr = ew.Bytes()
			coerced := []format.Operand{operands[0], operands[1], synth}
			return encodeShiftedRegInst(rec.MnemonicID, coerced, pc, p1, f)
		}
	}
	// Same coercion for 2-register `tst Rn, Rm` (no Rd; xzr baked).
	if len(operands) == 2 && rec.MnemonicID == 46 {
		if isPlainGPR(operands[0].Kind) && isPlainGPR(operands[1].Kind) {
			synth := operands[1]
			synth.Kind = format.OpShiftedReg
			synth.Reg = operands[1].Reg
			synth.ShiftKind = format.ShiftLSL
			if operands[0].Kind == format.OpRegX {
				synth.Width = 1
			} else {
				synth.Width = 0
			}
			var ew format.ExprWriter
			ew.WriteImm(0)
			synth.AmtExpr = ew.Bytes()
			coerced := []format.Operand{operands[0], synth}
			return encodeShiftedRegInst(rec.MnemonicID, coerced, pc, p1, f)
		}
	}

	// MOV imm autoselect: GNU as picks the smallest single-instruction
	// encoding for `mov Rd, #imm` — movz/movn/orr-imm. For values
	// that fit in movz with shift {0,16,32,48} the standard mov-via-
	// movz form table entry handles it (with encodeImm16Shifted
	// auto-selecting the shift). For ~imm that fits in 16 bits at
	// some shift, GNU emits movn. For other values that are logical
	// immediates, GNU emits `orr Rd, XZR, #imm`. We mirror those
	// fallbacks here so e.g. `mov w1, #0xffffffff` encodes as
	// `movn w1, #0` not `movz w1, #0xffff` (truncating).
	movID, _ := format.MnemonicID("mov")
	if rec.MnemonicID == movID && len(operands) == 2 &&
		(operands[0].Kind == format.OpRegX || operands[0].Kind == format.OpRegW) &&
		operands[1].Kind == format.OpImmExpr {
		if w, ok, err := tryEncodeMovImm(operands, pc, p1, f); ok {
			return w, nil
		} else if err != nil {
			return 0, err
		}
	}

	// LDR (literal) direct-label form: `ldr Xt, label` or `ldr Wt, label`
	// (no `=`). Encodes as PC-relative 19-bit immediate; the label IS
	// the target. Distinct from `ldr Xt, =expr` (literal-pool slot).
	ldrID, _ := format.MnemonicID("ldr")
	if rec.MnemonicID == ldrID && len(operands) == 2 &&
		(operands[0].Kind == format.OpRegX || operands[0].Kind == format.OpRegW) &&
		operands[1].Kind == format.OpImmExpr {
		return encodeLdrLitDirect(operands, pc, p1, f)
	}

	// TBZ / TBNZ — test bit and branch. Encoding (ARM ARM C6.2.298 /
	// C6.2.299):
	//   bits 31    = b5 (bit number bit 5)
	//   bits 30:24 = 0110110 (op)
	//   bit  24    = 0 for TBZ, 1 for TBNZ
	//   bits 23:19 = b40 (bit number bits 4:0)
	//   bits 18:5  = imm14 (signed PC-relative branch / 4)
	//   bits 4:0   = Rt
	if rec.MnemonicID == 22 || rec.MnemonicID == 23 { // tbz/tbnz
		return encodeTbzTbnz(rec.MnemonicID, operands, pc, p1, f)
	}

	// Mnemonic-specific intercepts before the generic form table:
	// lsl/lsr use UBFM with computed immr/imms; bfi/bfxil/ubfx use
	// BFM/UBFM with alias-specific computations.
	switch rec.MnemonicID {
	case 17, 18: // lsl, lsr
		return encodeLSLSR(rec.MnemonicID, operands, pc, p1, f)
	case 49, 50, 51, 83, 84: // bfi, bfxil, ubfx, bfc, sbfx
		return encodeBitfieldInst(rec.MnemonicID, operands, pc, p1, f)
	case 47: // bic — immediate form: negate the immediate before LogicalImm
		if len(kinds) >= 3 && kinds[2] == format.OpImmExpr {
			return encodeBicImm(operands, pc, p1, f)
		}
	case 52: // csetm — invert the condition code before encoding
		return encodeCsetm(operands, pc, p1, f)
	case 66, 67, 68: // isb, dsb, dmb — single CRm operand encoded in bits 11:8
		return encodeBarrierInst(rec.MnemonicID, operands, pc, p1, f)
	case 70: // ror immediate form — alias of EXTR Rd, Rn, Rn, #imm
		if len(kinds) >= 3 && kinds[2] == format.OpImmExpr {
			return encodeRorImm(operands, pc, p1, f)
		}
	case 76: // mrs
		return encodeMrs(operands)
	case 77: // msr (register or PSTATE-immediate)
		return encodeMsr(operands, pc, p1, f)
	case 78: // dc
		return encodeDc(operands)
	case 79: // tlbi
		return encodeTlbi(operands)
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
			v, err := EvalUsage(o.Expr, ctx, usage)
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
					// ADRP uses page-aligned PC; the encoded
					// pageOffset is a signed 21-bit value, so the
					// 21+12 = 33-bit byte-offset diff is implicitly
					// modulo 2^33. Matches GNU as's behaviour of
					// silently truncating absolute targets that wrap
					// around the kernel VMA — used by spectrum4's
					// `adrp xN, 0x<physical_addr>` idioms whose
					// resulting wrapped target lands in the kernel's
					// virtual mapping of that physical page.
					diff := (v & ^int64(0xFFF)) - (pc & ^int64(0xFFF))
					const mask33 = int64(1<<33 - 1)
					diff &= mask33
					if diff&(int64(1)<<32) != 0 {
						diff |= ^mask33
					}
					v = diff
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

// alignPadBytes returns the byte sequence used to pad an alignment
// directive in `.text`. When both the current PC and the padding
// length are 4-byte multiples, the pad is filled with `nop`
// (0xd503201f) — matching GNU as's behaviour in `.text` sections.
// Otherwise the leading non-aligned bytes are zero-filled and the
// 4-aligned tail (if any) is NOPs.
//
// For now the current pipeline emits everything into a single
// implicit `.text` section, so this is the right behaviour for all
// alignment directives we see. If we later carry section type in
// the record stream, this should consult that (`.data`/BSS pad with
// zeros, `.text` pads with NOPs).
func alignPadBytes(pc, pad int64) []byte {
	if pad <= 0 {
		return nil
	}
	const nop = uint32(0xd503201f)
	// Zero-fill leading bytes until PC is 4-aligned.
	out := make([]byte, 0, pad)
	cur := pc
	rem := pad
	for rem > 0 && uint64(cur)%4 != 0 {
		out = append(out, 0)
		cur++
		rem--
	}
	for rem >= 4 {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], nop)
		out = append(out, buf[:]...)
		rem -= 4
		cur += 4
	}
	for rem > 0 {
		out = append(out, 0)
		rem--
	}
	return out
}

// tryEncodeMovImm attempts to encode `mov Rd, #imm` using the
// single-instruction movz / movn / orr-imm fallbacks that GNU as
// picks when the bare value doesn't fit a 16-bit slot. Returns
// (word, true, nil) if a one-instruction encoding was found,
// (0, false, nil) when no encoding works (caller falls through to the
// generic form table, which may also fail), or (_, _, err) on a hard
// error (e.g. malformed expression).
func tryEncodeMovImm(operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, bool, error) {
	rd := operands[0]
	ctx := makeCtx(pc, p1, f)
	imm, err := EvalUsage(operands[1].Expr, ctx, usage)
	if err != nil {
		return 0, false, err
	}
	is64 := rd.Kind == format.OpRegX
	u := uint64(imm)
	if !is64 {
		u &= 0xffffffff
	}
	// 1. Direct movz: value is one 16-bit chunk in any slot.
	for shift := uint(0); shift < 64; shift += 16 {
		if !is64 && shift >= 32 {
			break
		}
		chunk := (u >> shift) & 0xffff
		if chunk<<shift == u {
			hw := uint32(shift / 16)
			var base uint32
			if is64 {
				base = 0xd2800000
			} else {
				base = 0x52800000
			}
			return base | (hw << 21) | (uint32(chunk) << 5) | uint32(rd.Reg), true, nil
		}
	}
	// 2. movn: ~value is one 16-bit chunk in any slot. For W, the
	//    inverted-mask covers only the low 32 bits.
	nu := ^u
	if !is64 {
		nu &= 0xffffffff
	}
	for shift := uint(0); shift < 64; shift += 16 {
		if !is64 && shift >= 32 {
			break
		}
		chunk := (nu >> shift) & 0xffff
		if chunk<<shift == nu {
			hw := uint32(shift / 16)
			var base uint32
			if is64 {
				base = 0x92800000 // movn x
			} else {
				base = 0x12800000 // movn w
			}
			return base | (hw << 21) | (uint32(chunk) << 5) | uint32(rd.Reg), true, nil
		}
	}
	// 3. ORR-immediate (logical immediate): mov Rd, #imm aliases to
	//    `orr Rd, XZR, #imm` when imm is a valid logical-imm pattern.
	slot := enc.OperandSlot{SlotKind: enc.LogicalImm, BitPosition: 10, BitWidth: 13}
	if bits, lerr := enc.EncodeLogicalImmPub(slot, imm, is64); lerr == nil {
		var base uint32
		if is64 {
			base = 0xb20003e0 // orr Xd, XZR, #imm
		} else {
			base = 0x320003e0 // orr Wd, WZR, #imm
		}
		return base | bits | uint32(rd.Reg), true, nil
	}
	return 0, false, nil
}

// encodeLdrLitDirect encodes the direct PC-relative literal-load form
// `ldr Xt, label` (no `=`). The label IS the target; the encoded
// imm19 = (label - PC) / 4 (signed). 64-bit form base = 0x58000000;
// 32-bit form base = 0x18000000.
func encodeLdrLitDirect(operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	rt := operands[0]
	target := operands[1]
	ctx := makeCtx(pc, p1, f)
	v, err := EvalUsage(target.Expr, ctx, usage)
	if err != nil {
		return 0, fmt.Errorf("ldr (literal): %w", err)
	}
	off := v - pc
	if off%4 != 0 {
		return 0, fmt.Errorf("ldr (literal) @ pc=0x%x: target pc=0x%x not 4-byte aligned", uint64(pc), uint64(v))
	}
	imm19 := off / 4
	if imm19 < -(1<<18) || imm19 >= (1<<18) {
		return 0, fmt.Errorf("ldr (literal) @ pc=0x%x: offset %d out of ±1MiB range", uint64(pc), off)
	}
	var base uint32
	if rt.Kind == format.OpRegX {
		base = 0x58000000
	} else {
		base = 0x18000000
	}
	return base | ((uint32(imm19) & 0x7ffff) << 5) | uint32(rt.Reg), nil
}

// encodeTbzTbnz encodes the TBZ / TBNZ instructions.
//
//	tbz  Rt, #bit, label  → bit 24 = 0 (op = TBZ)
//	tbnz Rt, #bit, label  → bit 24 = 1 (op = TBNZ)
//
// Encoding (ARM ARM C6.2.298 / C6.2.299):
//
//	bits 31    = b5 (bit number bit 5; 1 when Rt is X and bit ≥ 32)
//	bits 30:25 = 011011
//	bit  24    = op  (0 = TBZ, 1 = TBNZ)
//	bits 23:19 = b40 (bit number bits 4:0)
//	bits 18:5  = imm14 (signed PC-relative branch / 4)
//	bits 4:0   = Rt
func encodeTbzTbnz(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 3 {
		return 0, fmt.Errorf("tbz/tbnz: need 3 operands, got %d", len(operands))
	}
	rt := operands[0]
	bitOp := operands[1]
	labelOp := operands[2]
	if rt.Kind != format.OpRegX && rt.Kind != format.OpRegW {
		return 0, fmt.Errorf("tbz/tbnz: operand 0 must be Rt")
	}
	if bitOp.Kind != format.OpImmExpr || labelOp.Kind != format.OpImmExpr {
		return 0, fmt.Errorf("tbz/tbnz: operand 1 must be immediate, operand 2 must be label")
	}
	ctx := makeCtx(pc, p1, f)
	bit, err := EvalUsage(bitOp.Expr, ctx, usage)
	if err != nil {
		return 0, fmt.Errorf("tbz/tbnz bit: %w", err)
	}
	if bit < 0 || bit > 63 {
		return 0, fmt.Errorf("tbz/tbnz: bit number %d out of range [0,63]", bit)
	}
	if rt.Kind == format.OpRegW && bit > 31 {
		return 0, fmt.Errorf("tbz/tbnz: bit %d > 31 with W register", bit)
	}
	target, err := EvalUsage(labelOp.Expr, ctx, usage)
	if err != nil {
		return 0, fmt.Errorf("tbz/tbnz label: %w", err)
	}
	off := target - pc
	if off%4 != 0 {
		return 0, fmt.Errorf("tbz/tbnz: target 0x%x not 4-byte aligned (pc=0x%x)", uint64(target), uint64(pc))
	}
	imm14 := off / 4
	if imm14 < -(1<<13) || imm14 >= (1<<13) {
		return 0, fmt.Errorf("tbz/tbnz: offset %d out of ±32 KiB range", off)
	}
	b5 := uint32(bit>>5) & 1
	b40 := uint32(bit & 0x1f)
	op := uint32(0)
	if mnemonicID == 23 { // tbnz
		op = 1
	}
	word := (b5 << 31) | (uint32(0b011011) << 25) | (op << 24) |
		(b40 << 19) | ((uint32(imm14) & 0x3fff) << 5) | uint32(rt.Reg)
	return word, nil
}

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
	// ldr=5, ldp=7, ldrb=54, ldrh=56, ldur=75,
	// ldrsb=85, ldrsh=86, ldrsw=87, ldurh=96, ldurb=97.
	switch mnemonicID {
	case 5, 7, 54, 56, 75, 85, 86, 87, 96, 97:
		return true
	}
	return false
}

// isSignedExtendLoad reports ldrsb / ldrsh / ldrsw (ARM ARM C6.2.117 /
// C6.2.118 / C6.2.119). These share the LDR/STR encoding template but
// use opc=10 (signed load to Xt) or opc=11 (signed load to Wt) at bits
// 23:22 rather than the regular load's opc=01.
func isSignedExtendLoad(mnemonicID uint16) bool {
	return mnemonicID == 85 || mnemonicID == 86 || mnemonicID == 87
}

// isUnscaledMemMnemonic reports the unscaled load/store family
// (ARM ARM C6.2.276 / C6.2.124 and the halfword/byte variants).
// stur=74, ldur=75, sturh=94, sturb=95, ldurh=96, ldurb=97.
func isUnscaledMemMnemonic(mnemonicID uint16) bool {
	switch mnemonicID {
	case 74, 75, 94, 95, 96, 97:
		return true
	}
	return false
}

// memInstSize returns the AArch64 "size" field (bits 31:30) and the byte
// scale factor for a given load/store mnemonic + Rt-kind.
// ldr/str: size depends on Rt kind: X → 11 (8-byte), W → 10 (4-byte)
// ldrb/strb/ldrsb: size=00 (byte), scale=1
// ldrh/strh/ldrsh: size=01 (halfword), scale=2
// ldrsw: size=10 (word), scale=4
func memInstSize(mnemonicID uint16, rtKind format.OperandKind) (sizeBits uint32, scale int64) {
	switch mnemonicID {
	case 54, 55, 85: // ldrb, strb, ldrsb
		return 0b00, 1
	case 56, 57, 86: // ldrh, strh, ldrsh
		return 0b01, 2
	case 87: // ldrsw
		return 0b10, 4
	default: // ldr(5), str(6), ldp(7), stp(8)
		if rtKind == format.OpRegW {
			return 0b10, 4
		}
		return 0b11, 8
	}
}

// memInstOpc returns the 2-bit opc field (bits 23:22) for a load/store
// instruction. For STR/STRB/STRH this is 00; for LDR/LDRB/LDRH it is 01.
// For LDRSB/LDRSH/LDRSW the opc selects the destination width: 10 when
// Rt is Xt (sign-extend to 64-bit) and 11 when Rt is Wt (sign-extend to
// 32-bit). LDRSW only has an Xt form.
func memInstOpc(mnemonicID uint16, rtKind format.OperandKind) (uint32, error) {
	if isSignedExtendLoad(mnemonicID) {
		switch rtKind {
		case format.OpRegX:
			if mnemonicID == 87 {
				return 0b10, nil // ldrsw (Xt only)
			}
			return 0b10, nil
		case format.OpRegW:
			if mnemonicID == 87 {
				return 0, fmt.Errorf("ldrsw: Rt must be Xn (no Wt form)")
			}
			return 0b11, nil
		default:
			return 0, fmt.Errorf("ldrs*: Rt must be Wn or Xn (got kind %v)", rtKind)
		}
	}
	if isLoadMnemonic(mnemonicID) {
		return 0b01, nil
	}
	return 0b00, nil
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

	// STUR / LDUR (unscaled signed-9-bit offset). ARM ARM C6.2.276 /
	// C6.2.124. Size is derived from Rt's width (W = 0b10, X = 0b11).
	if isUnscaledMemMnemonic(mnemonicID) {
		return encodeUnscaledMemInst(mnemonicID, operands[0], rt, mem, pc, p1, f)
	}

	sizeBits, scale := memInstSize(mnemonicID, operands[0].Kind)
	opc, err := memInstOpc(mnemonicID, operands[0].Kind)
	if err != nil {
		return 0, err
	}

	// AArch64 load/store encoding:
	// bits[31:30] = size (00=byte, 01=halfword, 10=word, 11=doubleword)
	// bits[29:27] = 111 (VFP=0 is 0b111_0; V=0 → 0b111, bit26=0)
	// bits[25:24] = addressing mode (01=unsigned-offset, 00=pre/post/unscaled)
	// bits[23:22] = opc (01=load, 00=store, 10=signed-load-to-Xt,
	//                    11=signed-load-to-Wt)
	const vfpMid = uint32(0b111) << 27

	switch mem.MemShape {
	case format.MemBase, format.MemBaseOff:
		// LDR/STR/LDRSx (unsigned offset): size(2)|111|01|opc(2)|imm12|Rn|Rt
		// bits 25:24 = 01 (unsigned offset encoding)
		base := (sizeBits << 30) | vfpMid | (uint32(1) << 24) | (opc << 22)
		// imm12 is a scaled (unsigned) offset: byte_offset / scale
		var byteOffset int64
		if mem.MemShape == format.MemBaseOff {
			ctx := makeCtx(pc, p1, f)
			v, err := EvalUsage(mem.Expr, ctx, usage)
			if err != nil {
				return 0, err
			}
			byteOffset = v
		}
		if byteOffset < 0 || byteOffset%scale != 0 || byteOffset/scale >= (1<<12) {
			// GNU as transparently rewrites `str/ldr Rt, [Rn, #-N]`
			// (and similar non-scaled offsets) to the STUR/LDUR
			// unscaled-signed-9-bit form. Mirror that here so
			// spectrum4's `str x2, [x3, #-0x10]` idioms encode.
			if byteOffset >= -256 && byteOffset <= 255 {
				return encodeUnscaledMemInst(mnemonicID, operands[0], rt, mem, pc, p1, f)
			}
			return 0, fmt.Errorf("LDR/STR unsigned offset: byte offset %d not representable as scaled imm12 (must be 0..%d, multiple of %d)", byteOffset, scale*(1<<12-1), scale)
		}
		imm12 := uint32(byteOffset / scale)
		word := base | (imm12 << 10) | (uint32(mem.Base) << 5) | uint32(rt)
		return word, nil

	case format.MemBaseOffPre:
		// LDR/STR (immediate, pre-index): size(2)|111|00|opc(2)|0|imm9|11|Rn|Rt
		// bits 25:24 = 00 (unscaled/pre/post)
		// bit  21    = 0
		// bits 20:12 = imm9 (signed)
		// bits 11:10 = 11 (pre-index)
		base := (sizeBits << 30) | vfpMid | (opc << 22) | (uint32(3) << 10)
		ctx := makeCtx(pc, p1, f)
		v, err := EvalUsage(mem.Expr, ctx, usage)
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
		base := (sizeBits << 30) | vfpMid | (opc << 22) | (uint32(1) << 10)
		ctx := makeCtx(pc, p1, f)
		v, err := EvalUsage(mem.Expr, ctx, usage)
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
		const optionLSL = uint32(0b011)
		base := (sizeBits << 30) | vfpMid | (opc << 22) | (uint32(1) << 21) |
			(optionLSL << 13) | (uint32(0b10) << 10)
		word := base | (uint32(mem.Idx) << 16) | (uint32(mem.Base) << 5) | uint32(rt)
		return word, nil

	case format.MemBaseIdxShifted:
		// LDR/STR (register, LSL): Xt, [Xn, Xm, LSL #N]
		// Same as MemBaseIdx but S=1; S lives at bit 12.
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

// encodeUnscaledMemInst encodes STUR (75) and LDUR (76). The base
// pattern depends on Rt's width and the load/store direction:
//
//	stur W: 0xb8000000 | (imm9 << 12) | (Rn << 5) | Rt
//	stur X: 0xf8000000 | ...
//	ldur W: 0xb8400000 | ...
//	ldur X: 0xf8400000 | ...
//
// imm9 is a signed byte offset (-256..+255); no scaling. Only the
// MemBase and MemBaseOff shapes are accepted — pre/post-index aren't
// the STUR/LDUR family.
func encodeUnscaledMemInst(mnemonicID uint16, rtOp format.Operand, rt byte, mem format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	isLoad := isLoadMnemonic(mnemonicID)
	// sizeBits (bits 31:30): halfword variants fix size=01, byte variants fix
	// size=00; stur/ldur derive size from Rt width (W→10, X→11).
	var sizeBits uint32
	switch mnemonicID {
	case 94, 96: // sturh, ldurh — halfword
		sizeBits = 0b01
		if rtOp.Kind != format.OpRegW {
			return 0, fmt.Errorf("sturh/ldurh: Rt must be Wn (got kind %v)", rtOp.Kind)
		}
	case 95, 97: // sturb, ldurb — byte
		sizeBits = 0b00
		if rtOp.Kind != format.OpRegW {
			return 0, fmt.Errorf("sturb/ldurb: Rt must be Wn (got kind %v)", rtOp.Kind)
		}
	default: // stur=74, ldur=75 — size from Rt width
		switch rtOp.Kind {
		case format.OpRegW:
			sizeBits = 0b10
		case format.OpRegX:
			sizeBits = 0b11
		default:
			return 0, fmt.Errorf("stur/ldur: Rt must be Wn or Xn (got kind %v)", rtOp.Kind)
		}
	}
	var byteOffset int64
	switch mem.MemShape {
	case format.MemBase:
		byteOffset = 0
	case format.MemBaseOff:
		ctx := makeCtx(pc, p1, f)
		v, err := EvalUsage(mem.Expr, ctx, usage)
		if err != nil {
			return 0, err
		}
		byteOffset = v
	default:
		return 0, fmt.Errorf("stur/ldur: unsupported MemShape %v (only base + signed offset)", mem.MemShape)
	}
	imm9, err := encodeSignedImm9(byteOffset)
	if err != nil {
		return 0, fmt.Errorf("stur/ldur: %w", err)
	}
	// bits[29:24] = 0b111000 (vfpMid | mode=00). bits[23:22] = opc = 01
	// for load, 00 for store. bits[11:10] = 00 for STUR/LDUR (unscaled).
	const vfpMid = uint32(0b111) << 27
	opc := uint32(0)
	if isLoad {
		opc = uint32(1)
	}
	base := (sizeBits << 30) | vfpMid | (opc << 22)
	word := base | (imm9 << 12) | (uint32(mem.Base) << 5) | uint32(rt)
	return word, nil
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
		v, err := EvalUsage(mem.Expr, ctx, usage)
		if err != nil {
			return 0, err
		}
		byteOffset = v
		modeBits = 0b10 // signed offset
	case format.MemBaseOffPre:
		ctx := makeCtx(pc, p1, f)
		v, err := EvalUsage(mem.Expr, ctx, usage)
		if err != nil {
			return 0, err
		}
		byteOffset = v
		modeBits = 0b11 // pre-index
	case format.MemBaseOffPost:
		ctx := makeCtx(pc, p1, f)
		v, err := EvalUsage(mem.Expr, ctx, usage)
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
	amt, err := EvalUsage(sr.AmtExpr, ctx, usage)
	if err != nil {
		return 0, fmt.Errorf("shift amount: %w", err)
	}
	if amt < 0 || amt > 63 {
		return 0, fmt.Errorf("shift amount %d out of range [0,63]", amt)
	}

	// Determine sf (64-bit flag), opc, N bit, and the logical/arithmetic
	// op class from mnemonic.
	//
	// Arithmetic shifted-reg (ADD/SUB/ADDS/SUBS) and logical shifted-reg
	// (AND/ORR/EOR/ANDS/BIC/ORN/EON) share most of the field layout but
	// differ in bits 28..24:
	//   arithmetic: 01011
	//   logical:    01010
	sf, opc, nBit, isLogical, err := shiftedRegMnemonicFields(mnemonicID, sr.Width == 1)
	if err != nil {
		return 0, err
	}

	// Encoding: sf(1)|opc(2)|0101X|shift(2)|N(1)|Rm(5)|imm6(6)|Rn(5)|Rd(5)
	mid := uint32(0b01011)
	if isLogical {
		mid = 0b01010
	}
	shiftEnc := uint32(sr.ShiftKind)
	word := (sf << 31) | (opc << 29) | mid<<24 |
		(shiftEnc << 22) | (nBit << 21) | (uint32(sr.Reg) << 16) |
		(uint32(amt) << 10) | (uint32(rn) << 5) | uint32(rd)
	return word, nil
}

// isShiftedRegMnemonic reports whether a mnemonic supports the
// shifted-register addressing form. Used by pass2 to coerce three
// plain-register operands into a no-shift ShiftedReg.
func isShiftedRegMnemonic(id uint16) bool {
	switch id {
	case 1, 2, 14, 15, 16, 45, 46, 47, 80, 98: // add, sub, and, orr, eor, subs, tst, bic, ands, adds
		return true
	}
	return false
}

// isPlainGPR reports whether kind is one of the plain GPR kinds
// (X, W, X-SP, W-SP) — i.e. anything that came in as a single
// register without an attached shift/extend.
func isPlainGPR(k format.OperandKind) bool {
	switch k {
	case format.OpRegX, format.OpRegW, format.OpRegXSP, format.OpRegWSP:
		return true
	}
	return false
}

// shiftedRegMnemonicFields returns sf, opc, N-bit, and an isLogical flag
// (true for AND/ORR/EOR/BIC/ORN/EON/ANDS/TST family — bits 28..24 = 01010;
// false for ADD/SUB/ADDS/SUBS — bits 28..24 = 01011).
// is64 is true when the operands are X registers.
// N=1 distinguishes BIC/ORN/EON from AND/ORR/EOR in the shifted-reg space.
func shiftedRegMnemonicFields(mnemonicID uint16, is64 bool) (sf, opc, nBit uint32, isLogical bool, err error) {
	sf = 0
	if is64 {
		sf = 1
	}
	switch mnemonicID {
	case 1: // add
		return sf, 0b00, 0, false, nil
	case 2: // sub
		return sf, 0b10, 0, false, nil
	case 14: // and (shifted-reg, N=0)
		return sf, 0b00, 0, true, nil
	case 15: // orr (shifted-reg, N=0)
		return sf, 0b01, 0, true, nil
	case 16: // eor (shifted-reg, N=0)
		return sf, 0b10, 0, true, nil
	case 45: // subs (shifted-reg): opc=11
		return sf, 0b11, 0, false, nil
	case 98: // adds (shifted-reg): opc=01
		return sf, 0b01, 0, false, nil
	case 46: // tst = ands (shifted-reg, N=0): opc=11 (ANDS discards result)
		return sf, 0b11, 0, true, nil
	case 47: // bic (shifted-reg, N=1): AND NOT
		return sf, 0b00, 1, true, nil
	case 80: // ands (shifted-reg, N=0): opc=11
		return sf, 0b11, 0, true, nil
	default:
		return 0, 0, 0, false, fmt.Errorf("shiftedReg: unsupported mnemonic id %d", mnemonicID)
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
		amt, err = EvalUsage(er.AmtExpr, ctx, usage)
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

// encodeLSLSR encodes lsl and lsr in both forms:
//
//	immediate (UBFM alias):  lsl/lsr Rd, Rn, #shift
//	register  (LSLV/LSRV):   lsl/lsr Rd, Rn, Rm
//
// LSL imm: immr = (-shift) mod regsize, imms = regsize - 1 - shift
// LSR imm: immr = shift, imms = regsize - 1
// LSLV:    32-bit 0x1ac02000 | (Rm<<16) | (Rn<<5) | Rd
//          64-bit 0x9ac02000 | ...
// LSRV:    32-bit 0x1ac02400 | (Rm<<16) | (Rn<<5) | Rd
//          64-bit 0x9ac02400 | ...
func encodeLSLSR(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 3 {
		return 0, fmt.Errorf("lsl/lsr: need 3 operands, got %d", len(operands))
	}
	rd := operands[0]
	rn := operands[1]
	immOp := operands[2]
	// Register-shift form: dispatch to LSLV / LSRV.
	if immOp.Kind == format.OpRegX || immOp.Kind == format.OpRegW {
		is64 := rd.Kind == format.OpRegX
		var base uint32
		if mnemonicID == 17 { // lsl → LSLV
			if is64 {
				base = 0x9ac02000
			} else {
				base = 0x1ac02000
			}
		} else { // lsr → LSRV
			if is64 {
				base = 0x9ac02400
			} else {
				base = 0x1ac02400
			}
		}
		word := base | (uint32(immOp.Reg) << 16) | (uint32(rn.Reg) << 5) | uint32(rd.Reg)
		return word, nil
	}
	if immOp.Kind != format.OpImmExpr {
		return 0, fmt.Errorf("lsl/lsr: operand 2 must be an immediate or register")
	}

	ctx := makeCtx(pc, p1, f)
	shift, err := EvalUsage(immOp.Expr, ctx, usage)
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

// encodeBitfieldInst encodes bfi / bfxil / ubfx / bfc / sbfx.
// All five use BFM/SBFM/UBFM bases with computed immr/imms.
//
// BFI  (49): Rd, Rn,  #lsb, #width → BFM  immr=(-lsb)%regsize, imms=width-1
// BFXIL(50): Rd, Rn,  #lsb, #width → BFM  immr=lsb,            imms=lsb+width-1
// UBFX (51): Rd, Rn,  #lsb, #width → UBFM immr=lsb,            imms=lsb+width-1
// BFC  (84): Rd,      #lsb, #width → BFM Rn=XZR, immr=(-lsb)%regsize, imms=width-1
// SBFX (85): Rd, Rn,  #lsb, #width → SBFM immr=lsb,            imms=lsb+width-1
func encodeBitfieldInst(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	// BFC is a 3-operand alias (no Rn — it is implicitly XZR).
	isBfc := mnemonicID == 83
	wantOps := 4
	if isBfc {
		wantOps = 3
	}
	if len(operands) < wantOps {
		return 0, fmt.Errorf("bitfield: need %d operands, got %d", wantOps, len(operands))
	}
	rd := operands[0]
	var rn format.Operand
	var lsbOp, widthOp format.Operand
	if isBfc {
		// Rn is implicitly XZR (31). Build a synthetic operand matching Rd's width.
		rn = format.Operand{Kind: rd.Kind, Reg: 31}
		lsbOp = operands[1]
		widthOp = operands[2]
	} else {
		rn = operands[1]
		lsbOp = operands[2]
		widthOp = operands[3]
	}

	if lsbOp.Kind != format.OpImmExpr || widthOp.Kind != format.OpImmExpr {
		return 0, fmt.Errorf("bitfield: lsb and width must be immediates")
	}

	ctx := makeCtx(pc, p1, f)
	lsb, err := EvalUsage(lsbOp.Expr, ctx, usage)
	if err != nil {
		return 0, fmt.Errorf("bitfield lsb: %w", err)
	}
	width, err := EvalUsage(widthOp.Expr, ctx, usage)
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
	case 83: // bfc: BFM alias with Rn=XZR — immr=(-lsb)%regsize, imms=width-1
		immr = uint32((-lsb)&(regsize-1)) & 0x3F
		imms = uint32(width-1) & 0x3F
		if is64 {
			base = 0xb3400000 // BFM 64-bit
		} else {
			base = 0x33000000 // BFM 32-bit
		}
	case 84: // sbfx: SBFM alias — immr=lsb, imms=lsb+width-1
		immr = uint32(lsb) & 0x3F
		imms = uint32(lsb+width-1) & 0x3F
		if is64 {
			base = 0x93400000 // SBFM 64-bit
		} else {
			base = 0x13000000 // SBFM 32-bit
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
	imm, err := EvalUsage(immOp.Expr, ctx, usage)
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

// ---------------------------------------------------------------------------
// ROR immediate — alias of EXTR Rd, Rn, Rn, #imm
// ---------------------------------------------------------------------------

// encodeRorImm encodes `ror Rd, Rn, #shift` as the EXTR alias.
// ARM ARM C6.2.196: ROR (immediate) = EXTR Rd, Rn, Rn, #imm.
//
//	32-bit EXTR: 0x13800000 | (Rm<<16) | (imms<<10) | (Rn<<5) | Rd  (Rm=Rn)
//	64-bit EXTR: 0x93c00000 | (Rm<<16) | (imms<<10) | (Rn<<5) | Rd  (Rm=Rn, N=1)
func encodeRorImm(operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 3 {
		return 0, fmt.Errorf("ror imm: need 3 operands, got %d", len(operands))
	}
	rd := operands[0]
	rn := operands[1]
	immOp := operands[2]
	if immOp.Kind != format.OpImmExpr {
		return 0, fmt.Errorf("ror imm: operand 2 must be an immediate")
	}
	ctx := makeCtx(pc, p1, f)
	shift, err := EvalUsage(immOp.Expr, ctx, usage)
	if err != nil {
		return 0, fmt.Errorf("ror imm shift: %w", err)
	}
	is64 := rd.Kind == format.OpRegX
	regsize := int64(64)
	if !is64 {
		regsize = 32
	}
	if shift < 0 || shift >= regsize {
		return 0, fmt.Errorf("ror imm: shift %d out of range [0,%d)", shift, regsize)
	}
	var base uint32
	if is64 {
		base = 0x93c00000
	} else {
		base = 0x13800000
	}
	rnIdx := uint32(rn.Reg)
	word := base | (rnIdx << 16) | (uint32(shift) << 10) | (rnIdx << 5) | uint32(rd.Reg)
	return word, nil
}

// ---------------------------------------------------------------------------
// Barrier instructions: isb, dsb, dmb
// ---------------------------------------------------------------------------

// encodeBarrierInst encodes isb / dsb / dmb. The single operand is an
// immediate carrying the CRm value (bits 11:8) — the parser converts
// the barrier-arg keyword (sy, st, ld, ish, ...) to the CRm value per
// ARM ARM C6.2.74 (DSB) / C6.2.73 (DMB) / C6.2.99 (ISB).
//
//	isb (66) base: 0xd50330df ( | (CRm << 8) )
//	dsb (67) base: 0xd503309f ( | (CRm << 8) )
//	dmb (68) base: 0xd50330bf ( | (CRm << 8) )
func encodeBarrierInst(mnemonicID uint16, operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 1 {
		return 0, fmt.Errorf("barrier: need 1 operand, got %d", len(operands))
	}
	if operands[0].Kind != format.OpImmExpr {
		return 0, fmt.Errorf("barrier: operand 0 must be an immediate")
	}
	ctx := makeCtx(pc, p1, f)
	crm, err := EvalUsage(operands[0].Expr, ctx, usage)
	if err != nil {
		return 0, fmt.Errorf("barrier CRm: %w", err)
	}
	if crm < 0 || crm > 0xf {
		return 0, fmt.Errorf("barrier CRm %d out of range [0,15]", crm)
	}
	var base uint32
	switch mnemonicID {
	case 66: // isb
		base = 0xd50330df
	case 67: // dsb
		base = 0xd503309f
	case 68: // dmb
		base = 0xd50330bf
	default:
		return 0, fmt.Errorf("barrier: unsupported mnemonic id %d", mnemonicID)
	}
	word := base | (uint32(crm) << 8)
	return word, nil
}

// ---------------------------------------------------------------------------
// System-register access (mrs / msr) and system instructions (dc / tlbi)
// ---------------------------------------------------------------------------

// encodeMrs encodes `mrs Xt, <sysreg>` per ARM ARM C6.2.137.
//
//	1101 0101 0011 op0:1 op1:3 CRn:4 CRm:4 op2:3 Rt:5
//	base 0xd5300000 | (op0<<19) | (op1<<16) | (CRn<<12) | (CRm<<8) | (op2<<5) | Rt
func encodeMrs(operands []format.Operand) (uint32, error) {
	if len(operands) < 2 {
		return 0, fmt.Errorf("mrs: need 2 operands, got %d", len(operands))
	}
	if operands[0].Kind != format.OpRegX {
		return 0, fmt.Errorf("mrs: operand 0 must be Xt")
	}
	if operands[1].Kind != format.OpSysName {
		return 0, fmt.Errorf("mrs: operand 1 must be a system register name")
	}
	sr, ok := format.ParseSysReg(string(operands[1].Str))
	if !ok {
		return 0, fmt.Errorf("mrs: unknown system register %q", string(operands[1].Str))
	}
	return sysRegEncode(0xd5300000, sr, operands[0].Reg), nil
}

// encodeMsr encodes both forms of `msr`:
//
//   - Register form `msr <sysreg>, Xt` (ARM ARM C6.2.141):
//     1101 0101 0001 op0:1 op1:3 CRn:4 CRm:4 op2:3 Rt:5
//     base 0xd5100000 | (op0<<19) | (op1<<16) | (CRn<<12) | (CRm<<8) | (op2<<5) | Rt
//
//   - PSTATE-immediate form `msr <pstatefield>, #imm` (ARM ARM C6.2.140):
//     1101 0101 0000 op1:3 0100 CRm:4 op2:3 11111
//     base 0xd500401f | (op1<<16) | (CRm<<8) | (op2<<5)
//     where CRm carries the 4-bit immediate.
func encodeMsr(operands []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (uint32, error) {
	if len(operands) < 2 {
		return 0, fmt.Errorf("msr: need 2 operands, got %d", len(operands))
	}
	if operands[0].Kind != format.OpSysName {
		return 0, fmt.Errorf("msr: operand 0 must be a system register or PSTATE field name")
	}
	name := string(operands[0].Str)

	switch operands[1].Kind {
	case format.OpRegX:
		sr, ok := format.ParseSysReg(name)
		if !ok {
			return 0, fmt.Errorf("msr (register): unknown system register %q", name)
		}
		return sysRegEncode(0xd5100000, sr, operands[1].Reg), nil
	case format.OpImmExpr:
		pf, ok := format.ParsePState(name)
		if !ok {
			return 0, fmt.Errorf("msr (immediate): unknown PSTATE field %q", name)
		}
		ctx := makeCtx(pc, p1, f)
		imm, err := EvalUsage(operands[1].Expr, ctx, usage)
		if err != nil {
			return 0, fmt.Errorf("msr (immediate): %w", err)
		}
		if imm < 0 || imm > 0xf {
			return 0, fmt.Errorf("msr (immediate): #imm %d out of range [0,15]", imm)
		}
		word := uint32(0xd500401f) |
			(uint32(pf.Op1) << 16) |
			(uint32(imm) << 8) |
			(uint32(pf.Op2) << 5)
		return word, nil
	}
	return 0, fmt.Errorf("msr: operand 1 must be a register or immediate (got kind %v)", operands[1].Kind)
}

// encodeDc encodes `dc <op>, Xt` as the SYS-instruction encoding:
//
//	1101 0101 0000 1 op1:3 0111 CRm:4 op2:3 Rt:5
//	base 0xd5080000 | (op1<<16) | (CRn<<12) | (CRm<<8) | (op2<<5) | Rt
//
// All DC ops have CRn=7 — that's what makes them DC rather than another
// SYS family — but we read CRn from the op table for clarity.
func encodeDc(operands []format.Operand) (uint32, error) {
	if len(operands) < 2 {
		return 0, fmt.Errorf("dc: need 2 operands, got %d", len(operands))
	}
	if operands[0].Kind != format.OpSysName {
		return 0, fmt.Errorf("dc: operand 0 must be op name")
	}
	if operands[1].Kind != format.OpRegX {
		return 0, fmt.Errorf("dc: operand 1 must be Xt")
	}
	op, ok := format.ParseDC(string(operands[0].Str))
	if !ok {
		return 0, fmt.Errorf("dc: unknown op %q", string(operands[0].Str))
	}
	word := uint32(0xd5080000) |
		(uint32(op.Op1) << 16) |
		(uint32(op.CRn) << 12) |
		(uint32(op.CRm) << 8) |
		(uint32(op.Op2) << 5) |
		uint32(operands[1].Reg)
	return word, nil
}

// encodeTlbi encodes `tlbi <op>[, Xt]` as the SYS-instruction encoding.
// Same base pattern as dc but the op table fixes CRn=8 (TLBI family).
// Ops with no Xt operand encode Rt=11111 (xzr), matching GNU's output.
func encodeTlbi(operands []format.Operand) (uint32, error) {
	if len(operands) < 1 {
		return 0, fmt.Errorf("tlbi: need at least 1 operand")
	}
	if operands[0].Kind != format.OpSysName {
		return 0, fmt.Errorf("tlbi: operand 0 must be op name")
	}
	op, ok := format.ParseTLBI(string(operands[0].Str))
	if !ok {
		return 0, fmt.Errorf("tlbi: unknown op %q", string(operands[0].Str))
	}
	rt := byte(31) // xzr when no Xt is given
	if len(operands) >= 2 {
		if operands[1].Kind != format.OpRegX {
			return 0, fmt.Errorf("tlbi: operand 1 must be Xt")
		}
		if !op.NeedsXt {
			return 0, fmt.Errorf("tlbi: op %q does not take a register operand", string(operands[0].Str))
		}
		rt = operands[1].Reg
	} else if op.NeedsXt {
		return 0, fmt.Errorf("tlbi: op %q requires an Xt operand", string(operands[0].Str))
	}
	word := uint32(0xd5080000) |
		(uint32(op.Op1) << 16) |
		(uint32(op.CRn) << 12) |
		(uint32(op.CRm) << 8) |
		(uint32(op.Op2) << 5) |
		uint32(rt)
	return word, nil
}

// sysRegEncode packs the (op0, op1, CRn, CRm, op2, Rt) fields onto a
// base pattern for the MRS/MSR (register-form) encodings. The base
// pattern supplies the L bit that distinguishes read (mrs) from write
// (msr-reg).
func sysRegEncode(base uint32, sr format.SysReg, rt byte) uint32 {
	return base |
		(uint32(sr.Op0&1) << 19) |
		(uint32(sr.Op1) << 16) |
		(uint32(sr.CRn) << 12) |
		(uint32(sr.CRm) << 8) |
		(uint32(sr.Op2) << 5) |
		uint32(rt)
}

func encodeDirective(rec format.Record, pc int64, p1 *Pass1Result, f *format.File) ([]byte, error) {
	name := format.DirectiveName(rec.DirectiveID)
	ctx := makeCtx(pc, p1, f)
	switch name {
	case ".byte":
		return evalImmsAsBytes(rec, ctx, 1)
	case ".short", ".hword":
		return evalImmsAsBytes(rec, ctx, 2)
	case ".word":
		return evalImmsAsBytes(rec, ctx, 4)
	case ".quad":
		return evalImmsAsBytes(rec, ctx, 8)
	case ".ascii":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		// Copy o.Str: OperandReader returns a view into the source
		// buffer; returning that slice directly is fine, but any
		// caller-side append would overwrite subsequent records.
		out := make([]byte, len(o.Str))
		copy(out, o.Str)
		return out, nil
	case ".asciz":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		// Append the terminating NUL into a fresh slice to avoid
		// mutating the underlying record buffer (the original cause
		// of subsequent directives being misread — issue surfaced
		// during M3 release-build byte-match).
		out := make([]byte, len(o.Str)+1)
		copy(out, o.Str)
		return out, nil
	case ".inst":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		v, err := EvalUsage(o.Expr, ctx, usage)
		if err != nil {
			return nil, fmt.Errorf(".inst: %w", err)
		}
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(v))
		return b, nil
	case ".text", ".data", ".global", ".equ", ".set":
		return nil, nil
	case ".section":
		// .section is a no-op in the current flat-layout pipeline. See
		// docs/notes/m2-status.md for the multi-section gap this leaves.
		return nil, nil
	case ".arch", ".cpu":
		// .arch / .cpu architecture-selection directives are no-ops at
		// encoding time — see pass1 sizeOfRecord for rationale.
		return nil, nil
	case ".balign":
		// Round PC up to next multiple of alignment. In .text the
		// padding bytes are NOP (0xd503201f) instructions when both
		// the current PC and the pad length are 4-byte aligned —
		// mirrors GNU as's behaviour in `.text` sections.
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		align, err := EvalUsage(o.Expr, ctx, usage)
		if err != nil {
			return nil, fmt.Errorf(".balign: %w", err)
		}
		if align <= 1 {
			return nil, nil
		}
		ua := uint64(align)
		up := uint64(pc)
		pad := int64((ua - (up % ua)) % ua)
		return alignPadBytes(pc, pad), nil
	case ".align":
		// aarch64 GNU as convention: `.align N` aligns to 2^N bytes.
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		n, err := EvalUsage(o.Expr, ctx, usage)
		if err != nil {
			return nil, fmt.Errorf(".align: %w", err)
		}
		if n <= 0 {
			return nil, nil
		}
		align := int64(1) << uint64(n)
		ua := uint64(align)
		up := uint64(pc)
		pad := int64((ua - (up % ua)) % ua)
		return alignPadBytes(pc, pad), nil
	case ".skip", ".space":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		v, err := EvalUsage(o.Expr, ctx, usage)
		if err != nil {
			return nil, fmt.Errorf(".skip: %w", err)
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
		v, err := EvalUsage(o.Expr, ctx, usage)
		if err != nil {
			return nil, err
		}
		for i := 0; i < size; i++ {
			out = append(out, byte(v>>(8*i)))
		}
	}
	return out, nil
}
