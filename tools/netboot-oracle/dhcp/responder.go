package dhcp

import (
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
)

// Responder is the Go authority for the i86 SAM DHCP responder loop: the
// dispatch + address-pool logic the Z80 dhcp_loop.asm ports. Given a received
// DHCP request frame it decides DISCOVER -> OFFER / REQUEST -> ACK, allocates a
// client address from a tiny fixed pool keyed by client MAC (so a REQUEST gets
// the same address it was OFFERed), builds the reply body with BuildReply, and
// wraps it as a UDP 67->68 broadcast via frame.BuildUDPFrame.
//
// This is reply-driven: the Pi initiates the DORA cycle (DISCOVER -> OFFER,
// REQUEST -> ACK). The responder owns no timers; the caller's read loop drives
// it one request at a time (the Z80 loop calls drv_read, then OnRequest, then
// drv_write). Non-DISCOVER/REQUEST messages and non-DHCP frames yield nil — the
// loop ignores them and keeps serving.
//
// Config (the SAM's fixed network identity) mirrors the captured proxyDHCP
// behaviour (oracle §1) and the plan §3.1 template.
type Responder struct {
	ServerMAC  [6]byte // the SAM's MAC (Ethernet source of the reply)
	ServerIP   [4]byte // the SAM's IP: siaddr / server-id (54) / router (3)
	SubnetMask [4]byte // option 1
	Broadcast  [4]byte // option 28 (the subnet broadcast)
	LeaseSecs  uint32  // option 51
	T1Secs     uint32  // option 58
	T2Secs     uint32  // option 59

	// poolBase is the first address handed out; successive new clients get
	// poolBase, poolBase+1, ... (low byte incremented), up to poolSize.
	poolBase [4]byte
	poolSize int
	leases   map[[6]byte][4]byte // client MAC -> assigned address
	nextIdx  int
}

// NewResponder builds a responder with a fixed address pool of poolSize
// addresses starting at poolBase (mirroring dnsmasq's 192.168.50.10-.20 range;
// for the direct-cable single-Pi case poolSize 1 suffices, but the LAN case is
// in scope so the table is always present — plan §3.1).
func NewResponder(serverMAC [6]byte, serverIP, subnet, broadcast, poolBase [4]byte, poolSize int, leaseSecs, t1, t2 uint32) *Responder {
	if poolSize < 1 {
		poolSize = 1
	}
	return &Responder{
		ServerMAC: serverMAC, ServerIP: serverIP,
		SubnetMask: subnet, Broadcast: broadcast,
		LeaseSecs: leaseSecs, T1Secs: t1, T2Secs: t2,
		poolBase: poolBase, poolSize: poolSize,
		leases: map[[6]byte][4]byte{},
	}
}

// lease returns the address assigned to a client MAC, allocating a fresh one
// from the pool on first contact. A client that REQUESTs after DISCOVERing gets
// the same address (the lease table is keyed by MAC). The pool wraps when
// exhausted (the LAN edge case; a real server would track expiry — out of scope
// for the netboot responder, which serves a handful of Pis).
func (r *Responder) lease(mac [6]byte) [4]byte {
	if a, ok := r.leases[mac]; ok {
		return a
	}
	a := r.poolBase
	a[3] += byte(r.nextIdx % r.poolSize)
	r.leases[mac] = a
	r.nextIdx++
	return a
}

// OnRequest processes one received Ethernet frame. If it is a DHCP DISCOVER it
// returns the OFFER frame; if a REQUEST, the ACK frame; otherwise nil (ignored,
// keep serving). The returned frame is the complete Ethernet/IPv4/UDP broadcast
// reply, ready for drv_write.
func (r *Responder) OnRequest(reqFrame []byte) []byte {
	u, ok := frame.ParseUDP(reqFrame)
	if !ok || u.DstPort != 67 {
		return nil // not a DHCP-server-bound frame
	}
	msg, err := Parse(u.Payload)
	if err != nil || msg.Op != OpRequest {
		return nil
	}

	var replyType byte
	switch msg.MsgType() {
	case MsgDiscover:
		replyType = MsgOffer
	case MsgRequest:
		replyType = MsgACK
	default:
		return nil // DECLINE/RELEASE/INFORM etc.: not part of the netboot DORA
	}

	var uuid []byte
	if o := msg.Option(OptClientUUID); o != nil {
		uuid = o.Value
	}
	body := BuildReply(ReplyParams{
		MsgType:    replyType,
		XID:        msg.XID,
		YIAddr:     r.lease(msg.CHAddr),
		ServerIP:   r.ServerIP,
		SubnetMask: r.SubnetMask,
		Broadcast:  r.Broadcast,
		LeaseSecs:  r.LeaseSecs, T1Secs: r.T1Secs, T2Secs: r.T2Secs,
		CHAddr:     msg.CHAddr,
		ClientUUID: uuid,
		Flags:      msg.Flags, // echo the request's broadcast flag
	})

	return frame.BuildUDPFrame(frame.UDP{
		DstMAC:  frame.BroadcastMAC,
		SrcMAC:  r.ServerMAC,
		SrcIP:   r.ServerIP,
		DstIP:   frame.BroadcastIP,
		SrcPort: 67,
		DstPort: 68,
		Payload: body,
	})
}
