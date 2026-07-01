// sd_record_write_guard_test.go — i295 DEFENSIVE DATA-SAFETY guard on the raw-LBA
// record write (bd_record_write_hw). Pete's SD card is a shared user resource; a raw
// absolute CMD24 to the wrong LBA silently corrupts the record LIST, sector 0, or a
// neighbouring record. This test proves the guard (bd_record_lba_in_band) REFUSES any
// write whose final LBA is not provably inside the claimed record's own body sectors
// AND on the card — a valid write commits, an out-of-band one does NOT touch the card.
//
// It Loads the standalone CSD fixture (which includes sd_csd.asm, so bd_record_write_hw
// / csd_base / csd_blocks / bd_rec_guard_tripped are all exported symbols), sets the
// geometry directly, attaches the SD model, and CALLs bd_record_write_hw by symbol.
package z80_test

import (
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// recordWriteGuardMachine loads the CSD fixture, sets csd_base + csd_blocks directly,
// attaches the SD model for the given CSD, and returns the machine + SD card.
func recordWriteGuardMachine(t *testing.T, csd [16]byte, base, blocks uint32) (*z80h.Machine, *z80h.SDCard) {
	t.Helper()
	if _, err := os.Stat(sdCSDBin); err != nil {
		t.Fatalf("sd_csd fixture not built (%s); run `make netboot-sd-csd`", sdCSDBin)
	}
	mac, err := z80h.Load(sdCSDBin, sdCSDMap)
	if err != nil {
		t.Fatalf("load sd_csd fixture: %v", err)
	}
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csd)
	mac.AttachIO(enc)
	// Set the geometry the guard validates against directly (bypassing the CSD read —
	// this test drives the write path, not the decode). csd_blocks = card capacity.
	mac.WriteU16LE(symAddr(t, mac, "csd_base"), uint16(base))
	setU32(mac, symAddr(t, mac, "csd_blocks"), blocks)
	// The write path reads CSD_STAGE[0] to pick block- vs byte-addressing
	// (bd_lba_apply_v1_shift): mark it v2/SDHC (bit7:6 == 01) so the LBA is used
	// verbatim (no <<9), matching the SDHC card under test. Without this the stage is
	// zero -> mis-detected as SDv1 -> the LBA is shifted, corrupting the test.
	mac.Write(symAddr(t, mac, "CSD_STAGE"), []byte{0x40})
	return mac, sd
}

// callRecordWrite sets BD_REC_WRITE_REC / BD_REC_WRITE_LINEAR, points a source buffer
// at a scratch address, zeroes the guard flag, CALLs bd_record_write_hw, and returns
// (committed writes, guardTripped).
func callRecordWrite(t *testing.T, mac *z80h.Machine, sd *z80h.SDCard, rec, linear uint16) (writes []uint32, tripped bool) {
	t.Helper()
	mac.Write(symAddr(t, mac, "bd_rec_guard_tripped"), []byte{0}) // clear the sticky flag (1 byte — it abuts BD_REC_WRITE_REC)
	mac.WriteU16LE(symAddr(t, mac, "BD_REC_WRITE_REC"), rec)
	mac.WriteU16LE(symAddr(t, mac, "BD_REC_WRITE_LINEAR"), linear)
	// A 512-byte source buffer of recognisable data at a safe scratch address.
	const src = 0x9000
	buf := make([]byte, 512)
	for i := range buf {
		buf[i] = byte(i)
	}
	mac.Write(src, buf)
	if _, err := mac.CallEntry("bd_record_write_hw", z80h.Entry{HL: src}); err != nil {
		t.Fatalf("call bd_record_write_hw: %v", err)
	}
	g := mac.Read(symAddr(t, mac, "bd_rec_guard_tripped"), 1)
	return sd.WrittenSectors(), g[0] != 0
}

// TestRecordWriteCommitsInBand: a valid (record, linearSec) commits exactly ONE CMD24
// at csd_base + 1600*(record-1) + linearSec, and the guard does not trip.
func TestRecordWriteCommitsInBand(t *testing.T) {
	const base, blocks = 152, 7694336 // the small ~3.7 GB card geometry
	mac, sd := recordWriteGuardMachine(t, csdV2(0x001D59), base, blocks)

	const rec, linear = 3, 5
	writes, tripped := callRecordWrite(t, mac, sd, rec, linear)
	wantLBA := uint32(base) + 1600*(rec-1) + linear
	if tripped {
		t.Fatalf("guard tripped on a VALID in-band write (rec=%d linear=%d LBA=%d)", rec, linear, wantLBA)
	}
	if len(writes) != 1 || writes[0] != wantLBA {
		t.Fatalf("in-band write: committed %v, want exactly [%d]", writes, wantLBA)
	}
	t.Logf("in-band write committed at LBA %d = %d + 1600*(%d-1) + %d (guard OK)", wantLBA, base, rec, linear)
}

// TestRecordWriteRefusesOutOfBand: every out-of-band condition is REFUSED — NO CMD24
// reaches the card, and the sticky guard flag is set. Covers a bad linearSec, a record
// number large enough to overflow past the card capacity, and a wrong (too-high) base
// that pushes the write off the card. Each proves the guard protects Pete's card even
// if the base/record math were ever wrong.
func TestRecordWriteRefusesOutOfBand(t *testing.T) {
	const base, blocks = 152, 7694336

	cases := []struct {
		name         string
		base, blocks uint32
		rec, linear  uint16
	}{
		// linearSec >= 1600 is not a valid within-record sector -> refuse.
		{"linearSec>=1600", base, blocks, 3, 1600},
		{"linearSec huge", base, blocks, 3, 60000},
		// A record number so large that csd_base + 1600*(rec-1) runs off the card
		// (capacity check). blocks=7694336 -> ~4808 full records; rec=4810's band
		// starts at 7694552 > capacity 7694336, and rec=6000 is far past.
		{"record just past last", base, blocks, 4810, 0},
		{"record far past capacity", base, blocks, 6000, 0},
		// The last record (4809) is PARTIAL on this card (band 7692952..7694552, but
		// capacity is 7694336): a within-record sector whose absolute LBA exceeds
		// capacity (linear=1400 -> LBA 7694352 > 7694336) must be refused.
		{"partial-last-record sector off card", base, blocks, 4809, 1400},
		// A DELIBERATELY-WRONG base set beyond the card capacity (csd_base is 16-bit, so
		// use a small-capacity card where a bad base can exceed it): the record-1 write
		// would land off the card -> capacity check refuses (proves a wrong base can't
		// silently corrupt: a base >= capacity can never yield an in-card LBA).
		{"wrong base past capacity", 40000, 30000, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mac, sd := recordWriteGuardMachine(t, csdV2(0x001D59), tc.base, tc.blocks)
			writes, tripped := callRecordWrite(t, mac, sd, tc.rec, tc.linear)
			if len(writes) != 0 {
				t.Fatalf("%s: an out-of-band write COMMITTED to the card at %v — data-safety guard FAILED", tc.name, writes)
			}
			if !tripped {
				t.Fatalf("%s: no CMD24 committed but the guard flag was not set — the refusal path is not the guard", tc.name)
			}
			t.Logf("%s: refused (no CMD24, guard tripped) — Pete's card untouched", tc.name)
		})
	}
}

// setU32 writes a 32-bit LE value to a DOS/RAM address (WriteU16LE only covers 16).
func setU32(mac *z80h.Machine, addr uint16, v uint32) {
	mac.WriteU16LE(addr, uint16(v))
	mac.WriteU16LE(addr+2, uint16(v>>16))
}
