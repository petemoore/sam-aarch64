// csd_to_bd_records_test.go — i145b: the test that ENDS the inject-only shortcut.
//
// The netboot serve/client startup must COMPUTE BD_RECORDS (the card's total
// B-DOS record count) from the inserted SD card's CSD, not have it injected by a
// test. Until i145b the only writer of BD_RECORDS was test code
// (mac.WriteU16LE(..., N)); on real hardware it stayed 0, so the WRQ server and the
// client picker found "no free record" and declined every push (the gap that
// shipped un-emulated). src/netboot/sd_csd.asm reads the CSD over the modelled
// SPI bus and derives BD_RECORDS.
//
// This test Loads the standalone host-test fixture (netboot_sd_csd.bin: encdrv's
// wait_ready + bdos_seam's BD_RECORDS + sd_csd.asm — the same set the boot images
// include), attaches the i145c SD model (sdcard.go) with a configured CSD, and
// CALLs csd_set_bd_records by symbol. It then reads BD_RECORDS back and asserts it
// equals the i145e-validated Go reference refRecords(refBlocksV2/V1(...)) — i.e.
// COMPUTED from the modelled card, including the q40 16-bit wrap on Pete's 64 GB
// card. No WriteU16LE inject anywhere: the value is produced by the Z80 decode.
//
// The fixture (Makefile netboot-sd-csd) is the isolated unit test of the decode.
// It now ALSO ships in the serve/client boot images as a section-D overlay (i145b-b2;
// the ~600-byte module's tail runs above &C000, which is RAM at boot), exercised
// through the real serve_main by TestServeBootComputesBDRecordsFromCSD.
package z80_test

import (
	"fmt"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	sdCSDBin = "../../../build/netboot_sd_csd.bin"
	sdCSDMap = "../../../build/netboot_sd_csd.map"
)

// loadSDCSDFixture loads the standalone CSD-decode fixture and attaches an ENC28J60
// with the given CSD configured on the SD model. Skips if the fixture is not built.
func loadSDCSDFixture(t *testing.T, csd [16]byte) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(sdCSDBin); err != nil {
		t.Fatalf("sd_csd fixture not built (%s); run `make netboot-sd-csd`", sdCSDBin)
	}
	mac, err := z80h.Load(sdCSDBin, sdCSDMap)
	if err != nil {
		t.Fatalf("load sd_csd fixture: %v", err)
	}
	enc := z80h.NewENC28J60()
	enc.AttachSD(csd)
	mac.AttachIO(enc)
	return mac
}

// computeBDRecords CALLs csd_set_bd_records (the production startup routine) against
// the attached SD model and returns the BD_RECORDS word it stored. The value is
// COMPUTED by the Z80 decode from the modelled card's CSD — never injected.
func computeBDRecords(t *testing.T, mac *z80h.Machine) uint16 {
	t.Helper()
	// Guard against a stale/injected value masquerading as a computed one: zero
	// BD_RECORDS first, so a green result can only come from the routine writing it.
	addr := symAddr(t, mac, "BD_RECORDS")
	mac.WriteU16LE(addr, 0)
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("call csd_set_bd_records: %v", err)
	}
	b := mac.Read(addr, 2)
	return uint16(b[0]) | uint16(b[1])<<8
}

// TestCSDToBDRecordsV2 is the headline check: against a range of v2.0/SDHC cards
// (incl. Pete's ~64 GB, which overflows the 16-bit BD_RECORDS slot), the startup
// routine reads the CSD over the modelled SPI bus and stores the SAME 16-bit
// record count the i145e-validated Go reference computes — COMPUTED, not injected.
func TestCSDToBDRecordsV2(t *testing.T) {
	cases := []struct {
		name  string
		cSize uint32
	}{
		{"8MB-min", 0x00000F},
		{"~3.7GB", 0x001D59},
		{"~30GB", 0x00F3FF},
		{"64GB-Pete", 0x01E8FF}, // records overflow the 16-bit slot -> wraps
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			csd := csdV2(tc.cSize)
			mac := loadSDCSDFixture(t, csd)

			got := computeBDRecords(t, mac)

			blocks := refBlocksV2(tc.cSize)
			_, wantRecords := refRecords(blocks)
			_, fullRecords := refRecordsFull(blocks)
			if uint32(got) != wantRecords {
				t.Fatalf("C_SIZE=0x%X blocks=%d: computed BD_RECORDS=%d, Go reference=%d",
					tc.cSize, blocks, got, wantRecords)
			}
			note := "computed==Go"
			if fullRecords > 0xFFFF {
				note = fmt.Sprintf("computed==Go(16-bit); TRUE records=%d wraps to %d (q40)", fullRecords, got)
			}
			t.Logf("v2 C_SIZE=0x%X CSD[7..9]=%02x %02x %02x blocks=%d BD_RECORDS=%d (%s)",
				tc.cSize, csd[7], csd[8], csd[9], blocks, got, note)
		})
	}
}

// TestCSDToBDRecordsV1 repeats the check for v1.0/SDSC cards (CSD_STRUCTURE 00):
// the decode takes the v1 capacity branch and the computed BD_RECORDS matches the
// Go reference.
func TestCSDToBDRecordsV1(t *testing.T) {
	cases := []struct {
		name                        string
		readBlLen, cSize, cSizeMult uint32
	}{
		{"2GB-SDSC", 10, 4095, 7},
		{"1GB-SDSC", 10, 3823, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			csd := csdV1(tc.readBlLen, tc.cSize, tc.cSizeMult)
			mac := loadSDCSDFixture(t, csd)

			got := computeBDRecords(t, mac)

			blocks := refBlocksV1(tc.readBlLen, tc.cSize, tc.cSizeMult)
			_, wantRecords := refRecords(blocks)
			if uint32(got) != wantRecords {
				t.Fatalf("v1 BL=%d C_SIZE=%d MULT=%d blocks=%d: computed BD_RECORDS=%d, Go reference=%d",
					tc.readBlLen, tc.cSize, tc.cSizeMult, blocks, got, wantRecords)
			}
			t.Logf("v1 BL=%d C_SIZE=%d MULT=%d blocks=%d BD_RECORDS=%d (computed==Go)",
				tc.readBlLen, tc.cSize, tc.cSizeMult, blocks, got)
		})
	}
}

// TestCSDToBDRecordsBaseFor64GB is the i295 create-record regression gate: the Z80
// csd_set_bd_records must compute csd_base (the record-body LBA anchor:
// LBA = csd_base + 1600*(n-1) + i) EQUAL to what B-DOS 1.5t uses, or every raw-LBA
// record write lands where B-DOS does not look. It asserts csd_base against the Go
// reference refRecords (which mirrors B-DOS's &A452 16-bit-overflow clamp), and pins
// the empirically-proven value 2050 for Pete's 64 GB card. This runs on the built
// fixture + SD model — NO private B-DOS binary — so it gates CI directly (it is the
// unit-level counterpart of the full-boot TestBDOSRecordsMathBase64GB).
func TestCSDToBDRecordsBaseFor64GB(t *testing.T) {
	cases := []struct {
		name     string
		cSize    uint32
		wantBase uint16 // empirically-proven / B-DOS-matching base
	}{
		{"64GB-Pete", 0x01DBD3, 2050}, // Pete's real card: the 16-bit clamp -> base 2050 (not 2438)
		{"64GB-alt", 0x01E8FF, 2050},  // another >51 GB card: also clamps to base 2050
		{"~30GB", 0x00F3FF, 1251},     // below the clamp threshold: base unaffected
		{"~3.7GB", 0x001D59, 152},     // the small card the gold rigs use
		{"8MB-min", 0x00000F, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mac := loadSDCSDFixture(t, csdV2(tc.cSize))
			// Zero csd_base first so a green result can only be the routine writing it.
			baseAddr := symAddr(t, mac, "csd_base")
			mac.WriteU16LE(baseAddr, 0)
			if _, err := mac.Call("csd_set_bd_records"); err != nil {
				t.Fatalf("csd_set_bd_records: %v", err)
			}
			b := mac.Read(baseAddr, 2)
			gotBase := uint16(b[0]) | uint16(b[1])<<8

			blocks := refBlocksV2(tc.cSize)
			wantBase, _ := refRecords(blocks)
			if uint32(gotBase) != wantBase {
				t.Fatalf("C_SIZE=0x%X blocks=%d: Z80 csd_base=%d, Go reference (B-DOS-matching) base=%d",
					tc.cSize, blocks, gotBase, wantBase)
			}
			if gotBase != tc.wantBase {
				t.Fatalf("C_SIZE=0x%X: Z80 csd_base=%d, want %d (the empirically-proven base)", tc.cSize, gotBase, tc.wantBase)
			}
			t.Logf("C_SIZE=0x%X blocks=%d -> csd_base=%d (record n body LBA = %d + 1600*(n-1); == B-DOS)",
				tc.cSize, blocks, gotBase, gotBase)
		})
	}
}

// TestCSDToBDRecordsBoundedOnStuckBusy is the i250/fix-#5 hang-safety gate, run as
// part of the DEFAULT suite (no opt-in): it models a wedged Trinity whose &DC BUSY
// bit (bit 3) NEVER clears — the real-hardware failure that hung Pete's SAM (the
// 2026-06-24 3-restart hang) — and asserts the SHARED serve/client SD path
// (csd_set_bd_records → sdc_init_ladder → sdc_in/sdc_out) TERMINATES instead of
// spinning forever.
//
// Before fix #5 the shared sdc_out/sdc_in called the vendored UNBOUNDED wait_ready
// (encdrv.asm:427, `JR wait_ready` forever): against this stuck controller the
// routine would never return, the harness would run to its step cap, and Call would
// error. The bounded sdc_wait_ready (sd_csd.asm) gives up after SDC_BUSY_LIMIT polls
// and sets the sticky sdc_timed_out, so the whole ladder unwinds fast and the read
// fails gracefully (BD_RECORDS left 0 — the same safe decline as no card). Revert
// the bound (point sdc_out/sdc_in back at wait_ready) and this test fails by hanging
// to the step cap — the revert-the-fix-fails proof that the emulator now CATCHES the
// unbounded-wait class by default, not only under the opt-in csd_probe probe (i241).
func TestCSDToBDRecordsBoundedOnStuckBusy(t *testing.T) {
	if _, err := os.Stat(sdCSDBin); err != nil {
		t.Fatalf("sd_csd fixture not built (%s); run `make netboot-sd-csd`", sdCSDBin)
	}
	mac, err := z80h.Load(sdCSDBin, sdCSDMap)
	if err != nil {
		t.Fatalf("load sd_csd fixture: %v", err)
	}
	enc := z80h.NewENC28J60()
	enc.AttachSD(csdV2(0x001D59)) // a real CSD is configured, but it can never be read
	mac.AttachIO(enc)
	enc.StuckBusy = true // the controller's BUSY bit never clears

	// Must TERMINATE. With the unbounded wait_ready this Call would run to the step
	// cap and error; the bound makes it return (the busy-wait gives up).
	addr := symAddr(t, mac, "BD_RECORDS")
	mac.WriteU16LE(addr, 0)
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("csd_set_bd_records did not terminate against a stuck Trinity: %v "+
			"(the SD busy-wait is unbounded — fix #5 missing, the SAM would hang)", err)
	}

	// The bounded busy-wait actually fired (sticky flag set), and the read declined
	// safely (no bogus record count from garbage SPI reads).
	if to := mac.Read(symAddr(t, mac, "sdc_timed_out"), 1)[0]; to != 1 {
		t.Errorf("sdc_timed_out = %d after a stuck-BUSY transaction, want 1 (the bounded wait must have given up)", to)
	}
	b := mac.Read(addr, 2)
	if got := uint16(b[0]) | uint16(b[1])<<8; got != 0 {
		t.Errorf("BD_RECORDS = %d against a stuck Trinity, want 0 (safe decline on a failed read)", got)
	}
}

// TestCSDToBDRecordsDeselectTailProper is the i251/fix-#6 gate, run as part of the
// DEFAULT suite: a real shared-path SD transaction (csd_set_bd_records, which ends
// in sdc_deselect) must close the card with Colin's proven 4-step deselect tail
// (&30 / dummy &FF on &DF / &30 / &04), not the old 2-step (&30 / &04) that leaves
// SPI state a real card mishandles on silicon. The model tracks the ordered close
// (trackSDClose) and LastSDCloseProper reports whether the proven order ran; this
// test gates on it. Revert sdc_deselect to the 2-step close and this fails — the
// revert-the-fix-fails proof that the wrong-deselect class is now a build-time catch
// (CLAUDE.md rule 7), where before it was observable-but-ungated for this path.
func TestCSDToBDRecordsDeselectTailProper(t *testing.T) {
	if _, err := os.Stat(sdCSDBin); err != nil {
		t.Fatalf("sd_csd fixture not built (%s); run `make netboot-sd-csd`", sdCSDBin)
	}
	mac, err := z80h.Load(sdCSDBin, sdCSDMap)
	if err != nil {
		t.Fatalf("load sd_csd fixture: %v", err)
	}
	enc := z80h.NewENC28J60()
	enc.AttachSD(csdV2(0x001D59))
	mac.AttachIO(enc)

	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("call csd_set_bd_records: %v", err)
	}

	proper, observed := enc.LastSDCloseProper()
	if !observed {
		t.Fatal("no SD deselect-tail close observed after csd_set_bd_records (the transaction did not deselect the card?)")
	}
	if !proper {
		t.Fatal("SD deselect was not Colin's proven 4-step close (&30 / dummy &DF / &30 / &04) — " +
			"fix #6 missing, a real card mishandles the SPI tail on silicon")
	}
}

// TestCSDToBDRecordsNoCard confirms the safe-decline path: with no SD card
// configured (the model inert), csd_read_into_stage cannot read a CSD and
// BD_RECORDS is left 0 — so the picker finds no free record and declines, never a
// bogus non-zero count.
func TestCSDToBDRecordsNoCard(t *testing.T) {
	if _, err := os.Stat(sdCSDBin); err != nil {
		t.Fatalf("sd_csd fixture not built (%s); run `make netboot-sd-csd`", sdCSDBin)
	}
	mac, err := z80h.Load(sdCSDBin, sdCSDMap)
	if err != nil {
		t.Fatalf("load sd_csd fixture: %v", err)
	}
	enc := z80h.NewENC28J60() // no AttachSD: the SD model is inert
	mac.AttachIO(enc)

	got := computeBDRecords(t, mac)
	if got != 0 {
		t.Fatalf("no card: BD_RECORDS=%d, want 0 (safe decline)", got)
	}
}
