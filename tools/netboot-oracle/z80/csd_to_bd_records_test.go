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
		t.Skipf("sd_csd fixture not built (%s); run `make netboot-sd-csd`", sdCSDBin)
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

// TestCSDToBDRecordsNoCard confirms the safe-decline path: with no SD card
// configured (the model inert), csd_read_into_stage cannot read a CSD and
// BD_RECORDS is left 0 — so the picker finds no free record and declines, never a
// bogus non-zero count.
func TestCSDToBDRecordsNoCard(t *testing.T) {
	if _, err := os.Stat(sdCSDBin); err != nil {
		t.Skipf("sd_csd fixture not built (%s); run `make netboot-sd-csd`", sdCSDBin)
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
