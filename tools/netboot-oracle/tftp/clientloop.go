package tftp

import (
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
)

// ClientLoop is the Go authority for the i82 TFTP client transfer loop (the
// receive side), the framed wrapper around ClientXfer that the Z80
// tftp_client_loop.asm ports. It owns the client endpoint (its own IP + TID)
// and the server endpoint (learned from the first DATA's source), and turns a
// received DATA frame into the ACK frame to send back, accumulating the file.
//
// One received packet at a time (the Z80 loop calls drv_read, then OnDATA, then
// drv_write): OnDATA(frame) -> the ACK frame to send (and Done on the short
// final block), an ERROR(5) frame on a wrong server TID, or nil for a duplicate
// the caller need not re-ACK here. OnTimeout applies the Sorcerer's-Apprentice
// fix — retransmit the last ACK frame only, never the RRQ.
//
// Reply framing uses frame.BuildUDPFrame (client IP + client TID -> server IP +
// server TID), reusing the same primitive the server + DHCP loops use.
type ClientLoop struct {
	ClientMAC [6]byte
	ClientIP  [4]byte
	ClientTID uint16 // the client's own source port (its TID)

	serverMAC [6]byte
	serverIP  [4]byte
	serverTID uint16 // learned from the first DATA's source port
	gotServer bool

	xfer *ClientXfer
}

// NewClientLoop starts a client receive loop at the negotiated block size. The
// server TID is learned from the first DATA (RFC 1350 §4): the first response's
// source port becomes the transfer's server TID, and a later DATA from a
// different port is rejected with ERROR(5).
func NewClientLoop(clientMAC [6]byte, clientIP [4]byte, clientTID uint16, blksize int) *ClientLoop {
	return &ClientLoop{
		ClientMAC: clientMAC, ClientIP: clientIP, ClientTID: clientTID,
		xfer: NewClientXfer(blksize, 0),
	}
}

// OnDATA processes a received DATA frame and returns the ACK (or ERROR) frame to
// send, or nil for a future/duplicate block that needs no new reply. It learns
// the server TID from the first DATA and validates it thereafter.
func (c *ClientLoop) OnDATA(dataFrame []byte) []byte {
	u, ok := frame.ParseUDP(dataFrame)
	if !ok || u.DstPort != c.ClientTID {
		return nil
	}
	if Opcode(u.Payload) != OpDATA {
		return nil
	}
	if !c.gotServer {
		// Learn the server endpoint from the first DATA.
		copy(c.serverMAC[:], dataFrame[6:12])
		c.serverIP = u.SrcIP
		c.serverTID = u.SrcPort
		c.gotServer = true
		c.xfer.serverID = u.SrcPort
	}

	act := c.xfer.OnData(u.SrcPort, u.Payload)
	if act.Reply == nil {
		return nil
	}
	if u.SrcPort != c.serverTID {
		// RFC 1350 §4: a DATA from a non-matching source TID gets an ERROR sent
		// to the *source of the incorrect packet* (to tell that stray sender to
		// stop), without disturbing the real transfer — not back to our server.
		var strayMAC [6]byte
		copy(strayMAC[:], dataFrame[6:12])
		return frame.BuildUDPFrame(frame.UDP{
			DstMAC:  strayMAC,
			SrcMAC:  c.ClientMAC,
			SrcIP:   c.ClientIP,
			DstIP:   u.SrcIP,
			SrcPort: c.ClientTID,
			DstPort: u.SrcPort,
			Payload: act.Reply,
		})
	}
	return c.wrap(act.Reply)
}

// OnServerReply dispatches a received server frame in the transfer phase. A
// standards-compliant server answers an *optioned* RRQ with an OACK (RFC 2347)
// BEFORE any DATA: the client must learn the negotiated blksize + the server's
// TID from it and ACK block 0 to start the transfer. A server that does not
// support options instead streams DATA block 1 directly (no OACK) at the 512-byte
// default. This handles both: OACK -> ACK 0 (adopting the negotiated blksize);
// DATA -> OnDATA. It is the entry the driver calls in the transfer phase.
func (c *ClientLoop) OnServerReply(rx []byte) []byte {
	u, ok := frame.ParseUDP(rx)
	if !ok || u.DstPort != c.ClientTID {
		return nil
	}
	switch Opcode(u.Payload) {
	case OpOACK:
		return c.onOACK(rx, u)
	case OpDATA:
		return c.OnDATA(rx)
	default:
		return nil
	}
}

// onOACK learns the server endpoint (the OACK's source becomes the transfer's
// server TID — RFC 1350 §4 / RFC 2347), adopts the server's negotiated blksize
// (absent or unparseable leaves the 512 default in place), and returns the ACK
// for block 0 that tells the server to begin sending DATA at block 1.
func (c *ClientLoop) onOACK(oackFrame []byte, u frame.UDP) []byte {
	if !c.gotServer {
		copy(c.serverMAC[:], oackFrame[6:12])
		c.serverIP = u.SrcIP
		c.serverTID = u.SrcPort
		c.gotServer = true
		c.xfer.serverID = u.SrcPort
	}
	if opts, err := ParseOACK(u.Payload); err == nil {
		if bs, ok := OptionUint(opts, "blksize"); ok && bs > 0 {
			c.xfer.blksize = int(bs)
		}
		// RFC 7440: adopt the server-granted window so the receive loop ACKs only
		// the last block of each window (absent leaves the lock-step default).
		if ws, ok := OptionUint(opts, "windowsize"); ok && ws > 0 {
			c.xfer.SetWindowsize(int(ws))
		}
	}
	return c.wrap(BuildACK(0))
}

// OnTimeout applies the Sorcerer's-Apprentice-Syndrome fix: retransmit the last
// ACK frame only (never the RRQ). Before any block is ACKed it returns nil (the
// caller re-sends the RRQ at that stage, not here).
func (c *ClientLoop) OnTimeout() []byte {
	ack := c.xfer.OnTimeout()
	if ack == nil {
		return nil
	}
	return c.wrap(ack)
}

// Done reports whether the transfer has completed (a short final block).
func (c *ClientLoop) Done() bool { return c.xfer.Done() }

// Bytes returns the accumulated file bytes.
func (c *ClientLoop) Bytes() []byte { return c.xfer.Bytes() }

// wrap frames a TFTP payload as a UDP datagram from the client to the server.
func (c *ClientLoop) wrap(payload []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC:  c.serverMAC,
		SrcMAC:  c.ClientMAC,
		SrcIP:   c.ClientIP,
		DstIP:   c.serverIP,
		SrcPort: c.ClientTID,
		DstPort: c.serverTID,
		Payload: payload,
	})
}
