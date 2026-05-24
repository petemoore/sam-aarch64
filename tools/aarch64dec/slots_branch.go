package aarch64dec

import (
	"fmt"

	"github.com/petemoore/sam-aarch64/tools/aarch64enc"
)

// decodeBranchImm is the inverse of aarch64enc.encodeBranchImm.  The
// encoder takes a byte offset and stores byteOffset/4 as a sign-
// extended BitWidth-bit immediate at slot.BitPosition.  To render,
// we sign-extend the BitWidth-bit field back to a signed instruction
// offset, multiply by 4 to get the byte offset, add to pc, and
// emit the resulting absolute byte address as `0x<hex>` (no `#`).
//
// Verified against objdump:
//
//	echo -ne '\x04\x00\x00\x14' > /tmp/t.bin   # b 0x10 at pc=0
//	aarch64-elf-objdump -D -b binary -m aarch64 /tmp/t.bin
//	  → 0:  14000004  b  0x10
//
// Negative offsets show as the unsigned 64-bit absolute address
// (e.g. 0x97fffff1 = `bl 0xffffffffffffffd0`); we match by treating
// pc + signed_offset as a uint64 with wraparound, which is what
// objdump prints.  See also the BranchImm14 (tbz/tbnz) encoder which
// uses the same encodeBranchImm helper.
func decodeBranchImm(ctx decodeCtx, slot aarch64enc.OperandSlot) string {
	bits := extractBits(ctx.word, slot.BitPosition, slot.BitWidth)
	// Sign-extend bits from BitWidth to 32, then to 64.
	instrOffset := signExtend32(bits, slot.BitWidth)
	byteOffset := int64(instrOffset) * 4
	target := ctx.pc + uint64(byteOffset)
	return fmt.Sprintf("%#x", target)
}

// decodeAdrpImm is the inverse of aarch64enc.encodeAdrpImm.  ADRP
// packs a 21-bit signed page offset (in 4096-byte pages) split
// across the instruction word: immlo (2 bits) at bits 30:29, immhi
// (19 bits) at bits 5:23.  We reassemble, sign-extend to 64 bits,
// shift left by 12 (to get a byte offset), mask the bottom 12 bits
// of pc, and add — objdump's exact formula (it then renders the
// result as `0x<hex>`).
//
// Verified:
//
//	echo -ne '\x00\x00\x00\xb0' > /tmp/t.bin
//	aarch64-elf-objdump -D -b binary -m aarch64 /tmp/t.bin
//	  → 0:  b0000000  adrp  x0, 0x1000
func decodeAdrpImm(ctx decodeCtx) string {
	immlo := extractBits(ctx.word, 29, 2)
	immhi := extractBits(ctx.word, 5, 19)
	imm21 := (immhi << 2) | immlo
	pageOffset := signExtend64(uint64(imm21), 21)
	target := (ctx.pc &^ 0xFFF) + uint64(pageOffset<<12)
	return fmt.Sprintf("%#x", target)
}

// decodeAdrImm is the inverse of aarch64enc.encodeAdrImm.  Same bit
// layout as ADRP — (immlo:2, immhi:19) — but the encoded value is a
// raw byte offset, not a page count, and the base is the full pc
// (no page-masking).
//
// Verified:
//
//	echo -ne '\x20\x00\x00\x10' > /tmp/t.bin
//	aarch64-elf-objdump -D -b binary -m aarch64 /tmp/t.bin
//	  → 0:  10000020  adr  x0, 0x4
func decodeAdrImm(ctx decodeCtx) string {
	immlo := extractBits(ctx.word, 29, 2)
	immhi := extractBits(ctx.word, 5, 19)
	imm21 := (immhi << 2) | immlo
	byteOffset := signExtend64(uint64(imm21), 21)
	target := ctx.pc + uint64(byteOffset)
	return fmt.Sprintf("%#x", target)
}

// signExtend32 sign-extends an n-bit unsigned value to int32.  Both
// the input bits and `n` are clamped: 1 ≤ n ≤ 32.
func signExtend32(bits uint32, n uint8) int32 {
	shift := 32 - uint(n)
	return int32(bits<<shift) >> shift
}

// signExtend64 sign-extends an n-bit unsigned value to int64.
// 1 ≤ n ≤ 64.
func signExtend64(bits uint64, n uint8) int64 {
	shift := 64 - uint(n)
	return int64(bits<<shift) >> shift
}
