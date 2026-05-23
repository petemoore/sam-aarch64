package aarch64enc

import (
	"fmt"
	"math/bits"
)

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

	// Rotate until the low ones-run starts at bit 0.
	rotation := 0
	for element&1 == 0 {
		element = ((element >> 1) | (element << (size - 1))) & mask
		rotation++
	}
	expected := (uint64(1) << ones) - 1
	if element != expected {
		return 0, fmt.Errorf("LogicalImm: not a single ones-run")
	}

	var n uint32
	var immr uint32 = uint32(rotation)
	var imms uint32

	if size == 64 {
		n = 1
		imms = uint32(ones - 1)
	} else {
		n = 0
		// nimms: top (6 - log2(size)) bits ones, bottom log2(size) bits = ones-1.
		sizeLog2 := 0
		for s := size; s > 1; s >>= 1 {
			sizeLog2++
		}
		nimmsTop := (uint32(0x3F) << sizeLog2) & 0x3F
		imms = nimmsTop | uint32(ones-1)
	}

	combined := (n << 12) | (immr << 6) | imms
	return combined << slot.BitPosition, nil
}
