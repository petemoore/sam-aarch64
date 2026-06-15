package http

import (
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
)

// Fetcher is the Go authority for the i70 integrated HTTP fetch — the phase
// machine the Z80 src/netboot/netboot_http.asm will port. It originates the
// whole flow a SAM firmware self-provision needs and is the HTTP analogue of the
// TFTP client.Client driver: ARP-for-server → TCP active-open handshake →
// HTTP/1.0 GET → accumulate the streamed response → end on the server's FIN
// (HTTP/1.0 closes the connection after the body). The accumulated bytes are
// what the B-DOS write-out (i93) lands on Trinity storage.
//
// It composes the already-host-verified pieces, adding no new packet arithmetic:
//   - frame.BuildARPRequest / frame.ParseARPReply — learn the server MAC,
//   - tcp.Conn — the active-open handshake, the data ACK cadence, the FIN teardown,
//   - http.BuildRequest / http.ParseResponse — the GET and the response parse.
//
// It is rx-driven exactly like client.Client (the Z80 loop calls drv_read, then
// OnFrame, then drv_write the returned frame):
//
//	First()      -> the broadcast ARP request (the first frame on the wire)
//	OnFrame(rx)  -> the next frame to send (or nil), plus done:
//	  PhaseARP       : an ARP reply for ServerIP -> the SYN (learn MAC, active-open)
//	  PhaseHandshake : the SYN-ACK               -> one ACK+GET segment
//	  PhaseRecv      : a response DATA segment    -> the data ACK (accumulate);
//	                   the server FIN             -> the FIN-ACK, done=true
//	Bytes()      -> the accumulated response (status line + headers + body)
//	Response()   -> the parsed status + body offset (valid after Done)
//
// The one subtlety vs the TFTP client: TCP needs a handshake before the request,
// and the handshake-completing ACK carries the GET payload as a single segment
// (RFC 793 permits data on that ACK) — so the flow stays one-tx-per-rx and the
// server never sees a separate bare ACK. This is the byte-for-byte porting spec
// for netboot_http.asm.
//
// Verification: host-verifiable end-to-end over the i80 emulation (the wire side
// — the ARP request, the SYN, the ACK+GET, the response ACK cadence, the
// accumulated bytes — asserted byte-for-byte against this authority). NOT
// host-verifiable: the B-DOS RST-8 HSAVE write-out, the real ENC28J60 silicon,
// and a real fetch against a live HTTP server — gated on real Trinity
// (CLAUDE.md §5). Emulation-verified is not hardware-verified.
type Fetcher struct {
	cfg   FetchConfig
	conn  *tcp.Conn
	phase Phase
}

// Phase is the fetch driver's current step.
type Phase int

const (
	// PhaseARP: waiting for the ARP reply that yields the server MAC.
	PhaseARP Phase = iota
	// PhaseHandshake: the SYN is sent; waiting for the SYN-ACK.
	PhaseHandshake
	// PhaseRecv: the GET is sent; receiving the response and ACKing it.
	PhaseRecv
	// PhaseDone: the server has FINed — the body is complete.
	PhaseDone
)

// FetchConfig is the fetch's identity + target.
type FetchConfig struct {
	ClientMAC  frame.MAC
	ClientIP   frame.IPv4
	ClientPort uint16 // the client's ephemeral source port
	ServerIP   frame.IPv4
	ServerPort uint16 // default 80
	ISS        uint32 // the client's initial send sequence number
	Path       string // the request path
	Host       string // the Host header value
}

// NewFetcher builds a fetch driver for cfg. The server MAC is learned by ARP, so
// the connection starts with a zero server MAC and the ARP phase fills it before
// the handshake.
func NewFetcher(cfg FetchConfig) *Fetcher {
	if cfg.ServerPort == 0 {
		cfg.ServerPort = 80
	}
	conn := tcp.NewConn(cfg.ClientMAC, cfg.ClientIP, cfg.ClientPort,
		frame.MAC{}, cfg.ServerIP, cfg.ServerPort, cfg.ISS)
	return &Fetcher{cfg: cfg, conn: conn, phase: PhaseARP}
}

// StreamTo opts this fetch into streaming mode (i99): the response body is
// flushed to dst in bounded windows of the given size (0 selects the connection
// default) as it arrives, instead of accumulating whole in the connection's
// Data. The HTTP response header is skipped — the first byte handed to dst is
// the first body byte (see bodySink for the header-skip mechanics). Call this
// once, before driving the fetch.
//
// The header skip lives here, at the HTTP layer, not in tcp.Conn: the connection
// is a pure byte transport with no notion of an HTTP header, so it streams the
// header bytes too. This Fetcher knows the stream is an HTTP/1.0 response, so it
// interposes a bodySink that drops everything up to the \r\n\r\n terminator
// before forwarding to dst. Because a flush window comfortably exceeds any
// HTTP/1.0 header (defaultFlushWindow >> a status line + a few headers), the
// terminator always lands within the first window the connection flushes — the
// header never straddles two windows, so bodySink only has to scan its first
// received chunk.
func (f *Fetcher) StreamTo(dst tcp.Sink, window int) {
	f.conn.SetSink(NewBodySink(dst), window)
}

// NewBodySink wraps dst with the HTTP-header skip: it drops everything up to and
// including the first "\r\n\r\n", then forwards body bytes to dst (and forwards
// every subsequent chunk whole). StreamTo uses it internally; it is exported as
// the authority the Z80 src/netboot/body_sink.asm is host-verified against
// (z80/body_sink_test.go feeds this and the Z80 port the same chunk sequence and
// compares the forwarded body bytes + per-Write boundaries).
func NewBodySink(dst tcp.Sink) tcp.Sink {
	return &bodySink{dst: dst}
}

// First returns the broadcast ARP request frame — the first thing on the wire
// (the client must learn the server MAC before opening the connection).
func (f *Fetcher) First() []byte {
	return frame.BuildARPRequest(f.cfg.ClientMAC, f.cfg.ClientIP, f.cfg.ServerIP)
}

// OnFrame processes one received frame and returns the next frame to send (or
// nil) plus whether the fetch has completed.
func (f *Fetcher) OnFrame(rx []byte) (tx []byte, done bool) {
	switch f.phase {
	case PhaseARP:
		mac, ip, ok := frame.ParseARPReply(rx)
		if !ok || ip != f.cfg.ServerIP {
			return nil, false // not the server's ARP reply: keep waiting
		}
		f.conn.ServerMAC = mac
		f.phase = PhaseHandshake
		return f.conn.Connect(), false // the SYN
	case PhaseHandshake:
		// The SYN-ACK acking our SYN. OnSegment validates it, sets the
		// connection ESTABLISHED + rcvNxt, and returns the bare handshake ACK —
		// which we discard: we send the GET instead, a single segment whose ACK
		// field completes the handshake and whose payload is the request.
		if f.conn.OnSegment(rx) == nil {
			return nil, false // not the SYN-ACK we expect: keep waiting
		}
		f.phase = PhaseRecv
		return f.conn.Send(BuildRequest(f.cfg.Path, f.cfg.Host)), false
	case PhaseRecv:
		txf := f.conn.OnSegment(rx)
		// The HTTP/1.0 server FINs after the response; that moves the connection
		// to FIN-WAIT and is the end-of-body signal.
		if f.conn.State == tcp.StateFinWait || f.conn.State == tcp.StateClosed {
			f.phase = PhaseDone
			return txf, true
		}
		return txf, false
	default:
		return nil, true
	}
}

// Phase returns the driver's current phase.
func (f *Fetcher) Phase() Phase { return f.phase }

// Done reports whether the fetch has completed (the server FINed).
func (f *Fetcher) Done() bool { return f.phase == PhaseDone }

// Bytes returns the accumulated response (status line + headers + body). On the
// SAM the body slice (Bytes()[BodyOff:]) is what the B-DOS HSAVE writes to
// Trinity storage.
func (f *Fetcher) Bytes() []byte { return f.conn.Data }

// Response parses the accumulated response (valid after Done).
func (f *Fetcher) Response() (Response, bool) { return ParseResponse(f.conn.Data) }

// ServerMAC returns the learned server MAC (valid after the ARP phase).
func (f *Fetcher) ServerMAC() frame.MAC { return f.conn.ServerMAC }

// bodySink adapts a body-only tcp.Sink to the header+body byte stream the
// connection flushes. It drops the HTTP response header (everything up to and
// including the \r\n\r\n terminator) before forwarding the first body bytes to
// dst; once the header is past, every subsequent flush is forwarded whole.
//
// The header-skip assumption (documented at StreamTo): the terminator falls
// within the first flush window, so it is found in the first Write — there is no
// straddle case to buffer across windows. If a malformed response somehow had no
// terminator in its first chunk (header larger than the window), bodySink
// forwards nothing for that chunk and keeps scanning subsequent ones, so the
// failure is a missing/short body, never a panic or a header leak.
type bodySink struct {
	dst        tcp.Sink
	headerDone bool // the \r\n\r\n terminator has been seen and skipped
}

// Write skips the header on the first relevant chunk, then forwards body bytes.
func (b *bodySink) Write(chunk []byte) {
	if b.headerDone {
		if len(chunk) > 0 {
			b.dst.Write(chunk)
		}
		return
	}
	r, ok := ParseResponse(chunk)
	if !ok || !r.Complete {
		// No header terminator in this chunk: nothing of the body has arrived
		// here yet (the header-fits-in-first-window assumption holds for any
		// real HTTP/1.0 response). Drop it and keep scanning the next chunk.
		return
	}
	b.headerDone = true
	if body := chunk[r.BodyOff:]; len(body) > 0 {
		b.dst.Write(body)
	}
}
