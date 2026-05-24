package aarch64dec

import (
	"fmt"

	"github.com/petemoore/sam-aarch64/tools/aarch64enc"
)

// decodeLogicalImm is the inverse of aarch64enc.encodeLogicalImm:
// expand the (N:1, immr:6, imms:6) 13-bit field at slot.BitPosition
// back into the 32- or 64-bit bitmask immediate it represents, then
// render it as `#<hex>` matching objdump.
//
// Width selection (32 vs 64) comes from bit 31 of the instruction
// word (the SF bit) — every logical-immediate Form in our table has
// SF as the high bit of Pattern, so we read it directly rather than
// passing width through the Form metadata.  See ARM ARM C6.2.10
// (AND immediate): bit 31 == sf.
//
// Implementation follows ARM ARM C4.1.64 "DecodeBitMasks": find
// element size from N:NOT(imms), build a base run of (S+1) ones,
// rotate right by R, then replicate to the destination width.
//
// Verified:
//
//	echo -ne '\x20\x00\x7c\x92' > /tmp/t.bin
//	aarch64-elf-objdump -D -b binary -m aarch64 /tmp/t.bin
//	  → 0:  927c0420  and  x0, x1, #0x30      ← wait, hex differs
//	# (0x927c0420 from the encoder for `and x0, x1, #0x30`; little-
//	# endian on disk is 20 04 7c 92.)
func decodeLogicalImm(word uint32, slot aarch64enc.OperandSlot) string {
	field := extractBits(word, slot.BitPosition, 13)
	n := (field >> 12) & 1
	immr := (field >> 6) & 0x3F
	imms := field & 0x3F

	sf := (word >> 31) & 1
	regsize := 32
	if sf == 1 {
		regsize = 64
	}

	value, ok := decodeBitMasks(n, immr, imms, regsize)
	if !ok {
		// Should never happen for words that match a real Form's
		// Pattern + Mask, but guard anyway so a corrupt input
		// produces visible output rather than a panic.
		return fmt.Sprintf("#<invalid logical imm N=%d immr=%d imms=%d>", n, immr, imms)
	}
	return fmt.Sprintf("#%#x", value)
}

// decodeBitMasks expands (N, immr, imms) to the regsize-bit bitmask
// immediate per ARM ARM C4.1.64.  Returns (value, false) when the
// fields are invalid (e.g. element-size discovery fails).
func decodeBitMasks(n, immr, imms uint32, regsize int) (uint64, bool) {
	// len = highest set bit of (n:NOT(imms)), where NOT is on 6 bits.
	// For regsize=32, n must be 0 (and len ranges 0..5); for regsize=64,
	// n may be 0 or 1 (len ranges 0..6).
	notImms := (^imms) & 0x3F
	combined := (n << 6) | notImms
	length := -1
	for i := 6; i >= 0; i-- {
		if (combined>>uint(i))&1 == 1 {
			length = i
			break
		}
	}
	if length < 1 {
		// length = 0 is not a valid encoding (element size would be 1 bit).
		// length = -1 means all-zero combined, also invalid.
		// Note ARM defines length >= 1 only — single-bit elements would
		// require a degenerate all-ones/all-zeros mask.
		if length != 0 {
			return 0, false
		}
		// length==0 → element size 2 bits.  Allowed.
	}
	esize := 1 << uint(length)
	if esize > regsize {
		return 0, false
	}
	levels := uint32(esize - 1)
	s := imms & levels
	r := immr & levels
	if s == levels {
		// All-ones run within the element — also invalid per ARM.
		return 0, false
	}
	// welem = (1 << (S+1)) - 1, all-ones run of (S+1) bits.
	welem := (uint64(1) << (s + 1)) - 1
	// Rotate right by R within esize bits.
	mask := (uint64(1) << uint(esize)) - 1
	welem &= mask
	rotated := ((welem >> r) | (welem << (uint32(esize) - r))) & mask
	// Replicate rotated (esize bits) up to regsize.
	result := uint64(0)
	for shift := 0; shift < regsize; shift += esize {
		result |= rotated << uint(shift)
	}
	if regsize == 32 {
		result &= 0xFFFFFFFF
	}
	return result, true
}

