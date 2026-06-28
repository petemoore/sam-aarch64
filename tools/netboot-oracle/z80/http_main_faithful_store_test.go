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
// wedge, so it could not clear it (necessary-but-insufficient). The FIX — writing
// into a free record (pre-format the record's MGT dir, or port sd_push's raw CMD24
// record-write) — is tracked as i357 (its red acceptance test is the free-record
// store leaf persisting), which i70b now depends on.
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
// into a VALID (MGT-formatted, "BDOS"-stamped) record — bootToEditorIdleSDENC seeds
// record 1's dir sector with the stamp — and asserts it completes cleanly and
// commits CMD24 writes. This proves the store LEAF is correct against real B-DOS
// 1.5t (the i355 source page from HMPR, the section-C offset, the UIFA, HRECORD,
// HSAVE) when the target record is valid.
//
// IMPORTANT: this is NOT http_main's runtime path. At runtime the leaf targets the
// first FREE record (store_begin), which has no "BDOS" stamp, so HRECORD longjmps
// and the leaf wedges — the i70b hardware wedge (see the file header ROOT CAUSE).
// That free-record path is the red acceptance test for the fix, tracked as i357;
// it is deliberately NOT asserted here (a red test lives on the fix branch until it
// passes — CLAUDE.md §5), so `main` keeps an honest, passing gate that isolates
// "the leaf works given a valid record" from "the leaf's runtime context is wrong".
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
