// tcp_conn_stream_test.go — the i99 host-verification of the OPT-IN streaming
// sink in src/netboot/tcp_conn.asm. It runs the *same* composed tcp_conn module
// as tcp_conn_test.go, but opts the connection into streaming mode (writes
// CONN_SINK_ENABLED=1 + a small CONN_FLUSH_WINDOW before any data arrives),
// drives a multi-segment body through tcp_conn_recv, then the FIN, and asserts
// the recording test-double sink (CONN_SINK_OUT / CONN_SINK_CHUNKS) captured the
// body byte-for-byte across bounded flushes — the streaming analogue of
// tcp_conn_test.go's accumulated-CONN_DATA assert. This mirrors the Go-authority
// streaming tests added in #263 (tcp/conn_test.go::TestConnStream*).
//
// The wire frames are NOT re-checked here (tcp_conn_test.go already proves the
// default path is byte-for-byte the Go authority); this test only checks that
// turning the sink ON does not change the wire and routes the body into bounded
// flushes. The real B-DOS bounded write is q16/hardware-gated and out of scope —
// storage_sink_flush is a recording double in this build.
package z80_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// enableSink turns on streaming mode in the loaded machine: CONN_SINK_ENABLED=1
// and CONN_FLUSH_WINDOW=window. Must be called before any data segment arrives
// (the streaming spec is "flush as it arrives", no retroactive draining).
func enableSink(t *testing.T, mac *z80h.Machine, window uint16) {
	t.Helper()
	put := func(name string, data []byte) {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("%v", err)
		}
		mac.Write(a, data)
	}
	put("CONN_SINK_ENABLED", []byte{1})
	// CONN_FLUSH_WINDOW is read with `ld hl,(addr)` → little-endian in memory.
	put("CONN_FLUSH_WINDOW", []byte{byte(window), byte(window >> 8)})
}

// readWord reads a 16-bit little-endian value at symbol name.
func readWord(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return binary.LittleEndian.Uint16(mac.Read(a, 2))
}

// sinkOut returns the bytes the test-double sink streamed (CONN_SINK_OUT, length
// CONN_SINK_OUT_LEN).
func sinkOut(t *testing.T, mac *z80h.Machine) []byte {
	t.Helper()
	n := readWord(t, mac, "CONN_SINK_OUT_LEN")
	a, err := mac.Sym("CONN_SINK_OUT")
	if err != nil {
		t.Fatalf("%v", err)
	}
	return mac.Read(a, int(n))
}

// sinkChunks returns the per-flush lengths the sink recorded (CONN_SINK_CHUNKS,
// CONN_SINK_CHUNK_COUNT entries, 2 little-endian bytes each).
func sinkChunks(t *testing.T, mac *z80h.Machine) []uint16 {
	t.Helper()
	n := readWord(t, mac, "CONN_SINK_CHUNK_COUNT")
	a, err := mac.Sym("CONN_SINK_CHUNKS")
	if err != nil {
		t.Fatalf("%v", err)
	}
	raw := mac.Read(a, int(n)*2)
	out := make([]uint16, n)
	for i := range out {
		out[i] = binary.LittleEndian.Uint16(raw[i*2 : i*2+2])
	}
	return out
}

// establishStream runs SYN -> SYN-ACK -> ACK on BOTH the Z80 machine and the Go
// authority ref (so ref tracks the same state when data arrives) and returns the
// server's next seq.
func establishStream(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, ref *tcp.Conn) uint32 {
	t.Helper()
	c := tcpConnCfg
	connect(t, mac, enc)
	synAck := serverSeg(c.serverISS, c.iss+1, tcp.FlagSYN|tcp.FlagACK, nil)
	got, want := recvOnce(t, mac, enc, synAck), ref.OnSegment(synAck)
	if got == nil || want == nil {
		t.Fatalf("no handshake ACK (z80 nil=%v, go nil=%v)", got == nil, want == nil)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("handshake ACK != Go\n  z80 %x\n  go  %x", got, want)
	}
	return c.serverISS + 1
}

// feedBodyZ80 delivers body across segments of segLen bytes, each ACKed, and
// returns the server's next seq. It cross-checks the Z80 ACK against the Go
// authority so the wire stays byte-identical with the sink on.
func feedBodyZ80(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, ref *tcp.Conn, srvSeq uint32, body []byte, segLen int) uint32 {
	t.Helper()
	c := tcpConnCfg
	for off := 0; off < len(body); off += segLen {
		end := off + segLen
		if end > len(body) {
			end = len(body)
		}
		seg := serverSeg(srvSeq, c.iss+1, tcp.FlagPSH|tcp.FlagACK, body[off:end])
		got, want := recvOnce(t, mac, enc, seg), ref.OnSegment(seg)
		if !bytes.Equal(got, want) {
			t.Fatalf("data ACK != Go (sink on must not change the wire)\n  z80 %x\n  go  %x", got, want)
		}
		srvSeq += uint32(end - off)
	}
	return srvSeq
}

// finStream sends the server FIN and checks the Z80 FIN-ACK matches the Go
// authority.
func finStream(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, ref *tcp.Conn, srvSeq uint32) {
	t.Helper()
	c := tcpConnCfg
	fin := serverSeg(srvSeq, c.iss+1, tcp.FlagFIN|tcp.FlagACK, nil)
	got, want := recvOnce(t, mac, enc, fin), ref.OnSegment(fin)
	if !bytes.Equal(got, want) {
		t.Fatalf("FIN-ACK != Go (sink on must not change the wire)\n  z80 %x\n  go  %x", got, want)
	}
}

// goConnStream builds a Go authority connection with the matching streaming sink
// so the test can assert the Z80 sink output equals the Go sink output.
func goConnStream(window int) (*tcp.Conn, *tcp.ChunkSink) {
	c := goConn()
	sink := &tcp.ChunkSink{}
	c.SetSink(sink, window)
	return c, sink
}

// assertStream checks the Z80 sink captured the body: the concatenation equals
// body, every chunk but possibly the last is exactly window bytes, none exceeds
// window, none is zero-length, and CONN_DATA stayed empty (streaming never
// touches it). It also cross-checks the Z80 chunk boundaries against the Go
// authority's ChunkSink.
func assertStream(t *testing.T, mac *z80h.Machine, goSink *tcp.ChunkSink, body []byte, window uint16) {
	t.Helper()
	got := sinkOut(t, mac)
	if !bytes.Equal(got, body) {
		t.Errorf("streamed bytes != body\n  z80 %q\n  exp %q", got, body)
	}
	chunks := sinkChunks(t, mac)
	if len(chunks) == 0 {
		t.Fatal("no chunks recorded")
	}
	var total int
	for i, ln := range chunks {
		if ln == 0 {
			t.Errorf("chunk %d is zero-length; no flush should be empty", i)
		}
		if ln > window {
			t.Errorf("chunk %d is %d bytes, exceeds window %d", i, ln, window)
		}
		if i < len(chunks)-1 && ln != window {
			t.Errorf("non-final chunk %d is %d bytes, want a full window %d", i, ln, window)
		}
		total += int(ln)
	}
	if total != len(body) {
		t.Errorf("chunk lengths sum to %d, want body length %d", total, len(body))
	}
	// Cross-check the Z80 chunk boundaries against the Go authority.
	if len(chunks) != len(goSink.Chunks) {
		t.Errorf("Z80 recorded %d chunks, Go authority %d", len(chunks), len(goSink.Chunks))
	} else {
		for i, ch := range goSink.Chunks {
			if int(chunks[i]) != len(ch) {
				t.Errorf("chunk %d: z80 %d bytes, go %d bytes", i, chunks[i], len(ch))
			}
		}
	}
	// Streaming must not accumulate into CONN_DATA.
	if n := readWord(t, mac, "CONN_DATA_LEN"); n != 0 {
		t.Errorf("CONN_DATA_LEN = %d in streaming mode; body must not accumulate", n)
	}
}

// TestTCPConnStreamPartialFinalFlush: a body whose length is NOT a multiple of
// the window streams full windows as it fills and flushes the partial remainder
// at the FIN. window(10) < body(24) → 2 full windows + a 4-byte remainder, ≥2
// flushes, a non-window-multiple total, and the FIN-flush — the i99 design's
// required coverage.
func TestTCPConnStreamPartialFinalFlush(t *testing.T) {
	mac := loadTCPConn(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	const window = 10
	enableSink(t, mac, window)
	ref, goSink := goConnStream(window)
	ref.Connect()

	srvSeq := establishStream(t, mac, enc, ref)
	body := []byte("0123456789ABCDEFGHIJKLMN") // 24 bytes: windows 10,10 + remainder 4
	srvSeq = feedBodyZ80(t, mac, enc, ref, srvSeq, body, 5)

	// Before the FIN: two full windows flushed, the 4-byte remainder buffered.
	if got := readWord(t, mac, "CONN_SINK_CHUNK_COUNT"); got != 2 {
		t.Errorf("before FIN: chunk count = %d, want 2 full windows (remainder not yet flushed)", got)
	}

	finStream(t, mac, enc, ref, srvSeq)

	if got := readWord(t, mac, "CONN_SINK_CHUNK_COUNT"); got != 3 {
		t.Errorf("after FIN: chunk count = %d, want 3 (2 full + 1 partial)", got)
	}
	chunks := sinkChunks(t, mac)
	if last := chunks[len(chunks)-1]; last != 4 {
		t.Errorf("final chunk = %d bytes, want the 4-byte remainder", last)
	}
	assertStream(t, mac, goSink, body, window)
}

// TestTCPConnStreamMultiWindow: a body that is an exact multiple of the window,
// delivered across odd-sized segments that straddle window boundaries, streams
// as full windows with no remainder (the FIN flushes nothing).
func TestTCPConnStreamMultiWindow(t *testing.T) {
	mac := loadTCPConn(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	const window = 16
	enableSink(t, mac, window)
	ref, goSink := goConnStream(window)
	ref.Connect()

	srvSeq := establishStream(t, mac, enc, ref)
	body := bytes.Repeat([]byte("abcd"), 16)                // 64 bytes = 4 full windows of 16
	srvSeq = feedBodyZ80(t, mac, enc, ref, srvSeq, body, 7) // odd seg size straddles windows

	if got := readWord(t, mac, "CONN_SINK_CHUNK_COUNT"); got != 4 {
		t.Errorf("before FIN: chunk count = %d, want 4 full windows", got)
	}
	finStream(t, mac, enc, ref, srvSeq)
	// The FIN must NOT emit a zero-length flush when the buffer is empty.
	if got := readWord(t, mac, "CONN_SINK_CHUNK_COUNT"); got != 4 {
		t.Errorf("after FIN: chunk count = %d, want 4 (no spurious zero-length flush)", got)
	}
	assertStream(t, mac, goSink, body, window)
}

// TestTCPConnStreamMatchesGoBody: a larger, multi-segment body with a window
// well below the body size streams the body byte-for-byte, with the chunk
// boundaries identical to the Go authority's. This is the streaming analogue of
// TestTCPConnFullLifecycle's accumulated-body assert.
func TestTCPConnStreamMatchesGoBody(t *testing.T) {
	mac := loadTCPConn(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	const window = 24
	enableSink(t, mac, window)
	ref, goSink := goConnStream(window)
	ref.Connect()

	srvSeq := establishStream(t, mac, enc, ref)
	// 100 bytes, not a multiple of 24 → 4 full windows + a 4-byte remainder.
	body := make([]byte, 100)
	for i := range body {
		body[i] = byte('A' + i%26)
	}
	srvSeq = feedBodyZ80(t, mac, enc, ref, srvSeq, body, 13) // segments straddle windows
	finStream(t, mac, enc, ref, srvSeq)

	if got := readWord(t, mac, "CONN_SINK_CHUNK_COUNT"); got < 2 {
		t.Errorf("chunk count = %d, want >=2 (window < body)", got)
	}
	assertStream(t, mac, goSink, body, window)
}
