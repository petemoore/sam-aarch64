// dhcp_loop_test.go — the i86 host-verification of the DHCP responder loop.
// It runs the *real* composed state machine (src/netboot/dhcp_loop.asm: the
// driver encdrv.asm + the host-verified build_udp_frame/dhcp_reply primitives)
// under the flat-memory koron-go/z80 harness with the emulated Trinity
// (enc28j60.go) attached, and asserts that an injected DHCP DISCOVER/REQUEST is
// answered with an OFFER/ACK whose frame bytes on the virtual wire are
// byte-for-byte the Go authority's (dhcp.Responder.OnRequest).
//
// This is the wire-I/O state machine made host-verifiable end-to-end by i80:
// drv_read -> dispatch + build -> drv_write, the whole DHCP half of the netboot
// DORA cycle, proven against the byte-exact Go reference. It is emulation
// verification, NOT hardware verification — real Trinity remains the final gate
// (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/dhcp"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	dhcpLoopBinPath = "../../../build/netboot_dhcp_loop.bin"
	dhcpLoopMapPath = "../../../build/netboot_dhcp_loop.map"
)

// dhcpLoopConfig is the SAM's fixed DHCP identity, shared between the Z80 CONFIG
// block and the Go Responder so both produce identical OFFER/ACK bytes.
var dhcpLoopConfig = struct {
	serverMAC         [6]byte
	serverIP, subnet  [4]byte
	broadcast         [4]byte
	poolBase          [4]byte
	poolSize          int
	leaseSecs, t1, t2 uint32
}{
	serverMAC: mask.ServerMAC,
	serverIP:  mask.ServerIP,
	subnet:    [4]byte{255, 255, 255, 0},
	broadcast: mask.Broadcast,
	poolBase:  [4]byte{192, 0, 2, 100},
	poolSize:  8,
	leaseSecs: 7200, t1: 3600, t2: 6300,
}

func loadDHCPLoop(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(dhcpLoopBinPath); err != nil {
		t.Fatalf("dhcp_loop binary not built (%s); run `make netboot-dhcp-loop`", dhcpLoopBinPath)
	}
	mac, err := z80h.Load(dhcpLoopBinPath, dhcpLoopMapPath)
	if err != nil {
		t.Fatalf("load dhcp_loop: %v", err)
	}
	return mac
}

// fillDHCPConfig writes the CONFIG_* block in the loaded machine to match the
// Go responder config. Lease/T1/T2 are big-endian on the wire (the build copies
// them verbatim into the option), so they are written big-endian here.
func fillDHCPConfig(t *testing.T, mac *z80h.Machine) {
	t.Helper()
	put := func(name string, data []byte) {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("%v", err)
		}
		mac.Write(a, data)
	}
	be32 := func(v uint32) []byte {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		return b[:]
	}
	put("CONFIG_SERVERMAC", dhcpLoopConfig.serverMAC[:])
	put("CONFIG_SERVERIP", dhcpLoopConfig.serverIP[:])
	put("CONFIG_SUBNET", dhcpLoopConfig.subnet[:])
	put("CONFIG_BROADCAST", dhcpLoopConfig.broadcast[:])
	put("CONFIG_LEASE", be32(dhcpLoopConfig.leaseSecs))
	put("CONFIG_T1", be32(dhcpLoopConfig.t1))
	put("CONFIG_T2", be32(dhcpLoopConfig.t2))
	put("CONFIG_POOLBASE", dhcpLoopConfig.poolBase[:])
	put("CONFIG_POOLSIZE", []byte{byte(dhcpLoopConfig.poolSize)})
}

// goResponder builds the matching Go authority responder.
func goResponder() *dhcp.Responder {
	c := dhcpLoopConfig
	return dhcp.NewResponder(c.serverMAC, c.serverIP, c.subnet, c.broadcast,
		c.poolBase, c.poolSize, c.leaseSecs, c.t1, c.t2)
}

// serveOnce injects reqFrame on the wire and runs one dhcp_serve_once, returning
// the frame the driver transmitted (or nil if it sent nothing).
func serveOnce(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, reqFrame []byte) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	enc.InjectRX(reqFrame)
	res, err := mac.Call("dhcp_serve_once")
	if err != nil {
		t.Fatalf("call dhcp_serve_once: %v", err)
	}
	tx := enc.TXFrames()
	if res.BC == 0 {
		if len(tx) != before {
			t.Fatalf("dhcp_serve_once returned BC=0 but transmitted a frame")
		}
		return nil
	}
	if len(tx) != before+1 {
		t.Fatalf("dhcp_serve_once transmitted %d frames, want 1", len(tx)-before)
	}
	out := tx[len(tx)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("dhcp_serve_once returned BC=%d but the wire frame is %d bytes", res.BC, len(out))
	}
	return out
}

// initDHCPLoopDriver attaches the emulated Trinity and runs drv_init with the
// SAM's MAC (the driver programs it into the ENC).
func initDHCPLoopDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	mac.AttachIO(enc)
	macAddr, err := mac.Sym("CONFIG_SERVERMAC")
	if err != nil {
		t.Fatalf("%v", err)
	}
	mac.Write(macAddr, dhcpLoopConfig.serverMAC[:])
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_init returned BC=%d, want 1", res.BC)
	}
}

// TestDHCPResponderOffer is the headline i86 host check: an injected DISCOVER is
// answered with an OFFER on the virtual wire, byte-for-byte the Go Responder's
// OnRequest output.
func TestDHCPResponderOffer(t *testing.T) {
	mac := loadDHCPLoop(t)
	fillDHCPConfig(t, mac)
	enc := z80h.NewENC28J60()
	initDHCPLoopDriver(t, mac, enc)

	got := serveOnce(t, mac, enc, golden.DHCPDiscover)
	if got == nil {
		t.Fatal("dhcp_serve_once ignored a DISCOVER")
	}
	want := goResponder().OnRequest(golden.DHCPDiscover)
	if want == nil {
		t.Fatal("Go responder ignored a DISCOVER (test bug)")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("OFFER frame != Go authority\n  z80 %x\n  go  %x", got, want)
	}
}

// TestDHCPResponderDORA runs the full DISCOVER->OFFER, REQUEST->ACK cycle and
// asserts both frames match the Go authority byte-for-byte, with the lease
// stable across the two (the same client MAC gets the same yiaddr).
func TestDHCPResponderDORA(t *testing.T) {
	mac := loadDHCPLoop(t)
	fillDHCPConfig(t, mac)
	enc := z80h.NewENC28J60()
	initDHCPLoopDriver(t, mac, enc)

	ref := goResponder()

	offer := serveOnce(t, mac, enc, golden.DHCPDiscover)
	wantOffer := ref.OnRequest(golden.DHCPDiscover)
	if !bytes.Equal(offer, wantOffer) {
		t.Errorf("OFFER != Go authority\n  z80 %x\n  go  %x", offer, wantOffer)
	}

	ack := serveOnce(t, mac, enc, golden.DHCPRequest)
	wantAck := ref.OnRequest(golden.DHCPRequest)
	if !bytes.Equal(ack, wantAck) {
		t.Errorf("ACK != Go authority\n  z80 %x\n  go  %x", ack, wantAck)
	}

	// The OFFER and ACK must carry the same yiaddr (lease stable per MAC). The
	// yiaddr sits at frame offset 42 (UDP payload) + 16 (DHCP yiaddr) = 58.
	const yiOff = 42 + 16
	if !bytes.Equal(offer[yiOff:yiOff+4], ack[yiOff:yiOff+4]) {
		t.Errorf("yiaddr differs OFFER %v vs ACK %v (lease not stable)",
			offer[yiOff:yiOff+4], ack[yiOff:yiOff+4])
	}
}

// mutateVendorClass returns a copy of a golden DHCP request frame with its
// vendor-class (option 60) TLV removed (val == nil) or its value replaced by
// val. The option is located by walking the option tags — never a hardcoded
// offset — and the frame is re-originated with BuildUDPFrame so the IP/UDP
// lengths and checksums stay valid. Mirrors the helper beside
// TestResponderRequiresPXEVendorClass in the parent module's oracle_test.go
// (test helpers are not importable across the module boundary).
func mutateVendorClass(t *testing.T, orig, val []byte) []byte {
	t.Helper()
	u, ok := frame.ParseUDP(orig)
	if !ok {
		t.Fatal("golden frame did not parse as UDP")
	}
	body := u.Payload
	out := append([]byte(nil), body[:dhcp.OffOptions]...)
	found := false
	for off := dhcp.OffOptions; off < len(body); {
		code := body[off]
		if code == dhcp.OptPad {
			out = append(out, code)
			off++
			continue
		}
		if code == dhcp.OptEnd {
			out = append(out, body[off:]...)
			break
		}
		l := int(body[off+1])
		if code == dhcp.OptVendorClass {
			found = true
			if val != nil {
				out = append(out, code, byte(len(val)))
				out = append(out, val...)
			}
		} else {
			out = append(out, body[off:off+2+l]...)
		}
		off += 2 + l
	}
	if !found {
		t.Fatal("golden frame carries no option 60 (helper bug)")
	}
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: u.DstMAC, SrcMAC: u.SrcMAC,
		SrcIP: u.SrcIP, DstIP: u.DstIP,
		SrcPort: u.SrcPort, DstPort: u.DstPort,
		Payload: out,
	})
}

// nonPXEDHCPVariants are the rogue-DHCP negative frames: a DISCOVER and a
// REQUEST with the vendor class (option 60) stripped, and with a non-PXE
// vendor class — none may be answered (responder.go's option-60 gate, ported
// as check_vendor_pxe in dhcp_loop.asm and netboot_server.asm).
func nonPXEDHCPVariants(t *testing.T) []struct {
	name string
	req  []byte
} {
	t.Helper()
	return []struct {
		name string
		req  []byte
	}{
		{"DISCOVER without option 60", mutateVendorClass(t, golden.DHCPDiscover, nil)},
		{"DISCOVER with a non-PXE vendor class", mutateVendorClass(t, golden.DHCPDiscover, []byte("MSFT 5.0"))},
		{"REQUEST without option 60", mutateVendorClass(t, golden.DHCPRequest, nil)},
		{"REQUEST with a non-PXE vendor class", mutateVendorClass(t, golden.DHCPRequest, []byte("MSFT 5.0"))},
	}
}

// TestDHCPResponderRequiresPXEVendorClass — rogue-DHCP protection: the loop
// serves only PXE netboot clients, so a DISCOVER/REQUEST whose vendor class
// (option 60) is absent or lacks the "PXEClient" prefix is ignored (nothing
// transmitted), and the conformant golden DISCOVER is still answered
// afterwards. Verifies the Z80 check_vendor_pxe port of responder.go's gate.
func TestDHCPResponderRequiresPXEVendorClass(t *testing.T) {
	mac := loadDHCPLoop(t)
	fillDHCPConfig(t, mac)
	enc := z80h.NewENC28J60()
	initDHCPLoopDriver(t, mac, enc)

	for _, tc := range nonPXEDHCPVariants(t) {
		if got := serveOnce(t, mac, enc, tc.req); got != nil {
			t.Errorf("%s: loop replied (%d bytes), want silence", tc.name, len(got))
		}
	}

	// The conformant golden DISCOVER on the same machine is still answered,
	// byte-for-byte the Go authority — the silences above are the gate, not a
	// wedged loop, and the ignored frames allocated no lease.
	got := serveOnce(t, mac, enc, golden.DHCPDiscover)
	want := goResponder().OnRequest(golden.DHCPDiscover)
	if got == nil || !bytes.Equal(got, want) {
		t.Errorf("conformant DISCOVER after non-PXE frames != Go authority\n  z80 %x\n  go  %x", got, want)
	}
}

// TestDHCPResponderIgnoresNonDHCP confirms the loop ignores a non-DHCP frame
// (an RRQ to port 69) and transmits nothing — keep serving, never choke.
func TestDHCPResponderIgnoresNonDHCP(t *testing.T) {
	mac := loadDHCPLoop(t)
	fillDHCPConfig(t, mac)
	enc := z80h.NewENC28J60()
	initDHCPLoopDriver(t, mac, enc)

	if got := serveOnce(t, mac, enc, golden.TFTPRrqRoot1024); got != nil {
		t.Errorf("loop replied to a non-DHCP frame: %x", got)
	}
}

// TestDHCPResponderEmptyWire: with nothing injected, dhcp_serve_once returns
// BC=0 and sends nothing.
func TestDHCPResponderEmptyWire(t *testing.T) {
	mac := loadDHCPLoop(t)
	fillDHCPConfig(t, mac)
	enc := z80h.NewENC28J60()
	initDHCPLoopDriver(t, mac, enc)

	res, err := mac.Call("dhcp_serve_once")
	if err != nil {
		t.Fatalf("call dhcp_serve_once: %v", err)
	}
	if res.BC != 0 {
		t.Fatalf("empty wire: dhcp_serve_once returned BC=%d, want 0", res.BC)
	}
	if len(enc.TXFrames()) != 0 {
		t.Fatalf("empty wire: transmitted %d frames, want 0", len(enc.TXFrames()))
	}
}
