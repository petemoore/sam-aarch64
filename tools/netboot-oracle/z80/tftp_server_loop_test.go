// tftp_server_loop_test.go — the i83 host-verification of the TFTP server
// transfer loop. It runs the *real* composed state machine
// (src/netboot/tftp_server_loop.asm: the driver encdrv.asm + the host-verified
// build_udp_frame/tftp_build/tftp_parse primitives) under the flat-memory
// koron-go/z80 harness with the emulated Trinity (enc28j60.go) attached, and
// asserts that a captured RRQ for a stored file is answered with an OACK frame,
// then the DATA frames stream the file at the negotiated blksize ending on a
// short final block, and a miss / serial-subdir yields an ERROR(1) frame — each
// byte-for-byte the Go authority's (tftp.ServerLoop).
//
// This is the TFTP-server half of the netboot transfer made host-verifiable
// end-to-end by i80. Emulation verification, NOT hardware verification — real
// Trinity remains the final gate (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"strconv"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	tftpSrvBinPath = "../../../build/netboot_tftp_server_loop.bin"
	tftpSrvMapPath = "../../../build/netboot_tftp_server_loop.map"
)

const (
	srvTID    = 40136  // the SAM's ephemeral source port for the transfer
	clientTID = 30574  // the captured client RRQ source port
	srcOrg    = 0xC000 // where the test stages the file source bytes in Z80 RAM
)

func loadTFTPSrv(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(tftpSrvBinPath); err != nil {
		t.Fatalf("tftp_server_loop binary not built (%s); run `make netboot-tftp-server-loop`", tftpSrvBinPath)
	}
	mac, err := z80h.Load(tftpSrvBinPath, tftpSrvMapPath)
	if err != nil {
		t.Fatalf("load tftp_server_loop: %v", err)
	}
	return mac
}

// fillTFTPSrv writes the CONFIG_* identity, the flat store (one named file), and
// the file source bytes, returning the Go ServerLoop configured identically.
func fillTFTPSrv(t *testing.T, mac *z80h.Machine, name string, file []byte) *tftp.ServerLoop {
	t.Helper()
	put := func(sym string, data []byte) {
		a, err := mac.Sym(sym)
		if err != nil {
			t.Fatalf("%v", err)
		}
		mac.Write(a, data)
	}
	put("CONFIG_SERVERMAC", mask.ServerMAC[:])
	put("CONFIG_SERVERIP", mask.ServerIP[:])
	srvTIDbe := []byte{byte(srvTID >> 8), byte(srvTID & 0xff)}
	put("CONFIG_SERVERTID", srvTIDbe)

	// Flat store: "name\0" + 4-byte LE size, then a single NUL terminator.
	var store bytes.Buffer
	store.WriteString(name)
	store.WriteByte(0)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(file)))
	store.Write(sz[:])
	store.WriteByte(0) // empty name = end of store
	put("STORE", store.Bytes())

	// Source bytes at srcOrg; SRC_PTR points there.
	mac.Write(srcOrg, file)
	mac.WriteU16LE(symAddr(t, mac, "SRC_PTR"), srcOrg)

	store2 := tftp.MapStore{name: uint64(len(file))}
	sl := tftp.NewServerLoop(store2, mask.ServerMAC, mask.ServerIP, srvTID)
	sl.SetSource(tftp.ByteSource(file))
	return sl
}

func symAddr(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return a
}

func initTFTPSrvDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	mac.AttachIO(enc)
	macAddr := symAddr(t, mac, "CONFIG_SERVERMAC")
	mac.Write(macAddr, mask.ServerMAC[:])
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_init returned BC=%d, want 1", res.BC)
	}
}

// txAfter injects req (if non-nil) and runs entry, returning the single frame
// the driver transmitted (or nil if it sent nothing).
func txAfter(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, entry string, req []byte) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	if req != nil {
		enc.InjectRX(req)
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

// ackFrame builds the client's ACK frame (client TID -> server TID).
func ackFrame(block uint16) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
		SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
		SrcPort: clientTID, DstPort: srvTID,
		Payload: tftp.BuildACK(block),
	})
}

// TestTFTPServerOACK is the headline i83 host check: a captured RRQ for a stored
// file yields an OACK frame on the virtual wire, byte-for-byte the Go ServerLoop.
func TestTFTPServerOACK(t *testing.T) {
	mac := loadTFTPSrv(t)
	file := makeFile(2*1024 + 300)
	ref := fillTFTPSrv(t, mac, "config.txt", file)
	enc := z80h.NewENC28J60()
	initTFTPSrvDriver(t, mac, enc)

	got := txAfter(t, mac, enc, "tftp_handle_rrq", golden.TFTPRrqRoot1024)
	if got == nil {
		t.Fatal("server ignored a valid RRQ")
	}
	want := ref.OnRRQ(golden.TFTPRrqRoot1024)
	if !bytes.Equal(got, want) {
		t.Errorf("OACK frame != Go authority\n  z80 %x\n  go  %x", got, want)
	}
}

// TestTFTPServerTransfer drives the full OACK + DATA/ACK transfer and asserts
// every frame matches the Go ServerLoop, with the short-final-block termination.
func TestTFTPServerTransfer(t *testing.T) {
	mac := loadTFTPSrv(t)
	const blksize = 1024
	file := makeFile(2*blksize + 300) // 2 full blocks + a 300-byte tail
	ref := fillTFTPSrv(t, mac, "config.txt", file)
	enc := z80h.NewENC28J60()
	initTFTPSrvDriver(t, mac, enc)

	// RRQ -> OACK.
	oack := txAfter(t, mac, enc, "tftp_handle_rrq", golden.TFTPRrqRoot1024)
	if !bytes.Equal(oack, ref.OnRRQ(golden.TFTPRrqRoot1024)) {
		t.Fatalf("OACK mismatch")
	}

	// Client ACKs block 0 -> first DATA (block 1).
	d1 := txAfter(t, mac, enc, "tftp_first_data", nil)
	if !bytes.Equal(d1, ref.FirstData()) {
		t.Errorf("DATA 1 != Go authority\n  z80 %x\n  go  %x", d1, ref.FirstData())
	}

	// ACK 1 -> DATA 2 (full).
	d2 := txAfter(t, mac, enc, "tftp_handle_ack", ackFrame(1))
	if !bytes.Equal(d2, ref.OnACK(ackFrame(1))) {
		t.Errorf("DATA 2 mismatch\n  z80 %x", d2)
	}

	// ACK 2 -> DATA 3 (short final, 300 bytes).
	d3 := txAfter(t, mac, enc, "tftp_handle_ack", ackFrame(2))
	wantD3 := ref.OnACK(ackFrame(2))
	if !bytes.Equal(d3, wantD3) {
		t.Errorf("DATA 3 (short final) mismatch\n  z80 %x\n  go  %x", d3, wantD3)
	}
	if _, p3, _ := tftp.ParseDATA(udpPayload(t, d3)); len(p3) != 300 {
		t.Errorf("final block payload = %d bytes, want 300", len(p3))
	}

	// ACK 3 -> transfer complete, nothing sent.
	if fin := txAfter(t, mac, enc, "tftp_handle_ack", ackFrame(3)); fin != nil {
		t.Errorf("ACK of the short final block should end the transfer, got a frame")
	}
	_ = ref.OnACK(ackFrame(3)) // keep the Go model in lockstep
}

// TestTFTPServerExactMultiple checks the zero-length final block when the file
// is an exact multiple of the blksize (RFC 1350), matching the Go authority.
func TestTFTPServerExactMultiple(t *testing.T) {
	mac := loadTFTPSrv(t)
	const blksize = 1024
	file := makeFile(blksize) // exactly one full block
	ref := fillTFTPSrv(t, mac, "config.txt", file)
	enc := z80h.NewENC28J60()
	initTFTPSrvDriver(t, mac, enc)

	txAfter(t, mac, enc, "tftp_handle_rrq", golden.TFTPRrqRoot1024)
	ref.OnRRQ(golden.TFTPRrqRoot1024)

	d1 := txAfter(t, mac, enc, "tftp_first_data", nil)
	if !bytes.Equal(d1, ref.FirstData()) {
		t.Errorf("DATA 1 mismatch")
	}
	// ACK 1 -> a zero-length DATA block 2 (the exact-multiple terminator).
	d2 := txAfter(t, mac, enc, "tftp_handle_ack", ackFrame(1))
	wantD2 := ref.OnACK(ackFrame(1))
	if !bytes.Equal(d2, wantD2) {
		t.Errorf("DATA 2 (zero-length terminator) mismatch\n  z80 %x\n  go  %x", d2, wantD2)
	}
	if _, p2, _ := tftp.ParseDATA(udpPayload(t, d2)); len(p2) != 0 {
		t.Errorf("exact-multiple terminator payload = %d bytes, want 0", len(p2))
	}
}

// TestTFTPServerMissError confirms a miss yields ERROR(1) and serves no data —
// the headline robustness rule (keep serving the session).
func TestTFTPServerMissError(t *testing.T) {
	mac := loadTFTPSrv(t)
	_ = fillTFTPSrv(t, mac, "config.txt", makeFile(512))
	enc := z80h.NewENC28J60()
	initTFTPSrvDriver(t, mac, enc)

	// An RRQ for a file NOT in the store.
	missRRQ := frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
		SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
		SrcPort: clientTID, DstPort: 69,
		Payload: tftp.BuildRRQ("recovery.elf", "octet", []tftp.Option{{Name: "tsize", Value: "0"}, {Name: "blksize", Value: "1024"}}),
	})
	got := txAfter(t, mac, enc, "tftp_handle_rrq", missRRQ)
	if got == nil {
		t.Fatal("server sent nothing for a miss (should send ERROR(1))")
	}
	if code, _, _ := tftp.ParseError(udpPayload(t, got)); code != tftp.ErrFileNotFound {
		t.Errorf("miss reply code = %d, want 1 (file not found)", code)
	}
	// And it must serve no data afterwards.
	if d := txAfter(t, mac, enc, "tftp_first_data", nil); d != nil {
		t.Errorf("server served data after a miss: %x", d)
	}
}

// TestTFTPServerSerialSubdir confirms a serial-subdir-prefixed RRQ 404s (the Pi
// retries at root) — ERROR(1), matching the Go authority's resolve.
func TestTFTPServerSerialSubdir(t *testing.T) {
	mac := loadTFTPSrv(t)
	// The captured serial RRQ asks for "00000000/start4.elf"; even if that name
	// were in the store, the serial-subdir prefix must 404.
	_ = fillTFTPSrv(t, mac, "00000000/start4.elf", makeFile(512))
	enc := z80h.NewENC28J60()
	initTFTPSrvDriver(t, mac, enc)

	got := txAfter(t, mac, enc, "tftp_handle_rrq", golden.TFTPRrqSerial)
	if got == nil {
		t.Fatal("server sent nothing for a serial-subdir RRQ")
	}
	if code, _, _ := tftp.ParseError(udpPayload(t, got)); code != tftp.ErrFileNotFound {
		t.Errorf("serial-subdir reply code = %d, want 1", code)
	}
}

// txWindow injects req (if non-nil), runs entry, and returns ALL frames the
// driver transmitted during the call — the RFC 7440 windowed counterpart of
// txAfter (a single window send emits up to windowsize DATA frames at once).
func txWindow(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, entry string, req []byte) [][]byte {
	t.Helper()
	before := len(enc.TXFrames())
	if req != nil {
		enc.InjectRX(req)
	}
	if _, err := mac.Call(entry); err != nil {
		t.Fatalf("call %s: %v", entry, err)
	}
	tx := enc.TXFrames()
	out := make([][]byte, 0, len(tx)-before)
	for _, f := range tx[before:] {
		out = append(out, f)
	}
	return out
}

// eqFrames asserts two windows of frames are byte-for-byte identical.
func eqFrames(t *testing.T, label string, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d frames, want %d", label, len(got), len(want))
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("%s: frame %d mismatch\n  z80 %x\n  go  %x", label, i, got[i], want[i])
		}
	}
}

// windowedRRQ builds an RRQ for "config.txt" requesting the given blksize and
// windowsize (RFC 7440), to port 69 from the captured client TID.
func windowedRRQ(blksize, winsize int) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ServerMAC, SrcMAC: mask.ClientMAC,
		SrcIP: mask.ClientIP, DstIP: mask.ServerIP,
		SrcPort: clientTID, DstPort: 69,
		Payload: tftp.BuildRRQ("config.txt", "octet", []tftp.Option{
			{Name: "blksize", Value: strconv.Itoa(blksize)},
			{Name: "tsize", Value: "0"},
			{Name: "windowsize", Value: strconv.Itoa(winsize)},
		}),
	})
}

// TestTFTPServerWindowedFull drives a multi-window transfer (windowsize 3) and
// asserts the OACK echoes the windowsize and every window of DATA frames matches
// the Go ServerLoop authority (FirstWindow / OnACKWindow), ending on the short
// final block's ACK.
func TestTFTPServerWindowedFull(t *testing.T) {
	mac := loadTFTPSrv(t)
	const blksize, winsize = 512, 3
	file := makeFile(6*blksize + 100) // blocks 1..6 full, block 7 short (100B)
	ref := fillTFTPSrv(t, mac, "config.txt", file)
	enc := z80h.NewENC28J60()
	initTFTPSrvDriver(t, mac, enc)

	rrq := windowedRRQ(blksize, winsize)
	oack := txAfter(t, mac, enc, "tftp_handle_rrq", rrq)
	wantOACK := ref.OnRRQ(rrq)
	if !bytes.Equal(oack, wantOACK) {
		t.Fatalf("windowed OACK != Go authority\n  z80 %x\n  go  %x", oack, wantOACK)
	}

	// ACK 0 -> first window {1,2,3}.
	w1 := txWindow(t, mac, enc, "tftp_first_data", nil)
	eqFrames(t, "first window", w1, ref.FirstWindow())

	// ACK 3 -> {4,5,6}.
	w2 := txWindow(t, mac, enc, "tftp_handle_ack", ackFrame(3))
	eqFrames(t, "window after ACK 3", w2, ref.OnACKWindow(ackFrame(3)))

	// ACK 6 -> {7} (short final block ends the window early).
	w3 := txWindow(t, mac, enc, "tftp_handle_ack", ackFrame(6))
	eqFrames(t, "window after ACK 6", w3, ref.OnACKWindow(ackFrame(6)))
	if len(w3) != 1 {
		t.Errorf("final window = %d frames, want 1 (short final stops the window)", len(w3))
	}

	// ACK 7 (the short final) -> transfer complete, nothing sent.
	if fin := txWindow(t, mac, enc, "tftp_handle_ack", ackFrame(7)); len(fin) != 0 {
		t.Errorf("ACK of the short final block should end the transfer, got %d frames", len(fin))
	}
	if got := ref.OnACKWindow(ackFrame(7)); got != nil {
		t.Errorf("Go authority still sent frames after the final ACK")
	}
}

// TestTFTPServerWindowedGap drives a lost-block scenario: a cumulative ACK below
// the last block sent must rewind the window's left edge and re-send from there,
// matching the Go authority's OnWindowAck rewind.
func TestTFTPServerWindowedGap(t *testing.T) {
	mac := loadTFTPSrv(t)
	const blksize, winsize = 512, 3
	file := makeFile(6*blksize + 100)
	ref := fillTFTPSrv(t, mac, "config.txt", file)
	enc := z80h.NewENC28J60()
	initTFTPSrvDriver(t, mac, enc)

	rrq := windowedRRQ(blksize, winsize)
	if !bytes.Equal(txAfter(t, mac, enc, "tftp_handle_rrq", rrq), ref.OnRRQ(rrq)) {
		t.Fatalf("windowed OACK mismatch")
	}

	// ACK 0 -> {1,2,3}.
	eqFrames(t, "first window", txWindow(t, mac, enc, "tftp_first_data", nil), ref.FirstWindow())

	// Blocks 2,3 lost: the client only got block 1, so it ACKs 1 (cumulative).
	// The server rewinds winAcked to 1 and re-sends {2,3,4}.
	wGap := txWindow(t, mac, enc, "tftp_handle_ack", ackFrame(1))
	eqFrames(t, "rewind window after ACK 1", wGap, ref.OnACKWindow(ackFrame(1)))

	// ACK 4 -> {5,6,7} (block 7 short final).
	w := txWindow(t, mac, enc, "tftp_handle_ack", ackFrame(4))
	eqFrames(t, "window after ACK 4", w, ref.OnACKWindow(ackFrame(4)))

	// ACK 7 -> complete.
	if fin := txWindow(t, mac, enc, "tftp_handle_ack", ackFrame(7)); len(fin) != 0 {
		t.Errorf("final ACK should end the transfer, got %d frames", len(fin))
	}
	_ = ref.OnACKWindow(ackFrame(7))
}

// TestTFTPServerWindowedShortFinalMidWindow checks a short final block that falls
// BEFORE the window is full: the window stops early at the short block, and its
// ACK completes the transfer (matching NextWindow's break + OnWindowAck).
func TestTFTPServerWindowedShortFinalMidWindow(t *testing.T) {
	mac := loadTFTPSrv(t)
	const blksize, winsize = 512, 4
	file := makeFile(2*blksize + 100) // blocks 1,2 full, block 3 short — window of 4 stops at 3
	ref := fillTFTPSrv(t, mac, "config.txt", file)
	enc := z80h.NewENC28J60()
	initTFTPSrvDriver(t, mac, enc)

	rrq := windowedRRQ(blksize, winsize)
	if !bytes.Equal(txAfter(t, mac, enc, "tftp_handle_rrq", rrq), ref.OnRRQ(rrq)) {
		t.Fatalf("windowed OACK mismatch")
	}

	// ACK 0 -> {1,2,3} (only 3 frames though windowsize is 4: block 3 is short).
	w1 := txWindow(t, mac, enc, "tftp_first_data", nil)
	eqFrames(t, "first window (short final mid-window)", w1, ref.FirstWindow())
	if len(w1) != 3 {
		t.Errorf("first window = %d frames, want 3 (short final stops before windowsize=4)", len(w1))
	}

	// ACK 3 (the short final) -> complete.
	if fin := txWindow(t, mac, enc, "tftp_handle_ack", ackFrame(3)); len(fin) != 0 {
		t.Errorf("ACK of the short final block should end the transfer, got %d frames", len(fin))
	}
	if got := ref.OnACKWindow(ackFrame(3)); got != nil {
		t.Errorf("Go authority sent frames after the final ACK")
	}
}

func makeFile(n int) []byte {
	f := make([]byte, n)
	for i := range f {
		f[i] = byte(i * 7)
	}
	return f
}

func udpPayload(t *testing.T, f []byte) []byte {
	t.Helper()
	u, ok := frame.ParseUDP(f)
	if !ok {
		t.Fatal("frame did not parse as UDP")
	}
	return u.Payload
}
