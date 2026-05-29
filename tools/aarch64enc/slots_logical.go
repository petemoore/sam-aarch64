package aarch64enc

import (
	"fmt"
	"math/bits"
)

// EncodeLogicalImmPub is the exported wrapper used by pass2 for bic-immediate
// encoding (where the operand has already been inverted to ~imm by the caller).
func EncodeLogicalImmPub(slot OperandSlot, imm int64, is64 bool) (uint32, error) {
	return encodeLogicalImm(slot, imm, is64)
}

// encodeLogicalImm implements ARM's bitmask-immediate encoding
// (N:1, immr:6, imms:6), following LLVM's processLogicalImmediate.
// Returns the 13-bit packed (N|immr|imms) at slot.BitPosition, or
// an error if the value is not encodable.
//
// Source: LLVM-project AArch64AddressingModes.h ::processLogicalImmediate.
func encodeLogicalImm(slot OperandSlot, imm int64, is64 bool) (uint32, error) {
	u := uint64(imm)
	if !is64 {
		u = u & 0xFFFFFFFF
		u = u | (u << 32)
	}
	if u == 0 || u == ^uint64(0) {
		return 0, fmt.Errorf("LogicalImm: 0 and -1 cannot be encoded")
	}

	// Find smallest replicating element size.
	size := 64
	for size > 2 {
		nextSize := size / 2
		mask := (uint64(1) << nextSize) - 1
		if (u & mask) != ((u >> nextSize) & mask) {
			break
		}
		size = nextSize
	}

	mask := (uint64(1) << size) - 1
	if size < 64 {
		// Verify the pattern truly replicates at `size`.
		element := u & mask
		check := element
		for s := size; s < 64; s += size {
			check |= element << s
		}
		if check != u {
			return 0, fmt.Errorf("LogicalImm: non-replicating pattern")
		}
		u = element
	}
	element := u & mask

	ones := bits.OnesCount64(element)
	if ones == 0 || ones == size {
		return 0, fmt.Errorf("LogicalImm: element not encodable")
	}

	// Rotate right until bit 0 is the start of the ones-run — i.e.
	// rotate so the value becomes (1<<ones) - 1 (all ones in the
	// low `ones` bits). The ones-run may wrap around the element
	// (e.g. 0xfffffffd has a 31-bit run wrapping from bit 2 through
	// bit 0), so we cannot rely on "rotate while LSB == 0": instead
	// we scan for the first 0→1 transition and rotate by that amount.
	expected := (uint64(1) << ones) - 1
	rotation := -1
	for r := 0; r < size; r++ {
		// Try rotating right by r.
		rotated := ((element >> r) | (element << (size - r))) & mask
		if rotated == expected {
			rotation = r
			break
		}
	}
	if rotation < 0 {
		return 0, fmt.Errorf("LogicalImm: not a single ones-run")
	}

	var n uint32
	// We rotated the value RIGHT by `rotation` to recover the canonical
	// (1<<ones)-1 pattern. The encoded immr is the inverse rotation
	// (i.e. ARM's DecodeBitMasks does `welem ROR immr` to recover the
	// original value), so immr = (size - rotation) modulo size.
	var immr uint32 = uint32(rotation)
	if rotation != 0 {
		immr = uint32(size - rotation)
	}
	var imms uint32

	if size == 64 {
		n = 1
		imms = uint32(ones - 1)
	} else {
		n = 0
		// For element size 2^len (len = sizeLog2):
		// The decoder finds len = highest set bit of N:NOT(imms).
		// We need NOT(imms)[len]=1 and NOT(imms)[k]=0 for k > len (k ≤ 5).
		// This means imms[k]=1 for k > len (i.e., k in [len+1, 5]) and
		// imms[len]=0, with imms[len-1:0] = ones-1.
		// nimmsTop = bits [5 : len+1] set = ~((1 << (len+1)) - 1) & 0x3F.
		// (ARM DDI 0487L.a C4.1.64 "DecodeBitMasks".)
		sizeLog2 := 0
		for s := size; s > 1; s >>= 1 {
			sizeLog2++
		}
		nimmsTop := ^uint32((1<<(sizeLog2+1))-1) & 0x3F
		imms = nimmsTop | uint32(ones-1)
	}

	combined := (n << 12) | (immr << 6) | imms
	return combined << slot.BitPosition, nil
}
