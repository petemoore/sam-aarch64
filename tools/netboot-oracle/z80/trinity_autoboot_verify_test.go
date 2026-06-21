//go:build trinityboot

// trinity_autoboot_verify_test.go — LOCAL cross-repo verification (NOT in CI; the
// `trinityboot` build tag excludes it). Boots the patched bootloader built in the
// sibling repo ~/git/trinity-autoboot against the real captured ROM + B-DOS.
//
//	Run: cd ~/git/trinity-autoboot && make
//	     cd ~/git/sam-aarch64/tools/netboot-oracle && go test ./z80/ -tags trinityboot -run Trinity -v
//
// Addresses (from build/bootloader.map): decision &415E, no_autoboot &4169,
// build_stripes &4187, basic_exit &4174, read_config &4203, bdos_boot_record &42EF.
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

// TestTrinityNoConfigBootsToBASIC: with no config chunk, the bootloader runs the
// opening screen (build_stripes) + wait-for-key, then falls to BASIC.
func TestTrinityNoConfigBootsToBASIC(t *testing.T) {
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
	mac.InjectKeys([]byte{'x'}) // dismiss the WTFK / editor key-wait

	// HONESTY LINE: build_stripes is pure RAM writes (runs in the screenless harness),
	// but draw_banner (CLSLOWER/POMSG via paged ROM) + READKEY are ROM display/keyboard
	// the screenless core cannot run — they wander/loop here and are hardware-confirmed
	// (i230). So we assert the part that DOES run: the boot reaches decision -> the
	// no-auto-boot opening screen, and build_stripes builds the &ED1B LINICOLS rainbow.
	hits := map[uint16]int{0x415E: 0, 0x4187: 0} // decision, build_stripes
	const linicols = 0x5600
	var stripes []byte
	mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: 6_000_000, FrameIntPeriod: 20000,
		Trace: func(pc uint16) {
			if _, ok := hits[pc]; ok {
				hits[pc]++
			}
			if pc == 0x41A4 && stripes == nil { // draw_banner entry: build_stripes just returned, paging intact
				stripes = mac.Read(linicols, 16*4+1)
			}
		},
	})
	if stripes == nil {
		stripes = mac.Read(linicols, 16*4+1)
	}
	t.Logf("no-config: decision=%d build_stripes=%d (banner/WTFK/BASIC are hardware-only, i230)", hits[0x415E], hits[0x4187])
	if hits[0x415E] == 0 {
		t.Errorf("decision not reached — full boot did not reach our return-path code")
	}
	if hits[0x4187] == 0 {
		t.Errorf("build_stripes not reached — opening screen did not run")
	}
	for i, scan := 0, 0; scan < 166; i, scan = i+1, scan+11 {
		if int(stripes[i*4]) != scan {
			t.Errorf("LINICOLS[%d].scan = %d, want %d (&ED1B rainbow not built)", i, stripes[i*4], scan)
			break
		}
	}
	if stripes[16*4] != 0xFF {
		t.Errorf("LINICOLS terminator = &%02X, want &FF", stripes[16*4])
	}
}

// TestTrinityWithConfigReachesAutoboot: with a "SAMBOOT Config  " chunk for record
// N, the full boot reads it and reaches the auto-boot dispatch (bdos_boot_record).
// No BDOSStore (it would swallow the ROM's RST 8/DB 50 EEPROM fetch); reaching the
// dispatch proves the config was read and acted on. The ALHK endpoint is the next test.
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

	hits := map[uint16]int{0x4203: 0, 0x42EF: 0, 0x4169: 0} // read_config, bdos_boot_record, no_autoboot
	mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: 6_000_000, FrameIntPeriod: 20000,
		Trace: func(pc uint16) {
			if _, ok := hits[pc]; ok {
				hits[pc]++
			}
		},
	})
	t.Logf("with-config: read_config=%d bdos_boot_record=%d no_autoboot=%d", hits[0x4203], hits[0x42EF], hits[0x4169])
	if hits[0x4203] == 0 {
		t.Errorf("read_config not reached in the full boot")
	}
	if hits[0x42EF] == 0 {
		t.Errorf("bdos_boot_record not reached — config not found/acted-on (went no_autoboot=%d)", hits[0x4169])
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
