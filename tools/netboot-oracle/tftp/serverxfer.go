package tftp

// ServerXfer models the i83 server's send side of a TFTP transfer: stream a
// file block-by-block at the negotiated blksize, number each DATA, wait for the
// matching ACK, retransmit the last DATA on an ACK timeout, and end with a
// short final block. It is the Go reference for the Z80 server DATA/ACK loop
// (plan §3 step 4-5; oracle §2). A host test drives it against the captured
// block cadence to pin the block-numbering + short-final-block termination.
//
// The file is streamed (not fully buffered) — start4.elf is multi-MB — so the
// model reads from a Source by offset, mirroring the B-DOS record walk the Z80
// server does over the flat store.
type ServerXfer struct {
	src     Source
	blksize int
	next    uint16 // the next block number to send (1-based)
	sent    []byte // the last DATA payload sent (for retransmit)
	done    bool   // the final short block has been ACKed

	// RFC 7440 windowed send (i120). windowsize == 1 is the lock-step path above;
	// > 1 uses NextWindow/OnWindowAck instead of NextData/OnAck. winAcked is the
	// highest block the receiver has cumulatively ACKed (the window's left edge);
	// winFinal is the short final block's number once built (0 = not yet reached).
	windowsize int
	winAcked   uint16
	winFinal   uint16
}

// Source yields a file's bytes by offset (the flat-store object). ReadAt fills
// p from offset off and returns the count; a short count signals end-of-file.
type Source interface {
	Size() int
	ReadAt(p []byte, off int) int
}

// ByteSource is an in-memory Source for tests.
type ByteSource []byte

// Size implements Source.
func (b ByteSource) Size() int { return len(b) }

// ReadAt implements Source.
func (b ByteSource) ReadAt(p []byte, off int) int {
	if off >= len(b) {
		return 0
	}
	return copy(p, b[off:])
}

// NewServerXfer starts a send of src at the negotiated block size, lock-step
// (windowsize 1) until SetWindowsize raises it.
func NewServerXfer(src Source, blksize int) *ServerXfer {
	if blksize <= 0 {
		blksize = 512
	}
	return &ServerXfer{src: src, blksize: blksize, next: 1, windowsize: 1}
}

// SetWindowsize sets the RFC 7440 window — the number of DATA blocks the sender
// transmits via NextWindow before it waits for an ACK. 1 keeps the lock-step
// behaviour. Call before the first NextWindow.
func (s *ServerXfer) SetWindowsize(w int) {
	if w < 1 {
		w = 1
	}
	s.windowsize = w
}

// NextWindow builds the DATA packets for the current window: blocks
// winAcked+1 .. winAcked+windowsize, stopping after the short final block (whose
// number it records). It is what the windowed sender transmits back-to-back
// before waiting for an ACK; the next NextWindow continues from wherever the
// latest OnWindowAck left winAcked (so a rewind simply re-sends from there). The
// lock-step NextData/OnAck pair is unaffected.
func (s *ServerXfer) NextWindow() [][]byte {
	if s.done {
		return nil
	}
	w := s.windowsize
	if w < 1 {
		w = 1
	}
	out := make([][]byte, 0, w)
	for i := 0; i < w; i++ {
		block := s.winAcked + uint16(i) + 1
		off := (int(block) - 1) * s.blksize
		buf := make([]byte, s.blksize)
		n := s.src.ReadAt(buf, off)
		out = append(out, BuildDATA(block, buf[:n]))
		if n < s.blksize {
			s.winFinal = block // the short final block ends the window early
			break
		}
	}
	return out
}

// OnWindowAck processes an ACK of `block` (RFC 7440 cumulative: the receiver's
// last in-sequence block). It advances or rewinds the window's left edge to that
// block — so the next NextWindow re-sends from block+1, which covers both the
// happy path (block == the last block sent) and the gap path (block < it, a loss
// was detected) — and reports completion once the short final block is ACKed. A
// stale ACK below the confirmed edge, or one beyond the last block sent, is
// ignored.
func (s *ServerXfer) OnWindowAck(block uint16) bool {
	if s.done {
		return true
	}
	if s.winFinal != 0 && block == s.winFinal {
		s.done = true
		return true
	}
	maxSent := s.winAcked + uint16(s.windowsize)
	if s.winFinal != 0 && s.winFinal < maxSent {
		maxSent = s.winFinal
	}
	if block < s.winAcked || block > maxSent {
		return false // stale or impossible ACK
	}
	s.winAcked = block
	return false
}

// NextData builds the next DATA packet to send, or nil when every block
// (including the short final one) has been ACKed. The block at offset
// (next-1)*blksize is read from the source; a block shorter than blksize is the
// final one (RFC 1350). A zero-length final block is emitted when the file is an
// exact multiple of blksize, per the protocol.
func (s *ServerXfer) NextData() []byte {
	if s.done {
		return nil
	}
	off := (int(s.next) - 1) * s.blksize
	buf := make([]byte, s.blksize)
	n := s.src.ReadAt(buf, off)
	s.sent = buf[:n]
	return BuildDATA(s.next, s.sent)
}

// OnAck processes an ACK for a block number. A matching ACK advances to the
// next block (and completes the transfer if the just-ACKed block was the short
// final one); a stale/duplicate ACK is ignored (the caller will retransmit on
// timeout). Returns true when the transfer has completed.
func (s *ServerXfer) OnAck(block uint16) bool {
	if block != s.next {
		return s.done // stale or duplicate ACK: no advance
	}
	if len(s.sent) < s.blksize {
		s.done = true // the short final block has now been ACKed
		return true
	}
	s.next++
	return false
}

// OnTimeout retransmits the last DATA sent (RFC 1350 server-side recovery).
func (s *ServerXfer) OnTimeout() []byte {
	if s.done || s.sent == nil {
		return nil
	}
	return BuildDATA(s.next, s.sent)
}

// Done reports whether the transfer has completed.
func (s *ServerXfer) Done() bool { return s.done }
