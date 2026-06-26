package serve

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
)

var (
	srvMAC  = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	srvIP   = [4]byte{192, 0, 2, 1}
	cliMAC  = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	cliIP   = [4]byte{192, 0, 2, 44}
	cliTID  = uint16(30574)
	srvTID  = uint16(40136)
	cfgDemo = Config{ServerMAC: srvMAC, ServerIP: srvIP, ServerTID: srvTID}
)

func makeFile(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// newServer builds a demo server over a 2-file store.
func newServer(files map[string][]byte) *Responder {
	store := tftp.MapStore{}
	for name, b := range files {
		store[name] = uint64(len(b))
	}
	return New(cfgDemo, store, func(name string) tftp.Source {
		return tftp.ByteSource(files[name])
	})
}

func rrqFrame(name string, opts []tftp.Option) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: srvMAC, SrcMAC: cliMAC, SrcIP: cliIP, DstIP: srvIP,
		SrcPort: cliTID, DstPort: 69,
		Payload: tftp.BuildRRQ(name, "octet", opts),
	})
}

func ackFrame(block uint16) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: srvMAC, SrcMAC: cliMAC, SrcIP: cliIP, DstIP: srvIP,
		SrcPort: cliTID, DstPort: srvTID,
		Payload: tftp.BuildACK(block),
	})
}

func dataPayload(t *testing.T, f []byte) (block uint16, data []byte) {
	t.Helper()
	u, ok := frame.ParseUDP(f)
	if !ok {
		t.Fatalf("reply is not a UDP frame: %x", f)
	}
	blk, d, err := tftp.ParseDATA(u.Payload)
	if err != nil {
		t.Fatalf("reply is not a DATA packet: %v (%x)", err, u.Payload)
	}
	return blk, d
}

// TestBareRRQStreamsData is the headline i96 behaviour: a plain TFTP client's
// RRQ (no options) is answered with DATA block 1 directly (no OACK, RFC 2347),
// at the 512-byte default, then ACK/DATA to the short final block.
func TestBareRRQStreamsData(t *testing.T) {
	file := makeFile(512 + 200) // one full 512 block + a 200-byte tail
	s := newServer(map[string][]byte{"hello.txt": file})

	// Bare RRQ -> DATA block 1 (no OACK).
	reply := s.OnFrame(rrqFrame("hello.txt", nil))
	blk, d1 := dataPayload(t, reply)
	if blk != 1 {
		t.Fatalf("first reply block = %d, want 1 (DATA, not OACK)", blk)
	}
	if len(d1) != 512 {
		t.Fatalf("block 1 = %d bytes, want 512", len(d1))
	}
	if !bytes.Equal(d1, file[:512]) {
		t.Fatalf("block 1 bytes mismatch")
	}

	// ACK 1 -> DATA block 2 (short final, 200 bytes).
	reply = s.OnFrame(ackFrame(1))
	blk, d2 := dataPayload(t, reply)
	if blk != 2 {
		t.Fatalf("second reply block = %d, want 2", blk)
	}
	if len(d2) != 200 {
		t.Fatalf("final block = %d bytes, want 200", len(d2))
	}
	if !bytes.Equal(d2, file[512:]) {
		t.Fatalf("final block bytes mismatch")
	}

	// ACK 2 -> transfer complete, nothing sent.
	if fin := s.OnFrame(ackFrame(2)); fin != nil {
		t.Fatalf("ACK of the short final block should end the transfer, got %x", fin)
	}
}

// TestOptionedRRQSendsOACK confirms an RRQ that requests options is answered with
// an OACK (the curl/tsize path), then the OACK->ACK0->DATA1 cadence.
func TestOptionedRRQSendsOACK(t *testing.T) {
	file := makeFile(1024 + 50)
	s := newServer(map[string][]byte{"readme.txt": file})

	reply := s.OnFrame(rrqFrame("readme.txt", []tftp.Option{
		{Name: "blksize", Value: "1024"}, {Name: "tsize", Value: "0"},
	}))
	u, ok := frame.ParseUDP(reply)
	if !ok || tftp.Opcode(u.Payload) != tftp.OpOACK {
		t.Fatalf("optioned RRQ should get an OACK, got %x", reply)
	}
	opts, err := tftp.ParseOACK(u.Payload)
	if err != nil {
		t.Fatalf("parse OACK: %v", err)
	}
	if v, _ := tftp.OptionUint(opts, "tsize"); v != uint64(len(file)) {
		t.Fatalf("OACK tsize = %d, want %d", v, len(file))
	}
	if v, _ := tftp.OptionUint(opts, "blksize"); v != 1024 {
		t.Fatalf("OACK blksize = %d, want 1024", v)
	}

	// ACK 0 -> DATA block 1 (the OACK-path FirstData handoff).
	blk, d1 := dataPayload(t, s.OnFrame(ackFrame(0)))
	if blk != 1 || len(d1) != 1024 {
		t.Fatalf("after ACK0 expected DATA block 1 of 1024 bytes, got block %d len %d", blk, len(d1))
	}
}

// TestMissReturnsError confirms a request for an unknown name gets ERROR(1) and
// the server keeps serving.
func TestMissReturnsError(t *testing.T) {
	s := newServer(map[string][]byte{"hello.txt": makeFile(10)})
	reply := s.OnFrame(rrqFrame("nope.txt", nil))
	u, ok := frame.ParseUDP(reply)
	if !ok {
		t.Fatalf("miss reply is not UDP: %x", reply)
	}
	code, _, err := tftp.ParseError(u.Payload)
	if err != nil || code != tftp.ErrFileNotFound {
		t.Fatalf("miss should get ERROR(1), got %x (code %d, err %v)", reply, code, err)
	}
	// A subsequent valid RRQ still works.
	if got := s.OnFrame(rrqFrame("hello.txt", nil)); got == nil {
		t.Fatalf("server stopped serving after a miss")
	}
}

// TestARPAnswered confirms an ARP request for the SAM's IP is answered (so a
// plain client can resolve the SAM's MAC without DHCP).
func TestARPAnswered(t *testing.T) {
	s := newServer(map[string][]byte{"hello.txt": makeFile(10)})
	req := frame.BuildARPRequest(cliMAC, cliIP, srvIP)
	reply := s.OnFrame(req)
	if reply == nil {
		t.Fatalf("ARP request for the SAM's IP was not answered")
	}
	mac, ip, ok := frame.ParseARPReply(reply)
	if !ok || ip != srvIP || mac != srvMAC {
		t.Fatalf("ARP reply wrong: mac=%x ip=%v ok=%v", mac, ip, ok)
	}
	// An ARP request for a different IP is ignored.
	if r := s.OnFrame(frame.BuildARPRequest(cliMAC, cliIP, cliIP)); r != nil {
		t.Fatalf("answered an ARP request for a different IP: %x", r)
	}
}

// TestNonUDPIgnored confirms a frame that is neither ARP-for-us nor UDP is
// dropped silently.
func TestNonUDPIgnored(t *testing.T) {
	s := newServer(map[string][]byte{"hello.txt": makeFile(10)})
	junk := make([]byte, 60)
	junk[12], junk[13] = 0x86, 0xdd // EtherType IPv6 — not ARP, not IPv4/UDP
	if r := s.OnFrame(junk); r != nil {
		t.Fatalf("non-UDP frame produced a reply: %x", r)
	}
}

// wrqFrame builds a client WRQ frame for name with the given options (i121a).
func wrqFrame(name string, opts []tftp.Option) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: srvMAC, SrcMAC: cliMAC, SrcIP: cliIP, DstIP: srvIP,
		SrcPort: cliTID, DstPort: 69,
		Payload: tftp.BuildWRQ(name, "octet", opts),
	})
}

// TestBareWRQSendsACK0 confirms that a bare WRQ (no options) is answered with
// ACK block 0 (`00 04 00 00`) wrapped as a UDP frame back to the client TID.
// This is the i121a handshake: the server learns the client endpoint and
// signals readiness to receive (DATA reception is i121b, not here).
func TestBareWRQSendsACK0(t *testing.T) {
	s := newServer(map[string][]byte{"hello.txt": makeFile(10)})
	req := wrqFrame("upload.bin", nil)
	reply := s.OnFrame(req)
	if reply == nil {
		t.Fatalf("bare WRQ: no reply from server")
	}
	u, ok := frame.ParseUDP(reply)
	if !ok {
		t.Fatalf("bare WRQ reply is not a UDP frame: %x", reply)
	}
	if tftp.Opcode(u.Payload) != tftp.OpACK {
		t.Fatalf("bare WRQ reply opcode = %d, want ACK(%d)", tftp.Opcode(u.Payload), tftp.OpACK)
	}
	blk, err := tftp.ParseACK(u.Payload)
	if err != nil || blk != 0 {
		t.Fatalf("bare WRQ reply ACK block = %d (err %v), want 0", blk, err)
	}
	if u.DstPort != cliTID {
		t.Fatalf("bare WRQ reply dst port = %d, want client TID %d", u.DstPort, cliTID)
	}
	if u.SrcPort != srvTID {
		t.Fatalf("bare WRQ reply src port = %d, want server TID %d", u.SrcPort, srvTID)
	}
}

// TestOptionedWRQSendsOACK confirms that an optioned WRQ (blksize + tsize) is
// answered with an OACK echoing the accepted blksize and the client's tsize.
// This is the i121a OACK handshake path (RFC 2347). DATA reception is i121b.
func TestOptionedWRQSendsOACK(t *testing.T) {
	s := newServer(map[string][]byte{"hello.txt": makeFile(10)})
	req := wrqFrame("upload.bin", []tftp.Option{
		{Name: "blksize", Value: "512"},
		{Name: "tsize", Value: "4096"},
	})
	reply := s.OnFrame(req)
	if reply == nil {
		t.Fatalf("optioned WRQ: no reply from server")
	}
	u, ok := frame.ParseUDP(reply)
	if !ok {
		t.Fatalf("optioned WRQ reply is not a UDP frame: %x", reply)
	}
	if tftp.Opcode(u.Payload) != tftp.OpOACK {
		t.Fatalf("optioned WRQ reply opcode = %d, want OACK(%d)", tftp.Opcode(u.Payload), tftp.OpOACK)
	}
	opts, err := tftp.ParseOACK(u.Payload)
	if err != nil {
		t.Fatalf("parse OACK: %v", err)
	}
	if v, _ := tftp.OptionUint(opts, "blksize"); v != 512 {
		t.Fatalf("OACK blksize = %d, want 512", v)
	}
	if v, _ := tftp.OptionUint(opts, "tsize"); v != 4096 {
		t.Fatalf("OACK tsize = %d, want 4096", v)
	}
	if u.DstPort != cliTID {
		t.Fatalf("optioned WRQ reply dst port = %d, want client TID %d", u.DstPort, cliTID)
	}
	if u.SrcPort != srvTID {
		t.Fatalf("optioned WRQ reply src port = %d, want server TID %d", u.SrcPort, srvTID)
	}
}

// wrqDataFrame builds a client DATA frame for a WRQ upload (client TID -> server
// transfer TID), the frame the WRQ receive loop consumes.
func wrqDataFrame(block uint16, data []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: srvMAC, SrcMAC: cliMAC, SrcIP: cliIP, DstIP: srvIP,
		SrcPort: cliTID, DstPort: srvTID,
		Payload: tftp.BuildDATA(block, data),
	})
}

// pushImage drives a whole disk-record push through the Go authority's OnFrame:
// the bare-WRQ handshake for name, then every 512-byte DATA block of img (ending
// on a short final block, with an explicit empty final block when img is an exact
// multiple of the block size). Used to exercise the i121g claim model end-to-end
// in pure Go.
func pushImage(s *Responder, name string, img []byte) {
	const blksize = 512
	s.OnFrame(wrqFrame(name, nil))
	block := uint16(1)
	for off := 0; off < len(img); off += blksize {
		end := off + blksize
		if end > len(img) {
			end = len(img)
		}
		s.OnFrame(wrqDataFrame(block, img[off:end]))
		block++
	}
	if len(img)%blksize == 0 {
		s.OnFrame(wrqDataFrame(block, nil))
	}
}

// validRecordImage builds a RecordSize image with the BDOS stamp at +232 so it
// passes ValidateDiskRecord.
func validRecordImage() []byte {
	img := make([]byte, bdos.RecordSize)
	copy(img[bdos.BDOSStampOffset:bdos.BDOSStampOffset+4], []byte("BDOS"))
	return img
}

// newPushServer builds a disk-record-push server with the given free-record
// sequence (the records the free-record scan would return) under strategy. The
// lowest-free strategy returns the sequence head-first; pass it where the test
// asserts a lowest-first advance.
func newPushServer(free []int, strategy PlacementStrategy) *Responder {
	cfg := cfgDemo
	cfg.DiskRecordPush = true
	cfg.Strategy = strategy
	s := New(cfg, tftp.MapStore{}, func(string) tftp.Source { return tftp.ByteSource(nil) })
	s.SetFreeRecords(free)
	return s
}

// TestFlatFilePush pins the i121c FlatFile-class push in the Go authority: a WRQ
// filename with NO "trinity-sam-disks/" prefix classifies as FlatFile (the default
// class, design §6.5), so the body is flat-accumulated and — on the short final
// block — HSAVE'd into the claimed free record as a plain file (no 819,200-byte
// disk-record validation). The final reply is the final ACK (not ERROR), the store
// records one FlatStore (the claimed record + storage name + the pushed bytes), and
// the record is claimed exactly like the disk-record path.
func TestFlatFilePush(t *testing.T) {
	s := newPushServer([]int{5}, StrategyLowestFree)

	const blksize = 512
	data := []byte("hello flat world")
	name := "notes.dat"

	// Drive the whole exchange so the final reply frame is observed directly.
	s.OnFrame(wrqFrame(name, nil))
	var final []byte
	block := uint16(1)
	for off := 0; off < len(data); off += blksize {
		end := off + blksize
		if end > len(data) {
			end = len(data)
		}
		final = s.OnFrame(wrqDataFrame(block, data[off:end]))
		block++
	}
	if len(data)%blksize == 0 {
		final = s.OnFrame(wrqDataFrame(block, nil))
	}

	// The short final block is answered with an ACK, not an ERROR.
	u, ok := frame.ParseUDP(final)
	if !ok {
		t.Fatalf("final reply is not a UDP frame: %x", final)
	}
	if tftp.Opcode(u.Payload) != tftp.OpACK {
		t.Fatalf("final reply opcode = %d, want ACK(%d) — got %x", tftp.Opcode(u.Payload), tftp.OpACK, u.Payload)
	}

	// Exactly one FlatStore: claimed record 5, the Classify internal storage name
	// (no prefix to strip → "notes.dat"), and the pushed bytes verbatim.
	stores := s.FlatStores()
	if len(stores) != 1 {
		t.Fatalf("FlatStores() = %d, want 1", len(stores))
	}
	if stores[0].Record != 5 {
		t.Errorf("FlatStore record = %d, want 5 (the free record)", stores[0].Record)
	}
	if stores[0].Name != name {
		t.Errorf("FlatStore name = %q, want %q (Classify internal name, no prefix)", stores[0].Name, name)
	}
	if !bytes.Equal(stores[0].Data, data) {
		t.Errorf("FlatStore data = %x, want %x", stores[0].Data, data)
	}

	// The flat store claims its record (so the next push lands elsewhere), like the
	// disk-record path.
	claims := s.Claims()
	if len(claims) != 1 {
		t.Fatalf("Claims() = %d, want 1", len(claims))
	}
	if claims[0].Record != 5 {
		t.Errorf("claim record = %d, want 5", claims[0].Record)
	}
}

// TestFlatFilePushNoFree pins the no-free-record rejection for a FlatFile push: with
// no free record, the WRQ is rejected with ERROR(3) at the handshake and no flat
// store is recorded. Mirrors the disk-record no-free assertion.
func TestFlatFilePushNoFree(t *testing.T) {
	s := newPushServer(nil, StrategyLowestFree) // no free record

	reply := s.OnFrame(wrqFrame("notes.dat", nil))
	u, ok := frame.ParseUDP(reply)
	if !ok {
		t.Fatalf("no-free WRQ reply is not a UDP frame: %x", reply)
	}
	if tftp.Opcode(u.Payload) != tftp.OpERROR {
		t.Fatalf("no-free WRQ reply opcode = %d, want ERROR(%d) — got %x", tftp.Opcode(u.Payload), tftp.OpERROR, u.Payload)
	}
	if code, _, err := tftp.ParseError(u.Payload); err != nil || code != tftp.ErrDiskFull {
		t.Fatalf("no-free WRQ reply = ERROR code %d (err %v), want %d", code, err, tftp.ErrDiskFull)
	}
	if fs := s.FlatStores(); len(fs) != 0 {
		t.Fatalf("FlatStores() = %d, want 0 (nothing stored when no record is free)", len(fs))
	}
}

// TestDiskRecordPushClaimsAdvance pins the i121g claim model in the Go authority
// under the lowest-free strategy: two successive valid pushes claim DIFFERENT
// records (the first claim advances the free-record sequence) with the right
// filename-derived names, and a third push to an exhausted card is rejected with
// ERROR(3, "no free record"). This is the authority the Z80 bdos_claim_record is
// compared against. (The placement-strategy variants are pinned by
// TestDiskRecordPushStrategy.)
func TestDiskRecordPushClaimsAdvance(t *testing.T) {
	s := newPushServer([]int{3, 4}, StrategyLowestFree)

	pushImage(s, "trinity-sam-disks/firstdisk.mgt", validRecordImage())
	pushImage(s, "trinity-sam-disks/seconddiskimage.mgt", validRecordImage())

	claims := s.Claims()
	if len(claims) != 2 {
		t.Fatalf("Claims() = %d, want 2", len(claims))
	}
	if claims[0].Record != 3 || claims[1].Record != 4 {
		t.Errorf("claimed records %d, %d; want 3, 4 (the claim advanced)", claims[0].Record, claims[1].Record)
	}
	if claims[0].Name != "firstdisk" {
		t.Errorf("claim[0] name = %q, want %q (suffix dropped, prefix stripped)", claims[0].Name, "firstdisk")
	}
	if claims[1].Name != "seconddiskimage" {
		t.Errorf("claim[1] name = %q, want %q (16-char record-name field)", claims[1].Name, "seconddiskimage")
	}

	// The card is now exhausted (both free records claimed): the next WRQ is rejected.
	reply := s.OnFrame(wrqFrame("trinity-sam-disks/third.mgt", nil))
	u, ok := frame.ParseUDP(reply)
	if !ok {
		t.Fatalf("third-push reply is not a UDP frame: %x", reply)
	}
	if tftp.Opcode(u.Payload) != tftp.OpERROR {
		t.Fatalf("third-push reply opcode = %d, want ERROR(%d)", tftp.Opcode(u.Payload), tftp.OpERROR)
	}
}

// TestDiskRecordPushClaimOnlyOnValid pins the validation gate in the Go authority:
// an invalid push (wrong size) does NOT claim its record (no Claims entry, the
// free-record sequence does not advance), so a following valid push reuses the same
// record. Mirrors the Z80 claim-only-on-valid behaviour.
func TestDiskRecordPushClaimOnlyOnValid(t *testing.T) {
	s := newPushServer([]int{5}, StrategyLowestFree)

	// Invalid: one sector short of RecordSize → rejected, no claim.
	pushImage(s, "trinity-sam-disks/broken.mgt", validRecordImage()[:bdos.RecordSize-bdos.SectorSize])
	if c := s.Claims(); len(c) != 0 {
		t.Fatalf("invalid push recorded %d claims, want 0", len(c))
	}

	// Valid: reuses record 5 (never claimed by the bad push) and now claims it.
	pushImage(s, "trinity-sam-disks/good.mgt", validRecordImage())
	claims := s.Claims()
	if len(claims) != 1 || claims[0].Record != 5 {
		t.Fatalf("Claims() = %+v, want one claim of record 5 (reused)", claims)
	}
	if claims[0].Name != "good" {
		t.Errorf("claim name = %q, want %q", claims[0].Name, "good")
	}
}

// TestDiskRecordPushStrategy pins the i121h placement-strategy authority in the Go
// reference (the model the Z80 bdos_find_record_for_strategy mirrors): highest-free
// (the default) grows storage DOWN from the top; lowest-free grows up from the
// bottom; explicit places at a named record only when it is free. Two free records
// {3,4} make the high/low choice observable; each push claims and advances.
func TestDiskRecordPushStrategy(t *testing.T) {
	t.Run("highest-free (default) claims top-down 4 then 3", func(t *testing.T) {
		s := newPushServer([]int{3, 4}, StrategyHighestFree)
		pushImage(s, "trinity-sam-disks/first.mgt", validRecordImage())
		pushImage(s, "trinity-sam-disks/second.mgt", validRecordImage())
		claims := s.Claims()
		if len(claims) != 2 || claims[0].Record != 4 || claims[1].Record != 3 {
			t.Fatalf("highest-free claims = %+v, want records 4 then 3", claims)
		}
	})

	t.Run("lowest-free claims bottom-up 3 then 4", func(t *testing.T) {
		s := newPushServer([]int{3, 4}, StrategyLowestFree)
		pushImage(s, "trinity-sam-disks/first.mgt", validRecordImage())
		pushImage(s, "trinity-sam-disks/second.mgt", validRecordImage())
		claims := s.Claims()
		if len(claims) != 2 || claims[0].Record != 3 || claims[1].Record != 4 {
			t.Fatalf("lowest-free claims = %+v, want records 3 then 4", claims)
		}
	})

	t.Run("explicit free record places there", func(t *testing.T) {
		cfg := cfgDemo
		cfg.DiskRecordPush = true
		cfg.Strategy = StrategyExplicit
		cfg.ExplicitRecord = 4 // 4 is free; 3 is too, but explicit picks 4
		s := New(cfg, tftp.MapStore{}, func(string) tftp.Source { return tftp.ByteSource(nil) })
		s.SetFreeRecords([]int{3, 4})
		pushImage(s, "trinity-sam-disks/pinned.mgt", validRecordImage())
		claims := s.Claims()
		if len(claims) != 1 || claims[0].Record != 4 {
			t.Fatalf("explicit claims = %+v, want one claim of record 4", claims)
		}
	})

	t.Run("explicit named record rejects with ERROR(3)", func(t *testing.T) {
		cfg := cfgDemo
		cfg.DiskRecordPush = true
		cfg.Strategy = StrategyExplicit
		cfg.ExplicitRecord = 7 // 7 is NOT in the free set: already named
		s := New(cfg, tftp.MapStore{}, func(string) tftp.Source { return tftp.ByteSource(nil) })
		s.SetFreeRecords([]int{3, 4})
		reply := s.OnFrame(wrqFrame("trinity-sam-disks/pinned.mgt", nil))
		u, ok := frame.ParseUDP(reply)
		if !ok {
			t.Fatalf("explicit-taken reply is not a UDP frame: %x", reply)
		}
		if tftp.Opcode(u.Payload) != tftp.OpERROR {
			t.Fatalf("explicit-taken reply opcode = %d, want ERROR(%d)", tftp.Opcode(u.Payload), tftp.OpERROR)
		}
		if c := s.Claims(); len(c) != 0 {
			t.Fatalf("explicit-taken recorded %d claims, want 0", len(c))
		}
	})
}
