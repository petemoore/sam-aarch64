// splice.go — produce the combined patched-bootblock chunk-1 image (i229).
//
// The SAMBOOT no-brick patch is a 3-byte splice into Colin Piggot's Trinity
// bootblock (chunk 1, the first 1 KB of the EEPROM, run at ORG &4000): the
// original `CALL &805F` at file offset &9E (bytes CD 5F 80) becomes `CALL inject`,
// where `inject` is our routine assembled into the bootblock's free space at file
// offset &15E (logical &415E). Splice copies the captured bootblock, patches those
// 3 bytes, and lays the assembled `inject` blob into the free space — yielding the
// exact 1 KB chunk-1 that would be flashed (the i230 RAM test / i135c flash).
//
// The captured bootblock is Colin's PROPRIETARY EEPROM image and is NEVER committed
// — Splice operates on bytes the caller supplies (the CLI reads the local capture;
// the reset-chain test passes eeprom[0:1024]) and the CLI writes only to a
// git-ignored path. This package never embeds capture bytes.
package samboot

import "fmt"

const (
	// ChunkSize is chunk 1's size: the first 1 KB of the EEPROM device, run at
	// ORG &4000. The combined image is exactly this many bytes.
	ChunkSize = 1024

	// SpliceOffset is the file offset of the `CALL &805F` the splice patches
	// (logical &409E; verified against the local capture, 2026-06-25).
	SpliceOffset = 0x9E

	// InjectFileOffset is the file offset of the bootblock free space `inject` is
	// assembled into (logical &415E). The original bootblock's final RET is at file
	// &15D; &15E..&3FF (674 bytes) are all zero.
	InjectFileOffset = 0x15E

	// InjectLogical is the logical address `inject` runs at (&415E). The patched
	// CALL targets this address.
	InjectLogical = 0x415E

	// FreeSpace is the number of zero bytes available for `inject` (&15E..&3FF).
	FreeSpace = ChunkSize - InjectFileOffset // 674
)

// callOpcode is the Z80 CALL nn opcode; the original splice site holds CALL &805F.
const callOpcode = 0xCD

// origCallTarget is the original CALL target at the splice site (&805F = the real
// B-DOS init). The splice asserts the site still holds CALL &805F before patching,
// so a changed capture/layout is caught loudly rather than silently mis-patched.
const origCallTarget = 0x805F

// Splice returns the combined 1 KB chunk-1 image: a copy of the captured bootblock
// (chunk1, exactly 1024 bytes) with the 3 bytes at SpliceOffset changed from
// `CALL &805F` to `CALL inject`, and the assembled inject blob copied into the free
// space at InjectFileOffset. It errors (never guesses) if:
//   - chunk1 is not exactly ChunkSize bytes,
//   - inject is larger than FreeSpace (it would overrun chunk 1), or
//   - the splice site does not hold the expected `CALL &805F` (the capture/layout
//     changed — a real bug to surface, not paper over).
func Splice(chunk1, inject []byte) ([]byte, error) {
	if len(chunk1) != ChunkSize {
		return nil, fmt.Errorf("samboot splice: chunk1 is %d bytes, want exactly %d (the bootblock = chunk 1 = the first 1 KB of the EEPROM)", len(chunk1), ChunkSize)
	}
	if len(inject) > FreeSpace {
		return nil, fmt.Errorf("samboot splice: inject is %d bytes, but only %d free bytes (&15E..&3FF) — gate more out under SAMBOOT_BOOTBLOCK", len(inject), FreeSpace)
	}

	// Assert the splice site still holds CALL &805F (CD 5F 80) before patching.
	wantLo, wantHi := byte(origCallTarget&0xFF), byte(origCallTarget>>8)
	if chunk1[SpliceOffset] != callOpcode || chunk1[SpliceOffset+1] != wantLo || chunk1[SpliceOffset+2] != wantHi {
		return nil, fmt.Errorf("samboot splice: site &409E = %02X %02X %02X, want %02X %02X %02X (CALL &805F) — capture/layout changed, refusing to guess",
			chunk1[SpliceOffset], chunk1[SpliceOffset+1], chunk1[SpliceOffset+2], callOpcode, wantLo, wantHi)
	}

	out := make([]byte, ChunkSize)
	copy(out, chunk1)

	// Patch the 3 bytes: CALL inject (CD <lo> <hi>), inject = &415E.
	out[SpliceOffset] = callOpcode
	out[SpliceOffset+1] = byte(InjectLogical & 0xFF)
	out[SpliceOffset+2] = byte(InjectLogical >> 8)

	// Copy the assembled inject blob into the free space.
	copy(out[InjectFileOffset:], inject)

	return out, nil
}
