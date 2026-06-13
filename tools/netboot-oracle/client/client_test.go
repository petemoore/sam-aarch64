package client

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
)

var (
	cliMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	cliIP  = [4]byte{192, 0, 2, 44}
	srvMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	srvIP  = [4]byte{192, 0, 2, 1}
	cliTID = uint16(30574)
	srvTID = uint16(40136)
)

func cfg(blksize int) Config {
	return Config{
		ClientMAC: cliMAC, ClientIP: cliIP, ServerIP: srvIP,
		ClientTID: cliTID, Filename: "recovery.mgt", Blksize: blksize,
	}
}

func makeFile(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 3)
	}
	return b
}

func arpReply() []byte {
	return frame.BuildARPReply(srvMAC, cliMAC, srvIP, cliIP)
}

func dataFrame(serverTID, block uint16, payload []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: cliMAC, SrcMAC: srvMAC, SrcIP: srvIP, DstIP: cliIP,
		SrcPort: serverTID, DstPort: cliTID,
		Payload: tftp.BuildDATA(block, payload),
	})
}

// TestClientFullFetch is the headline driver check: First() ARP -> ARP reply ->
// RRQ -> DATA/ACK -> done, with the accumulated bytes equal to the file.
func TestClientFullFetch(t *testing.T) {
	const blksize = 1428
	file := makeFile(blksize*2 + 100) // 2 full blocks + a 100-byte tail
	c := New(cfg(blksize))

	// 1. First frame: the broadcast ARP request for the server IP.
	arpReq := c.First()
	if ip, ok := parseTargetIP(arpReq); !ok || ip != srvIP {
		t.Fatalf("First() is not an ARP request for the server IP")
	}

	// 2. A non-ARP frame in the ARP phase is ignored.
	if tx, done := c.OnFrame(dataFrame(srvTID, 1, file[:blksize])); tx != nil || done {
		t.Fatalf("a DATA frame in the ARP phase should be ignored")
	}

	// 3. The ARP reply -> the RRQ frame; advance to the transfer phase.
	rrq, done := c.OnFrame(arpReply())
	if done || rrq == nil {
		t.Fatalf("the ARP reply should produce the RRQ frame, not done")
	}
	if c.Phase() != PhaseXFER {
		t.Fatalf("after the ARP reply the phase should be XFER, got %v", c.Phase())
	}
	u, ok := frame.ParseUDP(rrq)
	if !ok || u.DstPort != 69 || tftp.Opcode(u.Payload) != tftp.OpRRQ {
		t.Fatalf("the RRQ frame is malformed: %x", rrq)
	}
	if c.ServerMAC() != srvMAC {
		t.Fatalf("server MAC not learned: %x", c.ServerMAC())
	}

	// 4. DATA block 1 -> ACK 1.
	ack1, done := c.OnFrame(dataFrame(srvTID, 1, file[:blksize]))
	if done {
		t.Fatalf("block 1 should not finish the transfer")
	}
	if blk := ackBlock(t, ack1); blk != 1 {
		t.Fatalf("expected ACK 1, got ACK %d", blk)
	}
	// 5. DATA block 2 -> ACK 2.
	ack2, done := c.OnFrame(dataFrame(srvTID, 2, file[blksize:2*blksize]))
	if done {
		t.Fatalf("block 2 (full) should not finish the transfer")
	}
	if blk := ackBlock(t, ack2); blk != 2 {
		t.Fatalf("expected ACK 2, got ACK %d", blk)
	}
	// 6. DATA block 3 (short final, 100 bytes) -> ACK 3 + done.
	ack3, done := c.OnFrame(dataFrame(srvTID, 3, file[2*blksize:]))
	if !done {
		t.Fatalf("the short final block should finish the transfer")
	}
	if blk := ackBlock(t, ack3); blk != 3 {
		t.Fatalf("expected ACK 3, got ACK %d", blk)
	}
	if !c.Done() {
		t.Fatalf("Done() should be true after the short final block")
	}
	if !bytes.Equal(c.Bytes(), file) {
		t.Fatalf("accumulated bytes != file (%d vs %d bytes)", len(c.Bytes()), len(file))
	}
}

// TestClientWrongIPArpIgnored confirms an ARP reply for a different IP does not
// advance the client.
func TestClientWrongIPArpIgnored(t *testing.T) {
	c := New(cfg(512))
	c.First()
	other := frame.BuildARPReply(srvMAC, cliMAC, cliIP, cliIP) // SPA = cliIP, not srvIP
	if tx, done := c.OnFrame(other); tx != nil || done {
		t.Fatalf("an ARP reply for a different IP should be ignored")
	}
	if c.Phase() != PhaseARP {
		t.Fatalf("phase should still be ARP, got %v", c.Phase())
	}
}

// TestClientStrayTIDError confirms a DATA from a wrong server TID gets an ERROR(5)
// sent to the stray sender, without disturbing the transfer.
func TestClientStrayTIDError(t *testing.T) {
	const blksize = 512
	file := makeFile(blksize * 2)
	c := New(cfg(blksize))
	c.First()
	c.OnFrame(arpReply()) // -> RRQ, phase XFER
	// block 1 from the real server.
	if _, done := c.OnFrame(dataFrame(srvTID, 1, file[:blksize])); done {
		t.Fatalf("block 1 should not finish")
	}
	// a stray DATA from a different TID -> ERROR(5) to that stray sender.
	stray, done := c.OnFrame(dataFrame(srvTID+7, 2, file[blksize:]))
	if done {
		t.Fatalf("a stray-TID DATA must not finish the transfer")
	}
	if stray == nil {
		t.Fatalf("a stray-TID DATA should produce an ERROR(5) reply")
	}
	code, _, err := tftp.ParseError(udpPayloadOf(t, stray))
	if err != nil || code != tftp.ErrUnknownTID {
		t.Fatalf("stray reply should be ERROR(5), got code %d err %v", code, err)
	}
}

// TestClientTimeoutSAS confirms OnTimeout in the transfer phase retransmits the
// last ACK only (the Sorcerer's-Apprentice fix), never the RRQ.
func TestClientTimeoutSAS(t *testing.T) {
	const blksize = 512
	file := makeFile(blksize * 2)
	c := New(cfg(blksize))
	c.First()
	c.OnFrame(arpReply())
	c.OnFrame(dataFrame(srvTID, 1, file[:blksize])) // ACK 1

	rt := c.OnTimeout()
	if rt == nil {
		t.Fatalf("OnTimeout after ACK 1 should retransmit the last ACK")
	}
	if tftp.Opcode(udpPayloadOf(t, rt)) != tftp.OpACK {
		t.Fatalf("SAS violation: timeout retransmitted opcode %d, want ACK", tftp.Opcode(udpPayloadOf(t, rt)))
	}
	if blk := ackBlock(t, rt); blk != 1 {
		t.Fatalf("SAS should retransmit ACK 1, got ACK %d", blk)
	}
}

// --- helpers ---------------------------------------------------------------

func parseTargetIP(arpReq []byte) (frame.IPv4, bool) {
	if len(arpReq) < 14+24+4 {
		return frame.IPv4{}, false
	}
	// EtherType ARP?
	if arpReq[12] != 0x08 || arpReq[13] != 0x06 {
		return frame.IPv4{}, false
	}
	var ip frame.IPv4
	copy(ip[:], arpReq[14+24:14+24+4]) // target protocol address
	return ip, true
}

func udpPayloadOf(t *testing.T, f []byte) []byte {
	t.Helper()
	u, ok := frame.ParseUDP(f)
	if !ok {
		t.Fatalf("frame is not UDP: %x", f)
	}
	return u.Payload
}

func ackBlock(t *testing.T, f []byte) uint16 {
	t.Helper()
	blk, err := tftp.ParseACK(udpPayloadOf(t, f))
	if err != nil {
		t.Fatalf("frame is not an ACK: %v", err)
	}
	return blk
}
