package http

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
)

// Fetcher test fixtures: a SAM client fetching from a firmware server.
var (
	fClientMAC = frame.MAC{0x02, 0, 0, 0, 0, 0x44}
	fClientIP  = frame.IPv4{192, 0, 2, 44}
	fClientPrt = uint16(0xC000)
	fServerMAC = frame.MAC{0x02, 0, 0, 0, 0, 0x01}
	fServerIP  = frame.IPv4{192, 0, 2, 1}
	fServerPrt = uint16(80)
	fISS       = uint32(0x11223344)
	fServerISS = uint32(0x99aabb00)
	fPath      = "/firmware/start4.elf"
	fHost      = "fw.local"
)

func newTestFetcher() *Fetcher {
	return NewFetcher(FetchConfig{
		ClientMAC: fClientMAC, ClientIP: fClientIP, ClientPort: fClientPrt,
		ServerIP: fServerIP, ServerPort: fServerPrt, ISS: fISS,
		Path: fPath, Host: fHost,
	})
}

// fServerSeg builds a TCP segment as the server sends it back to the client.
func fServerSeg(seq, ack uint32, flags uint8, payload []byte) []byte {
	return tcp.BuildSegment(tcp.Segment{
		DstMAC: fClientMAC, SrcMAC: fServerMAC, SrcIP: fServerIP, DstIP: fClientIP,
		SrcPort: fServerPrt, DstPort: fClientPrt, Seq: seq, Ack: ack,
		Flags: flags, Window: 5840, Payload: payload,
	})
}

func TestFetcherFullFetch(t *testing.T) {
	f := newTestFetcher()

	// First: a broadcast ARP request for the server IP.
	arpReq := f.First()
	if _, _, ok := frame.ParseARPReply(arpReq); ok {
		t.Errorf("First() should be an ARP request, not a reply")
	}

	// ARP reply (server -> client) -> the SYN.
	arpReply := frame.BuildARPReply(fServerMAC, fClientMAC, fServerIP, fClientIP)
	syn, done := f.OnFrame(arpReply)
	if done {
		t.Fatal("done after ARP reply")
	}
	s, ok := tcp.ParseSegment(syn)
	if !ok || s.Flags != tcp.FlagSYN {
		t.Fatalf("expected SYN after ARP reply, got flags %#x ok=%v", s.Flags, ok)
	}
	if s.Seq != fISS {
		t.Errorf("SYN seq = %#x, want ISS %#x", s.Seq, fISS)
	}
	if f.ServerMAC() != fServerMAC {
		t.Errorf("learned server MAC %x, want %x", f.ServerMAC(), fServerMAC)
	}
	if f.Phase() != PhaseHandshake {
		t.Errorf("phase = %d, want Handshake", f.Phase())
	}

	// SYN-ACK -> a single ACK+GET segment.
	synAck := fServerSeg(fServerISS, fISS+1, tcp.FlagSYN|tcp.FlagACK, nil)
	getFrame, done := f.OnFrame(synAck)
	if done {
		t.Fatal("done after SYN-ACK")
	}
	gs, ok := tcp.ParseSegment(getFrame)
	if !ok || gs.Flags != tcp.FlagPSH|tcp.FlagACK {
		t.Fatalf("expected PSH|ACK GET segment, got flags %#x ok=%v", gs.Flags, ok)
	}
	if gs.Ack != fServerISS+1 {
		t.Errorf("GET segment ack = %#x, want %#x (acks the server SYN)", gs.Ack, fServerISS+1)
	}
	if !bytes.Equal(gs.Payload, BuildRequest(fPath, fHost)) {
		t.Errorf("GET payload = %q, want the request bytes", gs.Payload)
	}
	if f.Phase() != PhaseRecv {
		t.Errorf("phase = %d, want Recv", f.Phase())
	}

	// The server may ACK the GET with a bare ACK first: ignored, keep waiting.
	srvAck := fISS + 1 + uint32(len(BuildRequest(fPath, fHost)))
	if tx, done := f.OnFrame(fServerSeg(fServerISS+1, srvAck, tcp.FlagACK, nil)); tx != nil || done {
		t.Errorf("bare ACK of the GET should be ignored, got tx=%x done=%v", tx, done)
	}

	// Response in two segments (header, then body), each ACKed.
	head := []byte("HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\n")
	body := []byte("hello")
	srvSeq := fServerISS + 1
	if tx, done := f.OnFrame(fServerSeg(srvSeq, srvAck, tcp.FlagPSH|tcp.FlagACK, head)); tx == nil || done {
		t.Fatalf("no ACK for response head (tx=%x done=%v)", tx, done)
	}
	srvSeq += uint32(len(head))
	if tx, done := f.OnFrame(fServerSeg(srvSeq, srvAck, tcp.FlagPSH|tcp.FlagACK, body)); tx == nil || done {
		t.Fatalf("no ACK for response body (tx=%x done=%v)", tx, done)
	}
	srvSeq += uint32(len(body))

	// Server FIN -> our FIN-ACK, done.
	finAck, done := f.OnFrame(fServerSeg(srvSeq, srvAck, tcp.FlagFIN|tcp.FlagACK, nil))
	if !done {
		t.Fatal("not done after server FIN")
	}
	fs, _ := tcp.ParseSegment(finAck)
	if fs.Flags != tcp.FlagFIN|tcp.FlagACK {
		t.Errorf("teardown flags = %#x, want FIN|ACK", fs.Flags)
	}

	// The fetched bytes and the parsed response.
	wantBytes := append(append([]byte{}, head...), body...)
	if !bytes.Equal(f.Bytes(), wantBytes) {
		t.Errorf("fetched bytes %q, want %q", f.Bytes(), wantBytes)
	}
	r, ok := f.Response()
	if !ok || r.Status != 200 || !r.Complete {
		t.Fatalf("Response = %+v ok=%v", r, ok)
	}
	if got := string(f.Bytes()[r.BodyOff:]); got != "hello" {
		t.Errorf("body = %q, want %q", got, "hello")
	}
}

// driveFetchHandshake runs a fetcher through ARP + handshake and returns the
// server's first data seq and the server-side ack value (the GET acknowledged).
func driveFetchHandshake(t *testing.T, f *Fetcher) (srvSeq, srvAck uint32) {
	t.Helper()
	f.OnFrame(frame.BuildARPReply(fServerMAC, fClientMAC, fServerIP, fClientIP)) // -> SYN
	f.OnFrame(fServerSeg(fServerISS, fISS+1, tcp.FlagSYN|tcp.FlagACK, nil))      // -> GET
	srvAck = fISS + 1 + uint32(len(BuildRequest(fPath, fHost)))
	return fServerISS + 1, srvAck
}

// feedResponse delivers head+body to the fetcher across segments of at most
// segSize bytes, then the server FIN, asserting each data segment is ACKed.
func feedResponse(t *testing.T, f *Fetcher, srvSeq, srvAck uint32, resp []byte, segSize int) {
	t.Helper()
	for off := 0; off < len(resp); off += segSize {
		end := off + segSize
		if end > len(resp) {
			end = len(resp)
		}
		seg := resp[off:end]
		tx, done := f.OnFrame(fServerSeg(srvSeq, srvAck, tcp.FlagPSH|tcp.FlagACK, seg))
		if tx == nil || done {
			t.Fatalf("no ACK for response segment at offset %d (tx=%x done=%v)", off, tx, done)
		}
		srvSeq += uint32(len(seg))
	}
	if _, done := f.OnFrame(fServerSeg(srvSeq, srvAck, tcp.FlagFIN|tcp.FlagACK, nil)); !done {
		t.Fatal("not done after server FIN")
	}
}

// TestFetcherStreamTo: a fetch with a sink streams the body (header skipped) in
// bounded windows, the chunks concatenate to the body, and Conn.Data never holds
// the whole body.
func TestFetcherStreamTo(t *testing.T) {
	f := newTestFetcher()
	sink := &tcp.ChunkSink{}
	const window = 32
	f.StreamTo(sink, window)

	srvSeq, srvAck := driveFetchHandshake(t, f)

	head := []byte("HTTP/1.0 200 OK\r\nContent-Length: 100\r\n\r\n")
	// A body whose length is NOT a multiple of the window (100 = 3*32 + 4),
	// exercising the partial-final-flush path.
	body := bytes.Repeat([]byte("0123456789"), 10) // 100 bytes
	resp := append(append([]byte{}, head...), body...)

	// Deliver across many small segments so windows straddle segment boundaries.
	feedResponse(t, f, srvSeq, srvAck, resp, 13)

	if !bytes.Equal(sink.Bytes(), body) {
		t.Errorf("streamed %d bytes, want the %d-byte body (header should be skipped)", len(sink.Bytes()), len(body))
	}
	// Every flushed chunk is within the window; the header is gone.
	for i, ch := range sink.Chunks {
		if len(ch) > window {
			t.Errorf("chunk %d is %d bytes, exceeds window %d", i, len(ch), window)
		}
	}
	if bytes.Contains(sink.Bytes(), []byte("HTTP/1.0")) {
		t.Errorf("streamed bytes still contain the HTTP header")
	}
	// In streaming mode the connection must not hold the whole body.
	if got := len(f.Bytes()); got >= len(body) {
		t.Errorf("Conn.Data holds %d bytes in streaming mode; the body must not accumulate", got)
	}
}

// TestFetcherStreamSmallWindowBoundary: the header fits in the first window and
// the body that shares that window streams correctly (the no-straddle case made
// explicit), with a tiny window relative to the body.
func TestFetcherStreamSmallWindowBoundary(t *testing.T) {
	f := newTestFetcher()
	sink := &tcp.ChunkSink{}
	// Window large enough to hold the header but small relative to the body, so
	// the first window is header+body-start and subsequent windows are pure body.
	const window = 64
	f.StreamTo(sink, window)

	srvSeq, srvAck := driveFetchHandshake(t, f)
	head := []byte("HTTP/1.0 200 OK\r\n\r\n") // 19 bytes, well under the window
	body := bytes.Repeat([]byte("X"), 200)
	resp := append(append([]byte{}, head...), body...)
	feedResponse(t, f, srvSeq, srvAck, resp, 200) // whole response in one segment

	if !bytes.Equal(sink.Bytes(), body) {
		t.Errorf("streamed body length %d, want %d", len(sink.Bytes()), len(body))
	}
	// First flush is the rest of the first window after the header: window-len(head).
	if first := sink.Chunks[0]; len(first) != window-len(head) {
		t.Errorf("first body flush = %d bytes, want %d (window minus header)", len(first), window-len(head))
	}
}

// TestFetcherIgnoresStrayARP: an ARP reply for a different IP does not advance
// the fetch.
func TestFetcherIgnoresStrayARP(t *testing.T) {
	f := newTestFetcher()
	stray := frame.BuildARPReply(fServerMAC, fClientMAC, frame.IPv4{10, 0, 0, 9}, fClientIP)
	if tx, done := f.OnFrame(stray); tx != nil || done {
		t.Errorf("stray ARP reply advanced the fetch: tx=%x done=%v", tx, done)
	}
	if f.Phase() != PhaseARP {
		t.Errorf("phase advanced on a stray ARP: %d", f.Phase())
	}
}

// TestFetcherIgnoresBadSynAck: a SYN-ACK with the wrong ack number does not
// complete the handshake.
func TestFetcherIgnoresBadSynAck(t *testing.T) {
	f := newTestFetcher()
	f.OnFrame(frame.BuildARPReply(fServerMAC, fClientMAC, fServerIP, fClientIP)) // -> SYN
	bad := fServerSeg(fServerISS, fISS+99, tcp.FlagSYN|tcp.FlagACK, nil)
	if tx, done := f.OnFrame(bad); tx != nil || done {
		t.Errorf("bad SYN-ACK advanced the fetch: tx=%x done=%v", tx, done)
	}
	if f.Phase() != PhaseHandshake {
		t.Errorf("phase advanced on a bad SYN-ACK: %d", f.Phase())
	}
}

// TestFetcherFinWithData: the final data riding on the FIN segment is still
// accumulated before completion.
func TestFetcherFinWithData(t *testing.T) {
	f := newTestFetcher()
	f.OnFrame(frame.BuildARPReply(fServerMAC, fClientMAC, fServerIP, fClientIP))
	f.OnFrame(fServerSeg(fServerISS, fISS+1, tcp.FlagSYN|tcp.FlagACK, nil)) // -> GET
	srvAck := fISS + 1 + uint32(len(BuildRequest(fPath, fHost)))

	// One segment carries the whole response AND the FIN.
	resp := []byte("HTTP/1.0 200 OK\r\n\r\nbye")
	_, done := f.OnFrame(fServerSeg(fServerISS+1, srvAck, tcp.FlagFIN|tcp.FlagPSH|tcp.FlagACK, resp))
	if !done {
		t.Fatal("not done after FIN-with-data")
	}
	if !bytes.Equal(f.Bytes(), resp) {
		t.Errorf("fetched %q, want %q", f.Bytes(), resp)
	}
	r, ok := f.Response()
	if !ok || r.Status != 200 || string(f.Bytes()[r.BodyOff:]) != "bye" {
		t.Errorf("Response = %+v ok=%v body=%q", r, ok, f.Bytes()[r.BodyOff:])
	}
}
