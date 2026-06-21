// netboot_serve_test.go — the i96 host-verification of the serve-files TFTP demo
// server. It runs the real composed state machine (src/netboot/netboot_serve.asm:
// the driver encdrv.asm + the host-verified build_udp_frame/build_arp_reply/
// tftp_build/tftp_parse primitives) under the flat-memory koron-go/z80 harness with
// the emulated Trinity (enc28j60.go) attached, and asserts that the served frames —
// an ARP reply, a bare-RRQ -> DATA-block-1 transfer (RFC 2347, no OACK), an
// optioned-RRQ -> OACK transfer, and a miss -> ERROR(1) — driven through the single
// serve_serve_once dispatcher match the Go authority serve.Responder.OnFrame
// byte-for-byte.
//
// This is the demo server made host-verifiable end-to-end by the i80 emulation. It
// is emulation verification, NOT hardware verification — the real ENC28J60 silicon
// and an end-to-end run on real hardware (with a stock tftp/curl client) stay gated
// on real Trinity (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/serve"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	srvDemoBinPath = "../../../build/netboot_serve.bin"
	srvDemoMapPath = "../../../build/netboot_serve.map"

	demoServerTID = 40136
	demoClientTID = 30574

	// Where the test stages each demo file's source bytes in Z80 RAM.
	demoSrcOrgA = 0xC000
	demoSrcOrgB = 0xD000
)

var (
	demoServerMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	demoServerIP  = [4]byte{192, 0, 2, 1}
	demoClientMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	demoClientIP  = [4]byte{192, 0, 2, 44}
)

func loadServeDemo(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(srvDemoBinPath); err != nil {
		t.Skipf("netboot_serve binary not built (%s); run `make netboot-serve`", srvDemoBinPath)
	}
	mac, err := z80h.Load(srvDemoBinPath, srvDemoMapPath)
	if err != nil {
		t.Fatalf("load netboot_serve: %v", err)
	}
	return mac
}

// demoFile is one served file: a name, its bytes, and where the test stages those
// bytes in Z80 RAM (the SRC_TABLE points there).
type demoFile struct {
	name string
	data []byte
	org  uint16
}

// fillServeConfig writes the CONFIG_* block + the flat STORE + the SRC_TABLE +
// every file's source bytes, and returns the matching Go serve.Responder.
func fillServeConfig(t *testing.T, mac *z80h.Machine, files []demoFile) *serve.Responder {
	t.Helper()
	put := func(sym string, data []byte) {
		mac.Write(symAddr(t, mac, sym), data)
	}
	be16 := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v & 0xff)} }

	put("CONFIG_SERVERMAC", demoServerMAC[:])
	put("CONFIG_SERVERIP", demoServerIP[:])
	put("CONFIG_SERVERTID", be16(demoServerTID))

	// STORE: name\0 + 4-byte LE size per file, then a single NUL terminator.
	var store bytes.Buffer
	// SRC_TABLE: name\0 + 2-byte LE source ptr + 4-byte LE size, then a NUL.
	var srcTab bytes.Buffer
	goFiles := map[string][]byte{}
	for _, f := range files {
		mac.Write(f.org, f.data)
		goFiles[f.name] = f.data

		var sz [4]byte
		binary.LittleEndian.PutUint32(sz[:], uint32(len(f.data)))

		store.WriteString(f.name)
		store.WriteByte(0)
		store.Write(sz[:])

		srcTab.WriteString(f.name)
		srcTab.WriteByte(0)
		srcTab.Write([]byte{byte(f.org & 0xff), byte(f.org >> 8)})
		srcTab.Write(sz[:])
	}
	store.WriteByte(0)
	srcTab.WriteByte(0)
	put("STORE", store.Bytes())
	put("SRC_TABLE", srcTab.Bytes())

	goStore := tftp.MapStore{}
	for name, b := range goFiles {
		goStore[name] = uint64(len(b))
	}
	cfg := serve.Config{ServerMAC: demoServerMAC, ServerIP: demoServerIP, ServerTID: demoServerTID}
	return serve.New(cfg, goStore, func(name string) tftp.Source { return tftp.ByteSource(goFiles[name]) })
}

func initServeDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	mac.AttachIO(enc)
	macAddr := symAddr(t, mac, "CONFIG_SERVERMAC")
	mac.Write(macAddr, demoServerMAC[:])
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_init returned BC=%d, want 1", res.BC)
	}
}

// serveDemo injects req (if non-nil) and runs one serve_serve_once, returning the
// single frame the driver transmitted (or nil if it sent nothing).
func serveDemo(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, req []byte) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	if req != nil {
		enc.InjectRX(req)
	}
	res, err := mac.Call("serve_serve_once")
	if err != nil {
		t.Fatalf("call serve_serve_once: %v", err)
	}
	tx := enc.TXFrames()
	if res.BC == 0 {
		if len(tx) != before {
			t.Fatalf("serve_serve_once returned BC=0 but transmitted a frame")
		}
		return nil
	}
	if len(tx) != before+1 {
		t.Fatalf("serve_serve_once transmitted %d frames, want 1", len(tx)-before)
	}
	out := tx[len(tx)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("serve_serve_once returned BC=%d but the wire frame is %d bytes", res.BC, len(out))
	}
	return out
}

// demoRRQ builds the client's RRQ frame for name with the given options.
func demoRRQ(name string, opts []tftp.Option) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: demoServerMAC, SrcMAC: demoClientMAC,
		SrcIP: demoClientIP, DstIP: demoServerIP,
		SrcPort: demoClientTID, DstPort: 69,
		Payload: tftp.BuildRRQ(name, "octet", opts),
	})
}

// demoWRQ builds the client's WRQ frame for name with the given options (i121a).
func demoWRQ(name string, opts []tftp.Option) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: demoServerMAC, SrcMAC: demoClientMAC,
		SrcIP: demoClientIP, DstIP: demoServerIP,
		SrcPort: demoClientTID, DstPort: 69,
		Payload: tftp.BuildWRQ(name, "octet", opts),
	})
}

// demoAck builds the client's ACK frame (client TID -> server TID).
func demoAck(block uint16) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: demoServerMAC, SrcMAC: demoClientMAC,
		SrcIP: demoClientIP, DstIP: demoServerIP,
		SrcPort: demoClientTID, DstPort: demoServerTID,
		Payload: tftp.BuildACK(block),
	})
}

// demoData builds the client's DATA frame for a WRQ upload (client TID -> server
// TID), the frames the WRQ receive loop accumulates into STAGING (i121b).
func demoData(block uint16, data []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: demoServerMAC, SrcMAC: demoClientMAC,
		SrcIP: demoClientIP, DstIP: demoServerIP,
		SrcPort: demoClientTID, DstPort: demoServerTID,
		Payload: tftp.BuildDATA(block, data),
	})
}

// wrqStagingAddr is where the Z80 serve unit accumulates a WRQ upload
// (WRQ_STAGING equ &C000, section D — flat RAM under the host harness). It is an
// assembler equate, so it is read by literal address rather than via the map.
const wrqStagingAddr = 0xC000

func eqFrame(t *testing.T, label string, got, want []byte) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: dispatch sent nothing", label)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s != Go authority\n  z80 %x\n  go  %x", label, got, want)
	}
}

// TestServeDemoFullSession is the headline i96 host check: a full demo session
// driven through serve_serve_once matches serve.Responder.OnFrame byte-for-byte —
// an ARP reply, a bare-RRQ DATA transfer, an optioned-RRQ OACK transfer, and a miss
// ERROR. The Go authority and the Z80 share one identity + store, so they produce
// identical bytes.
func TestServeDemoFullSession(t *testing.T) {
	fileA := makeFile(512 + 200) // bare-RRQ at 512: 1 full block + 200-byte tail
	fileB := makeFile(1024 + 50) // optioned-RRQ at 1024: 1 full block + 50-byte tail
	mac := loadServeDemo(t)
	ref := fillServeConfig(t, mac, []demoFile{
		{"hello.txt", fileA, demoSrcOrgA},
		{"readme.txt", fileB, demoSrcOrgB},
	})
	enc := z80h.NewENC28J60()
	initServeDriver(t, mac, enc)

	// 1. ARP request for the SAM's IP -> an ARP reply.
	arpReq := frame.BuildARPRequest(demoClientMAC, demoClientIP, demoServerIP)
	eqFrame(t, "ARP reply", serveDemo(t, mac, enc, arpReq), ref.OnFrame(arpReq))

	// 2. Bare RRQ for hello.txt (no options) -> DATA block 1 directly (no OACK).
	bare := demoRRQ("hello.txt", nil)
	eqFrame(t, "bare DATA 1", serveDemo(t, mac, enc, bare), ref.OnFrame(bare))
	// 3. ACK 1 -> DATA block 2 (short final, 200 bytes).
	a1 := demoAck(1)
	d2 := serveDemo(t, mac, enc, a1)
	eqFrame(t, "bare DATA 2", d2, ref.OnFrame(a1))
	if _, p, _ := tftp.ParseDATA(udpPayload(t, d2)); len(p) != 200 {
		t.Errorf("bare final block = %d bytes, want 200", len(p))
	}
	// 4. ACK 2 -> transfer complete, nothing sent.
	a2 := demoAck(2)
	if fin := serveDemo(t, mac, enc, a2); fin != nil {
		t.Errorf("ACK of the short final block should end the transfer, got %x", fin)
	}
	_ = ref.OnFrame(a2) // keep the reference in lockstep

	// 5. Optioned RRQ for readme.txt -> OACK.
	opt := demoRRQ("readme.txt", []tftp.Option{{Name: "blksize", Value: "1024"}, {Name: "tsize", Value: "0"}})
	eqFrame(t, "OACK", serveDemo(t, mac, enc, opt), ref.OnFrame(opt))
	// 6. ACK 0 -> DATA block 1 (the OACK-path FirstData handoff).
	a0 := demoAck(0)
	d1 := serveDemo(t, mac, enc, a0)
	eqFrame(t, "opt DATA 1", d1, ref.OnFrame(a0))
	if _, p, _ := tftp.ParseDATA(udpPayload(t, d1)); len(p) != 1024 {
		t.Errorf("opt block 1 = %d bytes, want 1024", len(p))
	}
	// 7. ACK 1 -> DATA block 2 (short final, 50 bytes).
	a1b := demoAck(1)
	d2b := serveDemo(t, mac, enc, a1b)
	eqFrame(t, "opt DATA 2", d2b, ref.OnFrame(a1b))
	// 8. ACK 2 -> complete.
	a2b := demoAck(2)
	if fin := serveDemo(t, mac, enc, a2b); fin != nil {
		t.Errorf("opt ACK of the short final block should end the transfer, got %x", fin)
	}
	_ = ref.OnFrame(a2b)
}

// TestServeDemoMiss confirms a request for an unknown name gets ERROR(1) and the
// server keeps serving (a subsequent valid RRQ still works).
func TestServeDemoMiss(t *testing.T) {
	file := makeFile(100)
	mac := loadServeDemo(t)
	ref := fillServeConfig(t, mac, []demoFile{{"hello.txt", file, demoSrcOrgA}})
	enc := z80h.NewENC28J60()
	initServeDriver(t, mac, enc)

	miss := demoRRQ("nope.txt", nil)
	got := serveDemo(t, mac, enc, miss)
	eqFrame(t, "ERROR(1)", got, ref.OnFrame(miss))
	code, _, err := tftp.ParseError(udpPayload(t, got))
	if err != nil || code != tftp.ErrFileNotFound {
		t.Fatalf("miss should be ERROR(1), got code %d err %v", code, err)
	}
	// A valid RRQ after a miss still serves.
	good := demoRRQ("hello.txt", nil)
	eqFrame(t, "after-miss DATA 1", serveDemo(t, mac, enc, good), ref.OnFrame(good))
}

// TestServeDemoArpOtherIPIgnored confirms an ARP request for a different IP is not
// answered (matching the Go authority).
func TestServeDemoArpOtherIPIgnored(t *testing.T) {
	mac := loadServeDemo(t)
	fillServeConfig(t, mac, []demoFile{{"hello.txt", makeFile(10), demoSrcOrgA}})
	enc := z80h.NewENC28J60()
	initServeDriver(t, mac, enc)

	other := frame.BuildARPRequest(demoClientMAC, demoClientIP, demoClientIP)
	if r := serveDemo(t, mac, enc, other); r != nil {
		t.Errorf("answered an ARP request for a different IP: %x", r)
	}
}

// TestServeDemoWRQBareACK0 is the i121a host check (bare WRQ path): a bare WRQ
// (no options, `tftp put` default) from the client is answered with ACK-0
// (`00 04 00 00`) byte-for-byte matching the Go authority serve.Responder.OnFrame.
// The Z80 serve_serve_once dispatch learns the client endpoint and calls
// build_ack0; the reply wraps via build_udp_frame back to the client TID.
func TestServeDemoWRQBareACK0(t *testing.T) {
	mac := loadServeDemo(t)
	ref := fillServeConfig(t, mac, []demoFile{{"hello.txt", makeFile(10), demoSrcOrgA}})
	enc := z80h.NewENC28J60()
	initServeDriver(t, mac, enc)

	wrq := demoWRQ("upload.bin", nil)
	got := serveDemo(t, mac, enc, wrq)
	eqFrame(t, "bare WRQ -> ACK-0", got, ref.OnFrame(wrq))

	// Confirm opcode and block number of the received frame.
	u, ok := frame.ParseUDP(got)
	if !ok {
		t.Fatalf("bare WRQ reply is not a UDP frame: %x", got)
	}
	if tftp.Opcode(u.Payload) != tftp.OpACK {
		t.Fatalf("bare WRQ reply opcode = %d, want ACK(%d)", tftp.Opcode(u.Payload), tftp.OpACK)
	}
	blk, err := tftp.ParseACK(u.Payload)
	if err != nil || blk != 0 {
		t.Fatalf("bare WRQ reply block = %d (err %v), want 0", blk, err)
	}
}

// TestServeDemoWRQOptionedOACK is the i121a host check (optioned WRQ path): a
// WRQ carrying blksize + tsize options is answered with an OACK that echoes the
// accepted blksize and the client's tsize, byte-for-byte matching the Go authority.
// The Z80 handle_wrq calls negotiate_blksize + build_oack_opts_wrq + build_oack.
func TestServeDemoWRQOptionedOACK(t *testing.T) {
	mac := loadServeDemo(t)
	ref := fillServeConfig(t, mac, []demoFile{{"hello.txt", makeFile(10), demoSrcOrgA}})
	enc := z80h.NewENC28J60()
	initServeDriver(t, mac, enc)

	wrq := demoWRQ("upload.bin", []tftp.Option{
		{Name: "blksize", Value: "512"},
		{Name: "tsize", Value: "4096"},
	})
	got := serveDemo(t, mac, enc, wrq)
	eqFrame(t, "optioned WRQ -> OACK", got, ref.OnFrame(wrq))

	// Also confirm the OACK carries the right option values.
	u, ok := frame.ParseUDP(got)
	if !ok {
		t.Fatalf("optioned WRQ reply is not a UDP frame: %x", got)
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
}

// TestServeDemoWRQReceiveToStaging is the i121b host check: after the WRQ
// handshake (i121a) the server receives the pushed file's DATA blocks 1..N,
// ACKing each one back to the client and accumulating the bytes into STAGING,
// ending on the short final block. Both the bare-WRQ (blksize 512) and the
// optioned-WRQ (negotiated blksize) handshakes are exercised. Every ACK on the
// virtual wire is asserted byte-for-byte against the Go authority
// serve.Responder.OnFrame, and the Z80 STAGING buffer is asserted to hold the
// uploaded bytes exactly (compared against the authority's accumulated bytes).
func TestServeDemoWRQReceiveToStaging(t *testing.T) {
	cases := []struct {
		name    string
		opts    []tftp.Option
		blksize int
	}{
		{"bare", nil, 512},
		{"optioned", []tftp.Option{{Name: "blksize", Value: "1024"}, {Name: "tsize", Value: "2098"}}, 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mac := loadServeDemo(t)
			ref := fillServeConfig(t, mac, []demoFile{{"hello.txt", makeFile(10), demoSrcOrgA}})
			enc := z80h.NewENC28J60()
			initServeDriver(t, mac, enc)

			// The uploaded file: two full blocks + a short final tail, so the
			// transfer spans three DATA blocks and ends on a short one.
			file := makeFile(2*tc.blksize + 50)

			// 1. WRQ -> handshake reply (ACK-0 for a bare WRQ, OACK for optioned).
			wrq := demoWRQ("upload.bin", tc.opts)
			eqFrame(t, "WRQ handshake", serveDemo(t, mac, enc, wrq), ref.OnFrame(wrq))

			// 2. DATA blocks 1..N -> an ACK per block, accumulating into STAGING.
			block := uint16(1)
			for off := 0; off < len(file); off += tc.blksize {
				end := off + tc.blksize
				if end > len(file) {
					end = len(file)
				}
				dataFrame := demoData(block, file[off:end])
				gotACK := serveDemo(t, mac, enc, dataFrame)
				eqFrame(t, "DATA -> ACK", gotACK, ref.OnFrame(dataFrame))

				// Confirm the reply is an ACK of this block.
				u, ok := frame.ParseUDP(gotACK)
				if !ok {
					t.Fatalf("block %d ACK is not a UDP frame: %x", block, gotACK)
				}
				if tftp.Opcode(u.Payload) != tftp.OpACK {
					t.Fatalf("block %d reply opcode = %d, want ACK", block, tftp.Opcode(u.Payload))
				}
				if blk, err := tftp.ParseACK(u.Payload); err != nil || blk != block {
					t.Fatalf("block %d reply ACKs block %d (err %v)", block, blk, err)
				}

				// After block 1, replay it: a duplicate is re-ACKed (RFC 1350
				// server recovery) without re-staging — the staged length must
				// not grow. This mirrors tftp.ClientXfer.OnData's duplicate path.
				if block == 1 {
					dup := demoData(1, file[:tc.blksize])
					eqFrame(t, "duplicate DATA 1 -> re-ACK", serveDemo(t, mac, enc, dup), ref.OnFrame(dup))
				}
				block++
			}

			// 3. The authority reports the transfer complete and the staged bytes.
			staged, done := ref.WRQStaged()
			if !done {
				t.Fatalf("Go authority did not mark the WRQ transfer complete")
			}
			if !bytes.Equal(staged, file) {
				t.Fatalf("Go authority staged %d bytes, want %d", len(staged), len(file))
			}

			// 4. The Z80 STAGING buffer holds the uploaded file byte-for-byte.
			z80Staged := mac.Read(wrqStagingAddr, len(file))
			if !bytes.Equal(z80Staged, file) {
				t.Errorf("Z80 STAGING != uploaded file\n  z80 %x...\n  want %x...",
					z80Staged[:min(16, len(z80Staged))], file[:min(16, len(file))])
			}
		})
	}
}
