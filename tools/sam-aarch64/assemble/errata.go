package assemble

// Cortex-A53 erratum workarounds (--fix-cortex-a53-835769 / -843419).
//
// This is the host *authority* implementation; the SAM-side Z80 assembler
// ports it (items i27b/i27c). Both errata afflict Cortex-A53 r0p0–r0p4 (the
// Pi 3 / Pi Zero 2 SoC) and were never fixed in silicon. The workarounds are
// an explicit, opt-in feature: off by default (later silicon such as the Pi 4
// is unaffected), on when targeting an A53.
//
// The detection predicates are ported VERBATIM from GNU binutils 2.44
// (bfd/elfnn-aarch64.c and bfd/elfxx-aarch64.c) — that is the encoding
// authority. Citations name the binutils function each block mirrors.
//
// Pipeline placement (CLAUDE.md §6 — port, don't reinvent), and where it
// differs from binutils because we are an assembler, not a linker
// post-processing opaque objects:
//
//   835769 (a data-dependency erratum — NO page-boundary condition): a
//     memory op immediately followed by a 64-bit multiply-accumulate (Ra≠XZR)
//     can corrupt the MAC result. binutils relocates the MAC into a branch-to
//     -stub patch region (it must not move already-placed code). We instead
//     insert ARM's recommended NOP between the two and re-run layout, so all
//     PC-relative references recompute exactly. A NOP only ever *separates* a
//     mem→MAC pair, so it can never create a new 835769 sequence — one
//     re-layout converges.
//
//   843419 (intrinsically link/locate-time — depends on the final VMA): an
//     ADRP whose final address sits in the last two 4-byte slots of a 4 KiB
//     page (low 12 bits 0xff8/0xffc), followed by a load/store, an optional
//     non-branch instruction, then an unsigned-immediate load/store using the
//     ADRP's destination as base. binutils prefers an in-place ADRP→ADR
//     rewrite (and only falls back to a veneer when the ±1 MiB ADR range
//     can't reach). For our small images the ADR range always fits, so the
//     in-place rewrite is the whole fix — no stubs, no address shift.

import (
	"encoding/binary"
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// ErrataOptions selects which Cortex-A53 workarounds to apply.
type ErrataOptions struct {
	Fix835769 bool
	Fix843419 bool
}

// Any reports whether any workaround is enabled.
func (o ErrataOptions) Any() bool { return o.Fix835769 || o.Fix843419 }

// erratumNOP is the AArch64 NOP (HINT #0) used as the 835769 separator.
const erratumNOP = uint32(0xd503201f)

// Pass2WithErrata runs Pass2 and applies the enabled Cortex-A53 workarounds
// to the linked image. With no workaround enabled it is exactly Pass2.
//
// Order matters: 835769 may insert NOPs and re-lay-out the image (shifting
// addresses), so it runs first; 843419 then scans the final addresses and
// rewrites in place (no shift), so it cannot disturb the 835769 result.
func Pass2WithErrata(f *format.File, p1 *Pass1Result, opts ErrataOptions) ([]byte, error) {
	out, cmap, err := Pass2WithMap(f, p1)
	if err != nil {
		return nil, err
	}
	if !opts.Any() {
		return out, nil
	}
	if opts.Fix835769 {
		out, cmap, err = fixErratum835769(f, out, cmap)
		if err != nil {
			return nil, err
		}
	}
	if opts.Fix843419 {
		if err := fixErratum843419(out, cmap); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Erratum 835769
// ---------------------------------------------------------------------------

// fixErratum835769 inserts a NOP between every (memory-op, 64-bit MAC) pair
// and, if any were inserted, re-runs Pass1+Pass2 so addresses recompute. The
// returned image and code map are the (possibly re-laid-out) final ones.
func fixErratum835769(f *format.File, out []byte, cmap []CodeWord) ([]byte, []CodeWord, error) {
	// Slide a 2-instruction window over the code stream. Adjacency is PC
	// contiguity (cur.PC == prev.PC+4): any intervening data/padding leaves
	// a gap, which is exactly binutils skipping a $d span.
	insertSet := make(map[int]bool)
	for i := 1; i < len(cmap); i++ {
		prev, cur := cmap[i-1], cmap[i]
		if cur.PC != prev.PC+4 {
			continue
		}
		if !aarch64ErratumSequence(prev.Word, cur.Word) {
			continue
		}
		ri := cur.RecIdx
		// We insert a NOP *record* before the MAC's record and re-assemble.
		// That requires the MAC to be its own INST record — true for every
		// source→binary input (the release path). A MAC living inside a
		// pre-assembled multi-word run would need splitting; the SAM side
		// (i27b) handles the overlay/run form, so flag it here rather than
		// mis-place the NOP.
		if f.Records[ri].Kind != format.KindInst {
			return nil, nil, fmt.Errorf("erratum 835769: hazard at pc=0x%x is inside a %v record, not a plain instruction; apply the workaround on the SAM side",
				uint64(cur.PC), f.Records[ri].Kind)
		}
		insertSet[ri] = true
	}
	if len(insertSet) == 0 {
		return out, cmap, nil
	}

	nop := nopRecord()
	newRecs := make([]format.Record, 0, len(f.Records)+len(insertSet))
	for ri, rec := range f.Records {
		if insertSet[ri] {
			newRecs = append(newRecs, nop)
		}
		newRecs = append(newRecs, rec)
	}
	f2 := *f
	f2.Records = newRecs

	p2, err := Pass1(&f2)
	if err != nil {
		return nil, nil, fmt.Errorf("erratum 835769 re-layout (pass1): %w", err)
	}
	out2, cmap2, err := Pass2WithMap(&f2, p2)
	if err != nil {
		return nil, nil, fmt.Errorf("erratum 835769 re-layout (pass2): %w", err)
	}
	return out2, cmap2, nil
}

// nopRecord is a single pre-assembled NOP, injected as the 835769 separator.
func nopRecord() format.Record {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], erratumNOP)
	return format.Record{Kind: format.KindLitInsts, LitCount: 1, LitWords: b[:]}
}

// ---------------------------------------------------------------------------
// Erratum 843419
// ---------------------------------------------------------------------------

// fixErratum843419 scans the laid-out image for the erratum sequence and
// rewrites each offending ADRP to an ADR in place.
func fixErratum843419(out []byte, cmap []CodeWord) error {
	for i := 0; i < len(cmap); i++ {
		insn1 := cmap[i].Word
		if !aarch64ADRP(insn1) {
			continue
		}
		// The page-offset gate: only the last two instruction slots of a
		// 4 KiB page are affected (bfd _bfd_aarch64_erratum_843419_p).
		off := uint64(cmap[i].PC) & 0xfff
		if off != 0xff8 && off != 0xffc {
			continue
		}
		// Case A — ADRP, ld/st, unsigned-imm ld/st (no intervening insn).
		if i+2 < len(cmap) &&
			cmap[i+1].PC == cmap[i].PC+4 && cmap[i+2].PC == cmap[i].PC+8 &&
			aarch64Erratum843419SequenceP(insn1, cmap[i+1].Word, cmap[i+2].Word) {
			if err := rewriteADRPtoADR(out, cmap[i]); err != nil {
				return err
			}
			continue
		}
		// Case B — ADRP, ld/st, one non-branch insn, unsigned-imm ld/st.
		if i+3 < len(cmap) &&
			cmap[i+1].PC == cmap[i].PC+4 && cmap[i+2].PC == cmap[i].PC+8 &&
			cmap[i+3].PC == cmap[i].PC+12 &&
			aarch64Erratum843419SequenceP(insn1, cmap[i+1].Word, cmap[i+3].Word) {
			if err := rewriteADRPtoADR(out, cmap[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// rewriteADRPtoADR replaces the ADRP word at cw with an ADR computing the
// identical address as a byte-exact PC-relative offset (bfd
// _bfd_aarch64_erratum_843419_branch_to_stub, ERRAT_ADR path).
func rewriteADRPtoADR(out []byte, cw CodeWord) error {
	insn := cw.Word
	place := uint64(cw.PC)
	imm := aarch64SignExtend(uint64(aarch64DecodeADRPImm(insn))<<12, 33) - int64(place&0xfff)
	if imm < aarch64MinADRPImm || imm > aarch64MaxADRPImm {
		// Out of ADR's ±1 MiB reach. binutils would emit a veneer; our
		// images are far too small for this, so treat it as a hard error
		// rather than silently mis-encode (faithful to the =adr path).
		return fmt.Errorf("erratum 843419 @ pc=0x%x: ADR offset %d out of ±1MiB range (image too large for the in-place rewrite)", place, imm)
	}
	newInsn := aarch64ReencodeADRImm(aarch64ADROp, uint32(imm)) | aarch64RT(insn)
	binary.LittleEndian.PutUint32(out[cw.Off:], newInsn)
	return nil
}

// ---------------------------------------------------------------------------
// binutils 2.44 predicates (ported verbatim — bfd/elfnn-aarch64.c,
// bfd/elfxx-aarch64.{c,h}). These are the encoding authority.
// ---------------------------------------------------------------------------

// mask returns (1<<n)-1 (bfd MASK).
func mask(n uint) uint32 { return (1 << n) - 1 }

// aarch64Bits/Bit/field accessors — bfd AARCH64_BITS and the RT/RN/… macros.
func aarch64Bits(insn uint32, pos, n uint) uint32 { return (insn >> pos) & mask(n) }
func aarch64Bit(insn uint32, pos uint) uint32     { return aarch64Bits(insn, pos, 1) }
func aarch64RT(insn uint32) uint32                { return aarch64Bits(insn, 0, 5) }
func aarch64RT2(insn uint32) uint32               { return aarch64Bits(insn, 10, 5) }
func aarch64RA(insn uint32) uint32                { return aarch64Bits(insn, 10, 5) }
func aarch64RD(insn uint32) uint32                { return aarch64Bits(insn, 0, 5) }
func aarch64RN(insn uint32) uint32                { return aarch64Bits(insn, 5, 5) }
func aarch64RM(insn uint32) uint32                { return aarch64Bits(insn, 16, 5) }
func aarch64OP31(insn uint32) uint32              { return aarch64Bits(insn, 21, 3) }

const aarch64ZR = 0x1f

// Load/store decode masks (bfd AARCH64_LD/LDST*).
func aarch64LD(insn uint32) bool        { return aarch64Bit(insn, 22) == 1 }
func aarch64LDST(insn uint32) bool      { return insn&0x0a000000 == 0x08000000 }
func aarch64LDSTEX(insn uint32) bool    { return insn&0x3f000000 == 0x08000000 }
func aarch64LDSTPCREL(insn uint32) bool { return insn&0x3b000000 == 0x18000000 }
func aarch64LDSTNAP(insn uint32) bool   { return insn&0x3b800000 == 0x28000000 }
func aarch64LDSTPPI(insn uint32) bool   { return insn&0x3b800000 == 0x28800000 }
func aarch64LDSTPO(insn uint32) bool    { return insn&0x3b800000 == 0x29000000 }
func aarch64LDSTPPRE(insn uint32) bool  { return insn&0x3b800000 == 0x29800000 }
func aarch64LDSTUI(insn uint32) bool    { return insn&0x3b200c00 == 0x38000000 }
func aarch64LDSTPIIMM(insn uint32) bool { return insn&0x3b200c00 == 0x38000400 }
func aarch64LDSTU(insn uint32) bool     { return insn&0x3b200c00 == 0x38000800 }
func aarch64LDSTPREIMM(insn uint32) bool {
	return insn&0x3b200c00 == 0x38000c00
}
func aarch64LDSTRO(insn uint32) bool      { return insn&0x3b200c00 == 0x38200800 }
func aarch64LDSTUIMM(insn uint32) bool    { return insn&0x3b000000 == 0x39000000 }
func aarch64LDSTSIMDM(insn uint32) bool   { return insn&0xbfbf0000 == 0x0c000000 }
func aarch64LDSTSIMDMPI(insn uint32) bool { return insn&0xbfa00000 == 0x0c800000 }
func aarch64LDSTSIMDS(insn uint32) bool   { return insn&0xbf9f0000 == 0x0d000000 }
func aarch64LDSTSIMDSPI(insn uint32) bool { return insn&0xbf800000 == 0x0d800000 }

// aarch64MAC reports the 64-bit data-processing 3-source group (bfd AARCH64_MAC).
func aarch64MAC(insn uint32) bool { return insn&0xff000000 == 0x9b000000 }

// aarch64MlxlP reports a 64-bit multiply-accumulate with Ra≠XZR (bfd
// aarch64_mlxl_p): MADD/MSUB/SMADDL/SMSUBL/UMADDL/UMSUBL. The Ra≠XZR test
// excludes the MUL/MNEG aliases.
func aarch64MlxlP(insn uint32) bool {
	op31 := aarch64OP31(insn)
	return aarch64MAC(insn) && (op31 == 0 || op31 == 1 || op31 == 5) && aarch64RA(insn) != aarch64ZR
}

// aarch64MemOp classifies a load/store (bfd aarch64_mem_op_p). It returns
// ok=false for non-memory instructions; otherwise rt/rt2/pair/load as bfd
// computes them.
func aarch64MemOp(insn uint32) (rt, rt2 uint32, pair, load, ok bool) {
	if !aarch64LDST(insn) {
		return 0, 0, false, false, false
	}
	switch {
	case aarch64LDSTEX(insn):
		rt = aarch64RT(insn)
		rt2 = rt
		if aarch64Bit(insn, 21) == 1 {
			pair = true
			rt2 = aarch64RT2(insn)
		}
		load = aarch64LD(insn)
		return rt, rt2, pair, load, true
	case aarch64LDSTNAP(insn) || aarch64LDSTPPI(insn) || aarch64LDSTPO(insn) || aarch64LDSTPPRE(insn):
		pair = true
		rt = aarch64RT(insn)
		rt2 = aarch64RT2(insn)
		load = aarch64LD(insn)
		return rt, rt2, pair, load, true
	case aarch64LDSTPCREL(insn) || aarch64LDSTUI(insn) || aarch64LDSTPIIMM(insn) ||
		aarch64LDSTU(insn) || aarch64LDSTPREIMM(insn) || aarch64LDSTRO(insn) || aarch64LDSTUIMM(insn):
		rt = aarch64RT(insn)
		rt2 = rt
		if aarch64LDSTPCREL(insn) {
			load = true
		}
		opc := aarch64Bits(insn, 22, 2)
		v := aarch64Bit(insn, 26)
		opcV := opc | (v << 2)
		load = opcV == 1 || opcV == 2 || opcV == 3 || opcV == 5 || opcV == 7
		return rt, rt2, false, load, true
	case aarch64LDSTSIMDM(insn) || aarch64LDSTSIMDMPI(insn):
		rt = aarch64RT(insn)
		load = aarch64Bit(insn, 22) == 1
		switch (insn >> 12) & 0xf {
		case 0, 2:
			rt2 = rt + 3
		case 4, 6:
			rt2 = rt + 2
		case 7:
			rt2 = rt
		case 8, 10:
			rt2 = rt + 1
		default:
			return 0, 0, false, false, false
		}
		return rt, rt2, false, load, true
	case aarch64LDSTSIMDS(insn) || aarch64LDSTSIMDSPI(insn):
		rt = aarch64RT(insn)
		r := (insn >> 21) & 1
		load = aarch64Bit(insn, 22) == 1
		switch (insn >> 13) & 0x7 {
		case 0, 2, 4, 6:
			rt2 = rt + r
		case 1, 3, 5, 7:
			if r == 0 {
				rt2 = rt + 2
			} else {
				rt2 = rt + 3
			}
		default:
			return 0, 0, false, false, false
		}
		return rt, rt2, false, load, true
	}
	return 0, 0, false, false, false
}

// aarch64ErratumSequence reports whether (insn_1, insn_2) is an 835769 hazard:
// a memory op immediately followed by a 64-bit MAC, excluding the true-RAW
// -dependency carve-out (bfd aarch64_erratum_sequence).
func aarch64ErratumSequence(insn1, insn2 uint32) bool {
	if !aarch64MlxlP(insn2) {
		return false
	}
	rt, rt2, pair, load, ok := aarch64MemOp(insn1)
	if !ok {
		return false
	}
	// Any SIMD memory op is independent of the MAC by definition.
	if aarch64Bit(insn1, 26) == 1 {
		return true
	}
	rn := aarch64RN(insn2)
	ra := aarch64RA(insn2)
	rm := aarch64RM(insn2)
	// A true (RAW) dependency means the pipeline already serialises: safe.
	if load && (rt == rn || rt == rm || rt == ra ||
		(pair && (rt2 == rn || rt2 == rm || rt2 == ra))) {
		return false
	}
	// All other cases (including writebacks) are conservatively a hazard.
	return true
}

// ADRP / ADR constants (bfd elfxx-aarch64.h).
const (
	aarch64ADROp      = 0x10000000
	aarch64ADRPOp     = 0x90000000
	aarch64ADRPOpMask = 0x9f000000
	aarch64MaxADRPImm = (1 << 20) - 1
	aarch64MinADRPImm = -(1 << 20)
)

// aarch64ADRP reports an ADRP (bfd _bfd_aarch64_adrp_p).
func aarch64ADRP(insn uint32) bool { return insn&aarch64ADRPOpMask == aarch64ADRPOp }

// aarch64Erratum843419SequenceP implements bfd
// _bfd_aarch64_erratum_843419_sequence_p (sequence 1): insn_2 a load/store
// (a load-pair is excluded; a store-pair is allowed), insn_3 an unsigned-imm
// load/store whose base register is the ADRP's destination.
func aarch64Erratum843419SequenceP(insn1, insn2, insn3 uint32) bool {
	_, _, pair, load, ok := aarch64MemOp(insn2)
	if !ok {
		return false
	}
	return (!pair || (pair && !load)) &&
		aarch64LDSTUIMM(insn3) &&
		aarch64RN(insn3) == aarch64RD(insn1)
}

// aarch64SignExtend sign-extends the low `bits` of value (bfd
// _bfd_aarch64_sign_extend).
func aarch64SignExtend(value uint64, bits uint) int64 {
	if value&(1<<(bits-1)) != 0 {
		value |= ^uint64(0) << bits
	}
	return int64(value)
}

// aarch64DecodeADRPImm extracts the 21-bit immhi:immlo of an ADRP (bfd
// _bfd_aarch64_decode_adrp_imm).
func aarch64DecodeADRPImm(insn uint32) uint32 {
	return (((insn >> 5) & mask(19)) << 2) | ((insn >> 29) & mask(2))
}

// aarch64ReencodeADRImm places imm into an ADR's immhi:immlo (bfd
// _bfd_aarch64_reencode_adr_imm).
func aarch64ReencodeADRImm(insn, imm uint32) uint32 {
	return (insn &^ ((mask(2) << 29) | (mask(19) << 5))) |
		((imm & mask(2)) << 29) | ((imm & (mask(19) << 2)) << 3)
}
