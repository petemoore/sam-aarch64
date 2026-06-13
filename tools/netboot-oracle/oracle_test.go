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
