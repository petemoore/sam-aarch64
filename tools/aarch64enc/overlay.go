package aarch64enc

import "fmt"

// FoldSlot identifies one relocated bitfield in the compact-`.tbn` v2
// instruction overlay (M8 / i39a). Each instruction in an INSN_RUN record
// is stored as an assembled base word with its relocated field(s) zeroed,
// plus a sparse overlay of {FoldSlot, expression-bytecode} patches. At
// assemble time the expression is evaluated to a value and Fold turns that
// value into the field's bits, which are ORed into the base word.
//
// The byte values are a wire contract: they are written into the `.tbn`
// and must match the Z80 decoder's slot dispatch. Append-only.
type FoldSlot byte

const (
	// PC-dependent slots — the fold depends on the instruction's PC.
	FoldBranch26 FoldSlot = 1 // b/bl: imm26 @0  = (target-pc)/4
	FoldBranch19 FoldSlot = 2 // b.cc/cbz/cbnz/ldr-literal: imm19 @5 = (target-pc)/4
	FoldBranch14 FoldSlot = 3 // tbz/tbnz: imm14 @5 = (target-pc)/4
	FoldAdr      FoldSlot = 4 // adr: immlo@29:immhi@5 = (target-pc)
	FoldAdrp     FoldSlot = 5 // adrp: immlo@29:immhi@5 = page(target)-page(pc) /4096
	// PC-invariant symbol-bearing slots — the fold uses the value directly.
	FoldAddSubImm12 FoldSlot = 6  // add/sub/cmp imm: (sh,imm12) @10 (:lo12:, symbol-diff)
	FoldMemImm12    FoldSlot = 7  // ldr/str scaled offset: imm12 @10 = byteOff/scale
	FoldMemImm9     FoldSlot = 8  // stur/ldur/pre/post: imm9 @12 (signed byte offset)
	FoldMovkImm16   FoldSlot = 9  // explicit movz/movk: imm16 @5 (= value&0xFFFF; hw in base)
	FoldLogical     FoldSlot = 10 // orr/and/eor/bic imm: N:immr:imms @10 (bitmask immediate)
	FoldPairImm7    FoldSlot = 11 // ldp/stp: imm7 @15 = byteOff/scale (signed)
	// Litpool — PC-dependent, but the value is the pool-entry PC (looked up
	// by the instruction's PC), not an evaluated expression.
	FoldLitpool19 FoldSlot = 12 // ldr =expr: imm19 @5 = (poolPC-pc)/4
	// FoldMovzAuto is `mov Rd, #value` collapsed to movz: the value is the
	// full constant and the base word's hw field selects the 16-bit chunk.
	FoldMovzAuto FoldSlot = 13 // mov-imm: imm16 @5 = (value >> hw*16)&0xFFFF
)

// Fold computes the bits a relocated field contributes, given the resolved
// value of its patch expression, the instruction's PC, and the assembled
// base word (whose size/opc/hw fields some folds consult). The result is
// ORed into the base word's zeroed field. Each rule mirrors, exactly, the
// conversion the literal encoder performs for that field — see the cited
// pass2.go / slot-encoder source — so the overlay and literal paths cannot
// diverge. For litpool, `value` is the pool-entry PC, not an eval result.
func Fold(slot FoldSlot, value int64, pc int64, baseWord uint32) (uint32, error) {
	switch slot {
	case FoldBranch26:
		// operandsToValues: v = v - pc; encodeBranchImm: /4, imm26 @0.
		return encodeBranchImm(OperandSlot{BitPosition: 0, BitWidth: 26}, value-pc)
	case FoldBranch19:
		return encodeBranchImm(OperandSlot{BitPosition: 5, BitWidth: 19}, value-pc)
	case FoldBranch14:
		// encodeTbzTbnz: imm14 = (target-pc)/4 @5.
		return encodeBranchImm(OperandSlot{BitPosition: 5, BitWidth: 14}, value-pc)
	case FoldAdr:
		// operandsToValues AdrImm: v = v - pc; encodeAdrImm: immlo@29/immhi@5.
		return encodeAdrImm(OperandSlot{}, value-pc)
	case FoldAdrp:
		// operandsToValues AdrpImm: page diff masked to a signed 33-bit
		// value; encodeAdrpImm: /4096, immlo@29/immhi@5.
		diff := (value &^ int64(0xFFF)) - (pc &^ int64(0xFFF))
		const mask33 = int64(1<<33 - 1)
		diff &= mask33
		if diff&(int64(1)<<32) != 0 {
			diff |= ^mask33
		}
		return encodeAdrpImm(OperandSlot{}, diff)
	case FoldAddSubImm12:
		// Imm12Shifted: no PC conversion; value used directly (sh bit @22).
		return encodeImm12Shifted(OperandSlot{BitPosition: 10}, value)
	case FoldMemImm12:
		// encodeMemInst MemBaseOff: imm12 = byteOffset/scale @10; scale is
		// recoverable from the base word's size field (bits 31:30).
		scale := int64(1) << ((baseWord >> 30) & 3)
		if value < 0 || value%scale != 0 || value/scale >= (1<<12) {
			return 0, fmt.Errorf("FoldMemImm12: byte offset %d not a scaled imm12 (scale %d)", value, scale)
		}
		return uint32(value/scale) << 10, nil
	case FoldMemImm9:
		// encodeUnscaledMemInst / pre/post: signed 9-bit byte offset @12.
		if value < -256 || value > 255 {
			return 0, fmt.Errorf("FoldMemImm9: value %d out of [-256,255]", value)
		}
		return (uint32(value) & 0x1ff) << 12, nil
	case FoldMovkImm16:
		// Explicit movz/movk via the form table: text2bin packs the hw
		// shift into bits 17:16 of the operand value (encodeImm16Shifted
		// derives hw from there), so imm16 is the low 16 bits and hw stays
		// in the base word.
		return uint32(uint64(value)&0xFFFF) << 5, nil
	case FoldMovzAuto:
		// `mov Rd, #value` collapsed to movz (i48b): the value alone
		// determines both the hw shift and imm16 — text→overlay never
		// pre-bakes hw, so the base word's bits 22:21 are zeroed and the
		// fold computes hw here. Mirrors tryEncodeMovImm (pass2.go:494) —
		// the lowest 16-bit chunk position whose single chunk reproduces
		// the value. is64 (W vs X) comes from the base word's sf bit.
		is64 := (baseWord>>31)&1 == 1
		u := uint64(value)
		if !is64 {
			u &= 0xFFFFFFFF
		}
		for shift := uint(0); shift < 64; shift += 16 {
			if !is64 && shift >= 32 {
				break
			}
			chunk := (u >> shift) & 0xFFFF
			if chunk<<shift == u {
				hw := uint32(shift / 16)
				return (uint32(chunk) << 5) | (hw << 21), nil
			}
		}
		return 0, fmt.Errorf("FoldMovzAuto: value %#x not movz-encodable", uint64(value))
	case FoldLogical:
		// encodeLogicalImm: bitmask immediate N:immr:imms @10; is64 from sf.
		is64 := (baseWord>>31)&1 == 1
		return encodeLogicalImm(OperandSlot{BitPosition: 10}, value, is64)
	case FoldPairImm7:
		// encodePairInst: imm7 = byteOffset/scale @15 (signed [-64,63]);
		// scale 8 (X-pair) or 4 (W-pair) from the opc/sf bit.
		scale := int64(4)
		if (baseWord>>31)&1 == 1 {
			scale = 8
		}
		if value%scale != 0 {
			return 0, fmt.Errorf("FoldPairImm7: byte offset %d not a multiple of %d", value, scale)
		}
		so := value / scale
		if so < -64 || so > 63 {
			return 0, fmt.Errorf("FoldPairImm7: scaled offset %d out of [-64,63]", so)
		}
		return (uint32(so) & 0x7F) << 15, nil
	case FoldLitpool19:
		// encodeLdrLitPoolInst: imm19 = (poolPC-pc)/4 @5.
		return encodeBranchImm(OperandSlot{BitPosition: 5, BitWidth: 19}, value-pc)
	}
	return 0, fmt.Errorf("Fold: unknown slot %d", slot)
}

// FoldSlotForKind maps a form-table SlotKind (the encoder's per-operand
// slot) to the overlay FoldSlot for a symbol/PC-bearing operand in that
// slot. ok=false for slot kinds that never carry a relocatable expression
// (registers, condition codes, constant shift/extend amounts). The
// hand-rolled families (mem, litpool, tbz, ldr-literal, mov-imm) are
// classified by their encoder path, not this table.
func FoldSlotForKind(k SlotKind) (FoldSlot, bool) {
	switch k {
	case BranchImm26:
		return FoldBranch26, true
	case BranchImm19:
		return FoldBranch19, true
	case BranchImm14:
		return FoldBranch14, true
	case AdrImm:
		return FoldAdr, true
	case AdrpImm:
		return FoldAdrp, true
	case Imm12Shifted:
		return FoldAddSubImm12, true
	case Imm16Shifted:
		return FoldMovkImm16, true
	case LogicalImm:
		return FoldLogical, true
	}
	return 0, false
}

// ZeroSlot clears the bit-range a slot's Fold writes into, recovering the
// patch-free base word. The cleared range matches Fold's output bits
// exactly (locked by TestZeroSlotClearsFoldBits).
func ZeroSlot(word uint32, slot FoldSlot) uint32 {
	switch slot {
	case FoldBranch26:
		return word &^ 0x03FFFFFF
	case FoldBranch19, FoldLitpool19:
		return word &^ (0x7FFFF << 5)
	case FoldBranch14:
		return word &^ (0x3FFF << 5)
	case FoldAdr, FoldAdrp:
		return word &^ ((0x3 << 29) | (0x7FFFF << 5))
	case FoldAddSubImm12, FoldLogical:
		return word &^ (0x1FFF << 10)
	case FoldMemImm12:
		return word &^ (0xFFF << 10)
	case FoldMemImm9:
		return word &^ (0x1FF << 12)
	case FoldMovkImm16:
		// Explicit movz/movk: hw stays in the base word (syntactic lsl #N).
		return word &^ (0xFFFF << 5)
	case FoldMovzAuto:
		// Auto-movz (i48b): hw is computed in the fold, so clear it (22:21)
		// alongside imm16 (@5) — the base word carries neither.
		return word &^ ((0xFFFF << 5) | (0x3 << 21))
	case FoldPairImm7:
		return word &^ (0x7F << 15)
	}
	return word
}
