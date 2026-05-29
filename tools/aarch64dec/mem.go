package aarch64dec

import "fmt"

// mem.go — special-case decoders for the load/store (memory) instruction
// family.  These instructions are NOT in aarch64enc.AllForms() (the encoder
// hand-rolls them in refenc/pass2.go's encodeMemInst / encodePairInst /
// encodeUnscaledMemInst), so the form-table walk in DecodeAt can't reach
// them.  decodeMem is the inverse of those three encoder functions.
//
// The two encoding families handled here, identified by bits[29:26]:
//
//	0b1110 — scalar load/store (single register).  Inverse of
//	         encodeMemInst / encodeUnscaledMemInst.  Layout (refenc
//	         pass2.go ~749):
//	           size[31:30] | 111 | V[26]=0 | mode[25:24] | opc[23:22]
//	           | <imm/Rm> | Rn[9:5] | Rt[4:0]
//	0b1010 — load/store pair.  Inverse of encodePairInst (pass2.go
//	         ~918):
//	           opc[31:30] | 101 | V[26]=0 | mode[24:23] | L[22]
//	           | imm7[21:15] | Rt2[14:10] | Rn[9:5] | Rt1[4:0]
//
// All offsets render as signed/unsigned *decimal* (`#8`, `#-16`) — verified
// against objdump, which uses decimal for memory offsets even though it uses
// hex for add/movz immediates (see decodeImm12Shifted).  A zero offset is
// omitted entirely: `[x0]` not `[x0, #0]`.

// decodeMem decodes a load/store word.  ok=false when the word is not in the
// scalar/pair load-store space this file owns, or when it falls in a sub-
// encoding we don't render (so DecodeAt can fall back to .inst, matching
// objdump's `undefined`).
func decodeMem(pc uint64, word uint32) (mnem string, operands string, ok bool) {
	switch (word >> 26) & 0xf {
	case 0b1110:
		return decodeScalarMem(word)
	case 0b1010:
		// Load/store register pair requires bit25==0 (the group selector
		// is bits[29:25]==0b1010_0).  Other instructions share bits[29:26]
		// ==1010 with bit25==1 — notably add/sub-with-flags shifted
		// register (adds/subs, e.g. the cmn/cmp aliases at 0x2b03001f).
		// Without this guard the pair decoder spuriously claims those.
		if (word>>25)&1 == 0 {
			return decodePairMem(word)
		}
	case 0b0110:
		// LDR (literal) requires bits[25:24]==00 in addition to
		// bits[29:26]==0110.  Other instructions share the bits[29:26]
		// signature with bits[25:24]!=00 — notably conditional-select
		// (csel, bits[25:24]=10) and data-processing-3-source (madd/
		// umulh/umaddl/..., bits[25:24]=11).  Without this guard the
		// literal-LDR decoder spuriously claims those words; decline so
		// the alias/Form decoders handle them.
		if (word>>24)&0x3 == 0 {
			return decodeLiteralMem(pc, word)
		}
	}
	return "", "", false
}

// decodeLiteralMem decodes the PC-relative LDR (literal) family
// (ARM ARM C6.2.121 / C6.2.123).  Bits[29:26]=0b0110.  Layout:
//
//	opc[31:30] | 011 | V[26]=0 | 00[25:24] | imm19[23:5] | Rt[4:0]
//
//	opc 00 → ldr  Wt   opc 01 → ldr  Xt   opc 10 → ldrsw Xt
//
// The target is pc + (sext(imm19) << 2), rendered as a hex address
// (objdump uses `0x...` here, unlike the decimal it uses for the
// base+offset forms).  These appear in spectrum4 as `ldr Rt, =literal`
// (literal-pool) loads — the encoder emits the pool entry separately so
// the literal LDR isn't in encodeMemInst, but objdump linear-sweeps it
// as real code, so we must decode it.
func decodeLiteralMem(pc uint64, word uint32) (string, string, bool) {
	opc := (word >> 30) & 0x3
	rt := word & 0x1f
	imm19 := (word >> 5) & 0x7ffff
	off := signExtend(imm19, 19) << 2
	target := pc + uint64(off)
	var mnem string
	var is64 bool
	switch opc {
	case 0b00:
		mnem, is64 = "ldr", false
	case 0b01:
		mnem, is64 = "ldr", true
	case 0b10:
		mnem, is64 = "ldrsw", true
	default:
		return "", "", false // opc 11 is PRFM (literal); SIMD uses V=1
	}
	return mnem, fmt.Sprintf("%s, %#x", memReg(rt, is64), target), true
}

// memReg renders a Rt/Rt1/Rt2 register given the size/opc-derived width.
// is64 picks x vs w; index 31 is the zero register (xzr/wzr), never sp.
func memReg(idx uint32, is64 bool) string {
	if is64 {
		return decodeReg(idx, "x", "xzr")
	}
	return decodeReg(idx, "w", "wzr")
}

// memBaseReg renders a base register Rn — always 64-bit, and index 31 is sp
// (the stack pointer is the canonical base; xzr is not addressable as a base).
func memBaseReg(idx uint32) string {
	return decodeReg(idx, "x", "sp")
}

// signExtend sign-extends the low `bits` of v to a signed int64.
// v is widened to 64-bit *before* the shift — shifting a uint32 by
// (64-bits) would overflow the 32-bit operand to zero.
func signExtend(v uint32, bits uint) int64 {
	shift := 64 - bits
	return int64(uint64(v)<<shift) >> shift
}

// memOffset renders an immediate offset as objdump does: omitted when zero,
// `, #N` (decimal, signed) otherwise.  base is the already-rendered base
// register text, e.g. "x0".
func memOffset(base string, off int64) string {
	if off == 0 {
		return fmt.Sprintf("[%s]", base)
	}
	return fmt.Sprintf("[%s, #%d]", base, off)
}

// scalarMnem maps (size, opc) → (mnemonic, is64-Rt) for the scalar
// load/store single-register family.  Mirrors memInstSize / memInstOpc /
// isLoadMnemonic / isSignedExtendLoad in refenc/pass2.go (~668-716).
//
//	size 00 → byte     : strb/ldrb (opc 00/01), ldrsb (opc 10 Xt / 11 Wt)
//	size 01 → halfword : strh/ldrh (opc 00/01), ldrsh (opc 10 Xt / 11 Wt)
//	size 10 → word     : str/ldr W (opc 00/01), ldrsw (opc 10 Xt)
//	size 11 → double   : str/ldr X (opc 00/01)
//
// ok=false for (size,opc) combinations objdump treats as undefined / a
// different family (e.g. size=11 opc=10/11 is PRFM, not emitted here).
func scalarMnem(size, opc uint32) (mnem string, is64 bool, ok bool) {
	switch size {
	case 0b00: // byte
		switch opc {
		case 0b00:
			return "strb", false, true
		case 0b01:
			return "ldrb", false, true
		case 0b10:
			return "ldrsb", true, true // sign-extend to Xt
		case 0b11:
			return "ldrsb", false, true // sign-extend to Wt
		}
	case 0b01: // halfword
		switch opc {
		case 0b00:
			return "strh", false, true
		case 0b01:
			return "ldrh", false, true
		case 0b10:
			return "ldrsh", true, true
		case 0b11:
			return "ldrsh", false, true
		}
	case 0b10: // word
		switch opc {
		case 0b00:
			return "str", false, true
		case 0b01:
			return "ldr", false, true
		case 0b10:
			return "ldrsw", true, true // Xt only
		}
	case 0b11: // doubleword
		switch opc {
		case 0b00:
			return "str", true, true
		case 0b01:
			return "ldr", true, true
		}
	}
	return "", false, false
}

// scaleFromSize returns the byte scale factor for the unsigned-offset
// imm12 (1<<size).
func scaleFromSize(size uint32) int64 { return int64(1) << size }

// decodeScalarMem decodes the scalar (single-register) load/store space:
// unsigned offset, pre/post-index, unscaled (stur/ldur), and register
// offset.  Bits[29:26] are already known to be 0b1110.
func decodeScalarMem(word uint32) (string, string, bool) {
	size := (word >> 30) & 0x3
	opc := (word >> 22) & 0x3
	mode := (word >> 24) & 0x3 // bits[25:24]
	rt := word & 0x1f
	rn := (word >> 5) & 0x1f

	if mode == 0b01 {
		// Unsigned immediate offset: imm12[21:10] scaled by 1<<size.
		mnem, is64, ok := scalarMnem(size, opc)
		if !ok {
			return "", "", false
		}
		imm12 := (word >> 10) & 0xfff
		off := int64(imm12) * scaleFromSize(size)
		ops := memReg(rt, is64) + ", " + memOffset(memBaseReg(rn), off)
		return mnem, ops, true
	}

	if mode == 0b00 {
		// bits[25:24]=00: unscaled / pre / post / register offset, split on
		// bits[11:10] and bit21.
		//
		// bit21 is the load/store-register sub-group discriminator in this
		// space (ARM ARM C4.1.4 "Loads and stores"):
		//
		//   bit21=0 : immediate forms — unscaled (STUR/LDUR, bits[11:10]=00),
		//             post-index (bits[11:10]=01), pre-index (bits[11:10]=11).
		//             bits[11:10]=10 with bit21=0 is undefined.
		//   bit21=1 : register-offset load/store ONLY when bits[11:10]=10.
		//             bits[11:10]=00 with bit21=1 is the ATOMIC memory-ops
		//             sub-space (LDADD/LDCLR/LDEOR/LDSET/LDSMAX/LDSMIN/
		//             LDUMAX/LDUMIN/SWP, e.g. 0x38307030 lduminb,
		//             0x78652079 ldeorlh); bits[11:10]=01/11 with bit21=1 are
		//             reserved/undefined (e.g. 0x7830fc38, 0xf83ff03f).
		//
		// The project's encoder only emits the immediate forms and the
		// register-offset form; everything else in this space is an atomic
		// (out of scope) or reserved encoding objdump renders `.inst`, so we
		// decline it.
		idxBits := (word >> 10) & 0x3 // bits[11:10]
		bit21 := (word >> 21) & 1
		switch idxBits {
		case 0b00:
			// STUR/LDUR family: unscaled signed imm9 at [20:12].
			// refenc encodeUnscaledMemInst (pass2.go ~874): GNU renders
			// these as stur/ldur, and the encoder rewrites out-of-range
			// str/ldr offsets into this form, so objdump shows stur/ldur.
			// bit21=1 here is the atomic memory-ops sub-space — decline.
			if bit21 != 0 {
				return "", "", false
			}
			mnem, is64, ok := sturMnem(size, opc)
			if !ok {
				return "", "", false
			}
			off := signExtend((word>>12)&0x1ff, 9)
			ops := memReg(rt, is64) + ", " + memOffset(memBaseReg(rn), off)
			return mnem, ops, true
		case 0b10:
			// Register offset (bit21 must be 1): [Rn, Rm{, ext/lsl #amt}].
			if bit21 != 1 {
				return "", "", false
			}
			return decodeRegOffset(word, size, opc, rt, rn)
		case 0b01:
			// Post-index: signed imm9 at [20:12]; `[Rn], #N`.
			// bit21=1 here is reserved/undefined — decline.
			if bit21 != 0 {
				return "", "", false
			}
			return decodeIndexed(word, size, opc, rt, rn, false)
		case 0b11:
			// Pre-index: signed imm9 at [20:12]; `[Rn, #N]!`.
			// bit21=1 here is reserved/undefined — decline.
			if bit21 != 0 {
				return "", "", false
			}
			return decodeIndexed(word, size, opc, rt, rn, true)
		}
	}
	return "", "", false
}

// sturMnem maps (size, opc) → (mnemonic, is64) for the unscaled STUR/LDUR
// family.  Same size/opc decode as scalarMnem (byte/half/word/sign-extend
// variants exist as sturb/ldurb/… but spectrum4 only emits stur/ldur word/
// double + the rewritten str/ldr — objdump names every unscaled form
// stur/ldur/sturb/ldurb/sturh/ldurh/ldursb/ldursh/ldursw).
func sturMnem(size, opc uint32) (string, bool, bool) {
	switch size {
	case 0b00:
		switch opc {
		case 0b00:
			return "sturb", false, true
		case 0b01:
			return "ldurb", false, true
		case 0b10:
			return "ldursb", true, true
		case 0b11:
			return "ldursb", false, true
		}
	case 0b01:
		switch opc {
		case 0b00:
			return "sturh", false, true
		case 0b01:
			return "ldurh", false, true
		case 0b10:
			return "ldursh", true, true
		case 0b11:
			return "ldursh", false, true
		}
	case 0b10:
		switch opc {
		case 0b00:
			return "stur", false, true
		case 0b01:
			return "ldur", false, true
		case 0b10:
			return "ldursw", true, true
		}
	case 0b11:
		switch opc {
		case 0b00:
			return "stur", true, true
		case 0b01:
			return "ldur", true, true
		}
	}
	return "", false, false
}

// decodeIndexed decodes pre-index (pre=true, `[Rn, #N]!`) and post-index
// (pre=false, `[Rn], #N`) scalar load/store.  imm9 is the signed offset at
// [20:12].  refenc encodeMemInst MemBaseOffPre/Post cases (pass2.go ~786).
func decodeIndexed(word, size, opc, rt, rn uint32, pre bool) (string, string, bool) {
	mnem, is64, ok := scalarMnem(size, opc)
	if !ok {
		return "", "", false
	}
	off := signExtend((word>>12)&0x1ff, 9)
	base := memBaseReg(rn)
	var addr string
	if pre {
		addr = fmt.Sprintf("[%s, #%d]!", base, off)
	} else {
		addr = fmt.Sprintf("[%s], #%d", base, off)
	}
	return mnem, memReg(rt, is64) + ", " + addr, true
}

// decodeRegOffset decodes the register-offset scalar load/store:
// `[Rn, Rm{, <ext> {#amt}}]`.  refenc MemBaseIdx / MemBaseIdxShifted /
// MemBaseIdxExtended (pass2.go ~820): option[15:13], S[12], bits[11:10]=10.
//
//	option 011 → LSL (Rm is an X register): rendered `[Rn, Xm]` or, when
//	             S=1, `[Rn, Xm, lsl #amt]` where amt = size.
//	option 010 → UXTW, 110 → SXTW, 111 → SXTX (Rm is a W register for
//	             UXTW/SXTW, X for SXTX/LSL): `[Rn, Wm, uxtw {#amt}]` etc.
//
// objdump emits the extend/shift keyword; the shift amount (size, in bytes-
// log2) is shown only when S=1, and for LSL the `#0` is suppressed when S=0.
func decodeRegOffset(word, size, opc, rt, rn uint32) (string, string, bool) {
	mnem, is64, ok := scalarMnem(size, opc)
	if !ok {
		return "", "", false
	}
	rm := (word >> 16) & 0x1f
	option := (word >> 13) & 0x7
	s := (word >> 12) & 1

	// amount applied when S=1 is the access size (log2 of the byte width).
	amt := size

	base := memBaseReg(rn)
	rtTxt := memReg(rt, is64)

	switch option {
	case 0b011: // LSL — Xm
		idx := decodeReg(rm, "x", "xzr")
		if s == 1 {
			return mnem, fmt.Sprintf("%s, [%s, %s, lsl #%d]", rtTxt, base, idx, amt), true
		}
		return mnem, fmt.Sprintf("%s, [%s, %s]", rtTxt, base, idx), true
	case 0b010, 0b110: // UXTW / SXTW — Wm
		extName := "uxtw"
		if option == 0b110 {
			extName = "sxtw"
		}
		idx := decodeReg(rm, "w", "wzr")
		if s == 1 {
			return mnem, fmt.Sprintf("%s, [%s, %s, %s #%d]", rtTxt, base, idx, extName, amt), true
		}
		return mnem, fmt.Sprintf("%s, [%s, %s, %s]", rtTxt, base, idx, extName), true
	case 0b111: // SXTX — Xm
		idx := decodeReg(rm, "x", "xzr")
		if s == 1 {
			return mnem, fmt.Sprintf("%s, [%s, %s, sxtx #%d]", rtTxt, base, idx, amt), true
		}
		return mnem, fmt.Sprintf("%s, [%s, %s, sxtx]", rtTxt, base, idx), true
	}
	return "", "", false
}

// decodePairMem decodes the load/store pair family.  Bits[29:26]=0b1010.
// Inverse of encodePairInst (pass2.go ~918):
//
//	opc[31:30] | 101 | V[26]=0 | mode[24:23] | L[22] | imm7[21:15]
//	| Rt2[14:10] | Rn[9:5] | Rt1[4:0]
//
//	opc 00 → 32-bit (W, scale 4); opc 10 → 64-bit (X, scale 8).
//	L 0 → stp, L 1 → ldp.
//	mode 01 → post-index, 10 → signed offset, 11 → pre-index.
func decodePairMem(word uint32) (string, string, bool) {
	opc := (word >> 30) & 0x3
	l := (word >> 22) & 1
	mode := (word >> 23) & 0x3
	imm7 := (word >> 15) & 0x7f
	rt2 := (word >> 10) & 0x1f
	rn := (word >> 5) & 0x1f
	rt1 := word & 0x1f

	var is64 bool
	var scale int64
	switch opc {
	case 0b00:
		is64, scale = false, 4
	case 0b10:
		is64, scale = true, 8
	default:
		return "", "", false // opc 01/11 are SIMD or undefined for our set
	}

	mnem := "stp"
	if l == 1 {
		mnem = "ldp"
	}

	off := signExtend(imm7, 7) * scale
	regs := memReg(rt1, is64) + ", " + memReg(rt2, is64) + ", "
	base := memBaseReg(rn)

	switch mode {
	case 0b10: // signed offset
		return mnem, regs + memOffset(base, off), true
	case 0b11: // pre-index
		return mnem, regs + fmt.Sprintf("[%s, #%d]!", base, off), true
	case 0b01: // post-index
		return mnem, regs + fmt.Sprintf("[%s], #%d", base, off), true
	}
	return "", "", false
}
