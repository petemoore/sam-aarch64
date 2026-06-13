// tftp_client_front_test.go — the i82 host-verification of the TFTP client's
// request-origination front (the step before the receive loop). It runs the
// *real* composed routine (src/netboot/tftp_client_front.asm: the driver
// encdrv.asm + the host-verified build_arp_request/build_rrq/build_udp_frame
// primitives) under the flat-memory koron-go/z80 harness with the emulated
// Trinity (enc28j60.go) attached, and asserts the full originate flow on the
// virtual wire byte-for-byte against the Go authority (tftp.ClientFront):
//
//	tftp_send_arp  -> a broadcast ARP request == ClientFront.ARPRequest()
//	inject an ARP reply; tftp_recv_arp -> learns the server MAC (BC=1)
//	tftp_send_rrq  -> the RRQ frame == ClientFront.RRQFrame(filename)
//
// Emulation verification, NOT hardware verification — real Trinity remains the
// final gate (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	tftpFrontBinPath = "../../../build/netboot_tftp_client_front.bin"
	tftpFrontMapPath = "../../../build/netboot_tftp_client_front.map"
	frontTID         = 30574 // the client's source TID for the RRQ
)

func loadTFTPFront(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(tftpFrontBinPath); err != nil {
		t.Skipf("tftp_client_front binary not built (%s); run `make netboot-tftp-client-front`", tftpFrontBinPath)
	}
	mac, err := z80h.Load(tftpFrontBinPath, tftpFrontMapPath)
	if err != nil {
		t.Fatalf("load tftp_client_front: %v", err)
	}
	return mac
}

// fillTFTPFront writes the CLIENT_* identity + the target SERVER_IP + the RRQ
// filename, returning the Go ClientFront configured identically.
func fillTFTPFront(t *testing.T, mac *z80h.Machine, filename string) *tftp.ClientFront {
	t.Helper()
	put := func(sym string, data []byte) {
		mac.Write(symAddr(t, mac, sym), data)
	}
	put("CLIENT_MAC", mask.ClientMAC[:])
	put("CLIENT_IP", mask.ClientIP[:])
	put("CLIENT_TID", []byte{byte(frontTID >> 8), byte(frontTID & 0xff)})
	put("SERVER_IP", mask.ServerIP[:])
	put("RRQ_FILENAME", append([]byte(filename), 0))
	return tftp.NewClientFront(mask.ClientMAC, mask.ClientIP, mask.ServerIP, frontTID)
}

func initTFTPFrontDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	mac.AttachIO(enc)
	macAddr := symAddr(t, mac, "CLIENT_MAC")
	mac.Write(macAddr, mask.ClientMAC[:])
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_init returned BC=%d, want 1", res.BC)
	}
}

// arpReplyFromServer builds the server's unicast ARP reply to the client's
// broadcast request (the input tftp_recv_arp parses).
func arpReplyFromServer(senderMAC frame.MAC, senderIP frame.IPv4) []byte {
	f := make([]byte, frame.ARPFrameLen)
	copy(f[frame.OffDstMAC:], mask.ClientMAC[:]) // unicast to the asker
	copy(f[frame.OffSrcMAC:], senderMAC[:])
	f[frame.OffEtherType] = byte(frame.EtherTypeARP >> 8)
	f[frame.OffEtherType+1] = byte(frame.EtherTypeARP & 0xff)
	p := f[frame.EthHeaderLen:]
	p[0], p[1] = byte(frame.ARPHTypeEthernet>>8), byte(frame.ARPHTypeEthernet&0xff)
	p[2], p[3] = byte(frame.ARPPTypeIPv4>>8), byte(frame.ARPPTypeIPv4&0xff)
	p[4], p[5] = frame.ARPHLen, frame.ARPPLen
	p[6], p[7] = byte(frame.ARPOpReply>>8), byte(frame.ARPOpReply&0xff)
	copy(p[8:14], senderMAC[:])
	copy(p[14:18], senderIP[:])
	copy(p[18:24], mask.ClientMAC[:])
	copy(p[24:28], mask.ClientIP[:])
	return f
}

// TestTFTPClientFrontOriginate is the headline i82 originate check: the client
// broadcasts an ARP request, learns the server MAC from the reply, and sends the
// RRQ to that MAC — the ARP request and the RRQ frame both byte-for-byte the Go
// ClientFront authority.
func TestTFTPClientFrontOriginate(t *testing.T) {
	mac := loadTFTPFront(t)
	const filename = "config.txt"
	ref := fillTFTPFront(t, mac, filename)
	enc := z80h.NewENC28J60()
	initTFTPFrontDriver(t, mac, enc)

	// 1. tftp_send_arp -> a broadcast ARP request on the wire.
	gotARP := txAfter(t, mac, enc, "tftp_send_arp", nil)
	if gotARP == nil {
		t.Fatal("tftp_send_arp transmitted nothing")
	}
	if !bytes.Equal(gotARP, ref.ARPRequest()) {
		t.Errorf("ARP request != Go authority\n  z80 %x\n  go  %x", gotARP, ref.ARPRequest())
	}

	// 2. Inject the server's ARP reply; tftp_recv_arp learns the server MAC.
	reply := arpReplyFromServer(mask.ServerMAC, mask.ServerIP)
	enc.InjectRX(reply)
	res, err := mac.Call("tftp_recv_arp")
	if err != nil {
		t.Fatalf("call tftp_recv_arp: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("tftp_recv_arp returned BC=%d, want 1 (learned)", res.BC)
	}
	if !ref.OnARPReply(reply) {
		t.Fatal("Go ClientFront rejected the matching reply")
	}
	gotMAC := mac.Read(symAddr(t, mac, "SERVER_MAC"), 6)
	if !bytes.Equal(gotMAC, mask.ServerMAC[:]) {
		t.Errorf("learned SERVER_MAC = %x, want %x", gotMAC, mask.ServerMAC)
	}

	// 3. tftp_send_rrq -> the RRQ frame to the learned server MAC.
	gotRRQ := txAfter(t, mac, enc, "tftp_send_rrq", nil)
	if gotRRQ == nil {
		t.Fatal("tftp_send_rrq transmitted nothing")
	}
	want := ref.RRQFrame(filename)
	if !bytes.Equal(gotRRQ, want) {
		t.Errorf("RRQ frame != Go authority\n  z80 %x\n  go  %x", gotRRQ, want)
	}
	// And it must parse as a UDP RRQ from our TID to port 69.
	u, ok := frame.ParseUDP(gotRRQ)
	if !ok || u.SrcPort != frontTID || u.DstPort != 69 {
		t.Errorf("RRQ frame UDP = src %d dst %d, want src %d dst 69", u.SrcPort, u.DstPort, frontTID)
	}
	if tftp.Opcode(u.Payload) != tftp.OpRRQ {
		t.Errorf("RRQ opcode = %d, want RRQ", tftp.Opcode(u.Payload))
	}
}

// TestTFTPClientFrontIgnoresNonMatch confirms tftp_recv_arp ignores a frame that
// is not a matching ARP reply (a non-ARP frame, and an ARP reply for a different
// IP) — BC=0, no MAC learned — matching ClientFront.OnARPReply.
func TestTFTPClientFrontIgnoresNonMatch(t *testing.T) {
	mac := loadTFTPFront(t)
	ref := fillTFTPFront(t, mac, "config.txt")
	enc := z80h.NewENC28J60()
	initTFTPFrontDriver(t, mac, enc)

	// An ARP reply for a *different* IP must be ignored.
	otherIP := frame.IPv4{192, 0, 2, 99}
	enc.InjectRX(arpReplyFromServer(mask.ServerMAC, otherIP))
	res, err := mac.Call("tftp_recv_arp")
	if err != nil {
		t.Fatalf("call tftp_recv_arp: %v", err)
	}
	if res.BC != 0 {
		t.Errorf("tftp_recv_arp learned a MAC from a wrong-IP reply (BC=%d)", res.BC)
	}
	if got := mac.Read(symAddr(t, mac, "GOT_MAC"), 1); got[0] != 0 {
		t.Errorf("GOT_MAC set after a non-matching reply")
	}
	if ref.OnARPReply(arpReplyFromServer(mask.ServerMAC, otherIP)) {
		t.Error("Go ClientFront accepted a wrong-IP reply")
	}

	// The matching reply is still learned afterwards.
	good := arpReplyFromServer(mask.ServerMAC, mask.ServerIP)
	enc.InjectRX(good)
	res, err = mac.Call("tftp_recv_arp")
	if err != nil {
		t.Fatalf("call tftp_recv_arp (match): %v", err)
	}
	if res.BC != 1 {
		t.Errorf("tftp_recv_arp BC=%d after the matching reply, want 1", res.BC)
	}
}
