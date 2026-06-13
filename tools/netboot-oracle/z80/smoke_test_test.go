// smoke_test_test.go — the i94 host-verification of the Trinity netboot bring-up
// smoke test. It runs the *real* composed routine (src/netboot/smoke_test.asm:
// the driver encdrv.asm + the host-verified build_arp_reply primitive) under the
// flat-memory koron-go/z80 harness with the emulated Trinity (enc28j60.go)
// attached, and asserts that an injected ARP request for the SAM's IP is answered
// with an ARP reply whose frame bytes on the virtual wire are byte-for-byte the
// Go authority's (smoke.Responder.OnFrame). It also confirms the loop ignores
// frames that are not an ARP request for the SAM's IP.
//
// This is the bring-up "the wire works" check made host-verifiable end-to-end by
// the i80 emulation: drv_read -> dispatch + build -> drv_write, the one
// observable network action the bootable smoke disk does on real Trinity. It is
// emulation verification, NOT hardware verification — the real ENC28J60 TX/RX
// timing, the EEPROM config read, and the actual round trip with Pete's Pi stay
// gated on real Trinity (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/smoke"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	smokeBinPath = "../../../build/netboot_smoke_test.bin"
	smokeMapPath = "../../../build/netboot_smoke_test.map"
)

func loadSmoke(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(smokeBinPath); err != nil {
		t.Skipf("smoke_test binary not built (%s); run `make netboot-smoke-test`", smokeBinPath)
	}
	mac, err := z80h.Load(smokeBinPath, smokeMapPath)
	if err != nil {
		t.Fatalf("load smoke_test: %v", err)
	}
	return mac
}

// fillSmokeConfig writes the SAM's identity (SMOKE_MAC/SMOKE_IP) and returns the
// matching Go authority responder so both produce identical reply bytes. The
// SAM plays the server role here (it answers for its own IP), so it reuses the
// masked server MAC/IP placeholders.
func fillSmokeConfig(t *testing.T, mac *z80h.Machine) *smoke.Responder {
	t.Helper()
	mac.Write(symAddr(t, mac, "SMOKE_MAC"), mask.ServerMAC[:])
	mac.Write(symAddr(t, mac, "SMOKE_IP"), mask.ServerIP[:])
	return smoke.NewResponder(mask.ServerMAC, mask.ServerIP)
}

// initSmokeDriver attaches the emulated Trinity and runs drv_init with the SAM's
// MAC (the driver programs it into the ENC).
func initSmokeDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	mac.AttachIO(enc)
	macAddr := symAddr(t, mac, "SMOKE_MAC")
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_init returned BC=%d, want 1", res.BC)
	}
}

// serveSmoke injects reqFrame on the wire and runs one smoke_serve_once,
// returning the frame the driver transmitted (or nil if it sent nothing).
func serveSmoke(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, reqFrame []byte) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	enc.InjectRX(reqFrame)
	res, err := mac.Call("smoke_serve_once")
	if err != nil {
		t.Fatalf("call smoke_serve_once: %v", err)
	}
	tx := enc.TXFrames()
	if res.BC == 0 {
		if len(tx) != before {
			t.Fatalf("smoke_serve_once returned BC=0 but transmitted a frame")
		}
		return nil
	}
	if len(tx) != before+1 {
		t.Fatalf("smoke_serve_once transmitted %d frames, want 1", len(tx)-before)
	}
	out := tx[len(tx)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("smoke_serve_once returned BC=%d but the wire frame is %d bytes", res.BC, len(out))
	}
	return out
}

// arpRequestForServer is the ARP request a machine on the LAN broadcasts asking
// "who has the SAM's IP?" — built with the masked client identity. It is the
// frame the smoke test must answer.
func arpRequestForServer() []byte {
	return frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ServerIP)
}

// TestSmokeAnswersARP is the headline i94 host check: an injected ARP request
// for the SAM's IP is answered with an ARP reply on the virtual wire,
// byte-for-byte the Go authority smoke.Responder.OnFrame output.
func TestSmokeAnswersARP(t *testing.T) {
	mac := loadSmoke(t)
	ref := fillSmokeConfig(t, mac)
	enc := z80h.NewENC28J60()
	initSmokeDriver(t, mac, enc)

	req := arpRequestForServer()
	got := serveSmoke(t, mac, enc, req)
	if got == nil {
		t.Fatal("smoke_serve_once ignored an ARP request for the SAM's IP")
	}
	want := ref.OnFrame(req)
	if want == nil {
		t.Fatal("Go responder ignored the ARP request (test bug)")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("ARP reply != Go authority\n  z80 %x\n  go  %x", got, want)
	}
}

// TestSmokeReplyDecodes confirms the emitted reply is a well-formed ARP reply
// that answers the asker: a parse of the SAM's reply yields the SAM's MAC for
// the SAM's IP, and unicasts back to the asking client.
func TestSmokeReplyDecodes(t *testing.T) {
	mac := loadSmoke(t)
	fillSmokeConfig(t, mac)
	enc := z80h.NewENC28J60()
	initSmokeDriver(t, mac, enc)

	got := serveSmoke(t, mac, enc, arpRequestForServer())
	if got == nil {
		t.Fatal("no reply emitted")
	}
	// Ethernet destination must be the asking client's MAC (unicast back).
	if !bytes.Equal(got[frame.OffDstMAC:frame.OffDstMAC+6], mask.ClientMAC[:]) {
		t.Errorf("reply Ethernet dst = %x, want the asker %x",
			got[frame.OffDstMAC:frame.OffDstMAC+6], mask.ClientMAC[:])
	}
	// Parsed as an ARP reply, the sender (the answer) is the SAM's MAC/IP.
	senderMAC, senderIP, ok := frame.ParseARPReply(got)
	if !ok {
		t.Fatal("emitted frame is not a well-formed ARP reply")
	}
	if senderMAC != mask.ServerMAC {
		t.Errorf("reply announces MAC %x, want the SAM's %x", senderMAC, mask.ServerMAC)
	}
	if senderIP != mask.ServerIP {
		t.Errorf("reply announces IP %v, want the SAM's %v", senderIP, mask.ServerIP)
	}
}

// TestSmokeIgnoresWrongIP confirms the loop stays silent for an ARP request that
// asks for a different IP (not the SAM's) — keep listening, never choke.
func TestSmokeIgnoresWrongIP(t *testing.T) {
	mac := loadSmoke(t)
	fillSmokeConfig(t, mac)
	enc := z80h.NewENC28J60()
	initSmokeDriver(t, mac, enc)

	// Ask for the client's own IP, not the SAM's — the SAM must not answer.
	req := frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ClientIP)
	if got := serveSmoke(t, mac, enc, req); got != nil {
		t.Errorf("smoke answered an ARP request for a different IP: %x", got)
	}
}

// TestSmokeIgnoresNonARP confirms the loop ignores a non-ARP frame (a DHCP
// DISCOVER) — it answers only ARP requests for its IP.
func TestSmokeIgnoresNonARP(t *testing.T) {
	mac := loadSmoke(t)
	fillSmokeConfig(t, mac)
	enc := z80h.NewENC28J60()
	initSmokeDriver(t, mac, enc)

	if got := serveSmoke(t, mac, enc, golden.DHCPDiscover); got != nil {
		t.Errorf("smoke answered a non-ARP frame: %x", got)
	}
}

// TestSmokeEmptyWire: with nothing injected, smoke_serve_once returns BC=0 and
// sends nothing.
func TestSmokeEmptyWire(t *testing.T) {
	mac := loadSmoke(t)
	fillSmokeConfig(t, mac)
	enc := z80h.NewENC28J60()
	initSmokeDriver(t, mac, enc)

	res, err := mac.Call("smoke_serve_once")
	if err != nil {
		t.Fatalf("call smoke_serve_once: %v", err)
	}
	if res.BC != 0 {
		t.Fatalf("empty wire: smoke_serve_once returned BC=%d, want 0", res.BC)
	}
	if len(enc.TXFrames()) != 0 {
		t.Fatalf("empty wire: transmitted %d frames, want 0", len(enc.TXFrames()))
	}
}
