// Package tcp is the TCP/IPv4/Ethernet segment layer for the netboot oracle —
// the transport the i70 firmware self-provisioning HTTP client rides on. The
// existing netboot stack (frame/dhcp/tftp) is UDP-only; HTTP needs TCP, so this
// package adds the one new transport. It is the Go authority/oracle for the
// forthcoming Z80 `build_tcp_segment` / TCP-parse primitives (src/netboot/),
// exactly as frame.BuildUDPFrame is the authority for `build_udp_frame`.
//
// What is genuinely new vs UDP (frame.BuildUDPFrame, plan §5.1):
//
//   - The TCP header is 20 bytes (no options here) with sequence/ack numbers,
//     a data-offset+flags word, a window, and a checksum.
//   - The TCP checksum is mandatory (RFC 793), computed over a pseudo-header
//     (src IP, dst IP, zero, protocol=6, TCP length) prepended to the TCP header
//     plus data. UDP may legally zero its checksum for IPv4 (RFC 768); TCP may
//     not. This is the one arithmetic difference the Z80 port must get right.
//
// Everything else — the Ethernet/IPv4 header layout, the IP header checksum,
// the §1.2 frame offsets — is shared with the frame package and reused here.
//
// Verification: host-verifiable. BuildSegment is pure arithmetic + buffer
// writes, so the Z80 port runs under the koron-go/z80 harness and its output is
// byte-compared against this authority (the same check frame.BuildUDPFrame
// gets). NOT host-verifiable: the ENC28J60 wire I/O timing and a real TCP
// handshake against a live server — gated on i80 / real Trinity (CLAUDE.md §5).
package tcp

import (
	"encoding/binary"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
)

// ProtoTCP is the IP protocol number for TCP (RFC 793).
const ProtoTCP = 0x06

// TCP header field offsets, relative to the start of the TCP header. The TCP
// header begins at the L4 start — the same byte the UDP header would start at:
// offset 34 in the Ethernet/IPv4 frame (frame.OffUDPSrcPort). OffL4 names that
// absolute offset; the constants below are relative to it.
const (
	OffTCPSrcPort  = 0  // 2 bytes, big-endian
	OffTCPDstPort  = 2  // 2 bytes, big-endian
	OffTCPSeq      = 4  // 4 bytes, big-endian
	OffTCPAck      = 8  // 4 bytes, big-endian
	OffTCPDataOff  = 12 // high nibble = data offset in 32-bit words; low nibble reserved
	OffTCPFlags    = 13 // control bits
	OffTCPWindow   = 14 // 2 bytes, big-endian
	OffTCPChecksum = 16 // 2 bytes, big-endian
	OffTCPUrgent   = 18 // 2 bytes, big-endian
	TCPHeaderLen   = 20 // no options (data offset = 5)

	// OffL4 is the byte offset of the L4 (TCP/UDP) header start within the full
	// Ethernet/IPv4 frame — identical to frame.OffUDPSrcPort (34).
	OffL4 = frame.OffUDPSrcPort
	// OffTCPPayload is where the TCP segment data begins in the full frame.
	OffTCPPayload = OffL4 + TCPHeaderLen // 54
)

// TCP control flags (RFC 793).
const (
	FlagFIN = 0x01
	FlagSYN = 0x02
	FlagRST = 0x04
	FlagPSH = 0x08
	FlagACK = 0x10
	FlagURG = 0x20
)

// Segment describes a TCP/IPv4/Ethernet segment for BuildSegment, and is also
// the decoded view returned by ParseSegment.
type Segment struct {
	DstMAC  frame.MAC
	SrcMAC  frame.MAC
	SrcIP   frame.IPv4
	DstIP   frame.IPv4
	SrcPort uint16
	DstPort uint16
	Seq     uint32
	Ack     uint32
	Flags   uint8
	Window  uint16
	Payload []byte
}

// BuildSegment originates a fresh TCP/IPv4/Ethernet segment from scratch — the
// Go authority for the Z80 `build_tcp_segment` primitive. It writes the three
// headers at the frame offsets, fills the IP total-length / header checksum
// (reusing the frame layout) and, crucially, the **TCP checksum over the
// pseudo-header** (the one thing UDP did not need).
func BuildSegment(s Segment) []byte {
	f := make([]byte, OffTCPPayload+len(s.Payload))

	// Ethernet header.
	copy(f[frame.OffDstMAC:], s.DstMAC[:])
	copy(f[frame.OffSrcMAC:], s.SrcMAC[:])
	binary.BigEndian.PutUint16(f[frame.OffEtherType:], frame.EtherTypeIPv4)

	// IPv4 header (no options, IHL=5).
	f[frame.OffIPVerIHL] = 0x45
	f[frame.OffIPVerIHL+1] = 0x00
	ipTotal := frame.IPHeaderLen + TCPHeaderLen + len(s.Payload)
	binary.BigEndian.PutUint16(f[frame.OffIPTotalLen:], uint16(ipTotal))
	f[frame.OffIPProto-1] = 64 // TTL at offset 22
	f[frame.OffIPProto] = ProtoTCP
	copy(f[frame.OffIPSrc:], s.SrcIP[:])
	copy(f[frame.OffIPDst:], s.DstIP[:])
	binary.BigEndian.PutUint16(f[frame.OffIPChecksum:], ipChecksum(f[frame.OffIPVerIHL:frame.OffIPVerIHL+frame.IPHeaderLen]))

	// TCP header.
	t := f[OffL4:]
	binary.BigEndian.PutUint16(t[OffTCPSrcPort:], s.SrcPort)
	binary.BigEndian.PutUint16(t[OffTCPDstPort:], s.DstPort)
	binary.BigEndian.PutUint32(t[OffTCPSeq:], s.Seq)
	binary.BigEndian.PutUint32(t[OffTCPAck:], s.Ack)
	t[OffTCPDataOff] = (TCPHeaderLen / 4) << 4 // data offset = 5 words, reserved = 0
	t[OffTCPFlags] = s.Flags
	binary.BigEndian.PutUint16(t[OffTCPWindow:], s.Window)
	// checksum (16-17) left zero for the computation below.
	// urgent pointer (18-19) left zero.

	copy(f[OffTCPPayload:], s.Payload)

	// TCP checksum over the pseudo-header + TCP header + data.
	binary.BigEndian.PutUint16(t[OffTCPChecksum:], tcpChecksum(s.SrcIP, s.DstIP, t[:TCPHeaderLen+len(s.Payload)]))
	return f
}

// IsIPv4TCP reports whether a frame is a non-fragmented IPv4/TCP frame long
// enough to carry a TCP header — the TCP analogue of frame.IsIPv4UDP.
func IsIPv4TCP(f []byte) bool {
	if len(f) < OffTCPPayload {
		return false
	}
	if binary.BigEndian.Uint16(f[frame.OffEtherType:]) != frame.EtherTypeIPv4 {
		return false
	}
	if f[frame.OffIPFlags]&0x20 != 0 { // More-Fragments
		return false
	}
	return f[frame.OffIPProto] == ProtoTCP
}

// ParseSegment decodes a TCP/IPv4/Ethernet segment and returns the framing
// fields + the payload slice (aliasing f). It tolerates TCP options (data
// offset > 5) by skipping them to locate the payload, and trailing-truncated
// captures. It does not validate the checksum.
func ParseSegment(f []byte) (Segment, bool) {
	if !IsIPv4TCP(f) {
		return Segment{}, false
	}
	ipTotal := int(binary.BigEndian.Uint16(f[frame.OffIPTotalLen:]))
	ipEnd := frame.OffIPVerIHL + ipTotal
	if ipEnd > len(f) {
		ipEnd = len(f) // tolerate trailing truncation
	}
	t := f[OffL4:]
	if len(t) < TCPHeaderLen {
		return Segment{}, false
	}
	dataOff := int(t[OffTCPDataOff]>>4) * 4
	if dataOff < TCPHeaderLen {
		dataOff = TCPHeaderLen
	}
	payStart := OffL4 + dataOff
	if payStart > ipEnd {
		payStart = ipEnd
	}
	s := Segment{
		SrcPort: binary.BigEndian.Uint16(t[OffTCPSrcPort:]),
		DstPort: binary.BigEndian.Uint16(t[OffTCPDstPort:]),
		Seq:     binary.BigEndian.Uint32(t[OffTCPSeq:]),
		Ack:     binary.BigEndian.Uint32(t[OffTCPAck:]),
		Flags:   t[OffTCPFlags],
		Window:  binary.BigEndian.Uint16(t[OffTCPWindow:]),
		Payload: f[payStart:ipEnd],
	}
	copy(s.DstMAC[:], f[frame.OffDstMAC:])
	copy(s.SrcMAC[:], f[frame.OffSrcMAC:])
	copy(s.SrcIP[:], f[frame.OffIPSrc:])
	copy(s.DstIP[:], f[frame.OffIPDst:])
	return s, true
}

// tcpChecksum computes the RFC 793 TCP checksum: the one's-complement word sum
// over the 12-byte IPv4 pseudo-header (src IP, dst IP, zero, protocol=6, TCP
// length) prepended to the TCP header + data, folded and inverted. The seg slice
// is the TCP header (with the checksum field already zero) + the payload.
func tcpChecksum(srcIP, dstIP frame.IPv4, seg []byte) uint16 {
	var sum uint32

	// Pseudo-header: src IP (2 words), dst IP (2 words), zero+proto, TCP length.
	sum += uint32(srcIP[0])<<8 | uint32(srcIP[1])
	sum += uint32(srcIP[2])<<8 | uint32(srcIP[3])
	sum += uint32(dstIP[0])<<8 | uint32(dstIP[1])
	sum += uint32(dstIP[2])<<8 | uint32(dstIP[3])
	sum += uint32(ProtoTCP) // zero byte (0) << 8 | protocol
	sum += uint32(len(seg))

	// TCP header + data, big-endian words. If the length is odd, the final byte
	// is treated as the high byte of a word padded with a zero low byte.
	i := 0
	for ; i+1 < len(seg); i += 2 {
		sum += uint32(seg[i])<<8 | uint32(seg[i+1])
	}
	if i < len(seg) {
		sum += uint32(seg[i]) << 8
	}

	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// ChecksumValid reports whether a received TCP segment's checksum verifies: the
// one's-complement word sum over the pseudo-header + segment is 0xffff. Used in
// tests to confirm a built segment is self-consistent (mirrors the IP-checksum
// validity helper in oracle_test.go).
func ChecksumValid(srcIP, dstIP frame.IPv4, seg []byte) bool {
	var sum uint32
	sum += uint32(srcIP[0])<<8 | uint32(srcIP[1])
	sum += uint32(srcIP[2])<<8 | uint32(srcIP[3])
	sum += uint32(dstIP[0])<<8 | uint32(dstIP[1])
	sum += uint32(dstIP[2])<<8 | uint32(dstIP[3])
	sum += uint32(ProtoTCP)
	sum += uint32(len(seg))
	i := 0
	for ; i+1 < len(seg); i += 2 {
		sum += uint32(seg[i])<<8 | uint32(seg[i+1])
	}
	if i < len(seg) {
		sum += uint32(seg[i]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return sum == 0xffff
}

// ipChecksum computes the RFC 1071 one's-complement header checksum over a
// 20-byte IPv4 header whose checksum field is zero (the same routine the frame
// package uses; duplicated to keep packages decoupled).
func ipChecksum(hdr []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(hdr); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(hdr[i:]))
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
