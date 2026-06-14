// Package z80_test, enc28j60_test.go — the i80 host-verification of the
// ENC28J60 wire I/O path. It runs the *real* vendored Trinity driver
// (src/netboot/encdrv.asm) under the flat-memory koron-go/z80 harness with the
// emulated Trinity (enc28j60.go) attached, and asserts:
//
//  1. drv_init completes successfully (BC=1) against the emulated board;
//  2. drv_write places the frame's bytes byte-exact on the virtual wire;
//  3. an injected RX frame comes back byte-exact through drv_read.
//
// This turns the previously hardware-gated ENC28J60 path into a host-verifiable
// one (i80). It is emulation verification, NOT hardware verification — real
// Trinity hardware remains the final integration gate (trinity-capabilities.md;
// CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	encBinPath = "../../../build/netboot_encdrv.bin"
	encMapPath = "../../../build/netboot_encdrv.map"
)

// testMAC is the SAM's MAC drv_init programs into the ENC; arbitrary but fixed.
var testMAC = [6]byte{0x02, 0x53, 0x41, 0x4D, 0x00, 0x01}

func loadEnc(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(encBinPath); err != nil {
		t.Skipf("ENC driver binary not built (%s); run `make netboot-encdrv`", encBinPath)
	}
	mac, err := z80h.Load(encBinPath, encMapPath)
	if err != nil {
		t.Fatalf("load encdrv: %v", err)
	}
	return mac
}

func sym(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return a
}

// initDriver attaches the emulated Trinity and runs drv_init with the test MAC.
func initDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	mac.AttachIO(enc)
	macAddr := sym(t, mac, "PARAM_MAC")
	mac.Write(macAddr, testMAC[:])
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_init returned BC=%d, want 1 (Trinity present + ENC ok)", res.BC)
	}
}

// TestDrvInitCompletes is increment goal (1): the real driver's init sequence —
// chk_trinity probe, ENC reset, MAC program, RX/TX pointer setup, PHY writes,
// RX enable — runs to a clean BC=1 against the emulated board.
func TestDrvInitCompletes(t *testing.T) {
	mac := loadEnc(t)
	enc := z80h.NewENC28J60()
	initDriver(t, mac, enc)
}

// TestDrvInitDetectsMissingBoard is the negative control: with no Trinity
// attached (inert IO), chk_trinity reads 0xFF for the 'T'/'R' signature and
// drv_init must fail (BC=0). Proves the success in TestDrvInitCompletes is the
// emulation responding, not the harness always returning success.
func TestDrvInitDetectsMissingBoard(t *testing.T) {
	mac := loadEnc(t)
	// Deliberately do NOT attach the ENC emulation: IO is inert (0xFF).
	macAddr := sym(t, mac, "PARAM_MAC")
	mac.Write(macAddr, testMAC[:])
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 0 {
		t.Fatalf("drv_init with no board returned BC=%d, want 0 (missing)", res.BC)
	}
}

// TestDrvWriteFrameOnWire is increment goal (2): a frame handed to drv_write
// lands byte-exact on the virtual wire (the emulated TX buffer).
func TestDrvWriteFrameOnWire(t *testing.T) {
	mac := loadEnc(t)
	enc := z80h.NewENC28J60()
	initDriver(t, mac, enc)

	// A small but full Ethernet frame: dst MAC, src MAC, ethertype, payload.
	frame := []byte{
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, // dst (broadcast)
		0x02, 0x53, 0x41, 0x4d, 0x00, 0x01, // src (our MAC)
		0x08, 0x06, // ethertype ARP
		// payload (arbitrary, 20 bytes)
		0x00, 0x01, 0x08, 0x00, 0x06, 0x04, 0x00, 0x01,
		0x02, 0x53, 0x41, 0x4d, 0x00, 0x01, 0x0a, 0x00,
		0x00, 0x01, 0xde, 0xad,
	}
	pkt := sym(t, mac, "PACKET")
	mac.Write(pkt, frame)
	res, err := mac.CallEntry("drv_write", z80h.Entry{HL: pkt, BC: uint16(len(frame))})
	if err != nil {
		t.Fatalf("call drv_write: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_write returned BC=%d, want 1 (success)", res.BC)
	}

	tx := enc.TXFrames()
	if len(tx) != 1 {
		t.Fatalf("emulated wire saw %d frames, want 1", len(tx))
	}
	if !bytes.Equal(tx[0], frame) {
		t.Errorf("transmitted frame != input\n  wire %x\n   in  %x", tx[0], frame)
	}
}

// TestDrvReadFrameFromWire is increment goal (3): an Ethernet frame injected on
// the virtual wire is returned byte-exact by drv_read, with BC = its length.
func TestDrvReadFrameFromWire(t *testing.T) {
	mac := loadEnc(t)

	inFrame := []byte{
		0x02, 0x53, 0x41, 0x4d, 0x00, 0x01, // dst (our MAC)
		0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, // src
		0x08, 0x00, // ethertype IPv4
		// payload (28 bytes)
		0x45, 0x00, 0x00, 0x1c, 0x00, 0x00, 0x40, 0x00,
		0x40, 0x11, 0x00, 0x00, 0x0a, 0x00, 0x00, 0x02,
		0x0a, 0x00, 0x00, 0x01, 0x12, 0x34, 0x56, 0x78,
		0xca, 0xfe, 0xba, 0xbe,
	}
	enc := z80h.NewENC28J60(inFrame)
	initDriver(t, mac, enc)

	pkt := sym(t, mac, "PACKET")
	res, err := mac.CallEntry("drv_read", z80h.Entry{HL: pkt})
	if err != nil {
		t.Fatalf("call drv_read: %v", err)
	}
	if int(res.BC) != len(inFrame) {
		t.Fatalf("drv_read returned BC=%d, want %d", res.BC, len(inFrame))
	}
	got := mac.Read(pkt, int(res.BC))
	if !bytes.Equal(got, inFrame) {
		t.Errorf("received frame != injected\n  got %x\n  inj %x", got, inFrame)
	}
}

// TestDrvWriteCapturedRRQOnWire exercises the path with a *real* netboot frame:
// the captured TFTP RRQ from the Pi 400 trace (golden.TFTPRrqRoot1024). It must
// reach the virtual wire byte-exact — proving the driver + emulation carry a
// genuine protocol frame, not just a toy.
func TestDrvWriteCapturedRRQOnWire(t *testing.T) {
	mac := loadEnc(t)
	enc := z80h.NewENC28J60()
	initDriver(t, mac, enc)

	frame := golden.TFTPRrqRoot1024
	pkt := sym(t, mac, "PACKET")
	mac.Write(pkt, frame)
	res, err := mac.CallEntry("drv_write", z80h.Entry{HL: pkt, BC: uint16(len(frame))})
	if err != nil {
		t.Fatalf("call drv_write: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_write returned BC=%d, want 1", res.BC)
	}
	tx := enc.TXFrames()
	if len(tx) != 1 {
		t.Fatalf("wire saw %d frames, want 1", len(tx))
	}
	if !bytes.Equal(tx[0], frame) {
		t.Errorf("captured RRQ not transmitted byte-exact\n  wire %x\n   in  %x", tx[0], frame)
	}
}

// TestDrvWriteThenReadLoopback ties the two halves together end to end: a frame
// the driver transmits is looped back onto the wire and the driver reads it
// back identically. This is the strongest single statement of the i80 brick —
// the whole ENC28J60 send+receive path is exercised against the real driver and
// the bytes survive the full round trip.
func TestDrvWriteThenReadLoopback(t *testing.T) {
	mac := loadEnc(t)
	enc := z80h.NewENC28J60()
	initDriver(t, mac, enc)

	frame := golden.DHCPDiscover // a real captured broadcast frame
	pkt := sym(t, mac, "PACKET")
	mac.Write(pkt, frame)
	wr, err := mac.CallEntry("drv_write", z80h.Entry{HL: pkt, BC: uint16(len(frame))})
	if err != nil || wr.BC != 1 {
		t.Fatalf("drv_write failed: BC=%d err=%v", wr.BC, err)
	}
	tx := enc.TXFrames()
	if len(tx) != 1 || !bytes.Equal(tx[0], frame) {
		t.Fatalf("transmitted frame mismatch")
	}

	// Loop the transmitted frame back onto the wire and receive it.
	enc.InjectRX(tx[0])
	// Clear the read buffer so a stale copy can't masquerade as a fresh read.
	mac.Write(pkt, make([]byte, len(frame)))
	rd, err := mac.CallEntry("drv_read", z80h.Entry{HL: pkt})
	if err != nil {
		t.Fatalf("call drv_read: %v", err)
	}
	if int(rd.BC) != len(frame) {
		t.Fatalf("drv_read returned BC=%d, want %d", rd.BC, len(frame))
	}
	got := mac.Read(pkt, int(rd.BC))
	if !bytes.Equal(got, frame) {
		t.Errorf("loopback frame mismatch\n  got %x\n  exp %x", got, frame)
	}
}

// TestDrvWriteSilentWireWhileLinkDown is the i131 silent-wire reproduction: a
// transmit issued while the PHY link is still down egresses NOTHING, yet the
// driver sees a clean transmit (drv_write returns BC=1). This is the exact
// hardware failure Pete saw — a proactive transmitter (ARP-before-link-up, no
// drv_wait_link) clocks its frame into the ENC before 10BASE-T link-pulse
// detection completes, the PHY drops it, and the driver, none the wiser, waits
// forever for a reply that can't come. It is the un-fixed half of i127; the
// fixed client (drv_wait_link first) recovers, asserted in the boot test.
//
// The same drv_write that put a frame on the wire in TestDrvWriteFrameOnWire is
// reused here, the only difference being the modelled link state — so the test
// isolates "link down" as the sole cause of the silent wire.
func TestDrvWriteSilentWireWhileLinkDown(t *testing.T) {
	mac := loadEnc(t)
	enc := z80h.NewENC28J60()
	initDriver(t, mac, enc)

	// Model the link as down from here on (resets the op counter; the threshold
	// is far beyond any op count drv_write reaches, so it never comes up during
	// this test). This is the proactive case: no drv_wait_link precedes the send.
	enc.SetLinkUpAfterOps(1 << 30)

	frame := []byte{
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, // dst (broadcast)
		0x02, 0x53, 0x41, 0x4d, 0x00, 0x01, // src (our MAC)
		0x08, 0x06, // ethertype ARP
		0x00, 0x01, 0x08, 0x00, 0x06, 0x04, 0x00, 0x01,
		0x02, 0x53, 0x41, 0x4d, 0x00, 0x01, 0x0a, 0x00,
		0x00, 0x01, 0xde, 0xad,
	}
	pkt := sym(t, mac, "PACKET")
	mac.Write(pkt, frame)
	res, err := mac.CallEntry("drv_write", z80h.Entry{HL: pkt, BC: uint16(len(frame))})
	if err != nil {
		t.Fatalf("call drv_write: %v", err)
	}
	// The driver believes it succeeded — TXIF set, TXRTS cleared, clean status —
	// which is precisely why the loss is silent and the client then livelocks.
	if res.BC != 1 {
		t.Fatalf("drv_write returned BC=%d, want 1 (the driver sees a clean transmit even though the link silently dropped the frame)", res.BC)
	}
	if n := len(enc.TXFrames()); n != 0 {
		t.Fatalf("link down: %d frames egressed, want 0 — a TX before link-up must be silently dropped (i127 silent-wire reproduction)", n)
	}

	// Bring the link up and transmit the same frame: now it reaches the wire
	// byte-exact. Proves the drop is gated solely on link state, not a broken
	// transmit path (and that drv_wait_link's job — waiting for this — is real).
	enc.SetLinkUpAfterOps(0)
	mac.Write(pkt, frame)
	res, err = mac.CallEntry("drv_write", z80h.Entry{HL: pkt, BC: uint16(len(frame))})
	if err != nil {
		t.Fatalf("call drv_write (link up): %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_write (link up) returned BC=%d, want 1", res.BC)
	}
	tx := enc.TXFrames()
	if len(tx) != 1 {
		t.Fatalf("link up: %d frames egressed, want exactly 1 (only the post-link-up send)", len(tx))
	}
	if !bytes.Equal(tx[0], frame) {
		t.Errorf("post-link-up frame != input\n  wire %x\n   in  %x", tx[0], frame)
	}
}

// TestDrvReadEmptyReturnsZero: with nothing on the wire, drv_read returns BC=0
// (the EPKTCNT==0 path) rather than garbage.
func TestDrvReadEmptyReturnsZero(t *testing.T) {
	mac := loadEnc(t)
	enc := z80h.NewENC28J60() // no frames queued
	initDriver(t, mac, enc)

	pkt := sym(t, mac, "PACKET")
	res, err := mac.CallEntry("drv_read", z80h.Entry{HL: pkt})
	if err != nil {
		t.Fatalf("call drv_read: %v", err)
	}
	if res.BC != 0 {
		t.Fatalf("drv_read with empty wire returned BC=%d, want 0", res.BC)
	}
}
