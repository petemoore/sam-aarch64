// Package http is the minimal HTTP/1.0 GET client for the netboot oracle — the
// i70 firmware self-provisioning layer that rides the TCP connection (the tcp
// package) to fetch a Raspberry Pi firmware blob from a plain-HTTP server and
// hand it to the B-DOS store.
//
// It is the Go authority/oracle for the Z80 src/netboot/http_get.asm: two pure,
// host-verifiable pieces plus a thin state-machine front that ties them to a
// tcp.Conn —
//
//   - BuildRequest: the request bytes "GET <path> HTTP/1.0\r\nHost: <host>\r\n\r\n".
//     HTTP/1.0 (not 1.1) so the server closes the connection after the response;
//     the FIN is the end-of-body signal — no chunked/keep-alive parsing needed.
//   - ParseResponse: the status code from the status line + the body offset (just
//     past the \r\n\r\n header terminator). The minimal parse the fetch needs:
//     confirm the status, then take the body the connection accumulated.
//
// The transport — sending the GET as a TCP data segment, ACKing the streamed
// response, the FIN teardown — is the tcp.Conn's job (Conn.Send / OnSegment);
// Client just wires the two together. Verification mirrors the TCP bricks:
// BuildRequest is pure bytes (byte-compared to the Z80 http_build_request),
// ParseResponse is pure parsing (its status/offset compared to the Z80
// http_parse_response), and the Client flow runs over the i80 emulation
// byte-for-byte (z80/http_get_test.go). NOT host-verifiable: a real HTTP fetch
// against a live server — gated on real Trinity (CLAUDE.md §5).
package http

import (
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
)

// BuildRequest builds an HTTP/1.0 GET request: the request line + a Host header +
// the terminating blank line.
func BuildRequest(path, host string) []byte {
	return []byte("GET " + path + " HTTP/1.0\r\nHost: " + host + "\r\n\r\n")
}

// Response is the parsed view of an HTTP/1.0 response. It is deliberately minimal
// — the firmware fetch needs the status code (confirm 200) and where the body
// starts (the bytes past the header terminator); the connection holds the body.
type Response struct {
	Status   int  // status code from the status line, e.g. 200
	BodyOff  int  // offset of the body within the raw response (past \r\n\r\n)
	Complete bool // whether the \r\n\r\n header terminator was found
}

// ParseResponse parses the status line's status code and locates the body. It
// returns ok=false only when there is no status line at all (no space to delimit
// the status code); a response whose header terminator has not yet arrived still
// parses (ok=true) with Complete=false, so a caller can re-parse as more data
// streams in. The Z80 http_parse_response mirrors this byte-for-byte (status +
// body offset + complete flag).
func ParseResponse(raw []byte) (Response, bool) {
	// Status code: the decimal token after the first space of the status line
	// ("HTTP/1.0 200 OK" -> 200).
	sp := indexByte(raw, ' ')
	if sp < 0 {
		return Response{}, false
	}
	status := 0
	for i := sp + 1; i < len(raw) && raw[i] >= '0' && raw[i] <= '9'; i++ {
		status = status*10 + int(raw[i]-'0')
	}
	r := Response{Status: status}

	// Header terminator: the first "\r\n\r\n". The body is the 4 bytes past it.
	if term := indexCRLFCRLF(raw); term >= 0 {
		r.BodyOff = term + 4
		r.Complete = true
	}
	return r, true
}

// indexByte returns the index of the first occurrence of b, or -1.
func indexByte(s []byte, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// indexCRLFCRLF returns the index of the first "\r\n\r\n" window, or -1. This is
// the exact 4-byte scan the Z80 port walks, so the two agree on the body offset.
func indexCRLFCRLF(s []byte) int {
	for i := 0; i+3 < len(s); i++ {
		if s[i] == '\r' && s[i+1] == '\n' && s[i+2] == '\r' && s[i+3] == '\n' {
			return i
		}
	}
	return -1
}

// Client is the HTTP/1.0 GET state machine riding an established tcp.Conn. Start
// sends the request; OnSegment delegates to the connection (which accumulates the
// response body and ACKs it); Response parses the accumulated bytes once the
// transfer is done.
type Client struct {
	Conn *tcp.Conn
	Path string
	Host string
}

// NewClient wraps an established connection with the request target.
func NewClient(conn *tcp.Conn, path, host string) *Client {
	return &Client{Conn: conn, Path: path, Host: host}
}

// Start sends the GET request over the established connection and returns the
// frame for drv_write.
func (cl *Client) Start() []byte {
	return cl.Conn.Send(BuildRequest(cl.Path, cl.Host))
}

// OnSegment feeds one received frame to the connection (accumulate + ACK / FIN
// teardown) and returns the reply frame, or nil.
func (cl *Client) OnSegment(f []byte) []byte {
	return cl.Conn.OnSegment(f)
}

// Response parses the response the connection has accumulated so far.
func (cl *Client) Response() (Response, bool) {
	return ParseResponse(cl.Conn.Data)
}
