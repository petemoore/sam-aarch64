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
