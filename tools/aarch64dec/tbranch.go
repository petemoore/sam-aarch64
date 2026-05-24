package aarch64dec

import "fmt"

// tbranch.go — special-case decoder for the test-and-branch family
// (tbz / tbnz).  The encoder emits these via a hand-rolled path (the
// branch-immediate forms in AllForms cover only the 19/26-bit branch
// kinds), so the form walk in DecodeAt can't reach them.
//
// Encoding (ARM ARM C6.2.402 / C6.2.403):
//
//	b5(31) | op(24) | 011011 | b40(5)[23:19] | imm14[18:5] | Rt[4:0]
//
//	op 0 → tbz, op 1 → tbnz.
//	bit number = (b5 << 5) | b40  (0..63).
//	b5 also selects the register width: 0 → Wt, 1 → Xt.
//	branch target = pc + (sext(imm14) << 2), rendered as a hex address
//	(objdump uses `0x...` for branch targets).
//
// Verified against aarch64-none-elf-objdump:
//
//	@0x314 37ffffea → tbnz w10, #31, 0x310
//	@0x900 3627ff60 → tbz  w0, #4, 0x8ec
func decodeTestBranch(pc uint64, word uint32) (mnem string, operands string, ok bool) {
	// bits[30:25] must be 011011 to be in the test-and-branch group.
	if (word>>25)&0x3f != 0b011011 {
		return "", "", false
	}
	op := (word >> 24) & 1
	b5 := (word >> 31) & 1
	b40 := (word >> 19) & 0x1f
	imm14 := (word >> 5) & 0x3fff
	rt := word & 0x1f

	mnem = "tbz"
	if op == 1 {
		mnem = "tbnz"
	}
	bit := (b5 << 5) | b40
	is64 := b5 == 1
	off := signExtend(imm14, 14) << 2
	target := pc + uint64(off)

	return mnem, fmt.Sprintf("%s, #%d, %#x", dpReg(rt, is64), bit, target), true
}
