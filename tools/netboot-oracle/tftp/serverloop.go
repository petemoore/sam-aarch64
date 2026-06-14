package tftp

import (
	"strconv"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
)

// ServerLoop is the Go authority for the i83 TFTP server transfer loop: the
// reply-driven state machine the Z80 tftp_server_loop.asm ports. It composes the
// already-modelled pieces — Resolve (serve-by-name: 404 serial-subdir, OACK a
// hit, ERROR(1) every miss), AcceptedBlksize, BuildOACK/BuildDATA/BuildError,
// ServerXfer (the streamed DATA/ACK cadence) — into one packet-in / frame-out
// handler, and wraps every reply as a UDP frame back to the client TID.
//
// The handler is one received packet at a time (the Z80 loop calls drv_read,
// then OnRRQ / OnACK, then drv_write):
//
//	OnRRQ(rrqFrame): parse + resolve. On a hit, start a ServerXfer at the
//	  negotiated blksize and reply with the OACK frame (tsize=real size, echo
//	  blksize) — the client then ACKs block 0 to start the data flow. On a miss
//	  or a serial-subdir prefix, reply with an ERROR(1) frame and serve nothing
//	  (keep serving the session).
//	OnACK(ackFrame): advance the ServerXfer and return the next DATA frame, or
//	  nil when the short final block has been ACKed (transfer complete).
//
// Reply framing uses frame.BuildUDPFrame (the fresh-frame primitive): src = the
// SAM (server IP + the transfer TID), dst = the client (its IP + its RRQ source
// port, the client TID). This matches the captured exchange (server replies
// from an ephemeral TID to the client TID) and lets the Z80 reuse the same
// build_udp_frame the DHCP loop uses, rather than a trinload swap chain.
type ServerLoop struct {
	store     Store
	ServerMAC [6]byte
	ServerIP  [4]byte
	ServerTID uint16 // the SAM's ephemeral source port for this transfer

	// Per-transfer state, set by OnRRQ.
	clientMAC  [6]byte
	clientIP   [4]byte
	clientTID  uint16 // the client's RRQ source port (its TID)
	xfer       *ServerXfer
	pendingSrc Source // the file source installed by SetSource, used at OnRRQ
}

// NewServerLoop builds a server loop over the given store. src(name) yields the
// file Source for a resolved name (the streamed object behind the flat store).
func NewServerLoop(store Store, serverMAC [6]byte, serverIP [4]byte, serverTID uint16) *ServerLoop {
	return &ServerLoop{store: store, ServerMAC: serverMAC, ServerIP: serverIP, ServerTID: serverTID}
}

// SetSource installs the file source the transfer streams from (the resolved
// object). In the real server this is the B-DOS record walk for the resolved
// name; here the test supplies a ByteSource.
func (s *ServerLoop) SetSource(src Source) { s.xfer = nil; s.pendingSrc = src }

// OnRRQ processes a received RRQ frame and returns the reply frame (an OACK on a
// hit, an ERROR(1) on a miss / serial-subdir). It records the client endpoint
// (MAC/IP/TID) for the subsequent DATA frames.
func (s *ServerLoop) OnRRQ(rrqFrame []byte) []byte {
	u, ok := frame.ParseUDP(rrqFrame)
	if !ok || u.DstPort != 69 {
		return nil // not a TFTP-server-bound frame
	}
	req, err := ParseRequest(u.Payload)
	if err != nil || req.Opcode != OpRRQ {
		return nil
	}

	// Learn the client endpoint from the request frame.
	copy(s.clientMAC[:], rrqFrame[6:12]) // Ethernet source MAC
	copy(s.clientIP[:], u.SrcIP[:])
	s.clientTID = u.SrcPort

	action, size := Resolve(s.store, req.Filename)
	if action == ActionError404 {
		// ERROR(1) on every miss and keep serving (the headline robustness rule).
		s.xfer = nil
		return s.wrap(BuildError(ErrFileNotFound, "File not found"))
	}

	// A hit: negotiate the blksize (echo within bounds, else 512) and start the
	// streamed transfer. Reply with the OACK (blksize then tsize, the captured
	// order).
	blksize := uint64(512)
	if v, ok := req.Option("blksize"); ok {
		if n, perr := strconv.ParseUint(v, 10, 64); perr == nil {
			blksize = AcceptedBlksize(n)
		}
	}
	s.xfer = NewServerXfer(s.pendingSrc, int(blksize))
	oack := BuildOACK([]Option{
		{"blksize", strconv.FormatUint(blksize, 10)},
		{"tsize", strconv.FormatUint(size, 10)},
	})
	return s.wrap(oack)
}

// OnACK advances the transfer for a received ACK frame and returns the next
// DATA frame to send, or nil when the transfer has completed (the short final
// block was ACKed) or the ACK is unexpected. A stale/duplicate ACK retransmits
// the last DATA (RFC 1350 server recovery via NextData after a no-advance).
func (s *ServerLoop) OnACK(ackFrame []byte) []byte {
	if s.xfer == nil {
		return nil
	}
	u, ok := frame.ParseUDP(ackFrame)
	if !ok || u.SrcPort != s.clientTID || u.DstPort != s.ServerTID {
		return nil // not part of this transfer
	}
	block, err := ParseACK(u.Payload)
	if err != nil {
		return nil
	}
	if s.xfer.OnAck(block) {
		return nil // transfer complete
	}
	data := s.xfer.NextData()
	if data == nil {
		return nil
	}
	return s.wrap(data)
}

// FirstData returns the first DATA frame (block 1), sent after the client ACKs
// block 0 of the OACK. The Z80 loop sends this on the ACK-0 it receives.
func (s *ServerLoop) FirstData() []byte {
	if s.xfer == nil {
		return nil
	}
	data := s.xfer.NextData()
	if data == nil {
		return nil
	}
	return s.wrap(data)
}

// Done reports whether the current transfer has completed.
func (s *ServerLoop) Done() bool { return s.xfer != nil && s.xfer.Done() }

// wrap frames a TFTP payload as a UDP datagram back to the client TID, from the
// SAM's IP + the transfer TID.
func (s *ServerLoop) wrap(payload []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC:  s.clientMAC,
		SrcMAC:  s.ServerMAC,
		SrcIP:   s.ServerIP,
		DstIP:   s.clientIP,
		SrcPort: s.ServerTID,
		DstPort: s.clientTID,
		Payload: payload,
	})
}
