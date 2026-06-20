// samboot_config_test.go — the i176 host-verification of the SAMBOOT BIOS config
// reader. It loads the reader host-test binary (src/netboot/samboot_config.asm,
// built -D NETBOOT_HOSTTEST=1), programs the encoded config into the emulated
// Trinity flash under the "SAMBOOT Config  " name (ProgramNamedChunk), runs the
// real samboot_read_config against eeprom.asm's find_index + read_chunk, and
// asserts the decision (A/HL register contract) matches what the host encoder put
// in — a full Go-encode -> EEPROM -> Z80-decode round-trip.
//
// The Go authority is the samboot package (samboot.Config.Encode): the bytes it
// produces are exactly what the reader must decode, so the assertion is that the
// Z80 reader's A/HL out matches the encoded AutoBoot/Record. The flash-to-hardware
// write is NOT modelled (the harness EEPROM serves reads only); writing is the
// i135c hardware path. Emulation-verified is not hardware-verified (CLAUDE.md §5).
package z80_test

import (
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/samboot"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	sambootCfgBinPath = "../../../build/samboot_config.bin"
	sambootCfgMapPath = "../../../build/samboot_config.map"

	// The chunk number ProgramNamedChunk places the config at. find_index returns
	// this value; read_chunk reads its bytes. Any 1-based slot works (the reader
	// finds it by name, not number); 5 stands in for "wherever it lives on a real
	// card".
	sambootCfgChunkValue = 5
)

func loadSambootCfg(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(sambootCfgBinPath); err != nil {
		t.Skipf("samboot_config binary not built (%s); run `make netboot-samboot-config`", sambootCfgBinPath)
	}
	mac, err := z80h.Load(sambootCfgBinPath, sambootCfgMapPath)
	if err != nil {
		t.Fatalf("load samboot_config: %v", err)
	}
	return mac
}

// readConfig programs cfg's encoded bytes into the emulated EEPROM under the
// SAMBOOT config name (unless skipProgram, the absent-chunk case), runs
// samboot_read_config, and returns the reader's decision: autoBoot (A==1) and the
// record number (HL).
func readConfig(t *testing.T, cfg samboot.Config, skipProgram bool) (autoBoot bool, record uint16) {
	t.Helper()
	mac := loadSambootCfg(t)
	enc := z80h.NewENC28J60()
	mac.AttachIO(enc)
	if !skipProgram {
		// Sanity: the name the Go authority and the Z80 reader share is exactly 16
		// bytes — a mismatch would make find_index miss on hardware.
		if len(samboot.ChunkName) != 16 {
			t.Fatalf("samboot.ChunkName %q is %d bytes, want 16", samboot.ChunkName, len(samboot.ChunkName))
		}
		enc.ProgramNamedChunk(sambootCfgChunkValue, samboot.ChunkName, cfg.Encode())
	}

	res, err := mac.Call("samboot_read_config")
	if err != nil {
		t.Fatalf("call samboot_read_config: %v", err)
	}
	if !res.Halted {
		t.Fatalf("samboot_read_config did not return (PC=&%04X)", res.PC)
	}
	switch res.A {
	case 0:
		return false, 0
	case 1:
		return true, res.HL
	default:
		t.Fatalf("samboot_read_config returned A=%d, want 0 or 1", res.A)
		return false, 0
	}
}

// TestSambootConfigAutoBootRoundTrip is the headline i176 host check: a Go-encoded
// auto-boot config for a record, programmed into the EEPROM under the SAMBOOT
// name, decodes back through samboot_read_config to mode=auto-boot with the same
// record number.
func TestSambootConfigAutoBootRoundTrip(t *testing.T) {
	const rec = 7
	autoBoot, record := readConfig(t, samboot.Boot(rec), false)
	if !autoBoot {
		t.Fatalf("auto-boot config decoded to A=0 (no auto-boot), want auto-boot")
	}
	if record != rec {
		t.Fatalf("auto-boot record = %d, want %d", record, rec)
	}
}

// TestSambootConfigSecondRecord confirms the record number is read correctly for a
// different, multi-byte value (exercises the little-endian high byte at chunk+3).
func TestSambootConfigSecondRecord(t *testing.T) {
	const rec = 0x0123 // 291: low byte 0x23, high byte 0x01
	autoBoot, record := readConfig(t, samboot.Boot(rec), false)
	if !autoBoot {
		t.Fatalf("auto-boot config (record %d) decoded to no auto-boot", rec)
	}
	if record != rec {
		t.Fatalf("auto-boot record = %d (0x%04x), want %d (0x%04x)", record, record, rec, rec)
	}
}

// TestSambootConfigNoneMode confirms a mode=none config (auto-boot disabled)
// decodes to "no auto-boot" — the chunk is present but the BIOS is set to wait for
// the user.
func TestSambootConfigNoneMode(t *testing.T) {
	autoBoot, _ := readConfig(t, samboot.None(), false)
	if autoBoot {
		t.Fatalf("mode=none config decoded to auto-boot, want no auto-boot")
	}
}

// TestSambootConfigAbsentChunk is the find_index-miss case: when no "SAMBOOT
// Config  " chunk is programmed at all, samboot_read_config returns "no auto-boot"
// (fall through to a normal boot), not a spurious auto-boot.
func TestSambootConfigAbsentChunk(t *testing.T) {
	autoBoot, _ := readConfig(t, samboot.Config{}, true /* skipProgram: no chunk */)
	if autoBoot {
		t.Fatalf("absent config chunk decoded to auto-boot, want no auto-boot")
	}
}

// TestSambootConfigBadVersion confirms an unrecognised version byte is treated as
// "no auto-boot" even when the mode byte says auto-boot — the reader must not act
// on a config format it does not understand.
func TestSambootConfigBadVersion(t *testing.T) {
	mac := loadSambootCfg(t)
	enc := z80h.NewENC28J60()
	mac.AttachIO(enc)

	// Encode a valid auto-boot config, then corrupt the version byte (chunk+0).
	data := samboot.Boot(9).Encode()
	data[0] = 0xFF // unrecognised version
	enc.ProgramNamedChunk(sambootCfgChunkValue, samboot.ChunkName, data)

	res, err := mac.Call("samboot_read_config")
	if err != nil {
		t.Fatalf("call samboot_read_config: %v", err)
	}
	if res.A != 0 {
		t.Fatalf("bad-version config returned A=%d, want 0 (no auto-boot)", res.A)
	}
}

// TestSambootConfigGoRoundTrip is a pure-Go sanity check on the encoder/decoder
// pair (no Z80): every config round-trips through Encode -> Decode unchanged, and
// the encoded bytes match the documented fixed layout. This pins the host-side
// authority the Z80 reader is asserted against.
func TestSambootConfigGoRoundTrip(t *testing.T) {
	cases := []samboot.Config{samboot.None(), samboot.Boot(0), samboot.Boot(1), samboot.Boot(0xBEEF)}
	for _, c := range cases {
		enc := c.Encode()
		if len(enc) != samboot.ChunkBytes {
			t.Fatalf("Encode(%v) len = %d, want %d", c, len(enc), samboot.ChunkBytes)
		}
		got, err := samboot.Decode(enc)
		if err != nil {
			t.Fatalf("Decode(Encode(%v)): %v", c, err)
		}
		if got != c {
			t.Fatalf("round-trip %v -> %v", c, got)
		}
	}

	// Byte-layout assertions for an auto-boot record (little-endian record).
	enc := samboot.Boot(0x0102).Encode()
	if enc[0] != samboot.Version || enc[1] != samboot.ModeBoot || enc[2] != 0x02 || enc[3] != 0x01 {
		t.Fatalf("auto-boot layout = %x, want version=01 mode=01 rec=02 01", enc[:4])
	}
	for i := 4; i < len(enc); i++ {
		if enc[i] != 0 {
			t.Fatalf("reserved byte %d = 0x%02x, want 0", i, enc[i])
		}
	}
	// And the none config: version set, mode=none, record bytes zero.
	non := samboot.None().Encode()
	if non[0] != samboot.Version || non[1] != samboot.ModeNone || non[2] != 0 || non[3] != 0 {
		t.Fatalf("none layout = %x, want version=01 mode=00 rec=00 00", non[:4])
	}
}
