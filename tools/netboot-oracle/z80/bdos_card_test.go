// bdos_card_test.go — i119 brick 1: host-verification of the CardModel and the
// Z80 bdos_read_sector primitive (HRSAD hook 160). The harness models a
// multi-record Trinity SD card; the test builds a card with mixed record states,
// drives the real Z80 bdos_read_sector routine via CallEntry, and asserts the
// Z80 reads back the correct bytes from the harness model.
//
// This is the emulation foundation for i119 free-record-detection: brick 2 will
// use this model to verify the detection algorithm host-side.
package z80_test

import (
	"bytes"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// TestCardModelBDOSStamp verifies that bdos_read_sector (driven by the HRSAD
// handler) returns the correct sector data for a B-DOS-formatted record. Record 1
// has the "BDOS" stamp at offset 232 of its first data sector; record 2 is raw
// (all zeros). After selecting each record and calling bdos_read_sector, the test
// asserts the stamp is present / absent at offset 232 of BD_READ_BUF.
func TestCardModelBDOSStamp(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()

	// Record 1: formatted with a BDOS stamp at offset 232 of sector 0.
	card.SetBDOSStamp(1)
	// Record 2: raw (no stamp — all zeros).

	store.AttachCard(card)
	mac.AttachBDOS(store)

	readSector := func(record int, track, sector byte) []byte {
		t.Helper()
		// Select the record.
		if _, err := mac.CallEntry("bdos_select_record", z80h.Entry{A: uint8(record)}); err != nil {
			t.Fatalf("bdos_select_record(%d): %v", record, err)
		}
		// Set up BD_READ_TRACK / BD_READ_SECTOR and call bdos_read_sector.
		mac.Write(symAddr(t, mac, "BD_READ_TRACK"), []byte{track})
		mac.Write(symAddr(t, mac, "BD_READ_SECTOR"), []byte{sector})
		if _, err := mac.CallEntry("bdos_read_sector", z80h.Entry{}); err != nil {
			t.Fatalf("bdos_read_sector(record=%d track=%d sector=%d): %v", record, track, sector, err)
		}
		return mac.Read(symAddr(t, mac, "BD_READ_BUF"), 512)
	}

	// Record 1, first data sector (track=0, sector=1 → linear sector 0).
	buf1 := readSector(1, 0, 1)
	if !bytes.Equal(buf1[232:236], []byte("BDOS")) {
		t.Errorf("record 1 sector 0: bytes at offset 232 = %q, want \"BDOS\"", buf1[232:236])
	}

	// Record 2, first data sector — no stamp, expect all-zero at offset 232.
	buf2 := readSector(2, 0, 1)
	if !bytes.Equal(buf2[232:236], []byte{0, 0, 0, 0}) {
		t.Errorf("record 2 sector 0: bytes at offset 232 = %v, want all-zero (unformatted)", buf2[232:236])
	}
}

// TestCardModelRecordEntry verifies that SetRecordName sets the name bytes of a
// record-list entry correctly, accessible via RecordEntry. This exercises the
// CardModel API directly (no Z80 involved) — it is the Go-side unit test for the
// data model brick 2 will drive via Z80 code.
func TestCardModelRecordEntry(t *testing.T) {
	card := z80h.NewCardModel()
	// Record 3 with a 10-character name (exactly fills the name field).
	card.SetRecordName(3, "MYRECORD  ")

	entry := card.RecordEntry(3)
	wantName := [10]byte{'M', 'Y', 'R', 'E', 'C', 'O', 'R', 'D', ' ', ' '}
	if [10]byte(entry[:10]) != wantName {
		t.Errorf("RecordEntry(3) name bytes = %q, want %q", entry[:10], wantName)
	}

	// Records 1 and 2 were never set: their entries must read as all-zero.
	for _, n := range []int{1, 2} {
		e := card.RecordEntry(n)
		if e != ([16]byte{}) {
			t.Errorf("RecordEntry(%d) = %v, want all-zero (never set)", n, e)
		}
	}

	// A record number well beyond the list must also return all-zero.
	if card.RecordEntry(100) != ([16]byte{}) {
		t.Errorf("RecordEntry(100) should be all-zero for an uninitialised card")
	}
}

// TestInspectRecord drives the real Z80 bdos_inspect_record routine (i119 brick 2)
// — the user-reachable record-identity primitive that free-record detection is
// built on. For each record it performs the reachable sequence (HRECORD select →
// bdos_read_sector the first directory sector → bdos_inspect_record) and asserts
// the routine extracts the "BDOS" stamp flag (+232) and the disk label (+210)
// correctly. Record 1 is a formatted, named B-DOS record; record 2 is raw (no
// stamp, no name) — the two states the detection scan must tell apart.
func TestInspectRecord(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()

	// Record 1: a formatted B-DOS record named "MYDISK".
	card.SetBDOSStamp(1)
	card.SetRecordLabel(1, "MYDISK")
	// Record 2: raw — no stamp, no label.

	store.AttachCard(card)
	mac.AttachBDOS(store)

	inspect := func(record int) (isBDOS byte, name []byte) {
		t.Helper()
		if _, err := mac.CallEntry("bdos_select_record", z80h.Entry{A: uint8(record)}); err != nil {
			t.Fatalf("bdos_select_record(%d): %v", record, err)
		}
		// Read the record's first directory sector (track 0, sector 1 → linear 0).
		mac.Write(symAddr(t, mac, "BD_READ_TRACK"), []byte{0})
		mac.Write(symAddr(t, mac, "BD_READ_SECTOR"), []byte{1})
		if _, err := mac.CallEntry("bdos_read_sector", z80h.Entry{}); err != nil {
			t.Fatalf("bdos_read_sector(record=%d): %v", record, err)
		}
		if _, err := mac.CallEntry("bdos_inspect_record", z80h.Entry{}); err != nil {
			t.Fatalf("bdos_inspect_record(record=%d): %v", record, err)
		}
		isBDOS = mac.Read(symAddr(t, mac, "BD_REC_IS_BDOS"), 1)[0]
		name = mac.Read(symAddr(t, mac, "BD_REC_NAME"), 10)
		return isBDOS, name
	}

	// Record 1: stamped + named.
	isBDOS, name := inspect(1)
	if isBDOS != 1 {
		t.Errorf("record 1: BD_REC_IS_BDOS = %d, want 1 (formatted B-DOS record)", isBDOS)
	}
	wantName := []byte("MYDISK    ") // 10 bytes, space-padded
	if !bytes.Equal(name, wantName) {
		t.Errorf("record 1: BD_REC_NAME = %q, want %q", name, wantName)
	}

	// Record 2: raw — no stamp, label reads as all-zero (sector never written).
	isBDOS, name = inspect(2)
	if isBDOS != 0 {
		t.Errorf("record 2: BD_REC_IS_BDOS = %d, want 0 (raw/unformatted record)", isBDOS)
	}
	if !bytes.Equal(name, make([]byte, 10)) {
		t.Errorf("record 2: BD_REC_NAME = %q, want all-zero (raw record)", name)
	}
}
