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

	// (1) Discovery answers '!' + the record count (LE16) so the host knows how
	// many list sectors to query.
	if got := countPayload(frames, []byte{'!', byte(lrRecords & 0xFF), byte(lrRecords >> 8)}); got < 1 {
		t.Errorf("no '!'+records(%d LE16) discovery reply; tx payloads=%v", lrRecords, txPayloads(frames))
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
