package aarch64dec

import (
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// sys.go — special-case decoders for the System instruction group
// (bits[31:22] == 0b1101010100, mask 0xffc00000, pattern 0xd5000000).
// These are hand-rolled by the encoder (encodeMrs / encodeMsr /
// encodeDc / encodeTlbi / encodeBarrierInst in refenc/pass2.go) and so
// are absent from aarch64enc.AllForms().
//
// The group is sub-decoded on op0 (bits[20:19]) and L (bit21):
//
//	op0 00 — hints / barriers / msr-immediate (CRn picks the sub-family)
//	op0 01 — SYS / SYSL: dc / tlbi / at / ic
//	op0 10/11 — MRS (L=1) / MSR-register (L=0)
//
// We return ok=false for everything we don't own (notably the hint
// space — nop/wfi/etc. — which IS in AllForms, so the form walk handles
// it).  decodeSys runs before the form walk in DecodeAt.

// decodeSys decodes a System-group word.  ok=false when the word is
// outside the group, or in a sub-family we leave to the form walk
// (hints) / to .inst.
func decodeSys(word uint32) (mnem string, operands string, ok bool) {
	if word&0xffc00000 != 0xd5000000 {
		return "", "", false
	}
	op0 := (word >> 19) & 3
	l := (word >> 21) & 1
	switch op0 {
	case 0b00:
		return decodeSysOp0Zero(word)
	case 0b01:
		return decodeSysInstr(word) // dc / tlbi / at / ic
	case 0b10, 0b11:
		return decodeMrsMsrReg(word, l)
	}
	return "", "", false
}

// decodeSysOp0Zero handles the op0==0 sub-space: barriers (CRn==3) and
// the MSR (immediate) PSTATE form (CRn==4).  The hint space (CRn==2,
// e.g. nop/wfi) is left to AllForms — we return ok=false for it.
func decodeSysOp0Zero(word uint32) (string, string, bool) {
	crn := (word >> 12) & 0xf
	switch crn {
	case 3: // barriers
		return decodeBarrier(word)
	case 4: // msr <pstatefield>, #imm
		return decodeMsrImm(word)
	}
	return "", "", false
}

// decodeBarrier decodes dsb / dmb / isb.  Layout:
//
//	1101 0101 0000 0011 0011 CRm[11:8] op2[7:5] 11111
//
//	op2 4 → dsb, 5 → dmb, 6 → isb.  (op2 2 = clrex, not in our set.)
//	CRm carries the barrier option.
//
// Verified against aarch64-none-elf-objdump (option tables below).
func decodeBarrier(word uint32) (string, string, bool) {
	// Rt must be 11111 and CRm-region intact; bits[15:12] (CRn) == 3
	// already by the caller.  Require the fixed bits to match exactly.
	if word&0x1f != 0x1f {
		return "", "", false
	}
	op2 := (word >> 5) & 7
	crm := (word >> 8) & 0xf
	switch op2 {
	case 4: // dsb
		return "dsb", barrierOption(crm, true), true
	case 5: // dmb
		return "dmb", barrierOption(crm, false), true
	case 6: // isb
		// isb takes the option only when CRm != 15; objdump prints a
		// bare `isb` for the canonical (sy) form and `isb #0xN`
		// otherwise.
		if crm == 0xf {
			return "isb", "", true
		}
		return "isb", fmt.Sprintf("#%#x", crm), true
	}
	return "", "", false
}

// barrierOption maps the 4-bit CRm option for dsb/dmb to its keyword,
// deferring to the format package's option table (the single source of
// truth shared with the Z80 disassembler).  A CRm with no keyword — or a
// dsb-only option (ssbb/pssbb) seen on dmb — prints `#0xNN`.
func barrierOption(crm uint32, isDsb bool) string {
	if name, ok := format.BarrierOptionName(byte(crm), isDsb); ok {
		return name
	}
	return fmt.Sprintf("#%#02x", crm)
}

// decodeMsrImm decodes the MSR (immediate) PSTATE form:
//
//	1101 0101 0000 op1[18:16] 0100 CRm[11:8] op2[7:5] 11111
//
// (op1, op2) select the PSTATE field; CRm carries the 4-bit immediate.
// Mirrors encodeMsr's immediate branch (refenc/pass2.go:1587).  Verified:
//
//	d50343df → msr daifset, #0x3
//	d50342ff → msr daifclr, #0x2
func decodeMsrImm(word uint32) (string, string, bool) {
	if word&0x1f != 0x1f {
		return "", "", false
	}
	op1 := (word >> 16) & 7
	op2 := (word >> 5) & 7
	crm := (word >> 8) & 0xf
	name, ok := pstateName(op1, op2)
	if !ok {
		return "", "", false
	}
	return "msr", fmt.Sprintf("%s, #%#x", name, crm), true
}

// decodeMrsMsrReg decodes the register forms of mrs / msr:
//
//	1101 0101 00 L op0&1[19] op1[18:16] CRn[15:12] CRm[11:8] op2[7:5] Rt[4:0]
//
//	L 1 → mrs Xt, <sysreg>   L 0 → msr <sysreg>, Xt
//	op0 = 2 | bit19 (the high op0 bit is fixed 1 by the group).
//
// Mirrors sysRegEncode (refenc/pass2.go:1678).  Verified:
//
//	d5384240 → mrs x0, currentel
//	d519b040 → msr s3_1_c11_c0_2, x0   (generic fallback)
func decodeMrsMsrReg(word uint32, l uint32) (string, string, bool) {
	op0 := byte(2 | ((word >> 19) & 1))
	op1 := byte((word >> 16) & 7)
	crn := byte((word >> 12) & 0xf)
	crm := byte((word >> 8) & 0xf)
	op2 := byte((word >> 5) & 7)
	rt := word & 0x1f

	name := sysRegName(format.SysReg{Op0: op0, Op1: op1, CRn: crn, CRm: crm, Op2: op2})
	rtTxt := decodeReg(rt, "x", "xzr")
	if l == 1 {
		return "mrs", fmt.Sprintf("%s, %s", rtTxt, name), true
	}
	return "msr", fmt.Sprintf("%s, %s", name, rtTxt), true
}

// decodeSysInstr decodes the SYS / SYSL space (op0==1): dc / tlbi / at /
// ic.  Layout:
//
//	1101 0101 00 0 1 op1[18:16] CRn[15:12] CRm[11:8] op2[7:5] Rt[4:0]
//
//	CRn 7 → dc / at / ic (op1/op2 disambiguate)
//	CRn 8 → tlbi
//
// Mirrors encodeDc / encodeTlbi (refenc/pass2.go:1616, 1642).  We name
// the op from a reverse table built from the format package's dc/tlbi
// tables (plus the canonical at/ic ops); a register operand is appended
// for ops that take one (objdump prints xzr when Rt==31 and the op
// takes a register).  Verified:
//
//	d50b7e2a → dc civac, x10
//	d508871f → tlbi vmalle1
func decodeSysInstr(word uint32) (string, string, bool) {
	op1 := byte((word >> 16) & 7)
	crn := byte((word >> 12) & 0xf)
	crm := byte((word >> 8) & 0xf)
	op2 := byte((word >> 5) & 7)
	rt := word & 0x1f

	mnem, name, needsXt, ok := sysInstrName(op1, crn, crm, op2)
	if !ok {
		return "", "", false
	}
	if needsXt {
		return mnem, fmt.Sprintf("%s, %s", name, decodeReg(rt, "x", "xzr")), true
	}
	return mnem, name, true
}

// sysRegName resolves a (op0,op1,CRn,CRm,op2) tuple to objdump's name —
// architectural where known, generic s<...> spelling otherwise — by
// deferring to the format package's reverse of ParseSysReg.
func sysRegName(sr format.SysReg) string { return format.SysRegName(sr) }

// pstateName resolves a (op1,op2) PSTATE pair to its keyword.
func pstateName(op1, op2 uint32) (string, bool) {
	return format.PStateName(format.PState{Op1: byte(op1), Op2: byte(op2)})
}

// sysInstrName resolves a SYS-instruction (op1,CRn,CRm,op2) tuple to its
// mnemonic + op name + whether it carries an Xt operand.  CRn selects
// the family: CRn 8 → tlbi, CRn 7 → dc / at / ic (CRm disambiguates).
// All four families (dc/tlbi/at/ic) name their op from the format
// package's reverse tables — the single source of truth shared with the
// encoder and projected into the Z80 disassembler's option tables.  (The
// encoder doesn't emit at/ic and they don't occur in release.img, but
// decoding them keeps parity with objdump.)
func sysInstrName(op1, crn, crm, op2 byte) (mnem, name string, needsXt, ok bool) {
	switch crn {
	case 8: // TLBI
		if n, xt, found := format.TLBIName(op1, crn, crm, op2); found {
			return "tlbi", n, xt, true
		}
	case 7: // DC / AT / IC, disambiguated by CRm
		if n, xt, found := format.DCName(op1, crn, crm, op2); found {
			return "dc", n, xt, true
		}
		if n, xt, found := format.ATName(op1, crm, op2); found {
			return "at", n, xt, true
		}
		if n, xt, found := format.ICName(op1, crm, op2); found {
			return "ic", n, xt, true
		}
	}
	return "", "", false, false
}
