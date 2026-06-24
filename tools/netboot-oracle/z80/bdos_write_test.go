// bdos_write_test.go — i114c: host-verification of the storage-class
// mechanism (docs/specs/netboot-storage-manifest-design.md §6.5, decision 5).
//
// Two test groups:
//
//  1. bdos_validate_disk_record (host-build binary, NETBOOT_HOSTTEST):
//     pure memory reads, no RST 8 — assertable from the standalone bdos_seam
//     binary just like bdos_inspect_record. Compares the Z80 result byte
//     against the Go authority bdos.ValidateDiskRecord.
//
//  2. bdos_write_sector / bdos_write_record (client boot binary, RST 8):
//     exercises the real HWSAD dispatch via AttachBDOS, writes known bytes
//     into the CardModel, then reads them back via HRSAD to prove round-trip
//     fidelity. Mirrors the bdos_card_test.go pattern for bdos_read_sector.
//
// THE HONESTY LINE (CLAUDE.md §5): the HWSAD handler in bdos_store.go models
// the digital dispatch (which hook, which sector coordinates, from which
// buffer, for which record) — not a real SD write. Emulation-verified is not
// hardware-verified.
package z80_test

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// ---------------------------------------------------------------------------
// bdos_validate_disk_record — host-build (bdos_seam standalone binary)
// ---------------------------------------------------------------------------

// TestBDOSValidateDiskRecord asserts the Z80 bdos_validate_disk_record agrees
// with the Go authority bdos.ValidateDiskRecord across the four cases that
// matter: valid image, wrong size, missing stamp, wrong stamp bytes.
func TestBDOSValidateDiskRecord(t *testing.T) {
	// validSector is a 512-byte first sector with the BDOS stamp at offset 232.
	validSector := make([]byte, 512)
	copy(validSector[bdos.BDOSStampOffset:], []byte("BDOS"))

	// badStampSector has a 4-byte field at offset 232 that is not "BDOS".
	badStampSector := make([]byte, 512)
	copy(badStampSector[bdos.BDOSStampOffset:], []byte("XXXX"))

	// zeroSector has no stamp at all (all zeros at offset 232).
	zeroSector := make([]byte, 512)

	cases := []struct {
		name      string
		sizeLE    [4]byte // 32-bit LE value to write into BD_REC_SIZE
		sector    []byte  // 512 bytes to write into BD_READ_BUF
		wantValid byte    // expected BD_REC_VALID result (1 = valid, 0 = reject)
	}{
		{
			name:      "valid: size==819200 and stamp==BDOS",
			sizeLE:    le32(bdos.RecordSize),
			sector:    validSector,
			wantValid: 1,
		},
		{
			name:      "wrong size (too small): stamp present but size mismatch",
			sizeLE:    le32(bdos.RecordSize - 1),
			sector:    validSector,
			wantValid: 0,
		},
		{
			name:      "wrong size (too large): stamp present but size mismatch",
			sizeLE:    le32(bdos.RecordSize + 1),
			sector:    validSector,
			wantValid: 0,
		},
		{
			name:      "missing stamp: size correct but no BDOS at +232",
			sizeLE:    le32(bdos.RecordSize),
			sector:    zeroSector,
			wantValid: 0,
		},
		{
			name:      "wrong stamp bytes: size correct but stamp != BDOS",
			sizeLE:    le32(bdos.RecordSize),
			sector:    badStampSector,
			wantValid: 0,
		},
		{
			name:      "zero size: both checks fail",
			sizeLE:    le32(0),
			sector:    zeroSector,
			wantValid: 0,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			mac := loadBDOS(t)

			// Write BD_REC_SIZE (32-bit LE) and BD_READ_BUF.
			mac.Write(symAddr(t, mac, "BD_REC_SIZE"), c.sizeLE[:])
			mac.Write(symAddr(t, mac, "BD_READ_BUF"), c.sector)

			if _, err := mac.Call("bdos_validate_disk_record"); err != nil {
				t.Fatalf("call bdos_validate_disk_record: %v", err)
			}
			got := mac.Read(symAddr(t, mac, "BD_REC_VALID"), 1)[0]
			if got != c.wantValid {
				t.Errorf("BD_REC_VALID = %d, want %d", got, c.wantValid)
			}

			// Cross-check against the Go authority: they must agree.
			size := int(uint32(c.sizeLE[0]) | uint32(c.sizeLE[1])<<8 |
				uint32(c.sizeLE[2])<<16 | uint32(c.sizeLE[3])<<24)
			goErr := bdos.ValidateDiskRecord(size, c.sector)
			goValid := byte(0)
			if goErr == nil {
				goValid = 1
			}
			if got != goValid {
				t.Errorf("Z80 result %d disagrees with Go authority %d (Go err: %v)", got, goValid, goErr)
			}
		})
	}
}

// le32 encodes n as a 4-byte little-endian array.
func le32(n int) [4]byte {
	v := uint32(n)
	return [4]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

// ---------------------------------------------------------------------------
// bdos_write_sector / bdos_write_record — client boot binary (RST 8)
// ---------------------------------------------------------------------------

// TestBDOSWriteSector verifies the real Z80 bdos_write_sector issues HWSAD
// and the harness model captures the write, making the bytes readable back via
// HRSAD. Drives HRECORD → bdos_write_sector → bdos_read_sector and asserts
// round-trip byte-identity.
func TestBDOSWriteSector(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	store.AttachCard(card)
	mac.AttachBDOS(store)

	const record = 2

	// Select record 2 via HRECORD.
	if _, err := mac.CallEntry("bdos_select_record", z80h.Entry{A: record}); err != nil {
		t.Fatalf("bdos_select_record(%d): %v", record, err)
	}

	// Stage known bytes into BD_WRITE_BUF.
	wantData := makeSectorPayload(0xAB) // 512 bytes of 0xAB
	mac.Write(symAddr(t, mac, "BD_WRITE_BUF"), wantData)

	// Write track=3, sector=5 → linear sector = 3*10 + (5-1) = 34.
	const wantTrack = 3
	const wantSector = 5
	const wantLinear = wantTrack*10 + (wantSector - 1) // 34
	mac.Write(symAddr(t, mac, "BD_WRITE_TRACK"), []byte{wantTrack})
	mac.Write(symAddr(t, mac, "BD_WRITE_SECTOR"), []byte{wantSector})

	if _, err := mac.CallEntry("bdos_write_sector", z80h.Entry{}); err != nil {
		t.Fatalf("bdos_write_sector: %v", err)
	}

	// Verify the store captured exactly one write at the right coordinates.
	writes := store.SectorWrites()
	if len(writes) != 1 {
		t.Fatalf("SectorWrites() = %d entries, want 1", len(writes))
	}
	w := writes[0]
	if w.Record != record {
		t.Errorf("write record = %d, want %d", w.Record, record)
	}
	if w.LinearSec != wantLinear {
		t.Errorf("write linearSec = %d, want %d", w.LinearSec, wantLinear)
	}
	if !bytes.Equal(w.Data[:], wantData) {
		t.Errorf("write data mismatch (first byte: got %#x, want %#x)", w.Data[0], wantData[0])
	}

	// Read the sector back via HRSAD and confirm round-trip identity.
	mac.Write(symAddr(t, mac, "BD_READ_TRACK"), []byte{wantTrack})
	mac.Write(symAddr(t, mac, "BD_READ_SECTOR"), []byte{wantSector})
	if _, err := mac.CallEntry("bdos_read_sector", z80h.Entry{}); err != nil {
		t.Fatalf("bdos_read_sector: %v", err)
	}
	readBack := mac.Read(symAddr(t, mac, "BD_READ_BUF"), 512)
	if !bytes.Equal(readBack, wantData) {
		t.Errorf("round-trip mismatch: read back first byte %#x, want %#x", readBack[0], wantData[0])
	}
}

// TestBDOSWriteRecord verifies bdos_write_record writes N consecutive sectors
// from BD_WRITE_BUF into the card, starting at BD_WRITE_START, each readable
// back via HRSAD. Uses a 3-sector write spanning a track boundary (sectors 9,
// 10 on track 0 and sector 1 on track 1) to confirm track-carry logic.
func TestBDOSWriteRecord(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	store.AttachCard(card)
	mac.AttachBDOS(store)

	const record = 5
	if _, err := mac.CallEntry("bdos_select_record", z80h.Entry{A: record}); err != nil {
		t.Fatalf("bdos_select_record(%d): %v", record, err)
	}

	// Sector layout: start at linear sector 8 (track=0, sector=9), write 3 sectors.
	//   linear 8  → track 0, sector 9  (0*10 + 8 = 8)
	//   linear 9  → track 0, sector 10 (0*10 + 9 = 9)
	//   linear 10 → track 1, sector 1  (1*10 + 0 = 10)
	const startLinear = 8
	const sectorCount = 3
	mac.WriteU16LE(symAddr(t, mac, "BD_WRITE_START"), startLinear)
	mac.WriteU16LE(symAddr(t, mac, "BD_WRITE_COUNT"), sectorCount)

	// For bdos_write_record, the Z80 writes the same BD_WRITE_BUF content for
	// every sector in the loop (the routine doesn't advance the source pointer —
	// that is the caller's responsibility at the i114c scope; here we use the
	// same buffer for all 3 sectors so the harness writes the same 512 bytes
	// three times at consecutive linear sectors, which is all this primitive
	// test needs to verify).
	sectorData := makeSectorPayload(0xCD)
	mac.Write(symAddr(t, mac, "BD_WRITE_BUF"), sectorData)

	if _, err := mac.CallEntry("bdos_write_record", z80h.Entry{}); err != nil {
		t.Fatalf("bdos_write_record: %v", err)
	}

	// Verify exactly 3 writes were captured at the right linear sectors.
	writes := store.SectorWrites()
	if len(writes) != sectorCount {
		t.Fatalf("SectorWrites() = %d entries, want %d", len(writes), sectorCount)
	}
	for i, w := range writes {
		wantLinear := startLinear + i
		if w.Record != record {
			t.Errorf("write[%d] record = %d, want %d", i, w.Record, record)
		}
		if w.LinearSec != wantLinear {
			t.Errorf("write[%d] linearSec = %d, want %d", i, w.LinearSec, wantLinear)
		}
		if !bytes.Equal(w.Data[:], sectorData) {
			t.Errorf("write[%d] data mismatch (first byte %#x, want %#x)", i, w.Data[0], sectorData[0])
		}
	}

	// Read each sector back via HRSAD and confirm round-trip identity.
	for i := 0; i < sectorCount; i++ {
		linear := startLinear + i
		track := linear / 10
		sector := linear%10 + 1
		mac.Write(symAddr(t, mac, "BD_READ_TRACK"), []byte{byte(track)})
		mac.Write(symAddr(t, mac, "BD_READ_SECTOR"), []byte{byte(sector)})
		if _, err := mac.CallEntry("bdos_read_sector", z80h.Entry{}); err != nil {
			t.Fatalf("bdos_read_sector(linear=%d): %v", linear, err)
		}
		readBack := mac.Read(symAddr(t, mac, "BD_READ_BUF"), 512)
		if !bytes.Equal(readBack, sectorData) {
			t.Errorf("round-trip linear=%d: first byte %#x, want %#x", linear, readBack[0], sectorData[0])
		}
	}
}

// makeSectorPayload returns a 512-byte slice filled with the given byte value.
func makeSectorPayload(fill byte) []byte {
	s := make([]byte, 512)
	for i := range s {
		s[i] = fill
	}
	return s
}

// TestBDOSSaveWriteBackRoundTrips drives the real HSAVE save-hook over a staged
// section-C file and asserts the saved bytes land in the selected record and read
// back via HRSAD — the round-trip content path i144 adds (the write-back into the
// CardModel). Before i144 the store captured the HSAVE UIFA but copied no bytes, so
// a follow-up HRSAD read all-zero; this proves a saved file is now recoverable, the
// prerequisite for the i119e E2E to assert record CONTENTS (not just selection).
func TestBDOSSaveWriteBackRoundTrips(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	store.AttachCard(card)
	mac.AttachBDOS(store)

	const record = 3
	if _, err := mac.CallEntry("bdos_select_record", z80h.Entry{A: record}); err != nil {
		t.Fatalf("bdos_select_record(%d): %v", record, err)
	}

	// Stage a known file in section C. In the flat model &8000 is physical page 3,
	// so BD_SAVE_PAGE=3 / BD_SAVE_ADDR=&8000 names exactly these bytes. 600 bytes =>
	// two sectors, the second part-full (zero-padded) — exercises the layout + pad.
	body := make([]byte, 600)
	for i := range body {
		body[i] = byte(i*3 + 1)
	}
	mac.Write(0x8000, body)

	const name = "cfgfile"
	nameAddr := symAddr(t, mac, "RRQ_FILENAME")
	mac.Write(nameAddr, append([]byte(name), 0))
	mac.WriteU16LE(symAddr(t, mac, "BD_NAME_PTR"), nameAddr)
	mac.Write(symAddr(t, mac, "BD_SAVE_PAGE"), []byte{3})
	mac.WriteU16LE(symAddr(t, mac, "BD_SAVE_ADDR"), 0x8000)
	mac.WriteU16LE(symAddr(t, mac, "BD_SAVE_SIZE"), uint16(len(body)))
	if _, err := mac.CallEntry("bdos_fill_save_uifa", z80h.Entry{}); err != nil {
		t.Fatalf("bdos_fill_save_uifa: %v", err)
	}
	if _, err := mac.CallEntry("bdos_save_hook", z80h.Entry{}); err != nil {
		t.Fatalf("bdos_save_hook: %v", err)
	}

	// The write-back populated the record's sectors: sector 0 = first 512 bytes,
	// sector 1 = the 88-byte tail, zero-padded.
	sec0 := card.Sector(record, 0)
	var want0 [512]byte
	copy(want0[:], body[:512])
	if sec0 != want0 {
		t.Errorf("record %d sector 0 mismatch after HSAVE write-back", record)
	}
	sec1 := card.Sector(record, 1)
	var want1 [512]byte
	copy(want1[:], body[512:])
	if sec1 != want1 {
		t.Errorf("record %d sector 1 mismatch (tail + zero-pad)", record)
	}

	// And it round-trips through the real HRSAD read hook: track 0 / sector 1
	// (linear sector 0) reads the first 512 bytes back into BD_READ_BUF.
	mac.Write(symAddr(t, mac, "BD_READ_TRACK"), []byte{0})
	mac.Write(symAddr(t, mac, "BD_READ_SECTOR"), []byte{1})
	if _, err := mac.CallEntry("bdos_read_sector", z80h.Entry{}); err != nil {
		t.Fatalf("bdos_read_sector: %v", err)
	}
	readBack := mac.Read(symAddr(t, mac, "BD_READ_BUF"), 512)
	if !bytes.Equal(readBack, body[:512]) {
		t.Errorf("HRSAD round-trip mismatch: saved file did not read back (first byte got %#x want %#x)", readBack[0], body[0])
	}
}
