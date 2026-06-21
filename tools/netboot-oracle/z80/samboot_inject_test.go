// samboot_inject_test.go — i135d (also completes i112): host-verification of the
// patched-bootblock injected segment (samboot_inject), the decision+dispatch glue
// the SAMBOOT flash adds at samboot-bootblock-analysis.md §3's TODO hook.
//
// samboot_inject (src/netboot/samboot_inject.asm) chains two already-ported,
// individually harness-tested primitives:
//   1. samboot_stripes — redraw the MGT opening stripes UNCONDITIONALLY (i112).
//      The pixels are hardware-gated (the bdos_picker picker_render precedent); a
//      probe counter (SAMBOOT_STRIPES_CALLS) records that it was CALLED.
//   2. samboot_read_config — i176: read the SAMBOOT BIOS config from the EEPROM.
//   3. on auto-boot, bdos_boot_record — i122a: HRECORD-select + ALHK-boot the
//      configured record; on no-auto-boot, RET (fall through to a normal boot).
//
// The binary is built WITHOUT NETBOOT_HOSTTEST (like netboot_client_boot), so the
// real RST 8 ALHK dispatch is present and AttachBDOS captures the boot — exactly
// the bdos_boot_test.go pattern. The EEPROM read runs against the real find_index
// + read_chunk via the emulated Trinity ports (ENC28J60.ProgramNamedChunk), the
// samboot_config_test.go pattern. So one test exercises both seams end to end.
//
// THE HONESTY LINE (CLAUDE.md §5): this asserts the decision+dispatch control flow
// and the UNCONDITIONAL stripes redraw (the probe == 1 in EVERY branch). The
// stripes PIXELS, the real reset->ROM->bootblock chain, and the ALHK auto-load are
// hardware-first (i135c) and not modelled here. Emulation-verified is not
// hardware-verified.
package z80_test

import (
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/samboot"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	sambootInjectBinPath = "../../../build/samboot_inject.bin"
	sambootInjectMapPath = "../../../build/samboot_inject.map"

	// The chunk slot ProgramNamedChunk places the config at; the reader finds it
	// by name, not number, so any 1-based slot works (mirrors the i176 test).
	sambootInjectChunkValue = 5
)

// injectResult is what running samboot_inject yields the host: which record (if
// any) the harness captured an ALHK boot against, the HRECORD-selected record,
// and how many times samboot_stripes was called.
type injectResult struct {
	boots        []int
	selected     int
	stripesCalls byte
}

// runInject loads samboot_inject, attaches the EEPROM (config read) + the BDOS
// store (RST 8 boot capture), optionally programs the config chunk, calls
// samboot_inject, and returns what was captured. programChunk==nil means "leave
// the SAMBOOT Config chunk absent" (the find_index-miss case).
func runInject(t *testing.T, programChunk []byte) injectResult {
	t.Helper()
	if _, err := os.Stat(sambootInjectBinPath); err != nil {
		t.Skipf("samboot_inject binary not built (%s); run `make netboot-samboot-inject`", sambootInjectBinPath)
	}
	mac, err := z80h.Load(sambootInjectBinPath, sambootInjectMapPath)
	if err != nil {
		t.Fatalf("load samboot_inject: %v", err)
	}

	// EEPROM model serves the config read (find_index + read_chunk via the Trinity
	// ports); BDOS store intercepts the RST 8 HRECORD select + ALHK boot.
	enc := z80h.NewENC28J60()
	mac.AttachIO(enc)
	store := z80h.NewBDOSStore()
	mac.AttachBDOS(store)

	if programChunk != nil {
		if len(samboot.ChunkName) != 16 {
			t.Fatalf("samboot.ChunkName %q is %d bytes, want 16", samboot.ChunkName, len(samboot.ChunkName))
		}
		enc.ProgramNamedChunk(sambootInjectChunkValue, samboot.ChunkName, programChunk)
	}

	if _, err := mac.CallEntry("samboot_inject", z80h.Entry{}); err != nil {
		t.Fatalf("call samboot_inject: %v", err)
	}

	calls := mac.Read(symAddr(t, mac, "SAMBOOT_STRIPES_CALLS"), 1)[0]
	return injectResult{boots: store.Boots(), selected: store.Selected(), stripesCalls: calls}
}

// TestSambootInjectAutoBoot is the headline case: an auto-boot config for record 7
// drives samboot_inject to HRECORD-select + ALHK-boot record 7, and the stripes
// redraw ran exactly once.
func TestSambootInjectAutoBoot(t *testing.T) {
	const rec = 7
	got := runInject(t, samboot.Boot(rec).Encode())

	if got.selected != rec {
		t.Errorf("Selected() = %d, want %d (HRECORD inside bdos_boot_record)", got.selected, rec)
	}
	if len(got.boots) != 1 || got.boots[0] != rec {
		t.Errorf("Boots() = %v, want [%d] (one ALHK fire against the configured record)", got.boots, rec)
	}
	if got.stripesCalls != 1 {
		t.Errorf("stripes probe = %d, want 1 (unconditional redraw, called once)", got.stripesCalls)
	}
}

// TestSambootInjectSecondRecord confirms a different record number (0x12) is read
// from the config and booted — the dispatch is not hard-wired to record 7.
func TestSambootInjectSecondRecord(t *testing.T) {
	const rec = 0x12
	got := runInject(t, samboot.Boot(rec).Encode())

	if len(got.boots) != 1 || got.boots[0] != rec {
		t.Errorf("Boots() = %v, want [%d]", got.boots, rec)
	}
	if got.stripesCalls != 1 {
		t.Errorf("stripes probe = %d, want 1", got.stripesCalls)
	}
}

// TestSambootInjectNoneMode confirms a mode=none config (BIOS set to wait for the
// user) falls through to a normal boot: NO boot is captured, and the stripes
// redraw STILL ran exactly once — proving the redraw is unconditional, not gated
// on auto-boot.
func TestSambootInjectNoneMode(t *testing.T) {
	got := runInject(t, samboot.None().Encode())

	if len(got.boots) != 0 {
		t.Errorf("Boots() = %v, want empty (mode=none falls through, no auto-boot)", got.boots)
	}
	if got.selected != -1 {
		t.Errorf("Selected() = %d, want -1 (no HRECORD select on the fall-through path)", got.selected)
	}
	if got.stripesCalls != 1 {
		t.Errorf("stripes probe = %d, want 1 (redraw runs even when not auto-booting)", got.stripesCalls)
	}
}

// TestSambootInjectAbsentChunk is the find_index-miss case: with no "SAMBOOT
// Config  " chunk programmed, samboot_read_config returns no-auto-boot, so
// samboot_inject falls through to a normal boot — no boot captured, stripes still
// redrawn once.
func TestSambootInjectAbsentChunk(t *testing.T) {
	got := runInject(t, nil /* leave the config chunk absent */)

	if len(got.boots) != 0 {
		t.Errorf("Boots() = %v, want empty (absent config -> fall through)", got.boots)
	}
	if got.stripesCalls != 1 {
		t.Errorf("stripes probe = %d, want 1 (redraw runs even with no config)", got.stripesCalls)
	}
}

// TestSambootInjectBadVersion confirms an unrecognised config version is treated
// as no-auto-boot even when the mode byte says auto-boot: samboot_inject must not
// act on a config format it does not understand. The stripes redraw still ran.
func TestSambootInjectBadVersion(t *testing.T) {
	data := samboot.Boot(9).Encode()
	data[0] = 0xFF // corrupt the version byte (chunk+0)
	got := runInject(t, data)

	if len(got.boots) != 0 {
		t.Errorf("Boots() = %v, want empty (bad version -> no auto-boot)", got.boots)
	}
	if got.stripesCalls != 1 {
		t.Errorf("stripes probe = %d, want 1", got.stripesCalls)
	}
}
