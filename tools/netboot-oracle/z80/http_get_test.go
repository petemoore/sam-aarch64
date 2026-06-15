// http_get_test.go — the i70 host-verification of the HTTP/1.0 GET client. It
// runs the real composed module (src/netboot/http_get.asm: the connection
// tcp_conn.asm + build_tcp_segment + the driver encdrv.asm + the new HTTP layer)
// under the flat-memory koron-go/z80 harness with the emulated Trinity attached,
// and asserts that the GET request on the virtual wire and the parsed response
// match the Go authority (http.Client.Start / http.ParseResponse) byte-for-byte.
//
// This is the third i70 brick (after the TCP segment layer and the connection
// state machine) made host-verifiable end-to-end by i80. It reuses the helpers
// in tcp_conn_test.go (same package, same connection identity) for the handshake
// and the per-segment drive. Emulation verification, NOT hardware verification —
// a real HTTP fetch against a live server remains the final gate (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/http"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	httpGetBinPath = "../../../build/netboot_http_get.bin"
	httpGetMapPath = "../../../build/netboot_http_get.map"
)

func loadHTTPGet(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(httpGetBinPath); err != nil {
		t.Skipf("http_get binary not built (%s); run `make netboot-http-get`", httpGetBinPath)
	}
	mac, err := z80h.Load(httpGetBinPath, httpGetMapPath)
	if err != nil {
		t.Fatalf("load http_get: %v", err)
	}
	return mac
}

// readCStr reads a NUL-terminated string from memory at the named symbol — used
// to pull HTTP_PATH / HTTP_HOST out of the binary so the Go authority builds the
// request from the exact same strings (one source of truth).
func readCStr(t *testing.T, mac *z80h.Machine, name string) string {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	raw := mac.Read(a, 256)
	n := bytes.IndexByte(raw, 0)
	if n < 0 {
		t.Fatalf("%s not NUL-terminated", name)
	}
	return string(raw[:n])
}

// httpRef builds the Go authority Client matching the loaded binary's target.
func httpRef(t *testing.T, mac *z80h.Machine) *http.Client {
	t.Helper()
	return http.NewClient(goConn(), readCStr(t, mac, "HTTP_PATH"), readCStr(t, mac, "HTTP_HOST"))
}

// httpStart runs http_get_start and returns the GET frame it transmitted.
func httpStart(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	res, err := mac.Call("http_get_start")
	if err != nil {
		t.Fatalf("call http_get_start: %v", err)
	}
	tx := enc.TXFrames()
	if len(tx) != before+1 {
		t.Fatalf("http_get_start transmitted %d frames, want 1", len(tx)-before)
	}
	out := tx[len(tx)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("http_get_start returned BC=%d but the wire frame is %d bytes", res.BC, len(out))
	}
	return out
}

// z80Resp is the decoded http_parse_response output read back from memory.
type z80Resp struct {
	ok       bool
	status   int
	bodyOff  int
	complete bool
}

func readHTTPResp(t *testing.T, mac *z80h.Machine) z80Resp {
	t.Helper()
	rd := func(name string, n int) []byte {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("%v", err)
		}
		return mac.Read(a, n)
	}
	return z80Resp{
		ok:       rd("HTTP_OK", 1)[0] == 1,
		status:   int(binary.LittleEndian.Uint16(rd("HTTP_STATUS", 2))),
		bodyOff:  int(binary.LittleEndian.Uint16(rd("HTTP_BODY_OFF", 2))),
		complete: rd("HTTP_COMPLETE", 1)[0] == 1,
	}
}

// parseDirect writes raw into CONN_DATA + CONN_DATA_LEN and runs
// http_parse_response, returning the decoded output — a focused unit test of the
// parser independent of the wire flow.
func parseDirect(t *testing.T, mac *z80h.Machine, raw []byte) z80Resp {
	t.Helper()
	dataAddr, err := mac.Sym("CONN_DATA")
	if err != nil {
		t.Fatalf("%v", err)
	}
	lenAddr, err := mac.Sym("CONN_DATA_LEN")
	if err != nil {
		t.Fatalf("%v", err)
	}
	mac.Write(dataAddr, raw)
	mac.WriteU16LE(lenAddr, uint16(len(raw)))
	if _, err := mac.Call("http_parse_response"); err != nil {
		t.Fatalf("call http_parse_response: %v", err)
	}
	return readHTTPResp(t, mac)
}

// TestHTTPGetRequest: after the handshake, http_get_start emits the GET request
// segment byte-for-byte the Go authority's (http.Client.Start).
func TestHTTPGetRequest(t *testing.T) {
	mac := loadHTTPGet(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	ref := httpRef(t, mac)
	c := tcpConnCfg

	ref.Conn.Connect()
	connect(t, mac, enc)
	synAck := serverSeg(c.serverISS, c.iss+1, tcp.FlagSYN|tcp.FlagACK, nil)
	if got, want := recvOnce(t, mac, enc, synAck), ref.Conn.OnSegment(synAck); !bytes.Equal(got, want) {
		t.Fatalf("handshake ACK != Go\n  z80 %x\n  go  %x", got, want)
	}

	got := httpStart(t, mac, enc)
	want := ref.Start()
	if !bytes.Equal(got, want) {
		t.Errorf("GET request != Go authority\n  z80 %x\n  go  %x", got, want)
	}
	s, ok := tcp.ParseSegment(got)
	if !ok || s.Flags != tcp.FlagPSH|tcp.FlagACK {
		t.Errorf("GET segment flags = %#x ok=%v, want PSH|ACK", s.Flags, ok)
	}
	if !bytes.Equal(s.Payload, http.BuildRequest(ref.Path, ref.Host)) {
		t.Errorf("GET payload = %q, want the request bytes", s.Payload)
	}
}

// TestHTTPGetFullFetch drives handshake -> GET -> a two-segment response, each
// frame on the virtual wire byte-for-byte the Go authority's, then asserts
// http_parse_response and the accumulated body match.
func TestHTTPGetFullFetch(t *testing.T) {
	mac := loadHTTPGet(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	ref := httpRef(t, mac)
	c := tcpConnCfg

	// Handshake.
	ref.Conn.Connect()
	connect(t, mac, enc)
	synAck := serverSeg(c.serverISS, c.iss+1, tcp.FlagSYN|tcp.FlagACK, nil)
	if got, want := recvOnce(t, mac, enc, synAck), ref.Conn.OnSegment(synAck); !bytes.Equal(got, want) {
		t.Fatalf("handshake ACK != Go\n  z80 %x\n  go  %x", got, want)
	}

	// GET.
	if got, want := httpStart(t, mac, enc), ref.Start(); !bytes.Equal(got, want) {
		t.Fatalf("GET != Go\n  z80 %x\n  go  %x", got, want)
	}

	// Response in two segments (header, then body). The server's ack field is
	// the same in both and is not validated by the client in ESTABLISHED; we
	// build each segment once and feed it to both sides.
	head := []byte("HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\n")
	body := []byte("hello")
	srvAck := c.iss + 1 + uint32(len(http.BuildRequest(ref.Path, ref.Host)))
	srvSeq := c.serverISS + 1

	seg1 := serverSeg(srvSeq, srvAck, tcp.FlagPSH|tcp.FlagACK, head)
	if got, want := recvOnce(t, mac, enc, seg1), ref.Conn.OnSegment(seg1); !bytes.Equal(got, want) {
		t.Fatalf("response-head ACK != Go\n  z80 %x\n  go  %x", got, want)
	}
	srvSeq += uint32(len(head))
	seg2 := serverSeg(srvSeq, srvAck, tcp.FlagPSH|tcp.FlagACK, body)
	if got, want := recvOnce(t, mac, enc, seg2), ref.Conn.OnSegment(seg2); !bytes.Equal(got, want) {
		t.Fatalf("response-body ACK != Go\n  z80 %x\n  go  %x", got, want)
	}

	// The accumulated CONN_DATA must equal the whole response.
	wantData := append(append([]byte{}, head...), body...)
	if !bytes.Equal(ref.Conn.Data, wantData) {
		t.Fatalf("Go authority body %q != expected %q (test bug)", ref.Conn.Data, wantData)
	}
	dataAddr, err := mac.Sym("CONN_DATA")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := mac.Read(dataAddr, len(wantData)); !bytes.Equal(got, wantData) {
		t.Errorf("Z80 accumulated body != expected\n  z80 %q\n  exp %q", got, wantData)
	}

	// Parse: Z80 http_parse_response vs Go ParseResponse over the same bytes.
	wantResp, ok := ref.Response()
	if !ok {
		t.Fatal("Go ParseResponse ok=false (test bug)")
	}
	got := parseDirect(t, mac, ref.Conn.Data)
	assertRespMatch(t, "full fetch", got, wantResp, true)
	// And the body at the parsed offset is the payload.
	if string(ref.Conn.Data[got.bodyOff:]) != "hello" {
		t.Errorf("body at offset %d = %q, want %q", got.bodyOff, ref.Conn.Data[got.bodyOff:], "hello")
	}
}

// TestHTTPParseResponseVectors exercises http_parse_response directly against a
// range of inputs, each compared to the Go authority http.ParseResponse.
func TestHTTPParseResponseVectors(t *testing.T) {
	mac := loadHTTPGet(t)

	cases := []struct {
		name string
		raw  string
	}{
		{"200 with body", "HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\nhello"},
		{"404 no body", "HTTP/1.0 404 Not Found\r\n\r\n"},
		{"200 empty body", "HTTP/1.1 200 OK\r\n\r\n"},
		{"incomplete headers", "HTTP/1.0 200 OK\r\nContent-Length: 5\r\n"},
		{"no status line", "garbagewithnospace"},
		{"three-zero-three", "HTTP/1.0 303 See Other\r\nLocation: /x\r\n\r\nx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.raw)
			wantResp, wantOK := http.ParseResponse(raw)
			got := parseDirect(t, mac, raw)
			assertRespMatch(t, tc.name, got, wantResp, wantOK)
		})
	}
}

func assertRespMatch(t *testing.T, name string, got z80Resp, want http.Response, wantOK bool) {
	t.Helper()
	if got.ok != wantOK {
		t.Errorf("%s: ok = %v, want %v", name, got.ok, wantOK)
	}
	if !wantOK {
		return // status/offset undefined when there is no status line
	}
	if got.status != want.Status {
		t.Errorf("%s: status = %d, want %d", name, got.status, want.Status)
	}
	if got.complete != want.Complete {
		t.Errorf("%s: complete = %v, want %v", name, got.complete, want.Complete)
	}
	if want.Complete && got.bodyOff != want.BodyOff {
		t.Errorf("%s: bodyOff = %d, want %d", name, got.bodyOff, want.BodyOff)
	}
}

// TestHTTPBuildRequestDynamicPath: http_build_request copies the path from the
// settable HTTP_PATH_PTR (default = the baked HTTP_PATH), so the multi-file fetch
// loop (http_main's prov_start) can point each request at a per-file path. The
// built request matches the Go authority http.BuildRequest for both the default
// pointer and an overridden one.
func TestHTTPBuildRequestDynamicPath(t *testing.T) {
	buildReq := func(t *testing.T, mac *z80h.Machine) []byte {
		t.Helper()
		res, err := mac.Call("http_build_request")
		if err != nil {
			t.Fatalf("call http_build_request: %v", err)
		}
		addr, err := mac.Sym("CONN_TX_PAYLOAD")
		if err != nil {
			t.Fatalf("%v", err)
		}
		return mac.Read(addr, int(res.BC))
	}

	t.Run("default path", func(t *testing.T) {
		mac := loadHTTPGet(t)
		got := buildReq(t, mac)
		want := http.BuildRequest(readCStr(t, mac, "HTTP_PATH"), readCStr(t, mac, "HTTP_HOST"))
		if !bytes.Equal(got, want) {
			t.Errorf("default request != Go authority\n  z80 %q\n  go  %q", got, want)
		}
	})

	t.Run("overridden path", func(t *testing.T) {
		mac := loadHTTPGet(t)
		const altPath = "/raspberrypi/firmware/a43df3a0/boot/start4.elf"
		// CONN_DATA is a receive buffer, untouched by the request build — use it as
		// scratch for the alternate NUL-terminated path string.
		scratch, err := mac.Sym("CONN_DATA")
		if err != nil {
			t.Fatalf("%v", err)
		}
		mac.Write(scratch, append([]byte(altPath), 0))
		ptr, err := mac.Sym("HTTP_PATH_PTR")
		if err != nil {
			t.Fatalf("%v", err)
		}
		mac.WriteU16LE(ptr, scratch)

		got := buildReq(t, mac)
		host := readCStr(t, mac, "HTTP_HOST")
		if want := http.BuildRequest(altPath, host); !bytes.Equal(got, want) {
			t.Errorf("overridden request != Go authority\n  z80 %q\n  go  %q", got, want)
		}
		// It must differ from the default — proves the override took effect, not a
		// coincidental match.
		if def := http.BuildRequest(readCStr(t, mac, "HTTP_PATH"), host); bytes.Equal(got, def) {
			t.Errorf("overridden request equals the default — HTTP_PATH_PTR was not honoured")
		}
	})
}
