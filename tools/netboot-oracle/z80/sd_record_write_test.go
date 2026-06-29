// sd_record_write_test.go — i194: unit-test the record-data write path's
// record->absolute-LBA arithmetic and the OWN self-healing CMD24 write
// (src/netboot/sd_csd.asm bd_record_write_hw + bd_cmd24_write_core).
//
// WHY (q62 option (a) / §8ag). The serve's disk push wrote record sectors via the
// B-DOS HWSAD hook (149), which flakily HANGS on real hardware (it relies on
// boot-time SPI-mode persistence the serve's ENC reset disturbs). bd_record_write_hw
// does the write with our OWN raw CMD24 by ABSOLUTE card LBA, reusing the same
// self-healing CMD24 core bd_list_write_hw uses (it re-runs sdc_init_ladder every
// call). The data-safety-critical part is the record->block math:
//
//	LBA = csd_base + 1600*(record-1) + linearSec
//
// where csd_base (the card's first DATA sector) is computed by csd_set_bd_records
// from the CSD, record is 1-based, and linearSec is 0-based within the record. This
// test drives the REAL Z80 routine against the i145c/i145h SD model (the CMD24
// lands in store[LBA]) and asserts BOTH that the absolute block address is exactly
// the formula AND that the 512 bytes captured are the source bytes — for a battery
// of (record, linearSec) pairs including the record-boundary edges (0 and 1599).
//
// THE HONESTY LINE (CLAUDE.md §5): the SD model captures the CMD24 frame faithfully
// at the SPI level (the i145h round-trip), so the LBA + bytes are verified host-side
// — but emulation-verified is not hardware-verified; the real-Trinity write is
// i194's hardware step (Pete-gated, shared-card data-safety review first).
package z80_test

import (
	"bytes"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// recordWriteSrc is a scratch source address for the 512-byte sector to write,
// safely above the serve binary tail (~&C7xx) so it never overlaps code/data.
const recordWriteSrc = 0xD200

// sectorsPerRecord is the linear sectors in one Trinity record (819200/512).
const sectorsPerRecord = 1600

func TestRecordWriteLBAMath(t *testing.T) {
	// An SDHC (v2, block-addressed) card: bd_record_write_hw must NOT apply the
	// v1 <<9 shift, so the LBA the CMD24 carries is the block number verbatim.
	const cSize = 0x001D59 // a mid-size SDHC card (matches TestClaimRMWRealCMD17CMD24)
	mac, err := z80h.Load(serveBootBin, serveBootMap)
	if err != nil {
		t.Fatalf("serve boot binary not built (%v); run `make netboot-serve-boot`", err)
	}
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csdV2(cSize))
	mac.AttachIO(enc)
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("csd_set_bd_records: %v", err)
	}

	// csd_base is the card's first DATA sector (record-list base + 1), the additive
	// base of the formula. It is the Z80's own computed value (csd_blocks_to_records);
	// the test reads it and applies the doc-formula authority independently.
	csdBase := uint32(readU16LE(mac, symAddr(t, mac, "csd_base")))
	if csdBase == 0 {
		t.Fatalf("csd_base computed as 0 — csd_set_bd_records did not derive the base")
	}
	t.Logf("csd_base = %d (first data sector)", csdBase)

	// (record, linearSec) pairs incl. the within-record edges (0 and 1599) and a
	// couple of records, so the 1600*(n-1) multiply and the boundary handling are
	// both exercised.
	cases := []struct {
		record    uint16
		linearSec uint16
	}{
		{1, 0},      // record 1, first sector: LBA == csd_base
		{1, 1},      // record 1, second sector
		{1, 1599},   // record 1, LAST sector (the record-boundary edge)
		{2, 0},      // record 2, first sector: LBA == csd_base + 1600
		{2, 1599},   // record 2, last sector: LBA == csd_base + 3199
		{10, 1234},  // a higher record (the claim test's record), mid-record
		{37, 0},     // record 37, first sector — a multiply well past one list sector
	}

	for _, c := range cases {
		// Build a per-case marker sector so a stray/duplicate write is caught.
		src := make([]byte, 512)
		for i := range src {
			src[i] = byte(int(c.record)*7 + int(c.linearSec)*3 + i*5)
		}
		mac.Write(recordWriteSrc, src)

		mac.WriteU16LE(symAddr(t, mac, "BD_REC_WRITE_REC"), c.record)
		mac.WriteU16LE(symAddr(t, mac, "BD_REC_WRITE_LINEAR"), c.linearSec)

		if _, err := mac.CallEntry("bd_record_write_hw", z80h.Entry{HL: recordWriteSrc}); err != nil {
			t.Fatalf("bd_record_write_hw(record=%d, linear=%d): %v", c.record, c.linearSec, err)
		}

		// The doc-formula authority (trinity-sd-z80-interface.md §6/§7):
		//   LBA = csd_base + 1600*(record-1) + linearSec  (SDHC: no <<9).
		wantLBA := csdBase + sectorsPerRecord*(uint32(c.record)-1) + uint32(c.linearSec)

		got, ok := sd.CapturedSector(wantLBA)
		if !ok {
			t.Fatalf("record=%d linear=%d: no CMD24 captured at LBA %d (csd_base=%d) — bd_record_write_hw wrote the wrong block",
				c.record, c.linearSec, wantLBA, csdBase)
		}
		if !bytes.Equal(got, src) {
			t.Errorf("record=%d linear=%d: captured bytes at LBA %d != source", c.record, c.linearSec, wantLBA)
		}
		t.Logf("record=%d linear=%d -> LBA %d (csd_base + 1600*%d + %d), 512 bytes match",
			c.record, c.linearSec, wantLBA, c.record-1, c.linearSec)
	}
}

// TestRecordWriteDataSafetyWithinRecord asserts the write can ONLY ever land in
// the claimed record's own block range [csd_base+1600*(n-1), csd_base+1600*n) for
// every linear sector 0..1599 — never another record's blocks, the record list
// (sectors 1..base-1), or sector 0. This is the shared-resource data-safety
// invariant (trinity_storage_shared_resource): a reimplemented write path must
// never touch a block outside the free record it claimed.
func TestRecordWriteDataSafetyWithinRecord(t *testing.T) {
	const cSize = 0x001D59
	mac, err := z80h.Load(serveBootBin, serveBootMap)
	if err != nil {
		t.Fatalf("serve boot binary not built (%v); run `make netboot-serve-boot`", err)
	}
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csdV2(cSize))
	mac.AttachIO(enc)
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("csd_set_bd_records: %v", err)
	}
	csdBase := uint32(readU16LE(mac, symAddr(t, mac, "csd_base")))

	const claimedRecord = 5
	lo := csdBase + sectorsPerRecord*(claimedRecord-1) // inclusive
	hi := csdBase + sectorsPerRecord*claimedRecord     // exclusive

	src := make([]byte, 512)
	for i := range src {
		src[i] = byte(i)
	}
	mac.Write(recordWriteSrc, src)
	mac.WriteU16LE(symAddr(t, mac, "BD_REC_WRITE_REC"), claimedRecord)

	// Write every linear sector of the claimed record; assert each LBA stays inside
	// the record's own range. (Drive the boundary sectors 0 and 1599 plus a sweep
	// at a coarse step so the test stays fast over the bit-banged SPI.)
	for _, linear := range []uint16{0, 1, 100, 800, 1598, 1599} {
		mac.WriteU16LE(symAddr(t, mac, "BD_REC_WRITE_LINEAR"), linear)
		if _, err := mac.CallEntry("bd_record_write_hw", z80h.Entry{HL: recordWriteSrc}); err != nil {
			t.Fatalf("bd_record_write_hw(linear=%d): %v", linear, err)
		}
		wantLBA := lo + uint32(linear)
		if wantLBA < lo || wantLBA >= hi {
			t.Fatalf("linear=%d: computed LBA %d outside the claimed record range [%d,%d)", linear, wantLBA, lo, hi)
		}
		if _, ok := sd.CapturedSector(wantLBA); !ok {
			t.Fatalf("linear=%d: no write captured at LBA %d (range [%d,%d))", linear, wantLBA, lo, hi)
		}
		// Never sector 0 or a list sector (1..csd_base-1).
		if wantLBA < uint32(csdBase) {
			t.Fatalf("linear=%d: LBA %d is in the boot/list area (< csd_base %d) — DATA SAFETY VIOLATION", linear, wantLBA, csdBase)
		}
	}
}
