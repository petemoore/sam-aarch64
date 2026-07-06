// sd_listread_test.go — i141: the REAL card-absolute list-sector read.
//
// Until i141 the detection routines reached the record list through a harness
// RST-8 hook that the CardModel served from its RecordList — the detection
// geometry was exercised, but the raw SD SPI transaction was modelled
// away. i141 adds bd_list_read_hw (src/netboot/sd_csd.asm): a real CMD17
// single-block read on the Trinity SPI ports, the hardware implementation the
// boot images' card-absolute list read needs.
//
// This test Loads the standalone fixture built with NETBOOT_REAL_LISTREAD (Makefile
// netboot-sd-listread), so bdos_read_list_sector tail-calls bd_list_read_hw. It
// attaches the i145c/i145h SD model (sdcard.go, whose CMD17 path streams store[addr]
// back), SeedSectors the record-list sectors at their card-absolute LBAs, and proves
// (a) a single list sector reads back byte-for-byte through the raw CMD17 path, and
// (b) the whole free-record detection (bdos_find_free_record) finds the right record
// driven entirely by real CMD17 reads. Emulation-verified is not hardware-verified
// (CLAUDE.md §5): the real-Trinity SPI run is i141's hardware half.
package z80_test

import (
	"bytes"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	sdListReadBin = "../../../build/netboot_sd_listread.bin"
	sdListReadMap = "../../../build/netboot_sd_listread.map"
)

// loadListReadFixture loads the NETBOOT_REAL_LISTREAD fixture with a v2/SDHC
// (block-addressed) card — the common case, CMD17 carries the LBA verbatim.
func loadListReadFixture(t *testing.T) (*z80h.Machine, *z80h.SDCard) {
	t.Helper()
	const cSize = 0x001D59 // ~3.7 GB SDHC
	return loadListReadFixtureCSD(t, csdV2(cSize))
}

// loadListReadFixtureCSD loads the NETBOOT_REAL_LISTREAD fixture and attaches an
// ENC28J60 with the given CSD, returning the machine and the SD model so the test
// can SeedSector the list sectors. It reads the CSD first (csd_set_bd_records),
// exactly as serve_main/client_main do at startup: this populates CSD_STAGE (so
// bd_list_read_hw picks block vs byte addressing) and BD_RECORDS. A list read only
// runs once the CSD has been read — the ordering production guarantees. Skips if the
// fixture is absent.
func loadListReadFixtureCSD(t *testing.T, csd [16]byte) (*z80h.Machine, *z80h.SDCard) {
	t.Helper()
	if _, err := os.Stat(sdListReadBin); err != nil {
		t.Fatalf("sd_listread fixture not built (%s); run `make netboot-sd-listread`", sdListReadBin)
	}
	mac, err := z80h.Load(sdListReadBin, sdListReadMap)
	if err != nil {
		t.Fatalf("load sd_listread fixture: %v", err)
	}
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csd)
	mac.AttachIO(enc)
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("call csd_set_bd_records: %v", err)
	}
	return mac, sd
}

// TestListReadRealCMD17V1ByteAddr covers the SDv1 (byte-addressed) branch: for a v1
// card CMD17 carries the byte address N<<9, not the block number N. The card class
// comes from CSD_STAGE[0] (read by csd_set_bd_records), so bd_list_read_hw shifts
// the LBA <<9 and the model serves the sector seeded at that byte address.
func TestListReadRealCMD17V1ByteAddr(t *testing.T) {
	// A small v1/SDSC card (READ_BL_LEN=9 -> 512-byte blocks). Capacity only needs to
	// be enough that csd_set_bd_records does not fault; the read test addresses sectors
	// directly.
	mac, sd := loadListReadFixtureCSD(t, csdV1(9, 0x0F00, 7))

	for _, n := range []uint8{1, 4} {
		want := make([]byte, 512)
		for i := range want {
			want[i] = byte((i*3+int(n)*0x21)^(i>>5)) & 0xFF
		}
		sd.SeedSector(uint32(n)<<9, want) // v1: list sector N at byte address N<<9

		got := readListSector(t, mac, n)
		if !bytes.Equal(got, want) {
			d := firstDiff(got, want)
			t.Fatalf("v1 list sector %d read back wrong via real CMD17: first diff at %d (got 0x%02X want 0x%02X)",
				n, d, got[d], want[d])
		}
		t.Logf("v1 list sector %d (byte addr 0x%X): 512 bytes round-tripped through CMD17 byte-addressing", n, uint32(n)<<9)
	}
}

// readListSector sets BD_LIST_SECTOR = n and CALLs bdos_read_list_sector (which, in
// this fixture, tail-calls the real CMD17 bd_list_read_hw), returning the 512 bytes
// it landed in BD_LIST_BUF.
func readListSector(t *testing.T, mac *z80h.Machine, n uint8) []byte {
	t.Helper()
	mac.Write(symAddr(t, mac, "BD_LIST_SECTOR"), []byte{n, 0}) // 16-bit LE seam (i326)
	if _, err := mac.Call("bdos_read_list_sector"); err != nil {
		t.Fatalf("call bdos_read_list_sector(%d): %v", n, err)
	}
	return mac.Read(symAddr(t, mac, "BD_LIST_BUF"), 512)
}

// TestListReadRealCMD17 proves a single card-absolute list sector reads back
// byte-for-byte through the raw CMD17 SPI path. List sector N lives at card LBA N
// (sector 0 is the boot block; trinity-record-detection-design.md §4.1); for the
// SDHC card the CMD17 frame carries that LBA verbatim, so the model serves the
// sector seeded at addr == N.
func TestListReadRealCMD17(t *testing.T) {
	mac, sd := loadListReadFixture(t)

	for _, n := range []uint8{1, 3, 7} {
		// A recognisable 512-byte pattern, distinct per sector, so a stuck/duplicated
		// byte or a wrong-LBA read is caught.
		want := make([]byte, 512)
		for i := range want {
			want[i] = byte((i*5+int(n)*0x11)^(i>>4)) & 0xFF
		}
		sd.SeedSector(uint32(n), want)

		got := readListSector(t, mac, n)
		if !bytes.Equal(got, want) {
			d := firstDiff(got, want)
			t.Fatalf("list sector %d read back wrong via real CMD17: first diff at %d (got 0x%02X want 0x%02X)",
				n, d, got[d], want[d])
		}
		t.Logf("list sector %d (LBA %d): 512 bytes round-tripped through the raw CMD17 SPI path", n, n)
	}
}

// TestFindFreeRecordViaRealCMD17 proves the full free-record detection runs end to
// end on the real CMD17 read: a record list whose record 1 is named and record 2 is
// free is seeded into list sector 1, and bdos_find_free_record — which reads each
// record's entry via bdos_read_list_sector == the real CMD17 path — returns 2.
//
// The list entry's free test is (entry[0] & 0x7F) == 0 (the frec3x test;
// trinity-record-detection-design.md §4.4). Records 1..32 all live in list sector 1
// (listSector = (n-1)/32 + 1), entry n at byte offset (n-1)*16.
func TestFindFreeRecordViaRealCMD17(t *testing.T) {
	mac, sd := loadListReadFixture(t)

	// Build list sector 1: record 1 named ('A'), record 2 free (0), the rest named
	// ('C'), so the first free record is unambiguously 2.
	listSec1 := make([]byte, 512)
	for rec := 1; rec <= 32; rec++ {
		off := (rec - 1) * 16
		switch rec {
		case 1:
			listSec1[off] = 'A' // named
		case 2:
			listSec1[off] = 0x00 // free
		default:
			listSec1[off] = 'C' // named
		}
	}
	sd.SeedSector(1, listSec1) // list sector 1 -> card LBA 1

	// BD_RECORDS = 10: the sweep stays within list sector 1, so only LBA 1 is read.
	mac.WriteU16LE(symAddr(t, mac, "BD_RECORDS"), 10)
	// Zero BD_FREE_RECORD so a green result can only come from the routine.
	mac.WriteU16LE(symAddr(t, mac, "BD_FREE_RECORD"), 0)

	if _, err := mac.Call("bdos_find_free_record"); err != nil {
		t.Fatalf("call bdos_find_free_record: %v", err)
	}
	free := mac.Read(symAddr(t, mac, "BD_FREE_RECORD"), 2)
	got := uint16(free[0]) | uint16(free[1])<<8
	if got != 2 {
		t.Fatalf("bdos_find_free_record via real CMD17 returned %d, want 2", got)
	}
	t.Log("free-record detection found record 2 driven entirely by raw CMD17 list reads")
}
