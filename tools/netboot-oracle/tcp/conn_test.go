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
