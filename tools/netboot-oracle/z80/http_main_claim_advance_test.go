// http_main_claim_advance_test.go — i360b: host-verification that the HTTP store
// loop CLAIMS each stored Trinity SD record so the NEXT file's free-record search
// advances instead of re-picking the record just stored (the multi-file re-pick bug).
//
// With the NETBOOT_WANT_CLAIM full build (netboot_http_boot.bin), store_end writes
// the just-stored record's central record-LIST name entry via bdos_claim_record.
// bdos_find_free_record tests the list entry blank; store_format_record only sets the
// +232 directory stamp (which find_free does not read), so WITHOUT the claim a second
// file's store_begin re-picks the same record and overwrites the first. This test
// proves store_begin -> store_end(claim) -> store_begin advances the picked record.
//
// FLAT koron harness: the find-free scan and the claim are pure raw CMD17/CMD24 list
// operations the sdcard.go SPI model fully services (no B-DOS RST 8 hooks in this
// path — that is store_end's separate HSAVE leaf, faithful-tested elsewhere). Mirrors
// the SD-model setup in bdos_claim_name_test.go / TestClaimRMWRealCMD17CMD24.
package z80_test

import (
	"encoding/binary"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

func TestProvisionClaimAdvancesRecordZ80(t *testing.T) {
	mac := loadHTTPMain(t) // netboot_http_boot.bin — the full build, NETBOOT_WANT_CLAIM

	// Attach an SDHC card; csd_set_bd_records primes CSD_STAGE (the v1/v2 block-vs-byte
	// addressing decision used by bd_list_read_hw / bd_list_write_hw) and BD_RECORDS
	// (the find-free scan bound).
	const cSize = 0x001D59 // ~3.7 GB SDHC (block-addressed), same fixture as the claim tests
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csdV2(cSize))
	mac.AttachIO(enc)
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("csd_set_bd_records: %v", err)
	}

	// Seed list sector 1 (LBA 1 on SDHC): records 1 and 2 FREE (all-zero), records
	// 3..32 named — so the first two find-free scans should return 1, then 2.
	const totalRecords = 32
	var sec [512]byte
	for n := 3; n <= totalRecords; n++ {
		e := neighbourEntry(n)
		copy(sec[(n-1)*16:], e[:])
	}
	sd.SeedSector(1, sec[:]) // LBA 1 = list sector 1 on SDHC
	mac.WriteU16LE(symAddr(t, mac, "BD_RECORDS"), uint16(totalRecords))

	readBase := func() uint16 {
		return binary.LittleEndian.Uint16(mac.Read(symAddr(t, mac, "FW_BASE_RECORD"), 2))
	}

	// store_begin #1: the first free record is 1.
	if _, err := mac.Call("store_begin"); err != nil {
		t.Fatalf("store_begin #1: %v", err)
	}
	if base := readBase(); base != 1 {
		t.Fatalf("first store_begin picked record %d, want 1 (the first free record)", base)
	}

	// store_end: finalises the verify verdict (harmless here — no body was streamed),
	// then claims record 1 by writing its list entry via a real CMD24.
	if _, err := mac.Call("store_end"); err != nil {
		t.Fatalf("store_end (claim): %v", err)
	}
	// Record 1's own 16-byte list slot (entry 0, offset 0) must now be NON-blank —
	// the claim's CMD24 wrote a name into the slot the seed left all-zero (free).
	// (Checking CapturedSector alone is not enough: SeedSector already populated LBA
	// 1, so the discriminator is the slot content, not the sector's existence.)
	got, ok := sd.CapturedSector(1)
	if !ok {
		t.Fatalf("no captured sector at LBA 1 — the SD model lost the list sector")
	}
	if blank := isAllZero(got[0:16]); blank {
		t.Fatalf("record 1's list slot is still blank after store_end — the i360b claim CMD24 did not write it")
	}

	// store_begin #2: record 1 is now claimed (its list entry non-blank), so the scan
	// ADVANCES to record 2. This is the re-pick fix — without the claim it would pick
	// record 1 again and the second file would overwrite the first.
	if _, err := mac.Call("store_begin"); err != nil {
		t.Fatalf("store_begin #2: %v", err)
	}
	if base := readBase(); base != 2 {
		t.Fatalf("after claiming record 1, store_begin picked record %d, want 2 — "+
			"the claim did not advance bdos_find_free_record (the multi-file re-pick bug)", base)
	}
}

func isAllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
