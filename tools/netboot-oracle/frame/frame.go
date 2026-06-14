// Package frame is the Ethernet/IPv4/UDP framing layer for the netboot
// oracle. It encodes the byte-level offset contract that the SAM-side Z80
// netboot program (src/netboot/) must agree with byte-for-byte — the §1.2
// table of docs/plans/phase3-netboot-implementation-plan.md, itself derived
// from trinload's packet buffer (trinload.asm:419 `packet: defs 1518`).
//
// The Go side is the authority/oracle: BuildUDPFrame is the reference for the
// Z80 `build_udp_frame` primitive (plan §5.1), and the parse accessors mirror
// the offset constants the Z80 reads from the `packet` buffer.
package frame

import "encoding/binary"

// Offsets into a standard Ethernet/IPv4/UDP frame, measured from the start of
// the frame (dst MAC at 0). These are the §1.2 offset contract: the Z80 code
// reads/writes the same fields at the same offsets in its `packet` buffer.
const (
	OffDstMAC      = 0  // 6 bytes
	OffSrcMAC      = 6  // 6 bytes
	OffEtherType   = 12 // 2 bytes, big-endian
	OffIPVerIHL    = 14 // version<<4 | IHL
	OffIPTotalLen  = 16 // 2 bytes, big-endian
	OffIPFlags     = 20 // bit 5 (of the high byte) = More-Fragments
	OffIPProto     = 23 // 0x01 ICMP, 0x11 UDP
	OffIPChecksum  = 24 // 2 bytes, big-endian
	OffIPSrc       = 26 // 4 bytes
	OffIPDst       = 30 // 4 bytes
	OffUDPSrcPort  = 34 // 2 bytes, big-endian (the peer's TID for a reply)
	OffUDPDstPort  = 36 // 2 bytes, big-endian
	OffUDPLen      = 38 // 2 bytes, big-endian (header + payload)
	OffUDPChecksum = 40 // 2 bytes, big-endian (0 = "not computed", legal IPv4)
	OffUDPPayload  = 42 // TFTP opcode / DHCP op start here

	EthHeaderLen = 14
	IPHeaderLen  = 20 // no IP options (IHL=5)
	UDPHeaderLen = 8
	// HeaderLen is the total of all three headers up to the UDP payload.
	HeaderLen = OffUDPPayload // 42
)

// EtherTypes.
const (
	EtherTypeIPv4 = 0x0800
	EtherTypeARP  = 0x0806
)

// IP protocol numbers.
const (
	ProtoICMP = 0x01
	ProtoUDP  = 0x11
)

// MAC is a 6-byte hardware address.
type MAC [6]byte

// IPv4 is a 4-byte IPv4 address.
type IPv4 [4]byte

// Broadcast addresses.
var (
	BroadcastMAC = MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	BroadcastIP  = IPv4{0xff, 0xff, 0xff, 0xff}
)

// UDP describes a UDP/IPv4/Ethernet frame for BuildUDPFrame, plus the decoded
// view returned by ParseUDP.
type UDP struct {
	DstMAC  MAC
	SrcMAC  MAC
	SrcIP   IPv4
	DstIP   IPv4
	SrcPort uint16
	DstPort uint16
	Payload []byte
}

// BuildUDPFrame originates a fresh UDP/IPv4/Ethernet frame from scratch — the
// Go authority for the Z80 `build_udp_frame` primitive (plan §5.1). trinload
// has no fresh-frame origination path (every send is a reply-by-address-swap,
// plan §0); this is the one genuinely new L2/L3 routine the netboot program
// adds. The DHCP responder's broadcast OFFER/ACK reuses it.
//
// It writes the three headers at the §1.2 offsets, appends the payload, and
// fills the IP total-length / UDP-length fields and the IP header checksum
// (RFC 1071). The UDP checksum is left zero — legal for IPv4 (RFC 768), and
// what trinload's checksum_udp does (trinload.asm:353).
func BuildUDPFrame(u UDP) []byte {
	f := make([]byte, HeaderLen+len(u.Payload))

	// Ethernet header.
	copy(f[OffDstMAC:], u.DstMAC[:])
	copy(f[OffSrcMAC:], u.SrcMAC[:])
	binary.BigEndian.PutUint16(f[OffEtherType:], EtherTypeIPv4)

	// IPv4 header (no options, IHL=5).
	f[OffIPVerIHL] = 0x45   // version 4, IHL 5 (20 bytes)
	f[OffIPVerIHL+1] = 0x00 // DSCP/ECN
	ipTotal := IPHeaderLen + UDPHeaderLen + len(u.Payload)
	binary.BigEndian.PutUint16(f[OffIPTotalLen:], uint16(ipTotal))
	// identification (16) / flags+frag (20-21) left zero: DF unset, no frag.
	f[OffIPProto-1] = 64 // TTL at offset 22
	f[OffIPProto] = ProtoUDP
	// IP checksum field (24-25) left zero for the calculation below.
	copy(f[OffIPSrc:], u.SrcIP[:])
	copy(f[OffIPDst:], u.DstIP[:])
	binary.BigEndian.PutUint16(f[OffIPChecksum:], ipChecksum(f[OffIPVerIHL:OffIPVerIHL+IPHeaderLen]))

	// UDP header.
	binary.BigEndian.PutUint16(f[OffUDPSrcPort:], u.SrcPort)
	binary.BigEndian.PutUint16(f[OffUDPDstPort:], u.DstPort)
	binary.BigEndian.PutUint16(f[OffUDPLen:], uint16(UDPHeaderLen+len(u.Payload)))
	// UDP checksum (40-41) left zero — legal for IPv4 (RFC 768).

	copy(f[OffUDPPayload:], u.Payload)
	return f
}

// ARP constants (RFC 826). The client originates an ARP request to learn the
// netboot server's MAC before sending its RRQ (plan §5.1, §2 step 1) — trinload
// has no fresh-frame origination, so this (like BuildUDPFrame) is new code.
const (
	ARPHTypeEthernet = 1      // hardware type: Ethernet
	ARPPTypeIPv4     = 0x0800 // protocol type: IPv4
	ARPHLen          = 6      // hardware address length (MAC)
	ARPPLen          = 4      // protocol address length (IPv4)
	ARPOpRequest     = 1      // operation: request
	ARPOpReply       = 2      // operation: reply

	// ARPFrameLen is the wire length of an Ethernet ARP request: the 14-byte
	// Ethernet header + the 28-byte ARP payload (no trailing padding; the NIC
	// pads to the 60-byte minimum on TX).
	ARPFrameLen = EthHeaderLen + 28
)

// BuildARPRequest originates a fresh Ethernet ARP request (RFC 826) asking
// "who has targetIP?" — the Go authority for the Z80 `build_arp_request`
// primitive (plan §5.1, the ARP variant of the fresh-frame need). The client
// sends it broadcast to learn the server's MAC before its RRQ.
//
// The frame is the 14-byte Ethernet header (broadcast dst, srcMAC, EtherType
// 0x0806) followed by the 28-byte ARP payload: HTYPE=Ethernet, PTYPE=IPv4,
// HLEN=6, PLEN=4, OPER=request, sender MAC/IP = ours, target MAC = zero
// (unknown — the point of the request), target IP = the address we are
// resolving.
func BuildARPRequest(srcMAC MAC, srcIP, targetIP IPv4) []byte {
	f := make([]byte, ARPFrameLen)

	// Ethernet header: broadcast destination, our source, ARP EtherType.
	copy(f[OffDstMAC:], BroadcastMAC[:])
	copy(f[OffSrcMAC:], srcMAC[:])
	binary.BigEndian.PutUint16(f[OffEtherType:], EtherTypeARP)

	// ARP payload begins at offset 14.
	p := f[EthHeaderLen:]
	binary.BigEndian.PutUint16(p[0:], ARPHTypeEthernet)
	binary.BigEndian.PutUint16(p[2:], ARPPTypeIPv4)
	p[4] = ARPHLen
	p[5] = ARPPLen
	binary.BigEndian.PutUint16(p[6:], ARPOpRequest)
	copy(p[8:], srcMAC[:]) // sender hardware address
	copy(p[14:], srcIP[:]) // sender protocol address
	// target hardware address (p[18:24]) left zero — unknown.
	copy(p[24:], targetIP[:]) // target protocol address
	return f
}

// ParseARPReply decodes an Ethernet ARP reply (RFC 826) and returns the sender's
// MAC + IP — the Go authority for the Z80 ARP-reply parse the i82 client does to
// learn the server's MAC (plan §2 step 1). It accepts a frame iff it is an ARP
// reply (EtherType 0x0806, OPER=2) for Ethernet/IPv4 (HTYPE=1, PTYPE=0x0800,
// HLEN=6, PLEN=4); the sender hardware/protocol addresses are the answer to the
// "who has targetIP?" request. The target fields (the asker's own MAC/IP) are
// not validated here — the caller already knows them and matches the sender IP
// against the server IP it asked about.
func ParseARPReply(f []byte) (senderMAC MAC, senderIP IPv4, ok bool) {
	if len(f) < ARPFrameLen {
		return MAC{}, IPv4{}, false
	}
	if binary.BigEndian.Uint16(f[OffEtherType:]) != EtherTypeARP {
		return MAC{}, IPv4{}, false
	}
	p := f[EthHeaderLen:]
	if binary.BigEndian.Uint16(p[0:]) != ARPHTypeEthernet {
		return MAC{}, IPv4{}, false
	}
	if binary.BigEndian.Uint16(p[2:]) != ARPPTypeIPv4 {
		return MAC{}, IPv4{}, false
	}
	if p[4] != ARPHLen || p[5] != ARPPLen {
		return MAC{}, IPv4{}, false
	}
	if binary.BigEndian.Uint16(p[6:]) != ARPOpReply {
		return MAC{}, IPv4{}, false
	}
	copy(senderMAC[:], p[8:14]) // sender hardware address
	copy(senderIP[:], p[14:18]) // sender protocol address
	return senderMAC, senderIP, true
}

// ipChecksum computes the RFC 1071 one's-complement header checksum over a
// 20-byte IPv4 header whose checksum field (bytes 10-11) is zero. This is the
// Go form of trinload's checksum_ip / chksum_blk (trinload.asm:306, :360).
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

// IsIPv4UDP reports whether a frame is a non-fragmented IPv4/UDP frame long
// enough to carry a UDP payload — the trinload accept test (trinload.asm:74,
// :111, :115, :136).
func IsIPv4UDP(f []byte) bool {
	if len(f) < OffUDPPayload {
		return false
	}
	if binary.BigEndian.Uint16(f[OffEtherType:]) != EtherTypeIPv4 {
		return false
	}
	if f[OffIPFlags]&0x20 != 0 { // More-Fragments: trinload drops these
		return false
	}
	return f[OffIPProto] == ProtoUDP
}

// ParseUDP decodes the framing fields of an IPv4/UDP frame and returns the
// payload slice (aliasing f). It does not validate checksums.
func ParseUDP(f []byte) (UDP, bool) {
	if !IsIPv4UDP(f) {
		return UDP{}, false
	}
	udpLen := int(binary.BigEndian.Uint16(f[OffUDPLen:]))
	if udpLen < UDPHeaderLen {
		return UDP{}, false
	}
	payEnd := OffUDPPayload + (udpLen - UDPHeaderLen)
	if payEnd > len(f) {
		payEnd = len(f) // tolerate trailing-truncated captures
	}
	u := UDP{
		SrcPort: binary.BigEndian.Uint16(f[OffUDPSrcPort:]),
		DstPort: binary.BigEndian.Uint16(f[OffUDPDstPort:]),
		Payload: f[OffUDPPayload:payEnd],
	}
	copy(u.DstMAC[:], f[OffDstMAC:])
	copy(u.SrcMAC[:], f[OffSrcMAC:])
	copy(u.SrcIP[:], f[OffIPSrc:])
	copy(u.DstIP[:], f[OffIPDst:])
	return u, true
}
