package tcp

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

// TestHashingSinkSumMatchesCryptoSHA256 streams a body through HashingSink in
// several chunks and asserts the running digest equals crypto/sha256.Sum256 of
// the whole body — the Go authority for the Z80 streamed-body verify. The inner
// ChunkSink must still capture the bytes (HashingSink only adds the hash).
func TestHashingSinkSumMatchesCryptoSHA256(t *testing.T) {
	body := make([]byte, 250)
	for i := range body {
		body[i] = byte('A' + i%26)
	}
	inner := &ChunkSink{}
	hs := NewHashingSink(inner)

	// Stream the body in awkward, non-uniform chunks (mirrors the bounded
	// flush windows the connection produces).
	for off := 0; off < len(body); {
		n := 17
		if off+n > len(body) {
			n = len(body) - off
		}
		hs.Write(body[off : off+n])
		off += n
	}

	want := sha256.Sum256(body)
	if got := hs.Sum(); got != want {
		t.Errorf("Sum mismatch\n got %x\n want %x", got, want)
	}
	// The inner sink must still have received every byte, in order.
	if !bytes.Equal(inner.Bytes(), body) {
		t.Errorf("inner sink body mismatch\n got %q\n want %q", inner.Bytes(), body)
	}
}

// TestHashingSinkVerify asserts Verify returns true for the correct hash and
// false for a wrong one — the Go analogue of conn_verify_final's CONN_HASH ==
// CONN_PINNED_HASH check.
func TestHashingSinkVerify(t *testing.T) {
	body := bytes.Repeat([]byte("firmware"), 40) // 320 bytes
	hs := NewHashingSink(&ChunkSink{})
	for off := 0; off < len(body); off += 23 {
		end := off + 23
		if end > len(body) {
			end = len(body)
		}
		hs.Write(body[off:end])
	}

	correct := sha256.Sum256(body)
	if !hs.Verify(correct) {
		t.Errorf("Verify(correct) = false, want true")
	}
	wrong := correct
	wrong[0] ^= 0xFF
	if hs.Verify(wrong) {
		t.Errorf("Verify(wrong) = true, want false")
	}
}

// TestHashingSinkNilInner: HashingSink hashes correctly with no inner sink (a
// pure hash-only mode), so it composes with a real storage sink that is bound
// separately.
func TestHashingSinkNilInner(t *testing.T) {
	body := []byte("hello, streamed world")
	hs := NewHashingSink(nil)
	hs.Write(body)
	if got, want := hs.Sum(), sha256.Sum256(body); got != want {
		t.Errorf("nil-inner Sum mismatch\n got %x\n want %x", got, want)
	}
}

// TestHashingSinkEmptyBody: a zero-byte body hashes to the empty-string digest.
func TestHashingSinkEmptyBody(t *testing.T) {
	hs := NewHashingSink(&ChunkSink{})
	if got, want := hs.Sum(), sha256.Sum256(nil); got != want {
		t.Errorf("empty Sum mismatch\n got %x\n want %x", got, want)
	}
}
