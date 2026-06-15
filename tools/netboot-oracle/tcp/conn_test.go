package tcp

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
)

// Test fixtures: a client (the SAM) actively opening to a server (the firmware
// host). These mirror the captured-style identity used elsewhere in the oracle.
var (
	clientMAC  = frame.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	clientIP   = frame.IPv4{192, 0, 2, 44}
	clientPort = uint16(0xC000)
	serverMAC  = frame.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	serverIP   = frame.IPv4{192, 0, 2, 1}
	serverPort = uint16(80)
	iss        = uint32(0x11223344)
	serverISS  = uint32(0x99aabb00)
)

func newTestConn() *Conn {
	return NewConn(clientMAC, clientIP, clientPort, serverMAC, serverIP, serverPort, iss)
}

// serverSeg builds a segment as if sent by the server back to the client (src =
// server, dst = client) — what the test injects into Conn.OnSegment.
func serverSeg(seq, ack uint32, flags uint8, payload []byte) []byte {
	return BuildSegment(Segment{
		DstMAC:  clientMAC,
		SrcMAC:  serverMAC,
		SrcIP:   serverIP,
		DstIP:   clientIP,
		SrcPort: serverPort,
		DstPort: clientPort,
		Seq:     seq,
		Ack:     ack,
		Flags:   flags,
		Window:  5840,
		Payload: payload,
	})
}

// segFlags decodes the TCP flags byte of a built client segment for assertions.
func segFlags(t *testing.T, f []byte) uint8 {
	t.Helper()
	s, ok := ParseSegment(f)
	if !ok {
		t.Fatalf("built segment does not parse")
	}
	return s.Flags
}

func TestConnSYN(t *testing.T) {
	c := newTestConn()
	syn := c.Connect()
	s, ok := ParseSegment(syn)
	if !ok {
		t.Fatal("SYN does not parse")
	}
	if s.Flags != FlagSYN {
		t.Errorf("SYN flags = %#x, want %#x", s.Flags, FlagSYN)
	}
	if s.Seq != iss {
		t.Errorf("SYN seq = %#x, want ISS %#x", s.Seq, iss)
	}
	if s.Ack != 0 {
		t.Errorf("SYN ack = %#x, want 0", s.Ack)
	}
	if s.SrcPort != clientPort || s.DstPort != serverPort {
		t.Errorf("SYN ports = %d->%d, want %d->%d", s.SrcPort, s.DstPort, clientPort, serverPort)
	}
	if c.State != StateSynSent {
		t.Errorf("state = %d, want SynSent", c.State)
	}
}

func TestConnHandshake(t *testing.T) {
	c := newTestConn()
	c.Connect() // sndNxt = iss+1

	// Server SYN-ACK: its seq=serverISS, ack=iss+1.
	synAck := serverSeg(serverISS, iss+1, FlagSYN|FlagACK, nil)
	ack := c.OnSegment(synAck)
	if ack == nil {
		t.Fatal("no ACK in response to SYN-ACK")
	}
	s, _ := ParseSegment(ack)
	if s.Flags != FlagACK {
		t.Errorf("handshake ACK flags = %#x, want %#x", s.Flags, FlagACK)
	}
	if s.Seq != iss+1 {
		t.Errorf("ACK seq = %#x, want %#x", s.Seq, iss+1)
	}
	if s.Ack != serverISS+1 {
		t.Errorf("ACK ack = %#x, want %#x (server SYN consumes one)", s.Ack, serverISS+1)
	}
	if c.State != StateEstablished {
		t.Errorf("state = %d, want Established", c.State)
	}
}

func TestConnHandshakeRejectsWrongAck(t *testing.T) {
	c := newTestConn()
	c.Connect()
	// SYN-ACK acking the wrong sequence number: ignored.
	if r := c.OnSegment(serverSeg(serverISS, iss+99, FlagSYN|FlagACK, nil)); r != nil {
		t.Errorf("accepted SYN-ACK with wrong ack: %x", r)
	}
	if c.State != StateSynSent {
		t.Errorf("state advanced on a bad SYN-ACK: %d", c.State)
	}
}

func TestConnData(t *testing.T) {
	c := newTestConn()
	c.Connect()
	c.OnSegment(serverSeg(serverISS, iss+1, FlagSYN|FlagACK, nil))

	// Server sends data starting at its rcvNxt (serverISS+1).
	body := []byte("HTTP/1.0 200 OK\r\n\r\nhello")
	dataAck := c.OnSegment(serverSeg(serverISS+1, iss+1, FlagPSH|FlagACK, body))
	if dataAck == nil {
		t.Fatal("no ACK for in-order data")
	}
	s, _ := ParseSegment(dataAck)
	if s.Flags != FlagACK {
		t.Errorf("data ACK flags = %#x, want %#x", s.Flags, FlagACK)
	}
	if s.Ack != serverISS+1+uint32(len(body)) {
		t.Errorf("data ACK ack = %#x, want %#x", s.Ack, serverISS+1+uint32(len(body)))
	}
	if !bytes.Equal(c.Data, body) {
		t.Errorf("accumulated %q, want %q", c.Data, body)
	}
}

func TestConnDataIgnoresOutOfOrder(t *testing.T) {
	c := newTestConn()
	c.Connect()
	c.OnSegment(serverSeg(serverISS, iss+1, FlagSYN|FlagACK, nil))

	// A segment with a future sequence number (a gap) is ignored.
	if r := c.OnSegment(serverSeg(serverISS+100, iss+1, FlagPSH|FlagACK, []byte("x"))); r != nil {
		t.Errorf("ACKed an out-of-order segment: %x", r)
	}
	if len(c.Data) != 0 {
		t.Errorf("accumulated out-of-order data: %q", c.Data)
	}
}

func TestConnTeardownServerInitiated(t *testing.T) {
	c := newTestConn()
	c.Connect()
	c.OnSegment(serverSeg(serverISS, iss+1, FlagSYN|FlagACK, nil))
	body := []byte("body")
	c.OnSegment(serverSeg(serverISS+1, iss+1, FlagPSH|FlagACK, body))

	// Server FINs after the body. seq = serverISS+1+len(body).
	srvSeqAfterData := serverISS + 1 + uint32(len(body))
	finAck := c.OnSegment(serverSeg(srvSeqAfterData, iss+1, FlagFIN|FlagACK, nil))
	if finAck == nil {
		t.Fatal("no FIN-ACK in response to server FIN")
	}
	if got := segFlags(t, finAck); got != FlagFIN|FlagACK {
		t.Errorf("teardown flags = %#x, want %#x", got, FlagFIN|FlagACK)
	}
	s, _ := ParseSegment(finAck)
	if s.Ack != srvSeqAfterData+1 {
		t.Errorf("FIN-ACK ack = %#x, want %#x (server FIN consumes one)", s.Ack, srvSeqAfterData+1)
	}
	if c.State != StateFinWait {
		t.Errorf("state = %d, want FinWait", c.State)
	}

	// Final ACK of our FIN closes the connection (sndNxt advanced past our FIN).
	if r := c.OnSegment(serverSeg(srvSeqAfterData+1, c.sndNxt, FlagACK, nil)); r != nil {
		t.Errorf("replied to the final ACK: %x", r)
	}
	if c.State != StateClosed {
		t.Errorf("state = %d, want Closed", c.State)
	}
}

// establish drives a connection through Connect + the SYN-ACK so it is
// ESTABLISHED and ready to receive data, returning the server's first data seq.
func establish(t *testing.T, c *Conn) uint32 {
	t.Helper()
	c.Connect()
	c.OnSegment(serverSeg(serverISS, iss+1, FlagSYN|FlagACK, nil))
	if c.State != StateEstablished {
		t.Fatalf("not established after handshake: state %d", c.State)
	}
	return serverISS + 1
}

// feedBody splits body into payloads of at most segSize and delivers each as an
// in-order data segment, advancing the server seq. It asserts every segment is
// ACKed (the wire cadence is unchanged by streaming).
func feedBody(t *testing.T, c *Conn, srvSeq uint32, body []byte, segSize int) uint32 {
	t.Helper()
	for off := 0; off < len(body); off += segSize {
		end := off + segSize
		if end > len(body) {
			end = len(body)
		}
		seg := body[off:end]
		ack := c.OnSegment(serverSeg(srvSeq, iss+1, FlagPSH|FlagACK, seg))
		if ack == nil {
			t.Fatalf("no ACK for in-order data segment at offset %d", off)
		}
		s, _ := ParseSegment(ack)
		if s.Ack != srvSeq+uint32(len(seg)) {
			t.Fatalf("data ACK ack = %#x, want %#x", s.Ack, srvSeq+uint32(len(seg)))
		}
		srvSeq += uint32(len(seg))
	}
	return srvSeq
}

// finServer sends the server FIN and asserts the FIN-ACK teardown.
func finServer(t *testing.T, c *Conn, srvSeq uint32) {
	t.Helper()
	finAck := c.OnSegment(serverSeg(srvSeq, iss+1, FlagFIN|FlagACK, nil))
	if finAck == nil {
		t.Fatal("no FIN-ACK in response to server FIN")
	}
	if got := segFlags(t, finAck); got != FlagFIN|FlagACK {
		t.Fatalf("teardown flags = %#x, want %#x", got, FlagFIN|FlagACK)
	}
	if c.State != StateFinWait {
		t.Fatalf("state = %d, want FinWait", c.State)
	}
}

// assertChunks checks the recorded flushes: every chunk but possibly the last is
// exactly window bytes, none exceeds window, they are in order, and their
// concatenation equals body. Data must stay empty (streaming never touches it).
func assertChunks(t *testing.T, sink *ChunkSink, body []byte, window int, c *Conn) {
	t.Helper()
	if len(c.Data) != 0 {
		t.Errorf("Conn.Data grew in streaming mode (%d bytes); body must not accumulate", len(c.Data))
	}
	for i, ch := range sink.Chunks {
		if len(ch) > window {
			t.Errorf("chunk %d is %d bytes, exceeds window %d", i, len(ch), window)
		}
		if i < len(sink.Chunks)-1 && len(ch) != window {
			t.Errorf("non-final chunk %d is %d bytes, want a full window %d", i, len(ch), window)
		}
		if len(ch) == 0 {
			t.Errorf("chunk %d is empty; no flush should be zero-length", i)
		}
	}
	if !bytes.Equal(sink.Bytes(), body) {
		t.Errorf("streamed %d bytes, want %d; concatenation differs from body", len(sink.Bytes()), len(body))
	}
}

// TestConnStreamMultiWindow: a body that is an exact multiple of the window,
// delivered across several segments, streams as full windows with no remainder.
func TestConnStreamMultiWindow(t *testing.T) {
	c := newTestConn()
	sink := &ChunkSink{}
	c.SetSink(sink, 16)
	srvSeq := establish(t, c)

	body := bytes.Repeat([]byte("abcd"), 16) // 64 bytes = 4 full windows of 16
	srvSeq = feedBody(t, c, srvSeq, body, 7) // odd segment size straddles windows
	finServer(t, c, srvSeq)

	if got, want := len(sink.Chunks), 4; got != want {
		t.Errorf("chunk count = %d, want %d", got, want)
	}
	assertChunks(t, sink, body, 16, c)
}

// TestConnStreamPartialFinalFlush: a body whose length is NOT a multiple of the
// window leaves a partial remainder that flushes at the FIN.
func TestConnStreamPartialFinalFlush(t *testing.T) {
	c := newTestConn()
	sink := &ChunkSink{}
	c.SetSink(sink, 10)
	srvSeq := establish(t, c)

	body := []byte("0123456789ABCDEFGHIJKLMN") // 24 bytes: windows 10,10 + remainder 4
	srvSeq = feedBody(t, c, srvSeq, body, 5)
	if got := len(sink.Chunks); got != 2 {
		t.Errorf("before FIN: chunk count = %d, want 2 full windows (remainder not yet flushed)", got)
	}
	finServer(t, c, srvSeq) // the FIN flushes the final partial window

	if got, want := len(sink.Chunks), 3; got != want {
		t.Errorf("chunk count = %d, want %d (2 full + 1 partial)", got, want)
	}
	if last := sink.Chunks[len(sink.Chunks)-1]; len(last) != 4 {
		t.Errorf("final chunk = %d bytes, want the 4-byte remainder", len(last))
	}
	assertChunks(t, sink, body, 10, c)
}

// TestConnStreamFinFlushesRemainder: the FIN-carries-data path also streams and
// then flushes the remainder.
func TestConnStreamFinFlushesRemainder(t *testing.T) {
	c := newTestConn()
	sink := &ChunkSink{}
	c.SetSink(sink, 8)
	srvSeq := establish(t, c)

	body := []byte("HELLOWORLD") // 10 bytes: one full window of 8 + remainder 2
	finData := c.OnSegment(serverSeg(srvSeq, iss+1, FlagFIN|FlagPSH|FlagACK, body))
	if finData == nil {
		t.Fatal("no FIN-ACK for FIN-with-data")
	}
	if got, want := len(sink.Chunks), 2; got != want {
		t.Errorf("chunk count = %d, want %d (1 full window + remainder flushed at FIN)", got, want)
	}
	assertChunks(t, sink, body, 8, c)
}

// TestConnNoSinkUntouched: with no sink set, the data path is byte-identical to
// the legacy behavior — body accumulates in Data and nothing streams.
func TestConnNoSinkUntouched(t *testing.T) {
	c := newTestConn()
	srvSeq := establish(t, c)
	body := []byte("HTTP/1.0 200 OK\r\n\r\nthe quick brown fox")
	srvSeq = feedBody(t, c, srvSeq, body, 9)
	finServer(t, c, srvSeq)
	if !bytes.Equal(c.Data, body) {
		t.Errorf("accumulated %q, want %q", c.Data, body)
	}
}

func TestConnIgnoresWrongPort(t *testing.T) {
	c := newTestConn()
	c.Connect()
	// A SYN-ACK from a different server port is not for this connection.
	bad := BuildSegment(Segment{
		DstMAC: clientMAC, SrcMAC: serverMAC, SrcIP: serverIP, DstIP: clientIP,
		SrcPort: 443, DstPort: clientPort, Seq: serverISS, Ack: iss + 1,
		Flags: FlagSYN | FlagACK, Window: 5840,
	})
	if r := c.OnSegment(bad); r != nil {
		t.Errorf("accepted a segment from the wrong server port: %x", r)
	}
	if c.State != StateSynSent {
		t.Errorf("state advanced on a wrong-port segment: %d", c.State)
	}
}
