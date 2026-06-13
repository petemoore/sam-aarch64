// Package dhcp decodes BOOTP/DHCP bodies and builds the OFFER/ACK reply the
// SAM netboot DHCP responder (i86) must emit. The wire-level authority is the
// real Pi 400 capture, distilled in docs/notes/pi-netboot-capture-analysis.md
// §1; the builder here is the Go reference for the Z80 responder.
package dhcp

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// BOOTP/DHCP body field offsets, measured from the start of the UDP payload
// (which is frame offset 42, frame.OffUDPPayload).
const (
	OffOp      = 0   // 1 = BOOTREQUEST, 2 = BOOTREPLY
	OffHType   = 1   // 1 = Ethernet
	OffHLen    = 2   // 6
	OffHops    = 3   //
	OffXID     = 4   // 4 bytes
	OffSecs    = 8   // 2 bytes
	OffFlags   = 10  // 2 bytes; 0x8000 = broadcast
	OffCIAddr  = 12  // 4 bytes
	OffYIAddr  = 16  // 4 bytes — the address handed to the client
	OffSIAddr  = 20  // 4 bytes — next-server (the TFTP server = the SAM)
	OffGIAddr  = 24  // 4 bytes
	OffCHAddr  = 28  // 16 bytes (first HLen are the client MAC)
	OffSName   = 44  // 64 bytes
	OffFile    = 108 // 128 bytes
	OffCookie  = 236 // 4 bytes: 0x63 0x82 0x53 0x63
	OffOptions = 240 // TLV options start here
)

// BOOTP op codes.
const (
	OpRequest = 1
	OpReply   = 2
)

// MagicCookie is the DHCP magic cookie that precedes the options (RFC 2131).
var MagicCookie = [4]byte{0x63, 0x82, 0x53, 0x63}

// DHCP message types (option 53).
const (
	MsgDiscover = 1
	MsgOffer    = 2
	MsgRequest  = 3
	MsgDecline  = 4
	MsgACK      = 5
	MsgNAK      = 6
	MsgRelease  = 7
	MsgInform   = 8
)

// Common option codes (RFC 2132 + PXE).
const (
	OptSubnetMask   = 1
	OptRouter       = 3
	OptDNS          = 6
	OptRequestedIP  = 50
	OptLeaseTime    = 51
	OptMsgType      = 53
	OptServerID     = 54
	OptParamReqList = 55
	OptRenewalT1    = 58
	OptRebindT2     = 59
	OptVendorClass  = 60
	OptVendorEncap  = 43
	OptClientUUID   = 97
	OptEnd          = 255
	OptPad          = 0
)

// PXEClient is the option-60 vendor-class string the responder echoes — present
// in every working capture (oracle §1). The Pi 4 boot ROM's hard requirement is
// the option-43 "Raspberry Pi Boot" blob below, but echoing PXEClient is the
// observed-correct behaviour and tags the offer as a netboot offer on a shared LAN.
var PXEClient = []byte("PXEClient")

// Option43RaspberryPiBoot is the fixed 32-byte PXE vendor-encapsulated-options
// blob the SAM sends verbatim (oracle §1). Its decode:
//
//	06 01 03                        PXE sub-opt 6 DISCOVERY_CONTROL = 3
//	0a 04 00 50 58 45               PXE sub-opt 10 MENU_PROMPT timeout 0, "PXE"
//	09 14 00 00 11 "Raspberry Pi Boot"  sub-opt 9 BOOT_MENU item 0, len 0x11
//	ff                              end
//
// The literal "Raspberry Pi Boot" inside a PXE boot-menu is what the Pi 4 boot
// ROM looks for to accept the offer. Confirmed byte-for-byte against the real
// capture (OFFER frame 194 of rpi400-boot-spectrum4.pcapng).
var Option43RaspberryPiBoot = []byte{
	0x06, 0x01, 0x03,
	0x0a, 0x04, 0x00, 0x50, 0x58, 0x45,
	0x09, 0x14, 0x00, 0x00, 0x11,
	'R', 'a', 's', 'p', 'b', 'e', 'r', 'r', 'y', ' ', 'P', 'i', ' ', 'B', 'o', 'o', 't',
	0xff,
}

// Option is one decoded DHCP option (code + value; pad/end excluded).
type Option struct {
	Code  byte
	Value []byte
}

// Message is a decoded BOOTP/DHCP body.
type Message struct {
	Op      byte
	XID     [4]byte
	Flags   uint16
	YIAddr  [4]byte
	SIAddr  [4]byte
	CHAddr  [6]byte  // client MAC (first HLen bytes of chaddr)
	Options []Option // in wire order
}

// MsgType returns the option-53 message type, or 0 if absent.
func (m *Message) MsgType() byte {
	if o := m.Option(OptMsgType); o != nil && len(o.Value) == 1 {
		return o.Value[0]
	}
	return 0
}

// Option returns the first option with the given code, or nil.
func (m *Message) Option(code byte) *Option {
	for i := range m.Options {
		if m.Options[i].Code == code {
			return &m.Options[i]
		}
	}
	return nil
}

// Parse decodes a BOOTP/DHCP body (the UDP payload of a port-67/68 packet).
func Parse(payload []byte) (*Message, error) {
	if len(payload) < OffOptions {
		return nil, fmt.Errorf("dhcp: body too short (%d bytes)", len(payload))
	}
	if !bytes.Equal(payload[OffCookie:OffCookie+4], MagicCookie[:]) {
		return nil, fmt.Errorf("dhcp: bad magic cookie %x", payload[OffCookie:OffCookie+4])
	}
	m := &Message{Op: payload[OffOp]}
	copy(m.XID[:], payload[OffXID:])
	m.Flags = binary.BigEndian.Uint16(payload[OffFlags:])
	copy(m.YIAddr[:], payload[OffYIAddr:])
	copy(m.SIAddr[:], payload[OffSIAddr:])
	copy(m.CHAddr[:], payload[OffCHAddr:])

	off := OffOptions
	for off < len(payload) {
		code := payload[off]
		off++
		switch code {
		case OptPad:
			continue
		case OptEnd:
			return m, nil
		}
		if off >= len(payload) {
			break
		}
		l := int(payload[off])
		off++
		if off+l > len(payload) {
			return nil, fmt.Errorf("dhcp: option %d overruns body", code)
		}
		v := make([]byte, l)
		copy(v, payload[off:off+l])
		m.Options = append(m.Options, Option{Code: code, Value: v})
		off += l
	}
	return m, nil
}

// ReplyParams configures BuildReply with the SAM responder's fixed
// configuration plus the per-request echoed fields. The values mirror the
// captured dnsmasq/proxyDHCP behaviour (oracle §1).
type ReplyParams struct {
	MsgType    byte    // MsgOffer or MsgACK
	XID        [4]byte // echoed from the request
	YIAddr     [4]byte // assigned from the pool
	ServerIP   [4]byte // the SAM's IP: server-id (54), router (3), siaddr
	SubnetMask [4]byte // option 1
	Broadcast  [4]byte // option 28 (the subnet broadcast)
	LeaseSecs  uint32  // option 51
	T1Secs     uint32  // option 58
	T2Secs     uint32  // option 59
	CHAddr     [6]byte // echoed client MAC -> chaddr
	ClientUUID []byte  // echoed option-97 value (verbatim, 17 bytes typ.)
	Flags      uint16  // echoed request flags (0x8000 = broadcast)
}

// optBroadcast is option 28 (broadcast-address). RFC 2132.
const optBroadcast = 28

// BuildReply builds the DHCP OFFER or ACK body (the UDP payload) the SAM
// responder must emit, per the oracle §1 template: the message-type, the
// SAM-as-everything fields (server-id / router / siaddr), the lease block, the
// subnet/broadcast, the echoed PXEClient vendor-class, the echoed client UUID,
// and the fixed option-43 "Raspberry Pi Boot" blob. Option 66/67 are omitted —
// the Pi requests its own filenames, learning the server from siaddr (oracle §1).
//
// This is the Go authority for the Z80 i86 packet builder. The body is wrapped
// in a UDP/IP/Ethernet frame by frame.BuildUDPFrame (UDP 67 -> 68).
func BuildReply(p ReplyParams) []byte {
	var b bytes.Buffer
	body := make([]byte, OffOptions)
	body[OffOp] = OpReply
	body[OffHType] = 1
	body[OffHLen] = 6
	copy(body[OffXID:], p.XID[:])
	binary.BigEndian.PutUint16(body[OffFlags:], p.Flags)
	copy(body[OffYIAddr:], p.YIAddr[:])
	copy(body[OffSIAddr:], p.ServerIP[:]) // next-server = the SAM
	copy(body[OffCHAddr:], p.CHAddr[:])
	copy(body[OffCookie:], MagicCookie[:])
	b.Write(body)

	writeOpt := func(code byte, val []byte) {
		b.WriteByte(code)
		b.WriteByte(byte(len(val)))
		b.Write(val)
	}
	be32 := func(v uint32) []byte {
		var x [4]byte
		binary.BigEndian.PutUint32(x[:], v)
		return x[:]
	}

	writeOpt(OptMsgType, []byte{p.MsgType})
	writeOpt(OptServerID, p.ServerIP[:])
	writeOpt(OptLeaseTime, be32(p.LeaseSecs))
	writeOpt(OptRenewalT1, be32(p.T1Secs))
	writeOpt(OptRebindT2, be32(p.T2Secs))
	writeOpt(OptSubnetMask, p.SubnetMask[:])
	writeOpt(optBroadcast, p.Broadcast[:])
	writeOpt(OptRouter, p.ServerIP[:])
	writeOpt(OptVendorClass, PXEClient)
	if len(p.ClientUUID) > 0 {
		writeOpt(OptClientUUID, p.ClientUUID)
	}
	writeOpt(OptVendorEncap, Option43RaspberryPiBoot)
	b.WriteByte(OptEnd)
	return b.Bytes()
}
