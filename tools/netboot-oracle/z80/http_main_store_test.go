// http_main_store_test.go — Brick 5 host-verification of the per-file store
// double (store_begin/store_end) in src/netboot/http_main.asm: the Z80 port of
// the Go MemStore the Provisioner streams each fetched file into.
//
// TestProvStoreDemarcation drives two files through the composed streaming +
// bodySink path. For each file, prov_start(i) opens it (arming the header-skip
// seam and recording its start boundary via store_begin), a flush carries the
// file's HTTP/1.0 response (the header skipped, the body hashed + appended to the
// shared CONN_SINK_OUT), and store_end closes it (finishing the SHA-256 verify
// and recording the verdict + end boundary). It then asserts the recorded
// demarcation reproduces body0 then body1, the names match, and the verdicts
// (1 = the streamed body's hash matched the pin, 0 = it did not) equal the Go
// authority: a MemStore fed the same bodies + tcp.HashingSink.Verify against the
// same pins (the Provisioner's per-file FileResult.Verified).
//
// File 0 pins the body's own hash → a match (verdict 1); file 1 keeps the
// manifest pin prov_start copied in, which the synthetic body does not hash to →
// a mismatch (verdict 0), so the 1/0 case the design calls for is exercised.
//
// The flush is driven through storage_sink_flush directly (the per-window flush
// entry the TCP layer calls) rather than a full per-file TCP handshake — the
// per-file ARP/SYN/GET/ACK/FIN cadence is Brick 6's concern; this brick verifies
// the store demarcation, which the Go comparison (MemStore + HashingSink, not the
// full Conn) mirrors.
package z80_test

import (
	"bytes"
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
// the armed bodySink seam skips the header and the body streams into the SHA-256
// + CONN_SINK_OUT — the same path the TCP layer drives a window through. The full
// response fits one window (small synthetic bodies), satisfying the documented
// header-fits-in-the-first-window assumption.
func flushRespThroughSeam(t *testing.T, mac *z80h.Machine, resp []byte) {
	t.Helper()
	mac.Write(symAddr(t, mac, "CONN_FLUSH_BUF"), resp)
	if _, err := mac.CallEntry("storage_sink_flush", z80h.Entry{HL: uint16(len(resp))}); err != nil {
		t.Fatalf("storage_sink_flush: %v", err)
	}
}

// TestProvStoreDemarcation: two files driven through the store double leave
// PROV_STORE_OFFS/NAMES/VERDICTS demarcating body0 then body1, byte-for-byte the
// Go MemStore + HashingSink authority.
func TestProvStoreDemarcation(t *testing.T) {
	plan, err := http.RPiFirmware.Plan(nil)
	if err != nil {
		t.Fatalf("http.RPiFirmware.Plan(nil): %v", err)
	}

	// Small synthetic bodies (well under CONN_FLUSH_BUF and CONN_SINK_OUT).
	body0 := []byte("file-zero-body-the-licence-bytes-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	body1 := []byte("file-one-body-the-bootcode-bytes-0123456789-zyxwvutsrqponml")

	mac := loadHTTPMain(t)
	writeProvConfig(t, mac) // BASE_PORT / BASE_ISS (identity unused by this brick)

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

	// === assert: the Z80 store double matches the Go authority ===

	if got := readWord(t, mac, "PROV_STORE_COUNT"); got != 2 {
		t.Fatalf("PROV_STORE_COUNT = %d, want 2", got)
	}

	// Boundaries: OFFS[0..2] demarcate body0 then body1 within CONN_SINK_OUT.
	offsAddr := symAddr(t, mac, "PROV_STORE_OFFS")
	off := func(i int) uint16 {
		return binary.LittleEndian.Uint16(mac.Read(offsAddr+uint16(i*2), 2))
	}
	if off(0) != 0 {
		t.Errorf("OFFS[0] = %d, want 0", off(0))
	}
	if int(off(1)) != len(body0) {
		t.Errorf("OFFS[1] = %d, want %d (len body0)", off(1), len(body0))
	}
	if int(off(2)) != len(body0)+len(body1) {
		t.Errorf("OFFS[2] = %d, want %d (len body0+body1)", off(2), len(body0)+len(body1))
	}

	// CONN_SINK_OUT slices == the bodies, cross-checked against the Go MemStore.
	sinkAddr := symAddr(t, mac, "CONN_SINK_OUT")
	got0 := mac.Read(sinkAddr+off(0), int(off(1)-off(0)))
	got1 := mac.Read(sinkAddr+off(1), int(off(2)-off(1)))
	if !bytes.Equal(got0, body0) {
		t.Errorf("slice 0 = %q, want %q", got0, body0)
	}
	if !bytes.Equal(got1, body1) {
		t.Errorf("slice 1 = %q, want %q", got1, body1)
	}
	if !bytes.Equal(got0, goStore.Files[plan[0].Name]) {
		t.Errorf("slice 0 != Go MemStore.Files[%q]\n  z80 %q\n  go  %q", plan[0].Name, got0, goStore.Files[plan[0].Name])
	}
	if !bytes.Equal(got1, goStore.Files[plan[1].Name]) {
		t.Errorf("slice 1 != Go MemStore.Files[%q]\n  z80 %q\n  go  %q", plan[1].Name, got1, goStore.Files[plan[1].Name])
	}

	// Names: PROV_STORE_NAMES[i] -> the manifest name == Go MemStore.Order[i].
	namesAddr := symAddr(t, mac, "PROV_STORE_NAMES")
	name := func(i int) string {
		ptr := binary.LittleEndian.Uint16(mac.Read(namesAddr+uint16(i*2), 2))
		return readCStrAt(mac, ptr)
	}
	for i := 0; i < 2; i++ {
		if got, want := name(i), goStore.Order[i]; got != want {
			t.Errorf("name[%d] = %q, want %q", i, got, want)
		}
	}

	// Verdicts: PROV_FILE_VERDICTS[i] == the Go HashingSink.Verify verdict.
	verdicts := mac.Read(symAddr(t, mac, "PROV_FILE_VERDICTS"), 2)
	wantV := func(b bool) byte {
		if b {
			return 1
		}
		return 0
	}
	if verdicts[0] != wantV(goV0) {
		t.Errorf("verdict[0] = %d, want %d (Go Verify=%v)", verdicts[0], wantV(goV0), goV0)
	}
	if verdicts[1] != wantV(goV1) {
		t.Errorf("verdict[1] = %d, want %d (Go Verify=%v)", verdicts[1], wantV(goV1), goV1)
	}
	// Belt-and-braces: the design's 1/0 case (file 0 matched its pin, file 1 not).
	if verdicts[0] != 1 || verdicts[1] != 0 {
		t.Errorf("verdicts = %v, want [1 0] (file0 matches its pin, file1 does not)", verdicts)
	}
}
