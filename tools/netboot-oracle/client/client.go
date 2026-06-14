// Package client is the Go authority for the i82 TFTP client fetch driver — the
// combined phase machine the Z80 src/netboot/netboot_client.asm ports. It is the
// client boot disk (Phase-3 increment 3): a SAM + Quazar Trinity that fetches a
// file (a .mgt disk image) from a TFTP server and hands the received bytes to the
// B-DOS write-out (i93) to land on Trinity storage.
//
// It composes the two already-host-verified client halves into one driver:
//
//   - ClientFront (clientfront.go): ARP-for-server -> learn MAC -> send the RRQ,
//   - ClientLoop  (clientloop.go):  receive DATA -> send ACK, accumulate the file,
//     end on the short final block; ERROR(5) a stray TID; SAS retransmit on timeout.
//
// The driver is a small phase machine (trinload has no fresh-frame origination, so
// the client originates the whole ARP->RRQ->DATA/ACK flow):
//
//	First()         -> the broadcast ARP request frame (the first thing on the wire)
//	OnFrame(rx)     -> the next frame to send, plus done:
//	  phase ARP : an ARP reply for ServerIP   -> the RRQ frame   (learn server MAC)
//	              any other frame              -> nil, false      (keep waiting)
//	  phase XFER: an OACK (optioned-RRQ server) -> ACK 0           (start transfer)
//	              a DATA frame to our TID       -> the ACK frame   (accumulate)
//	              the short final block         -> the ACK frame, done=true
//	              a DATA from a stray TID       -> an ERROR(5)     (per RFC 1350 §4)
//	OnTimeout()     -> the SAS retransmit (the last ACK only), or the RRQ if no ACK yet
//	Bytes()         -> the accumulated file bytes (after Done)
//
// This is the byte-for-byte porting spec for the Z80 netboot_client.asm: First()
// == tftp_send_arp's frame, the ARP-phase OnFrame == tftp_recv_arp + tftp_send_rrq,
// the XFER-phase OnFrame == tftp_recv_data, OnTimeout == tftp_recv_timeout.
//
// Verification: host-verifiable end-to-end over the i80 emulation (the wire side —
// the ARP request, the RRQ, the ACK cadence, the accumulated bytes — is asserted
// byte-for-byte against this authority). NOT host-verifiable: the B-DOS RST-8 HSAVE
// that writes the received bytes to Trinity storage (no ROM/SAMDOS in the harness),
// the real ENC28J60 silicon, and an end-to-end fetch on real hardware — gated on
// real Trinity (CLAUDE.md §5). Emulation-verified is not hardware-verified.
package client

import (
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
)

// Phase is the client driver's current step.
type Phase int

const (
	// PhaseARP: waiting for the ARP reply that yields the server MAC.
	PhaseARP Phase = iota
	// PhaseXFER: the RRQ is sent; receiving DATA and sending ACKs.
	PhaseXFER
	// PhaseDone: the transfer is complete (a short final block was received).
	PhaseDone
)

// Config is the client's identity + target.
type Config struct {
	ClientMAC [6]byte
	ClientIP  [4]byte
	ServerIP  [4]byte
	ClientTID uint16 // the client's own source port (its TID)
	Filename  string // the file to fetch
}

// Client is the combined fetch driver.
type Client struct {
	cfg   Config
	phase Phase
	front *tftp.ClientFront
	loop  *tftp.ClientLoop
}

// New builds a client fetch driver for cfg. The transfer's effective block size
// is negotiated at run time: it starts at the 512-byte TFTP default and is raised
// to the server's OACK-granted blksize (the RRQ requests ClientOptionSet's 1428);
// a server that sends no OACK keeps the 512 default. The client handles both.
func New(cfg Config) *Client {
	return &Client{
		cfg:   cfg,
		phase: PhaseARP,
		front: tftp.NewClientFront(cfg.ClientMAC, cfg.ClientIP, cfg.ServerIP, cfg.ClientTID),
		// The loop starts at the 512-byte TFTP default; an OACK negotiates up.
		loop: tftp.NewClientLoop(cfg.ClientMAC, cfg.ClientIP, cfg.ClientTID, 512),
	}
}

// First returns the broadcast ARP request frame — the first thing the client puts
// on the wire (it must learn the server MAC before its RRQ).
func (c *Client) First() []byte {
	return c.front.ARPRequest()
}

// OnFrame processes one received frame and returns the next frame to send (or nil)
// plus whether the transfer has completed. In the ARP phase a matching ARP reply
// yields the RRQ frame and advances to the transfer phase; in the transfer phase
// the server's OACK yields ACK 0 (adopting the negotiated blksize) and a DATA
// frame yields the ACK (or ERROR) frame, with the short final block ending it.
func (c *Client) OnFrame(rx []byte) (tx []byte, done bool) {
	switch c.phase {
	case PhaseARP:
		if !c.front.OnARPReply(rx) {
			return nil, false // not the server's ARP reply: keep waiting
		}
		// Learned the server MAC: send the RRQ and enter the transfer phase.
		c.phase = PhaseXFER
		return c.front.RRQFrame(c.cfg.Filename), false
	case PhaseXFER:
		reply := c.loop.OnServerReply(rx)
		if c.loop.Done() {
			c.phase = PhaseDone
			return reply, true
		}
		return reply, false
	default:
		return nil, true
	}
}

// OnTimeout returns the frame to retransmit on a receive timeout: in the transfer
// phase the SAS fix (the last ACK only, or nil before any ACK — the caller then
// re-sends the RRQ); in the ARP phase nil (the caller re-broadcasts the ARP/RRQ).
func (c *Client) OnTimeout() []byte {
	if c.phase == PhaseXFER {
		return c.loop.OnTimeout()
	}
	return nil
}

// Phase returns the driver's current phase.
func (c *Client) Phase() Phase { return c.phase }

// Done reports whether the transfer has completed.
func (c *Client) Done() bool { return c.phase == PhaseDone }

// Bytes returns the accumulated file bytes (valid after Done). On the SAM these
// are what the B-DOS HSAVE writes to Trinity storage under the requested name.
func (c *Client) Bytes() []byte { return c.loop.Bytes() }

// ServerMAC returns the learned server MAC (valid after the ARP phase).
func (c *Client) ServerMAC() frame.MAC { return c.front.ServerMAC() }
