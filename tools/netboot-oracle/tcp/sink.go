package tcp

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
