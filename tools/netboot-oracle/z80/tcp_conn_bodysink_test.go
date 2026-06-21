// tcp_conn_bodysink_test.go — the Brick 3 host-verification of the bodySink
// interposition seam in src/netboot. It runs the composed http_main binary
// (which includes tcp_conn.asm + body_sink.asm), enables streaming with
// CONN_SINK_FILTER_MODE=1 (dispatching to body_sink_write via CONN_SINK_FILTER),
// sets BODY_DST_PTR=storage_sink_leaf (routing the body bytes into the hash +
// recording double), and drives a full HTTP/1.0 response across straddling
// segments, then asserts:
//
//  1. CONN_SINK_OUT equals the body bytes (header stripped by body_sink_write).
//  2. CONN_HASH (after conn_verify_final) equals crypto/sha256.Sum256(body)
//     (the digest is over the body only, NOT the HTTP header).
//
// This mirrors the Go authority composition:
//
//	Fetcher.StreamTo → tcp.Conn.SetSink(http.NewBodySink(tcp.NewHashingSink(store)))
//
// where the hash runs over the body bytes only.
//
// The wire frames (ACK cadence, seq/ack arithmetic) are not re-checked here —
// the existing stream tests already prove the wire is byte-for-byte the Go
// authority regardless of which sink mode is active. This test focuses on what
// the composed sink records and digests.
package z80_test

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// loadHTTPMainForStream loads the composed netboot_http_main.bin, which carries
// both tcp_conn (with the Brick 3 CONN_SINK_FILTER dispatcher) and body_sink
// (with the BODY_DST_PTR call-through). All tcp_conn stream test helpers work
// against this binary because the symbol names are identical.
func loadHTTPMainForStream(t *testing.T) *z80h.Machine {
	t.Helper()
	mac, err := z80h.Load(httpMainBinPath, httpMainMapPath)
	if err != nil {
		t.Fatalf("http_main binary not built (%s): %v; run `make netboot-http-main`", httpMainBinPath, err)
	}
	return mac
}

// writeSymU16LE writes a 16-bit little-endian value (a function pointer or
// other word) to the named symbol address.
func writeSymU16LE(t *testing.T, mac *z80h.Machine, name string, value uint16) {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("symbol %q: %v", name, err)
	}
	mac.WriteU16LE(a, value)
}

// enableBodySinkFilter arms the Brick 3 interposition seam on the loaded
// http_main binary:
//   - CONN_SINK_ENABLED = 1 (streaming mode on)
//   - CONN_FLUSH_WINDOW = window
//   - CONN_SINK_FILTER_MODE = 1 (dispatcher takes the filter branch)
//   - CONN_SINK_FILTER = addr(body_sink_write) (the header-skip filter)
//   - BODY_DST_PTR = addr(storage_sink_leaf) (route body bytes to hash + record)
//   - BODY_HDR_DONE = 0 (header not yet seen)
func enableBodySinkFilter(t *testing.T, mac *z80h.Machine, window uint16) {
	t.Helper()
	put := func(name string, data []byte) {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("symbol %q: %v", name, err)
		}
		mac.Write(a, data)
	}
	put("CONN_SINK_ENABLED", []byte{1})
	put("CONN_FLUSH_WINDOW", []byte{byte(window), byte(window >> 8)})
	put("CONN_SINK_FILTER_MODE", []byte{1})
	writeSymU16LE(t, mac, "CONN_SINK_FILTER", symAddr(t, mac, "body_sink_write"))
	writeSymU16LE(t, mac, "BODY_DST_PTR", symAddr(t, mac, "storage_sink_leaf"))
	put("BODY_HDR_DONE", []byte{0})
}

// bodySinkSinkOut reads back CONN_SINK_OUT (the bytes forwarded through the
// full seam: body_sink_write → storage_sink_leaf → CONN_SINK_OUT).
func bodySinkSinkOut(t *testing.T, mac *z80h.Machine) []byte {
	t.Helper()
	n := binary.LittleEndian.Uint16(mac.Read(symAddr(t, mac, "CONN_SINK_OUT_LEN"), 2))
	return mac.Read(symAddr(t, mac, "CONN_SINK_OUT"), int(n))
}

// TestStreamThroughBodySink — the Brick 3 seam test: stream a full HTTP/1.0
// response (header + body) through the composed bodySink interposition and
// assert (1) CONN_SINK_OUT == body (header stripped) and (2) CONN_HASH ==
// crypto/sha256.Sum256(body) (digest over body only).
//
// The response is delivered across segments that straddle the flush window
// boundary so the hash is updated incrementally across multiple windows and
// the partial remainder at the FIN, exercising the full incremental path.
func TestStreamThroughBodySink(t *testing.T) {
	const (
		// A window comfortably larger than the HTTP header so the header fits
		// in the first flush (the documented header-fits-in-first-window
		// assumption), but small enough that the body spans several windows.
		window = 64
	)

	body := []byte("firmware-body-bytes-for-brick-3-test-the-seam-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	httpResp := "HTTP/1.0 200 OK\r\nContent-Length: " +
		string(rune('0'+len(body)/100)) + string(rune('0'+(len(body)/10)%10)) + string(rune('0'+len(body)%10)) +
		"\r\n\r\n" + string(body)

	// Use the composed http_main binary (carries both tcp_conn and body_sink).
	mac := loadHTTPMainForStream(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	// Arm the Brick 3 seam and the SHA-256 verify before any data arrives.
	enableBodySinkFilter(t, mac, window)
	verifyInit(t, mac)

	// Drive the handshake, body, and FIN using the shared stream helpers.
	// The Go reference tracks the TCP wire independently — we do not pass a
	// body_sink-aware Go ref here because the wire is already proven identical
	// by the existing stream tests. We build a minimal Go ref just for the
	// wire cross-check inside feedBodyZ80 / finStream.
	ref, _ := goConnStream(window)
	ref.Connect()

	srvSeq := establishStream(t, mac, enc, ref)

	// Deliver the HTTP response (header + body) across segments of 17 bytes —
	// an odd size that straddles both the header/body boundary and the window
	// boundaries so the incremental hash path is exercised across multiple
	// flush windows.
	httpBytes := []byte(httpResp)
	srvSeq = feedBodyZ80(t, mac, enc, ref, srvSeq, httpBytes, 17)
	finStream(t, mac, enc, ref, srvSeq)

	// Assert (1): CONN_SINK_OUT must contain only the body (header stripped).
	got := bodySinkSinkOut(t, mac)
	if string(got) != string(body) {
		t.Errorf("CONN_SINK_OUT = %q\n       want  = %q\n(header must be stripped; body only)", got, body)
	}

	// Assert (2): CONN_HASH (after conn_verify_final with the correct pin) must
	// equal crypto/sha256.Sum256(body) — the digest is over the body only.
	wantHash := sha256.Sum256(body)
	pinHash(t, mac, wantHash)
	gotHash, match := verifyFinal(t, mac)
	if gotHash != wantHash {
		t.Errorf("CONN_HASH mismatch after bodySink interposition:\n  z80  %x\n  want %x\n(digest must be over body only, not including the HTTP header)",
			gotHash, wantHash)
	}
	if match != 1 {
		t.Errorf("CONN_HASH_MATCH = %d with correct pin, want 1", match)
	}

	// Sanity: CONN_SINK_OUT must NOT contain the HTTP header.
	if len(got) > 0 && string(got[:4]) == "HTTP" {
		t.Errorf("CONN_SINK_OUT starts with the HTTP header — header skip failed: %q", got[:min(len(got), 40)])
	}
}
