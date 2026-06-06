package aarch64dec

import "fmt"

// aliases.go — canonical-alias decoders for the instruction families
// where objdump (the oracle we match) prefers a shorter, GNU-canonical
// mnemonic over the architecturally-underlying base encoding:
//
//   - move-wide        (movz/movn → mov #<full-imm>)
//   - bitfield         (ubfm → lsr/lsl/ubfx/ubfiz/uxtb/uxth;
//     sbfm → asr/sbfx/sbfiz/sxtb/sxth/sxtw;
//     bfm  → bfi/bfxil/bfc)
//   - add/sub immediate (subs xzr → cmp; adds xzr → cmn;
//     add/sub #0 with SP → mov Rd, sp)
//   - logical / arith shifted register
//     (orr xzr → mov; orn xzr → mvn;
//     subs/adds/ands xzr-dest → cmp/cmn/tst;
//     sub/subs/sbc xzr-source → neg/negs/ngc)
//   - conditional select (csinc/csinv/csneg → cset/csetm/cinc/cinv/cneg)
//
// These families are decoded here by bit-pattern, computing the alias
// directly, rather than via the encoder's Form table.  The Form table is
// the *encoder's* view: it carries only the alias forms the encoder emits,
// each with a tight mask matching the specific (immr/imms/hw) the encoder
// produces — it is not a general decoder and so misses (or mis-renders)
// real words objdump linear-sweeps in release.img.  Decoding the base
// class here and applying objdump's exact alias-selection rules is robust
// and matches the oracle line-for-line.
//
// decodeAlias is consulted from DecodeAt ahead of the Form walk (alongside
// decodeMem / decodeSys), and returns ok=false for everything outside the
// families it owns so the Form walk still handles all other instructions.
//
// Every rule below was cross-checked against aarch64-none-elf-objdump on
// concrete words; the per-rule comments cite the deciding examples.
func decodeAlias(word uint32) (mnem string, operands string, ok bool) {
	if m, o, ok := decodeMoveWideAlias(word); ok {
		return m, o, true
	}
	if m, o, ok := decodeBitfieldAlias(word); ok {
		return m, o, true
	}
	if m, o, ok := decodeAddSubImmAlias(word); ok {
		return m, o, true
	}
	if m, o, ok := decodeLogicalImmAlias(word); ok {
		return m, o, true
	}
	if m, o, ok := decodeDPRegAlias(word); ok {
		return m, o, true
	}
	if m, o, ok := decodeCondSelAlias(word); ok {
		return m, o, true
	}
	if m, o, ok := decodeMul3Source(word); ok {
		return m, o, true
	}
	if m, o, ok := decodeShiftVarAlias(word); ok {
		return m, o, true
	}
	if m, o, ok := decodeMovk(word); ok {
		return m, o, true
	}
	if m, o, ok := tryDecodeExtr(word); ok {
		return m, o, true
	}
	return "", "", false
}

// undefinedInst renders an architecturally-undefined word as objdump
// does (`.inst 0x........`).  Used by the family decoders that own a
// whole encoding class: when a word falls in the class but is UNDEFINED,
// the decoder must claim it (ok=true) with this rendering so the Form
// walk's looser entries don't spuriously decode it.  The format matches
// the CLI's own ok=false fallback (`.inst\t%#08x`) byte-for-byte.
func undefinedInst(word uint32) (string, string, bool) {
	return ".inst", fmt.Sprintf("%#08x", word), true
}

// regWidth returns ("x","xzr") for sf=1 and ("w","wzr") for sf=0.
func regWidth(sf uint32) (prefix, zero string) {
	if sf == 1 {
		return "x", "xzr"
	}
	return "w", "wzr"
}

// -----------------------------------------------------------------------
// Move wide (movz / movn) → mov #<full-immediate>
//
// Encoding (ARM ARM C6.2.131 MOVZ / C6.2.129 MOVN):
//
//	sf | opc(2) | 100101 | hw(2) | imm16(16) | Rd(5)
//
//	opc 00 → movn   opc 10 → movz   opc 11 → movk (no mov alias)
//
// objdump renders movz/movn as `mov Rd, #<imm>` whenever the MOV-(wide)
// alias is *preferred*, where the immediate is the full register-width
// value the move materialises:
//
//	movz: imm = imm16 << (hw*16)                  (zero-extended)
//	movn: imm = NOT(imm16 << (hw*16)), masked to width
//
// The alias is NOT preferred (objdump keeps movz/movn) when:
//
//	movz/movn:  imm16 == 0 && hw != 0
//	movn only:  width == 32 && imm16 == 0xffff
//
// (ARM ARM "MOV (wide immediate)" / "MOV (inverted wide immediate)"
// alias conditions.)  Verified:
//
//	d2b00000 movz x0,#0x8000,lsl#16  → mov x0, #0x80000000
//	12800001 movn w1,#0              → mov w1, #0xffffffff
//	12a00001 movn w1,#0,lsl#16       → movn w1, #0x0, lsl #16   (kept)
//	129fffe1 movn w1,#0xffff         → movn w1, #0xffff         (kept)
//	d2a00000 movz x0,#0,lsl#16       → movz x0, #0x0, lsl #16   (kept)
func decodeMoveWideAlias(word uint32) (string, string, bool) {
	if (word>>23)&0x3f != 0b100101 {
		return "", "", false
	}
	sf := (word >> 31) & 1
	opc := (word >> 29) & 3
	hw := (word >> 21) & 3
	imm16 := (word >> 5) & 0xffff
	rd := word & 0x1f

	// hw must be 0/1 for 32-bit operands (sf=0); 2/3 is undefined there.
	if sf == 0 && hw >= 2 {
		return "", "", false
	}

	prefix, zero := regWidth(sf)
	rdTxt := decodeReg(rd, prefix, zero)

	switch opc {
	case 0b10: // movz
		if imm16 == 0 && hw != 0 {
			return "movz", movWideOperands(rdTxt, imm16, hw), true
		}
		val := uint64(imm16) << (hw * 16)
		if sf == 0 {
			val &= 0xffffffff
		}
		return "mov", fmt.Sprintf("%s, #%#x", rdTxt, val), true
	case 0b00: // movn
		notPreferred := (imm16 == 0 && hw != 0) || (sf == 0 && imm16 == 0xffff)
		if notPreferred {
			return "movn", movWideOperands(rdTxt, imm16, hw), true
		}
		val := ^(uint64(imm16) << (hw * 16))
		if sf == 0 {
			val &= 0xffffffff
		}
		return "mov", fmt.Sprintf("%s, #%#x", rdTxt, val), true
	}
	// opc 01 unallocated; opc 11 is movk (handled by its Form — no alias).
	return "", "", false
}

// movWideOperands renders the kept-base-form operands for movz/movn:
// `Rd, #<imm16>` with a trailing `, lsl #<hw*16>` when hw != 0.  Matches
// objdump's rendering of the non-aliased move-wide.
func movWideOperands(rdTxt string, imm16, hw uint32) string {
	if hw == 0 {
		return fmt.Sprintf("%s, #%#x", rdTxt, imm16)
	}
	return fmt.Sprintf("%s, #%#x, lsl #%d", rdTxt, imm16, hw*16)
}

// -----------------------------------------------------------------------
// Bitfield (UBFM / SBFM / BFM) → lsr/lsl/ubfx/ubfiz/uxtb/uxth /
//
//	asr/sbfx/sbfiz/sxtb/sxth/sxtw /
//	bfi/bfxil/bfc
//
// Encoding (ARM ARM C4.1.66 "Bitfield"):
//
//	sf | opc(2) | 100110 | N(1) | immr(6) | imms(6) | Rn(5) | Rd(5)
//
//	opc 00 → SBFM   opc 01 → BFM   opc 10 → UBFM
//
// N must equal sf (N=1 only for 64-bit).  datasize d = 32 (sf=0) or 64.
// All alias-selection predicates and operand formulae below follow the
// ARM ARM alias conditions and were verified against objdump (see the
// per-branch examples).
func decodeBitfieldAlias(word uint32) (string, string, bool) {
	if (word>>23)&0x3f != 0b100110 {
		return "", "", false
	}
	sf := (word >> 31) & 1
	opc := (word >> 29) & 3
	n := (word >> 22) & 1
	immr := (word >> 16) & 0x3f
	imms := (word >> 10) & 0x3f
	rn := (word >> 5) & 0x1f
	rd := word & 0x1f

	// N must equal sf, and opc=11 is unallocated.  Such words are
	// architecturally UNDEFINED; objdump renders them `.inst`.  This
	// decoder owns the whole bitfield encoding class (bits[28:23]==100110),
	// so it must claim these too — returning ok=true with the `.inst`
	// rendering — otherwise the form walk's loose ubfx/lsr/etc. Forms
	// (which ignore N) would spuriously decode them.  Verified: objdump on
	// 0x5355004e (sf=0,N=1) → `.inst 0x5355004e`.
	if n != sf || opc == 0b11 {
		return undefinedInst(word)
	}
	d := uint32(32)
	if sf == 1 {
		d = 64
	}
	prefix, zero := regWidth(sf)
	rdTxt := decodeReg(rd, prefix, zero)
	rnTxt := decodeReg(rn, prefix, zero)

	switch opc {
	case 0b10: // UBFM
		// lsr: imms == d-1.  d35efc62 → lsr x2,x3,#30.
		if imms == d-1 {
			return "lsr", fmt.Sprintf("%s, %s, #%d", rdTxt, rnTxt, immr), true
		}
		// lsl: imms+1 == immr (and imms != d-1).  d3607c62 → lsl x2,x3,#32.
		if imms+1 == immr {
			return "lsl", fmt.Sprintf("%s, %s, #%d", rdTxt, rnTxt, d-1-imms), true
		}
		// ubfiz: imms < immr.  d3647c62 → ubfiz x2,x3,#28,#32.
		if imms < immr {
			return "ubfiz", fmt.Sprintf("%s, %s, #%d, #%d", rdTxt, rnTxt, d-immr, imms+1), true
		}
		// ubfx (imms >= immr), with uxtb/uxth sub-aliases (W-reg, immr=0).
		// 53001c62 → uxtb w2,w3 ; 53003c62 → uxth w2,w3.  64-bit stays ubfx.
		if immr == 0 && sf == 0 {
			if imms == 7 {
				return "uxtb", fmt.Sprintf("%s, %s", rdTxt, rnTxt), true
			}
			if imms == 15 {
				return "uxth", fmt.Sprintf("%s, %s", rdTxt, rnTxt), true
			}
		}
		return "ubfx", fmt.Sprintf("%s, %s, #%d, #%d", rdTxt, rnTxt, immr, imms-immr+1), true

	case 0b00: // SBFM
		// asr: imms == d-1.  9340fc62 → asr x2,x3,#0.
		if imms == d-1 {
			return "asr", fmt.Sprintf("%s, %s, #%d", rdTxt, rnTxt, immr), true
		}
		// sxtb/sxth/sxtw: immr=0, imms=7/15/31; source is always a W-reg.
		// 93401c62 → sxtb x2,w3 ; 13001c62 → sxtb w2,w3.
		if immr == 0 {
			wn := decodeReg(rn, "w", "wzr")
			switch {
			case imms == 7:
				return "sxtb", fmt.Sprintf("%s, %s", rdTxt, wn), true
			case imms == 15:
				return "sxth", fmt.Sprintf("%s, %s", rdTxt, wn), true
			case imms == 31 && sf == 1:
				return "sxtw", fmt.Sprintf("%s, %s", rdTxt, wn), true
			}
		}
		// sbfiz: imms < immr.  935e1c62 → sbfiz x2,x3,#34,#8.
		if imms < immr {
			return "sbfiz", fmt.Sprintf("%s, %s, #%d, #%d", rdTxt, rnTxt, d-immr, imms+1), true
		}
		// sbfx: imms >= immr.  93441c62 → sbfx x2,x3,#4,#4.
		return "sbfx", fmt.Sprintf("%s, %s, #%d, #%d", rdTxt, rnTxt, immr, imms-immr+1), true

	case 0b01: // BFM
		// bfi / bfc: imms < immr.  bfc when Rn is the zero register.
		// b35c6d89 → bfi x9,x12,#36,#28 ; b35c1c62 → bfi x2,x3,#36,#8.
		if imms < immr {
			lsb := d - immr
			width := imms + 1
			if rn == 31 {
				// bfc Rd, #lsb, #width — no source register.
				return "bfc", fmt.Sprintf("%s, #%d, #%d", rdTxt, lsb, width), true
			}
			return "bfi", fmt.Sprintf("%s, %s, #%d, #%d", rdTxt, rnTxt, lsb, width), true
		}
		// bfxil: imms >= immr.  b3401c62 → bfxil x2,x3,#0,#8.
		return "bfxil", fmt.Sprintf("%s, %s, #%d, #%d", rdTxt, rnTxt, immr, imms-immr+1), true
	}
	return "", "", false
}

// -----------------------------------------------------------------------
// Add/sub immediate aliases.
//
// Encoding (ARM ARM C4.1.65 "Add/subtract (immediate)"):
//
//	sf | op(1) | S(1) | 100010 | sh(1) | imm12(12) | Rn(5) | Rd(5)
//
//	op 0 → ADD/ADDS   op 1 → SUB/SUBS   S = set-flags
//
// Aliases objdump prefers:
//
//	S=1, Rd=31 (zr):  subs → cmp Rn, #imm ; adds → cmn Rn, #imm
//	S=0, imm12=0, sh=0, (Rd==SP || Rn==SP): add/sub → mov Rd, Rn
//
// Verified:
//
//	f100301f subs xzr,x0,#0xc → cmp x0, #0xc
//	3100147f adds wzr,w3,#5   → cmn w3, #0x5
//	910003fd add x29,sp,#0    → mov x29, sp
//	91000000 add x0,x0,#0     → add (kept — no SP)
func decodeAddSubImmAlias(word uint32) (string, string, bool) {
	if (word>>23)&0x3f != 0b100010 {
		return "", "", false
	}
	sf := (word >> 31) & 1
	op := (word >> 30) & 1
	s := (word >> 29) & 1
	sh := (word >> 22) & 1
	imm12 := (word >> 10) & 0xfff
	rn := (word >> 5) & 0x1f
	rd := word & 0x1f

	prefix, zero := regWidth(sf)

	// cmp / cmn: S=1, Rd is the zero register.
	if s == 1 && rd == 31 {
		rnTxt := decodeReg(rn, prefix, zero)
		mnem := "cmn" // op 0 = adds → cmn
		if op == 1 {
			mnem = "cmp"
		}
		return mnem, rnTxt + ", " + immShift12(imm12, sh), true
	}

	// SP-able register text for this width (index 31 → sp/wsp).
	spPrefix, spZero := "x", "sp"
	if sf == 0 {
		spPrefix, spZero = "w", "wsp"
	}

	// mov Rd, sp / mov sp, Rn: ADD #0 (S=0, op=0), sh=0, imm12=0,
	// with SP as source or destination.  objdump keeps `add` when no SP
	// is involved (e.g. add x10, x10, #0).
	if s == 0 && op == 0 && sh == 0 && imm12 == 0 && (rd == 31 || rn == 31) {
		rdTxt := decodeReg(rd, spPrefix, spZero)
		rnTxt := decodeReg(rn, spPrefix, spZero)
		return "mov", rdTxt + ", " + rnTxt, true
	}

	// Base add/sub immediate.  This function fully owns the add/sub-imm
	// encoding space so the encoder's loose manual `mov Xd, SP` Form (mask
	// 0xfffffc00 over 0x91000000) never gets to mis-render a plain
	// `add Xd, Xn, #0` (no SP) as `mov`.  Rd and Rn are SP-able for the
	// non-flag-setting forms; for ADDS/SUBS Rn is SP-able and Rd is the
	// zero register (the Rd=zr case is the cmp/cmn alias handled above).
	mnem := map[uint32]string{0b00: "add", 0b01: "adds", 0b10: "sub", 0b11: "subs"}[(op<<1)|s]
	rdTxt := decodeReg(rd, spPrefix, spZero)
	if s == 1 {
		// adds/subs: Rd is a zr-register (sp not allowed as destination).
		rdTxt = decodeReg(rd, prefix, zero)
	}
	rnTxt := decodeReg(rn, spPrefix, spZero)
	return mnem, rdTxt + ", " + rnTxt + ", " + immShift12(imm12, sh), true
}

// immShift12 renders an add/sub-immediate's `#imm` operand, appending
// `, lsl #12` when the shift bit is set — matching objdump (e.g.
// `cmp x11, #0x870` vs `cmp x0, #0x123, lsl #12`).
func immShift12(imm12, sh uint32) string {
	if sh == 1 {
		return fmt.Sprintf("#%#x, lsl #12", imm12)
	}
	return fmt.Sprintf("#%#x", imm12)
}

// -----------------------------------------------------------------------
// Data-processing (shifted register) aliases for the add/sub-with-flags
// and logical families.
//
// Encoding (ARM ARM C4.1.67 / C4.1.69):
//
//	arithmetic:  sf | opc(2) | 01011 | shift(2) | 0 | Rm | imm6 | Rn | Rd
//	logical:     sf | opc(2) | 01010 | shift(2) | N | Rm | imm6 | Rn | Rd
//
// Aliases objdump prefers (all confirmed against objdump):
//
//	subs Rd=zr → cmp Rn, Rm{,shift}
//	adds Rd=zr → cmn Rn, Rm{,shift}
//	ands Rd=zr → tst Rn, Rm{,shift}
//	sub  Rn=zr → neg  Rd, Rm{,shift}
//	subs Rn=zr → negs Rd, Rm{,shift}
//	orr  Rn=zr → mov  Rd, Rm  (only when unshifted; else stays orr)
//	orn  Rn=zr → mvn  Rd, Rm{,shift}
//
// The non-aliased shifted-register words are left to decodeDPReg (the
// Form-walk fallback), so this returns ok=false for them.
func decodeDPRegAlias(word uint32) (string, string, bool) {
	cls := (word >> 24) & 0x1f
	isArith := cls == 0b01011
	isLogical := cls == 0b01010
	if !isArith && !isLogical {
		return "", "", false
	}
	// Only the shifted-register sub-encoding (bit21 == 0 for arithmetic;
	// for logical bit21 is the N bit).  Extended-register (arith bit21=1)
	// is handled by decodeExtendedReg and has no register aliases here.
	if isArith && (word>>21)&1 == 1 {
		return "", "", false
	}
	sf := (word >> 31) & 1
	opc := (word >> 29) & 3
	shift := (word >> 22) & 3
	nBit := (word >> 21) & 1
	rm := (word >> 16) & 0x1f
	imm6 := (word >> 10) & 0x3f
	rn := (word >> 5) & 0x1f
	rd := word & 0x1f

	// Same validity guards decodeShiftedReg applies, so an alias word that
	// would be undefined falls through to .inst identically.
	if sf == 0 && imm6 >= 32 {
		return "", "", false
	}
	if isArith && shift == 0b11 {
		return "", "", false
	}

	prefix, zero := regWidth(sf)
	rdTxt := decodeReg(rd, prefix, zero)
	rnTxt := decodeReg(rn, prefix, zero)
	rmTxt := decodeReg(rm, prefix, zero)
	shiftSuffix := func() string {
		if imm6 != 0 {
			return fmt.Sprintf(", %s #%d", shiftKindName(shift), imm6)
		}
		return ""
	}

	if isArith {
		switch opc {
		case 0b01: // adds
			if rd == 31 {
				return "cmn", rnTxt + ", " + rmTxt + shiftSuffix(), true
			}
		case 0b10: // sub
			if rn == 31 {
				return "neg", rdTxt + ", " + rmTxt + shiftSuffix(), true
			}
		case 0b11: // subs
			if rd == 31 {
				return "cmp", rnTxt + ", " + rmTxt + shiftSuffix(), true
			}
			if rn == 31 {
				return "negs", rdTxt + ", " + rmTxt + shiftSuffix(), true
			}
		}
		return "", "", false
	}

	// Logical shifted-register.
	switch opc {
	case 0b01: // orr (N=0) / orn (N=1)
		if nBit == 1 { // orn
			if rn == 31 {
				return "mvn", rdTxt + ", " + rmTxt + shiftSuffix(), true
			}
			return "", "", false
		}
		// orr → mov only for the unshifted form (no shift kind, imm6=0).
		// A shifted orr Rn=zr stays `orr` in objdump (survey §B.4).
		if rn == 31 && imm6 == 0 && shift == 0 {
			return "mov", rdTxt + ", " + rmTxt, true
		}
		return "", "", false
	case 0b11: // ands (N=0) → tst
		if nBit == 0 && rd == 31 {
			return "tst", rnTxt + ", " + rmTxt + shiftSuffix(), true
		}
	}
	return "", "", false
}

// -----------------------------------------------------------------------
// Conditional select aliases.
//
// Encoding (ARM ARM C4.1.68 "Conditional select"):
//
//	sf | op(1) | S(1) | 11010100 | Rm(5) | cond(4) | op2(2) | Rn(5) | Rd(5)
//
//	op=0,op2=01 → CSINC   op=1,op2=00 → CSINV   op=1,op2=01 → CSNEG
//
// Aliases objdump prefers (cond' = invert(cond); only when cond != 111x,
// i.e. not AL/NV, so the inverted condition is meaningful):
//
//	CSINC Rd,Rn,Rm,cond, Rn==Rm, Rn!=zr  → cinc  Rd, Rn, cond'
//	CSINC Rd,zr,zr,cond                  → cset  Rd, cond'
//	CSINV Rd,Rn,Rm,cond, Rn==Rm, Rn!=zr  → cinv  Rd, Rn, cond'
//	CSINV Rd,zr,zr,cond                  → csetm Rd, cond'
//	CSNEG Rd,Rn,Rm,cond, Rn==Rm          → cneg  Rd, Rn, cond'
//
// Verified:
//
//	1a830465 csinc w5,w3,w3,eq → cinc  w5, w3, ne
//	1a9f07e5 csinc w5,wzr,wzr,eq → cset  w5, ne
//	5a9f03e5 csinv w5,wzr,wzr,eq → csetm w5, ne
//	5a830065 csinv w5,w3,w3,eq → cinv  w5, w3, ne
//	5a830465 csneg w5,w3,w3,eq → cneg  w5, w3, ne
func decodeCondSelAlias(word uint32) (string, string, bool) {
	if (word>>21)&0xff != 0b11010100 {
		return "", "", false
	}
	if (word>>11)&1 != 0 { // op2 high bit must be 0
		return "", "", false
	}
	sf := (word >> 31) & 1
	op := (word >> 30) & 1
	s := (word >> 29) & 1
	rm := (word >> 16) & 0x1f
	cond := (word >> 12) & 0xf
	op2 := (word >> 10) & 1
	rn := (word >> 5) & 0x1f
	rd := word & 0x1f

	if s == 1 {
		return "", "", false // S=1 unallocated in this group
	}
	// Aliases require an invertable condition (cond != 111x).
	if cond>>1 == 0b111 {
		return "", "", false
	}
	invCond := decodeCond(cond ^ 1)

	prefix, zero := regWidth(sf)
	rdTxt := decodeReg(rd, prefix, zero)
	rnTxt := decodeReg(rn, prefix, zero)

	switch {
	case op == 0 && op2 == 1: // CSINC
		if rn == 31 && rm == 31 {
			return "cset", fmt.Sprintf("%s, %s", rdTxt, invCond), true
		}
		if rn == rm {
			return "cinc", fmt.Sprintf("%s, %s, %s", rdTxt, rnTxt, invCond), true
		}
	case op == 1 && op2 == 0: // CSINV
		if rn == 31 && rm == 31 {
			return "csetm", fmt.Sprintf("%s, %s", rdTxt, invCond), true
		}
		if rn == rm {
			return "cinv", fmt.Sprintf("%s, %s, %s", rdTxt, rnTxt, invCond), true
		}
	case op == 1 && op2 == 1: // CSNEG
		if rn == rm {
			return "cneg", fmt.Sprintf("%s, %s, %s", rdTxt, rnTxt, invCond), true
		}
	}
	return "", "", false
}

// -----------------------------------------------------------------------
// ORR (immediate) with Rn=zr → mov (bitmask immediate).
//
// Encoding (ARM ARM C4.1.69 "Logical (immediate)", opc=01 ORR):
//
//	sf | 01 | 100100 | N(1) | immr(6) | imms(6) | Rn(5) | Rd(5)
//
// objdump renders `orr Rd, zr, #imm` as `mov Rd, #imm` when the MOV
// (bitmask immediate) alias is preferred, i.e. when the immediate is NOT
// also expressible as a move-wide (movz/movn) — otherwise it keeps `orr`
// so the two alias domains do not overlap.  The predicate is the ARM ARM
// MoveWidePreferred() pseudocode.  Verified:
//
//	b202e7ef orr x15,xzr,#0xcc..  → mov x15, #0xcccccccccccccccc
//	b24003e0 orr x0,xzr,#0x1      → orr x0, xzr, #0x1   (movz-encodable)
//	b2407fe0 orr x0,xzr,#0xffffffff → mov x0, #0xffffffff
func decodeLogicalImmAlias(word uint32) (string, string, bool) {
	// opc=01 (ORR) and the 100100 logical-immediate signature.
	if (word>>23)&0x3f != 0b100100 {
		return "", "", false
	}
	if (word>>29)&3 != 0b01 {
		return "", "", false // only ORR has the mov alias here
	}
	rn := (word >> 5) & 0x1f
	if rn != 31 {
		return "", "", false // not the zr-source form
	}
	sf := (word >> 31) & 1
	n := (word >> 22) & 1
	immr := (word >> 16) & 0x3f
	imms := (word >> 10) & 0x3f
	rd := word & 0x1f

	if moveWidePreferred(sf, n, imms, immr) {
		return "", "", false // movz/movn-encodable → objdump keeps orr
	}
	regsize := 32
	if sf == 1 {
		regsize = 64
	}
	val, ok := decodeBitMasks(n, immr, imms, regsize)
	if !ok {
		return "", "", false
	}
	prefix, zero := regWidth(sf)
	return "mov", fmt.Sprintf("%s, #%#x", decodeReg(rd, prefix, zero), val), true
}

// moveWidePreferred is the ARM ARM MoveWidePreferred() pseudocode: true
// when a logical-immediate (N, imms, immr) value is also encodable as a
// MOVZ/MOVN move-wide immediate, in which case objdump prefers the
// move-wide-derived `mov` over the bitmask-derived `mov` and so keeps
// `orr` for the bitmask form.
func moveWidePreferred(sf, n, imms, immr uint32) bool {
	width := uint32(32)
	if sf == 1 {
		width = 64
	}
	if sf == 1 && n != 1 {
		return false
	}
	if sf == 0 && !(n == 0 && (imms>>5)&1 == 0) {
		return false
	}
	s := imms
	r := immr
	if s < 16 {
		// (-R) mod 16 <= 15 - S
		return ((16 - (r % 16)) % 16) <= (15 - s)
	}
	if s >= width-15 {
		return (r % 16) <= (s - (width - 16))
	}
	return false
}

// -----------------------------------------------------------------------
// Data-processing (3 source) — multiply family.
//
// Encoding (ARM ARM C4.1.70):
//
//	sf | 00 | 11011 | op54(2) | op31(3) | Rm | o0(1) | Ra | Rn | Rd
//
//	op54 must be 00.  (op31, o0) select the operation:
//	  000 0 madd   000 1 msub                 (Ra=zr → mul / mneg)
//	  001 0 smaddl 001 1 smsubl  (Xd,Wn,Wm,Xa)(Ra=zr → smull / smnegl)
//	  010 0 smulh                (Xd,Xn,Xm)
//	  101 0 umaddl 101 1 umsubl  (Xd,Wn,Wm,Xa)(Ra=zr → umull / umnegl)
//	  110 0 umulh                (Xd,Xn,Xm)
//
// These are real distinct instructions, not aliases of one another, but
// the encoder's madd Form mask is loose enough to spuriously claim the
// whole family on decode, so we decode them here by exact (op31,o0).
// Verified each against objdump (see survey words in commit message).
func decodeMul3Source(word uint32) (string, string, bool) {
	if (word>>24)&0x1f != 0b11011 {
		return "", "", false
	}
	if (word>>29)&3 != 0 { // op54 must be 00
		return "", "", false
	}
	sf := (word >> 31) & 1
	op31 := (word >> 21) & 7
	rm := (word >> 16) & 0x1f
	o0 := (word >> 15) & 1
	ra := (word >> 10) & 0x1f
	rn := (word >> 5) & 0x1f
	rd := word & 0x1f

	prefix, zero := regWidth(sf)
	x := func(idx uint32) string { return decodeReg(idx, prefix, zero) }
	w := func(idx uint32) string { return decodeReg(idx, "w", "wzr") }
	xfull := func(idx uint32) string { return decodeReg(idx, "x", "xzr") }

	switch op31 {
	case 0b000: // madd / msub (and mul / mneg when Ra=zr)
		if ra == 31 {
			if o0 == 0 {
				return "mul", fmt.Sprintf("%s, %s, %s", x(rd), x(rn), x(rm)), true
			}
			return "mneg", fmt.Sprintf("%s, %s, %s", x(rd), x(rn), x(rm)), true
		}
		mnem := "madd"
		if o0 == 1 {
			mnem = "msub"
		}
		return mnem, fmt.Sprintf("%s, %s, %s, %s", x(rd), x(rn), x(rm), x(ra)), true
	case 0b001: // smaddl / smsubl (smull / smnegl when Ra=zr) — 64-bit only
		if sf != 1 {
			return "", "", false
		}
		if ra == 31 {
			if o0 == 0 {
				return "smull", fmt.Sprintf("%s, %s, %s", xfull(rd), w(rn), w(rm)), true
			}
			return "smnegl", fmt.Sprintf("%s, %s, %s", xfull(rd), w(rn), w(rm)), true
		}
		mnem := "smaddl"
		if o0 == 1 {
			mnem = "smsubl"
		}
		return mnem, fmt.Sprintf("%s, %s, %s, %s", xfull(rd), w(rn), w(rm), xfull(ra)), true
	case 0b010: // smulh — 64-bit, o0=0
		if sf != 1 || o0 != 0 {
			return "", "", false
		}
		return "smulh", fmt.Sprintf("%s, %s, %s", xfull(rd), xfull(rn), xfull(rm)), true
	case 0b101: // umaddl / umsubl (umull / umnegl when Ra=zr) — 64-bit only
		if sf != 1 {
			return "", "", false
		}
		if ra == 31 {
			if o0 == 0 {
				return "umull", fmt.Sprintf("%s, %s, %s", xfull(rd), w(rn), w(rm)), true
			}
			return "umnegl", fmt.Sprintf("%s, %s, %s", xfull(rd), w(rn), w(rm)), true
		}
		mnem := "umaddl"
		if o0 == 1 {
			mnem = "umsubl"
		}
		return mnem, fmt.Sprintf("%s, %s, %s, %s", xfull(rd), w(rn), w(rm), xfull(ra)), true
	case 0b110: // umulh — 64-bit, o0=0
		if sf != 1 || o0 != 0 {
			return "", "", false
		}
		return "umulh", fmt.Sprintf("%s, %s, %s", xfull(rd), xfull(rn), xfull(rm)), true
	}
	return "", "", false
}

// -----------------------------------------------------------------------
// MOVK (move wide with keep) — not an alias, but the encoder's movk Form
// mask (0xffe00000) bakes hw=0 into its pattern and so fails to match any
// movk with a non-zero shift.  Decode it here so those words render
// instead of falling through to `.inst`.
//
// Encoding: sf | 11 | 100101 | hw(2) | imm16(16) | Rd(5).  objdump:
//
//	`movk Rd, #<imm16>` with a trailing `, lsl #<hw*16>` when hw != 0.
//	72bffc00 → movk w0, #0xffe0, lsl #16
//	f2e025ed → movk x13, #0x12f, lsl #48
func decodeMovk(word uint32) (string, string, bool) {
	if (word>>23)&0x3f != 0b100101 {
		return "", "", false
	}
	if (word>>29)&3 != 0b11 { // opc=11 is movk
		return "", "", false
	}
	sf := (word >> 31) & 1
	hw := (word >> 21) & 3
	imm16 := (word >> 5) & 0xffff
	rd := word & 0x1f
	if sf == 0 && hw >= 2 {
		return "", "", false // 32-bit movk: hw in {0,1} only
	}
	prefix, zero := regWidth(sf)
	return "movk", movWideOperands(decodeReg(rd, prefix, zero), imm16, hw), true
}

// -----------------------------------------------------------------------
// EXTR (extract) → ror Rd, Rn, #imms  (Rm==Rn alias) or extr Rd, Rn, Rm, #imms.
//
// Encoding (ARM ARM C6.2.72):
//
//	sf | 00100111 | N | 0 | Rm(5) | imms(6) | Rn(5) | Rd(5)
//
//	32-bit: sf=0, N=0 → pattern 0x13800000, mask 0xFFE00000
//	64-bit: sf=1, N=1 → pattern 0x93C00000, mask 0xFFE00000
//
// When Rm==Rn, objdump renders the ROR alias: `ror Rd, Rn, #imms`.
// When Rm!=Rn, it renders the base form: `extr Rd, Rn, Rm, #imms`.
//
// EXTR is absent from AllForms(): refenc encodes it by hand in
// encodeRorImm (Rn appears in two slots), so the generic form-table
// dispatch cannot find it and the word would otherwise fall through to
// `.inst`.  Decoding it here restores round-trip completeness.
//
// Verified:
//
//	13811420 ror w0, w1, #5   (sf=0, Rm=Rn=1, imms=5)
//	93c11420 ror x0, x1, #5   (sf=1, Rm=Rn=1, imms=5)
//	13821420 extr w0, w1, w2, #5  (sf=0, Rm=2, Rn=1, imms=5)
func tryDecodeExtr(word uint32) (mnem, ops string, ok bool) {
	var sf bool
	switch word & 0xFFE00000 {
	case 0x13800000:
		sf = false
	case 0x93C00000:
		sf = true
	default:
		return "", "", false
	}
	rm := (word >> 16) & 0x1f
	imms := (word >> 10) & 0x3f
	rn := (word >> 5) & 0x1f
	rd := word & 0x1f

	var prefix, zero string
	if sf {
		prefix, zero = "x", "xzr"
	} else {
		prefix, zero = "w", "wzr"
	}
	rdTxt := decodeReg(rd, prefix, zero)
	rnTxt := decodeReg(rn, prefix, zero)
	rmTxt := decodeReg(rm, prefix, zero)

	if rm == rn {
		return "ror", fmt.Sprintf("%s, %s, #%d", rdTxt, rnTxt, imms), true
	}
	return "extr", fmt.Sprintf("%s, %s, %s, #%d", rdTxt, rnTxt, rmTxt, imms), true
}

// -----------------------------------------------------------------------
// Data-processing (2 source) variable-shift → lsl/lsr/asr/ror.
//
// Encoding (ARM ARM C4.1.70 "Data-processing (2 source)"):
//
//	sf | 0 | 0 | 11010110 | Rm(5) | opcode2(6) | Rn(5) | Rd(5)
//
//	opcode2 001000 LSLV  001001 LSRV  001010 ASRV  001011 RORV
//
// objdump prefers the alias mnemonics lsl/lsr/asr/ror for the V (variable)
// forms.  Verified:
//
//	1ace258c lsrv w12,w12,w14 → lsr w12, w12, w14
//	1ac42c65 rorv w5,w3,w4    → ror w5, w3, w4
func decodeShiftVarAlias(word uint32) (string, string, bool) {
	// bits[30:21] == 0011010110 (the 2-source class, sf at bit31 separate).
	if (word>>21)&0x3ff != 0b0011010110 {
		return "", "", false
	}
	opcode2 := (word >> 10) & 0x3f
	var mnem string
	switch opcode2 {
	case 0b001000:
		mnem = "lsl"
	case 0b001001:
		mnem = "lsr"
	case 0b001010:
		mnem = "asr"
	case 0b001011:
		mnem = "ror"
	default:
		return "", "", false // other 2-source ops (udiv/sdiv/...) keep base
	}
	sf := (word >> 31) & 1
	rm := (word >> 16) & 0x1f
	rn := (word >> 5) & 0x1f
	rd := word & 0x1f
	prefix, zero := regWidth(sf)
	return mnem, fmt.Sprintf("%s, %s, %s",
		decodeReg(rd, prefix, zero), decodeReg(rn, prefix, zero), decodeReg(rm, prefix, zero)), true
}
