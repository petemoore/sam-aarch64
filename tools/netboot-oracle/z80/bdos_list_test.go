// bdos_list_test.go — i119c (B3): host-verification of the card-absolute list-
// sector read + free-record detection routines (src/netboot/bdos_seam.asm).
//
// Tests drive the real Z80 code under the koron-go/z80 harness. The binaries
// are built with NETBOOT_REAL_LISTREAD=1, so bdos_read_list_sector tail-calls
// bd_list_read_hw (a real SPI CMD17 via the Trinity ports). The SD model
// (sdcard.go) has list sectors seeded at card-absolute LBAs so the CMD17 reads
// return the expected 512-byte record-list sectors. The tests assert:
//
//   TestRecordEntryParse — bdos_record_entry fetches the correct 16-byte list
//     entry for records in both list sector 1 and list sector 2 (cross-sector
//     boundary). Also verifies the WP-bit-set case: entry[0] == 'A'|0x80 is
//     fetched verbatim (the free-test caller masks 0x7F; the entry itself is
//     not modified here).
//
//   TestFindFreeRecord — bdos_find_free_record scans a card with 40 records
//     (spanning two list sectors): several records in sector 1 named, the rest
//     free; a named record in sector 2; finds the first free in sector 1 and,
//     in a second sub-case, finds a free record only in sector 2. Also verifies
//     the all-named case (expects 0) and the WP-bit free case
//     (entry[0] == 0x80, masked to 0 ⇒ FREE).
//
// THE HONESTY LINE (CLAUDE.md §5): the SD model intercepts the real SPI CMD17
// traffic so the list bytes are served from seeded in-memory sectors. The real
// Trinity SPI ports and SD silicon stay gated on hardware
// (docs/specs/trinity-record-detection-design.md §8). Emulation-verified is
// not hardware-verified.
package z80_test

import (
	"bytes"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// makeEntry builds a 16-byte list entry from a name string (space-padded to 16
// bytes). Byte 0 is name[0]; if name is empty the entry is all-zero (free).
func makeEntry(name string) [16]byte {
	var e [16]byte
	for i := 0; i < 16; i++ {
		if i < len(name) {
			e[i] = name[i]
		} else {
			e[i] = ' '
		}
	}
	return e
}

// TestRecordEntryParse drives bdos_record_entry for records in list sector 1
// and list sector 2 and verifies the 16 bytes match the CardModel.
// Also verifies the WP-bit-set case: the entry is returned verbatim
// (byte 0 == 'A'|0x80); the free test is applied by the caller.
func TestRecordEntryParse(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	card := z80h.NewCardModel()

	// Record 1 — named, in list sector 1.
	entry1 := makeEntry("ALPHA")
	card.SetRecordEntry(1, entry1)

	// Record 10 — named in list sector 1 (10 <= 32).
	entry10 := makeEntry("TENTH")
	card.SetRecordEntry(10, entry10)

	// Record 33 — in list sector 2 ((33-1)/32 + 1 = 2).
	entry33 := makeEntry("SECTOR2REC")
	card.SetRecordEntry(33, entry33)

	// Record 5 — WP bit set on byte 0: entry[0] = 'A' | 0x80. Named, not free.
	var wpEntry [16]byte
	wpEntry = makeEntry("APROTECT")
	wpEntry[0] = wpEntry[0] | 0x80 // set write-protect bit
	card.SetRecordEntry(5, wpEntry)

	// Record 7 — WP bit set, but masked name byte is 0: entry[0] = 0x80 only.
	// This is the WP-bit-only case: masked first byte == 0 ⇒ FREE.
	var wpFreeEntry [16]byte
	wpFreeEntry[0] = 0x80 // WP set, no name → free after masking
	card.SetRecordEntry(7, wpFreeEntry)

	// Attach ENC + SD and seed list sectors from the card model.
	// Records 1..32 are in list sector 1 (LBA 1); record 33 is in list sector 2 (LBA 2).
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csdV2(1))
	mac.AttachIO(enc)
	sec1 := card.ListSector(1)
	sd.SeedSector(1, sec1[:])
	sec2 := card.ListSector(2)
	sd.SeedSector(2, sec2[:])
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("csd_set_bd_records: %v", err)
	}
	// maxListSec = ceil(33/32) = 2; BD_RECORDS must cover at least record 33.
	mac.WriteU16LE(symAddr(t, mac, "BD_RECORDS"), 33)

	getEntry := func(n uint16) []byte {
		t.Helper()
		mac.WriteU16LE(symAddr(t, mac, "BD_ENTRY_REC"), n)
		if _, err := mac.CallEntry("bdos_record_entry", z80h.Entry{}); err != nil {
			t.Fatalf("bdos_record_entry(n=%d): %v", n, err)
		}
		return mac.Read(symAddr(t, mac, "BD_ENTRY_BUF"), 16)
	}

	// Record 1 in list sector 1.
	got := getEntry(1)
	if !bytes.Equal(got, entry1[:]) {
		t.Errorf("record 1: got %q, want %q", got, entry1[:])
	}

	// Record 10 in list sector 1.
	got = getEntry(10)
	if !bytes.Equal(got, entry10[:]) {
		t.Errorf("record 10: got %q, want %q", got, entry10[:])
	}

	// Record 33 in list sector 2 — proves cross-sector fetch.
	got = getEntry(33)
	if !bytes.Equal(got, entry33[:]) {
		t.Errorf("record 33: got %q, want %q", got, entry33[:])
	}

	// Record 5 — WP bit on byte 0, returned verbatim (the 0x7F mask is
	// applied by bdos_find_free_record, not by bdos_record_entry).
	got = getEntry(5)
	if !bytes.Equal(got, wpEntry[:]) {
		t.Errorf("record 5 (WP named): got %q, want %q", got, wpEntry[:])
	}
	if got[0]&0x7F != 'A' {
		t.Errorf("record 5: byte 0 masked should be 'A', got %q", got[0]&0x7F)
	}

	// Record 7 — WP-only entry: entry[0] = 0x80; masked to 0 ⇒ FREE.
	got = getEntry(7)
	if !bytes.Equal(got, wpFreeEntry[:]) {
		t.Errorf("record 7 (WP-only free): got %q, want %q", got, wpFreeEntry[:])
	}
	if got[0]&0x7F != 0 {
		t.Errorf("record 7: byte 0 masked should be 0 (free), got %d", got[0]&0x7F)
	}

	// Record 2 — never set, reads as all-zero (free).
	got = getEntry(2)
	if !bytes.Equal(got, make([]byte, 16)) {
		t.Errorf("record 2 (unset): got %q, want all-zero", got)
	}
}

// TestFindFreeRecord drives bdos_find_free_record with 40 records spanning two
// list sectors and asserts it returns the correct first free record.
//
// Card layout:
//
//	records 1..3   named (in list sector 1)
//	record 4       free  (first free — expected result in sub-case A)
//	records 5..32  named (in list sector 1, records 5–32)
//	record 33      named (in list sector 2)
//	record 34      free  (first free in list sector 2 — expected in sub-case B)
//	records 35..40 named
func TestFindFreeRecord(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	card := z80h.NewCardModel()

	// Set up 40 records, leaving record 4 and record 34 free.
	for n := 1; n <= 40; n++ {
		if n == 4 || n == 34 {
			continue // free — leave as all-zero
		}
		// Name like "REC01", "REC02", …
		b := [16]byte{}
		name := make([]byte, 16)
		for i := range name {
			name[i] = ' '
		}
		copy(name, []byte("REC"))
		if n >= 10 {
			name[3] = byte('0' + n/10)
			name[4] = byte('0' + n%10)
		} else {
			name[3] = '0'
			name[4] = byte('0' + n)
		}
		copy(b[:], name)
		card.SetRecordEntry(n, b)
	}

	// Attach ENC + SD; seed list sectors 1 and 2 (records 1..32 in sec 1, 33..64 in sec 2).
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csdV2(1))
	mac.AttachIO(enc)

	// updateSD re-seeds both list sectors from the current card state and resets
	// BD_RECORDS to 40 so finds cover the full range.
	updateSD := func() {
		t.Helper()
		sec1 := card.ListSector(1)
		sd.SeedSector(1, sec1[:])
		sec2 := card.ListSector(2)
		sd.SeedSector(2, sec2[:])
	}
	updateSD()
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("csd_set_bd_records: %v", err)
	}

	findFree := func(records uint16) uint16 {
		t.Helper()
		mac.WriteU16LE(symAddr(t, mac, "BD_RECORDS"), records)
		if _, err := mac.CallEntry("bdos_find_free_record", z80h.Entry{}); err != nil {
			t.Fatalf("bdos_find_free_record(records=%d): %v", records, err)
		}
		b := mac.Read(symAddr(t, mac, "BD_FREE_RECORD"), 2)
		return uint16(b[0]) | uint16(b[1])<<8
	}

	// Sub-case A: all 40 records; first free is record 4 (in list sector 1).
	got := findFree(40)
	if got != 4 {
		t.Errorf("sub-case A: BD_FREE_RECORD = %d, want 4 (first free in list sector 1)", got)
	}

	// Sub-case B: only scan records 1..34 but make record 4 named too.
	// This forces the scan to cross into list sector 2 before finding record 34.
	card.SetRecordEntry(4, makeEntry("NOWNAMED"))
	updateSD()
	got = findFree(40)
	if got != 34 {
		t.Errorf("sub-case B: BD_FREE_RECORD = %d, want 34 (first free in list sector 2)", got)
	}

	// Sub-case C: all records named (no free) — expect 0.
	card.SetRecordEntry(34, makeEntry("ALSONAMED"))
	updateSD()
	got = findFree(40)
	if got != 0 {
		t.Errorf("sub-case C: BD_FREE_RECORD = %d, want 0 (no free record)", got)
	}

	// Sub-case D: record with only WP bit set (entry[0] = 0x80) is free after masking.
	// Replace record 20 entry[0] with 0x80 (WP set, no name ⇒ free).
	var wpFree [16]byte
	wpFree[0] = 0x80
	card.SetRecordEntry(20, wpFree)
	updateSD()
	got = findFree(40)
	if got != 20 {
		t.Errorf("sub-case D (WP-only = free): BD_FREE_RECORD = %d, want 20", got)
	}

	// Sub-case E: WP-bit-set named entry (entry[0] = 'A'|0x80) is NOT free.
	// Replace record 20 with a WP-named entry; expect no free (all still named).
	var wpNamed [16]byte
	wpNamed[0] = 'A' | 0x80 // WP set, name byte 'A' → masked to 'A' ≠ 0 ⇒ named
	copy(wpNamed[1:], "FOOBAR")
	card.SetRecordEntry(20, wpNamed)
	updateSD()
	got = findFree(40)
	if got != 0 {
		t.Errorf("sub-case E (WP named = not free): BD_FREE_RECORD = %d, want 0", got)
	}
}
