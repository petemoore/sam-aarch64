package tcp

import (
	"crypto/sha256"
	"hash"
)

// Sink is the streaming target for an inbound TCP body — the Go-authority
// abstraction the i99 streaming receive routes payload into instead of growing
// Conn.Data without bound. On real hardware this is the B-DOS bounded write (the
// "flush a window to Trinity storage" step, deferred to q16); in the host tests
// it is a recording double (ChunkSink). The contract is deliberately minimal so
// the Z80 port has nothing to interpret: each Write is one bounded flush of body
// bytes, in order; the connection never hands the sink more than flushWindow
// bytes in a single Write.
//
// A sink is HTTP-agnostic: it streams whatever body bytes the connection gives
// it. The HTTP header skip (the first sink Write must begin at the first body
// byte, not at the response header) lives one layer up, in the http.Fetcher —
// see http.bodySink. This keeps tcp.Conn a pure byte transport with no HTTP
// knowledge.
type Sink interface {
	// Write records one bounded flush of body bytes. The slice is owned by the
	// caller after the call returns; an implementation that retains it must copy.
	Write(chunk []byte)
}

// ChunkSink is a recording Sink for the host tests: it captures each Write as a
// separate chunk so a test can assert the boundaries, ordering, and sizes the
// streaming receive produced, then concatenate them to compare against the body.
// It is the streaming analogue of asserting against an accumulated Conn.Data.
type ChunkSink struct {
	Chunks [][]byte // one entry per Write, in arrival order
}

// Write appends a copy of chunk as a distinct recorded flush. The copy is
// deliberate: the connection reuses its flush buffer across Writes, so retaining
// the caller's slice would alias later flushes.
func (s *ChunkSink) Write(chunk []byte) {
	cp := make([]byte, len(chunk))
	copy(cp, chunk)
	s.Chunks = append(s.Chunks, cp)
}

// Bytes returns the concatenation of every recorded flush — the full body the
// stream delivered, for comparison against the expected body.
func (s *ChunkSink) Bytes() []byte {
	var out []byte
	for _, c := range s.Chunks {
		out = append(out, c...)
	}
	return out
}

// HashingSink wraps an inner Sink and runs SHA-256 over every Write before
// forwarding it — the Go authority for the streamed-body verify (i100, q15
// option c). It hashes the body incrementally AS it streams (never buffering the
// whole body), exactly mirroring the Z80 path: storage_sink_flush feeds each
// flushed window through sha256_update, then conn_verify_final compares the
// digest against the pinned hash. Sum() returns the running digest; Verify
// compares it against an expected hash. The inner Sink still records / persists
// the bytes; HashingSink only adds the hash, so it composes with ChunkSink (host
// tests) or a real storage sink (hardware) without changing where bytes land.
type HashingSink struct {
	inner Sink
	h     hash.Hash
}

// NewHashingSink wraps inner so every Write is hashed (SHA-256) and then
// forwarded to inner. inner may be nil to hash without recording.
func NewHashingSink(inner Sink) *HashingSink {
	return &HashingSink{inner: inner, h: sha256.New()}
}

// Write hashes the chunk into the running SHA-256, then forwards it to the inner
// sink. The order matches the Z80 (hash the window, then record/persist it), so
// every body byte is hashed exactly once, in arrival order.
func (s *HashingSink) Write(chunk []byte) {
	s.h.Write(chunk)
	if s.inner != nil {
		s.inner.Write(chunk)
	}
}

// Sum returns the SHA-256 digest of everything streamed so far — equal to
// sha256.Sum256(body) once the full body has been written, byte-for-byte the
// value conn_verify_final writes into CONN_HASH on the Z80.
func (s *HashingSink) Sum() [32]byte {
	var out [32]byte
	copy(out[:], s.h.Sum(nil))
	return out
}

// Verify reports whether the streamed body's digest equals expected — the Go
// analogue of conn_verify_final's CONN_HASH == CONN_PINNED_HASH check.
func (s *HashingSink) Verify(expected [32]byte) bool {
	return s.Sum() == expected
}
