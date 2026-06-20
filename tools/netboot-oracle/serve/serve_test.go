package serve

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
)

var (
	srvMAC  = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	srvIP   = [4]byte{192, 0, 2, 1}
	cliMAC  = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	cliIP   = [4]byte{192, 0, 2, 44}
	cliTID  = uint16(30574)
	srvTID  = uint16(40136)
	cfgDemo = Config{ServerMAC: srvMAC, ServerIP: srvIP, ServerTID: srvTID}
)

func makeFile(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// newServer builds a demo server over a 2-file store.
func newServer(files map[string][]byte) *Responder {
	store := tftp.MapStore{}
	for name, b := range files {
		store[name] = uint64(len(b))
	}
	return New(cfgDemo, store, func(name string) tftp.Source {
		return tftp.ByteSource(files[name])
	})
}

func rrqFrame(name string, opts []tftp.Option) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: srvMAC, SrcMAC: cliMAC, SrcIP: cliIP, DstIP: srvIP,
		SrcPort: cliTID, DstPort: 69,
		Payload: tftp.BuildRRQ(name, "octet", opts),
	})
}

func ackFrame(block uint16) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: srvMAC, SrcMAC: cliMAC, SrcIP: cliIP, DstIP: srvIP,
		SrcPort: cliTID, DstPort: srvTID,
		Payload: tftp.BuildACK(block),
	})
}

func dataPayload(t *testing.T, f []byte) (block uint16, data []byte) {
	t.Helper()
	u, ok := frame.ParseUDP(f)
	if !ok {
		t.Fatalf("reply is not a UDP frame: %x", f)
	}
	blk, d, err := tftp.ParseDATA(u.Payload)
	if err != nil {
		t.Fatalf("reply is not a DATA packet: %v (%x)", err, u.Payload)
	}
	return blk, d
}

// TestBareRRQStreamsData is the headline i96 behaviour: a plain TFTP client's
// RRQ (no options) is answered with DATA block 1 directly (no OACK, RFC 2347),
// at the 512-byte default, then ACK/DATA to the short final block.
func TestBareRRQStreamsData(t *testing.T) {
	file := makeFile(512 + 200) // one full 512 block + a 200-byte tail
	s := newServer(map[string][]byte{"hello.txt": file})

	// Bare RRQ -> DATA block 1 (no OACK).
	reply := s.OnFrame(rrqFrame("hello.txt", nil))
	blk, d1 := dataPayload(t, reply)
	if blk != 1 {
		t.Fatalf("first reply block = %d, want 1 (DATA, not OACK)", blk)
	}
	if len(d1) != 512 {
		t.Fatalf("block 1 = %d bytes, want 512", len(d1))
	}
	if !bytes.Equal(d1, file[:512]) {
		t.Fatalf("block 1 bytes mismatch")
	}

	// ACK 1 -> DATA block 2 (short final, 200 bytes).
	reply = s.OnFrame(ackFrame(1))
	blk, d2 := dataPayload(t, reply)
	if blk != 2 {
		t.Fatalf("second reply block = %d, want 2", blk)
	}
	if len(d2) != 200 {
		t.Fatalf("final block = %d bytes, want 200", len(d2))
	}
	if !bytes.Equal(d2, file[512:]) {
		t.Fatalf("final block bytes mismatch")
	}

	// ACK 2 -> transfer complete, nothing sent.
	if fin := s.OnFrame(ackFrame(2)); fin != nil {
		t.Fatalf("ACK of the short final block should end the transfer, got %x", fin)
	}
}

// TestOptionedRRQSendsOACK confirms an RRQ that requests options is answered with
// an OACK (the curl/tsize path), then the OACK->ACK0->DATA1 cadence.
func TestOptionedRRQSendsOACK(t *testing.T) {
	file := makeFile(1024 + 50)
	s := newServer(map[string][]byte{"readme.txt": file})

	reply := s.OnFrame(rrqFrame("readme.txt", []tftp.Option{
		{Name: "blksize", Value: "1024"}, {Name: "tsize", Value: "0"},
	}))
	u, ok := frame.ParseUDP(reply)
	if !ok || tftp.Opcode(u.Payload) != tftp.OpOACK {
		t.Fatalf("optioned RRQ should get an OACK, got %x", reply)
	}
	opts, err := tftp.ParseOACK(u.Payload)
	if err != nil {
		t.Fatalf("parse OACK: %v", err)
	}
	if v, _ := tftp.OptionUint(opts, "tsize"); v != uint64(len(file)) {
		t.Fatalf("OACK tsize = %d, want %d", v, len(file))
	}
	if v, _ := tftp.OptionUint(opts, "blksize"); v != 1024 {
		t.Fatalf("OACK blksize = %d, want 1024", v)
	}

	// ACK 0 -> DATA block 1 (the OACK-path FirstData handoff).
	blk, d1 := dataPayload(t, s.OnFrame(ackFrame(0)))
	if blk != 1 || len(d1) != 1024 {
		t.Fatalf("after ACK0 expected DATA block 1 of 1024 bytes, got block %d len %d", blk, len(d1))
	}
}

// TestMissReturnsError confirms a request for an unknown name gets ERROR(1) and
// the server keeps serving.
func TestMissReturnsError(t *testing.T) {
	s := newServer(map[string][]byte{"hello.txt": makeFile(10)})
	reply := s.OnFrame(rrqFrame("nope.txt", nil))
	u, ok := frame.ParseUDP(reply)
	if !ok {
		t.Fatalf("miss reply is not UDP: %x", reply)
	}
	code, _, err := tftp.ParseError(u.Payload)
	if err != nil || code != tftp.ErrFileNotFound {
		t.Fatalf("miss should get ERROR(1), got %x (code %d, err %v)", reply, code, err)
	}
	// A subsequent valid RRQ still works.
	if got := s.OnFrame(rrqFrame("hello.txt", nil)); got == nil {
		t.Fatalf("server stopped serving after a miss")
	}
}

// TestARPAnswered confirms an ARP request for the SAM's IP is answered (so a
// plain client can resolve the SAM's MAC without DHCP).
func TestARPAnswered(t *testing.T) {
	s := newServer(map[string][]byte{"hello.txt": makeFile(10)})
	req := frame.BuildARPRequest(cliMAC, cliIP, srvIP)
	reply := s.OnFrame(req)
	if reply == nil {
		t.Fatalf("ARP request for the SAM's IP was not answered")
	}
	mac, ip, ok := frame.ParseARPReply(reply)
	if !ok || ip != srvIP || mac != srvMAC {
		t.Fatalf("ARP reply wrong: mac=%x ip=%v ok=%v", mac, ip, ok)
	}
	// An ARP request for a different IP is ignored.
	if r := s.OnFrame(frame.BuildARPRequest(cliMAC, cliIP, cliIP)); r != nil {
		t.Fatalf("answered an ARP request for a different IP: %x", r)
	}
}

// TestNonUDPIgnored confirms a frame that is neither ARP-for-us nor UDP is
// dropped silently.
func TestNonUDPIgnored(t *testing.T) {
	s := newServer(map[string][]byte{"hello.txt": makeFile(10)})
	junk := make([]byte, 60)
	junk[12], junk[13] = 0x86, 0xdd // EtherType IPv6 — not ARP, not IPv4/UDP
	if r := s.OnFrame(junk); r != nil {
		t.Fatalf("non-UDP frame produced a reply: %x", r)
	}
}

// wrqFrame builds a client WRQ frame for name with the given options (i121a).
func wrqFrame(name string, opts []tftp.Option) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: srvMAC, SrcMAC: cliMAC, SrcIP: cliIP, DstIP: srvIP,
		SrcPort: cliTID, DstPort: 69,
		Payload: tftp.BuildWRQ(name, "octet", opts),
	})
}

// TestBareWRQSendsACK0 confirms that a bare WRQ (no options) is answered with
// ACK block 0 (`00 04 00 00`) wrapped as a UDP frame back to the client TID.
// This is the i121a handshake: the server learns the client endpoint and
// signals readiness to receive (DATA reception is i121b, not here).
func TestBareWRQSendsACK0(t *testing.T) {
	s := newServer(map[string][]byte{"hello.txt": makeFile(10)})
	req := wrqFrame("upload.bin", nil)
	reply := s.OnFrame(req)
	if reply == nil {
		t.Fatalf("bare WRQ: no reply from server")
	}
	u, ok := frame.ParseUDP(reply)
	if !ok {
		t.Fatalf("bare WRQ reply is not a UDP frame: %x", reply)
	}
	if tftp.Opcode(u.Payload) != tftp.OpACK {
		t.Fatalf("bare WRQ reply opcode = %d, want ACK(%d)", tftp.Opcode(u.Payload), tftp.OpACK)
	}
	blk, err := tftp.ParseACK(u.Payload)
	if err != nil || blk != 0 {
		t.Fatalf("bare WRQ reply ACK block = %d (err %v), want 0", blk, err)
	}
	if u.DstPort != cliTID {
		t.Fatalf("bare WRQ reply dst port = %d, want client TID %d", u.DstPort, cliTID)
	}
	if u.SrcPort != srvTID {
		t.Fatalf("bare WRQ reply src port = %d, want server TID %d", u.SrcPort, srvTID)
	}
}

// TestOptionedWRQSendsOACK confirms that an optioned WRQ (blksize + tsize) is
// answered with an OACK echoing the accepted blksize and the client's tsize.
// This is the i121a OACK handshake path (RFC 2347). DATA reception is i121b.
func TestOptionedWRQSendsOACK(t *testing.T) {
	s := newServer(map[string][]byte{"hello.txt": makeFile(10)})
	req := wrqFrame("upload.bin", []tftp.Option{
		{Name: "blksize", Value: "512"},
		{Name: "tsize", Value: "4096"},
	})
	reply := s.OnFrame(req)
	if reply == nil {
		t.Fatalf("optioned WRQ: no reply from server")
	}
	u, ok := frame.ParseUDP(reply)
	if !ok {
		t.Fatalf("optioned WRQ reply is not a UDP frame: %x", reply)
	}
	if tftp.Opcode(u.Payload) != tftp.OpOACK {
		t.Fatalf("optioned WRQ reply opcode = %d, want OACK(%d)", tftp.Opcode(u.Payload), tftp.OpOACK)
	}
	opts, err := tftp.ParseOACK(u.Payload)
	if err != nil {
		t.Fatalf("parse OACK: %v", err)
	}
	if v, _ := tftp.OptionUint(opts, "blksize"); v != 512 {
		t.Fatalf("OACK blksize = %d, want 512", v)
	}
	if v, _ := tftp.OptionUint(opts, "tsize"); v != 4096 {
		t.Fatalf("OACK tsize = %d, want 4096", v)
	}
	if u.DstPort != cliTID {
		t.Fatalf("optioned WRQ reply dst port = %d, want client TID %d", u.DstPort, cliTID)
	}
	if u.SrcPort != srvTID {
		t.Fatalf("optioned WRQ reply src port = %d, want server TID %d", u.SrcPort, srvTID)
	}
}
