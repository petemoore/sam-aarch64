// tls_transcript_test.go — the i88 host-verification of the TLS 1.3 handshake
// transcript hash (src/netboot/tls_transcript.asm), handshake brick 5. It
// assembles the standalone module under the koron-go/z80 harness, feeds message
// sequences with interleaved snapshots, and asserts each snapshot equals Go
// crypto/sha256 of the concatenation of all messages folded so far — proving the
// snapshot's save/restore leaves the running hash intact (incl. across the 64-byte
// block boundary) and that an empty-transcript snapshot is SHA-256("").
package z80_test

import (
	"bytes"
	"crypto/sha256"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	ttBinPath = "../../../build/netboot_tls_transcript.bin"
	ttMapPath = "../../../build/netboot_tls_transcript.map"

	ttMsgStage = 0x4000
	ttOutStage = 0x5000
)

func loadTT(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(ttBinPath); err != nil {
		t.Fatalf("tls_transcript binary not built (%s); run `make netboot-tls-transcript`", ttBinPath)
	}
	mac, err := z80h.Load(ttBinPath, ttMapPath)
	if err != nil {
		t.Fatalf("load tls_transcript: %v", err)
	}
	return mac
}

// TestTLSTranscript: across a sequence of handshake-message-sized blobs (incl.
// lengths straddling the 64-byte SHA-256 block boundary), each snapshot equals
// crypto/sha256 of everything folded so far, and the stream keeps growing after a
// snapshot. The same machine is reused so the SHA-256 state persists across calls.
func TestTLSTranscript(t *testing.T) {
	mac := loadTT(t)

	snapshot := func() []byte {
		if _, err := mac.CallEntry("tls_transcript_snapshot", z80h.Entry{HL: ttOutStage}); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		return mac.Read(ttOutStage, 32)
	}
	update := func(msg []byte) {
		mac.Write(ttMsgStage, msg)
		if _, err := mac.CallEntry("tls_transcript_update", z80h.Entry{HL: ttMsgStage, BC: uint16(len(msg))}); err != nil {
			t.Fatalf("update: %v", err)
		}
	}

	if _, err := mac.Call("tls_transcript_init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// An empty-transcript snapshot is SHA-256("").
	want0 := sha256.Sum256(nil)
	if got := snapshot(); !bytes.Equal(got, want0[:]) {
		t.Errorf("empty snapshot = %x, want SHA-256(\"\") %x", got, want0[:])
	}

	// Message lengths chosen to land before/at/after the 64-byte block boundary
	// and to make the running total cross it repeatedly.
	lengths := []int{1, 13, 50, 1, 64, 65, 100, 31, 200, 7}
	var concat []byte
	for i, n := range lengths {
		msg := make([]byte, n)
		for j := range msg {
			msg[j] = byte(i*101 + j*7 + 3)
		}
		update(msg)
		concat = append(concat, msg...)
		want := sha256.Sum256(concat)
		if got := snapshot(); !bytes.Equal(got, want[:]) {
			t.Errorf("snapshot after msg %d (total %d bytes) = %x, want %x", i, len(concat), got, want[:])
		}
	}

	// One more update after the last snapshot must still extend the same hash
	// (proving the final snapshot restored the state).
	tail := []byte("final handshake fragment")
	update(tail)
	concat = append(concat, tail...)
	want := sha256.Sum256(concat)
	if got := snapshot(); !bytes.Equal(got, want[:]) {
		t.Errorf("snapshot after tail = %x, want %x", got, want[:])
	}
}
