package tftp

// ClientXfer models the i82 client's receive side of a TFTP transfer: the
// lock-step DATA/ACK loop with the Sorcerer's-Apprentice-Syndrome fix and the
// short-final-block termination. It is the Go reference for the Z80 client
// transfer loop (plan §2 steps 3-5); a host test drives it with the captured
// DATA cadence + simulated timeouts to pin the behaviour the Z80 must match.
//
// SAS fix (research note §1.7): on a DATA timeout the client retransmits the
// LAST ACK only — never the RRQ — so a duplicated DATA cannot cascade.
type ClientXfer struct {
	blksize  int    // negotiated block size (from the OACK, or 512 default)
	serverID uint16 // the server's TID, learned from the first DATA/OACK source port
	acked    uint16 // the highest block number ACKed so far
	done     bool   // a short (< blksize) block has ended the transfer
	data     []byte // accumulated file bytes

	// RFC 7440 windowed receive (i120). windowsize == 1 is the lock-step path
	// (ACK every block); > 1 ACKs only the last block of each window, and on a gap
	// re-ACKs the last in-sequence block to make the sender rewind. winCount is
	// the number of in-sequence blocks taken since the last ACK.
	windowsize int
	winCount   int
}

// NewClientXfer starts a transfer with a negotiated block size. serverTID is
// the source port of the first server response (OACK or DATA); subsequent DATA
// from a different TID is rejected (RFC 1350 §4, the "unknown TID" rule).
func NewClientXfer(blksize int, serverTID uint16) *ClientXfer {
	if blksize <= 0 {
		blksize = 512
	}
	return &ClientXfer{blksize: blksize, serverID: serverTID, windowsize: 1}
}

// SetWindowsize sets the RFC 7440 window the receiver expects — the number of
// in-sequence blocks it accumulates before ACKing (it ACKs only the last block
// of each window). 1 keeps the lock-step (ACK-every-block) behaviour. Set it
// from the OACK before the first DATA.
func (c *ClientXfer) SetWindowsize(w int) {
	if w < 1 {
		w = 1
	}
	c.windowsize = w
}

// Action is what the client emits in response to an event.
type Action struct {
	// Reply is the TFTP packet to send (an ACK or ERROR), or nil for none.
	Reply []byte
	// Done reports the transfer has completed (a short final block arrived).
	Done bool
}

// OnData processes a received DATA packet from source port srcTID. It returns
// the ACK to send (and Done on the final short block), or an ERROR(5) reply if
// the source TID is wrong, or no reply for an out-of-window duplicate.
//
// Mirrors plan §2 step 3-4: validate the TID, read the block number, append the
// payload, ACK, and detect the short-final-block end.
func (c *ClientXfer) OnData(srcTID uint16, payload []byte) Action {
	if srcTID != c.serverID {
		// Stray packet from an unexpected port: ERROR(5), discard (don't ACK).
		return Action{Reply: BuildError(ErrUnknownTID, "unknown transfer ID")}
	}
	block, data, err := ParseDATA(payload)
	if err != nil {
		return Action{}
	}
	if c.windowsize > 1 {
		return c.onDataWindowed(block, data)
	}
	switch {
	case block == c.acked+1:
		// The next expected block.
		c.data = append(c.data, data...)
		c.acked = block
		if len(data) < c.blksize {
			c.done = true
		}
		return Action{Reply: BuildACK(block), Done: c.done}
	case block <= c.acked:
		// A duplicate (the server retransmitted): re-ACK it, don't re-store.
		return Action{Reply: BuildACK(block)}
	default:
		// A future block we haven't reached: ignore (no gap-filling here).
		return Action{}
	}
}

// onDataWindowed is the RFC 7440 receive path (windowsize > 1). An in-sequence
// block is accumulated; the client ACKs only when the window fills or the short
// final block arrives. Any other block — a gap (a block was lost, so this one is
// ahead of acked+1) or a duplicate from a rewind — makes it re-ACK its last
// in-sequence block (RFC 7440 §4), which tells the sender to rewind to acked+1,
// and resets the window counter.
func (c *ClientXfer) onDataWindowed(block uint16, data []byte) Action {
	if block != c.acked+1 {
		c.winCount = 0
		return Action{Reply: BuildACK(c.acked)}
	}
	c.data = append(c.data, data...)
	c.acked = block
	c.winCount++
	if len(data) < c.blksize {
		c.done = true
	}
	if c.done || c.winCount >= c.windowsize {
		c.winCount = 0
		return Action{Reply: BuildACK(c.acked), Done: c.done}
	}
	return Action{} // mid-window: accumulate silently, ACK at the window's end
}

// OnTimeout applies the Sorcerer's-Apprentice fix: retransmit the last ACK
// only. Before any block is ACKed there is nothing to retransmit (the caller
// re-sends the RRQ at that stage, not here), so it returns nil.
func (c *ClientXfer) OnTimeout() []byte {
	if c.acked == 0 {
		return nil
	}
	return BuildACK(c.acked)
}

// Done reports whether the transfer has completed.
func (c *ClientXfer) Done() bool { return c.done }

// Bytes returns the accumulated file bytes.
func (c *ClientXfer) Bytes() []byte { return c.data }
