// delete_record_test.go — i317: emulation verification of the trinload-pushable
// "free/delete Trinity SD record N" program (src/netboot/delete_record.asm).
//
// delete_record is the store/boot/DELETE toolkit counterpart to sd_push (i293, push a
// disk into a record) and boot_record (i316, boot a record): it frees a named record
// by clearing its central record-LIST name entry so the slot reads as unnamed/reusable
// and the next push lands there. It is a thin wrapper over bdos_free_record (the i317
// primitive — the exact inverse of bdos_claim_record): read the host-chosen record from
// its DEL_CONFIG block, init the card (csd_set_bd_records: read the CSD for LBA
// addressing + the record count), range-check, then RMW the record list — real CMD17
// read, ZERO this record's 16-byte entry, real CMD24 write-back.
//
// This test loads the REAL built binary, attaches the SD-SPI model (sdcard.go — the same
// real CMD9/CMD17/CMD24 model sd_push and sd_listread drive), seeds a record-list sector
// with some NAMED records, patches DEL_CFG_RECORD to one of them, runs
// delete_record_main end-to-end, and asserts the card's captured list sector now reads
// that record FREE while every neighbour entry is byte-for-byte intact, and that exactly
// one CMD24 landed (to the list sector — data-safety: nothing else touched).
//
// THE HONESTY LINE (CLAUDE.md §5): the CMD17/CMD24 run against the SD MODEL, not real
// Trinity silicon; the actual on-hardware free is a SEPARATE follow-up (Pete-gated, i295
// family). Emulation-verified is not hardware-verified.
package z80_test

import (
	"os"
	"strconv"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	deleteRecordBin = "../../../build/delete_record.bin"
	deleteRecordMap = "../../../build/delete_record.map"

	// delCfgMagic mirrors DEL_CFG_MAGIC_VAL in src/netboot/delete_record.asm — the
	// magic byte at DEL_CONFIG+0 the host launcher sanity-checks before patching
	// DEL_CFG_RECORD. Asserted here so a binary/layout drift (the config block moving
	// or losing its anchor) fails the test rather than silently breaking the patcher.
	delCfgMagic = 0x5A

	// A small SDHC/v2 card so the computed record count is < 256 (a 1-byte config can
	// then name an out-of-range record for the data-safety guard test). csdV2(7) →
	// (7+1)*1024 = 8192 blocks → a handful of 1600-sector records (see delRecordCount).
	delSmallCSD = 7
	// A large SDHC/v2 card (~3.7 GB, thousands of records) so a cross-list-sector record
	// (33, which lives in list sector 2) is in range — proving the program handles the
	// multi-list-sector geometry a real card has.
	delBigCSD = 0x001D59
)

// loadDeleteRecord loads the built delete_record binary under the flat harness, failing
// (never skipping — i253) if it is not built.
func loadDeleteRecord(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(deleteRecordBin); err != nil {
		t.Fatalf("delete_record binary not built (%s); run `make netboot-delete-record`", deleteRecordBin)
	}
	mac, err := z80h.Load(deleteRecordBin, deleteRecordMap)
	if err != nil {
		t.Fatalf("load delete_record: %v", err)
	}
	if _, err := mac.Sym("delete_record_main"); err != nil {
		t.Fatalf("delete_record_main symbol absent from %s — wrong build?", deleteRecordMap)
	}
	return mac
}

// setupDeleteRecord loads delete_record and attaches an ENC28J60 hosting the SD-SPI card
// model with the given CSD (the state a trinload-pushed delete_record runs in, minus real
// hardware). It also checks the DEL_CONFIG magic anchor (the host patcher's sanity byte).
// It returns the machine + the SD card so the caller seeds the list sector and asserts.
func setupDeleteRecord(t *testing.T, cSize uint32) (*z80h.Machine, *z80h.SDCard) {
	t.Helper()
	mac := loadDeleteRecord(t)
	if got := mac.Read(symAddr(t, mac, "DEL_CONFIG"), 1)[0]; got != delCfgMagic {
		t.Fatalf("DEL_CONFIG magic = %#x, want %#x (config-block layout drifted; the host patcher would fail its sanity check)", got, delCfgMagic)
	}
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csdV2(cSize))
	mac.AttachIO(enc)
	return mac, sd
}

// runDeleteRecordMain patches DEL_CFG_RECORD to `record` and runs delete_record_main
// end-to-end (which reads the CSD, range-checks, and — if in range — frees the record via
// the real CMD17/CMD24 list RMW).
func runDeleteRecordMain(t *testing.T, mac *z80h.Machine, record byte) {
	t.Helper()
	mac.Write(symAddr(t, mac, "DEL_CFG_RECORD"), []byte{record})
	if _, err := mac.Call("delete_record_main"); err != nil {
		t.Fatalf("call delete_record_main (record %d): %v", record, err)
	}
}

// delRecordCount reads BD_RECORDS after a run — the record count delete_record_main
// computed from the CSD. Used to pin the small card's size and to prove the out-of-range
// guard test really names a record beyond the card.
func delRecordCount(t *testing.T, mac *z80h.Machine) int {
	t.Helper()
	b := mac.Read(symAddr(t, mac, "BD_RECORDS"), 2)
	return int(b[0]) | int(b[1])<<8
}

// TestDeleteRecordFreesEntry is the core i317 gate: a card with records 1, 2, 3 named;
// a host names record 2 (via the patched DEL_CFG_RECORD byte); delete_record_main must
// clear record 2's 16-byte list entry (so it reads FREE) while leaving records 1 and 3
// — and every other byte of the list sector — byte-for-byte intact, and must issue
// exactly one CMD24 (to the list sector). This proves the config-read + init + RMW
// plumbing frees the right record, data-safely.
func TestDeleteRecordFreesEntry(t *testing.T) {
	mac, sd := setupDeleteRecord(t, delSmallCSD)

	// Seed list sector 1 (LBA 1: records 1..32) with three named records. The RMW reads
	// this via CMD17 and writes the modified sector back via CMD24 to the same LBA.
	card := z80h.NewCardModel()
	card.SetRecordEntry(1, makeEntry("ALPHA"))
	card.SetRecordEntry(2, makeEntry("BETA"))
	card.SetRecordEntry(3, makeEntry("GAMMA"))
	seeded := card.ListSector(1)
	sd.SeedSector(1, seeded[:])

	const target = 2
	runDeleteRecordMain(t, mac, target)

	// The card must be big enough that record 2 is in range (else the guard would bail
	// and nothing would change — a false pass).
	if recs := delRecordCount(t, mac); recs < target {
		t.Fatalf("BD_RECORDS = %d, need >= %d (small card too small; pick a bigger delSmallCSD)", recs, target)
	}

	// Exactly one CMD24 write, to the list sector (LBA 1) — nothing else on the card was
	// touched (the data-safety invariant: only the record's own list entry is written,
	// and it lives in list sector 1).
	if writes := sd.WrittenSectors(); len(writes) != 1 || writes[0] != 1 {
		t.Fatalf("CMD24 writes = %v, want exactly [1] (one list-sector write, no stray writes)", writes)
	}

	got, ok := sd.CapturedSector(1)
	if !ok {
		t.Fatal("no captured sector at LBA 1 after the free (the CMD24 write-back did not land)")
	}

	// Record 2 now reads FREE ((entry[0] & 0x7F) == 0).
	if e := sdSlotEntry(sd, 1, target); e[0]&0x7F != 0 {
		t.Errorf("record %d still reads NAMED after the free (entry[0]=%#x), want FREE", target, e[0])
	}
	// Its whole 16-byte entry is zeroed (a clean delete, no stale name bytes).
	for i, b := range got[(target-1)*16 : target*16] {
		if b != 0 {
			t.Errorf("record %d entry byte %d = %#x, want 0 (whole entry must be cleared)", target, i, b)
		}
	}
	// Every byte OUTSIDE record 2's 16-byte entry is byte-for-byte unchanged — records 1
	// and 3 (and all padding) survive the RMW untouched. This is the safety invariant.
	for i := 0; i < 512; i++ {
		if i >= (target-1)*16 && i < target*16 {
			continue // the deliberately-zeroed target entry
		}
		if got[i] != seeded[i] {
			t.Fatalf("list-sector byte %d changed by the free: got %#x, want %#x (a neighbour entry was corrupted)", i, got[i], seeded[i])
		}
	}
	// Belt-and-braces on the named neighbours specifically.
	if n := sdSlotName(sd, 1, 1); n != "ALPHA" {
		t.Errorf("record 1 name after freeing record 2 = %q, want %q", n, "ALPHA")
	}
	if n := sdSlotName(sd, 1, 3); n != "GAMMA" {
		t.Errorf("record 3 name after freeing record 2 = %q, want %q", n, "GAMMA")
	}
}

// TestDeleteRecordDefaultIsRecordOne verifies the baked default: an UN-patched binary
// (DEL_CFG_RECORD left at its assembled value) frees record 1. This pins the default the
// host launcher relies on when no record is specified, and guards the config block's
// default byte from silent drift.
func TestDeleteRecordDefaultIsRecordOne(t *testing.T) {
	mac, sd := setupDeleteRecord(t, delSmallCSD)

	// Do NOT patch DEL_CFG_RECORD — assert the shipped default, then run untouched.
	if def := mac.Read(symAddr(t, mac, "DEL_CFG_RECORD"), 1)[0]; def != 1 {
		t.Fatalf("baked DEL_CFG_RECORD default = %d, want 1 (the un-patched default the host relies on)", def)
	}

	card := z80h.NewCardModel()
	card.SetRecordEntry(1, makeEntry("FIRST"))
	card.SetRecordEntry(2, makeEntry("SECOND"))
	sd.SeedSector(1, sliceOf(card.ListSector(1)))

	if _, err := mac.Call("delete_record_main"); err != nil {
		t.Fatalf("call delete_record_main (default): %v", err)
	}

	if writes := sd.WrittenSectors(); len(writes) != 1 || writes[0] != 1 {
		t.Fatalf("CMD24 writes = %v, want [1] (one list-sector write freeing the default record)", writes)
	}
	if e := sdSlotEntry(sd, 1, 1); e[0]&0x7F != 0 {
		t.Errorf("default record 1 still reads NAMED after the free (entry[0]=%#x)", e[0])
	}
	if n := sdSlotName(sd, 1, 2); n != "SECOND" {
		t.Errorf("record 2 name changed by freeing the default record 1 = %q, want %q", n, "SECOND")
	}
}

// TestDeleteRecordRefusesOutOfRange is the data-safety guard: a record number the card
// does not have (beyond BD_RECORDS) and record 0 (the floppy, which has no list entry)
// must both be REFUSED — delete_record_main exits without any CMD24 write, so no stray
// LBA is ever addressed by a typo'd number.
func TestDeleteRecordRefusesOutOfRange(t *testing.T) {
	// First learn how many records the small card actually has, so the out-of-range
	// probe is provably beyond it (and still fits in the 1-byte config).
	probe, _ := setupDeleteRecord(t, delSmallCSD)
	runDeleteRecordMain(t, probe, 1) // any run populates BD_RECORDS from the CSD
	recs := delRecordCount(t, probe)
	if recs <= 0 || recs >= 255 {
		t.Fatalf("small card record count = %d; need 1..254 so a 1-byte config can name an out-of-range record", recs)
	}

	// record 0 = the floppy (no list entry); recs+1 = one past the last real record.
	for _, bad := range []byte{0, byte(recs + 1)} {
		bad := bad
		t.Run(strconv.Itoa(int(bad)), func(t *testing.T) {
			mac, sd := setupDeleteRecord(t, delSmallCSD)
			// Seed a named record 1 so a wrongful write would be observable.
			card := z80h.NewCardModel()
			card.SetRecordEntry(1, makeEntry("KEEP"))
			sd.SeedSector(1, sliceOf(card.ListSector(1)))

			runDeleteRecordMain(t, mac, bad)

			if writes := sd.WrittenSectors(); len(writes) != 0 {
				t.Fatalf("out-of-range record %d caused CMD24 writes %v, want none (the guard must refuse it)", bad, writes)
			}
			// The seeded record 1 is untouched (the guard bailed before any RMW).
			if n := sdSlotName(sd, 1, 1); n != "KEEP" {
				t.Errorf("record 1 changed by a refused free of record %d: name = %q, want %q", bad, n, "KEEP")
			}
		})
	}
}

// TestDeleteRecordCrossListSector proves the program handles a record whose list entry
// lives beyond the first list sector: record 33 is entry 0 of list sector 2 (LBA 2,
// (33-1)/32+1 = 2). On a large card it is in range; the free must read LBA 2, zero record
// 33's entry, and write LBA 2 back — never touching list sector 1.
func TestDeleteRecordCrossListSector(t *testing.T) {
	mac, sd := setupDeleteRecord(t, delBigCSD)

	// Seed list sector 2 (records 33..64) with record 33 named.
	card := z80h.NewCardModel()
	card.SetRecordEntry(33, makeEntry("HIREC"))
	card.SetRecordEntry(34, makeEntry("NEXT"))
	seeded := card.ListSector(2)
	sd.SeedSector(2, seeded[:])

	const target = 33
	runDeleteRecordMain(t, mac, target)

	if recs := delRecordCount(t, mac); recs < target {
		t.Fatalf("BD_RECORDS = %d, need >= %d (big card too small for the cross-sector test)", recs, target)
	}
	// Exactly one CMD24, to list sector 2 (LBA 2) — list sector 1 is never written.
	if writes := sd.WrittenSectors(); len(writes) != 1 || writes[0] != 2 {
		t.Fatalf("CMD24 writes = %v, want [2] (one write to list sector 2, none to sector 1)", writes)
	}
	got, ok := sd.CapturedSector(2)
	if !ok {
		t.Fatal("no captured sector at LBA 2 after the free (the CMD24 write-back did not land)")
	}
	// Record n's entry within its list sector is at ((n-1) mod 32)*16 (each sector holds
	// 32 × 16-byte entries) — the shared sdSlotEntry helper only handles list sector 1,
	// so compute the in-sector offset directly here.
	entryOff := ((target - 1) % 32) * 16
	if got[entryOff]&0x7F != 0 {
		t.Errorf("record %d still reads NAMED after the free (entry[0]=%#x)", target, got[entryOff])
	}
	// Record 34 (entry 1 of sector 2) is untouched.
	nextOff := ((34 - 1) % 32) * 16
	if got[nextOff]&0x7F == 0 {
		t.Errorf("record 34 was corrupted to FREE by freeing record 33 (entry[0]=%#x)", got[nextOff])
	}
	if got[nextOff] != seeded[nextOff] {
		t.Errorf("record 34 entry changed by freeing record 33: got %#x, want %#x", got[nextOff], seeded[nextOff])
	}
}

// sliceOf returns a heap slice over a copy of a [512]byte, for SeedSector calls that take
// a []byte (SeedSector copies, so a value-array copy is safe to pass by slice).
func sliceOf(a [512]byte) []byte {
	b := make([]byte, len(a))
	copy(b, a[:])
	return b
}
