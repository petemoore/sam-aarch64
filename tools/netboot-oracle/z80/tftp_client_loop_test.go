// tftp_client_loop_test.go — the i82 host-verification of the TFTP client
// transfer loop (the receive side). It runs the *real* composed state machine
// (src/netboot/tftp_client_loop.asm: the driver encdrv.asm + the host-verified
// build_udp_frame/tftp_client primitives) under the flat-memory koron-go/z80
// harness with the emulated Trinity (enc28j60.go) attached, and asserts that a
// received DATA frame is answered with an ACK frame on the virtual wire,
// byte-for-byte the Go authority's (tftp.ClientLoop), with the Sorcerer's-
// Apprentice timeout retransmit and the unknown-TID ERROR(5).
//
// Emulation verification, NOT hardware verification — real Trinity remains the
// final gate (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	tftpCliBinPath = "../../../build/netboot_tftp_client_loop.bin"
	tftpCliMapPath = "../../../build/netboot_tftp_client_loop.map"
	cliTID         = 30574 // the captured client TID (matches golden.TFTPData dst)
)

func loadTFTPCli(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(tftpCliBinPath); err != nil {
		t.Fatalf("tftp_client_loop binary not built (%s); run `make netboot-tftp-client-loop`", tftpCliBinPath)
	}
	mac, err := z80h.Load(tftpCliBinPath, tftpCliMapPath)
	if err != nil {
		t.Fatalf("load tftp_client_loop: %v", err)
	}
	return mac
}

// fillTFTPCli writes the CLIENT_* identity + the negotiated blksize, returning
// the Go ClientLoop configured identically.
func fillTFTPCli(t *testing.T, mac *z80h.Machine, blksize int) *tftp.ClientLoop {
	t.Helper()
	put := func(sym string, data []byte) {
		mac.Write(symAddr(t, mac, sym), data)
	}
	put("CLIENT_MAC", mask.ClientMAC[:])
	put("CLIENT_IP", mask.ClientIP[:])
	put("CLIENT_TID", []byte{byte(cliTID >> 8), byte(cliTID & 0xff)})
	put("CLIENT_BLKSIZE", []byte{byte(blksize & 0xff), byte(blksize >> 8)})
	return tftp.NewClientLoop(mask.ClientMAC, mask.ClientIP, cliTID, blksize)
}

func initTFTPCliDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
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

// TestTFTPClientACK is the headline i82 host check: a received DATA block 1 is
// answered with an ACK frame on the virtual wire, byte-for-byte the Go authority.
func TestTFTPClientACK(t *testing.T) {
	mac := loadTFTPCli(t)
	const blksize = 1024
	ref := fillTFTPCli(t, mac, blksize)
	enc := z80h.NewENC28J60()
	initTFTPCliDriver(t, mac, enc)

	got := txAfter(t, mac, enc, "tftp_recv_data", golden.TFTPData)
	if got == nil {
		t.Fatal("client loop produced no ACK for DATA block 1")
	}
	want := ref.OnDATA(golden.TFTPData)
	if !bytes.Equal(got, want) {
		t.Errorf("ACK frame != Go authority\n  z80 %x\n  go  %x", got, want)
	}
}

// TestTFTPClientTransfer drives a two-block transfer (the captured full block 1
// then a synthesised short final block 2) and asserts both ACK frames match the
// Go authority, with the short final block ending the transfer + the file bytes
// accumulated into STAGING.
func TestTFTPClientTransfer(t *testing.T) {
	mac := loadTFTPCli(t)
	const blksize = 1024
	ref := fillTFTPCli(t, mac, blksize)
	enc := z80h.NewENC28J60()
	initTFTPCliDriver(t, mac, enc)

	// Block 1 (captured): ACK 1.
	a1 := txAfter(t, mac, enc, "tftp_recv_data", golden.TFTPData)
	if !bytes.Equal(a1, ref.OnDATA(golden.TFTPData)) {
		t.Fatalf("ACK 1 mismatch")
	}

	// Block 2: a short final block from the same server TID -> ACK 2 + done.
	du, _ := frame.ParseUDP(golden.TFTPData)
	tail := []byte("the tail of the file")
	data2 := dataFrameFromServer(du.SrcPort, 2, tail)
	a2 := txAfter(t, mac, enc, "tftp_recv_data", data2)
	if !bytes.Equal(a2, ref.OnDATA(data2)) {
		t.Errorf("ACK 2 (short final) mismatch\n  z80 %x\n  go  %x", a2, ref.OnDATA(data2))
	}
	if blk, _ := tftp.ParseACK(udpPayload(t, a2)); blk != 2 {
		t.Errorf("final ACK block = %d, want 2", blk)
	}

	// The accumulated STAGING must hold block1 (1024 bytes) + the tail.
	_, b1data, _ := tftp.ParseDATA(du.Payload)
	stage := mac.Read(symAddr(t, mac, "STAGING"), len(b1data)+len(tail))
	if !bytes.Equal(stage[:len(b1data)], b1data) {
		t.Errorf("STAGING block-1 bytes != captured DATA payload")
	}
	if !bytes.Equal(stage[len(b1data):], tail) {
		t.Errorf("STAGING tail bytes != block-2 payload")
	}
}

// TestTFTPClientUnknownTID confirms a DATA from a wrong server TID is answered
// with ERROR(5) and not accepted (RFC 1350 §4), matching the Go authority.
func TestTFTPClientUnknownTID(t *testing.T) {
	mac := loadTFTPCli(t)
	const blksize = 1024
	ref := fillTFTPCli(t, mac, blksize)
	enc := z80h.NewENC28J60()
	initTFTPCliDriver(t, mac, enc)

	// Accept block 1 from the real server TID first (learns the TID).
	txAfter(t, mac, enc, "tftp_recv_data", golden.TFTPData)
	ref.OnDATA(golden.TFTPData)

	// A DATA from a different server port -> ERROR(5).
	du, _ := frame.ParseUDP(golden.TFTPData)
	stray := dataFrameFromServer(du.SrcPort+1, 2, make([]byte, blksize))
	got := txAfter(t, mac, enc, "tftp_recv_data", stray)
	want := ref.OnDATA(stray)
	if !bytes.Equal(got, want) {
		t.Errorf("unknown-TID ERROR frame != Go authority\n  z80 %x\n  go  %x", got, want)
	}
	if code, _, _ := tftp.ParseError(udpPayload(t, got)); code != tftp.ErrUnknownTID {
		t.Errorf("unknown-TID reply code = %d, want 5", code)
	}
}

// TestTFTPClientDuplicate exercises the duplicate-block re-ACK path with a
// block strictly below the highest ACKed (the server retransmitted an old
// block): the client must re-ACK the *received* block (not its highest) without
// re-storing, byte-for-byte the Go authority (ClientXfer: BuildACK(block),
// acked unchanged).
func TestTFTPClientDuplicate(t *testing.T) {
	mac := loadTFTPCli(t)
	const blksize = 1024
	ref := fillTFTPCli(t, mac, blksize)
	enc := z80h.NewENC28J60()
	initTFTPCliDriver(t, mac, enc)

	du, _ := frame.ParseUDP(golden.TFTPData)
	// Accept block 1 (captured) then block 2 (full) to advance acked to 2.
	txAfter(t, mac, enc, "tftp_recv_data", golden.TFTPData)
	ref.OnDATA(golden.TFTPData)
	data2 := dataFrameFromServer(du.SrcPort, 2, make([]byte, blksize))
	txAfter(t, mac, enc, "tftp_recv_data", data2)
	ref.OnDATA(data2)

	// Now a duplicate of block 1 arrives (block < acked): re-ACK 1, no store.
	dup := dataFrameFromServer(du.SrcPort, 1, make([]byte, blksize))
	got := txAfter(t, mac, enc, "tftp_recv_data", dup)
	want := ref.OnDATA(dup)
	if !bytes.Equal(got, want) {
		t.Errorf("duplicate-block re-ACK != Go authority\n  z80 %x\n  go  %x", got, want)
	}
	if blk, _ := tftp.ParseACK(udpPayload(t, got)); blk != 1 {
		t.Errorf("duplicate re-ACK block = %d, want 1 (the received block, not the highest)", blk)
	}
}

// TestTFTPClientSAS asserts the Sorcerer's-Apprentice fix: a timeout after an
// ACK retransmits the last ACK frame only (never an RRQ), matching the Go
// authority byte-for-byte.
func TestTFTPClientSAS(t *testing.T) {
	mac := loadTFTPCli(t)
	const blksize = 1024
	ref := fillTFTPCli(t, mac, blksize)
	enc := z80h.NewENC28J60()
	initTFTPCliDriver(t, mac, enc)

	// Block 1 -> ACK 1 (so there is a last ACK to retransmit).
	txAfter(t, mac, enc, "tftp_recv_data", golden.TFTPData)
	ref.OnDATA(golden.TFTPData)

	got := txAfter(t, mac, enc, "tftp_recv_timeout", nil)
	want := ref.OnTimeout()
	if !bytes.Equal(got, want) {
		t.Errorf("SAS retransmit frame != Go authority\n  z80 %x\n  go  %x", got, want)
	}
	if tftp.Opcode(udpPayload(t, got)) != tftp.OpACK {
		t.Errorf("SAS violation: timeout retransmitted opcode %d, want ACK", tftp.Opcode(udpPayload(t, got)))
	}
}

// dataFrameFromServer builds a server->client DATA frame (from the given server
// TID) carrying the block + payload.
func dataFrameFromServer(serverTID, block uint16, payload []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: mask.ClientMAC, SrcMAC: mask.ServerMAC,
		SrcIP: mask.ServerIP, DstIP: mask.ClientIP,
		SrcPort: serverTID, DstPort: cliTID,
		Payload: tftp.BuildDATA(block, payload),
	})
}
