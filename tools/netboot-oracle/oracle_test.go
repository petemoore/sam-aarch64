// Package netboot_test runs the golden-vector oracle: it replays the masked
// frames extracted from the real Pi 400 netboot capture (the golden package)
// against the netboot-oracle packet builders and parsers, asserting they agree
// with the captured ground truth byte-for-byte. This is the only host-side
// verification possible before i80 (SimCoupé Trinity-net emulation) — it
// validates the protocol logic in isolation, not the Z80 execution or the
// ENC28J60 hardware (plan §6.1).
//
// The builders here are the Go authority for the forthcoming Z80 port: every
// assertion below pins a byte the Z80 code must reproduce.
package netboot_test

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/dhcp"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/server"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/smoke"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
)

// --- Framing layer (§1.2 offset contract) -------------------------------

// TestFrameOffsetsMatchCapture asserts the §1.2 offset constants locate the
// real fields in a captured frame. If these drift, the Z80 `packet` buffer
// accessors drift with them.
func TestFrameOffsetsMatchCapture(t *testing.T) {
	f := golden.TFTPRrqRoot1024
	u, ok := frame.ParseUDP(f)
	if !ok {
		t.Fatal("ParseUDP rejected a valid RRQ frame")
	}
	if u.DstMAC != mask.ServerMAC {
		t.Errorf("dst MAC = %x, want server placeholder %x", u.DstMAC, mask.ServerMAC)
	}
	if u.SrcMAC != mask.ClientMAC {
		t.Errorf("src MAC = %x, want client placeholder %x", u.SrcMAC, mask.ClientMAC)
	}
	if u.SrcIP != mask.ClientIP || u.DstIP != mask.ServerIP {
		t.Errorf("IPs = %v->%v, want %v->%v", u.SrcIP, u.DstIP, mask.ClientIP, mask.ServerIP)
	}
	if u.DstPort != 69 {
		t.Errorf("dst port = %d, want 69 (TFTP)", u.DstPort)
	}
	if tftp.Opcode(u.Payload) != tftp.OpRRQ {
		t.Errorf("payload opcode = %d, want RRQ", tftp.Opcode(u.Payload))
	}
}

// TestBuildUDPFrameRoundTrips asserts BuildUDPFrame (the Z80 build_udp_frame
// authority, §5.1) re-creates a captured frame's framing exactly when given the
// same fields and payload. This is the headline §8.2 increment: the fresh-frame
// primitive the trinload stack lacks, verified against ground truth.
func TestBuildUDPFrameRoundTrips(t *testing.T) {
	orig := golden.TFTPRrqRoot1024
	u, _ := frame.ParseUDP(orig)

	built := frame.BuildUDPFrame(frame.UDP{
		DstMAC:  u.DstMAC,
		SrcMAC:  u.SrcMAC,
		SrcIP:   u.SrcIP,
		DstIP:   u.DstIP,
		SrcPort: u.SrcPort,
		DstPort: u.DstPort,
		Payload: u.Payload,
	})

	// The captured frame has TTL/identification/flags the builder fixes to its
	// own canonical values, so compare the fields the builder owns rather than
	// the whole frame: L2 addresses, L3 addresses+proto, L4 ports+len, payload,
	// and a self-consistent IP header checksum.
	if !bytes.Equal(built[0:14], orig[0:14]) {
		t.Errorf("ethernet header differs\n built %x\n  orig %x", built[0:14], orig[0:14])
	}
	bu, _ := frame.ParseUDP(built)
	if bu.SrcIP != u.SrcIP || bu.DstIP != u.DstIP {
		t.Errorf("built IPs %v->%v != orig %v->%v", bu.SrcIP, bu.DstIP, u.SrcIP, u.DstIP)
	}
	if bu.SrcPort != u.SrcPort || bu.DstPort != u.DstPort {
		t.Errorf("built ports %d->%d != orig %d->%d", bu.SrcPort, bu.DstPort, u.SrcPort, u.DstPort)
	}
	if !bytes.Equal(bu.Payload, u.Payload) {
		t.Errorf("built payload differs from orig")
	}
	if !ipChecksumValid(built[14:34]) {
		t.Errorf("built IP header checksum is not self-consistent")
	}
	if built[frame.OffIPProto] != frame.ProtoUDP {
		t.Errorf("built IP proto = %#x, want UDP", built[frame.OffIPProto])
	}
}

// TestBuildARPRequest asserts BuildARPRequest (the Z80 build_arp_request
// authority, §5.1) constructs a structurally-correct RFC 826 Ethernet ARP
// request: broadcast L2 destination, ARP EtherType, and the 28-byte payload
// with our sender MAC/IP, a zeroed target MAC, and the resolved target IP.
// (No ARP frame is in the Pi netboot capture — the Pi's RRQ presupposes the
// client already knows the server MAC; this pins the on-wire structure the Z80
// must emit.)
func TestBuildARPRequest(t *testing.T) {
	srcMAC := frame.MAC{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}
	srcIP := frame.IPv4{192, 168, 50, 1}
	targetIP := frame.IPv4{192, 168, 50, 10}

	f := frame.BuildARPRequest(srcMAC, srcIP, targetIP)
	if len(f) != frame.ARPFrameLen {
		t.Fatalf("ARP frame len = %d, want %d", len(f), frame.ARPFrameLen)
	}

	// Ethernet header: broadcast dst, our src, ARP EtherType.
	if !bytes.Equal(f[frame.OffDstMAC:frame.OffDstMAC+6], frame.BroadcastMAC[:]) {
		t.Errorf("dst MAC = %x, want broadcast", f[0:6])
	}
	if !bytes.Equal(f[frame.OffSrcMAC:frame.OffSrcMAC+6], srcMAC[:]) {
		t.Errorf("src MAC = %x, want %x", f[6:12], srcMAC)
	}
	if et := uint16(f[frame.OffEtherType])<<8 | uint16(f[frame.OffEtherType+1]); et != frame.EtherTypeARP {
		t.Errorf("EtherType = %#x, want ARP %#x", et, frame.EtherTypeARP)
	}

	// ARP payload at offset 14.
	p := f[frame.EthHeaderLen:]
	if htype := uint16(p[0])<<8 | uint16(p[1]); htype != frame.ARPHTypeEthernet {
		t.Errorf("HTYPE = %d, want Ethernet", htype)
	}
	if ptype := uint16(p[2])<<8 | uint16(p[3]); ptype != frame.ARPPTypeIPv4 {
		t.Errorf("PTYPE = %#x, want IPv4", ptype)
	}
	if p[4] != frame.ARPHLen || p[5] != frame.ARPPLen {
		t.Errorf("HLEN/PLEN = %d/%d, want %d/%d", p[4], p[5], frame.ARPHLen, frame.ARPPLen)
	}
	if oper := uint16(p[6])<<8 | uint16(p[7]); oper != frame.ARPOpRequest {
		t.Errorf("OPER = %d, want request", oper)
	}
	if !bytes.Equal(p[8:14], srcMAC[:]) {
		t.Errorf("sender MAC = %x, want %x", p[8:14], srcMAC)
	}
	if !bytes.Equal(p[14:18], srcIP[:]) {
		t.Errorf("sender IP = %x, want %x", p[14:18], srcIP)
	}
	if !bytes.Equal(p[18:24], []byte{0, 0, 0, 0, 0, 0}) {
		t.Errorf("target MAC = %x, want zero", p[18:24])
	}
	if !bytes.Equal(p[24:28], targetIP[:]) {
		t.Errorf("target IP = %x, want %x", p[24:28], targetIP)
	}
}

// ipChecksumValid reports whether a 20-byte IP header's checksum verifies
// (the one's-complement sum of all words is 0xffff).
func ipChecksumValid(hdr []byte) bool {
	var sum uint32
	for i := 0; i+1 < len(hdr); i += 2 {
		sum += uint32(hdr[i])<<8 | uint32(hdr[i+1])
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return sum == 0xffff
}

// --- DHCP (i86 responder) -----------------------------------------------

// TestDHCPOfferParse confirms the captured PXE OFFER carries exactly the fields
// the oracle note §1 says the SAM must reproduce, including the fixed 32-byte
// option-43 "Raspberry Pi Boot" blob byte-for-byte.
func TestDHCPOfferParse(t *testing.T) {
	u, ok := frame.ParseUDP(golden.DHCPOfferPXE)
	if !ok {
		t.Fatal("OFFER frame is not IPv4/UDP")
	}
	if u.SrcPort != 67 || u.DstPort != 68 {
		t.Errorf("OFFER ports %d->%d, want 67->68", u.SrcPort, u.DstPort)
	}
	msg, err := dhcp.Parse(u.Payload)
	if err != nil {
		t.Fatalf("parse OFFER: %v", err)
	}
	if msg.MsgType() != dhcp.MsgOffer {
		t.Errorf("msg type = %d, want OFFER(2)", msg.MsgType())
	}
	if o := msg.Option(dhcp.OptVendorClass); o == nil || !bytes.Equal(o.Value, dhcp.PXEClient) {
		t.Errorf("opt 60 = %v, want PXEClient", o)
	}
	o43 := msg.Option(dhcp.OptVendorEncap)
	if o43 == nil {
		t.Fatal("OFFER missing option 43")
	}
	if !bytes.Equal(o43.Value, dhcp.Option43RaspberryPiBoot) {
		t.Errorf("option 43 blob mismatch\n got %x\nwant %x", o43.Value, dhcp.Option43RaspberryPiBoot)
	}
	if msg.Option(dhcp.OptClientUUID) == nil {
		t.Error("OFFER missing echoed client UUID (opt 97)")
	}
}

// TestBuildReplyMatchesTemplate asserts the i86 BuildReply emits an OFFER whose
// decoded fields match the oracle §1 template: message type, server-id/router/
// siaddr = the SAM, the lease block, subnet/broadcast, PXEClient, the echoed
// UUID, and the exact option-43 blob. (The capture's OFFER and ACK come from
// two cooperating servers; the SAM merges both roles, so the comparison is
// field-wise against the template, not byte-wise against one captured frame.)
func TestBuildReplyMatchesTemplate(t *testing.T) {
	// Echoed request fields, taken from the captured DISCOVER.
	du, _ := frame.ParseUDP(golden.DHCPDiscover)
	req, err := dhcp.Parse(du.Payload)
	if err != nil {
		t.Fatalf("parse DISCOVER: %v", err)
	}

	var uuid []byte
	if o := req.Option(dhcp.OptClientUUID); o != nil {
		uuid = o.Value
	}
	p := dhcp.ReplyParams{
		MsgType:    dhcp.MsgOffer,
		XID:        req.XID,
		YIAddr:     mask.ClientIP,
		ServerIP:   mask.ServerIP,
		SubnetMask: [4]byte{255, 255, 255, 0},
		Broadcast:  mask.Broadcast,
		LeaseSecs:  7200, T1Secs: 3600, T2Secs: 6300,
		CHAddr:     req.CHAddr,
		ClientUUID: uuid,
		Flags:      0x8000,
	}
	body := dhcp.BuildReply(p)
	out, err := dhcp.Parse(body)
	if err != nil {
		t.Fatalf("parse built reply: %v", err)
	}
	if out.Op != dhcp.OpReply {
		t.Errorf("op = %d, want BOOTREPLY", out.Op)
	}
	if out.MsgType() != dhcp.MsgOffer {
		t.Errorf("msg type = %d, want OFFER", out.MsgType())
	}
	if out.YIAddr != mask.ClientIP {
		t.Errorf("yiaddr = %v, want %v", out.YIAddr, mask.ClientIP)
	}
	if out.SIAddr != mask.ServerIP {
		t.Errorf("siaddr (next-server) = %v, want SAM %v", out.SIAddr, mask.ServerIP)
	}
	if o := out.Option(dhcp.OptServerID); o == nil || !bytes.Equal(o.Value, mask.ServerIP[:]) {
		t.Errorf("opt 54 server-id != SAM IP")
	}
	if o := out.Option(dhcp.OptRouter); o == nil || !bytes.Equal(o.Value, mask.ServerIP[:]) {
		t.Errorf("opt 3 router != SAM IP")
	}
	if o := out.Option(dhcp.OptVendorEncap); o == nil || !bytes.Equal(o.Value, dhcp.Option43RaspberryPiBoot) {
		t.Errorf("opt 43 blob not reproduced exactly")
	}
	if o := out.Option(dhcp.OptClientUUID); o == nil || !bytes.Equal(o.Value, uuid) {
		t.Errorf("opt 97 UUID not echoed verbatim")
	}
	if msg := out.Option(66); msg != nil {
		t.Errorf("opt 66 present, must be omitted (Pi requests own filenames)")
	}
	// The whole reply must wrap into a UDP 67->68 frame cleanly.
	rf := frame.BuildUDPFrame(frame.UDP{
		DstMAC: frame.BroadcastMAC, SrcMAC: mask.ServerMAC,
		SrcIP: mask.ServerIP, DstIP: frame.BroadcastIP,
		SrcPort: 67, DstPort: 68, Payload: body,
	})
	if ru, ok := frame.ParseUDP(rf); !ok || ru.SrcPort != 67 || ru.DstPort != 68 {
		t.Errorf("reply frame did not wrap as UDP 67->68")
	}
}

// TestResponderDORA drives the i86 DHCP responder loop authority through a full
// DORA cycle with the captured DISCOVER + REQUEST: a DISCOVER yields an OFFER, a
// REQUEST yields an ACK, both addressed to the same pooled yiaddr (lease keyed
// by client MAC), each wrapped as a UDP 67->68 broadcast. This is the Go
// reference the Z80 dhcp_loop.asm dispatch + pool logic ports (plan §3.1).
func TestResponderDORA(t *testing.T) {
	r := dhcp.NewResponder(
		mask.ServerMAC, mask.ServerIP,
		[4]byte{255, 255, 255, 0}, mask.Broadcast,
		[4]byte{192, 0, 2, 100}, 8, // pool 192.0.2.100 .. .107
		7200, 3600, 6300,
	)

	// DISCOVER -> OFFER.
	offerFrame := r.OnRequest(golden.DHCPDiscover)
	if offerFrame == nil {
		t.Fatal("responder ignored a DISCOVER")
	}
	ou, ok := frame.ParseUDP(offerFrame)
	if !ok || ou.SrcPort != 67 || ou.DstPort != 68 {
		t.Fatalf("OFFER not UDP 67->68 (ok=%v src=%d dst=%d)", ok, ou.SrcPort, ou.DstPort)
	}
	if !bytes.Equal(offerFrame[0:6], frame.BroadcastMAC[:]) {
		t.Errorf("OFFER not L2-broadcast: dst MAC %x", offerFrame[0:6])
	}
	offer, err := dhcp.Parse(ou.Payload)
	if err != nil {
		t.Fatalf("parse OFFER body: %v", err)
	}
	if offer.MsgType() != dhcp.MsgOffer {
		t.Errorf("DISCOVER reply msg type = %d, want OFFER", offer.MsgType())
	}
	wantIP := [4]byte{192, 0, 2, 100}
	if offer.YIAddr != wantIP {
		t.Errorf("OFFER yiaddr = %v, want first pool addr %v", offer.YIAddr, wantIP)
	}

	// REQUEST -> ACK, same address (same client MAC).
	ackFrame := r.OnRequest(golden.DHCPRequest)
	if ackFrame == nil {
		t.Fatal("responder ignored a REQUEST")
	}
	au, _ := frame.ParseUDP(ackFrame)
	ack, err := dhcp.Parse(au.Payload)
	if err != nil {
		t.Fatalf("parse ACK body: %v", err)
	}
	if ack.MsgType() != dhcp.MsgACK {
		t.Errorf("REQUEST reply msg type = %d, want ACK", ack.MsgType())
	}
	if ack.YIAddr != offer.YIAddr {
		t.Errorf("ACK yiaddr %v != OFFER yiaddr %v (lease not stable per MAC)", ack.YIAddr, offer.YIAddr)
	}

	// A non-DHCP frame is ignored (keep serving).
	if r.OnRequest(golden.TFTPRrqRoot1024) != nil {
		t.Error("responder replied to a non-DHCP frame")
	}
}

// --- TFTP (i82 client + i83 server) -------------------------------------

// TestRRQBuilderMatchesClientOptionSet asserts the i82 client's RRQ builder
// emits the settled option string (plan §2; research note §5.7) and that a
// captured RRQ parses to the expected fields.
func TestRRQBuilderMatchesClientOptionSet(t *testing.T) {
	rrq := tftp.BuildRRQ("spectrum4.img", "octet", tftp.ClientOptionSet)
	got, err := tftp.ParseRequest(rrq)
	if err != nil {
		t.Fatalf("parse built RRQ: %v", err)
	}
	if got.Filename != "spectrum4.img" || got.Mode != "octet" {
		t.Errorf("RRQ filename/mode = %q/%q", got.Filename, got.Mode)
	}
	for _, want := range []tftp.Option{
		{Name: "blksize", Value: "1428"},
		{Name: "tsize", Value: "0"},
		{Name: "timeout", Value: "2"},
		{Name: "windowsize", Value: "4"},
	} {
		if v, ok := got.Option(want.Name); !ok || v != want.Value {
			t.Errorf("RRQ option %s = %q (ok=%v), want %q", want.Name, v, ok, want.Value)
		}
	}

	// A captured RRQ must parse to octet/tsize=0 with a 1024 or 1468 blksize.
	for _, f := range [][]byte{golden.TFTPRrqRoot1024, golden.TFTPRrqRoot1468} {
		u, _ := frame.ParseUDP(f)
		r, err := tftp.ParseRequest(u.Payload)
		if err != nil {
			t.Fatalf("parse captured RRQ: %v", err)
		}
		if r.Mode != "octet" {
			t.Errorf("captured RRQ mode = %q, want octet", r.Mode)
		}
		if v, _ := r.Option("tsize"); v != "0" {
			t.Errorf("captured RRQ tsize = %q, want 0", v)
		}
		if v, _ := r.Option("blksize"); v != "1024" && v != "1468" {
			t.Errorf("captured RRQ blksize = %q, want 1024 or 1468", v)
		}
	}
}

// TestRRQBuilderByteExact asserts the RRQ wire bytes the builder produces for
// the Pi's captured option set match the captured RRQ payload exactly (filename
// + mode + the tsize/blksize option pairs the Pi sent). This pins the on-wire
// byte order the Z80 must emit.
func TestRRQBuilderByteExact(t *testing.T) {
	u, _ := frame.ParseUDP(golden.TFTPRrqRoot1024)
	captured, err := tftp.ParseRequest(u.Payload)
	if err != nil {
		t.Fatalf("parse captured RRQ: %v", err)
	}
	rebuilt := tftp.BuildRRQ(captured.Filename, captured.Mode, captured.Options)
	if !bytes.Equal(rebuilt, u.Payload) {
		t.Errorf("rebuilt RRQ != captured\n got %x\nwant %x", rebuilt, u.Payload)
	}
}

// TestOACKParse asserts the i82 client parses a captured OACK into the right
// negotiated values (oracle §2: tsize=<actual size>, echoed blksize).
func TestOACKParse(t *testing.T) {
	u, _ := frame.ParseUDP(golden.TFTPOack)
	opts, err := tftp.ParseOACK(u.Payload)
	if err != nil {
		t.Fatalf("parse OACK: %v", err)
	}
	bs, ok := tftp.OptionUint(opts, "blksize")
	if !ok || bs != 1024 {
		t.Errorf("OACK blksize = %d (ok=%v), want 1024", bs, ok)
	}
	ts, ok := tftp.OptionUint(opts, "tsize")
	if !ok {
		t.Fatal("OACK missing tsize")
	}
	if ts == 0 {
		t.Error("OACK tsize is 0; server must report the real size")
	}
}

// TestServerOACKByteExact asserts the i83 server's OACK builder reproduces the
// captured server OACK byte-for-byte given the same negotiated values.
func TestServerOACKByteExact(t *testing.T) {
	u, _ := frame.ParseUDP(golden.TFTPOack)
	captured, _ := tftp.ParseOACK(u.Payload)
	rebuilt := tftp.BuildOACK(captured)
	if !bytes.Equal(rebuilt, u.Payload) {
		t.Errorf("rebuilt OACK != captured\n got %x\nwant %x", rebuilt, u.Payload)
	}
}

// TestErrorParse asserts the captured "file not found" ERROR decodes to code 1,
// and that the server's ERROR builder reproduces the code+opcode structure.
func TestErrorParse(t *testing.T) {
	u, _ := frame.ParseUDP(golden.TFTPErrorNotFound)
	code, msg, err := tftp.ParseError(u.Payload)
	if err != nil {
		t.Fatalf("parse ERROR: %v", err)
	}
	if code != tftp.ErrFileNotFound {
		t.Errorf("error code = %d, want 1 (file not found)", code)
	}
	if msg == "" {
		t.Error("ERROR message is empty")
	}
	rebuilt := tftp.BuildError(code, msg)
	if !bytes.Equal(rebuilt[:4], u.Payload[:4]) {
		t.Errorf("rebuilt ERROR header != captured")
	}
}

// TestDataParse asserts a captured DATA block decodes (block 1, blksize bytes).
func TestDataParse(t *testing.T) {
	u, _ := frame.ParseUDP(golden.TFTPData)
	blk, data, err := tftp.ParseDATA(u.Payload)
	if err != nil {
		t.Fatalf("parse DATA: %v", err)
	}
	if blk != 1 {
		t.Errorf("DATA block = %d, want 1", blk)
	}
	if len(data) != 1024 {
		t.Errorf("DATA payload = %d bytes, want 1024 (negotiated blksize)", len(data))
	}
}

// TestServerResolveBehaviour pins the i83 server's mandatory resolve rules
// (oracle §2-§3): serve a flat-root hit via OACK, 404 a serial-subdir prefix,
// ERROR(1) every miss and keep serving.
func TestServerResolveBehaviour(t *testing.T) {
	store := tftp.MapStore{
		"config.txt":  1591,
		"start4.elf":  2250656,
		"kernel8.img": 1000,
	}

	// A serial-subdir RRQ from the capture must 404.
	su, _ := frame.ParseUDP(golden.TFTPRrqSerial)
	sreq, _ := tftp.ParseRequest(su.Payload)
	if act, _ := tftp.Resolve(store, sreq.Filename); act != tftp.ActionError404 {
		t.Errorf("serial-subdir %q resolved to %v, want ERROR404", sreq.Filename, act)
	}

	// A flat-root hit serves via OACK with the real size.
	if act, size := tftp.Resolve(store, "config.txt"); act != tftp.ActionOACK || size != 1591 {
		t.Errorf("config.txt resolved to (%v,%d), want (OACK,1591)", act, size)
	}

	// Misses ERROR(1) — and the resolver is pure, so the server stays alive.
	for _, miss := range []string{"recovery.elf", "pieeprom.sig", "dt-blob.bin", "armstub8-gic.bin"} {
		if act, _ := tftp.Resolve(store, miss); act != tftp.ActionError404 {
			t.Errorf("miss %q resolved to %v, want ERROR404", miss, act)
		}
	}
}

// TestAcceptedBlksize pins the server's blksize negotiation (1024 and 1468
// accepted; out-of-range falls back to the RFC 1350 default).
func TestAcceptedBlksize(t *testing.T) {
	cases := map[uint64]uint64{1024: 1024, 1468: 1468, 4: 512, 9000: 512, 512: 512}
	for in, want := range cases {
		if got := tftp.AcceptedBlksize(in); got != want {
			t.Errorf("AcceptedBlksize(%d) = %d, want %d", in, got, want)
		}
	}
}

// --- Transfer-loop state machines (i82 client / i83 server, plan §6.1) ---

// TestClientTransferLoop drives the i82 client receive model with the captured
// DATA block 1 then a synthesised short final block, asserting block numbering,
// ACK emission, and short-final-block termination (plan §2 steps 3-4).
func TestClientTransferLoop(t *testing.T) {
	u, _ := frame.ParseUDP(golden.TFTPData)
	const serverTID = 4242
	c := tftp.NewClientXfer(1024, serverTID)

	// Block 1: the captured full block.
	act := c.OnData(serverTID, u.Payload)
	if blk, _ := tftp.ParseACK(act.Reply); blk != 1 {
		t.Errorf("block-1 ACK = %d, want 1", blk)
	}
	if act.Done {
		t.Error("transfer ended on a full block")
	}

	// A wrong-TID DATA must be rejected with ERROR(5), not ACKed.
	stray := c.OnData(serverTID+1, tftp.BuildDATA(2, make([]byte, 1024)))
	if code, _, _ := tftp.ParseError(stray.Reply); code != tftp.ErrUnknownTID {
		t.Errorf("stray-TID reply code = %d, want 5 (unknown TID)", code)
	}

	// Block 2: a short final block ends the transfer.
	final := c.OnData(serverTID, tftp.BuildDATA(2, []byte("tail")))
	if blk, _ := tftp.ParseACK(final.Reply); blk != 2 {
		t.Errorf("block-2 ACK = %d, want 2", blk)
	}
	if !final.Done || !c.Done() {
		t.Error("short final block did not end the transfer")
	}

	_, data, _ := tftp.ParseDATA(u.Payload)
	if len(c.Bytes()) != len(data)+4 {
		t.Errorf("accumulated %d bytes, want %d", len(c.Bytes()), len(data)+4)
	}
}

// TestClientSorcerersApprentice asserts the SAS fix: a timeout retransmits the
// last ACK only, never the RRQ (research note §1.7).
func TestClientSorcerersApprentice(t *testing.T) {
	c := tftp.NewClientXfer(512, 4242)
	if c.OnTimeout() != nil {
		t.Error("timeout before any ACK should retransmit nothing (caller re-sends RRQ)")
	}
	c.OnData(4242, tftp.BuildDATA(1, make([]byte, 512))) // ACK block 1
	rt := c.OnTimeout()
	if blk, err := tftp.ParseACK(rt); err != nil || blk != 1 {
		t.Errorf("timeout retransmit = %x (blk %d), want ACK block 1", rt, blk)
	}
	if tftp.Opcode(rt) != tftp.OpACK {
		t.Errorf("SAS violation: timeout retransmitted opcode %d, want ACK (never RRQ)", tftp.Opcode(rt))
	}
}

// TestServerTransferLoop drives the i83 server send model over a 3.5-block file
// at blksize 1024, asserting block numbering, the streamed read, and the
// short-final-block termination (plan §3 steps 4-5; oracle §2).
func TestServerTransferLoop(t *testing.T) {
	const blksize = 1024
	file := make([]byte, 3*blksize+200) // 3 full blocks + a 200-byte tail
	for i := range file {
		file[i] = byte(i)
	}
	s := tftp.NewServerXfer(tftp.ByteSource(file), blksize)

	for wantBlk := uint16(1); ; wantBlk++ {
		data := s.NextData()
		if data == nil {
			t.Fatalf("server ran out of DATA before completing")
		}
		blk, payload, err := tftp.ParseDATA(data)
		if err != nil {
			t.Fatalf("server DATA parse: %v", err)
		}
		if blk != wantBlk {
			t.Errorf("server block = %d, want %d", blk, wantBlk)
		}
		// Verify the streamed bytes are the right slice of the file.
		off := (int(wantBlk) - 1) * blksize
		if !bytes.Equal(payload, file[off:off+len(payload)]) {
			t.Errorf("block %d payload != file slice", wantBlk)
		}
		if s.OnAck(blk) {
			if wantBlk != 4 || len(payload) != 200 {
				t.Errorf("transfer ended at block %d len %d, want block 4 len 200", wantBlk, len(payload))
			}
			break
		}
		if wantBlk > 10 {
			t.Fatal("server transfer did not terminate")
		}
	}
	if !s.Done() {
		t.Error("server transfer not marked done")
	}
}

// TestServerExactMultipleFinalBlock asserts the protocol's zero-length final
// block when the file size is an exact multiple of blksize (RFC 1350).
func TestServerExactMultipleFinalBlock(t *testing.T) {
	const blksize = 512
	file := make([]byte, 2*blksize) // exactly 2 full blocks
	s := tftp.NewServerXfer(tftp.ByteSource(file), blksize)

	d1 := s.NextData()
	b1, _, _ := tftp.ParseDATA(d1)
	if s.OnAck(b1) {
		t.Fatal("transfer ended after block 1 of an exact-multiple file")
	}
	d2 := s.NextData()
	b2, p2, _ := tftp.ParseDATA(d2)
	if len(p2) != blksize {
		t.Errorf("block 2 len = %d, want full %d", len(p2), blksize)
	}
	if s.OnAck(b2) {
		t.Fatal("exact-multiple file must send a zero-length final block, not end here")
	}
	d3 := s.NextData()
	b3, p3, _ := tftp.ParseDATA(d3)
	if len(p3) != 0 {
		t.Errorf("final block len = %d, want 0 (exact-multiple terminator)", len(p3))
	}
	if !s.OnAck(b3) {
		t.Error("zero-length final block did not end the transfer")
	}
}

// TestServerRetransmitOnTimeout asserts the server retransmits the last DATA
// (not the next) on an ACK timeout (RFC 1350 server recovery).
func TestServerRetransmitOnTimeout(t *testing.T) {
	s := tftp.NewServerXfer(tftp.ByteSource(make([]byte, 4096)), 1024)
	d1 := s.NextData()
	rt := s.OnTimeout()
	if !bytes.Equal(d1, rt) {
		t.Errorf("timeout retransmit != last DATA sent")
	}
	if blk, _, _ := tftp.ParseDATA(rt); blk != 1 {
		t.Errorf("retransmitted block = %d, want 1 (the unacked block)", blk)
	}
}

// TestServerLoopFrames drives the i83 server loop authority (the framed
// reply-driven state machine the Z80 tftp_server_loop.asm ports): a captured
// RRQ for a stored file yields an OACK frame back to the client TID; the
// subsequent DATA frames carry the streamed file at the negotiated blksize,
// ending on a short final block; a miss yields an ERROR(1) frame and serves
// nothing. The server TID + client endpoint come from the RRQ frame.
func TestServerLoopFrames(t *testing.T) {
	const blksize = 1024
	const serverTID = 40136 // an ephemeral source port for the transfer
	file := make([]byte, 2*blksize+300)
	for i := range file {
		file[i] = byte(i * 7)
	}
	store := tftp.MapStore{"config.txt": uint64(len(file))}
	sl := tftp.NewServerLoop(store, mask.ServerMAC, mask.ServerIP, serverTID)
	sl.SetSource(tftp.ByteSource(file))

	// RRQ (captured config.txt, blksize=1024) -> OACK frame to the client TID.
	oackFrame := sl.OnRRQ(golden.TFTPRrqRoot1024)
	if oackFrame == nil {
		t.Fatal("server loop ignored a valid RRQ")
	}
	ou, ok := frame.ParseUDP(oackFrame)
	if !ok || ou.SrcPort != serverTID || ou.DstPort != 30574 { // 30574 = captured client TID
		t.Fatalf("OACK frame ports src=%d dst=%d, want %d -> 30574", ou.SrcPort, ou.DstPort, serverTID)
	}
	opts, err := tftp.ParseOACK(ou.Payload)
	if err != nil {
		t.Fatalf("parse OACK: %v", err)
	}
	if bs, _ := tftp.OptionUint(opts, "blksize"); bs != blksize {
		t.Errorf("OACK blksize = %d, want %d", bs, blksize)
	}
	if ts, _ := tftp.OptionUint(opts, "tsize"); ts != uint64(len(file)) {
		t.Errorf("OACK tsize = %d, want %d (real file size)", ts, len(file))
	}

	// Client ACKs block 0 -> first DATA (block 1, full block).
	first := sl.FirstData()
	b1, p1, _ := tftp.ParseDATA(mustPayload(t, first))
	if b1 != 1 || len(p1) != blksize {
		t.Errorf("first DATA block=%d len=%d, want 1/%d", b1, len(p1), blksize)
	}
	if !bytes.Equal(p1, file[:blksize]) {
		t.Error("first DATA payload != file[0:blksize]")
	}

	// ACK 1 -> DATA 2 (full); ACK 2 -> DATA 3 (short, 300 bytes); ACK 3 -> done.
	ackFrame := func(block uint16) []byte {
		return frame.BuildUDPFrame(frame.UDP{
			DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
			SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
			SrcPort: 30574, DstPort: serverTID,
			Payload: tftp.BuildACK(block),
		})
	}
	d2 := sl.OnACK(ackFrame(1))
	b2, p2, _ := tftp.ParseDATA(mustPayload(t, d2))
	if b2 != 2 || len(p2) != blksize {
		t.Errorf("DATA 2 block=%d len=%d, want 2/%d", b2, len(p2), blksize)
	}
	d3 := sl.OnACK(ackFrame(2))
	b3, p3, _ := tftp.ParseDATA(mustPayload(t, d3))
	if b3 != 3 || len(p3) != 300 {
		t.Errorf("DATA 3 block=%d len=%d, want 3/300 (short final)", b3, len(p3))
	}
	if fin := sl.OnACK(ackFrame(3)); fin != nil {
		t.Errorf("ACK of the short final block should end the transfer, got a frame")
	}
	if !sl.Done() {
		t.Error("transfer not marked done after final ACK")
	}

	// A miss -> ERROR(1), no data served.
	miss := tftp.NewServerLoop(store, mask.ServerMAC, mask.ServerIP, serverTID)
	miss.SetSource(tftp.ByteSource(file))
	errFrame := miss.OnRRQ(rebuildRRQ("recovery.elf"))
	eu, _ := frame.ParseUDP(errFrame)
	if code, _, _ := tftp.ParseError(eu.Payload); code != tftp.ErrFileNotFound {
		t.Errorf("miss reply code = %d, want 1 (file not found)", code)
	}
	if miss.FirstData() != nil {
		t.Error("server served data for a missed file")
	}

	// A serial-subdir prefix -> ERROR(1) too (the Pi retries at root).
	serial := tftp.NewServerLoop(store, mask.ServerMAC, mask.ServerIP, serverTID)
	serial.SetSource(tftp.ByteSource(file))
	if ef := serial.OnRRQ(golden.TFTPRrqSerial); ef == nil {
		t.Error("serial-subdir RRQ ignored")
	} else {
		su, _ := frame.ParseUDP(ef)
		if code, _, _ := tftp.ParseError(su.Payload); code != tftp.ErrFileNotFound {
			t.Errorf("serial-subdir reply code = %d, want 1", code)
		}
	}
}

// TestIntegratedServerDispatch drives the i95 integrated netboot server
// (server.Server.OnFrame, the combined dispatcher the Z80 netboot_server.asm
// ports): one frame-in/reply-out handler routes ARP / DHCP / TFTP. It runs a
// full netboot session — DISCOVER->OFFER, REQUEST->ACK, an ARP request,
// RRQ->OACK, ACK->DATA to the short final block — and asserts each reply matches
// the standalone sub-responder authority byte-for-byte, plus that a non-matching
// frame is ignored.
func TestIntegratedServerDispatch(t *testing.T) {
	const blksize = 1024
	const serverTID = 40136
	const clientTID = 30574 // the captured client RRQ source port
	file := make([]byte, 2*blksize+300)
	for i := range file {
		file[i] = byte(i * 7)
	}
	store := tftp.MapStore{"config.txt": uint64(len(file))}

	cfg := server.Config{
		ServerMAC: mask.ServerMAC, ServerIP: mask.ServerIP,
		Subnet: [4]byte{255, 255, 255, 0}, Broadcast: mask.Broadcast,
		PoolBase: [4]byte{192, 0, 2, 100}, PoolSize: 8,
		LeaseSecs: 7200, T1: 3600, T2: 6300, ServerTID: serverTID,
	}
	srv := server.New(cfg, store, func(string) tftp.Source { return tftp.ByteSource(file) })

	// Standalone references the dispatch must match byte-for-byte.
	refDHCP := dhcp.NewResponder(cfg.ServerMAC, cfg.ServerIP, cfg.Subnet, cfg.Broadcast,
		cfg.PoolBase, cfg.PoolSize, cfg.LeaseSecs, cfg.T1, cfg.T2)
	refARP := smoke.NewResponder(cfg.ServerMAC, cfg.ServerIP)
	refTFTP := tftp.NewServerLoop(store, cfg.ServerMAC, cfg.ServerIP, serverTID)
	refTFTP.SetSource(tftp.ByteSource(file))

	eq := func(label string, got, want []byte) {
		t.Helper()
		if !bytes.Equal(got, want) {
			t.Errorf("%s != standalone authority\n  dispatch %x\n  ref      %x", label, got, want)
		}
	}

	// 1. DHCP DISCOVER -> OFFER.
	eq("OFFER", srv.OnFrame(golden.DHCPDiscover), refDHCP.OnRequest(golden.DHCPDiscover))
	// 2. DHCP REQUEST -> ACK.
	eq("DHCP ACK", srv.OnFrame(golden.DHCPRequest), refDHCP.OnRequest(golden.DHCPRequest))
	// 3. An ARP request for the SAM's IP -> an ARP reply.
	arpReq := frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ServerIP)
	eq("ARP reply", srv.OnFrame(arpReq), refARP.OnFrame(arpReq))
	// 4. TFTP RRQ -> OACK (arms the transfer).
	eq("OACK", srv.OnFrame(golden.TFTPRrqRoot1024), refTFTP.OnRRQ(golden.TFTPRrqRoot1024))

	ack := func(block uint16) []byte {
		return frame.BuildUDPFrame(frame.UDP{
			DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
			SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
			SrcPort: clientTID, DstPort: serverTID,
			Payload: tftp.BuildACK(block),
		})
	}
	// 5. ACK 0 -> first DATA (block 1); the dispatch routes it to FirstData.
	eq("DATA 1", srv.OnFrame(ack(0)), refTFTP.FirstData())
	// 6. ACK 1 -> DATA 2 (full).
	eq("DATA 2", srv.OnFrame(ack(1)), refTFTP.OnACK(ack(1)))
	// 7. ACK 2 -> DATA 3 (short final, 300 bytes).
	eq("DATA 3", srv.OnFrame(ack(2)), refTFTP.OnACK(ack(2)))
	// 8. ACK 3 -> transfer complete, nothing sent.
	if fin := srv.OnFrame(ack(3)); fin != nil {
		t.Errorf("ACK of the short final block should end the transfer, got %x", fin)
	}
	_ = refTFTP.OnACK(ack(3)) // keep the reference in lockstep

	// 9. A non-matching frame (an ARP request for a different IP) is ignored.
	if r := srv.OnFrame(frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ClientIP)); r != nil {
		t.Errorf("dispatch replied to an ARP request for a different IP: %x", r)
	}
}

// TestIntegratedServerMiss confirms the integrated dispatch serves ERROR(1) on a
// TFTP miss and keeps going (no transfer armed).
func TestIntegratedServerMiss(t *testing.T) {
	store := tftp.MapStore{"config.txt": 100}
	cfg := server.Config{
		ServerMAC: mask.ServerMAC, ServerIP: mask.ServerIP,
		Subnet: [4]byte{255, 255, 255, 0}, Broadcast: mask.Broadcast,
		PoolBase: [4]byte{192, 0, 2, 100}, PoolSize: 8, ServerTID: 40136,
	}
	srv := server.New(cfg, store, func(string) tftp.Source { return tftp.ByteSource(make([]byte, 100)) })

	errFrame := srv.OnFrame(rebuildRRQ("recovery.elf"))
	if errFrame == nil {
		t.Fatal("dispatch ignored a TFTP miss")
	}
	eu, _ := frame.ParseUDP(errFrame)
	if code, _, _ := tftp.ParseError(eu.Payload); code != tftp.ErrFileNotFound {
		t.Errorf("miss reply code = %d, want 1 (file not found)", code)
	}
	// An ACK after a miss is not part of any transfer -> ignored.
	ack := frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
		SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
		SrcPort: 30574, DstPort: 40136,
		Payload: tftp.BuildACK(0),
	})
	if r := srv.OnFrame(ack); r != nil {
		t.Errorf("dispatch sent data after a miss: %x", r)
	}
}

// mustPayload extracts a UDP payload from a frame or fails the test.
func mustPayload(t *testing.T, f []byte) []byte {
	t.Helper()
	if f == nil {
		t.Fatal("expected a reply frame, got nil")
	}
	u, ok := frame.ParseUDP(f)
	if !ok {
		t.Fatal("reply frame did not parse as UDP")
	}
	return u.Payload
}

// rebuildRRQ builds a minimal RRQ frame (client -> server:69) for name, matching
// the captured client's option set, for the miss/serial cases.
func rebuildRRQ(name string) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
		SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
		SrcPort: 30574, DstPort: 69,
		Payload: tftp.BuildRRQ(name, "octet", []tftp.Option{{Name: "tsize", Value: "0"}, {Name: "blksize", Value: "1024"}}),
	})
}

// TestClientLoopFrames drives the i82 client receive loop authority (the framed
// DATA/ACK loop the Z80 tftp_client_loop.asm ports): the captured DATA block 1
// yields an ACK frame back to the server TID (learned from that first DATA),
// then a short final block ends the transfer; the SAS timeout retransmits the
// last ACK frame only.
func TestClientLoopFrames(t *testing.T) {
	const blksize = 1024
	clientTID := uint16(30574)
	cl := tftp.NewClientLoop(mask.ClientMAC, mask.ClientIP, clientTID, blksize)

	// Block 1: the captured DATA frame (server -> client). The loop learns the
	// server TID (its source port) and ACKs block 1 back to it.
	ackFrame := cl.OnDATA(golden.TFTPData)
	if ackFrame == nil {
		t.Fatal("client loop produced no ACK for DATA block 1")
	}
	au, ok := frame.ParseUDP(ackFrame)
	if !ok || au.SrcPort != clientTID {
		t.Fatalf("ACK src port = %d, want client TID %d", au.SrcPort, clientTID)
	}
	// The captured DATA's source port is the server TID; the ACK must go there.
	du, _ := frame.ParseUDP(golden.TFTPData)
	if au.DstPort != du.SrcPort {
		t.Errorf("ACK dst port = %d, want learned server TID %d", au.DstPort, du.SrcPort)
	}
	// The captured DATA is block 1; the loop (acked starts at 0) accepts it and
	// ACKs block 1.
	if blk, err := tftp.ParseACK(au.Payload); err != nil || blk != 1 {
		t.Errorf("ACK block = %d (err %v), want 1", blk, err)
	}

	// A stray DATA from a different server TID -> ERROR(5), not an ACK.
	stray := frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ClientMAC, SrcMAC: mask.ServerMAC,
		SrcIP: mask.ServerIP, DstIP: mask.ClientIP,
		SrcPort: du.SrcPort + 1, DstPort: clientTID,
		Payload: tftp.BuildDATA(99, make([]byte, blksize)),
	})
	sf := cl.OnDATA(stray)
	su, _ := frame.ParseUDP(sf)
	if code, _, _ := tftp.ParseError(su.Payload); code != tftp.ErrUnknownTID {
		t.Errorf("stray-TID reply code = %d, want 5 (unknown TID)", code)
	}

	// The SAS timeout retransmits the last ACK frame only (never an RRQ).
	rt := cl.OnTimeout()
	if rt == nil {
		t.Fatal("timeout after an ACK should retransmit the last ACK")
	}
	ru, _ := frame.ParseUDP(rt)
	if tftp.Opcode(ru.Payload) != tftp.OpACK {
		t.Errorf("SAS violation: timeout retransmitted opcode %d, want ACK", tftp.Opcode(ru.Payload))
	}
}

// --- Client originate front (i82 ARP-for-server + RRQ-send, plan §2 1-2) ----

// buildARPReply constructs an Ethernet ARP reply (RFC 826) from senderMAC/IP to
// the asker — the frame the netboot server (or its router) returns to the
// client's broadcast ARP request. It is the input the Z80 tftp_recv_arp parses.
func buildARPReply(senderMAC frame.MAC, senderIP frame.IPv4, targetMAC frame.MAC, targetIP frame.IPv4) []byte {
	f := make([]byte, frame.ARPFrameLen)
	copy(f[frame.OffDstMAC:], targetMAC[:]) // unicast to the asker
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
	copy(p[18:24], targetMAC[:])
	copy(p[24:28], targetIP[:])
	return f
}

// TestParseARPReply pins the ARP-reply parser the Z80 tftp_recv_arp ports: an
// ARP reply yields the sender's MAC + IP; a request, a non-ARP frame, and a
// too-short frame are rejected.
func TestParseARPReply(t *testing.T) {
	reply := buildARPReply(mask.ServerMAC, mask.ServerIP, mask.ClientMAC, mask.ClientIP)
	mac, ip, ok := frame.ParseARPReply(reply)
	if !ok {
		t.Fatal("ParseARPReply rejected a valid reply")
	}
	if mac != mask.ServerMAC {
		t.Errorf("sender MAC = %x, want %x", mac, mask.ServerMAC)
	}
	if ip != mask.ServerIP {
		t.Errorf("sender IP = %v, want %v", ip, mask.ServerIP)
	}

	// An ARP *request* (OPER=1) is not a reply.
	req := frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ServerIP)
	if _, _, ok := frame.ParseARPReply(req); ok {
		t.Error("ParseARPReply accepted an ARP request")
	}
	// A UDP frame is not ARP.
	if _, _, ok := frame.ParseARPReply(golden.TFTPData); ok {
		t.Error("ParseARPReply accepted a non-ARP frame")
	}
	// A truncated frame is rejected.
	if _, _, ok := frame.ParseARPReply(reply[:10]); ok {
		t.Error("ParseARPReply accepted a truncated frame")
	}
}

// TestBuildARPReply pins frame.BuildARPReply (the Z80 build_arp_reply primitive,
// used by the i94 smoke test): it must produce the same bytes as the established
// in-test reply builder, a unicast reply back to the asker announcing the
// sender's MAC for its IP.
func TestBuildARPReply(t *testing.T) {
	// BuildARPReply(srcMAC, dstMAC, srcIP, dstIP): the SAM answers the asker.
	got := frame.BuildARPReply(mask.ServerMAC, mask.ClientMAC, mask.ServerIP, mask.ClientIP)
	// The in-test helper takes (senderMAC, senderIP, targetMAC, targetIP).
	want := buildARPReply(mask.ServerMAC, mask.ServerIP, mask.ClientMAC, mask.ClientIP)
	if !bytes.Equal(got, want) {
		t.Errorf("BuildARPReply != reference\n  got  %x\n  want %x", got, want)
	}
	// It round-trips through ParseARPReply as the sender's MAC/IP.
	mac, ip, ok := frame.ParseARPReply(got)
	if !ok || mac != mask.ServerMAC || ip != mask.ServerIP {
		t.Errorf("BuildARPReply output does not parse back to the sender (mac=%x ip=%v ok=%v)", mac, ip, ok)
	}
}

// TestParseARPRequest pins the ARP-request parser the Z80 smoke test ports: an
// ARP request yields the asker's MAC/IP + the target IP; a reply, a non-ARP
// frame, and a too-short frame are rejected.
func TestParseARPRequest(t *testing.T) {
	req := frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ServerIP)
	r, ok := frame.ParseARPRequest(req)
	if !ok {
		t.Fatal("ParseARPRequest rejected a valid request")
	}
	if r.SenderMAC != mask.ClientMAC {
		t.Errorf("sender MAC = %x, want %x", r.SenderMAC, mask.ClientMAC)
	}
	if r.SenderIP != mask.ClientIP {
		t.Errorf("sender IP = %v, want %v", r.SenderIP, mask.ClientIP)
	}
	if r.TargetIP != mask.ServerIP {
		t.Errorf("target IP = %v, want %v", r.TargetIP, mask.ServerIP)
	}
	// An ARP *reply* (OPER=2) is not a request.
	reply := buildARPReply(mask.ServerMAC, mask.ServerIP, mask.ClientMAC, mask.ClientIP)
	if _, ok := frame.ParseARPRequest(reply); ok {
		t.Error("ParseARPRequest accepted an ARP reply")
	}
	// A UDP frame is not ARP.
	if _, ok := frame.ParseARPRequest(golden.TFTPData); ok {
		t.Error("ParseARPRequest accepted a non-ARP frame")
	}
	// A truncated frame is rejected.
	if _, ok := frame.ParseARPRequest(req[:10]); ok {
		t.Error("ParseARPRequest accepted a truncated frame")
	}
}

// TestSmokeResponder is the i94 bring-up authority check: smoke.Responder.OnFrame
// answers an ARP request for the SAM's IP with a unicast reply, and stays silent
// for anything else (a request for a different IP, a non-ARP frame).
func TestSmokeResponder(t *testing.T) {
	r := smoke.NewResponder(mask.ServerMAC, mask.ServerIP)

	// An ARP request for the SAM's IP is answered with the matching reply.
	req := frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ServerIP)
	reply := r.OnFrame(req)
	if reply == nil {
		t.Fatal("OnFrame ignored an ARP request for the SAM's IP")
	}
	want := frame.BuildARPReply(mask.ServerMAC, mask.ClientMAC, mask.ServerIP, mask.ClientIP)
	if !bytes.Equal(reply, want) {
		t.Errorf("OnFrame reply != BuildARPReply\n  got  %x\n  want %x", reply, want)
	}

	// An ARP request for a different IP is ignored.
	if r.OnFrame(frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ClientIP)) != nil {
		t.Error("OnFrame answered a request for a different IP")
	}
	// A non-ARP frame is ignored.
	if r.OnFrame(golden.DHCPDiscover) != nil {
		t.Error("OnFrame answered a non-ARP frame")
	}
}

// TestClientFrontOriginate is the headline i82 originate-front check: the client
// broadcasts an ARP request, learns the server MAC from the reply, and sends the
// RRQ to the learned MAC — the ARP request and the RRQ frame both byte-exact.
func TestClientFrontOriginate(t *testing.T) {
	const cliTID = 30574
	cf := tftp.NewClientFront(mask.ClientMAC, mask.ClientIP, mask.ServerIP, cliTID)

	// 1. The ARP request is the fresh-frame ARP primitive's output.
	arp := cf.ARPRequest()
	wantARP := frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ServerIP)
	if !bytes.Equal(arp, wantARP) {
		t.Errorf("ARP request != BuildARPRequest\n  got  %x\n  want %x", arp, wantARP)
	}

	// 2. A non-matching ARP reply (wrong IP) is ignored.
	otherIP := frame.IPv4{192, 0, 2, 99}
	if cf.OnARPReply(buildARPReply(mask.ServerMAC, otherIP, mask.ClientMAC, mask.ClientIP)) {
		t.Error("OnARPReply accepted a reply for the wrong IP")
	}
	if cf.GotMAC() {
		t.Error("GotMAC true after a non-matching reply")
	}

	// 3. The matching ARP reply learns the server MAC.
	if !cf.OnARPReply(buildARPReply(mask.ServerMAC, mask.ServerIP, mask.ClientMAC, mask.ClientIP)) {
		t.Fatal("OnARPReply rejected the matching reply")
	}
	if cf.ServerMAC() != mask.ServerMAC {
		t.Errorf("learned server MAC = %x, want %x", cf.ServerMAC(), mask.ServerMAC)
	}

	// 4. The RRQ frame is the RRQ payload wrapped UDP (client TID -> server :69).
	rrq := cf.RRQFrame("config.txt")
	wantRRQ := frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
		SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
		SrcPort: cliTID, DstPort: 69,
		Payload: tftp.BuildRRQ("config.txt", "octet", tftp.ClientOptionSet),
	})
	if !bytes.Equal(rrq, wantRRQ) {
		t.Errorf("RRQ frame != expected\n  got  %x\n  want %x", rrq, wantRRQ)
	}
	// And it parses as a UDP RRQ to port 69.
	u, ok := frame.ParseUDP(rrq)
	if !ok || u.DstPort != 69 || u.SrcPort != cliTID {
		t.Errorf("RRQ frame UDP = src %d dst %d, want src %d dst 69", u.SrcPort, u.DstPort, cliTID)
	}
	if tftp.Opcode(u.Payload) != tftp.OpRRQ {
		t.Errorf("RRQ frame opcode = %d, want RRQ", tftp.Opcode(u.Payload))
	}
}
