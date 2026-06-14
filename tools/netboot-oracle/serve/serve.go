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
//	  UDP dst = our transfer TID (ACK)  -> the next DATA       (tftp.ServerLoop)
//	  anything else                     -> nil (keep serving)
//
// The ARP responder is what lets a plain TFTP client resolve the SAM's MAC before
// its RRQ (there is no DHCP here to do it). The one behaviour beyond the i95 Pi
// server: an RRQ with **no options** is answered per RFC 2347 with DATA block 1
// directly (no OACK), which is what a classic `tftp get` sends; an RRQ that does
// request options (e.g. `curl`'s tsize) is answered with an OACK as before.
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

	// 2. TFTP RRQ (UDP dst 69). On a hit install the transfer source, then either
	//    OACK (options requested) or DATA block 1 directly (a bare RRQ, RFC 2347).
	if u.DstPort == 69 {
		r.justFirst = false
		req, err := tftp.ParseRequest(u.Payload)
		hit := false
		hasOpts := false
		if err == nil && req.Opcode == tftp.OpRRQ {
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
