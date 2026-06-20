// Package serve is the Go authority for the serve-files TFTP demo server (i96) —
// the combined frame-in/reply-out router the Z80 src/netboot/netboot_serve.asm
// ports. It is a focused, plain-TFTP demo: a SAM + Quazar Trinity that serves a
// few small files baked into the binary to an ordinary TFTP client (busybox/BSD
// `tftp`, `curl tftp://…`) on the LAN — TFTP only, no DHCP and no Pi PXE blob.
// That makes it testable from any machine with a stock `tftp`/`curl` client, with
// no Raspberry Pi, DHCP server, or option-43 negotiation involved.
//
// It composes two already-host-verified responders:
//
//	OnFrame(rxFrame) -> reply, or nil to stay silent:
//	  ARP request for our IP            -> an ARP reply        (smoke.Responder)
//	  UDP dst 69 (TFTP RRQ)             -> serve the file by name (tftp.ServerLoop)
//	  UDP dst 69 (TFTP WRQ) (i121a)     -> learn client endpoint, reply ACK-0 or OACK
//	  UDP dst = our transfer TID (ACK)  -> the next DATA       (tftp.ServerLoop)
//	  anything else                     -> nil (keep serving)
//
// The ARP responder is what lets a plain TFTP client resolve the SAM's MAC before
// its RRQ (there is no DHCP here to do it). The one behaviour beyond the i95 Pi
// server: an RRQ with **no options** is answered per RFC 2347 with DATA block 1
// directly (no OACK), which is what a classic `tftp get` sends; an RRQ that does
// request options (e.g. `curl`'s tsize) is answered with an OACK as before.
//
// WRQ handling (i121a — handshake only): a bare WRQ (no options) is answered with
// ACK-0 (`00 04 00 00`); an optioned WRQ is answered with an OACK echoing the
// accepted blksize and the client's tsize. DATA reception (i121b) is not included
// here; this is the handshake brick only.
//
// This mirrors how the Z80 demo loop dispatches one drv_read frame: it is the
// byte-for-byte porting spec for netboot_serve.asm.
//
// Verification: host-verifiable end-to-end over the i80 emulation (the Z80
// netboot_serve.asm runs the real driver against the emulated Trinity and the
// served frames are asserted byte-for-byte against this authority). NOT
// host-verifiable: the real ENC28J60 silicon and an end-to-end run on real
// hardware — gated on real Trinity (CLAUDE.md §5). Emulation-verified is not
// hardware-verified.
package serve

import (
	"strconv"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/smoke"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
)

// Config is the SAM's fixed identity for the demo server (no DHCP pool — there is
// no DHCP here).
type Config struct {
	ServerMAC [6]byte
	ServerIP  [4]byte
	ServerTID uint16 // the SAM's ephemeral source port for TFTP transfers
}

// Responder is the serve-files demo server. It owns the ARP + TFTP sub-responders
// and the flat store, and routes each received frame.
type Responder struct {
	cfg   Config
	store tftp.Store
	src   func(name string) tftp.Source // the file source for a resolved name

	arp  *smoke.Responder
	tftp *tftp.ServerLoop

	// justFirst is set after an OACK reply: the next ACK (of block 0) triggers
	// FirstData (block 1) rather than the OnACK advance. It is NOT set on the
	// bare-RRQ path, where DATA block 1 has already been sent and the next ACK
	// (of block 1) advances normally.
	justFirst bool

	// wrqClient holds the WRQ client endpoint (i121a — handshake only). It is
	// populated on a WRQ and used when wrapping the ACK-0 / OACK reply.
	wrqClient wrqEndpoint
}

// wrqEndpoint is the client-side identity learned from a WRQ frame (i121a).
type wrqEndpoint struct {
	mac [6]byte
	ip  [4]byte
	tid uint16
}

// New builds a serve-files demo server over a flat store. src(name) yields the
// file Source for a resolved filename (on the SAM the file is assembled into the
// binary; in the harness a ByteSource).
func New(cfg Config, store tftp.Store, src func(name string) tftp.Source) *Responder {
	return &Responder{
		cfg:   cfg,
		store: store,
		src:   src,
		arp:   smoke.NewResponder(cfg.ServerMAC, cfg.ServerIP),
		tftp:  tftp.NewServerLoop(store, cfg.ServerMAC, cfg.ServerIP, cfg.ServerTID),
	}
}

// OnFrame routes one received Ethernet frame and returns the reply frame to
// transmit, or nil to stay silent. ARP first (cheapest), then the TFTP RRQ /
// transfer-ACK paths.
func (r *Responder) OnFrame(rx []byte) []byte {
	// 1. ARP request for our IP -> an ARP reply (so a plain client can resolve
	//    the SAM's MAC without DHCP).
	if reply := r.arp.OnFrame(rx); reply != nil {
		return reply
	}

	u, ok := frame.ParseUDP(rx)
	if !ok {
		return nil
	}

	// 2. TFTP request on port 69 (RRQ or WRQ). Parse opcode first to dispatch.
	if u.DstPort == 69 {
		r.justFirst = false
		req, err := tftp.ParseRequest(u.Payload)
		if err != nil {
			return nil
		}

		// WRQ (i121a): learn the client endpoint and reply ACK-0 (bare WRQ)
		// or OACK (optioned WRQ). DATA reception is a later brick (i121b).
		if req.Opcode == tftp.OpWRQ {
			return r.startWrite(u, req)
		}

		// RRQ: on a hit install the transfer source, then either OACK (options
		// requested) or DATA block 1 directly (a bare RRQ, RFC 2347).
		hit := false
		hasOpts := false
		if req.Opcode == tftp.OpRRQ {
			if action, _ := tftp.Resolve(r.store, req.Filename); action == tftp.ActionOACK {
				r.tftp.SetSource(r.src(req.Filename))
				hit = true
				hasOpts = len(req.Options) > 0
			}
		}
		if hit && hasOpts {
			r.justFirst = true
			return r.tftp.StartTransfer(rx, true)
		}
		// A hit with no options -> DATA block 1 directly; a miss -> ERROR(1).
		return r.tftp.StartTransfer(rx, false)
	}

	// 3. TFTP ACK during a transfer (UDP dst = our transfer TID). On the OACK
	//    path the ACK of block 0 -> FirstData (block 1); otherwise advance.
	if u.DstPort == r.cfg.ServerTID {
		if r.justFirst {
			r.justFirst = false
			return r.tftp.FirstData()
		}
		return r.tftp.OnACK(rx)
	}

	return nil
}

// startWrite handles a WRQ (i121a handshake only). It learns the client endpoint
// from the received frame, then replies ACK-0 for a bare WRQ, or an OACK
// echoing the accepted blksize (and tsize the client declared) for an optioned
// WRQ (RFC 2347). DATA reception is deferred to i121b.
//
// Port of netboot_serve.asm handle_wrq (Z80 side).
func (r *Responder) startWrite(u frame.UDP, req *tftp.Request) []byte {
	// Learn the client endpoint from the request frame. HL/IP/TID learned here
	// are used only for the handshake reply; the wire-receive loop (i121b) will
	// use them to validate DATA source ports.
	copy(r.wrqClient.mac[:], u.SrcMAC[:])
	copy(r.wrqClient.ip[:], u.SrcIP[:])
	r.wrqClient.tid = u.SrcPort

	// Bare WRQ (no options) -> ACK-0 (`00 04 00 00`), RFC 1350.
	if len(req.Options) == 0 {
		return r.wrapToWRQClient(tftp.BuildACK(0))
	}

	// Optioned WRQ -> OACK echoing the accepted blksize; mirror blksize clamping
	// from AcceptedBlksize (same logic as the RRQ OACK path). If the client also
	// sent tsize, echo it back unchanged (the server learns the incoming size from
	// the client's declaration — it doesn't add its own).
	blksize := uint64(512)
	if v, ok := req.Option("blksize"); ok {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			blksize = tftp.AcceptedBlksize(n)
		}
	}
	var oackOpts []tftp.Option
	oackOpts = append(oackOpts, tftp.Option{Name: "blksize", Value: strconv.FormatUint(blksize, 10)})
	if ts, ok := req.Option("tsize"); ok {
		oackOpts = append(oackOpts, tftp.Option{Name: "tsize", Value: ts})
	}
	return r.wrapToWRQClient(tftp.BuildOACK(oackOpts))
}

// wrapToWRQClient wraps a TFTP payload as a UDP datagram back to the WRQ client
// (from the SAM's IP + transfer TID to the client's IP + TID).
func (r *Responder) wrapToWRQClient(payload []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC:  r.wrqClient.mac,
		SrcMAC:  r.cfg.ServerMAC,
		SrcIP:   r.cfg.ServerIP,
		DstIP:   r.wrqClient.ip,
		SrcPort: r.cfg.ServerTID,
		DstPort: r.wrqClient.tid,
		Payload: payload,
	})
}
