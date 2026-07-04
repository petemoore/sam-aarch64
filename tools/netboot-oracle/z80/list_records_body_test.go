// list_records_body_test.go — i362 record-body READ test (CI-safe;
// SKIP_PRIVATE_TESTS-green).
//
// Drives the real list_records binary (build/list_records.bin) end-to-end under the
// flat-memory harness, exercising the 'S' command added in i362: a record-body sector
// read (CMD17 at csd_base+1600*(record-1)+relSector, bd_record_read_hw). It seeds
// known 512-byte DATA sectors into a record's disk-body image on the SD model, drives
// '?'/'S'/'Q', and asserts the returned bytes equal the seeded bytes at that absolute
// LBA — the confirmation channel the i70b HTTP-store smoke build needs (that build
// writes a record's BODY but omits the record-LIST claim, so 'L'/list_records reports
// the record FREE and cannot see the store; 'S' reads the stored bytes directly).
//
// It also proves the build stays READ-ONLY: the SD model captures ZERO writes (the
// binary carries no NETBOOT_WANT_CLAIM / CMD24 write path — only the CMD17 record
// read). What it does NOT prove (hardware-gated, CLAUDE.md §5): the same CMD17s
// against real Trinity silicon. Emulation-verified is not hardware-verified.
package z80_test

import (
	"bytes"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// lrSectQuery builds an 'S' record-body query frame: [record LE16][relSector LE16].
func lrSectQuery(record, relSector uint16) []byte {
	return sdPushFrame([]byte{'S',
		byte(record), byte(record >> 8),
		byte(relSector), byte(relSector >> 8)})
}

// lrBodyPattern builds a distinctive 512-byte record-body sector: a per-(rec,rel)
// ramp plus, when marked, the "BDOS" catalog stamp at +232 and a "LICENCE" filename
// substring — so the returned bytes are unambiguous AND the launcher's stamp/name scan
// has something real to find in the harness image.
func lrBodyPattern(rec, rel byte, bdos bool) []byte {
	s := make([]byte, 512)
	for i := range s {
		s[i] = byte(i) ^ rec ^ (rel << 4)
	}
	if bdos {
		copy(s[232:], []byte("BDOS"))
		copy(s[16:], []byte("LICENCE")) // a B-DOS dir entry's name field (illustrative)
	}
	return s
}

// TestListRecordsBody drives the 'S' record-body read: seeded record-body sectors come
// back byte-exact ("s"+record+relSector+512B), an unseeded body sector reads as zeros
// (valid, not refused), out-of-range record/relSector are refused with 'E', and the
// card sees zero writes.
func TestListRecordsBody(t *testing.T) {
	if _, err := os.Stat(listRecordsBin); err != nil {
		t.Fatalf("list_records binary not built (%s); run `make netboot-list-records`", listRecordsBin)
	}
	mac, err := z80h.Load(listRecordsBin, listRecordsMap)
	if err != nil {
		t.Fatalf("load list_records: %v", err)
	}
	if _, err := mac.Sym("bd_record_read_hw"); err != nil {
		t.Fatalf("bd_record_read_hw symbol absent from %s — the i362 record-body read is not in this build", listRecordsMap)
	}

	// CSDForV2(0x001D59): the ~3.7 GB card the guard/list tests use — csd_base = 152,
	// records = 4809. base is the first record-data sector; a record's body sector LBA
	// is base + 1600*(record-1) + relSector (bd_record_lba_compute / RecordDataSector).
	base, records := refRecords(spCSDBlocks)
	if int(base) != spCSDBase || int(records) != lrRecords {
		t.Fatalf("test precondition drift: refRecords(%d) = base %d, records %d; want base %d, records %d",
			spCSDBlocks, base, records, spCSDBase, lrRecords)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(spSAMMac, spSAMIp)
	sd := enc.AttachSD(z80h.CSDForV2(0x001D59))
	mac.AttachIO(enc)
	mac.AttachPrintRecorder()
	mac.StubReturn(0x06B5) // CLSLOWER — hardware-only screen clear (i319a)

	// Seed known body sectors: record 2 sector 0 (with a BDOS stamp + LICENCE name),
	// record 2 sector 5, record 1 sector 0. Record 3 sector 0 stays unseeded (reads
	// back as 512 zeros — a valid, empty record body, not a refusal).
	type seed struct {
		rec, rel uint16
		pat      []byte
	}
	seeds := []seed{
		{2, 0, lrBodyPattern(2, 0, true)},
		{2, 5, lrBodyPattern(2, 5, false)},
		{1, 0, lrBodyPattern(1, 0, false)},
	}
	lba := func(rec, rel uint16) uint32 {
		return base + 1600*(uint32(rec)-1) + uint32(rel)
	}
	for _, s := range seeds {
		sd.SeedSector(lba(s.rec, s.rel), s.pat)
	}

	// Queue: discovery, three seeded reads, one unseeded (zeros), then three refusals
	// (record 0; record > BD_RECORDS; relSector == 1600), then quit.
	enc.InjectRX(sdPushFrame([]byte{'?'}))
	for _, s := range seeds {
		enc.InjectRX(lrSectQuery(s.rec, s.rel))
	}
	enc.InjectRX(lrSectQuery(3, 0))                    // unseeded body: zeros, valid
	enc.InjectRX(lrSectQuery(0, 0))                    // record 0: refused
	enc.InjectRX(lrSectQuery(uint16(records)+1, 0))    // record past the card: refused
	enc.InjectRX(lrSectQuery(2, 1600))                 // relSector 1600 (>= 1600): refused
	enc.InjectRX(sdPushFrame([]byte{'Q'}))

	if _, err := mac.RunBoot("list_records_main", z80h.Entry{StepCap: 30_000_000}); err != nil {
		t.Fatalf("RunBoot list_records_main faulted: %v", err)
	}
	frames := enc.TXFrames()

	// Sanity: the asm decoded csd_base to the value we seeded against (else a seed
	// would land at the wrong LBA and every read below would read zeros).
	if got := int(leU16(mac.Read(sym(t, mac, "csd_base"), 2))); got != int(base) {
		t.Fatalf("asm csd_base = %d, want %d (the seed LBAs assume this base)", got, base)
	}

	// (1) Seeded body sectors come back byte-exact: "s" + record + relSector + 512B.
	for _, s := range seeds {
		reply := findPayloadPrefix(frames, []byte{'s',
			byte(s.rec), byte(s.rec >> 8), byte(s.rel), byte(s.rel >> 8)})
		if reply == nil {
			t.Errorf("no 's' record-body reply for record %d sector %d; tx payloads=%v", s.rec, s.rel, txPayloads(frames))
			continue
		}
		if len(reply) != 5+512 {
			t.Errorf("record %d sector %d reply is %d bytes, want %d", s.rec, s.rel, len(reply), 5+512)
			continue
		}
		if !bytes.Equal(reply[5:], s.pat) {
			t.Errorf("record %d sector %d body bytes differ from the seeded pattern", s.rec, s.rel)
		}
		// Cross-check against the model's own record-geometry accessor.
		if want, ok := sd.RecordDataSector(base, int(s.rec), int(s.rel)); !ok || !bytes.Equal(reply[5:], want) {
			t.Errorf("record %d sector %d body diverges from the model's RecordDataSector view", s.rec, s.rel)
		}
	}

	// (2) An unseeded body sector is a VALID read of 512 zeros — not a refusal.
	if reply := findPayloadPrefix(frames, []byte{'s', 3, 0, 0, 0}); reply == nil || len(reply) != 5+512 {
		t.Errorf("unseeded record 3 sector 0 did not serve an 's'+512B reply; got %v", reply)
	} else if !bytes.Equal(reply[5:], make([]byte, 512)) {
		t.Errorf("unseeded record 3 sector 0 body is not all zeros")
	}

	// (3) Out-of-range queries are refused with 'E' + the echoed record + relSector.
	for _, bad := range []struct{ rec, rel uint16 }{
		{0, 0}, {uint16(records) + 1, 0}, {2, 1600},
	} {
		want := []byte{'E', byte(bad.rec), byte(bad.rec >> 8), byte(bad.rel), byte(bad.rel >> 8)}
		if got := countPayload(frames, want); got < 1 {
			t.Errorf("no 'E' refusal for out-of-range record %d sector %d", bad.rec, bad.rel)
		}
	}

	// (4) DATA SAFETY — the record-body read is CMD17 only: the SD model saw ZERO
	// writes across the whole session.
	if writes := sd.WrittenSectors(); len(writes) != 0 {
		t.Fatalf("READ-ONLY VIOLATION: list_records issued %d SD write(s) at %v — the 'S' read must never write", len(writes), writes)
	}
}
