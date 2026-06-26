//go:build trinityboot

// trinity_autoboot_verify_test.go — LOCAL cross-repo verification (NOT in CI; the
// `trinityboot` build tag excludes it). Boots the patched bootloader built in the
// sibling repo ~/git/trinity-autoboot against the real captured ROM + B-DOS.
//
//	Run: cd ~/git/trinity-autoboot && make
//	     cd ~/git/sam-aarch64/tools/netboot-oracle && go test ./z80/ -tags trinityboot -run Trinity -v
//
// Addresses (re-derive from build/bootloader.map after any bootloader edit; they
// shift as the free-space code grows): decision &415E, config_decision &4166,
// boot_fallback &4171, no_autoboot &4179, build_stripes &4197, read_config &4213,
// bdos_boot_record &42FF. The i264 selector decision: ESC held at boot -> manual
// control (no_autoboot -> opening screen -> BASIC); else -> config-driven auto-boot,
// falling back to BOOT_RECORD (3) when no "SAMBOOT Config" chunk exists.
package z80_test

import (
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/samboot"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	trinityBootBin = "../../../../trinity-autoboot/build/bootloader.bin"
	trinityBootMap = "../../../../trinity-autoboot/build/bootloader.map"
)

func loadTrinityBoot(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(trinityBootBin)
	if err != nil {
		t.Fatalf("bootloader not built — run `cd ~/git/trinity-autoboot && make`: %v", err)
	}
	if len(b) != 1024 {
		t.Fatalf("bootloader.bin is %d bytes, want 1024", len(b))
	}
	return b
}

// trinityDevice: the device-linear EEPROM with chunk 1 = our bootloader.
func trinityDevice(t *testing.T, eeprom, boot []byte) []byte {
	t.Helper()
	dev := deviceLinearEEPROM(eeprom)
	copy(dev[0x2000:0x2400], boot)
	return dev
}

// TestTrinityNoConfigFallsBackToRecord3: with NO "SAMBOOT Config" chunk and ESC not
// held, the i264 decision falls back to BOOT_RECORD (3 = trinload) — preserving
// today's auto-boot until the picker writes a config. The full boot reaches
// decision -> config_decision -> read_config (miss) -> boot_fallback ->
// bdos_boot_record, and must NOT run the opening screen (build_stripes).
func TestTrinityNoConfigFallsBackToRecord3(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	boot := loadTrinityBoot(t)
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(trinityDevice(t, eeprom, boot)) // no config chunk
	mac.AttachIO(enc)
	// ESC not held: harness keyMatrix defaults to 0xFF (no key) -> the auto-boot path.

	hits := map[uint16]int{0x415E: 0, 0x4171: 0, 0x42FF: 0, 0x4197: 0} // decision, boot_fallback, bdos_boot_record, build_stripes
	mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: 6_000_000, FrameIntPeriod: 20000,
		Trace: func(pc uint16) {
			if _, ok := hits[pc]; ok {
				hits[pc]++
			}
		},
	})
	t.Logf("no-config: decision=%d boot_fallback=%d bdos_boot_record=%d build_stripes=%d",
		hits[0x415E], hits[0x4171], hits[0x42FF], hits[0x4197])
	if hits[0x415E] == 0 {
		t.Errorf("decision not reached — full boot did not reach our return-path code")
	}
	if hits[0x4171] == 0 {
		t.Errorf("boot_fallback not reached — no-config did not fall back to BOOT_RECORD")
	}
	if hits[0x42FF] == 0 {
		t.Errorf("bdos_boot_record not reached — fallback did not dispatch the record")
	}
	if hits[0x4197] != 0 {
		t.Errorf("build_stripes reached (%d) — no-config must auto-boot, not show the opening screen", hits[0x4197])
	}
}

// TestTrinityWithConfigReachesAutoboot: with a "SAMBOOT Config  " chunk for record
// N and ESC not held, the full boot reads it and reaches the auto-boot dispatch
// (bdos_boot_record), NOT the fallback. No BDOSStore (it would swallow the ROM's
// RST 8/DB 50 EEPROM fetch); reaching the dispatch proves the config was read and
// acted on. The ALHK endpoint is TestTrinityAutobootALHK.
func TestTrinityWithConfigReachesAutoboot(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	boot := loadTrinityBoot(t)
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(trinityDevice(t, eeprom, boot))
	enc.ProgramNamedChunk(20, samboot.ChunkName, samboot.Boot(7).Encode())
	mac.AttachIO(enc)

	hits := map[uint16]int{0x4213: 0, 0x42FF: 0, 0x4171: 0} // read_config, bdos_boot_record, boot_fallback
	mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: 6_000_000, FrameIntPeriod: 20000,
		Trace: func(pc uint16) {
			if _, ok := hits[pc]; ok {
				hits[pc]++
			}
		},
	})
	t.Logf("with-config: read_config=%d bdos_boot_record=%d boot_fallback=%d", hits[0x4213], hits[0x42FF], hits[0x4171])
	if hits[0x4213] == 0 {
		t.Errorf("read_config not reached in the full boot")
	}
	if hits[0x42FF] == 0 {
		t.Errorf("bdos_boot_record not reached — config not found/acted-on (fell back=%d)", hits[0x4171])
	}
	if hits[0x4171] != 0 {
		t.Errorf("boot_fallback reached (%d) — a present config must auto-boot directly, not fall back", hits[0x4171])
	}
}

// TestTrinityEscHeldManualControl: holding ESC at boot takes the manual-control path
// (no_autoboot -> opening screen) instead of auto-booting. Entering at `decision` (the
// CallEntry pattern that avoids the ROM-boot RST-8 conflict), with ESC pressed the
// decision must reach build_stripes (the opening screen) and must NOT reach
// bdos_boot_record. build_stripes is pure RAM (runs in the screenless core); we stop
// there, before draw_banner/READKEY (paged-ROM, hardware-only).
func TestTrinityEscHeldManualControl(t *testing.T) {
	if _, err := os.Stat(trinityBootBin); err != nil {
		t.Fatalf("bootloader not built: %v", err)
	}
	mac, err := z80h.LoadAt(trinityBootBin, trinityBootMap, 0x4000)
	if err != nil {
		t.Fatalf("load bootloader: %v", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramNamedChunk(5, samboot.ChunkName, samboot.Boot(3).Encode()) // config present, but ESC overrides it
	mac.AttachIO(enc)
	store := z80h.NewBDOSStore()
	mac.AttachBDOS(store)
	mac.PressEsc(true) // hold ESC at the decision

	const mBuildStripes, mBdosBoot = 0x4197, 0x42FF
	hits := map[uint16]int{mBuildStripes: 0, mBdosBoot: 0}
	if _, err := mac.CallEntry("decision", z80h.Entry{
		StepCap: 2_000_000, StopPC: mBuildStripes,
		Trace: func(pc uint16) {
			if _, ok := hits[pc]; ok {
				hits[pc]++
			}
		},
	}); err != nil {
		t.Fatalf("call decision: %v", err)
	}
	t.Logf("ESC-held: build_stripes=%d bdos_boot_record=%d Boots=%v", hits[mBuildStripes], hits[mBdosBoot], store.Boots())
	if hits[mBuildStripes] == 0 {
		t.Errorf("build_stripes not reached — ESC held did not take the manual-control opening-screen path")
	}
	if hits[mBdosBoot] != 0 || len(store.Boots()) != 0 {
		t.Errorf("auto-boot happened despite ESC held (bdos_boot_record=%d, Boots=%v) — ESC must override the config", hits[mBdosBoot], store.Boots())
	}
}

// TestTrinityAutobootALHK: the endpoint — entering at `decision` (post-&805F, the
// way the decision tests enter, avoiding the ROM-boot RST-8 conflict), a config for
// record N drives HRECORD-select + ALHK against record N (captured by BDOSStore).
func TestTrinityAutobootALHK(t *testing.T) {
	const rec = 3  // bring-up build hardcodes record 3 (trinload)
	if _, err := os.Stat(trinityBootBin); err != nil {
		t.Fatalf("bootloader not built: %v", err)
	}
	mac, err := z80h.LoadAt(trinityBootBin, trinityBootMap, 0x4000)
	if err != nil {
		t.Fatalf("load bootloader: %v", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramNamedChunk(5, samboot.ChunkName, samboot.Boot(rec).Encode())
	mac.AttachIO(enc)
	store := z80h.NewBDOSStore()
	mac.AttachBDOS(store)

	if _, err := mac.CallEntry("decision", z80h.Entry{}); err != nil {
		t.Fatalf("call decision: %v", err)
	}
	t.Logf("ALHK endpoint: Boots=%v Selected=%d", store.Boots(), store.Selected())
	if got := store.Boots(); len(got) != 1 || got[0] != rec {
		t.Errorf("Boots() = %v, want [%d] — auto-boot did not ALHK the configured record", got, rec)
	}
}
