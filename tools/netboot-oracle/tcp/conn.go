package tcp

import (
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
)

// Conn is the Go authority for the i70 TCP connection state machine — the
// client-side connection management the Z80 src/netboot/tcp_conn.asm ports. It
// drives the active-open handshake (SYN -> SYN-ACK -> ACK), tracks the send and
// receive sequence numbers across the connection, accepts and ACKs inbound
// server data, and handles the FIN teardown the server initiates after the
// response. Inbound body bytes either accumulate whole in Data (the default) or,
// with a Sink installed via SetSink, stream out in bounded flush windows (i99) —
// the wire behavior is identical either way.
//
// This brick is connection management only: it originates the SYN and emits the
// right control segments (ACK / FIN-ACK), but it does not send application
// payload of its own. The HTTP/1.0 GET request body is the next brick; it will
// call Send (added then) to push the request and let this state machine ACK the
// streamed response.
//
// It is reply-driven the same way the DHCP/TFTP loops are: the caller's read
// loop drives it one received segment at a time (the Z80 loop calls drv_read,
// then OnSegment, then drv_write). Connect() is the one caller-initiated step
// (no inbound segment triggers the SYN). A segment that is not for us, carries a
// stale ack, or arrives out of order yields nil — ignored, keep waiting.
//
// Reply framing uses BuildSegment (the host-verified primitive): src = the
// client (its IP + ephemeral port), dst = the server (its IP + port). Inbound
// parse uses ParseSegment. RFC 793 governs the seq/ack arithmetic: SYN and FIN
// each consume one sequence number; data consumes len(payload).
type Conn struct {
	ClientMAC  frame.MAC
	ClientIP   frame.IPv4
	ClientPort uint16
	ServerMAC  frame.MAC
	ServerIP   frame.IPv4
	ServerPort uint16

	State  ConnState
	sndNxt uint32 // our next sequence number to send
	rcvNxt uint32 // next sequence number we expect from the server
	iss    uint32 // our initial send sequence number

	Data []byte // accumulated inbound payload (the response body, later)

	// Streaming sink (i99): opt-in bounded flush of inbound body bytes instead
	// of accumulating them whole in Data. nil sink = the legacy behavior (append
	// every payload to Data). When set, payload is buffered into flushBuf and a
	// full flushWindow is handed to sink.Write whenever the buffer fills; the
	// final partial buffer flushes at the FIN. The wire behavior (ACK cadence,
	// seq/ack arithmetic, every emitted frame) is byte-identical in both modes —
	// only where the bytes land differs. See SetSink and tcp/sink.go.
	sink        Sink
	flushWindow int    // flush chunk size; >0 only when sink is set
	flushBuf    []byte // bytes accumulated toward the next flush (len < flushWindow between flushes)
}

// ConnState is the TCP connection state (a minimal active-open client subset of
// the RFC 793 state machine — no LISTEN/SYN-RECEIVED, no simultaneous open).
type ConnState int

const (
	StateClosed      ConnState = iota // no connection / torn down
	StateSynSent                      // SYN sent, awaiting SYN-ACK
	StateEstablished                  // handshake complete, data may flow
	StateFinWait                      // our FIN sent, awaiting its ACK
)

// NewConn builds a client connection with a fixed identity and the chosen
// initial send sequence number (ISS). The window advertised in every segment is
// the receive buffer the chip can hold; a fixed value is fine for the firmware
// fetch (a single in-flight transfer). On hardware iss would be randomised; the
// test pins it so the bytes are reproducible.
func NewConn(clientMAC frame.MAC, clientIP frame.IPv4, clientPort uint16,
	serverMAC frame.MAC, serverIP frame.IPv4, serverPort uint16, iss uint32) *Conn {
	return &Conn{
		ClientMAC: clientMAC, ClientIP: clientIP, ClientPort: clientPort,
		ServerMAC: serverMAC, ServerIP: serverIP, ServerPort: serverPort,
		State: StateClosed, iss: iss,
	}
}

// connWindow is the fixed receive window advertised in every segment. The
// ENC28J60 has an 8 KB RX buffer; 5840 mirrors a common Linux default and is
// well within it.
const connWindow = 5840

// defaultFlushWindow is the streaming chunk size used when SetSink is given a
// window of 0. It is the RAM bound the streaming receive holds at once — large
// enough to comfortably exceed any HTTP/1.0 response header (so the header never
// straddles two windows; see http.bodySink) yet small relative to a multi-MB
// firmware blob, which is the whole point of streaming.
const defaultFlushWindow = 1024

// SetSink opts this connection into streaming mode (i99): inbound body bytes are
// routed to s in bounded flushes of window bytes instead of accumulating whole
// in Data. A window of 0 selects defaultFlushWindow. Passing a nil sink reverts
// to the legacy accumulate-to-Data behavior.
//
// Set the sink before any data segment arrives (the streaming spec is "flush as
// it arrives", with no retroactive draining of already-accumulated Data).
func (c *Conn) SetSink(s Sink, window int) {
	c.sink = s
	if s == nil {
		c.flushWindow = 0
		c.flushBuf = nil
		return
	}
	if window <= 0 {
		window = defaultFlushWindow
	}
	c.flushWindow = window
	c.flushBuf = make([]byte, 0, window)
}

// Connect originates the active open: it emits the SYN segment (seq=ISS, ack=0),
// advances sndNxt past the SYN's one consumed sequence number, and moves to
// SYN-SENT. The returned frame is ready for drv_write.
func (c *Conn) Connect() []byte {
	c.sndNxt = c.iss
	seg := c.build(FlagSYN, nil)
	c.sndNxt = c.iss + 1 // SYN consumes one sequence number (RFC 793 §3.3)
	c.State = StateSynSent
	return seg
}

// OnSegment processes one received Ethernet frame and returns the reply segment
// to send, or nil if there is nothing to send (ignored / state-only advance).
func (c *Conn) OnSegment(f []byte) []byte {
	s, ok := ParseSegment(f)
	if !ok || s.DstPort != c.ClientPort || s.SrcPort != c.ServerPort {
		return nil // not for this connection
	}

	switch c.State {
	case StateSynSent:
		// Expect SYN|ACK acking our SYN (ack == sndNxt == ISS+1).
		if s.Flags&FlagSYN == 0 || s.Flags&FlagACK == 0 {
			return nil
		}
		if s.Ack != c.sndNxt {
			return nil // acks something we did not send: ignore
		}
		c.rcvNxt = s.Seq + 1 // their SYN consumes one sequence number
		c.State = StateEstablished
		return c.build(FlagACK, nil)

	case StateEstablished:
		// A FIN from the server begins the teardown; ack it and send our FIN.
		if s.Flags&FlagFIN != 0 {
			if s.Seq != c.rcvNxt {
				return nil // out-of-order FIN: ignore (no reordering buffer)
			}
			// Account any payload riding on the FIN segment before the FIN bit.
			if len(s.Payload) > 0 {
				c.acceptPayload(s.Payload)
				c.rcvNxt += uint32(len(s.Payload))
			}
			c.flushFinal() // end-of-body: drain the partial remainder to the sink
			c.rcvNxt++     // FIN consumes one sequence number
			seg := c.build(FlagFIN|FlagACK, nil)
			c.sndNxt++ // our FIN consumes one
			c.State = StateFinWait
			return seg
		}
		// In-order data segment: accumulate and ACK.
		if len(s.Payload) == 0 {
			return nil // a bare ACK from the server: nothing to send back
		}
		if s.Seq != c.rcvNxt {
			return nil // out-of-order / duplicate: ignore (no gap filling)
		}
		c.acceptPayload(s.Payload)
		c.rcvNxt += uint32(len(s.Payload))
		return c.build(FlagACK, nil)

	case StateFinWait:
		// Final ACK of our FIN completes the close.
		if s.Flags&FlagACK != 0 && s.Ack == c.sndNxt {
			c.State = StateClosed
		}
		return nil
	}
	return nil
}

// Send pushes application payload as a data segment (PSH|ACK) over an
// established connection and advances sndNxt by the payload length (data
// consumes len, RFC 793 §3.3). Returns the frame for drv_write, or nil if the
// connection is not established. The i70 HTTP client uses this to send its GET
// request; this state machine then ACKs the streamed response.
func (c *Conn) Send(payload []byte) []byte {
	if c.State != StateEstablished {
		return nil
	}
	seg := c.build(FlagPSH|FlagACK, payload)
	c.sndNxt += uint32(len(payload))
	return seg
}

// Close originates the teardown from our side (the HTTP client uses this once it
// has the whole body): emit our FIN|ACK and move to FIN-WAIT. Returns nil unless
// the connection is established.
func (c *Conn) Close() []byte {
	if c.State != StateEstablished {
		return nil
	}
	seg := c.build(FlagFIN|FlagACK, nil)
	c.sndNxt++ // our FIN consumes one sequence number
	c.State = StateFinWait
	return seg
}

// acceptPayload records one in-order payload: it appends to Data in legacy mode
// (nil sink), or buffers into flushBuf and flushes full windows to the sink in
// streaming mode. In streaming mode Data is left untouched, so it never holds
// the whole body — the i99 RAM bound.
func (c *Conn) acceptPayload(payload []byte) {
	if c.sink == nil {
		c.Data = append(c.Data, payload...)
		return
	}
	c.flushBuf = append(c.flushBuf, payload...)
	for len(c.flushBuf) >= c.flushWindow {
		c.sink.Write(c.flushBuf[:c.flushWindow])
		// Drop the flushed window, keeping any overflow for the next flush. Copy
		// down rather than reslice so flushBuf's backing array stays bounded.
		n := copy(c.flushBuf, c.flushBuf[c.flushWindow:])
		c.flushBuf = c.flushBuf[:n]
	}
}

// flushFinal drains any partial remainder to the sink at end-of-body (the FIN).
// In legacy mode it is a no-op (Data already holds everything). A zero-length
// remainder emits no Write, so an empty body produces no spurious flush.
func (c *Conn) flushFinal() {
	if c.sink == nil || len(c.flushBuf) == 0 {
		return
	}
	c.sink.Write(c.flushBuf)
	c.flushBuf = c.flushBuf[:0]
}

// build frames a segment from this connection's current seq/ack with the given
// flags and payload. seq = sndNxt, ack = rcvNxt.
func (c *Conn) build(flags uint8, payload []byte) []byte {
	return BuildSegment(Segment{
		DstMAC:  c.ServerMAC,
		SrcMAC:  c.ClientMAC,
		SrcIP:   c.ClientIP,
		DstIP:   c.ServerIP,
		SrcPort: c.ClientPort,
		DstPort: c.ServerPort,
		Seq:     c.sndNxt,
		Ack:     c.rcvNxt,
		Flags:   flags,
		Window:  connWindow,
		Payload: payload,
	})
}
