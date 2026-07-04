// tcp_conn_bodysink_test.go — host-verification of the bodySink interposition
// seam in src/netboot. It runs the bootable http_main binary (which includes
// tcp_conn.asm + body_sink.asm), enables streaming with CONN_SINK_FILTER_MODE=1
// (dispatching to body_sink_write via CONN_SINK_FILTER), sets
// BODY_DST_PTR=storage_sink_leaf (routing the body bytes through the hash into the
// real B-DOS HSAVE-per-record store leaf), and drives a full HTTP/1.0 response
// across straddling segments, then asserts:
//
//  1. The store leaf HSAVE'd the body bytes only, one bounded record per flush
//     window (header stripped by body_sink_write) — asserted via store.Saves().
//  2. CONN_HASH (after conn_verify_final) equals crypto/sha256.Sum256(body)
//     (the digest is over the body only, NOT the HTTP header), and CONN_HASH_MATCH
//     is 1 when the correct hash is pinned.
//
// This mirrors the Go authority composition:
//
//	Fetcher.StreamTo → tcp.Conn.SetSink(http.NewBodySink(tcp.NewHashingSink(store)))
//
// where the hash runs over the body bytes only. The store leaf is exercised as the
// REAL leaf (a BDOSStore is attached), stronger than the former recording double:
// the digest verify is a cryptographic proof of the streamed bytes, and Saves()
// proves the real leaf emitted the right records.
//
// The wire frames (ACK cadence, seq/ack arithmetic) are not re-checked here —
// the existing stream tests already prove the wire is byte-for-byte the Go
// authority regardless of which sink mode is active. This test focuses on what
// the composed sink records and digests.
package z80_test

import (
	"crypto/sha256"
	"fmt"
	"testing"

	nbhttp "github.com/petemoore/sam-aarch64/tools/netboot-oracle/http"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// loadHTTPMainForStream loads the bootable http_main binary, which carries both
// tcp_conn (with the CONN_SINK_FILTER dispatcher) and body_sink (with the
// BODY_DST_PTR call-through). All tcp_conn stream test helpers work against this
// binary because the symbol names are identical.
func loadHTTPMainForStream(t *testing.T) *z80h.Machine {
	t.Helper()
	mac, err := z80h.Load(httpMainBinPath, httpMainMapPath)
	if err != nil {
		t.Fatalf("http_main binary not built (%s): %v; run `make netboot-http-boot`", httpMainBinPath, err)
	}
	return mac
}

// spanRecordName mirrors src/netboot/fw_span.asm fw_span_record_name: the record
// name is the first 3 bytes of hashBytes emitted as 6 lowercase hex chars followed
// by the 3-digit zero-padded record index. storage_sink_leaf builds each record's
// name this way from the file's pinned SHA-256 digest (CONN_PINNED_HASH) and
// FW_REC_IDX — content-addressing, not the filename.
func spanRecordName(nameBytes []byte, index int) string {
	return fmt.Sprintf("%02x%02x%02x%03d", nameBytes[0], nameBytes[1], nameBytes[2], index)
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

// enableBodySinkFilter arms the interposition seam on the loaded http_main binary:
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

// TestStreamThroughBodySink — the seam test: stream a full HTTP/1.0 response
// (header + body) through the composed bodySink interposition into the REAL store
// leaf, and assert (1) the store leaf HSAVE'd the body bytes only, one bounded
// record per flush window (header stripped), and (2) CONN_HASH ==
// crypto/sha256.Sum256(body) (digest over body only), a cryptographic proof that
// the exact streamed bytes are the body.
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

	body := []byte("firmware-body-bytes-for-the-seam-test-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	httpResp := fmt.Sprintf("HTTP/1.0 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)

	// Use the bootable http_main binary (carries both tcp_conn and body_sink) and
	// attach the REAL B-DOS store so storage_sink_leaf's rst 8 HSAVE runs. No card
	// is attached: the HSAVE write-back needs a card, and the record verdict here
	// is the cryptographic digest, not a card readback.
	mac := loadHTTPMainForStream(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)
	store := z80h.NewBDOSStore()
	mac.AttachBDOS(store)

	// Open a file in the store leaf so the record index is deterministic: store_begin
	// zeroes FW_REC_IDX (it takes no name input — record names are content-addressed
	// from CONN_PINNED_HASH by storage_sink_leaf). Pin the body's own SHA-256 as that
	// digest up-front (BEFORE the body streams), so each record is named
	// fw_span_record_name(pinnedHash, recIdx) — the same content-addressing the verify
	// (below) enforces. The pin is known up-front because HSAVE happens per-window
	// before conn_verify_final finalises the streamed hash.
	wantHash := sha256.Sum256(body)
	pinHash(t, mac, wantHash)
	if _, err := mac.Call("store_begin"); err != nil {
		t.Fatalf("store_begin: %v", err)
	}

	// Arm the seam and the SHA-256 verify before any data arrives.
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

	// Assert (1): the store leaf HSAVE'd the body as bounded records, header
	// stripped, one record per flush window. The AUTHORITY for the split is the Go
	// http.BodySink fed the same raw HTTP stream in the same window-sized flushes:
	// each recorded chunk is one HSAVE record's body size. (The window cut is over
	// the RAW stream, so the record holding the header carries fewer body bytes.)
	chunkSink := &tcp.ChunkSink{}
	bs := nbhttp.NewBodySink(chunkSink)
	for off := 0; off < len(httpResp); off += window {
		end := off + window
		if end > len(httpResp) {
			end = len(httpResp)
		}
		bs.Write([]byte(httpResp)[off:end])
	}
	saves := store.Saves()
	if len(saves) != len(chunkSink.Chunks) {
		t.Fatalf("store recorded %d HSAVE(s), want %d (Go BodySink chunk count)", len(saves), len(chunkSink.Chunks))
	}
	var total uint32
	for i, s := range saves {
		wantName := spanRecordName(wantHash[:], i)
		if s.Name != wantName {
			t.Errorf("record[%d] name = %q, want %q (fw_span_record_name of the pinned content hash)", i, s.Name, wantName)
		}
		wantSize := uint32(len(chunkSink.Chunks[i]))
		if s.Size != wantSize {
			t.Errorf("record[%d] size = %d, want %d (Go BodySink chunk size)", i, s.Size, wantSize)
		}
		total += s.Size
	}
	if total != uint32(len(body)) {
		t.Errorf("HSAVE'd bytes total %d, want %d (the whole body, header stripped)", total, len(body))
	}

	// Assert (2): CONN_HASH (after conn_verify_final with the correct pin — pinned
	// up-front above) must equal crypto/sha256.Sum256(body) — the digest is over the
	// body only, a cryptographic proof the streamed bytes are exactly the body
	// (header stripped).
	gotHash, match := verifyFinal(t, mac)
	if gotHash != wantHash {
		t.Errorf("CONN_HASH mismatch after bodySink interposition:\n  z80  %x\n  want %x\n(digest must be over body only, not including the HTTP header)",
			gotHash, wantHash)
	}
	if match != 1 {
		t.Errorf("CONN_HASH_MATCH = %d with correct pin, want 1", match)
	}
}
