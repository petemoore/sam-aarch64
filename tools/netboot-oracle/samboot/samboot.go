// Package samboot is the host-side authority for the SAMBOOT BIOS config — the
// persisted, editable default-boot-record setting the patched bootblock (i135d)
// reads from a named Trinity EEPROM chunk to decide whether to auto-boot a chosen
// record at power-on, or fall through to a normal boot.
//
// It encodes a config into the 1 KB chunk bytes that would be flashed (the "BIOS
// setup" editor produces these; cmd/samboot-config is the CLI) and decodes those
// bytes back to a human-readable config. The matching Z80 reader is
// src/netboot/samboot_config.asm (samboot_read_config); the format below is the
// single byte-layout contract both sides share — see docs/specs/samboot.md §4.
//
// SCOPE: this is the host editor + format authority only. The flash of the chunk
// to a real EEPROM is the inherently-hardware i135c path (via the Trinity
// write_chunk routine) and is out of scope here. The emulation round-trip test
// (tools/netboot-oracle/z80/samboot_config_test.go) programs the encoded bytes
// into the harness EEPROM and asserts samboot_read_config decodes them back.
package samboot

import (
	"encoding/binary"
	"fmt"
)

// ChunkName is the 16-byte Trinity EEPROM chunk name the BIOS config lives in,
// found by name via eeprom.asm find_index (like "Trinity Network "). It is
// space-padded to exactly 16 bytes: "SAMBOOT Config" (14) + two trailing spaces.
// (A single trailing space would be only 15 bytes.)
const ChunkName = "SAMBOOT Config  "

// ChunkBytes is the size of an EEPROM data chunk (the config payload occupies the
// first few bytes; the rest is reserved and zero).
const ChunkBytes = 1024

// Payload format (byte offsets into the chunk's 1 KB data). Mirrored exactly by
// src/netboot/samboot_config.asm.
const (
	offVersion = 0 // format version byte
	offMode    = 1 // mode byte
	offRecord  = 2 // record number, 16-bit little-endian (offRecord..offRecord+1)
)

// Version is the current config format version (chunk+0).
const Version = 0x01

// Boot modes (chunk+1).
const (
	ModeNone = 0x00 // no auto-boot: wait for the user (normal boot)
	ModeBoot = 0x01 // auto-boot the record at chunk+2..3
)

// Config is the decoded SAMBOOT BIOS config.
type Config struct {
	// AutoBoot is true when the BIOS should auto-boot Record at power-on, false
	// for a normal boot (wait for the user). It maps to the mode byte: true =
	// ModeBoot, false = ModeNone.
	AutoBoot bool
	// Record is the Trinity record number to auto-boot when AutoBoot is true.
	// Ignored when AutoBoot is false.
	Record uint16
}

// None is the config that disables auto-boot (a normal boot).
func None() Config { return Config{AutoBoot: false} }

// Boot is the config that auto-boots record n at power-on.
func Boot(n uint16) Config { return Config{AutoBoot: true, Record: n} }

// Encode returns the 1 KB chunk bytes for c: version, mode, and (when auto-boot)
// the little-endian record number, with the rest reserved and zero. These are the
// exact bytes the host editor would flash and that samboot_read_config decodes.
func (c Config) Encode() []byte {
	buf := make([]byte, ChunkBytes)
	buf[offVersion] = Version
	if c.AutoBoot {
		buf[offMode] = ModeBoot
		binary.LittleEndian.PutUint16(buf[offRecord:], c.Record)
	} else {
		buf[offMode] = ModeNone
		// Record bytes stay zero; reserved bytes stay zero.
	}
	return buf
}

// Decode parses chunk bytes back to a Config, applying the same reader semantics
// as samboot_read_config (docs/specs/samboot.md §4): a chunk with an unrecognised
// version, or mode != ModeBoot, decodes to "no auto-boot" (a normal boot). Only
// version 1 with mode 1 yields auto-boot of the encoded record. It errors only on
// a chunk that is too short to hold the fixed header.
func Decode(chunk []byte) (Config, error) {
	if len(chunk) < offRecord+2 {
		return Config{}, fmt.Errorf("samboot: chunk too short (%d bytes; need at least %d)", len(chunk), offRecord+2)
	}
	if chunk[offVersion] != Version {
		// Unrecognised version: treat as no auto-boot, like the Z80 reader.
		return None(), nil
	}
	if chunk[offMode] != ModeBoot {
		return None(), nil
	}
	return Boot(binary.LittleEndian.Uint16(chunk[offRecord:])), nil
}

// String renders a config for the editor's decode output.
func (c Config) String() string {
	if !c.AutoBoot {
		return "mode=none (normal boot, wait for user)"
	}
	return fmt.Sprintf("mode=auto-boot record=%d", c.Record)
}
