package tftp

import (
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
)

// ClientFront is the Go authority for the i82 TFTP client's request-origination
// front — the step that runs *before* ClientLoop's receive side (plan §2 steps
// 1-2). trinload has no fresh-frame origination (every send is a reply-by-swap,
// plan §0), so the client builds these frames from scratch:
//
//  1. ARPRequest()              -> broadcast an ARP request for the server's IP
//  2. OnARPReply(reply)         -> learn the server MAC from a matching reply
//  3. RRQFrame(filename)        -> the RRQ frame (client TID -> server :69)
//
// After RRQFrame the client enters ClientLoop.tftp_recv_data to pull the file.
// ClientFront owns only the originate state (the learned server MAC); the
// negotiated blksize/TID accounting belong to ClientLoop. The RRQ requests the
// settled ClientOptionSet (research note §5.7).
//
// This is the byte-for-byte porting spec for the Z80 tftp_client_front.asm:
// ARPRequest() == build_arp_request's frame, RRQFrame() == build_rrq wrapped by
// build_udp_frame, and OnARPReply mirrors the Z80 learn-server-MAC step.
type ClientFront struct {
	ClientMAC frame.MAC
	ClientIP  frame.IPv4
	ServerIP  frame.IPv4 // the IP we resolve + send the RRQ to
	ClientTID uint16     // the client's own source port (its TID)

	serverMAC frame.MAC
	gotMAC    bool
}

// NewClientFront starts an originate front for a fetch of files from serverIP.
func NewClientFront(clientMAC frame.MAC, clientIP, serverIP frame.IPv4, clientTID uint16) *ClientFront {
	return &ClientFront{
		ClientMAC: clientMAC,
		ClientIP:  clientIP,
		ServerIP:  serverIP,
		ClientTID: clientTID,
	}
}

// ARPRequest builds the broadcast ARP request asking "who has ServerIP?" — the
// first frame the client puts on the wire (plan §2 step 1). It reuses the
// fresh-frame ARP primitive frame.BuildARPRequest.
func (c *ClientFront) ARPRequest() []byte {
	return frame.BuildARPRequest(c.ClientMAC, c.ClientIP, c.ServerIP)
}

// OnARPReply processes a received frame: if it is an ARP reply from ServerIP it
// learns the server MAC and returns true; any other frame (a non-ARP frame, or
// an ARP reply for a different IP) is ignored and returns false. The client
// keeps reading until a matching reply arrives.
func (c *ClientFront) OnARPReply(f []byte) bool {
	mac, ip, ok := frame.ParseARPReply(f)
	if !ok || ip != c.ServerIP {
		return false
	}
	c.serverMAC = mac
	c.gotMAC = true
	return true
}

// ServerMAC returns the learned server MAC (valid only after OnARPReply
// returned true).
func (c *ClientFront) ServerMAC() frame.MAC { return c.serverMAC }

// GotMAC reports whether a matching ARP reply has been seen.
func (c *ClientFront) GotMAC() bool { return c.gotMAC }

// RRQFrame builds the read-request frame for filename: the RRQ payload (octet
// mode, the settled ClientOptionSet) wrapped as a UDP datagram from the client
// (its IP + TID) to the learned server (its MAC + IP, port 69). Plan §2 step 2.
// It must be called after a successful OnARPReply (it uses the learned MAC).
func (c *ClientFront) RRQFrame(filename string) []byte {
	payload := BuildRRQ(filename, "octet", ClientOptionSet)
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC:  c.serverMAC,
		SrcMAC:  c.ClientMAC,
		SrcIP:   c.ClientIP,
		DstIP:   c.ServerIP,
		SrcPort: c.ClientTID,
		DstPort: 69,
		Payload: payload,
	})
}
