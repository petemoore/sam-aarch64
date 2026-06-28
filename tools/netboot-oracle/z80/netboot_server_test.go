// netboot_server_test.go — the i95 host-verification of the integrated netboot
// server. It runs the *real* composed state machine
// (src/netboot/netboot_server.asm: the driver encdrv.asm + the host-verified
// build_udp_frame/build_arp_reply/dhcp_reply/tftp_build/tftp_parse primitives)
// under the flat-memory koron-go/z80 harness with the emulated Trinity
// (enc28j60.go) attached, and asserts that a full netboot session — DHCP
// DISCOVER->OFFER, REQUEST->ACK, an ARP request, TFTP RRQ->OACK, ACK->DATA to
// the short final block — driven through the single netboot_serve_once
// dispatcher matches the Go authority server.Server.OnFrame (and its standalone
// sub-responders) frame-for-frame, byte-for-byte.
//
// This is the integrated dispatch made host-verifiable end-to-end by the i80
// emulation: one drv_read -> route (ARP / DHCP / TFTP) -> drv_write, the whole
// netboot server in one loop, proven against the byte-exact Go reference. It is
// emulation verification, NOT hardware verification — the real ENC28J60 silicon,
// the B-DOS RST-8 hook dispatch (the real file source), and an end-to-end Pi boot
// stay gated on real Trinity (CLAUDE.md §5).
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
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/server"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/smoke"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	srvBinPath = "../../../build/netboot_server.bin"
	srvMapPath = "../../../build/netboot_server.map"

	nbServerTID = 40136  // the SAM's transfer source port
	nbClientTID = 30574  // the captured client RRQ source port
	nbSrcOrg    = 0xC000 // where the test stages the file source bytes in Z80 RAM
)

// nbConfig is the SAM's fixed identity + DHCP pool, shared by the Z80 CONFIG_*
// block and every Go sub-responder so they produce identical reply bytes. It
// mirrors the i86 dhcpLoopConfig + the i83 tftp server identity.
var nbConfig = struct {
	serverMAC         [6]byte
	serverIP, subnet  [4]byte
	broadcast         [4]byte
	poolBase          [4]byte
	poolSize          int
	leaseSecs, t1, t2 uint32
	serverTID         uint16
}{
	serverMAC: mask.ServerMAC,
	serverIP:  mask.ServerIP,
	subnet:    [4]byte{255, 255, 255, 0},
	broadcast: mask.Broadcast,
	poolBase:  [4]byte{192, 0, 2, 100},
	poolSize:  8,
	leaseSecs: 7200, t1: 3600, t2: 6300,
	serverTID: nbServerTID,
}

func loadServer(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(srvBinPath); err != nil {
		t.Fatalf("netboot_server binary not built (%s); run `make netboot-server`", srvBinPath)
	}
	mac, err := z80h.Load(srvBinPath, srvMapPath)
	if err != nil {
		t.Fatalf("load netboot_server: %v", err)
	}
	return mac
}

// fillServerConfig writes the integrated CONFIG_* block + the flat store + the
// file source, and returns the matching Go Server (the i95 authority) plus the
// standalone sub-responders the dispatch must match byte-for-byte.
func fillServerConfig(t *testing.T, mac *z80h.Machine, name string, file []byte) (*server.Server, *dhcp.Responder, *smoke.Responder, *tftp.ServerLoop) {
	t.Helper()
	put := func(sym string, data []byte) {
		a, err := mac.Sym(sym)
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
	be16 := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v & 0xff)} }

	put("CONFIG_SERVERMAC", nbConfig.serverMAC[:])
	put("CONFIG_SERVERIP", nbConfig.serverIP[:])
	put("CONFIG_SUBNET", nbConfig.subnet[:])
	put("CONFIG_BROADCAST", nbConfig.broadcast[:])
	put("CONFIG_LEASE", be32(nbConfig.leaseSecs))
	put("CONFIG_T1", be32(nbConfig.t1))
	put("CONFIG_T2", be32(nbConfig.t2))
	put("CONFIG_POOLBASE", nbConfig.poolBase[:])
	put("CONFIG_POOLSIZE", []byte{byte(nbConfig.poolSize)})
	put("CONFIG_SERVERTID", be16(nbConfig.serverTID))

	// Flat store: "name\0" + 4-byte LE size, then a single NUL terminator
	// (the resolve walk's end-of-store sentinel).
	var store bytes.Buffer
	store.WriteString(name)
	store.WriteByte(0)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(file)))
	store.Write(sz[:])
	store.WriteByte(0)
	put("STORE", store.Bytes())

	// Source bytes at nbSrcOrg; SRC_PTR points there.
	mac.Write(nbSrcOrg, file)
	mac.WriteU16LE(symAddr(t, mac, "SRC_PTR"), nbSrcOrg)

	cfg := server.Config{
		ServerMAC: nbConfig.serverMAC, ServerIP: nbConfig.serverIP,
		Subnet: nbConfig.subnet, Broadcast: nbConfig.broadcast,
		PoolBase: nbConfig.poolBase, PoolSize: nbConfig.poolSize,
		LeaseSecs: nbConfig.leaseSecs, T1: nbConfig.t1, T2: nbConfig.t2,
		ServerTID: nbConfig.serverTID,
	}
	goStore := tftp.MapStore{name: uint64(len(file))}
	srv := server.New(cfg, goStore, func(string) tftp.Source { return tftp.ByteSource(file) })

	refDHCP := dhcp.NewResponder(cfg.ServerMAC, cfg.ServerIP, cfg.Subnet, cfg.Broadcast,
		cfg.PoolBase, cfg.PoolSize, cfg.LeaseSecs, cfg.T1, cfg.T2)
	refARP := smoke.NewResponder(cfg.ServerMAC, cfg.ServerIP)
	refTFTP := tftp.NewServerLoop(goStore, cfg.ServerMAC, cfg.ServerIP, cfg.ServerTID)
	refTFTP.SetSource(tftp.ByteSource(file))
	return srv, refDHCP, refARP, refTFTP
}

func initServerDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	mac.AttachIO(enc)
	macAddr := symAddr(t, mac, "CONFIG_SERVERMAC")
	mac.Write(macAddr, nbConfig.serverMAC[:])
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_init returned BC=%d, want 1", res.BC)
	}
}

// serveServer injects req (if non-nil) and runs one netboot_serve_once,
// returning the single frame the driver transmitted (or nil if it sent nothing).
func serveServer(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, req []byte) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	if req != nil {
		enc.InjectRX(req)
	}
	res, err := mac.Call("netboot_serve_once")
	if err != nil {
		t.Fatalf("call netboot_serve_once: %v", err)
	}
	tx := enc.TXFrames()
	if res.BC == 0 {
		if len(tx) != before {
			t.Fatalf("netboot_serve_once returned BC=0 but transmitted a frame")
		}
		return nil
	}
	if len(tx) != before+1 {
		t.Fatalf("netboot_serve_once transmitted %d frames, want 1", len(tx)-before)
	}
	out := tx[len(tx)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("netboot_serve_once returned BC=%d but the wire frame is %d bytes", res.BC, len(out))
	}
	return out
}

// nbAck builds the client's ACK frame (client TID -> server TID).
func nbAck(block uint16) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
		SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
		SrcPort: nbClientTID, DstPort: nbServerTID,
		Payload: tftp.BuildACK(block),
	})
}

// TestServerFullSession is the headline i95 host check: a full netboot session
// driven through the single netboot_serve_once dispatcher matches the Go
// authority sub-responders frame-for-frame, byte-for-byte — exactly the sequence
// TestIntegratedServerDispatch drives the Go server.Server.OnFrame through.
func TestServerFullSession(t *testing.T) {
	const blksize = 1024
	file := makeFile(2*blksize + 300) // 2 full blocks + a 300-byte tail
	mac := loadServer(t)
	_, refDHCP, refARP, refTFTP := fillServerConfig(t, mac, "config.txt", file)
	enc := z80h.NewENC28J60()
	initServerDriver(t, mac, enc)

	eq := func(label string, got, want []byte) {
		t.Helper()
		if got == nil {
			t.Fatalf("%s: dispatch sent nothing", label)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s != Go authority\n  z80 %x\n  go  %x", label, got, want)
		}
	}

	// 1. DHCP DISCOVER -> OFFER.
	eq("OFFER", serveServer(t, mac, enc, golden.DHCPDiscover), refDHCP.OnRequest(golden.DHCPDiscover))
	// 2. DHCP REQUEST -> ACK.
	eq("DHCP ACK", serveServer(t, mac, enc, golden.DHCPRequest), refDHCP.OnRequest(golden.DHCPRequest))
	// 3. An ARP request for the SAM's IP -> an ARP reply.
	arpReq := frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ServerIP)
	eq("ARP reply", serveServer(t, mac, enc, arpReq), refARP.OnFrame(arpReq))
	// 4. TFTP RRQ -> OACK (arms the transfer).
	eq("OACK", serveServer(t, mac, enc, golden.TFTPRrqRoot1024), refTFTP.OnRRQ(golden.TFTPRrqRoot1024))
	// 5. ACK 0 -> first DATA (block 1); the dispatch routes it to FirstData.
	eq("DATA 1", serveServer(t, mac, enc, nbAck(0)), refTFTP.FirstData())
	// 6. ACK 1 -> DATA 2 (full).
	eq("DATA 2", serveServer(t, mac, enc, nbAck(1)), refTFTP.OnACK(nbAck(1)))
	// 7. ACK 2 -> DATA 3 (short final, 300 bytes).
	d3 := serveServer(t, mac, enc, nbAck(2))
	eq("DATA 3", d3, refTFTP.OnACK(nbAck(2)))
	if _, p3, _ := tftp.ParseDATA(udpPayload(t, d3)); len(p3) != 300 {
		t.Errorf("final block payload = %d bytes, want 300", len(p3))
	}
	// 8. ACK 3 -> transfer complete, nothing sent.
	if fin := serveServer(t, mac, enc, nbAck(3)); fin != nil {
		t.Errorf("ACK of the short final block should end the transfer, got %x", fin)
	}
	_ = refTFTP.OnACK(nbAck(3)) // keep the reference in lockstep

	// 9. A non-matching frame (an ARP request for a different IP) is ignored.
	if r := serveServer(t, mac, enc, frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ClientIP)); r != nil {
		t.Errorf("dispatch replied to an ARP request for a different IP: %x", r)
	}
}

// TestServerLeaseStable confirms the OFFER and ACK carry the same yiaddr (lease
// stable per MAC, the responder pool keyed by client MAC), driven through the
// integrated dispatcher.
func TestServerLeaseStable(t *testing.T) {
	mac := loadServer(t)
	fillServerConfig(t, mac, "config.txt", makeFile(512))
	enc := z80h.NewENC28J60()
	initServerDriver(t, mac, enc)

	offer := serveServer(t, mac, enc, golden.DHCPDiscover)
	ack := serveServer(t, mac, enc, golden.DHCPRequest)
	if offer == nil || ack == nil {
		t.Fatal("OFFER/ACK not produced")
	}
	const yiOff = 42 + 16 // UDP payload + DHCP yiaddr
	if !bytes.Equal(offer[yiOff:yiOff+4], ack[yiOff:yiOff+4]) {
		t.Errorf("yiaddr differs OFFER %v vs ACK %v (lease not stable)",
			offer[yiOff:yiOff+4], ack[yiOff:yiOff+4])
	}
}

// TestServerMissKeepsServing confirms the integrated dispatch serves ERROR(1) on
// a TFTP miss, arms no transfer, and an ACK afterwards is ignored — the headline
// robustness rule (keep serving the session).
func TestServerMissKeepsServing(t *testing.T) {
	mac := loadServer(t)
	fillServerConfig(t, mac, "config.txt", makeFile(100))
	enc := z80h.NewENC28J60()
	initServerDriver(t, mac, enc)

	missRRQ := frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
		SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
		SrcPort: nbClientTID, DstPort: 69,
		Payload: tftp.BuildRRQ("recovery.elf", "octet", []tftp.Option{{Name: "tsize", Value: "0"}, {Name: "blksize", Value: "1024"}}),
	})
	got := serveServer(t, mac, enc, missRRQ)
	if got == nil {
		t.Fatal("dispatch sent nothing for a TFTP miss (should send ERROR(1))")
	}
	if code, _, _ := tftp.ParseError(udpPayload(t, got)); code != tftp.ErrFileNotFound {
		t.Errorf("miss reply code = %d, want 1 (file not found)", code)
	}
	// An ACK after a miss is not part of any transfer -> nothing sent.
	if d := serveServer(t, mac, enc, nbAck(0)); d != nil {
		t.Errorf("dispatch served data after a miss: %x", d)
	}
}

// TestServerSerialSubdir confirms a serial-subdir-prefixed RRQ 404s through the
// integrated dispatcher (the Pi retries at root).
func TestServerSerialSubdir(t *testing.T) {
	mac := loadServer(t)
	// Even if the serial name were in the store, the prefix must 404.
	fillServerConfig(t, mac, "00000000/start4.elf", makeFile(512))
	enc := z80h.NewENC28J60()
	initServerDriver(t, mac, enc)

	got := serveServer(t, mac, enc, golden.TFTPRrqSerial)
	if got == nil {
		t.Fatal("dispatch sent nothing for a serial-subdir RRQ")
	}
	if code, _, _ := tftp.ParseError(udpPayload(t, got)); code != tftp.ErrFileNotFound {
		t.Errorf("serial-subdir reply code = %d, want 1", code)
	}
}

// TestServerIgnoresUnrelated confirms the dispatch stays silent for frames bound
// to none of its responders (a non-IPv4 frame, an unrelated UDP port) and for an
// empty wire — keep serving, never choke.
func TestServerIgnoresUnrelated(t *testing.T) {
	mac := loadServer(t)
	fillServerConfig(t, mac, "config.txt", makeFile(512))
	enc := z80h.NewENC28J60()
	initServerDriver(t, mac, enc)

	// An empty wire.
	if r := serveServer(t, mac, enc, nil); r != nil {
		t.Errorf("empty wire produced a frame: %x", r)
	}
	// A UDP frame to an unrelated port (not 67/69/our TID).
	unrelated := frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
		SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
		SrcPort: 12345, DstPort: 53, // DNS, not ours
		Payload: []byte{0, 0, 0, 0},
	})
	if r := serveServer(t, mac, enc, unrelated); r != nil {
		t.Errorf("dispatch replied to an unrelated UDP port: %x", r)
	}
}

// TestServerIgnoresNonPXE confirms the integrated dispatch answers only PXE
// netboot clients on its DHCP port — rogue-DHCP protection, the netboot_server.asm
// check_vendor_pxe port of responder.go's option-60 gate: a DISCOVER/REQUEST
// whose vendor class is absent or lacks the "PXEClient" prefix gets no reply,
// and the conformant golden DISCOVER is still served afterwards.
func TestServerIgnoresNonPXE(t *testing.T) {
	mac := loadServer(t)
	_, refDHCP, _, _ := fillServerConfig(t, mac, "config.txt", makeFile(512))
	enc := z80h.NewENC28J60()
	initServerDriver(t, mac, enc)

	for _, tc := range nonPXEDHCPVariants(t) {
		if r := serveServer(t, mac, enc, tc.req); r != nil {
			t.Errorf("%s: dispatch replied (%d bytes), want silence", tc.name, len(r))
		}
	}

	// The conformant golden DISCOVER is still answered, byte-for-byte the Go
	// authority — the silences above are the gate, not a wedged dispatch, and
	// the ignored frames allocated no lease.
	got := serveServer(t, mac, enc, golden.DHCPDiscover)
	want := refDHCP.OnRequest(golden.DHCPDiscover)
	if got == nil || !bytes.Equal(got, want) {
		t.Errorf("conformant DISCOVER after non-PXE frames != Go authority\n  z80 %x\n  go  %x", got, want)
	}
}

// TestServerIgnoresStrayACK confirms that, mid-transfer, an ACK arriving on our
// transfer TID from a *different* source port than the client of this transfer is
// ignored — matching serverloop.OnACK's SrcPort guard (not part of this transfer).
func TestServerIgnoresStrayACK(t *testing.T) {
	const blksize = 1024
	file := makeFile(2 * blksize) // enough for more than one DATA block
	mac := loadServer(t)
	_, _, _, refTFTP := fillServerConfig(t, mac, "config.txt", file)
	enc := z80h.NewENC28J60()
	initServerDriver(t, mac, enc)

	// RRQ -> OACK, then ACK 0 -> DATA 1 (arms + advances the transfer).
	serveServer(t, mac, enc, golden.TFTPRrqRoot1024)
	refTFTP.OnRRQ(golden.TFTPRrqRoot1024)
	serveServer(t, mac, enc, nbAck(0))
	refTFTP.FirstData()

	// A stray ACK to our TID but from a different source port: ignored.
	stray := frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
		SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
		SrcPort: nbClientTID + 1, DstPort: nbServerTID,
		Payload: tftp.BuildACK(1),
	})
	if r := serveServer(t, mac, enc, stray); r != nil {
		t.Errorf("dispatch advanced on a stray-source ACK: %x", r)
	}

	// The real client's ACK 1 still advances -> DATA 2, matching the authority.
	d2 := serveServer(t, mac, enc, nbAck(1))
	if !bytes.Equal(d2, refTFTP.OnACK(nbAck(1))) {
		t.Errorf("DATA 2 after the stray ACK != Go authority\n  z80 %x", d2)
	}
}
