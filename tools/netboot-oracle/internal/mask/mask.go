// Package mask rewrites the real LAN MACs and IPs in a captured frame to
// documentation-range placeholders, so extracted golden vectors can be
// committed without exposing Pete's network (publication policy). The protocol
// structure — every field offset, option, and the option-43 blob — is left
// byte-identical; only the address bytes change, and the IP/UDP checksums are
// recomputed (UDP zeroed, legal for IPv4).
//
// Placeholder addresses (stable, reviewable):
//   - SAM/server IP   192.0.2.1     (RFC 5737 TEST-NET-1)
//   - client IP       192.0.2.44
//   - server MAC      02:00:00:00:00:01 (locally-administered)
//   - client MAC      02:00:00:00:00:44
//   - subnet mask     255.255.255.0
//   - broadcast       192.0.2.255
package mask

import "encoding/binary"

// Placeholder addresses.
var (
	ServerIP  = [4]byte{192, 0, 2, 1}
	ClientIP  = [4]byte{192, 0, 2, 44}
	Broadcast = [4]byte{192, 0, 2, 255}
	ServerMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	ClientMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
)

// AddrMap maps each real address seen in a capture to its placeholder.
type AddrMap struct {
	macs map[[6]byte][6]byte
	ips  map[[4]byte][4]byte
}

// NewAddrMap builds a substitution map. realServerMAC/realClientMAC and
// realServerIPs/realClientIPs identify the endpoints; every other MAC/IP is
// left untouched (broadcast, 0.0.0.0, the option-43 ASCII, etc.).
func NewAddrMap(realServerMAC, realClientMAC [6]byte, serverIPs, clientIPs [][4]byte) *AddrMap {
	m := &AddrMap{macs: map[[6]byte][6]byte{}, ips: map[[4]byte][4]byte{}}
	m.macs[realServerMAC] = ServerMAC
	m.macs[realClientMAC] = ClientMAC
	for _, ip := range serverIPs {
		m.ips[ip] = ServerIP
	}
	for _, ip := range clientIPs {
		m.ips[ip] = ClientIP
	}
	return m
}

func (m *AddrMap) mac(b []byte) {
	var k [6]byte
	copy(k[:], b)
	if v, ok := m.macs[k]; ok {
		copy(b, v[:])
	}
}

func (m *AddrMap) ip(b []byte) {
	var k [4]byte
	copy(k[:], b)
	if v, ok := m.ips[k]; ok {
		copy(b, v[:])
	}
}

// placeholderUUID overwrites a DHCP option-97 value with a deterministic,
// device-anonymous placeholder, preserving its first (type) byte and length.
func placeholderUUID(v []byte) {
	if len(v) == 0 {
		return
	}
	for i := 1; i < len(v); i++ {
		v[i] = byte(i) // 01 02 03 ... — fixed, no device identity
	}
}

// frame offsets (duplicated here to avoid importing the frame package into an
// internal generator helper).
const (
	offDstMAC      = 0
	offSrcMAC      = 6
	offIPVerIHL    = 14
	offIPChecksum  = 24
	offIPSrc       = 26
	offIPDst       = 30
	offUDPChecksum = 40
	offUDPPayload  = 42
)

// Frame masks one Ethernet/IPv4/UDP frame in place and returns it. It rewrites
// the L2 MACs, the L3 IPs, any IPs embedded in a DHCP body (siaddr/yiaddr/
// chaddr/server-id/router), recomputes the IP header checksum, and zeroes the
// UDP checksum.
func (m *AddrMap) Frame(f []byte) []byte {
	if len(f) < offUDPPayload {
		return f
	}
	m.mac(f[offDstMAC : offDstMAC+6])
	m.mac(f[offSrcMAC : offSrcMAC+6])
	m.ip(f[offIPSrc : offIPSrc+4])
	m.ip(f[offIPDst : offIPDst+4])

	// DHCP bodies embed the client MAC + several IPs; mask those too so the
	// committed vector is fully scrubbed and self-consistent.
	if isDHCP(f) {
		m.maskDHCPBody(f[offUDPPayload:])
	} else {
		// TFTP (or other UDP): scrub the device serial that the Pi prepends to
		// its serial-subdir filenames (e.g. "e0ff06da/..."), which derives from
		// its MAC. The structural behaviour (404 a serial-subdir prefix) is
		// preserved; only the identifying value changes.
		maskSerialPrefix(f[offUDPPayload:])
	}

	// Recompute the IP header checksum over the (masked) 20-byte header.
	binary.BigEndian.PutUint16(f[offIPChecksum:], 0)
	binary.BigEndian.PutUint16(f[offIPChecksum:], ipChecksum(f[offIPVerIHL:offIPVerIHL+20]))
	// Zero the UDP checksum (legal for IPv4 — matches the SAM's behaviour).
	binary.BigEndian.PutUint16(f[offUDPChecksum:], 0)
	return f
}

func isDHCP(f []byte) bool {
	if len(f) < offUDPPayload+8 {
		return false
	}
	sport := binary.BigEndian.Uint16(f[34:])
	dport := binary.BigEndian.Uint16(f[36:])
	return sport == 67 || sport == 68 || dport == 67 || dport == 68
}

// DHCP body offsets within the UDP payload.
const (
	dhcpYIAddr  = 16
	dhcpSIAddr  = 20
	dhcpCHAddr  = 28
	dhcpCookie  = 236
	dhcpOptions = 240
)

func (m *AddrMap) maskDHCPBody(p []byte) {
	if len(p) < dhcpOptions {
		return
	}
	m.ip(p[dhcpYIAddr : dhcpYIAddr+4])
	m.ip(p[dhcpSIAddr : dhcpSIAddr+4])
	m.mac(p[dhcpCHAddr : dhcpCHAddr+6])
	// Mask IPs inside well-known options (server-id 54, router 3, DNS 6,
	// netmask 1, broadcast 28, requested-IP 50). Other options (60, 97, 43,
	// 53, 55, 51/58/59) are left intact.
	off := dhcpOptions
	for off < len(p) {
		code := p[off]
		off++
		if code == 0 {
			continue
		}
		if code == 255 || off >= len(p) {
			break
		}
		l := int(p[off])
		off++
		if off+l > len(p) {
			break
		}
		switch code {
		case 54, 3, 6, 50: // IP-valued options
			if l == 4 {
				m.ip(p[off : off+4])
			}
		case 1: // netmask -> fixed placeholder
			if l == 4 {
				copy(p[off:off+4], []byte{255, 255, 255, 0})
			}
		case 28: // broadcast -> fixed placeholder
			if l == 4 {
				copy(p[off:off+4], Broadcast[:])
			}
		case 97: // client-machine-id (UUID) — opaque, echoed verbatim
			// The Pi's opt-97 value embeds its own MAC/serial (device-
			// identifying). Overwrite it with a deterministic placeholder: the
			// protocol behaviour (echo verbatim) is independent of the value, so
			// a fixed UUID keeps the vector a faithful structural oracle while
			// scrubbing the device identity. Byte 0 is the type tag (kept).
			placeholderUUID(p[off : off+l])
		}
		off += l
	}
}

// maskSerialPrefix rewrites any run of 8 lowercase-hex digits immediately
// followed by '/' to "00000000" inside a UDP payload (TFTP filenames and the
// ERROR message path both embed the Pi's device serial, e.g. "e0ff06da/...").
func maskSerialPrefix(p []byte) {
	isHex := func(c byte) bool { return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') }
	for i := 0; i+9 <= len(p); i++ {
		if p[i+8] != '/' {
			continue
		}
		ok := true
		for j := 0; j < 8; j++ {
			if !isHex(p[i+j]) {
				ok = false
				break
			}
		}
		if ok {
			copy(p[i:i+8], []byte("00000000"))
		}
	}
}

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
