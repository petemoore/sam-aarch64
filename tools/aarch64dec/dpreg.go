package aarch64dec

import (
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// dpreg.go — special-case decoders for the two data-processing register
// addressing modes the encoder hand-rolls and that are therefore absent
// from aarch64enc.AllForms():
//
//   - shifted register   (encodeShiftedRegInst, refenc/pass2.go:1005)
//   - extended register  (encodeExtendedRegInst, refenc/pass2.go:1131)
//
// Both share the 0b01011 (arithmetic) / 0b01010 (logical) op class at
// bits[28:24]; bit21 distinguishes them (0 = shifted, 1 = extended for
// the arithmetic class).  We decode to the *base* mnemonic only
// (add/sub/adds/subs/and/orr/eor/ands/bic/orn/eon) — alias collapsing
// to mov/cmp/cmn/neg/mvn/tst is a separate later task, so where objdump
// prints an alias for one of these encodings (e.g. orr Xd,xzr,Xm → mov)
// we deliberately still emit the base form and that line stays on the
// worklist until the alias task lands.

// decodeDPReg decodes a data-processing (shifted/extended register) word.
// ok=false for anything outside the two op classes this file owns (so
// DecodeAt's form walk still handles everything else).
//
// Class selector is bits[28:24]:
//
//	0b01011 — arithmetic (add/sub/adds/subs): bit21 picks shifted(0)/extended(1)
//	0b01010 — logical    (and/orr/eor/ands + bic/orn/eon via N bit): shifted only
//
// We additionally require bit27=0 / the SIMD/FP & multiply spaces to be
// excluded: those have bit28=1 or other class bits, so matching exactly
// on bits[28:24] is sufficient.  Multiply/divide (0b11010110 etc.) and
// conditional-select live in different class fields and are not matched.
func decodeDPReg(word uint32) (mnem string, operands string, ok bool) {
	cls := (word >> 24) & 0x1f
	switch cls {
	case 0b01011:
		// Arithmetic add/sub family.  bit21 = 0 shifted, 1 extended.
		// But the multiply / data-processing-3-source / conditional
		// spaces also set higher bits; guard bit28 (already 0 here by
		// class match) and bit30/29 carry opc.  bits[23:22] must be the
		// shift-type (00/01/10) for shifted, or 00 for extended — the
		// sub-decoders validate.
		if (word>>21)&1 == 1 {
			return decodeExtendedReg(word)
		}
		return decodeShiftedReg(word, false)
	case 0b01010:
		// Logical shifted-register family.  bit21 here is the N bit
		// (BIC/ORN/EON when set), not a shifted/extended selector.
		return decodeShiftedReg(word, true)
	}
	return "", "", false
}

// shiftKindName maps the 2-bit shift-type field to its keyword.  For
// logical it can be any of lsl/lsr/asr/ror; for arithmetic ror (0b11)
// is reserved (objdump renders it `.inst`/undefined), so the caller
// rejects 0b11 in the arithmetic case.
func shiftKindName(s uint32) string {
	return [...]string{"lsl", "lsr", "asr", "ror"}[s&3]
}

// decodeShiftedReg decodes the shifted-register form:
//
//	sf | opc(2) | 0101X | shift(2) | N(1) | Rm(5) | imm6(6) | Rn(5) | Rd(5)
//
// (X = 1 arithmetic, 0 logical.)  Mnemonic comes from (sf-independent)
// opc + the op class + N bit, mirroring shiftedRegMnemonicFields in
// refenc/pass2.go:1096.  Operands render `Rd, Rn, Rm` with a trailing
// `, <shift> #imm6` only when imm6 != 0.  Verified against objdump:
//
//	8b010001 → add  x1, x0, x1
//	8b100a10 → add  x16, x16, x16, lsl #2
//	2a511230 → orr  w16, w17, w17, lsr #4
//	8a58816b → and  x11, x11, x24, lsr #32
//	eb09000b → subs x11, x0, x9
func decodeShiftedReg(word uint32, isLogical bool) (string, string, bool) {
	sf := (word >> 31) & 1
	opc := (word >> 29) & 3
	shift := (word >> 22) & 3
	nBit := (word >> 21) & 1
	rm := (word >> 16) & 0x1f
	imm6 := (word >> 10) & 0x3f
	rn := (word >> 5) & 0x1f
	rd := word & 0x1f

	// imm6's top bit must be 0 for 32-bit (sf=0) operands — shift amount
	// is [0,31] for W, [0,63] for X.  objdump treats sf=0 with imm6>=32
	// as undefined (.inst); reject so we fall through to .inst, matching.
	if sf == 0 && imm6 >= 32 {
		return "", "", false
	}

	var mnem string
	if isLogical {
		mnem = logicalShiftedMnem(opc, nBit)
	} else {
		// Arithmetic: ror (shift 0b11) is reserved → undefined.
		if shift == 0b11 {
			return "", "", false
		}
		mnem = arithShiftedMnem(opc)
	}
	if mnem == "" {
		return "", "", false
	}

	is64 := sf == 1
	rdTxt := dpReg(rd, is64)
	rnTxt := dpReg(rn, is64)
	rmTxt := dpReg(rm, is64)

	ops := fmt.Sprintf("%s, %s, %s", rdTxt, rnTxt, rmTxt)
	if imm6 != 0 {
		ops += fmt.Sprintf(", %s #%d", shiftKindName(shift), imm6)
	}
	return mnem, ops, true
}

// arithShiftedMnem maps the 2-bit opc of the arithmetic shifted-reg
// class to its mnemonic.  Mirrors shiftedRegMnemonicFields (add opc=00,
// adds opc=01, sub opc=10, subs opc=11) in refenc/pass2.go:1101.
func arithShiftedMnem(opc uint32) string {
	switch opc {
	case 0b00:
		return "add"
	case 0b01:
		return "adds"
	case 0b10:
		return "sub"
	case 0b11:
		return "subs"
	}
	return ""
}

// logicalShiftedMnem maps (opc, N) of the logical shifted-reg class to
// its mnemonic.  Mirrors shiftedRegMnemonicFields (refenc/pass2.go:1106):
//
//	opc 00 N0 → and   N1 → bic
//	opc 01 N0 → orr   N1 → orn
//	opc 10 N0 → eor   N1 → eon
//	opc 11 N0 → ands  N1 → bics
func logicalShiftedMnem(opc, n uint32) string {
	switch opc {
	case 0b00:
		if n == 1 {
			return "bic"
		}
		return "and"
	case 0b01:
		if n == 1 {
			return "orn"
		}
		return "orr"
	case 0b10:
		if n == 1 {
			return "eon"
		}
		return "eor"
	case 0b11:
		if n == 1 {
			return "bics"
		}
		return "ands"
	}
	return ""
}

// decodeExtendedReg decodes the extended-register form (add/sub family):
//
//	sf | opc(2) | 01011 | 00 | 1 | Rm(5) | option(3) | imm3(3) | Rn(5) | Rd(5)
//
// Mirrors encodeExtendedRegInst (refenc/pass2.go:1131); opc selects
// add/adds/sub/subs.  Rd and Rn are SP-able (zero index → sp/wsp), Rm
// is a zero-register operand whose width depends on the extend option
// (uxtx/sxtx → X, all others → W).
//
// objdump rendering rules (verified against aarch64-none-elf-objdump):
//   - The "LSL option" is option==011 when sf=1, option==010 when sf=0.
//   - When the option is the LSL option AND (Rd==SP or Rn==SP):
//     amt==0  → omit the extend phrase entirely  (add x0, sp, x1)
//     amt!=0  → render `lsl #amt`                (add x0, sp, x1, lsl #2)
//   - Otherwise render the extend keyword, plus ` #amt` when amt!=0:
//     8b234041 → add x1, x2, w3, uxtw
//     8b23c841 → add x1, x2, w3, sxtw #2
//     8b226020 → add x0, x1, x2, uxtx
func decodeExtendedReg(word uint32) (string, string, bool) {
	sf := (word >> 31) & 1
	opc := (word >> 29) & 3
	rm := (word >> 16) & 0x1f
	option := (word >> 13) & 7
	imm3 := (word >> 10) & 7
	rn := (word >> 5) & 0x1f
	rd := word & 0x1f

	// bits[23:22] are the "opt" field of the extended-register encoding
	// and MUST be 00 (the encoder ORs nothing here — see
	// encodeExtendedRegInst); any other value is unallocated and objdump
	// renders it `.inst`/undefined.  Reject so we fall through to .inst.
	if (word>>22)&3 != 0 {
		return "", "", false
	}

	// imm3 (shift amount) is architecturally [0,4]; >4 is undefined.
	if imm3 > 4 {
		return "", "", false
	}

	mnem := arithShiftedMnem(opc) // same opc→mnemonic map as shifted
	if mnem == "" {
		return "", "", false
	}

	is64 := sf == 1
	rdTxt := dpRegSP(rd, is64)
	rnTxt := dpRegSP(rn, is64)
	// Rm width: uxtx(3)/sxtx(7) use the full-size register; the byte/
	// half/word extends use the 32-bit (W) register regardless of sf.
	rmIs64 := option == 0b011 || option == 0b111
	rmTxt := dpReg(rm, rmIs64)

	ops := fmt.Sprintf("%s, %s, %s", rdTxt, rnTxt, rmTxt)

	// The option value that objdump renders as `lsl`: 011 for 64-bit
	// forms, 010 for 32-bit forms (the "natural-width" UXTX/UXTW).
	lslOption := uint32(0b011)
	if sf == 0 {
		lslOption = 0b010
	}
	spInvolved := rd == 31 || rn == 31
	if option == lslOption && spInvolved {
		if imm3 != 0 {
			ops += fmt.Sprintf(", lsl #%d", imm3)
		}
		// amt==0: extend omitted entirely.
		return mnem, ops, true
	}

	ext := format.ExtendKind(option).Name()
	if imm3 != 0 {
		ops += fmt.Sprintf(", %s #%d", ext, imm3)
	} else {
		ops += ", " + ext
	}
	return mnem, ops, true
}

// dpReg renders a data-processing register operand using the zero
// register (xzr/wzr) for index 31.
func dpReg(idx uint32, is64 bool) string {
	if is64 {
		return decodeReg(idx, "x", "xzr")
	}
	return decodeReg(idx, "w", "wzr")
}

// dpRegSP renders an SP-able data-processing register operand (sp/wsp
// for index 31).  Used for Rd/Rn of the extended-register forms.
func dpRegSP(idx uint32, is64 bool) string {
	if is64 {
		return decodeReg(idx, "x", "sp")
	}
	return decodeReg(idx, "w", "wsp")
}
