// tls_integration_test.go verifies the i88c-b1 TLS 6b integration composition
// (src/netboot/tls_main.asm): the wiring that connects the i88b record
// reassembler (tls_reasm.asm) to the i88a handshake state machine
// (tls_client.asm). It drives the SAME captured TLS 1.3 handshake that
// tls_client_test.go's TestTLSClientHandshakeReplay replays — but instead of
// feeding one whole record at a time, it concatenates the server flight into a
// single TCP byte stream, splits it into mis-aligned chunks, and feeds each chunk
// to tls_reasm_feed (the CONN_SINK_FILTER target). The reassembler must frame each
// record out of the chunk boundaries and drive the 6a machine — via the new
// tls_record_shim emit call-through — to DONE, reproducing the captured client
// Finished record and all four traffic secrets byte-for-byte.
//
// This is the emulation-first proof of the wiring (CLAUDE.md §7) before the
// hardware-gated legs (i88c-b2 paged layout / -b3 disk / -b4 RST-8 shot).
package z80_test

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tls"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	tlsMainBinPath = "../../../build/netboot_tls_main.bin"
	tlsMainMapPath = "../../../build/netboot_tls_main.map"
	// reasmBufBound is the REASM_MAX the flat host build pre-sets in tls_main.asm.
	// The captured records are <=332 B; a real github.com Certificate record needs
	// the full 16645 B buffer + a paged layout (i88c-b2 / q72), so a fixture that
	// grew past this bound must FAIL loudly here, not silently overflow REASM_BUF.
	reasmBufBound = 512
	// chunkStage is a free low-RAM scratch address (below the harness stack top
	// &6FFE and the &7000 HALT trap, clear of the &8000+ composite) where the test
	// stages each inbound chunk before handing it to tls_reasm_feed. The
	// reassembler copies the chunk into REASM_BUF at the start of the feed, before
	// any deep x25519 stack growth, so the staging area is never live across it.
	chunkStage = 0x2000
)

func loadTLSMain(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(tlsMainBinPath); err != nil {
		t.Fatalf("tls_main binary not built (%s); run `make netboot-tls-main`", tlsMainBinPath)
	}
	mac, err := z80h.Load(tlsMainBinPath, tlsMainMapPath)
	if err != nil {
		t.Fatalf("load tls_main: %v", err)
	}
	return mac
}

// initTLSMain loads the composition, arms the sink wiring (REASM_EMIT_PTR ->
// tls_record_shim, exactly what the composed bootable sets at init), resets the
// reassembler, and runs the client's own init/first from the capture's inputs —
// the shared setup, leaving the machine at PhaseSentCH ready for inbound chunks.
func initTLSMain(t *testing.T, cap *tls.Capture) *z80h.Machine {
	t.Helper()
	mac := loadTLSMain(t)

	// The reassembler module must sit below the x25519 qsq_table scratch (&FB00);
	// if a change pushed it up, the flat build silently corrupts qsq_table.
	if end := mustSym(t, mac, "tls_main_end"); int(end) >= qsqTableComposite {
		t.Fatalf("tls_main image top %#x reaches qsq_table %#x — shrink REASM_MAX or page (i88c-b2)",
			end, qsqTableComposite)
	}

	// Arm the wiring: REASM_EMIT_PTR = tls_record_shim, then reset the reassembler.
	mac.WriteU16LE(mustSym(t, mac, "REASM_EMIT_PTR"), mustSym(t, mac, "tls_record_shim"))
	if _, err := mac.CallEntry("tls_reasm_init", z80h.Entry{}); err != nil {
		t.Fatalf("tls_reasm_init: %v", err)
	}

	// The client's own setup (identical to tls_client_test.go's initFirstClient).
	mac.Write(mustSym(t, mac, "TC_CLIENT_PRIV"), cap.Priv[:])
	mac.Write(mustSym(t, mac, "CH_RANDOM"), cap.Random[:])
	mac.Write(mustSym(t, mac, "CH_SESSION_ID"), cap.Sid[:])
	mac.Write(mustSym(t, mac, "CH_HOSTNAME"), []byte("github.com"))
	mac.WriteU16LE(mustSym(t, mac, "CH_HOSTNAME_LEN"), uint16(len("github.com")))
	if _, err := mac.CallEntry("tls_client_init", z80h.Entry{StepCap: x25519StepCap}); err != nil {
		t.Fatalf("tls_client_init: %v", err)
	}
	if _, err := mac.CallEntry("tls_client_first", z80h.Entry{}); err != nil {
		t.Fatalf("tls_client_first: %v", err)
	}
	txLen := readU16LE(mac, mustSym(t, mac, "TC_TX_LEN"))
	if gotTx := mac.Read(mustSym(t, mac, "TC_TX"), txLen); !bytes.Equal(gotTx, cap.CHRecord) {
		t.Fatalf("ClientHello record mismatch:\n got %x\nwant %x", gotTx, cap.CHRecord)
	}
	return mac
}

// inboundStream concatenates the capture's inbound records into one byte stream —
// exactly the bytes the server sends, which tcp_conn's sink hands us in chunks.
// A record over the flat build's REASM_MAX fails loudly (see reasmBufBound).
func inboundStream(t *testing.T, cap *tls.Capture) []byte {
	t.Helper()
	var stream []byte
	for _, rec := range cap.Inbound {
		if len(rec) > reasmBufBound {
			t.Fatalf("capture record type 0x%02x is %d B, over the flat build's REASM_MAX (%d); "+
				"the full-size path is i88c-b2 (paged). Fixture changed?", rec[0], len(rec), reasmBufBound)
		}
		stream = append(stream, rec...)
	}
	return stream
}

// feedChunk stages one chunk at chunkStage and runs tls_reasm_feed (the
// CONN_SINK_FILTER target) over it. Completed records emit through tls_record_shim
// into tls_client_on_record during this call (the ServerHello chunk runs the ECDHE
// x25519 ladder, hence the raised step cap).
func feedChunk(t *testing.T, mac *z80h.Machine, chunk []byte) {
	t.Helper()
	mac.Write(chunkStage, chunk)
	if _, err := mac.CallEntry("tls_reasm_feed",
		z80h.Entry{HL: chunkStage, BC: uint16(len(chunk)), StepCap: x25519StepCap}); err != nil {
		t.Fatalf("tls_reasm_feed (%d-byte chunk): %v", len(chunk), err)
	}
}

// splitChunks slices b into consecutive chunks of at most size bytes (size <= 0
// means one whole chunk).
func splitChunks(b []byte, size int) [][]byte {
	if size <= 0 || size >= len(b) {
		return [][]byte{b}
	}
	var out [][]byte
	for i := 0; i < len(b); i += size {
		j := i + size
		if j > len(b) {
			j = len(b)
		}
		out = append(out, b[i:j])
	}
	return out
}

func captureGithub(t *testing.T) *tls.Capture {
	t.Helper()
	var priv, random, sid [32]byte
	for i := range priv {
		priv[i] = byte(i + 1)
		random[i] = byte(i*3 + 7)
		sid[i] = byte(i*5 + 0x40)
	}
	cap, err := tls.CaptureHandshake("github.com", priv, random, sid)
	if err != nil {
		t.Fatalf("CaptureHandshake: %v", err)
	}
	return cap
}

// TestTLSIntegrationChunkedHandshake drives the whole server flight through the
// reassembler+shim wiring under two chunkings and asserts the handshake reaches
// DONE reproducing the Go authority's outputs. chunkSize 13 splits record headers
// and bodies across feeds (records completed across calls); the whole-stream chunk
// coalesces all records into one feed (several records emitted back-to-back within
// one tls_reasm_feed loop) — the two distinct integration paths.
func TestTLSIntegrationChunkedHandshake(t *testing.T) {
	cap := captureGithub(t)
	stream := inboundStream(t, cap)

	for _, chunkSize := range []int{13, 0 /* whole stream */} {
		chunkSize := chunkSize
		name := fmt.Sprintf("chunk_%d", chunkSize)
		if chunkSize == 0 {
			name = "whole_stream"
		}
		t.Run(name, func(t *testing.T) {
			mac := initTLSMain(t, cap)
			for _, chunk := range splitChunks(stream, chunkSize) {
				feedChunk(t, mac, chunk)
			}

			if phase := mac.Read(mustSym(t, mac, "TC_PHASE"), 1)[0]; phase != tcPhaseDone {
				t.Fatalf("after the whole flight: TC_PHASE=%d, want DONE(%d)", phase, tcPhaseDone)
			}
			if st := mac.Read(mustSym(t, mac, "TC_STATUS"), 1)[0]; st != tcStatusDone {
				t.Fatalf("TC_STATUS=%d, want DONE(%d)", st, tcStatusDone)
			}
			// The client Finished record + all four traffic secrets, byte-for-byte.
			txLen := readU16LE(mac, mustSym(t, mac, "TC_TX_LEN"))
			if gotTx := mac.Read(mustSym(t, mac, "TC_TX"), txLen); !bytes.Equal(gotTx, cap.FinRecord) {
				t.Fatalf("client Finished mismatch (got %d B, want %d):\n got %x\nwant %x",
					txLen, len(cap.FinRecord), gotTx, cap.FinRecord)
			}
			for _, s := range []struct {
				sym  string
				want []byte
			}{
				{"KS_CHS", cap.CHS}, {"KS_SHS", cap.SHS}, {"KS_CAP", cap.CAP}, {"KS_SAP", cap.SAP},
			} {
				if got := mac.Read(mustSym(t, mac, s.sym), 32); !bytes.Equal(got, s.want) {
					t.Fatalf("%s mismatch:\n got %x\nwant %x", s.sym, got, s.want)
				}
			}
		})
	}
}

// TestTLSIntegrationTamperedFlightRejected is the negative control: flip one
// ciphertext byte of the encrypted flight in the reassembled stream. The AEAD tag
// must reject it — TC_PHASE=ERROR — through the chunked sink path (proving the
// wiring surfaces a failure, not just a success). Feeding stops at the first
// ERROR (the state machine's post-error behaviour is out of scope here).
func TestTLSIntegrationTamperedFlightRejected(t *testing.T) {
	cap := captureGithub(t)
	stream := inboundStream(t, cap)

	// Locate the first inbound 0x17 (application_data) record and tamper a byte
	// inside its ciphertext (past the 5-byte record header) at its stream offset.
	off, found := 0, false
	for _, rec := range cap.Inbound {
		if rec[0] == 0x17 {
			found = true
			break
		}
		off += len(rec)
	}
	if !found {
		t.Fatal("capture is missing its encrypted flight (no inbound 0x17) — fixture changed")
	}
	tampered := append([]byte(nil), stream...)
	tampered[off+6] ^= 0x01 // a ciphertext byte of the flight record

	mac := initTLSMain(t, cap)
	gotError := false
	for _, chunk := range splitChunks(tampered, 7) {
		feedChunk(t, mac, chunk)
		if mac.Read(mustSym(t, mac, "TC_PHASE"), 1)[0] == tcPhaseError {
			gotError = true
			break
		}
	}
	if !gotError {
		t.Fatal("tampered flight accepted through the chunked sink; expected TC_PHASE=ERROR")
	}
}
