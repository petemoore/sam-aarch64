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
// (MAC/IP/TID) for the subsequent DATA frames. This is the Pi-boot-ROM path: the
// ROM always sends options, so a hit is always answered with an OACK (the client
// then ACKs block 0 to start the data flow).
func (s *ServerLoop) OnRRQ(rrqFrame []byte) []byte {
	return s.StartTransfer(rrqFrame, true)
}

// StartTransfer is the shared RRQ handler for both the OACK path (sendOACK=true,
// the Pi boot ROM, which always negotiates options) and the plain-client path
// (sendOACK=false: a bare RRQ with no options, RFC 2347 — the server must NOT
// send an OACK and instead streams DATA block 1 straight away at the default
// 512-byte block size). It parses + resolves the request, learns the client
// endpoint, and on a hit either returns the OACK or the first DATA frame; on a
// miss it returns ERROR(1) and keeps serving. A nil return means the frame was
// not a TFTP RRQ this loop should answer.
func (s *ServerLoop) StartTransfer(rrqFrame []byte, sendOACK bool) []byte {
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

	if !sendOACK {
		// RFC 2347 bare-RRQ path: no options were requested, so the server omits
		// the OACK and begins the transfer at the 512-byte default, sending DATA
		// block 1 right away.
		s.xfer = NewServerXfer(s.pendingSrc, 512)
		return s.wrap(s.xfer.NextData())
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
	// RFC 7440: grant a windowsize only when the client asked for one, clamped to
	// what the server supports, and echo it in the OACK so both sides agree.
	windowsize := uint64(WindowsizeDefault)
	if v, ok := req.Option("windowsize"); ok {
		if n, perr := strconv.ParseUint(v, 10, 64); perr == nil {
			windowsize = AcceptedWindowsize(n)
		}
	}
	s.xfer = NewServerXfer(s.pendingSrc, int(blksize))
	s.xfer.SetWindowsize(int(windowsize))
	oackOpts := []Option{
		{"blksize", strconv.FormatUint(blksize, 10)},
		{"tsize", strconv.FormatUint(size, 10)},
	}
	if windowsize > WindowsizeDefault {
		oackOpts = append(oackOpts, Option{"windowsize", strconv.FormatUint(windowsize, 10)})
	}
	return s.wrap(BuildOACK(oackOpts))
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

// FirstWindow returns the first window of DATA frames (blocks 1..windowsize),
// sent after the client ACKs block 0 of an OACK that granted a windowsize. It is
// the windowed counterpart of FirstData; the Z80 loop writes each frame in turn.
func (s *ServerLoop) FirstWindow() [][]byte {
	if s.xfer == nil {
		return nil
	}
	return s.wrapWindow(s.xfer.NextWindow())
}

// OnACKWindow advances a windowed transfer for a received ACK frame and returns
// the next window of DATA frames to send, or nil when the transfer has completed
// or the ACK is not part of it. It is the windowed counterpart of OnACK; the ACK
// is cumulative, so a gap-triggered lower ACK rewinds and re-sends from there.
func (s *ServerLoop) OnACKWindow(ackFrame []byte) [][]byte {
	if s.xfer == nil {
		return nil
	}
	u, ok := frame.ParseUDP(ackFrame)
	if !ok || u.SrcPort != s.clientTID || u.DstPort != s.ServerTID {
		return nil
	}
	block, err := ParseACK(u.Payload)
	if err != nil {
		return nil
	}
	if s.xfer.OnWindowAck(block) {
		return nil // transfer complete
	}
	return s.wrapWindow(s.xfer.NextWindow())
}

// wrapWindow frames each DATA payload of a window as a UDP datagram to the client.
func (s *ServerLoop) wrapWindow(payloads [][]byte) [][]byte {
	if len(payloads) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(payloads))
	for _, p := range payloads {
		out = append(out, s.wrap(p))
	}
	return out
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
