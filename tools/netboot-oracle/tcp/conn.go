package tcp

import (
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
)

// Conn is the Go authority for the i70 TCP connection state machine — the
// client-side connection management the Z80 src/netboot/tcp_conn.asm ports. It
// drives the active-open handshake (SYN -> SYN-ACK -> ACK), tracks the send and
// receive sequence numbers across the connection, accepts and ACKs inbound
// server data (accumulating it for the HTTP response body), and handles the FIN
// teardown the server initiates after the response.
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
				c.Data = append(c.Data, s.Payload...)
				c.rcvNxt += uint32(len(s.Payload))
			}
			c.rcvNxt++ // FIN consumes one sequence number
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
		c.Data = append(c.Data, s.Payload...)
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
