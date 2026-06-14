// Package server is the Go authority for the integrated netboot server (i95) —
// the combined main-loop dispatcher the Z80 src/netboot/netboot_server.asm
// ports. It is the headline Phase-3 program: a SAM + Quazar Trinity that
// netboots a Raspberry Pi by answering its ARP, DHCP, and TFTP requests, serving
// boot files by name from a flat store.
//
// The individual state machines are already host-verified (the DHCP responder,
// the TFTP server transfer loop, the ARP bring-up responder). This package
// composes them into one frame-in / reply-out handler that routes a received
// frame to the right responder:
//
//	OnFrame(rxFrame) -> reply, or nil to stay silent:
//	  ARP request for our IP        -> an ARP reply           (smoke.Responder)
//	  UDP dst 67 (DHCP DISCOVER/REQUEST) -> an OFFER/ACK      (dhcp.Responder)
//	  UDP dst 69 (TFTP RRQ)         -> an OACK / ERROR(1)      (tftp.ServerLoop)
//	  UDP dst = our transfer TID (TFTP ACK) -> the next DATA   (tftp.ServerLoop)
//	  anything else                 -> nil (keep serving)
//
// This mirrors how the Z80 integrated loop dispatches one drv_read frame: it is
// the byte-for-byte porting spec for netboot_server.asm. Each sub-responder is
// already frame-in/frame-out (it returns nil for a frame not bound to it), so
// the dispatch is just "try each in turn, return the first non-nil reply" — with
// the TFTP transfer needing a SetSource on a resolved RRQ (the B-DOS store walk
// on the SAM; here a ByteSource over the in-memory store).
//
// Verification: host-verifiable end-to-end over the i80 emulation. The Z80
// netboot_server.asm runs the real driver against the emulated Trinity and a
// full injected DISCOVER->OFFER->REQUEST->ACK->RRQ->OACK->ACK->DATA session is
// asserted byte-for-byte against this authority. NOT host-verifiable: the real
// ENC28J60 silicon, the B-DOS RST-8 hook dispatch, and an end-to-end Pi boot —
// gated on real Trinity (CLAUDE.md §5). Emulation-verified is not hardware-
// verified.
package server

import (
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/dhcp"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/smoke"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
)

// Config is the SAM's fixed identity + DHCP pool, shared by every sub-responder
// so they all speak as one host.
type Config struct {
	ServerMAC [6]byte
	ServerIP  [4]byte
	Subnet    [4]byte
	Broadcast [4]byte
	PoolBase  [4]byte
	PoolSize  int
	LeaseSecs uint32
	T1, T2    uint32
	ServerTID uint16 // the SAM's ephemeral source port for TFTP transfers
}

// Server is the integrated netboot server. It owns the three sub-responders and
// the flat store, and routes each received frame.
type Server struct {
	cfg   Config
	store tftp.Store
	src   func(name string) tftp.Source // the file source for a resolved name

	arp  *smoke.Responder
	dhcp *dhcp.Responder
	tftp *tftp.ServerLoop

	// justOACKed is set after an OACK reply: the next ACK (of block 0) triggers
	// FirstData (block 1) rather than the OnACK advance, matching the Z80 loop's
	// tftp_first_data / tftp_handle_ack split.
	justOACKed bool
}

// New builds an integrated server over a flat store. src(name) yields the file
// Source for a resolved filename (the streamed object behind the store) — on the
// SAM this is the B-DOS record walk; in the harness a ByteSource.
func New(cfg Config, store tftp.Store, src func(name string) tftp.Source) *Server {
	return &Server{
		cfg:   cfg,
		store: store,
		src:   src,
		arp:   smoke.NewResponder(cfg.ServerMAC, cfg.ServerIP),
		dhcp: dhcp.NewResponder(cfg.ServerMAC, cfg.ServerIP, cfg.Subnet, cfg.Broadcast,
			cfg.PoolBase, cfg.PoolSize, cfg.LeaseSecs, cfg.T1, cfg.T2),
		tftp: tftp.NewServerLoop(store, cfg.ServerMAC, cfg.ServerIP, cfg.ServerTID),
	}
}

// OnFrame routes one received Ethernet frame to the right sub-responder and
// returns the reply frame to transmit, or nil to stay silent. The order matches
// the Z80 dispatch: ARP first (cheapest, EtherType test), then the UDP
// responders (each gated on its dst port). An RRQ that resolves to a file
// installs the transfer source before the OACK so the following ACKs stream it.
func (s *Server) OnFrame(rx []byte) []byte {
	// 1. ARP request for our IP -> an ARP reply.
	if reply := s.arp.OnFrame(rx); reply != nil {
		return reply
	}

	// Only IPv4/UDP frames can be DHCP or TFTP; anything else is ignored.
	u, ok := frame.ParseUDP(rx)
	if !ok {
		return nil
	}

	// 2. DHCP DISCOVER/REQUEST (UDP dst 67) -> an OFFER/ACK.
	if u.DstPort == 67 {
		return s.dhcp.OnRequest(rx)
	}

	// 3. TFTP RRQ (UDP dst 69) -> an OACK (hit) / ERROR(1) (miss). On a hit,
	//    install the transfer source for the resolved name first, so the OACK
	//    and the streamed DATA come from the same object, and arm the FirstData
	//    handoff for the client's ACK of block 0.
	if u.DstPort == 69 {
		s.justOACKed = false
		if req, err := tftp.ParseRequest(u.Payload); err == nil && req.Opcode == tftp.OpRRQ {
			if action, _ := tftp.Resolve(s.store, req.Filename); action == tftp.ActionOACK {
				s.tftp.SetSource(s.src(req.Filename))
				s.justOACKed = true
			}
		}
		return s.tftp.OnRRQ(rx)
	}

	// 4. TFTP ACK during a transfer (UDP dst = our transfer TID) -> the next
	//    DATA, the FirstData (block 1) on the ACK of block 0, or nil at the end.
	if u.DstPort == s.cfg.ServerTID {
		if s.justOACKed {
			s.justOACKed = false
			return s.tftp.FirstData()
		}
		return s.tftp.OnACK(rx)
	}

	return nil
}
