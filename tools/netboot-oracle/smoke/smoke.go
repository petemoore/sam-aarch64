// Package smoke is the Go authority for the netboot bring-up smoke test (i94) —
// the simplest possible "the Trinity Ethernet path is alive" responder. It does
// one observable network action: answer an ARP request for the SAM's own IP
// with an ARP reply, so a machine on the same LAN (Pete's Pi) sees the SAM's MAC
// appear and confirms the ENC28J60 path comes up and talks.
//
// It is the byte-for-byte porting spec for the Z80 src/netboot/smoke_test.asm:
// OnFrame mirrors the smoke loop's parse-request-then-build-reply step exactly,
// composing frame.ParseARPRequest + frame.BuildARPReply. The Z80 side runs the
// real vendored driver (encdrv.asm) over the i80 emulated Trinity; this Go side
// is the reference its emitted ARP reply is compared against.
package smoke

import (
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
)

// Responder answers ARP requests for the SAM's own IP. It holds only the SAM's
// identity (MAC + IP) — there is no state across requests.
type Responder struct {
	MAC frame.MAC
	IP  frame.IPv4
}

// NewResponder builds a bring-up ARP responder for the SAM's MAC/IP.
func NewResponder(mac frame.MAC, ip frame.IPv4) *Responder {
	return &Responder{MAC: mac, IP: ip}
}

// OnFrame is the whole smoke behaviour: given a received Ethernet frame, return
// the ARP reply to transmit, or nil to stay silent. It replies iff the frame is
// a well-formed ARP request asking for the SAM's own IP (TargetIP == r.IP); any
// other frame — a non-ARP frame, or an ARP request for a different IP — is
// ignored (nil), exactly as the Z80 smoke loop does (keep listening, never
// choke). The reply unicasts back to the asker, announcing r.MAC for r.IP.
func (r *Responder) OnFrame(f []byte) []byte {
	req, ok := frame.ParseARPRequest(f)
	if !ok || req.TargetIP != r.IP {
		return nil
	}
	return frame.BuildARPReply(r.MAC, req.SenderMAC, r.IP, req.SenderIP)
}
