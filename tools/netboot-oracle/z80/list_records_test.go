// list_records_test.go — i322 LOGIC test (CI-safe; SKIP_PRIVATE_TESTS-green).
//
// Runs the real list_records binary (build/list_records.bin) end-to-end under the
// flat-memory harness: the "Trinity Network " EEPROM chunk is served by the ENC
// model, the card's CSD + record-list sectors by the SD-SPI model (real CMD9 +
// CMD17 under NETBOOT_REAL_LISTREAD), and RST &10 by the print recorder. What this
// proves: the '?'/'L'/'Q' wire protocol serves the card's raw list sectors with the
// record count on discovery, range-checks a bad index, exits cleanly on 'Q' — and
// the program is READ-ONLY (the SD model captures ZERO writes; the binary is built
// without NETBOOT_WANT_CLAIM, so the list-write primitives are not even assembled).
// What it does NOT prove (hardware-gated, CLAUDE.md §5): the same CMD17s against
// real Trinity silicon. Emulation-verified is not hardware-verified.
package z80_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	listRecordsBin = "../../../build/list_records.bin"
	listRecordsMap = "../../../build/list_records.map"

	// CSDForV2(0x001D59): records = 4809 (0x12C9) -> ceil(4809/32) = 151 list sectors.
	lrRecords = 4809
	lrNList   = 151
)

// lrEntry builds one 16-byte record-list name entry: the name space-padded to 16,
// bit 7 of byte 0 set when write-protected (trinity-record-detection-design.md §4).
func lrEntry(name string, prot bool) []byte {
	e := bytes.Repeat([]byte{' '}, 16)
	copy(e, name)
	if prot {
		e[0] |= 0x80
	}
	return e
}

// lrQuery builds an 'L' list-sector query frame (listSec LE16).
func lrQuery(listSec uint16) []byte {
	return sdPushFrame([]byte{'L', byte(listSec), byte(listSec >> 8)})
}

// findPayloadPrefix returns the first TX UDP payload starting with prefix, or nil.
func findPayloadPrefix(frames [][]byte, prefix []byte) []byte {
	for _, p := range txPayloads(frames) {
		if bytes.HasPrefix(p, prefix) {
			return p
		}
	}
	return nil
}

// TestListRecords drives list_records end-to-end: discovery carries the record
// count, 'L' serves seeded + unseeded list sectors byte-exact, out-of-range indices
// are refused, 'Q' exits cleanly — and the card sees zero writes.
func TestListRecords(t *testing.T) {
	if _, err := os.Stat(listRecordsBin); err != nil {
		t.Fatalf("list_records binary not built (%s); run `make netboot-list-records`", listRecordsBin)
	}
	mac, err := z80h.Load(listRecordsBin, listRecordsMap)
	if err != nil {
		t.Fatalf("load list_records: %v", err)
	}
	if _, err := mac.Sym("list_records_main"); err != nil {
		t.Fatalf("list_records_main symbol absent from %s — wrong build?", listRecordsMap)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(spSAMMac, spSAMIp) // the EEPROM net-config chunk
	sd := enc.AttachSD(z80h.CSDForV2(0x001D59))  // real CMD9/CMD17 against sdcard.go
	mac.AttachIO(enc)
	rec := mac.AttachPrintRecorder()
	mac.StubReturn(0x06B5) // CLSLOWER — hardware-only screen clear (i319a)

	// Seed the record list: sector 1 holds records 1..32 (record 1 = "alpha",
	// record 3 = "beta" write-protected), sector 3 holds records 65..96 (record 65
	// = "gamma"). Sector 2 stays unseeded (reads as 512 zero bytes = 32 free slots).
	sec1 := make([]byte, 512)
	copy(sec1[0*16:], lrEntry("alpha", false))
	copy(sec1[2*16:], lrEntry("beta", true))
	sd.SeedSector(1, sec1)
	sec3 := make([]byte, 512)
	copy(sec3[0*16:], lrEntry("gamma", false))
	sd.SeedSector(3, sec3)

	// Queue: discovery, the two seeded sectors, the unseeded one, two bad indices
	// (0 = the boot sector; lrNList+1 = past the card's list), then quit.
	enc.InjectRX(sdPushFrame([]byte{'?'}))
	enc.InjectRX(lrQuery(1))
	enc.InjectRX(lrQuery(3))
	enc.InjectRX(lrQuery(2))
	enc.InjectRX(lrQuery(0))
	enc.InjectRX(lrQuery(lrNList + 1))
	enc.InjectRX(sdPushFrame([]byte{'Q'}))

	res, err := mac.RunBoot("list_records_main", z80h.Entry{StepCap: 30_000_000})
	if err != nil {
		t.Fatalf("RunBoot list_records_main faulted: %v", err)
	}
	frames := enc.TXFrames()
	t.Logf("list_records: halted=%v finalPC=&%04X steps=%d tx=%d",
		res.Halted, res.PC, res.Steps, len(frames))

	// (1) Discovery answers "!LR" ('!' + the tool tag, i329 — never trinload's
	// bare '!') + the record count (LE16) so the host knows how many list
	// sectors to query.
	if got := countPayload(frames, []byte{'!', 'L', 'R', byte(lrRecords & 0xFF), byte(lrRecords >> 8)}); got < 1 {
		t.Errorf("no '!LR'+records(%d LE16) discovery reply; tx payloads=%v", lrRecords, txPayloads(frames))
	}
	if got := countPayload(frames, []byte{'!'}); got != 0 {
		t.Errorf("%d BARE '!' discovery replies — that is trinload's signature; list_records must tag its reply '!LR' (i329)", got)
	}

	// (2) The seeded sectors come back byte-exact: 'R' + listSec (LE16) + 512 bytes.
	for _, c := range []struct {
		idx  uint16
		want []byte
	}{{1, sec1}, {3, sec3}, {2, make([]byte, 512)}} {
		reply := findPayloadPrefix(frames, []byte{'R', byte(c.idx), byte(c.idx >> 8)})
		if reply == nil {
			t.Errorf("no 'R' reply for list sector %d; tx payloads have prefixes %v", c.idx, txPayloads(frames))
			continue
		}
		if len(reply) != 3+512 {
			t.Errorf("list sector %d reply is %d bytes, want 515", c.idx, len(reply))
			continue
		}
		if !bytes.Equal(reply[3:], c.want) {
			t.Errorf("list sector %d bytes differ from the seeded/expected sector", c.idx)
		}
	}

	// (3) Out-of-range indices are refused with 'E' + the echoed index — sector 0
	// (the boot sector, not part of the list) and one past the card's list.
	for _, idx := range []uint16{0, lrNList + 1} {
		if got := countPayload(frames, []byte{'E', byte(idx), byte(idx >> 8)}); got < 1 {
			t.Errorf("no 'E' refusal for out-of-range list sector %d", idx)
		}
	}

	// (4) 'Q' is acked with 'q' and the program exits cleanly (RunBoot returned).
	if got := countPayload(frames, []byte{'q'}); got < 1 {
		t.Errorf("no 'q' ack for the quit message; tx payloads=%v", txPayloads(frames))
	}

	// (5) DATA SAFETY — the whole point of the read-only build: the SD model saw
	// ZERO writes across the entire session.
	if writes := sd.WrittenSectors(); len(writes) != 0 {
		t.Fatalf("READ-ONLY VIOLATION: list_records issued %d SD write(s) at %v — it must never write", len(writes), writes)
	}

	// (6) The screen story: banner + the stage markers, and the 'Q' goodbye.
	printed := string(rec.Chars())
	for _, want := range []string{"SD-LIST 1234", "DONE"} {
		if !strings.Contains(printed, want) {
			t.Errorf("screen output missing %q; printed=%q", want, printed)
		}
	}
}

// TestListRecordsBigCardReach — the 16-bit seam (i326): BD_LIST_SECTOR is now two
// bytes, so list_records reaches list sectors past 255 (records past 8160). On a card
// whose record count exceeds 8160 (a ~6.7 GB CSD here), a query for sector 256 now
// SERVES ('R'+512) — the higher list sectors are no longer unreachable. The range
// check still refuses a sector genuinely beyond the card's list (> lr_nlist) with 'E',
// so a bad index never addresses a stray LBA. (Before i326 the 1-byte store truncated
// sector 256 into a read of sector 0, so the tool clamped its reach to 255.)
func TestListRecordsBigCardReach(t *testing.T) {
	if _, err := os.Stat(listRecordsBin); err != nil {
		t.Fatalf("list_records binary not built (%s); run `make netboot-list-records`", listRecordsBin)
	}
	mac, err := z80h.Load(listRecordsBin, listRecordsMap)
	if err != nil {
		t.Fatalf("load list_records: %v", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(spSAMMac, spSAMIp)
	sd := enc.AttachSD(z80h.CSDForV2(0x3200)) // 12801 * 512 KiB ≈ 6.7 GB — records > 8160
	mac.AttachIO(enc)
	mac.AttachPrintRecorder()
	mac.StubReturn(0x06B5) // CLSLOWER — hardware-only screen clear (i319a)

	enc.InjectRX(sdPushFrame([]byte{'?'}))
	enc.InjectRX(lrQuery(255))
	enc.InjectRX(lrQuery(256))
	enc.InjectRX(lrQuery(60000)) // far beyond nlist for a 6.7 GB card — refused
	enc.InjectRX(sdPushFrame([]byte{'Q'}))

	if _, err := mac.RunBoot("list_records_main", z80h.Entry{StepCap: 30_000_000}); err != nil {
		t.Fatalf("RunBoot list_records_main faulted: %v", err)
	}
	frames := enc.TXFrames()

	// Precondition: the CSD decode really produced a record count past the old
	// 8160-record reach, and nlist covers sector 256 while 60000 is out of range
	// (else this test exercises nothing).
	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("symbol %q absent from %s", name, listRecordsMap)
		}
		return a
	}
	records := int(leU16(mac.Read(sym("BD_RECORDS"), 2)))
	if records <= 255*32 {
		t.Fatalf("test CSD decoded to %d records, need > %d to exercise the widened reach", records, 255*32)
	}
	nlist := int(leU16(mac.Read(sym("lr_nlist"), 2)))
	if nlist < 256 {
		t.Fatalf("nlist = %d, need >= 256 so sector 256 is in range", nlist)
	}
	if 60000 <= nlist {
		t.Fatalf("out-of-range probe 60000 <= nlist %d — pick a higher probe", nlist)
	}
	if got := countPayload(frames, []byte{'!', 'L', 'R', byte(records & 0xFF), byte(records >> 8)}); got < 1 {
		t.Errorf("no '!LR'+records(%d LE16) discovery reply; tx payloads=%v", records, txPayloads(frames))
	}

	// Sector 255: still serves (unseeded zeros).
	if reply := findPayloadPrefix(frames, []byte{'R', 255, 0}); reply == nil || len(reply) != 3+512 {
		t.Errorf("list sector 255 (in range) did not serve an 'R'+512B reply; got %v", reply)
	}
	// Sector 256: NOW serves — the i326 payoff (past the old 1-byte seam).
	if reply := findPayloadPrefix(frames, []byte{'R', 0, 1}); reply == nil || len(reply) != 3+512 {
		t.Errorf("list sector 256 did not serve an 'R'+512B reply — the 16-bit seam should reach past 8160; got %v", reply)
	}
	// Sector 60000: genuinely beyond the card's list — still refused ('E' + echoed
	// index 0xEA60 LE), never a stray LBA read.
	if got := countPayload(frames, []byte{'E', 0x60, 0xEA}); got < 1 {
		t.Errorf("out-of-range list sector 60000 was not refused with 'E' — the range check must still bound queries")
	}

	// Still read-only on the big card.
	if writes := sd.WrittenSectors(); len(writes) != 0 {
		t.Fatalf("READ-ONLY VIOLATION: %d SD write(s) at %v", len(writes), writes)
	}
}

// TestListRecords64GBCard — the REAL-hardware regression (i319a, found 2026-07-02 on
// the first list-records hardware shot): Pete's 64 GB card decodes — via B-DOS
// 1.5t's faithful >51 GB clamp (&A452, csd_compute_eff) — to BD_RECORDS = 65535
// EXACTLY, and the original nlist = (BD_RECORDS+31)>>5 computation overflowed 16
// bits (65535+31 wraps to 30 → nlist 0), so the range check refused EVERY 'L'
// query and the tool returned no inventory. This test attaches the card's REAL
// captured CSD (csd_probe read-back, 2026-07-02) and asserts the full decode chain
// (records 65535, base 2050 — the values live-traced from B-DOS on this card) and
// that list sectors are served again across the full 16-bit seam (i326).
func TestListRecords64GBCard(t *testing.T) {
	if _, err := os.Stat(listRecordsBin); err != nil {
		t.Fatalf("list_records binary not built (%s); run `make netboot-list-records`", listRecordsBin)
	}
	mac, err := z80h.Load(listRecordsBin, listRecordsMap)
	if err != nil {
		t.Fatalf("load list_records: %v", err)
	}
	// The real 64 GB card's CSD, byte-for-byte as csd_probe served it from the
	// live card (C_SIZE = 0x01DBD3 → ~63.9 GB).
	realCSD := [16]byte{
		0x40, 0x0E, 0x00, 0x32, 0x5B, 0x59, 0x00, 0x01,
		0xDB, 0xD3, 0x7F, 0x80, 0x0A, 0x40, 0x40, 0xDF,
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(spSAMMac, spSAMIp)
	sd := enc.AttachSD(realCSD)
	mac.AttachIO(enc)
	mac.AttachPrintRecorder()
	mac.StubReturn(0x06B5) // CLSLOWER — hardware-only screen clear (i319a)

	enc.InjectRX(sdPushFrame([]byte{'?'}))
	enc.InjectRX(lrQuery(1))     // pre-i319a-fix: refused ('E') because nlist wrapped to 0
	enc.InjectRX(lrQuery(255))   // in range: serves
	enc.InjectRX(lrQuery(256))   // past the OLD 1-byte seam — now serves (i326)
	enc.InjectRX(lrQuery(60000)) // beyond nlist (2048): still refused
	enc.InjectRX(sdPushFrame([]byte{'Q'}))

	if _, err := mac.RunBoot("list_records_main", z80h.Entry{StepCap: 30_000_000}); err != nil {
		t.Fatalf("RunBoot list_records_main faulted: %v", err)
	}
	frames := enc.TXFrames()

	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("symbol %q absent from %s", name, listRecordsMap)
		}
		return a
	}
	// The faithful decode chain on this CSD: BD_RECORDS = 65535 and base = 2050 —
	// the exact values traced live from B-DOS 1.5t on this card (csd_compute_eff).
	if records := int(leU16(mac.Read(sym("BD_RECORDS"), 2))); records != 65535 {
		t.Fatalf("BD_RECORDS = %d, want 65535 (the B-DOS >51GB clamp on the real 64GB CSD)", records)
	}
	if base := int(leU16(mac.Read(sym("csd_base"), 2))); base != 2050 {
		t.Fatalf("csd_base = %d, want 2050 (the live-traced B-DOS base for this card)", base)
	}
	if got := countPayload(frames, []byte{'!', 'L', 'R', 0xFF, 0xFF}); got < 1 {
		t.Errorf("no '!LR'+65535 discovery reply; tx payloads=%v", txPayloads(frames))
	}

	// THE regression pin: sector 1 must SERVE (pre-i319a-fix it was refused). Sectors
	// 255 and 256 both serve — the 16-bit seam (i326) reaches past the old 8160 limit
	// (nlist here is 2048).
	for _, idx := range []uint16{1, 255, 256} {
		if reply := findPayloadPrefix(frames, []byte{'R', byte(idx & 0xFF), byte(idx >> 8)}); reply == nil || len(reply) != 3+512 {
			t.Errorf("list sector %d did not serve an 'R'+512B reply", idx)
		}
	}
	// Sector 60000 is beyond nlist (2048) — still refused ('E' + echoed 0xEA60 LE).
	if got := countPayload(frames, []byte{'E', 0x60, 0xEA}); got < 1 {
		t.Errorf("out-of-range list sector 60000 was not refused — the range check must bound queries")
	}
	if writes := sd.WrittenSectors(); len(writes) != 0 {
		t.Fatalf("READ-ONLY VIOLATION: %d SD write(s) at %v", len(writes), writes)
	}
}
