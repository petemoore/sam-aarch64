// http_main_store_test.go — host-verification of the per-file store leaf
// (store_begin/storage_sink_leaf/store_end) in src/netboot/http_main.asm: the Z80
// port of the per-file store the Provisioner streams each fetched file into.
//
// TestProvStoreDemarcation drives two files through the composed streaming +
// bodySink path into the REAL B-DOS store leaf (a BDOSStore is attached). For each
// file, prov_start(i) opens it (arming the header-skip seam and, via store_begin,
// resetting the record index; record names are content-addressed from the file's
// pinned SHA-256 in CONN_PINNED_HASH, not the filename), a
// flush carries the file's HTTP/1.0 response (the header skipped, the body hashed
// and HSAVE'd as a bounded record), and store_end closes it (finishing the SHA-256
// verify and setting CONN_HASH_MATCH). It asserts each file's HSAVE'd record (name
// + size) and its per-file verdict (CONN_HASH_MATCH, read at store_end before the
// next prov_start overwrites it) against the Go authority: a MemStore fed the same
// bodies + tcp.HashingSink.Verify against the same pins (the Provisioner's per-file
// FileResult.Verified).
//
// File 0 pins the body's own hash → a match (verdict 1); file 1 keeps the
// manifest pin prov_start copied in, which the synthetic body does not hash to →
// a mismatch (verdict 0), so the 1/0 case the design calls for is exercised.
//
// The flush is driven through storage_sink_flush directly (the per-window flush
// entry the TCP layer calls) rather than a full per-file TCP handshake — the
// per-file ARP/SYN/GET/ACK/FIN cadence is the loop test's concern; this verifies
// the store leaf, which the Go comparison (MemStore + HashingSink, not the full
// Conn) mirrors. CONN_HASH_MATCH is a cryptographic per-file verdict and Saves()
// verifies the real leaf's records — stronger than the former recording double.
package z80_test

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/http"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// httpRespFor wraps a body in a minimal HTTP/1.0 response (status line +
// Content-Length + the blank-line terminator) — the bytes a flush window carries
// before the bodySink header-skip strips the header off.
func httpRespFor(body []byte) []byte {
	hdr := "HTTP/1.0 200 OK\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n"
	return append([]byte(hdr), body...)
}

// flushRespThroughSeam writes the HTTP response into CONN_FLUSH_BUF and calls
// storage_sink_flush (the per-window flush entry) with the response length, so
// the armed bodySink seam skips the header and the body streams through the
// SHA-256 into the real store leaf's HSAVE — the same path the TCP layer drives a
// window through. The full response fits one window (small synthetic bodies),
// satisfying the documented header-fits-in-the-first-window assumption.
func flushRespThroughSeam(t *testing.T, mac *z80h.Machine, resp []byte) {
	t.Helper()
	mac.Write(symAddr(t, mac, "CONN_FLUSH_BUF"), resp)
	if _, err := mac.CallEntry("storage_sink_flush", z80h.Entry{HL: uint16(len(resp))}); err != nil {
		t.Fatalf("storage_sink_flush: %v", err)
	}
}

// manifestNameBytes reads file i's manifest name string (rec+0) back out of the
// Z80 binary — the bytes storage_sink_leaf feeds fw_span_record_name to build the
// record name. Independent of the Go authority name.
func manifestNameBytes(t *testing.T, mac *z80h.Machine, i int) []byte {
	t.Helper()
	res, err := mac.CallEntry("fw_manifest_entry", z80h.Entry{BC: uint16(i)})
	if err != nil {
		t.Fatalf("fw_manifest_entry(%d): %v", i, err)
	}
	namePtr := binary.LittleEndian.Uint16(mac.Read(res.BC, 2)) // record+0 = name ptr
	return []byte(readCStrAt(mac, namePtr))
}

// TestProvStoreDemarcation: two files driven through the real store leaf HSAVE one
// bounded record each (name + size matching fw_span_record_name / the body length)
// and set CONN_HASH_MATCH per file (verdict 1 then 0), matching the Go MemStore +
// HashingSink authority.
func TestProvStoreDemarcation(t *testing.T) {
	plan, err := http.RPiFirmware.Plan(nil)
	if err != nil {
		t.Fatalf("http.RPiFirmware.Plan(nil): %v", err)
	}

	// Small synthetic bodies (each well under one flush window, so each file is a
	// single bounded record).
	body0 := []byte("file-zero-body-the-licence-bytes-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	body1 := []byte("file-one-body-the-bootcode-bytes-0123456789-zyxwvutsrqponml")

	mac := loadHTTPMain(t)
	writeProvConfig(t, mac) // BASE_PORT / BASE_ISS (identity unused by this test)
	// The free-record scan runs the REAL CMD17 list read (NETBOOT_REAL_LISTREAD,
	// i70e) — attach the SPI SD model with a CSD and run the boot's CSD read so
	// BD_RECORDS and the block-vs-byte addressing come from the real decode
	// ladder, exactly as on hardware. Un-seeded list sectors read back as zeros
	// = all records free, so prov_start(0) picks record 1.
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csdV2(0x001D59)) // ~3.7 GB SDHC
	mac.AttachIO(enc)
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("csd_set_bd_records: %v", err)
	}
	store := z80h.NewBDOSStore()
	// D2: after the first HSAVE the record must scan as used so prov_start(1)
	// picks record 2 — on the SPI path that means the record-LIST bytes on the
	// card model, not the hook-time overlay (i70e).
	store.MirrorUsedRecordsTo(sd)
	mac.AttachBDOS(store) // the REAL store leaf's rst 8 HSAVE runs (no card: verdict = the digest)

	name0 := manifestNameBytes(t, mac, 0)
	name1 := manifestNameBytes(t, mac, 1)

	// --- file 0: pin the body's own hash so the verdict is a match (1) ---
	if _, err := mac.CallEntry("prov_start", z80h.Entry{BC: 0}); err != nil {
		t.Fatalf("prov_start(0): %v", err)
	}
	pin0 := sha256.Sum256(body0)
	pinHash(t, mac, pin0) // override the manifest pin so the body matches
	flushRespThroughSeam(t, mac, httpRespFor(body0))
	if _, err := mac.Call("store_end"); err != nil {
		t.Fatalf("store_end(0): %v", err)
	}
	// Read the verdict at store_end, before the next prov_start overwrites it.
	v0 := mac.Read(symAddr(t, mac, "CONN_HASH_MATCH"), 1)[0]

	// --- file 1: keep prov_start's manifest pin; the synthetic body will not
	// hash to it, so the verdict is a mismatch (0) ---
	if _, err := mac.CallEntry("prov_start", z80h.Entry{BC: 1}); err != nil {
		t.Fatalf("prov_start(1): %v", err)
	}
	pin1 := http.RPiFirmware.Files[1].SHA256 // the manifest pin prov_start(1) copied in
	flushRespThroughSeam(t, mac, httpRespFor(body1))
	if _, err := mac.Call("store_end"); err != nil {
		t.Fatalf("store_end(1): %v", err)
	}
	v1 := mac.Read(symAddr(t, mac, "CONN_HASH_MATCH"), 1)[0]

	// --- Go authority: a MemStore fed the same bodies + HashingSink verdicts ---
	goStore := http.NewMemStore()
	s0 := tcp.NewHashingSink(goStore.Begin(plan[0].Name))
	s0.Write(body0)
	goStore.End(plan[0].Name)
	goV0 := s0.Verify(pin0)
	s1 := tcp.NewHashingSink(goStore.Begin(plan[1].Name))
	s1.Write(body1)
	goStore.End(plan[1].Name)
	goV1 := s1.Verify(pin1)

	// === assert: the Z80 store leaf matches the Go authority ===

	// Records: one bounded HSAVE per file (each body fits one window), in order,
	// each named <hash6>000 (single record → index 0) with size == the body length.
	saves := store.Saves()
	if len(saves) != 2 {
		t.Fatalf("store recorded %d HSAVE(s), want 2 (one bounded record per file)", len(saves))
	}
	type want struct {
		name   string
		size   uint32
		record int
	}
	wants := []want{
		// Names are content-addressed: fw_span_record_name of the file's pinned SHA-256
		// (CONN_PINNED_HASH), NOT the filename. File 0 pinned the body's own hash (pin0);
		// file 1 keeps the manifest pin (pin1). D2: prov_start(0) finds record 1 free,
		// prov_start(1) finds record 2 free (record 1 used by the first HSAVE).
		{spanRecordName(pin0[:], 0), uint32(len(body0)), 1},
		{spanRecordName(pin1[:], 0), uint32(len(body1)), 2},
	}
	for i, w := range wants {
		if saves[i].Name != w.name {
			t.Errorf("record[%d] name = %q, want %q (fw_span_record_name of the pinned content hash)", i, saves[i].Name, w.name)
		}
		if saves[i].Size != w.size {
			t.Errorf("record[%d] size = %d, want %d (the served body length, header stripped)", i, saves[i].Size, w.size)
		}
		// D2: storage_sink_leaf uses FW_BASE_RECORD (set in store_begin by
		// bdos_find_free_record) + FW_REC_IDX as the HRECORD selection. With
		// BD_RECORDS=10 and no pre-existing records, file 0 uses record 1 and
		// file 1 uses record 2 (the usedRecords overlay in BDOSStore marks record
		// 1 used after the first HSAVE so bdos_find_free_record returns 2 next).
		if saves[i].Record != w.record {
			t.Errorf("record[%d] HRECORD = %d, want %d (first-free base+index)", i, saves[i].Record, w.record)
		}
	}

	// The Go store order names the same two files (cross-check the manifest names).
	if string(name0) != goStore.Order[0] {
		t.Errorf("file 0 manifest name %q != Go MemStore.Order[0] %q", name0, goStore.Order[0])
	}
	if string(name1) != goStore.Order[1] {
		t.Errorf("file 1 manifest name %q != Go MemStore.Order[1] %q", name1, goStore.Order[1])
	}

	// Verdicts: CONN_HASH_MATCH per file == the Go HashingSink.Verify verdict.
	wantV := func(b bool) byte {
		if b {
			return 1
		}
		return 0
	}
	if v0 != wantV(goV0) {
		t.Errorf("verdict[0] = %d, want %d (Go Verify=%v)", v0, wantV(goV0), goV0)
	}
	if v1 != wantV(goV1) {
		t.Errorf("verdict[1] = %d, want %d (Go Verify=%v)", v1, wantV(goV1), goV1)
	}
	// Belt-and-braces: the design's 1/0 case (file 0 matched its pin, file 1 not).
	if v0 != 1 || v1 != 0 {
		t.Errorf("verdicts = [%d %d], want [1 0] (file0 matches its pin, file1 does not)", v0, v1)
	}
}
