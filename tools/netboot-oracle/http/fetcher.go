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
