// trinity_identity_stamp_test.go — the i213 host-verification of the Trinity
// firmware identity-stamp reader. It loads the reader host-test binary
// (src/netboot/trinity_identity_stamp.asm, built -D NETBOOT_HOSTTEST=1), programs
// an encoded stamp into the emulated Trinity flash under the "Trinity Firmware"
// name (ProgramNamedChunk), runs the real trinity_read_stamp against eeprom.asm's
// find_index + read_chunk, and asserts the A-register decision (firmware version,
// or 0 for "not our firmware") matches what the host authority (trinityfw) encoded
// — a full Go-encode -> EEPROM -> Z80-decode round-trip.
//
// The flash-to-hardware WRITE is NOT modelled (the harness EEPROM serves reads
// only); writing the stamp rides the i135c bootblock flash (private fork).
// Emulation-verified is not hardware-verified (CLAUDE.md §5).
package z80_test

import (
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/trinityfw"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	trinityStampBinPath = "../../../build/trinity_identity_stamp.bin"
	trinityStampMapPath = "../../../build/trinity_identity_stamp.map"

	// The chunk number ProgramNamedChunk places the stamp at. find_index returns
	// this value; read_chunk reads its bytes. Any 1-based slot works (the reader
	// finds it by name, not number); 6 stands in for "wherever it lives on a real
	// card".
	trinityStampChunkValue = 6
)

func loadTrinityStamp(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(trinityStampBinPath); err != nil {
		t.Fatalf("trinity_identity_stamp binary not built (%s); run `make netboot-trinity-identity`", trinityStampBinPath)
	}
	mac, err := z80h.Load(trinityStampBinPath, trinityStampMapPath)
	if err != nil {
		t.Fatalf("load trinity_identity_stamp: %v", err)
	}
	return mac
}

// readStamp programs chunk bytes into the emulated EEPROM under the "Trinity
// Firmware" name (unless chunk is nil, the absent-chunk case), runs
// trinity_read_stamp, and returns the reader's A: the firmware version when our
// stamp is present, or 0 when not.
func readStamp(t *testing.T, chunk []byte) uint8 {
	t.Helper()
	mac := loadTrinityStamp(t)
	enc := z80h.NewENC28J60()
	mac.AttachIO(enc)
	if chunk != nil {
		// Sanity: the name the Go authority and the Z80 reader share is exactly 16
		// bytes — a mismatch would make find_index miss on hardware.
		if len(trinityfw.ChunkName) != 16 {
			t.Fatalf("trinityfw.ChunkName %q is %d bytes, want 16", trinityfw.ChunkName, len(trinityfw.ChunkName))
		}
		enc.ProgramNamedChunk(trinityStampChunkValue, trinityfw.ChunkName, chunk)
	}
	res, err := mac.Call("trinity_read_stamp")
	if err != nil {
		t.Fatalf("call trinity_read_stamp: %v", err)
	}
	if !res.Halted {
		t.Fatalf("trinity_read_stamp did not return (PC=&%04X)", res.PC)
	}
	return res.A
}

// TestTrinityStampPresent is the headline i213 host check: a Go-encoded stamp,
// programmed into the EEPROM under the "Trinity Firmware" name, decodes back
// through trinity_read_stamp to "our firmware present" with the encoded version.
func TestTrinityStampPresent(t *testing.T) {
	if got := readStamp(t, trinityfw.Encode(trinityfw.Version)); got != trinityfw.Version {
		t.Fatalf("present stamp decoded to A=%d, want the version %d (our firmware)", got, trinityfw.Version)
	}
}

// TestTrinityStampVersionPassThrough confirms the reader returns the ACTUAL
// version byte, not a fixed 1 — so callers can distinguish firmware revisions
// (and a future version the reader was built before still reads as "ours, vN").
func TestTrinityStampVersionPassThrough(t *testing.T) {
	const v = 0x2A // 42 — a version the build's TRINITY_STAMP_VERSION constant is not
	if got := readStamp(t, trinityfw.Encode(v)); got != v {
		t.Fatalf("stamp version %d decoded to A=%d, want %d (version passed through)", v, got, v)
	}
}

// TestTrinityStampAbsent is the find_index-miss case: with no "Trinity Firmware"
// chunk programmed, trinity_read_stamp returns A=0 ("not our firmware" — a stock /
// floppy-loaded B-DOS), not a spurious detection.
func TestTrinityStampAbsent(t *testing.T) {
	if got := readStamp(t, nil); got != 0 {
		t.Fatalf("absent stamp chunk decoded to A=%d, want 0 (not our firmware)", got)
	}
}

// TestTrinityStampMagicMismatch confirms a chunk found under the name but carrying
// the wrong magic is rejected (A=0) — the magic is the belt-and-braces over a
// chance same-named chunk.
func TestTrinityStampMagicMismatch(t *testing.T) {
	bad := trinityfw.Encode(trinityfw.Version)
	bad[0] ^= 0xFF // corrupt the first magic byte
	if got := readStamp(t, bad); got != 0 {
		t.Fatalf("magic-mismatch stamp decoded to A=%d, want 0 (not our firmware)", got)
	}
}

// TestTrinityStampZeroVersion confirms a stamp with the right magic but a zero
// version byte is treated as "not our firmware" (a malformed / reserved marker),
// matching the reader's `version != 0` gate.
func TestTrinityStampZeroVersion(t *testing.T) {
	z := trinityfw.Encode(0) // valid magic, version 0
	if got := readStamp(t, z); got != 0 {
		t.Fatalf("zero-version stamp decoded to A=%d, want 0 (not our firmware)", got)
	}
}
