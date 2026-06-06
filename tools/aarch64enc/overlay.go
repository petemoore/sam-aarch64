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
	FoldMovkImm16   FoldSlot = 9  // mov/movz/movk: imm16 @5 (hw selects the 16-bit chunk)
	FoldLogical     FoldSlot = 10 // orr/and/eor/bic imm: N:immr:imms @10 (bitmask immediate)
	FoldPairImm7    FoldSlot = 11 // ldp/stp: imm7 @15 = byteOff/scale (signed)
	// Litpool — PC-dependent, but the value is the pool-entry PC (looked up
	// by the instruction's PC), not an evaluated expression.
	FoldLitpool19 FoldSlot = 12 // ldr =expr: imm19 @5 = (poolPC-pc)/4
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
		// encodeImm16Shifted / tryEncodeMovImm: the 16-bit chunk at the hw
		// shift carried in the base word (bits 22:21). For an expression
		// that already extracted a chunk (:abs_gN:) hw is 0 and this is a
		// no-op shift; for `mov Rd,#sym` hw selects the movz slot.
		hw := (baseWord >> 21) & 3
		chunk := (uint64(value) >> (hw * 16)) & 0xFFFF
		return uint32(chunk) << 5, nil
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
		return word &^ (0xFFFF << 5)
	case FoldPairImm7:
		return word &^ (0x7F << 15)
	}
	return word
}
