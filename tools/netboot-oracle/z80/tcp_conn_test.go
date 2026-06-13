// tcp_conn_test.go — the i70 host-verification of the TCP connection state
// machine. It runs the *real* composed module (src/netboot/tcp_conn.asm: the
// driver encdrv.asm + the host-verified build_tcp_segment primitive) under the
// flat-memory koron-go/z80 harness with the emulated Trinity (enc28j60.go)
// attached, and asserts that the active-open handshake, the data ACK cadence,
// and the FIN teardown produce frames on the virtual wire byte-for-byte the Go
// authority's (tcp.Conn.Connect / OnSegment).
//
// This is the second i70 brick (after the TCP segment layer) made
// host-verifiable end-to-end by i80: drv_read -> dispatch on connection state ->
// build + drv_write the control segment, the whole connection lifecycle proven
// against the byte-exact Go reference. Emulation verification, NOT hardware
// verification — a real TCP handshake against a live server remains the final
// gate (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	tcpConnBinPath = "../../../build/netboot_tcp_conn.bin"
	tcpConnMapPath = "../../../build/netboot_tcp_conn.map"
)

// tcpConnCfg is the connection identity, shared between the Z80 CONFIG block and
// the Go Conn so both produce identical segment bytes.
var tcpConnCfg = struct {
	clientMAC  frame.MAC
	clientIP   frame.IPv4
	clientPort uint16
	serverMAC  frame.MAC
	serverIP   frame.IPv4
	serverPort uint16
	iss        uint32
	serverISS  uint32
}{
	clientMAC:  frame.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x44},
	clientIP:   frame.IPv4{192, 0, 2, 44},
	clientPort: 0xC000,
	serverMAC:  frame.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01},
	serverIP:   frame.IPv4{192, 0, 2, 1},
	serverPort: 80,
	iss:        0x11223344,
	serverISS:  0x99aabb00,
}

func loadTCPConn(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(tcpConnBinPath); err != nil {
		t.Skipf("tcp_conn binary not built (%s); run `make netboot-tcp-conn`", tcpConnBinPath)
	}
	mac, err := z80h.Load(tcpConnBinPath, tcpConnMapPath)
	if err != nil {
		t.Fatalf("load tcp_conn: %v", err)
	}
	return mac
}

// goConn builds the matching Go authority connection.
func goConn() *tcp.Conn {
	c := tcpConnCfg
	return tcp.NewConn(c.clientMAC, c.clientIP, c.clientPort,
		c.serverMAC, c.serverIP, c.serverPort, c.iss)
}

// fillTCPConnConfig writes the CONFIG block (identity + ISS) in the loaded
// machine to match the Go connection. Ports / ISS are big-endian on the wire.
func fillTCPConnConfig(t *testing.T, mac *z80h.Machine) {
	t.Helper()
	put := func(name string, data []byte) {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("%v", err)
		}
		mac.Write(a, data)
	}
	be16 := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
	be32 := func(v uint32) []byte {
		return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	}
	c := tcpConnCfg
	put("CONN_CLIENT_MAC", c.clientMAC[:])
	put("CONN_CLIENT_IP", c.clientIP[:])
	put("CONN_CLIENT_PORT", be16(c.clientPort))
	put("CONN_SERVER_MAC", c.serverMAC[:])
	put("CONN_SERVER_IP", c.serverIP[:])
	put("CONN_SERVER_PORT", be16(c.serverPort))
	put("CONN_ISS", be32(c.iss))
}

// initTCPConnDriver attaches the emulated Trinity and runs drv_init with the
// client's MAC (the driver programs it into the ENC).
func initTCPConnDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	mac.AttachIO(enc)
	macAddr, err := mac.Sym("CONN_CLIENT_MAC")
	if err != nil {
		t.Fatalf("%v", err)
	}
	mac.Write(macAddr, tcpConnCfg.clientMAC[:])
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_init returned BC=%d, want 1", res.BC)
	}
}

// connect runs tcp_connect and returns the SYN frame it transmitted.
func connect(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	res, err := mac.Call("tcp_connect")
	if err != nil {
		t.Fatalf("call tcp_connect: %v", err)
	}
	tx := enc.TXFrames()
	if len(tx) != before+1 {
		t.Fatalf("tcp_connect transmitted %d frames, want 1", len(tx)-before)
	}
	out := tx[len(tx)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("tcp_connect returned BC=%d but the wire frame is %d bytes", res.BC, len(out))
	}
	return out
}

// recvOnce injects seg on the wire and runs one tcp_conn_recv, returning the
// frame the driver transmitted (or nil if it sent nothing).
func recvOnce(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, seg []byte) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	enc.InjectRX(seg)
	res, err := mac.Call("tcp_conn_recv")
	if err != nil {
		t.Fatalf("call tcp_conn_recv: %v", err)
	}
	tx := enc.TXFrames()
	if res.BC == 0 {
		if len(tx) != before {
			t.Fatalf("tcp_conn_recv returned BC=0 but transmitted a frame")
		}
		return nil
	}
	if len(tx) != before+1 {
		t.Fatalf("tcp_conn_recv transmitted %d frames, want 1", len(tx)-before)
	}
	out := tx[len(tx)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("tcp_conn_recv returned BC=%d but the wire frame is %d bytes", res.BC, len(out))
	}
	return out
}

// serverSeg builds a segment as the server would send it back to the client
// (src = server, dst = client) — what the test injects.
func serverSeg(seq, ack uint32, flags uint8, payload []byte) []byte {
	c := tcpConnCfg
	return tcp.BuildSegment(tcp.Segment{
		DstMAC:  c.clientMAC,
		SrcMAC:  c.serverMAC,
		SrcIP:   c.serverIP,
		DstIP:   c.clientIP,
		SrcPort: c.serverPort,
		DstPort: c.clientPort,
		Seq:     seq,
		Ack:     ack,
		Flags:   flags,
		Window:  5840,
		Payload: payload,
	})
}

// TestTCPConnSYN: tcp_connect emits the SYN byte-for-byte the Go authority's.
func TestTCPConnSYN(t *testing.T) {
	mac := loadTCPConn(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	got := connect(t, mac, enc)
	want := goConn().Connect()
	if !bytes.Equal(got, want) {
		t.Errorf("SYN != Go authority\n  z80 %x\n  go  %x", got, want)
	}
}

// TestTCPConnHandshake: a SYN-ACK in produces the handshake ACK out,
// byte-for-byte the Go authority's.
func TestTCPConnHandshake(t *testing.T) {
	mac := loadTCPConn(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	ref := goConn()
	ref.Connect()
	connect(t, mac, enc)

	c := tcpConnCfg
	synAck := serverSeg(c.serverISS, c.iss+1, tcp.FlagSYN|tcp.FlagACK, nil)
	got := recvOnce(t, mac, enc, synAck)
	want := ref.OnSegment(synAck)
	if want == nil {
		t.Fatal("Go authority ignored the SYN-ACK (test bug)")
	}
	if got == nil {
		t.Fatal("tcp_conn_recv ignored the SYN-ACK")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("handshake ACK != Go authority\n  z80 %x\n  go  %x", got, want)
	}
}

// TestTCPConnFullLifecycle drives SYN -> SYN-ACK -> ACK, two data segments each
// ACKed, and a server FIN answered with FIN-ACK, then the final ACK — every
// frame on the virtual wire byte-for-byte the Go authority's, and the
// accumulated body matches.
func TestTCPConnFullLifecycle(t *testing.T) {
	mac := loadTCPConn(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	ref := goConn()
	c := tcpConnCfg

	// SYN.
	ref.Connect()
	connect(t, mac, enc)

	// SYN-ACK -> ACK.
	synAck := serverSeg(c.serverISS, c.iss+1, tcp.FlagSYN|tcp.FlagACK, nil)
	if got, want := recvOnce(t, mac, enc, synAck), ref.OnSegment(synAck); !bytes.Equal(got, want) {
		t.Fatalf("handshake ACK != Go\n  z80 %x\n  go  %x", got, want)
	}

	// Two data segments (an even-length and an odd-length body), each ACKed.
	body1 := []byte("HTTP/1.0 200 OK\r\n\r\n") // 18 (even)
	srvSeq := c.serverISS + 1
	seg1 := serverSeg(srvSeq, c.iss+1, tcp.FlagPSH|tcp.FlagACK, body1)
	if got, want := recvOnce(t, mac, enc, seg1), ref.OnSegment(seg1); !bytes.Equal(got, want) {
		t.Fatalf("data1 ACK != Go\n  z80 %x\n  go  %x", got, want)
	}
	srvSeq += uint32(len(body1))

	body2 := []byte("hello, world!") // 13 (odd)
	seg2 := serverSeg(srvSeq, c.iss+1, tcp.FlagPSH|tcp.FlagACK, body2)
	if got, want := recvOnce(t, mac, enc, seg2), ref.OnSegment(seg2); !bytes.Equal(got, want) {
		t.Fatalf("data2 ACK != Go\n  z80 %x\n  go  %x", got, want)
	}
	srvSeq += uint32(len(body2))

	// Server FIN -> our FIN-ACK.
	fin := serverSeg(srvSeq, c.iss+1, tcp.FlagFIN|tcp.FlagACK, nil)
	if got, want := recvOnce(t, mac, enc, fin), ref.OnSegment(fin); !bytes.Equal(got, want) {
		t.Fatalf("FIN-ACK != Go\n  z80 %x\n  go  %x", got, want)
	}

	// The accumulated body in CONN_DATA must equal body1+body2.
	wantBody := append(append([]byte{}, body1...), body2...)
	if !bytes.Equal(ref.Data, wantBody) {
		t.Fatalf("Go authority body %q != expected %q (test bug)", ref.Data, wantBody)
	}
	dataAddr, err := mac.Sym("CONN_DATA")
	if err != nil {
		t.Fatalf("%v", err)
	}
	gotBody := mac.Read(dataAddr, len(wantBody))
	if !bytes.Equal(gotBody, wantBody) {
		t.Errorf("Z80 accumulated body != expected\n  z80 %q\n  exp %q", gotBody, wantBody)
	}

	// Final ACK of our FIN: nothing sent, both close.
	finalAck := serverSeg(srvSeq+1, c.iss+2, tcp.FlagACK, nil)
	if got := recvOnce(t, mac, enc, finalAck); got != nil {
		t.Errorf("replied to the final ACK: %x", got)
	}
	if r := ref.OnSegment(finalAck); r != nil {
		t.Errorf("Go authority replied to the final ACK: %x", r)
	}
}

// TestTCPConnIgnoresWrongPort: a SYN-ACK from a different server port is not for
// this connection and is dropped (nothing transmitted).
func TestTCPConnIgnoresWrongPort(t *testing.T) {
	mac := loadTCPConn(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	connect(t, mac, enc)

	c := tcpConnCfg
	bad := tcp.BuildSegment(tcp.Segment{
		DstMAC: c.clientMAC, SrcMAC: c.serverMAC, SrcIP: c.serverIP, DstIP: c.clientIP,
		SrcPort: 443, DstPort: c.clientPort, Seq: c.serverISS, Ack: c.iss + 1,
		Flags: tcp.FlagSYN | tcp.FlagACK, Window: 5840,
	})
	if got := recvOnce(t, mac, enc, bad); got != nil {
		t.Errorf("replied to a wrong-port segment: %x", got)
	}
}

// TestTCPConnIgnoresOutOfOrderData: an established connection ignores a data
// segment with a future sequence number (a gap) — no ACK, no accumulation.
func TestTCPConnIgnoresOutOfOrderData(t *testing.T) {
	mac := loadTCPConn(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	c := tcpConnCfg
	connect(t, mac, enc)
	synAck := serverSeg(c.serverISS, c.iss+1, tcp.FlagSYN|tcp.FlagACK, nil)
	recvOnce(t, mac, enc, synAck)

	// A segment 100 bytes ahead of rcvNxt (serverISS+1): out of order.
	gap := serverSeg(c.serverISS+101, c.iss+1, tcp.FlagPSH|tcp.FlagACK, []byte("x"))
	if got := recvOnce(t, mac, enc, gap); got != nil {
		t.Errorf("ACKed an out-of-order segment: %x", got)
	}
}

// TestTCPConnEmptyWire: with nothing injected, tcp_conn_recv returns BC=0 and
// sends nothing.
func TestTCPConnEmptyWire(t *testing.T) {
	mac := loadTCPConn(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	res, err := mac.Call("tcp_conn_recv")
	if err != nil {
		t.Fatalf("call tcp_conn_recv: %v", err)
	}
	if res.BC != 0 {
		t.Fatalf("empty wire: tcp_conn_recv returned BC=%d, want 0", res.BC)
	}
	if len(enc.TXFrames()) != 0 {
		t.Fatalf("empty wire: transmitted %d frames, want 0", len(enc.TXFrames()))
	}
}
