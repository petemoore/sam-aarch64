package http

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
)

func TestBuildRequest(t *testing.T) {
	got := BuildRequest("/firmware/start4.elf", "fw.local")
	want := "GET /firmware/start4.elf HTTP/1.0\r\nHost: fw.local\r\n\r\n"
	if string(got) != want {
		t.Errorf("BuildRequest = %q, want %q", got, want)
	}
}

func TestParseResponse(t *testing.T) {
	raw := []byte("HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\nhello")
	r, ok := ParseResponse(raw)
	if !ok {
		t.Fatal("ParseResponse returned ok=false for a valid response")
	}
	if r.Status != 200 {
		t.Errorf("Status = %d, want 200", r.Status)
	}
	if !r.Complete {
		t.Error("Complete = false, want true")
	}
	if got := string(raw[r.BodyOff:]); got != "hello" {
		t.Errorf("body = %q, want %q", got, "hello")
	}
}

func TestParseResponseNon200(t *testing.T) {
	r, ok := ParseResponse([]byte("HTTP/1.0 404 Not Found\r\n\r\n"))
	if !ok || r.Status != 404 || !r.Complete {
		t.Errorf("ParseResponse(404) = %+v ok=%v", r, ok)
	}
}

func TestParseResponseIncomplete(t *testing.T) {
	// Header terminator has not arrived yet: status parses, body does not.
	r, ok := ParseResponse([]byte("HTTP/1.0 200 OK\r\nContent-Length: 5\r\n"))
	if !ok {
		t.Fatal("ParseResponse returned ok=false")
	}
	if r.Status != 200 {
		t.Errorf("Status = %d, want 200", r.Status)
	}
	if r.Complete {
		t.Error("Complete = true, want false (no terminator yet)")
	}
}

func TestParseResponseNoStatusLine(t *testing.T) {
	if _, ok := ParseResponse([]byte("garbagewithnospace")); ok {
		t.Error("ParseResponse accepted input with no status line")
	}
}

// TestClientFlow drives the Client over a tcp.Conn the way the Z80 loop does:
// handshake, Start (GET), two response segments, then parse the accumulated body.
func TestClientFlow(t *testing.T) {
	var (
		clientMAC  = frame.MAC{0x02, 0, 0, 0, 0, 0x44}
		clientIP   = frame.IPv4{192, 0, 2, 44}
		clientPort = uint16(0xC000)
		serverMAC  = frame.MAC{0x02, 0, 0, 0, 0, 0x01}
		serverIP   = frame.IPv4{192, 0, 2, 1}
		serverPort = uint16(80)
		iss        = uint32(0x11223344)
		serverISS  = uint32(0x99aabb00)
	)
	conn := tcp.NewConn(clientMAC, clientIP, clientPort, serverMAC, serverIP, serverPort, iss)
	conn.Connect()
	// Server SYN-ACK -> handshake ACK.
	synAck := tcp.BuildSegment(tcp.Segment{
		DstMAC: clientMAC, SrcMAC: serverMAC, SrcIP: serverIP, DstIP: clientIP,
		SrcPort: serverPort, DstPort: clientPort, Seq: serverISS, Ack: iss + 1,
		Flags: tcp.FlagSYN | tcp.FlagACK, Window: 5840,
	})
	if conn.OnSegment(synAck) == nil {
		t.Fatal("no handshake ACK")
	}

	cl := NewClient(conn, "/firmware/start4.elf", "fw.local")
	get := cl.Start()
	s, ok := tcp.ParseSegment(get)
	if !ok || s.Flags != tcp.FlagPSH|tcp.FlagACK {
		t.Fatalf("GET segment flags = %#x ok=%v, want PSH|ACK", s.Flags, ok)
	}
	if !bytes.Equal(s.Payload, BuildRequest("/firmware/start4.elf", "fw.local")) {
		t.Errorf("GET payload = %q", s.Payload)
	}

	// Server streams the response in two segments.
	head := []byte("HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\n")
	body := []byte("hello")
	srvSeq := serverISS + 1
	seg := func(seq uint32, payload []byte) []byte {
		return tcp.BuildSegment(tcp.Segment{
			DstMAC: clientMAC, SrcMAC: serverMAC, SrcIP: serverIP, DstIP: clientIP,
			SrcPort: serverPort, DstPort: clientPort, Seq: seq, Ack: iss + 1 + uint32(len(BuildRequest("/firmware/start4.elf", "fw.local"))),
			Flags: tcp.FlagPSH | tcp.FlagACK, Window: 5840, Payload: payload,
		})
	}
	if cl.OnSegment(seg(srvSeq, head)) == nil {
		t.Fatal("no ACK for response head")
	}
	if cl.OnSegment(seg(srvSeq+uint32(len(head)), body)) == nil {
		t.Fatal("no ACK for response body")
	}

	r, ok := cl.Response()
	if !ok || r.Status != 200 || !r.Complete {
		t.Fatalf("Response = %+v ok=%v", r, ok)
	}
	if got := string(conn.Data[r.BodyOff:]); got != "hello" {
		t.Errorf("fetched body = %q, want %q", got, "hello")
	}
}
