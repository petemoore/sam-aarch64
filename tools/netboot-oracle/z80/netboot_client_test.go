// netboot_client_test.go — the i82 host-verification of the TFTP client boot
// disk. It runs the real composed state machine (src/netboot/netboot_client.asm:
// the driver encdrv.asm + the host-verified build_udp_frame / build_arp_request /
// tftp_client (build_rrq) primitives + the bdos_seam field arithmetic) under the
// flat-memory koron-go/z80 harness with the emulated Trinity (enc28j60.go)
// attached, and asserts that the client's wire side — the broadcast ARP request,
// the RRQ after the ARP reply, the ACK cadence, and the accumulated file bytes in
// STAGING — driven through client_first / client_run_once matches the Go authority
// client.Client byte-for-byte.
//
// This is the client fetch made host-verifiable end-to-end by the i80 emulation.
// It is emulation verification, NOT hardware verification — the B-DOS RST-8 HSAVE
// that writes the received bytes to Trinity storage (no ROM/DOS in the
// harness), the real ENC28J60 silicon, and an end-to-end fetch on real hardware
// stay gated on real Trinity (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/client"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	cliBinPath = "../../../build/netboot_client.bin"
	cliMapPath = "../../../build/netboot_client.map"

	cliBlksize  = 1428
	cliOwnTID   = 30574
	cliSrvTID   = 40136
	cliFilename = "recovery.mgt"
)

var (
	cClientMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	cClientIP  = [4]byte{192, 0, 2, 44}
	cServerMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	cServerIP  = [4]byte{192, 0, 2, 1}
)

func loadClient(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(cliBinPath); err != nil {
		t.Fatalf("netboot_client binary not built (%s); run `make netboot-client`", cliBinPath)
	}
	mac, err := z80h.Load(cliBinPath, cliMapPath)
	if err != nil {
		t.Fatalf("load netboot_client: %v", err)
	}
	return mac
}

// fillClientConfig writes the CONFIG_*/state and returns the matching Go Client.
func fillClientConfig(t *testing.T, mac *z80h.Machine) *client.Client {
	t.Helper()
	put := func(sym string, data []byte) { mac.Write(symAddr(t, mac, sym), data) }
	be16 := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v & 0xff)} }

	put("CLIENT_MAC", cClientMAC[:])
	put("CLIENT_IP", cClientIP[:])
	put("CLIENT_TID", be16(cliOwnTID))
	put("CLIENT_BLKSIZE", []byte{0x00, 0x02}) // 512 LE — the default until an OACK negotiates
	put("SERVER_IP", cServerIP[:])
	put("RRQ_FILENAME", append([]byte(cliFilename), 0))

	// clear the transfer state explicitly (the harness RAM is not guaranteed 0).
	put("PHASE", []byte{0})
	put("GOT_MAC", []byte{0})
	put("GOT_SERVER", []byte{0})
	put("XFER_DONE", []byte{0})
	put("ACKED", []byte{0, 0})
	put("STAGE_OFFSET", []byte{0, 0})

	return client.New(client.Config{
		ClientMAC: cClientMAC, ClientIP: cClientIP, ServerIP: cServerIP,
		ClientTID: cliOwnTID, Filename: cliFilename,
	})
}

func initClientDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	mac.AttachIO(enc)
	macAddr := symAddr(t, mac, "CLIENT_MAC")
	mac.Write(macAddr, cClientMAC[:])
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_init returned BC=%d, want 1", res.BC)
	}
}

// clientStep injects rx (if non-nil), runs entry (client_first or client_run_once),
// and returns the single transmitted frame (or nil if nothing was sent).
func clientStep(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, entry string, rx []byte) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	if rx != nil {
		enc.InjectRX(rx)
	}
	res, err := mac.Call(entry)
	if err != nil {
		t.Fatalf("call %s: %v", entry, err)
	}
	tx := enc.TXFrames()
	if res.BC == 0 {
		if len(tx) != before {
			t.Fatalf("%s returned BC=0 but transmitted a frame", entry)
		}
		return nil
	}
	if len(tx) != before+1 {
		t.Fatalf("%s transmitted %d frames, want 1", entry, len(tx)-before)
	}
	out := tx[len(tx)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("%s returned BC=%d but the wire frame is %d bytes", entry, res.BC, len(out))
	}
	return out
}

func cDataFrame(serverTID, block uint16, payload []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: cClientMAC, SrcMAC: cServerMAC, SrcIP: cServerIP, DstIP: cClientIP,
		SrcPort: serverTID, DstPort: cliOwnTID,
		Payload: tftp.BuildDATA(block, payload),
	})
}

func cOackFrame(serverTID uint16, opts []tftp.Option) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: cClientMAC, SrcMAC: cServerMAC, SrcIP: cServerIP, DstIP: cClientIP,
		SrcPort: serverTID, DstPort: cliOwnTID,
		Payload: tftp.BuildOACK(opts),
	})
}

func eqClient(t *testing.T, label string, got, want []byte) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: the driver sent nothing", label)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s != Go authority\n  z80 %x\n  go  %x", label, got, want)
	}
}

// TestClientFullFetchSession is the headline i82 host check: the whole fetch —
// ARP request, ARP reply -> RRQ, DATA/ACK to the short final block, with the
// staged bytes equal to the file — driven through client_first / client_run_once
// matches client.Client byte-for-byte.
func TestClientFullFetchSession(t *testing.T) {
	file := makeFile(cliBlksize*2 + 100) // 2 full blocks + a 100-byte tail
	mac := loadClient(t)
	ref := fillClientConfig(t, mac)
	enc := z80h.NewENC28J60()
	initClientDriver(t, mac, enc)

	// 1. client_first -> the broadcast ARP request.
	eqClient(t, "ARP request", clientStep(t, mac, enc, "client_first", nil), ref.First())

	// 2. A DATA frame while still in the ARP phase is ignored.
	if r := clientStep(t, mac, enc, "client_run_once", cDataFrame(cliSrvTID, 1, file[:cliBlksize])); r != nil {
		t.Fatalf("a DATA frame in the ARP phase should be ignored, got %x", r)
	}
	if tx, _ := ref.OnFrame(cDataFrame(cliSrvTID, 1, file[:cliBlksize])); tx != nil {
		t.Fatalf("Go authority also ignores a DATA in the ARP phase")
	}

	// 3. ARP reply -> the RRQ frame.
	arpReply := frame.BuildARPReply(cServerMAC, cClientMAC, cServerIP, cClientIP)
	goRRQ, _ := ref.OnFrame(arpReply)
	eqClient(t, "RRQ", clientStep(t, mac, enc, "client_run_once", arpReply), goRRQ)

	// 4. The server OACKs the requested options (blksize 1428) -> ACK block 0;
	//    the negotiated blksize is adopted (the DATA below streams at 1428).
	oack := cOackFrame(cliSrvTID, []tftp.Option{
		{Name: "blksize", Value: "1428"}, {Name: "tsize", Value: "2956"}, {Name: "timeout", Value: "2"},
	})
	goAck0, _ := ref.OnFrame(oack)
	eqClient(t, "ACK 0", clientStep(t, mac, enc, "client_run_once", oack), goAck0)

	// 5. DATA block 1 -> ACK 1.
	d1 := cDataFrame(cliSrvTID, 1, file[:cliBlksize])
	goAck1, _ := ref.OnFrame(d1)
	eqClient(t, "ACK 1", clientStep(t, mac, enc, "client_run_once", d1), goAck1)

	// 6. DATA block 2 -> ACK 2.
	d2 := cDataFrame(cliSrvTID, 2, file[cliBlksize:2*cliBlksize])
	goAck2, _ := ref.OnFrame(d2)
	eqClient(t, "ACK 2", clientStep(t, mac, enc, "client_run_once", d2), goAck2)

	// 7. DATA block 3 (short final, 100 bytes) -> ACK 3 + done.
	d3 := cDataFrame(cliSrvTID, 3, file[2*cliBlksize:])
	goAck3, goDone := ref.OnFrame(d3)
	eqClient(t, "ACK 3", clientStep(t, mac, enc, "client_run_once", d3), goAck3)
	if !goDone {
		t.Fatalf("the Go authority should be done after the short final block")
	}

	// XFER_DONE set, and the staged bytes equal the file.
	if mac.Read(symAddr(t, mac, "XFER_DONE"), 1)[0] != 1 {
		t.Fatalf("XFER_DONE not set after the short final block")
	}
	staged := mac.Read(symAddr(t, mac, "STAGING"), len(file))
	if !bytes.Equal(staged, file) {
		t.Fatalf("staged bytes != file (%d bytes); first mismatch matters", len(file))
	}
	if !bytes.Equal(ref.Bytes(), file) {
		t.Fatalf("Go authority staged bytes != file")
	}
}

// TestClientWrongIPArpIgnored confirms an ARP reply for a different IP does not
// advance the Z80 client (it stays in the ARP phase, sends nothing).
func TestClientWrongIPArpIgnored(t *testing.T) {
	mac := loadClient(t)
	fillClientConfig(t, mac)
	enc := z80h.NewENC28J60()
	initClientDriver(t, mac, enc)
	clientStep(t, mac, enc, "client_first", nil)

	other := frame.BuildARPReply(cServerMAC, cClientMAC, cClientIP, cClientIP) // SPA=clientIP
	if r := clientStep(t, mac, enc, "client_run_once", other); r != nil {
		t.Fatalf("an ARP reply for a different IP should be ignored, got %x", r)
	}
	if mac.Read(symAddr(t, mac, "PHASE"), 1)[0] != 0 {
		t.Fatalf("phase should still be ARP (0)")
	}
}

// TestClientStrayTIDError confirms a DATA from a wrong server TID gets an ERROR(5)
// to the stray sender, matching the Go authority.
func TestClientStrayTIDError(t *testing.T) {
	file := makeFile(cliBlksize * 2)
	mac := loadClient(t)
	ref := fillClientConfig(t, mac)
	enc := z80h.NewENC28J60()
	initClientDriver(t, mac, enc)

	clientStep(t, mac, enc, "client_first", nil)
	arpReply := frame.BuildARPReply(cServerMAC, cClientMAC, cServerIP, cClientIP)
	ref.OnFrame(arpReply)
	clientStep(t, mac, enc, "client_run_once", arpReply) // -> RRQ
	// block 1 from the real server.
	d1 := cDataFrame(cliSrvTID, 1, file[:cliBlksize])
	ref.OnFrame(d1)
	clientStep(t, mac, enc, "client_run_once", d1)
	// a stray DATA from a different TID -> ERROR(5).
	stray := cDataFrame(cliSrvTID+7, 2, file[cliBlksize:])
	goErr, _ := ref.OnFrame(stray)
	got := clientStep(t, mac, enc, "client_run_once", stray)
	eqClient(t, "ERROR(5)", got, goErr)
	code, _, err := tftp.ParseError(udpPayload(t, got))
	if err != nil || code != tftp.ErrUnknownTID {
		t.Fatalf("stray reply should be ERROR(5), got code %d err %v", code, err)
	}
}

// TestClientNoOACKFallbackSession confirms the Z80 client interoperates with a
// server that ignores options: no OACK, DATA streamed at the 512 default. A full
// 512-byte block 1 must NOT be treated as short (the bug a 1428 default caused);
// block 2 (300) is the short final. Matches the Go authority byte-for-byte.
func TestClientNoOACKFallbackSession(t *testing.T) {
	file := makeFile(512 + 300)
	mac := loadClient(t)
	ref := fillClientConfig(t, mac)
	enc := z80h.NewENC28J60()
	initClientDriver(t, mac, enc)

	clientStep(t, mac, enc, "client_first", nil)
	arpReply := frame.BuildARPReply(cServerMAC, cClientMAC, cServerIP, cClientIP)
	ref.OnFrame(arpReply)
	clientStep(t, mac, enc, "client_run_once", arpReply) // -> RRQ

	// block 1 (a full 512-byte block) -> ACK 1, not done.
	d1 := cDataFrame(cliSrvTID, 1, file[:512])
	goAck1, _ := ref.OnFrame(d1)
	eqClient(t, "ACK 1 (512 default)", clientStep(t, mac, enc, "client_run_once", d1), goAck1)
	if mac.Read(symAddr(t, mac, "XFER_DONE"), 1)[0] != 0 {
		t.Fatalf("a full 512-byte block must not finish the transfer")
	}
	// block 2 (300 bytes, short final) -> ACK 2 + done.
	d2 := cDataFrame(cliSrvTID, 2, file[512:])
	goAck2, goDone := ref.OnFrame(d2)
	eqClient(t, "ACK 2", clientStep(t, mac, enc, "client_run_once", d2), goAck2)
	if !goDone {
		t.Fatalf("the Go authority should be done after the 300-byte final block")
	}
	if mac.Read(symAddr(t, mac, "XFER_DONE"), 1)[0] != 1 {
		t.Fatalf("XFER_DONE not set after the short final block")
	}
	staged := mac.Read(symAddr(t, mac, "STAGING"), len(file))
	if !bytes.Equal(staged, file) {
		t.Fatalf("staged bytes != file")
	}
}
