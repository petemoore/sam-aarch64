// Trinity-HW provenance (i273): this model is DERIVED FROM the SAM/Colin
// authority recorded in tools/trinity-authority-ledger.txt (the Z80 reader
// src/netboot/trinity_identity_stamp.asm — trinity_read_stamp — is the byte-layout
// contract both sides share); it is never itself the authority for real Trinity
// hardware behaviour (CLAUDE.md rule 8 / feedback_port_diff_authority_first).
//
// Package trinityfw is the host-side MODEL of the i213 Trinity firmware
// IDENTITY STAMP — a small magic-signature + version marker written into a named
// Trinity EEPROM chunk when we customise the firmware (the i135c bootblock flash),
// so our software can DETECT whether our patched firmware is the one actually
// running and handle a mismatch gracefully.
//
// Why it matters (Pete, 2026-06-23): the B-DOS in control is NOT guaranteed to be
// the one we flashed — B-DOS can be loaded from FLOPPY (the traditional path),
// which is the normal case for anyone without our customised ROM chip. So our
// software cannot assume the EEPROM's patched firmware is present; the stamp lets
// it detect which firmware/patch is running. This is NOT capability negotiation
// (record access through the EEPROM B-DOS is transparent — i214 WONTFIX); it is
// just an identity marker.
//
// It encodes the stamp into the 1 KB chunk bytes that would be flashed and decodes
// those bytes back. The matching Z80 reader is src/netboot/trinity_identity_stamp.asm
// (trinity_read_stamp); the format below is the single byte-layout contract both
// sides share — see the registry item i213.
//
// SCOPE: host-side format model + read helper only. The flash of the chunk to a
// real EEPROM is the inherently-hardware i135c path (the Trinity write_chunk
// routine, in the private bootloader fork) and is out of scope here. The emulation
// round-trip test (tools/netboot-oracle/z80/trinity_identity_stamp_test.go)
// programs the encoded bytes into the harness EEPROM and asserts trinity_read_stamp
// decodes them back. Emulation-verified is not hardware-verified (CLAUDE.md §5).
package trinityfw

import (
	"bytes"
	"fmt"
)

// ChunkName is the 16-byte Trinity EEPROM chunk name the firmware stamp lives in,
// found by name via eeprom.asm find_index (like "Trinity Network " / "SAMBOOT
// Config  "). "Trinity Firmware" is exactly 16 bytes, so no padding is needed.
const ChunkName = "Trinity Firmware"

// ChunkBytes is the size of an EEPROM data chunk (the stamp occupies the first few
// bytes; the rest is reserved and zero).
const ChunkBytes = 1024

// Magic is the 4-byte signature at the start of the stamp payload — belt-and-braces
// over the unique chunk name, so a chance chunk with the same name but unrelated
// content is not mistaken for our stamp. ASCII "SAMB" (SAM Boot), hexdump-readable.
var Magic = [4]byte{'S', 'A', 'M', 'B'}

// Version is the current stamp format / firmware-patch version (chunk+4). Bump it
// when the patched firmware changes in a way our software must distinguish.
const Version = 0x01

// Payload byte offsets into the chunk's 1 KB data. Mirrored exactly by
// src/netboot/trinity_identity_stamp.asm.
const (
	offMagic   = 0 // 4-byte signature (offMagic..offMagic+3)
	offVersion = 4 // firmware/patch version byte
	payloadLen = 5 // magic(4) + version(1); the rest of the chunk is reserved zero
)

// Encode returns the 1 KB chunk bytes for a stamp of the given version: the 4-byte
// magic, the version, and the rest reserved and zero. These are the exact bytes the
// flash would write and that trinity_read_stamp decodes. A version of 0 is invalid
// (the reader treats 0 as "not our firmware"); callers should pass >= 1.
func Encode(version uint8) []byte {
	buf := make([]byte, ChunkBytes)
	copy(buf[offMagic:], Magic[:])
	buf[offVersion] = version
	return buf
}

// Detect decodes the chunk bytes a reader would find. ours is true when the magic
// matches AND the version is non-zero — i.e. our patched firmware is present;
// version is then the firmware-patch version. ours is false (version 0) when the
// magic mismatches or the version byte is 0 — the chunk is not our stamp (a
// floppy-loaded / stock B-DOS, or an unrelated chunk). This mirrors
// trinity_read_stamp's A/CY contract exactly.
func Detect(chunk []byte) (version uint8, ours bool, err error) {
	if len(chunk) < payloadLen {
		return 0, false, fmt.Errorf("trinityfw: chunk too short (%d bytes; need at least %d)", len(chunk), payloadLen)
	}
	if !bytes.Equal(chunk[offMagic:offMagic+4], Magic[:]) {
		return 0, false, nil
	}
	v := chunk[offVersion]
	if v == 0 {
		return 0, false, nil
	}
	return v, true, nil
}
