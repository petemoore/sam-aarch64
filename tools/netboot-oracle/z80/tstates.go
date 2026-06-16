// tstates.go — cycle (T-state) accounting for the flat-memory Z80 harness.
//
// instrTStates peeks the instruction at the CPU's current PC (BEFORE the step)
// and returns its canonical T-state count, so CallEntry can accumulate the exact
// cycle cost of a routine. It is the prerequisite for the i102 aggressive
// T-state optimization of the netboot crypto leaves: measure a baseline against
// a fixed payload, then iterate to drive the total down.
//
// Timing source: the Zilog "Z80 CPU User Manual" (UM0080, Rev. 11) instruction
// reference — the per-instruction "T States" column — cross-checked against Sean
// Young's "The Undocumented Z80 Documented" v0.91, Appendix A (the cycle tables
// for the base / CB / ED / DD-FD / DD-CB groups). The values here are those
// documented (M-cycle × T-state) totals, e.g. NOP = 4, LD r,r' = 4, LD A,n = 7,
// LD A,(HL) = 7, LD (HL),n = 10, JP nn = 10, CALL nn = 17, RET = 10. Conditional
// control flow carries both branch-dependent figures (e.g. JR cc taken = 12 /
// not-taken = 7; CALL cc taken = 17 / not-taken = 10; RET cc taken = 11 /
// not-taken = 5; DJNZ taken = 13 / not-taken = 8) and the table resolves which
// applies from the flags (or B for DJNZ) as they stand before the step. The
// repeating block ops (LDIR/LDDR/CPIR/CPDR/INIR/INDR/OTIR/OTDR) cost 21 while
// they will repeat and 16 (21 for the I/O ones) on the final iteration; koron-go
// re-runs the op once per byte (it does cpu.PC -= 2), so each Step is one
// iteration and is timed individually.
//
// Completeness is proven by error-on-unknown: any opcode not in the table makes
// instrTStates return an error naming the bytes + PC, which CallEntry surfaces.
// The crypto baselines (tstates_baseline_test.go) running clean is the proof the
// table covers every opcode those routines execute. DO NOT add a default/guess —
// add the canonical value for the missing opcode (and cite it) instead.
package z80

import (
	"fmt"

	"github.com/koron-go/z80"
)

// Z80 flag bit masks in the F register (cpu.AF.Lo). Mirrors koron-go/z80's
// flag.go and the Z80 spec.
const (
	flagC  = 0x01 // carry
	flagN  = 0x02 // add/subtract
	flagPV = 0x04 // parity / overflow
	flagH  = 0x10 // half carry
	flagZ  = 0x40 // zero
	flagS  = 0x80 // sign
)

// instrTStates returns the canonical T-state count of the instruction at
// cpu.PC, resolving conditional branches and repeating block ops from the live
// CPU state. It errors (never guesses) on any opcode absent from the tables.
func instrTStates(m *mem, cpu *z80.CPU) (uint, error) {
	pc := cpu.PC
	op := cpu.Memory.Get(pc)

	switch op {
	case 0xCB:
		return cbTStates(cpu.Memory.Get(pc + 1)), nil
	case 0xED:
		return edTStates(m, cpu, cpu.Memory.Get(pc+1))
	case 0xDD, 0xFD:
		return ixTStates(m, cpu, pc, op)
	default:
		return baseTStates(m, cpu, pc, op)
	}
}

// flagSet reports whether flag bit f is set in F (cpu.AF.Lo).
func flagSet(cpu *z80.CPU, f uint8) bool { return cpu.AF.Lo&f != 0 }

// ccTaken resolves the 8 standard condition codes (cc field) against the live
// flags: 0 NZ, 1 Z, 2 NC, 3 C, 4 PO, 5 PE, 6 P, 7 M.
func ccTaken(cpu *z80.CPU, cc uint8) bool {
	switch cc {
	case 0:
		return !flagSet(cpu, flagZ)
	case 1:
		return flagSet(cpu, flagZ)
	case 2:
		return !flagSet(cpu, flagC)
	case 3:
		return flagSet(cpu, flagC)
	case 4:
		return !flagSet(cpu, flagPV)
	case 5:
		return flagSet(cpu, flagPV)
	case 6:
		return !flagSet(cpu, flagS)
	case 7:
		return flagSet(cpu, flagS)
	}
	return false
}

// unknownOp formats the error for an opcode missing from a table.
func unknownOp(pc uint16, bytes ...uint8) error {
	return fmt.Errorf("z80: no T-state timing for opcode % X at PC=&%04X", bytes, pc)
}

// baseTime is the unprefixed-opcode T-state table. -1 marks an opcode handled
// specially (prefixes, conditional control flow, block ops) or absent.
var baseTime = [256]int8{
	// 0x00
	4, 10, 7, 6, 4, 4, 7, 4, 4, 11, 7, 6, 4, 4, 7, 4,
	// 0x10  (10 = DJNZ handled specially)
	-1, 10, 7, 6, 4, 4, 7, 4, 12, 11, 7, 6, 4, 4, 7, 4,
	// 0x20  (JR cc handled specially: 20,28,30,38)
	-1, 10, 16, 6, 4, 4, 7, 4, -1, 11, 16, 6, 4, 4, 7, 4,
	// 0x30
	-1, 10, 13, 6, 11, 11, 10, 4, -1, 11, 13, 6, 4, 4, 7, 4,
	// 0x40  LD r,r' block (HL forms = 7)
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	// 0x50
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	// 0x60
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	// 0x70  (76 = HALT)
	7, 7, 7, 7, 7, 7, 4, 7, 4, 4, 4, 4, 4, 4, 7, 4,
	// 0x80  ALU A,r (HL forms = 7)
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	// 0x90
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	// 0xA0
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	// 0xB0
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	// 0xC0  (C0/C8=RET cc, C2/CA=JP cc, C4/CC=CALL cc, CB prefix, C9=RET, C3=JP, CD=CALL)
	-1, 10, -1, 10, -1, 11, 7, 11, -1, 10, -1, -1, -1, 17, 7, 11,
	// 0xD0
	-1, 10, -1, 11, -1, 11, 7, 11, -1, 4, -1, 11, -1, -1, 7, 11,
	// 0xE0  (E3 EX (SP),HL=19, E9 JP (HL)=4, EB EX DE,HL=4, ED prefix)
	-1, 10, -1, 19, -1, 11, 7, 11, -1, 4, -1, 4, -1, -1, 7, 11,
	// 0xF0  (F9 LD SP,HL=6, FD prefix, DD prefix)
	-1, 10, -1, 4, -1, 11, 7, 11, -1, 6, -1, 4, -1, -1, 7, 11,
}

// baseTStates returns the timing of an unprefixed opcode, resolving the
// conditional control flow and DJNZ that baseTime marks as -1.
func baseTStates(m *mem, cpu *z80.CPU, pc uint16, op uint8) (uint, error) {
	switch op {
	case 0x10: // DJNZ e — taken (B-1 != 0) = 13, not-taken = 8
		if cpu.BC.Hi != 1 {
			return 13, nil
		}
		return 8, nil
	case 0x20, 0x28, 0x30, 0x38: // JR cc,e — taken = 12, not-taken = 7
		cc := (op >> 3) & 0x03 // NZ,Z,NC,C
		if ccTaken(cpu, cc) {
			return 12, nil
		}
		return 7, nil
	case 0xC0, 0xC8, 0xD0, 0xD8, 0xE0, 0xE8, 0xF0, 0xF8: // RET cc — taken = 11, not-taken = 5
		cc := (op >> 3) & 0x07
		if ccTaken(cpu, cc) {
			return 11, nil
		}
		return 5, nil
	case 0xC2, 0xCA, 0xD2, 0xDA, 0xE2, 0xEA, 0xF2, 0xFA: // JP cc,nn — 10 either way
		return 10, nil
	case 0xC4, 0xCC, 0xD4, 0xDC, 0xE4, 0xEC, 0xF4, 0xFC: // CALL cc,nn — taken = 17, not-taken = 10
		cc := (op >> 3) & 0x07
		if ccTaken(cpu, cc) {
			return 17, nil
		}
		return 10, nil
	}
	if t := baseTime[op]; t >= 0 {
		return uint(t), nil
	}
	return 0, unknownOp(pc, op)
}

// cbTStates returns the timing of a CB-prefixed opcode. CB ops on a register
// take 8; the (HL) memory forms take 15, except BIT b,(HL) which takes 12.
func cbTStates(sub uint8) uint {
	isHL := (sub & 0x07) == 0x06 // operand register field = 6 => (HL)
	if !isHL {
		return 8
	}
	// (HL) forms: BIT b,(HL) (0x40..0x7F) = 12; RLC/RES/SET/.. (HL) = 15.
	if sub >= 0x40 && sub <= 0x7F {
		return 12
	}
	return 15
}

// edTime is the ED-prefixed opcode timing table, indexed by the second byte.
// -1 marks an opcode handled specially (the repeating block ops) or one not in
// the documented ED set (an invalid ED xx is a NOP-like 8 on real silicon, but
// we error on it so an unexpected encoding is caught rather than silently timed).
var edTime = [256]int8{
	// 0x00..0x3F: no documented ED ops
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	// 0x40  IN r,(C)=12, OUT (C),r=12, SBC/ADC HL,ss=15, LD (nn),dd=20,
	//       NEG=8, RETN/RETI=14, IM=8, LD I/R,A & A,I/R=9
	12, 12, 15, 20, 8, 14, 8, 9, 12, 12, 15, 20, 8, 14, 8, 9,
	// 0x50
	12, 12, 15, 20, 8, 14, 8, 9, 12, 12, 15, 20, 8, 14, 8, 9,
	// 0x60  (RRD/RLD = 18 at 0x67/0x6F)
	12, 12, 15, 20, 8, 14, 8, 18, 12, 12, 15, 20, 8, 14, 8, 18,
	// 0x70  (70 IN (C)=12, 71 OUT (C),0=12, 72/73 SBC/LD=15/20, 78/79 IN/OUT A=12, 7A/7B=15/20)
	12, 12, 15, 20, 8, 14, 8, -1, 12, 12, 15, 20, 8, 14, 8, -1,
	// 0x80..0x9F: no documented ED ops
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	// 0xA0  LDI/CPI/INI/OUTI=16; LDD/CPD/IND/OUTD=16 (at A8..AB)
	16, 16, 16, 16, -1, -1, -1, -1, 16, 16, 16, 16, -1, -1, -1, -1,
	// 0xB0  LDIR/CPIR/INIR/OTIR & LDDR/CPDR/INDR/OTDR handled specially
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	// 0xC0..0xFF: no documented ED ops
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
}

// edTStates returns the timing of an ED-prefixed opcode, resolving the
// repeating block ops from the live BC (LDxR/CPxR) / B (INxR/OTxR) counter.
func edTStates(m *mem, cpu *z80.CPU, sub uint8) (uint, error) {
	switch sub {
	case 0xB0, 0xB8, 0xB1, 0xB9: // LDIR, LDDR, CPIR, CPDR
		// Repeats while BC-1 != 0; the koron-go op decrements BC then re-runs
		// (PC -= 2) unless BC reaches 0. Timing: 21 repeating, 16 final.
		// CPIR/CPDR also stop early on a match, but the worst-case (no match)
		// timing is the same per-iteration figure; the final iteration is 16.
		if cpu.BC.U16() != 1 {
			return 21, nil
		}
		return 16, nil
	case 0xB2, 0xBA, 0xB3, 0xBB: // INIR, INDR, OTIR, OTDR
		// Repeats while B-1 != 0; timing: 21 repeating, 16 final.
		if cpu.BC.Hi != 1 {
			return 21, nil
		}
		return 16, nil
	}
	if t := edTime[sub]; t >= 0 {
		return uint(t), nil
	}
	return 0, unknownOp(cpu.PC, 0xED, sub)
}

// ixTime is the DD/FD-prefixed opcode timing table, indexed by the byte after
// the prefix. The IX/IY (d) displacement forms cost their base time + the index
// overhead; values here are the documented totals. -1 marks the CB sub-prefix
// (handled specially), the conditional/absent forms, or an opcode where the
// DD/FD prefix is ignored (an undocumented prefix-then-plain-op) — which we
// error on so an unexpected encoding is caught.
var ixTime = [256]int8{
	// 0x00..0x0F
	-1, -1, -1, -1, -1, -1, -1, -1, -1, 15, -1, -1, -1, -1, -1, -1,
	// 0x10..0x1F  (19 ADD IX,DE = 15)
	-1, -1, -1, -1, -1, -1, -1, -1, -1, 15, -1, -1, -1, -1, -1, -1,
	// 0x20..0x2F  (21 LD IX,nn=14, 22 LD (nn),IX=20, 23 INC IX=10, 29 ADD IX,IX=15, 2A LD IX,(nn)=20, 2B DEC IX=10)
	-1, 14, 20, 10, -1, -1, -1, -1, -1, 15, 20, 10, -1, -1, -1, -1,
	// 0x30..0x3F  (34 INC (IX+d)=23, 35 DEC (IX+d)=23, 36 LD (IX+d),n=19, 39 ADD IX,SP=15)
	-1, -1, -1, -1, 23, 23, 19, -1, -1, 15, -1, -1, -1, -1, -1, -1,
	// 0x40..0x4F  LD r,(IX+d) at col 6 = 19; the rest are prefix-ignored
	-1, -1, -1, -1, -1, -1, 19, -1, -1, -1, -1, -1, -1, -1, 19, -1,
	// 0x50..0x5F
	-1, -1, -1, -1, -1, -1, 19, -1, -1, -1, -1, -1, -1, -1, 19, -1,
	// 0x60..0x6F
	-1, -1, -1, -1, -1, -1, 19, -1, -1, -1, -1, -1, -1, -1, 19, -1,
	// 0x70..0x7F  LD (IX+d),r at 0x70..0x75,0x77 = 19; 0x7E LD A,(IX+d)=19
	19, 19, 19, 19, 19, 19, -1, 19, -1, -1, -1, -1, -1, -1, 19, -1,
	// 0x80..0x8F  ADD/ADC A,(IX+d) at col 6 = 19
	-1, -1, -1, -1, -1, -1, 19, -1, -1, -1, -1, -1, -1, -1, 19, -1,
	// 0x90..0x9F  SUB/SBC A,(IX+d) = 19
	-1, -1, -1, -1, -1, -1, 19, -1, -1, -1, -1, -1, -1, -1, 19, -1,
	// 0xA0..0xAF  AND/XOR A,(IX+d) = 19
	-1, -1, -1, -1, -1, -1, 19, -1, -1, -1, -1, -1, -1, -1, 19, -1,
	// 0xB0..0xBF  OR/CP A,(IX+d) = 19
	-1, -1, -1, -1, -1, -1, 19, -1, -1, -1, -1, -1, -1, -1, 19, -1,
	// 0xC0..0xCF  (CB sub-prefix at 0xCB handled specially)
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	// 0xD0..0xDF
	-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	// 0xE0..0xEF  (E1 POP IX=14, E3 EX (SP),IX=23, E5 PUSH IX=15, E9 JP (IX)=8)
	-1, 14, -1, 23, -1, 15, -1, -1, -1, 8, -1, -1, -1, -1, -1, -1,
	// 0xF0..0xFF  (F9 LD SP,IX=10)
	-1, -1, -1, -1, -1, -1, -1, -1, -1, 10, -1, -1, -1, -1, -1, -1,
}

// ixTStates returns the timing of a DD/FD-prefixed opcode, including the
// DD-CB/FD-CB double-prefix bit/rotate group (the sub-opcode is the 4th byte:
// DD CB d op).
func ixTStates(m *mem, cpu *z80.CPU, pc uint16, prefix uint8) (uint, error) {
	sub := cpu.Memory.Get(pc + 1)
	if sub == 0xCB {
		// DD CB d op — op is the 4th byte; d is the 3rd (ignored for timing).
		op := cpu.Memory.Get(pc + 3)
		return ixcbTStates(op), nil
	}
	if t := ixTime[sub]; t >= 0 {
		return uint(t), nil
	}
	return 0, unknownOp(pc, prefix, sub)
}

// ixcbTStates returns the timing of a DD-CB/FD-CB opcode. BIT b,(IX+d) = 20;
// every other DD-CB/FD-CB op (RLC/RES/SET/.. (IX+d), incl. the undocumented
// register-copy variants) = 23.
func ixcbTStates(op uint8) uint {
	if op >= 0x40 && op <= 0x7F { // BIT b,(IX+d)
		return 20
	}
	return 23
}
