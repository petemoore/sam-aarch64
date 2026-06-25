// netboot_http_test.go — the i70 host-verification of the integrated HTTP fetch
// phase machine (src/netboot/netboot_http.asm). It runs the real composed module
// (the fetch dispatcher + http_get.asm + tcp_conn + build_tcp_segment +
// build_arp_request + the driver encdrv.asm) under the koron-go/z80 harness with
// the emulated Trinity attached, and asserts that the whole self-provision flow
// — ARP request, SYN, the handshake-completing ACK+GET, the response ACK cadence,
// the FIN-ACK, and the accumulated body — is byte-for-byte the Go http.Fetcher
// authority's. The capstone of the i70 wire path, host-verified end-to-end over
// the i80 emulation. Emulation verification, NOT hardware verification — a real
// fetch against a live HTTP server stays the final gate (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	nbhttp "github.com/petemoore/sam-aarch64/tools/netboot-oracle/http"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	netbootHTTPBinPath = "../../../build/netboot_http.bin"
	netbootHTTPMapPath = "../../../build/netboot_http.map"
	phDone             = 3 // PH_DONE in netboot_http.asm
)

func loadNetbootHTTP(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(netbootHTTPBinPath); err != nil {
		t.Fatalf("netboot_http binary not built (%s); run `make netboot-http`", netbootHTTPBinPath)
	}
	mac, err := z80h.Load(netbootHTTPBinPath, netbootHTTPMapPath)
	if err != nil {
		t.Fatalf("load netboot_http: %v", err)
	}
	return mac
}

// goFetcher builds the Go authority Fetcher matching the loaded binary's config
// (the connection identity from tcpConnCfg + the baked-in path/host).
func goFetcher(t *testing.T, mac *z80h.Machine) *nbhttp.Fetcher {
	t.Helper()
	c := tcpConnCfg
	return nbhttp.NewFetcher(nbhttp.FetchConfig{
		ClientMAC: c.clientMAC, ClientIP: c.clientIP, ClientPort: c.clientPort,
		ServerIP: c.serverIP, ServerPort: c.serverPort, ISS: c.iss,
		Path: readCStr(t, mac, "HTTP_PATH"), Host: readCStr(t, mac, "HTTP_HOST"),
	})
}

// zeroServerMAC clears CONN_SERVER_MAC so the ARP phase must learn it — the SYN's
// destination MAC is only correct if ARP-learning actually wrote it.
func zeroServerMAC(t *testing.T, mac *z80h.Machine) {
	t.Helper()
	a, err := mac.Sym("CONN_SERVER_MAC")
	if err != nil {
		t.Fatalf("%v", err)
	}
	mac.Write(a, make([]byte, 6))
}

func fetchPhase(t *testing.T, mac *z80h.Machine) int {
	t.Helper()
	a, err := mac.Sym("FETCH_PHASE")
	if err != nil {
		t.Fatalf("%v", err)
	}
	return int(mac.Read(a, 1)[0])
}

// fetchFirst runs http_fetch_first and returns the ARP frame it transmitted.
func fetchFirst(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	res, err := mac.Call("http_fetch_first")
	if err != nil {
		t.Fatalf("call http_fetch_first: %v", err)
	}
	tx := enc.TXFrames()
	if len(tx) != before+1 {
		t.Fatalf("http_fetch_first transmitted %d frames, want 1", len(tx)-before)
	}
	out := tx[len(tx)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("http_fetch_first BC=%d but wire frame is %d bytes", res.BC, len(out))
	}
	return out
}

// fetchOnframe injects rx and runs one http_fetch_onframe, returning the frame
// transmitted (or nil) and the resulting done flag (FETCH_PHASE == PH_DONE).
func fetchOnframe(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, rx []byte) (tx []byte, done bool) {
	t.Helper()
	before := len(enc.TXFrames())
	enc.InjectRX(rx)
	res, err := mac.Call("http_fetch_onframe")
	if err != nil {
		t.Fatalf("call http_fetch_onframe: %v", err)
	}
	frames := enc.TXFrames()
	done = fetchPhase(t, mac) == phDone
	if res.BC == 0 {
		if len(frames) != before {
			t.Fatalf("http_fetch_onframe BC=0 but transmitted a frame")
		}
		return nil, done
	}
	if len(frames) != before+1 {
		t.Fatalf("http_fetch_onframe transmitted %d frames, want 1", len(frames)-before)
	}
	out := frames[len(frames)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("http_fetch_onframe BC=%d but wire frame is %d bytes", res.BC, len(out))
	}
	return out, done
}

// TestNetbootHTTPFullFetch drives the complete self-provision flow and asserts
// every wire frame + the done flag + the accumulated body match the Go Fetcher.
func TestNetbootHTTPFullFetch(t *testing.T) {
	mac := loadNetbootHTTP(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)
	zeroServerMAC(t, mac) // force the ARP phase to learn the server MAC

	ref := goFetcher(t, mac)
	c := tcpConnCfg

	// First: the broadcast ARP request.
	if got, want := fetchFirst(t, mac, enc), ref.First(); !bytes.Equal(got, want) {
		t.Fatalf("ARP request != Go\n  z80 %x\n  go  %x", got, want)
	}

	step := func(name string, rx []byte) {
		t.Helper()
		got, gotDone := fetchOnframe(t, mac, enc, rx)
		want, wantDone := ref.OnFrame(rx)
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: frame != Go\n  z80 %x\n  go  %x", name, got, want)
		}
		if gotDone != wantDone {
			t.Fatalf("%s: done = %v, want %v", name, gotDone, wantDone)
		}
	}

	// ARP reply -> SYN (and the server MAC is learned).
	step("arp-reply", frame.BuildARPReply(c.serverMAC, c.clientMAC, c.serverIP, c.clientIP))
	macAddr, _ := mac.Sym("CONN_SERVER_MAC")
	if got := mac.Read(macAddr, 6); !bytes.Equal(got, c.serverMAC[:]) {
		t.Errorf("learned server MAC %x, want %x", got, c.serverMAC)
	}

	// SYN-ACK -> the handshake-completing ACK+GET segment.
	step("syn-ack", serverSeg(c.serverISS, c.iss+1, tcp.FlagSYN|tcp.FlagACK, nil))

	// The server bare-ACKs the GET first: ignored.
	getLen := uint32(len(nbhttp.BuildRequest(readCStr(t, mac, "HTTP_PATH"), readCStr(t, mac, "HTTP_HOST"))))
	srvAck := c.iss + 1 + getLen
	step("bare-ack", serverSeg(c.serverISS+1, srvAck, tcp.FlagACK, nil))

	// Response in two segments, each ACKed.
	head := []byte("HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\n")
	body := []byte("hello")
	srvSeq := c.serverISS + 1
	step("resp-head", serverSeg(srvSeq, srvAck, tcp.FlagPSH|tcp.FlagACK, head))
	srvSeq += uint32(len(head))
	step("resp-body", serverSeg(srvSeq, srvAck, tcp.FlagPSH|tcp.FlagACK, body))
	srvSeq += uint32(len(body))

	// Server FIN -> our FIN-ACK, done.
	step("fin", serverSeg(srvSeq, srvAck, tcp.FlagFIN|tcp.FlagACK, nil))
	if fetchPhase(t, mac) != phDone {
		t.Fatalf("phase = %d after FIN, want PH_DONE", fetchPhase(t, mac))
	}

	// The accumulated body in CONN_DATA == the whole response.
	wantData := append(append([]byte{}, head...), body...)
	if !bytes.Equal(ref.Bytes(), wantData) {
		t.Fatalf("Go Fetcher body %q != expected %q (test bug)", ref.Bytes(), wantData)
	}
	dataAddr, _ := mac.Sym("CONN_DATA")
	if got := mac.Read(dataAddr, len(wantData)); !bytes.Equal(got, wantData) {
		t.Errorf("Z80 accumulated body != expected\n  z80 %q\n  exp %q", got, wantData)
	}

	// And the parse matches (run http_parse_response over CONN_DATA).
	wantResp, ok := ref.Response()
	if !ok {
		t.Fatal("Go Fetcher Response ok=false (test bug)")
	}
	got := parseDirect(t, mac, ref.Bytes())
	assertRespMatch(t, "fetch", got, wantResp, true)
}

// TestNetbootHTTPIgnoresStrayARP: an ARP reply for a different IP does not
// advance the fetch (no SYN sent, phase stays PH_ARP).
func TestNetbootHTTPIgnoresStrayARP(t *testing.T) {
	mac := loadNetbootHTTP(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)
	zeroServerMAC(t, mac)

	c := tcpConnCfg
	fetchFirst(t, mac, enc)
	stray := frame.BuildARPReply(c.serverMAC, c.clientMAC, frame.IPv4{10, 0, 0, 9}, c.clientIP)
	if tx, done := fetchOnframe(t, mac, enc, stray); tx != nil || done {
		t.Errorf("stray ARP reply advanced the fetch: tx=%x done=%v", tx, done)
	}
	if fetchPhase(t, mac) != 0 { // PH_ARP
		t.Errorf("phase advanced on a stray ARP: %d", fetchPhase(t, mac))
	}
}
