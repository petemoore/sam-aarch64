package aarch64dec

import "github.com/petemoore/sam-aarch64/tools/aarch64enc"

// BranchTarget returns the target address for a direct branch at pc,
// or (0, false) for any other instruction.  Covers b, bl, b.<cond>,
// cbz, cbnz, tbz, tbnz.  Does NOT cover indirect branches (blr, br,
// ret) or adrp/adr — those have no fixed compile-time target.
func BranchTarget(pc uint64, word uint32) (uint64, bool) {
	// tbz / tbnz: bits[30:25] == 0b011011 (not in AllForms; hand-rolled
	// in the encoder — same pattern check as decodeTestBranch in tbranch.go).
	if (word>>25)&0x3f == 0b011011 {
		imm14 := (word >> 5) & 0x3fff
		off := signExtend(imm14, 14) << 2
		return pc + uint64(off), true
	}
	// Walk AllForms for entries that carry BranchImm26/19/14 slots.
	// Mirror the arithmetic in decodeBranchImm (slots_branch.go).
	for _, f := range aarch64enc.AllForms() {
		if word&f.Mask != f.Pattern {
			continue
		}
		for _, slot := range f.Slots {
			switch slot.SlotKind {
			case aarch64enc.BranchImm26, aarch64enc.BranchImm19, aarch64enc.BranchImm14:
				bits := extractBits(word, slot.BitPosition, slot.BitWidth)
				instrOff := signExtend32(bits, slot.BitWidth)
				byteOff := int64(instrOff) * 4
				return pc + uint64(byteOff), true
			}
		}
	}
	return 0, false
}
