package tcp

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
)

var (
	srcMAC = frame.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	dstMAC = frame.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	srcIP  = frame.IPv4{192, 0, 2, 44}
	dstIP  = frame.IPv4{192, 0, 2, 1}
)

// TestBuildSegmentRoundTrips asserts BuildSegment then ParseSegment recovers
// every field — the basic faithfulness check for the Z80 build/parse pair.
func TestBuildSegmentRoundTrips(t *testing.T) {
	in := Segment{
		DstMAC: dstMAC, SrcMAC: srcMAC, SrcIP: srcIP, DstIP: dstIP,
		SrcPort: 49152, DstPort: 80,
		Seq: 0x01020304, Ack: 0x05060708,
		Flags: FlagPSH | FlagACK, Window: 5840,
		Payload: []byte("GET /firmware/start4.elf HTTP/1.0\r\n\r\n"),
	}
	f := BuildSegment(in)
	got, ok := ParseSegment(f)
	if !ok {
		t.Fatal("ParseSegment rejected a built segment")
	}
	if got.SrcPort != in.SrcPort || got.DstPort != in.DstPort {
		t.Errorf("ports %d->%d, want %d->%d", got.SrcPort, got.DstPort, in.SrcPort, in.DstPort)
	}
	if got.Seq != in.Seq || got.Ack != in.Ack {
		t.Errorf("seq/ack %#x/%#x, want %#x/%#x", got.Seq, got.Ack, in.Seq, in.Ack)
	}
	if got.Flags != in.Flags {
		t.Errorf("flags %#x, want %#x", got.Flags, in.Flags)
	}
	if got.Window != in.Window {
		t.Errorf("window %d, want %d", got.Window, in.Window)
	}
	if got.SrcIP != in.SrcIP || got.DstIP != in.DstIP {
		t.Errorf("IPs %v->%v, want %v->%v", got.SrcIP, got.DstIP, in.SrcIP, in.DstIP)
	}
	if !bytes.Equal(got.Payload, in.Payload) {
		t.Errorf("payload\n got %q\nwant %q", got.Payload, in.Payload)
	}
}

// TestBuildSegmentChecksums asserts the IP header checksum and the TCP checksum
// of a built segment both verify — the property the Z80 port must reproduce
// (the TCP pseudo-header checksum is the one piece UDP did not have).
func TestBuildSegmentChecksums(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
		flags   uint8
	}{
		{"syn-no-payload", nil, FlagSYN},
		{"ack-even-payload", []byte("hello!!!"), FlagACK},               // 8 bytes (even)
		{"psh-odd-payload", []byte("odd-length-7x"), FlagPSH | FlagACK}, // 13 bytes (odd)
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := BuildSegment(Segment{
				DstMAC: dstMAC, SrcMAC: srcMAC, SrcIP: srcIP, DstIP: dstIP,
				SrcPort: 49152, DstPort: 80, Seq: 1000, Ack: 2000,
				Flags: tc.flags, Window: 5840, Payload: tc.payload,
			})
			// IP header checksum self-consistency.
			var ipsum uint32
			for i := frame.OffIPVerIHL; i < frame.OffIPVerIHL+frame.IPHeaderLen; i += 2 {
				ipsum += uint32(binary.BigEndian.Uint16(f[i:]))
			}
			for ipsum>>16 != 0 {
				ipsum = (ipsum & 0xffff) + (ipsum >> 16)
			}
			if ipsum != 0xffff {
				t.Errorf("IP header checksum not self-consistent (sum=%#x)", ipsum)
			}
			// TCP checksum self-consistency over the pseudo-header + segment.
			tcpLen := TCPHeaderLen + len(tc.payload)
			if !ChecksumValid(srcIP, dstIP, f[OffL4:OffL4+tcpLen]) {
				t.Errorf("TCP checksum not self-consistent")
			}
		})
	}
}

// TestBuildSegmentExactBytes pins the exact bytes of a small SYN segment, so a
// regression in any field (offset, flag bit, data-offset nibble, checksum) is
// caught against a hand-verified reference — the golden the Z80 port matches.
func TestBuildSegmentExactBytes(t *testing.T) {
	f := BuildSegment(Segment{
		DstMAC: dstMAC, SrcMAC: srcMAC, SrcIP: srcIP, DstIP: dstIP,
		SrcPort: 0xC000, DstPort: 0x0050, // 49152 -> 80
		Seq: 0x11223344, Ack: 0,
		Flags: FlagSYN, Window: 0x1680, // 5760
		Payload: nil,
	})
	// Expected frame: 14 (eth) + 20 (ip) + 20 (tcp) = 54 bytes, no payload.
	if len(f) != OffTCPPayload {
		t.Fatalf("frame length %d, want %d", len(f), OffTCPPayload)
	}
	// Ethernet + EtherType.
	if !bytes.Equal(f[0:6], dstMAC[:]) || !bytes.Equal(f[6:12], srcMAC[:]) {
		t.Errorf("ethernet MACs wrong: %x", f[0:12])
	}
	if binary.BigEndian.Uint16(f[12:]) != frame.EtherTypeIPv4 {
		t.Errorf("ethertype %#x, want IPv4", binary.BigEndian.Uint16(f[12:]))
	}
	// IP: version/IHL, proto, addresses.
	if f[frame.OffIPVerIHL] != 0x45 {
		t.Errorf("ip ver/ihl %#x, want 0x45", f[frame.OffIPVerIHL])
	}
	if f[frame.OffIPProto] != ProtoTCP {
		t.Errorf("ip proto %#x, want TCP", f[frame.OffIPProto])
	}
	if binary.BigEndian.Uint16(f[frame.OffIPTotalLen:]) != 40 {
		t.Errorf("ip total len %d, want 40", binary.BigEndian.Uint16(f[frame.OffIPTotalLen:]))
	}
	// TCP fields.
	t1 := f[OffL4:]
	if binary.BigEndian.Uint16(t1[OffTCPSrcPort:]) != 0xC000 {
		t.Errorf("tcp src port %#x, want 0xC000", binary.BigEndian.Uint16(t1[OffTCPSrcPort:]))
	}
	if binary.BigEndian.Uint16(t1[OffTCPDstPort:]) != 0x0050 {
		t.Errorf("tcp dst port %#x, want 0x0050", binary.BigEndian.Uint16(t1[OffTCPDstPort:]))
	}
	if binary.BigEndian.Uint32(t1[OffTCPSeq:]) != 0x11223344 {
		t.Errorf("tcp seq %#x", binary.BigEndian.Uint32(t1[OffTCPSeq:]))
	}
	if t1[OffTCPDataOff] != 0x50 { // 5 words << 4
		t.Errorf("tcp data offset byte %#x, want 0x50", t1[OffTCPDataOff])
	}
	if t1[OffTCPFlags] != FlagSYN {
		t.Errorf("tcp flags %#x, want SYN", t1[OffTCPFlags])
	}
	if binary.BigEndian.Uint16(t1[OffTCPWindow:]) != 0x1680 {
		t.Errorf("tcp window %#x", binary.BigEndian.Uint16(t1[OffTCPWindow:]))
	}
	if !ChecksumValid(srcIP, dstIP, t1[:TCPHeaderLen]) {
		t.Errorf("tcp checksum not valid")
	}
}

// TestParseSegmentRejectsNonTCP asserts ParseSegment rejects a UDP frame and a
// too-short frame.
func TestParseSegmentRejectsNonTCP(t *testing.T) {
	udp := frame.BuildUDPFrame(frame.UDP{
		DstMAC: dstMAC, SrcMAC: srcMAC, SrcIP: srcIP, DstIP: dstIP,
		SrcPort: 1, DstPort: 2, Payload: []byte("x"),
	})
	if _, ok := ParseSegment(udp); ok {
		t.Error("ParseSegment accepted a UDP frame")
	}
	if _, ok := ParseSegment(make([]byte, 10)); ok {
		t.Error("ParseSegment accepted a too-short frame")
	}
}

// TestParseSegmentSkipsOptions asserts ParseSegment locates the payload past TCP
// options (data offset > 5) — real servers send MSS/SACK/timestamp options in
// the SYN-ACK. The Z80 client must read the data offset, not assume 20.
func TestParseSegmentSkipsOptions(t *testing.T) {
	// Hand-build a frame with a 24-byte TCP header (data offset 6) and a 4-byte
	// option, then 3 payload bytes.
	payload := []byte("abc")
	opt := []byte{0x02, 0x04, 0x05, 0xb4} // MSS = 1460
	f := make([]byte, OffL4+24+len(payload))
	binary.BigEndian.PutUint16(f[frame.OffEtherType:], frame.EtherTypeIPv4)
	f[frame.OffIPVerIHL] = 0x45
	f[frame.OffIPProto] = ProtoTCP
	binary.BigEndian.PutUint16(f[frame.OffIPTotalLen:], uint16(frame.IPHeaderLen+24+len(payload)))
	tcphdr := f[OffL4:]
	tcphdr[OffTCPDataOff] = 6 << 4 // 24-byte header
	copy(tcphdr[TCPHeaderLen:], opt)
	copy(tcphdr[24:], payload)
	got, ok := ParseSegment(f)
	if !ok {
		t.Fatal("ParseSegment rejected an optioned segment")
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("payload past options = %q, want %q", got.Payload, payload)
	}
}
