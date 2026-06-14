package z80_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// TestZZStartELF streams the real ~/tftpboot/start.elf (2,979,296 bytes) through
// the Z80 sha256_init/update(32KB)/final API and asserts the digest equals the
// known dd9b4204...ec84 — the firmware-download verify path end to end. This is
// the final real-data confirmation; it emulates millions of Z80 instructions so
// it takes a few minutes (skipped under -short).
func TestZZStartELF(t *testing.T) {
	if testing.Short() {
		t.Skip("start.elf full-file emulation is slow; -short")
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, "tftpboot", "start.elf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("start.elf not present (%v)", err)
	}
	const want = "dd9b42041b566d8b94529a6eda68ded147fd18c6a4b5d6b9743226082114ec84"

	mac := loadSHA256Machine(t)
	if _, err := mac.Call("sha256_init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	// The harness is a 64KB flat space; staging sits at 0x9000, so a chunk must
	// fit below 0x10000. 16KB is well clear and exercises the multi-block +
	// partial-block streaming carry the same as any chunk size.
	const chunk = 16 * 1024
	for off := 0; off < len(data); off += chunk {
		n := chunk
		if off+n > len(data) {
			n = len(data) - off
		}
		mac.Write(shaInputStage, data[off:off+n])
		// A 16KB update runs 256 compressions (~16M Z80 steps), well past the
		// harness's default 5M runaway cap — lift it for these large updates.
		if _, err := mac.CallEntry("sha256_update", z80h.Entry{HL: shaInputStage, BC: uint16(n), StepCap: 100_000_000}); err != nil {
			t.Fatalf("update(off=%d,len=%d): %v", off, n, err)
		}
	}
	if _, err := mac.CallEntry("sha256_final", z80h.Entry{HL: shaOutputStage, StepCap: 100_000_000}); err != nil {
		t.Fatalf("final: %v", err)
	}
	got := hex.EncodeToString(mac.Read(shaOutputStage, 32))
	if got != want {
		t.Fatalf("start.elf digest mismatch\n got %s\nwant %s", got, want)
	}
	t.Logf("start.elf (%d bytes) digest = %s  ✓", len(data), got)
}
