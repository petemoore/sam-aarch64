// http_main_faithful_store_test.go — i356: the faithful full-come-up + store-leaf
// gate for the http-boot store path, and the diagnosis of the i70b store wedge.
//
// The i70b hardware shots (2, then 3 after the i355 HMPR source-page fix) both
// wedged IDENTICALLY at the stream-end store leg: the body streamed to the FIN,
// then rx went dead at the first real RST-8 store (markers reached FILE_START but
// never FILE_SAVED, a wild-jump not a clean halt). The i355 fix (source page from
// HMPR, not ptr>>14) was necessary-but-insufficient; the residual root cause was
// unknown. The wire-side suite (http_main_loop_test / http_main_store_test) runs
// the store leaf against a BDOSStore MOCK, so it cannot see a real-B-DOS-1.5t /
// SD-driver wedge (the rule-7 false-confidence the i355 fix was merged on).
//
// This file closes that gap. It boots Colin's real ROM + B-DOS 1.5t + the SPI
// SD-card model (no BDOSStore mock), loads the REAL smoke boot image into the
// exact trinload-pushed page-1/2 context, and:
//
//   - TestHTTPMainFaithfulComeUp asserts the come-up runs end-to-end (drv_init ->
//     csd_set_bd_records -> enc_rx_reestablish -> drv_wait_link -> prov_first),
//     reaching ht_prov_loop with a live card. This closes the item's blocker #1
//     ("the come-up CSD read declines under emulation -> BD_RECORDS=0"): the
//     decline was a harness LOAD-ORDER bug (the 32 KB overlay's section-D half was
//     written while ROM1 was mapped read-only at section D, so it was dropped and
//     the whole come-up ran as NOPs), NOT a model-fidelity gap. Writing after
//     armServeDispatch (LMPR=&1F = RAM at section D) lands the whole image.
//
//   - TestHTTPMainFaithfulStoreLeafValidRecord drives the store leaf's real RST-8
//     store into a VALID (already MGT-formatted, "BDOS"-stamped) record and
//     asserts it completes cleanly + commits CMD24 writes — proving the leaf
//     mechanics (source page from HMPR per i355, section-C offset, UIFA fill,
//     HRECORD select, HSAVE) are correct against real B-DOS 1.5t. This is the
//     hook_roundtrip scenario (a record sd_push pre-wrote), NOT http_main's
//     runtime scenario.
//
// ROOT CAUSE of the i70b wedge (i356 finding, reproduced this session): http_main's
// store leaf targets the first FREE record (store_begin's bdos_find_free_record),
// and B-DOS 1.5t HRECORD validates the record's "BDOS" +232 body stamp and longjmps
// ("Invalid record") when it is absent — which it is on a genuinely free record.
// Seeding the record's dir sector blank (a real free record) makes storage_sink_leaf
// reach bdos_select_record (HRECORD) and never return — the exact wild-jump the
// hardware shots showed. The i355 source-page fix was downstream of this HRECORD
// wedge, so it could not clear it (necessary-but-insufficient). The FIX (i357,
// IMPLEMENTED) is store_format_record (http_main.asm, NETBOOT_WANT_RECORD_WRITE): it
// reproduces B-DOS FORMAT's directory-level effect — blank tracks 0-3 + stamp "BDOS"
// at +232 of the record's first sector, via the hardware-proven raw CMD24 record write
// — before the HRECORD/HSAVE hooks, so HRECORD accepts the record. Its acceptance gate
// is TestHTTPMainFaithfulStoreLeafFreeRecord (below), which i70b depends on.
//
// GATED on the proprietary captures; skips under SKIP_PRIVATE_TESTS (the one
// sanctioned skip, i253). Emulation-verified is not hardware-verified (CLAUDE.md §5).
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	httpSmokeFaithBin = "../../../build/netboot_http_smoke_boot.bin"
	httpSmokeFaithMap = "../../../build/netboot_http_smoke_boot.map"
)

// comeUpHTTPFaithful boots the faithful machine (real ROM + B-DOS 1.5t + SPI SD +
// ENC), loads the REAL smoke boot image into the trinload page-1/2 context, and
// runs the real come-up from http_main's entry to ht_prov_loop. It returns the
// machine, a symbol resolver, and the SD card model — the come-up has decoded the
// CSD (BD_RECORDS set), re-armed the ENC RX, waited for link, and (via prov_first
// -> prov_start(0)) opened file 0 in the store (FW_BASE_RECORD = first free record,
// the SHA-256 verify initialised, the bodySink header-skip seam armed).
func comeUpHTTPFaithful(t *testing.T) (*z80h.Machine, func(string) uint16, *z80h.SDCard) {
	t.Helper()
	mac, _, sd, _ := bootToEditorIdleSDENC(t)

	code := mustReadFile(t, httpSmokeFaithBin, "make netboot-http-smoke-boot")
	if err := mac.LoadSymbols(httpSmokeFaithMap); err != nil {
		t.Fatalf("load %s: %v", httpSmokeFaithMap, err)
	}
	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("symbol %q absent from %s: %v", name, httpSmokeFaithMap, err)
		}
		return a
	}

	// The trinload deployment contract: the 32 KB section-D overlay is pushed to
	// physical pages 1 (section C) + 2 (section D); `out (HMPR),1; jp &8000`.
	// armServeDispatch establishes that context FIRST (LMPR=&1F puts RAM — not
	// ROM1 — at section D, HMPR=1), so the subsequent whole-image write at &8000
	// lands section C in page 1 AND section D in page 2 (secDPage = HMPR+1). The
	// boot LMPR (&40) maps ROM1 read-only at section D, so writing before this
	// would silently DROP the image's section-D half (the whole come-up as NOPs) —
	// the harness bug behind the item's "come-up declines" blocker #1.
	const page = 1
	armServeDispatch(mac, page)
	mac.Write(0x8000, code)

	// Run the real come-up from http_main's entry to ht_prov_loop (prov_first has
	// broadcast the ARP by then). FrameIntPeriod models the SAM's 50 Hz interrupt
	// through the real ROM &0038 handler — the come-up does `ei`, so this is the
	// faithful interrupt environment the store leg runs in.
	res, err := mac.ContinueFrom(sym("http_main"), z80h.Entry{
		StepCap: 60_000_000, FrameIntPeriod: 60000,
		StopPC: sym("ht_prov_loop"),
	})
	if err != nil {
		t.Fatalf("come-up faulted: %v (PC=&%04X, steps=%d)", err, res.PC, res.Steps)
	}
	if !res.ReachedStop {
		labels := map[uint16]string{
			sym("ht_fail_cfg"):  "ht_fail_cfg (red: EEPROM config read)",
			sym("ht_fail_init"): "ht_fail_init (blue: ENC drv_init)",
			sym("ht_fail_link"): "ht_fail_link (yellow: PHY link never up)",
		}
		where := "(unmapped spin site)"
		if l, ok := labels[res.PC]; ok {
			where = l
		}
		t.Fatalf("come-up did NOT reach ht_prov_loop (&%04X): stopped at PC=&%04X %s after %d steps",
			sym("ht_prov_loop"), res.PC, where, res.Steps)
	}
	return mac, sym, sd
}

// TestHTTPMainFaithfulComeUp asserts the http-boot come-up runs end-to-end under
// faithful emulation, reaching ht_prov_loop with a live card (BD_RECORDS != 0 and
// a free record found). Closes the item's blocker #1 (see the file header).
func TestHTTPMainFaithfulComeUp(t *testing.T) {
	mac, sym, _ := comeUpHTTPFaithful(t)

	bd := mac.Read(sym("BD_RECORDS"), 2)
	bdRecords := int(bd[0]) | int(bd[1])<<8
	fw := mac.Read(sym("FW_BASE_RECORD"), 2)
	fwBase := int(fw[0]) | int(fw[1])<<8
	t.Logf("come-up reached ht_prov_loop; BD_RECORDS=%d, FW_BASE_RECORD=%d, HMPR=&%02X",
		bdRecords, fwBase, mac.Pager().HMPR)

	if bdRecords == 0 {
		t.Fatalf("BD_RECORDS=0 after the come-up: the faithful CSD read declined — " +
			"the come-up cannot reach the store leg with a live card")
	}
	if fwBase == 0 {
		t.Fatalf("FW_BASE_RECORD=0 after the come-up: store_begin found no free record " +
			"(bdos_find_free_record scan failed)")
	}
}

// TestHTTPMainFaithfulStoreLeafValidRecord drives the store leaf's real RST-8 store
// into an already-VALID (MGT-formatted, "BDOS"-stamped) record — bootToEditorIdleSDENC
// seeds record 1's dir sector with the stamp — and asserts it completes cleanly and
// commits CMD24 writes. This proves the store LEAF is correct against real B-DOS 1.5t
// (the i355 source page from HMPR, the section-C offset, the UIFA, HRECORD, HSAVE) and
// that the i357 pre-format (store_format_record) is IDEMPOTENT over an already-stamped
// record: it re-blanks the directory region + re-stamps, then HSAVE persists the body.
//
// The genuinely-free (unstamped) record — http_main's real runtime scenario, which
// wedged in the i70b shots — is asserted by TestHTTPMainFaithfulStoreLeafFreeRecord
// below (the i357 acceptance gate). This test keeps the "valid record" case as a
// distinct regression: the leaf works whether or not the record was pre-stamped.
func TestHTTPMainFaithfulStoreLeafValidRecord(t *testing.T) {
	mac, sym, sd := comeUpHTTPFaithful(t)

	// A LICENCE.broadcom-sized body (1594 B, one FW_RECORD_CAP=6144 window -> one
	// bounded record), wrapped in a minimal HTTP/1.0 response (httpRespFor lives in
	// http_main_store_test.go, same package). The bodySink seam skips the header
	// and streams the body through the SHA-256 into storage_sink_leaf's HSAVE.
	body := make([]byte, 1594)
	for i := range body {
		body[i] = byte(i*7 + 3)
	}
	resp := httpRespFor(body)
	mac.Write(sym("CONN_FLUSH_BUF"), resp)

	before := len(sd.WrittenSectors())
	res, err := mac.ContinueFrom(sym("storage_sink_flush"), z80h.Entry{
		HL: uint16(len(resp)), StepCap: 60_000_000, FrameIntPeriod: 60000,
	})
	if err != nil {
		t.Fatalf("store-leaf drive faulted: %v (PC=&%04X, steps=%d)", err, res.PC, res.Steps)
	}
	wrote := len(sd.WrittenSectors()) - before
	t.Logf("store leaf (valid record): returned=%v PC=&%04X steps=%d CMD24writes=%d",
		res.Halted, res.PC, res.Steps, wrote)

	if !res.Halted {
		t.Fatalf("store leaf did NOT return (PC=&%04X after %d steps) — the RST-8 store "+
			"wild-jumped even on a VALID record; the leaf mechanics are broken, not just the "+
			"free-record context", res.PC, res.Steps)
	}
	if wrote == 0 {
		t.Fatalf("store leaf returned but committed no CMD24 write to the SD model — the HSAVE " +
			"did not persist the record")
	}
}

// TestHTTPMainFaithfulStoreLeafFreeRecord is the i357 acceptance gate: it drives the
// store leaf's real RST-8 store into a genuinely FREE (unformatted, NO "BDOS" stamp)
// record — http_main's ACTUAL runtime scenario — and asserts the leaf now completes
// cleanly and persists the file. This is the free-record path that wedged in the i70b
// hardware shots and in the i356 diagnosis (HRECORD longjmps "Invalid record" on a
// record lacking the +232 stamp, so the leaf never returns).
//
// The fix (store_format_record, http_main.asm, NETBOOT_WANT_RECORD_WRITE): before the
// HRECORD/HSAVE hooks, reproduce B-DOS FORMAT's directory-level effect via raw CMD24 —
// blank the directory region (tracks 0-3 = linearSec 0..39) and stamp "BDOS" at +232 of
// sector 0 — so HRECORD then accepts the record and HSAVE persists the body. Without the
// fix this test wedges (the leaf never returns); with it, it returns cleanly and the
// stamp + body land in the record's own band.
//
// bootToEditorIdleSDENC seeds record 1's dir sector WITH the stamp (a valid record); this
// test BLANKS it (SeedSector with a zero sector) so record 1 is genuinely free — exactly
// what bdos_find_free_record picks at runtime (its list entry is blank) and exactly what
// real B-DOS HRECORD rejects until we format it.
func TestHTTPMainFaithfulStoreLeafFreeRecord(t *testing.T) {
	mac, sym, sd := comeUpHTTPFaithful(t)

	// The come-up's store_begin picked FW_BASE_RECORD (record 1 here). Blank THAT
	// record's first dir sector so it is a genuinely free/unformatted record (no
	// "BDOS" +232 stamp) — the runtime scenario the i70b shots wedged on.
	fw := mac.Read(sym("FW_BASE_RECORD"), 2)
	freeRec := int(fw[0]) | int(fw[1])<<8
	if freeRec != 1 {
		t.Fatalf("FW_BASE_RECORD=%d, expected 1 (record 1 is the first free record on this card model)", freeRec)
	}
	const csdBase = 152 // csdV2(0x001D59) base — record 1's first dir sector LBA
	sd.SeedSector(csdBase, make([]byte, 512))
	if pre, ok := sd.CapturedSector(csdBase); ok && string(pre[232:236]) == "BDOS" {
		t.Fatalf("record 1 dir sector still carries the BDOS stamp after blanking — test setup wrong")
	}

	// Same 1594 B body / one FW_RECORD_CAP window as the valid-record test.
	body := make([]byte, 1594)
	for i := range body {
		body[i] = byte(i*7 + 3)
	}
	resp := httpRespFor(body)
	mac.Write(sym("CONN_FLUSH_BUF"), resp)

	before := len(sd.WrittenSectors())
	res, err := mac.ContinueFrom(sym("storage_sink_flush"), z80h.Entry{
		HL: uint16(len(resp)), StepCap: 60_000_000, FrameIntPeriod: 60000,
	})
	if err != nil {
		t.Fatalf("store-leaf drive faulted: %v (PC=&%04X, steps=%d)", err, res.PC, res.Steps)
	}
	wrote := len(sd.WrittenSectors()) - before
	t.Logf("store leaf (FREE record): returned=%v PC=&%04X steps=%d CMD24writes=%d",
		res.Halted, res.PC, res.Steps, wrote)

	// (1) The leaf must RETURN cleanly. Without the pre-format, HRECORD longjmps on the
	// unstamped record and the leaf wild-jumps — never halting (the i70b wedge).
	if !res.Halted {
		t.Fatalf("store leaf did NOT return on a FREE record (PC=&%04X after %d steps) — the "+
			"pre-format did not make HRECORD accept the record; this is the i70b store-leg wedge",
			res.PC, res.Steps)
	}

	// (2) The pre-format stamped the record's first dir sector: record 1 sector 0
	// (LBA 152) must now carry "BDOS" at +232 (a CMD24 write, not the blanked seed).
	dir0, ok := sd.RecordDataSector(csdBase, freeRec, 0)
	if !ok {
		t.Fatalf("record %d sector 0 (LBA %d) absent after the flush — store_format_record did not write it", freeRec, csdBase)
	}
	if string(dir0[232:236]) != "BDOS" {
		t.Fatalf("record %d sector 0 lacks the BDOS +232 stamp after the flush (got %q) — the pre-format did not run",
			freeRec, dir0[232:236])
	}

	// (3) The body was persisted BEYOND the 40-sector directory region: the HSAVE store
	// wrote the file into record 1's data area (track 4+), so more than the 40 format
	// sectors were committed.
	if wrote <= 40 {
		t.Fatalf("only %d CMD24 writes — expected the 40-sector directory pre-format PLUS the HSAVE "+
			"body writes; the file was not persisted after the format", wrote)
	}

	// (4) DATA SAFETY (the Trinity SD card is a shared user resource): every committed
	// write fell inside record 1's own band — the pre-format's raw CMD24s never stray to
	// the record list, sector 0, or a neighbouring record (bd_record_write_hw's i295 guard).
	if outside := sd.WrittenSectorsOutsideRecord(csdBase, freeRec); len(outside) != 0 {
		t.Fatalf("DATA-SAFETY VIOLATION: %d CMD24 write(s) landed outside record %d's range: %v",
			len(outside), freeRec, outside)
	}
}
