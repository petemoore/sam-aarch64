// netboot_serve_dbg_test.go — i271: host-verification of the NETBOOT_DEBUG network
// debug step-markers (src/netboot/dbg_marker.asm). The serve boot binary built with
// -D NETBOOT_DEBUG (netboot_serve_boot_debug.bin) broadcasts a small "SDBG" UDP
// packet at each WRQ / SD-write step, so an agent reading the wire (tcpdump / a UDP
// listener on port 9001) sees exactly how far the SAM got and a hang localizes to
// the last marker seen — the i270 debug bottleneck removed.
//
// These tests load the DEBUG boot binary (not the production netboot_serve_boot.bin,
// which is byte-identical to before — the markers are additive, only under
// NETBOOT_DEBUG) and drive the REAL serve_serve_once dispatcher over the ENC with
// the same SD + BDOS + record-list setup as netboot_serve_wrq_record_test.go. Each
// serve call now emits marker frames *in addition to* the reply, so these tests use
// their own frame collector (driveDbg) rather than serveDemo (which asserts exactly
// one TX frame). The markers are asserted to appear in the expected order; the reply
// opcode is checked too, but byte-for-byte reply fidelity vs the Go authority is the
// non-debug wire test's job — here the assertion is the debug channel itself.
//
// THE HONESTY LINE (CLAUDE.md §5): this proves the markers are EMITTED in emulation;
// using them to localize a real hang stays gated on a real-hardware serve run (i270).
package z80_test

import (
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	serveBootDebugBin = "../../../build/netboot_serve_boot_debug.bin"
	serveBootDebugMap = "../../../build/netboot_serve_boot_debug.map"
)

// Marker codes — must match src/netboot/dbg_marker.asm.
const (
	dbgWRQEntry      = 0x10
	dbgWRQClaimed    = 0x11
	dbgWRQNoFree     = 0x12
	dbgWRQHandshake  = 0x13
	dbgDataBlock     = 0x20
	dbgFinalize      = 0x30
	dbgFinalizeValid = 0x31
	dbgFinalizeBad   = 0x32
	dbgDoneCtrl      = 0x40
)

// dbgMarkerCode returns (code, true) if f is an "SDBG" debug-marker frame. The UDP
// payload sits at offset 42 (Ethernet 14 + IPv4 20 + UDP 8): magic "SDBG" (4) +
// version (1) + marker code (1).
func dbgMarkerCode(f []byte) (byte, bool) {
	const payOff = 42
	if len(f) < payOff+6 {
		return 0, false
	}
	if string(f[payOff:payOff+4]) != "SDBG" {
		return 0, false
	}
	if f[payOff+4] != 1 {
		return 0, false // unexpected version — not a marker we recognize
	}
	return f[payOff+5], true
}

// driveDbg injects req, runs serve_serve_once once, and returns the marker codes
// emitted (in order) plus the single non-marker reply frame (nil if none).
func driveDbg(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, req []byte) (markers []byte, reply []byte) {
	t.Helper()
	before := len(enc.TXFrames())
	if req != nil {
		enc.InjectRX(req)
	}
	if _, err := mac.Call("serve_serve_once"); err != nil {
		t.Fatalf("call serve_serve_once: %v", err)
	}
	for _, f := range enc.TXFrames()[before:] {
		if code, ok := dbgMarkerCode(f); ok {
			markers = append(markers, code)
		} else {
			reply = f
		}
	}
	return markers, reply
}

// wantMarkers asserts the marker sequence equals want, with a readable message.
func wantMarkers(t *testing.T, label string, got []byte, want ...byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: markers = %#x, want %#x", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: markers = %#x, want %#x (differ at %d)", label, got, want, i)
		}
	}
}

// TestServeDebugMarkersWRQRecordPush drives a sub-record disk-record push over the
// wire through the DEBUG dispatcher and asserts the full marker pipeline: the WRQ
// handshake (entry → claimed → handshake), a marker per accepted DATA block, and the
// final block's finalize → invalid (a sub-record image is rejected). The replies are
// the same ACK-0 / ACK / ERROR(3) the non-debug wire test asserts.
func TestServeDebugMarkersWRQRecordPush(t *testing.T) {
	const records, freeRecord = 8, 4
	mac, enc, _, _, _ := loadServeRecordPushBin(t, serveBootDebugBin, serveBootDebugMap, records, freeRecord)

	const blksize = 512
	img := make([]byte, 2*blksize+200) // three DATA blocks, the last short → final
	for i := range img {
		img[i] = byte(i*13 + 5)
	}

	// 1. Bare WRQ (disk-record class via the prefix) → entry, claimed, handshake; ACK-0.
	markers, reply := driveDbg(t, mac, enc, demoWRQ("trinity-sam-disks/upload.mgt", nil))
	wantMarkers(t, "WRQ", markers, dbgWRQEntry, dbgWRQClaimed, dbgWRQHandshake)
	if op := tftp.Opcode(udpPayload(t, reply)); op != tftp.OpACK {
		t.Fatalf("WRQ reply opcode = %d, want ACK(%d)", op, tftp.OpACK)
	}

	// 2. DATA blocks: one DATA_BLOCK marker each; the final short block also finalizes.
	block := uint16(1)
	var finalReply []byte
	for off := 0; off < len(img); off += blksize {
		end := off + blksize
		if end > len(img) {
			end = len(img)
		}
		markers, reply = driveDbg(t, mac, enc, demoData(block, img[off:end]))
		if end-off < blksize {
			// final short block → DATA_BLOCK, FINALIZE, FINALIZE_BAD (sub-record image)
			wantMarkers(t, "final DATA", markers, dbgDataBlock, dbgFinalize, dbgFinalizeBad)
			finalReply = reply
		} else {
			wantMarkers(t, "DATA", markers, dbgDataBlock)
			if op := tftp.Opcode(udpPayload(t, reply)); op != tftp.OpACK {
				t.Fatalf("DATA %d reply opcode = %d, want ACK(%d)", block, op, tftp.OpACK)
			}
		}
		block++
	}
	assertErrorDiskFull(t, finalReply) // sub-record image → ERROR(3) in place of the final ACK
}

// TestServeDebugMarkersNoFreeRecord asserts the no-free-record WRQ path emits
// entry → no-free and replies ERROR(3), nothing armed.
func TestServeDebugMarkersNoFreeRecord(t *testing.T) {
	const records, freeRecord = 8, 0 // 0 → every record named, none free
	mac, enc, _, _, _ := loadServeRecordPushBin(t, serveBootDebugBin, serveBootDebugMap, records, freeRecord)

	markers, reply := driveDbg(t, mac, enc, demoWRQ("trinity-sam-disks/upload.mgt", nil))
	wantMarkers(t, "WRQ no-free", markers, dbgWRQEntry, dbgWRQNoFree)
	assertErrorDiskFull(t, reply)
}

// TestServeDebugMarkersDoneCtrl asserts the reserved "tftp.done" control name emits
// entry → done-ctrl (checked before any claim) and replies ACK-0.
func TestServeDebugMarkersDoneCtrl(t *testing.T) {
	const records, freeRecord = 8, 4
	mac, enc, _, _, _ := loadServeRecordPushBin(t, serveBootDebugBin, serveBootDebugMap, records, freeRecord)

	markers, reply := driveDbg(t, mac, enc, demoWRQ("tftp.done", nil))
	wantMarkers(t, "tftp.done", markers, dbgWRQEntry, dbgDoneCtrl)
	if op := tftp.Opcode(udpPayload(t, reply)); op != tftp.OpACK {
		t.Fatalf("tftp.done reply opcode = %d, want ACK(%d)", op, tftp.OpACK)
	}
}

// TestServeDebugMarkersFinalizeValid drives a VALID full-record push (the WRQ
// handshake over the wire, the 819,200-byte image streamed straight through the sink,
// then wd_finalize directly — the runFullPush pattern, the wire can't carry 1600
// frames practically) and asserts the valid finalize emits FINALIZE → FINALIZE_VALID
// and the final ACK.
func TestServeDebugMarkersFinalizeValid(t *testing.T) {
	const records, freeRecord = 8, 4
	mac, enc, _, _, _ := loadServeRecordPushBin(t, serveBootDebugBin, serveBootDebugMap, records, freeRecord)

	// WRQ handshake (sets PARSE_FILENAME for the claim, arms the disk-record sink).
	markers, _ := driveDbg(t, mac, enc, demoWRQ("trinity-sam-disks/goodisk.mgt", nil))
	wantMarkers(t, "valid WRQ", markers, dbgWRQEntry, dbgWRQClaimed, dbgWRQHandshake)

	// Stream the whole valid image through the sink at full scale.
	img := recordValidImage()
	for off := 0; off < len(img); {
		end := off + rrsSliceMax
		if end > len(img) {
			end = len(img)
		}
		slice := img[off:end]
		mac.Write(rrsScratch, slice)
		if _, err := mac.CallEntry("raw_record_sink_leaf", z80h.Entry{HL: rrsScratch, BC: uint16(len(slice))}); err != nil {
			t.Fatalf("raw_record_sink_leaf @%d: %v", off, err)
		}
		off = end
	}
	mac.WriteU16LE(symAddr(t, mac, "WRQ_ACKED"), uint16(bdos.RecordSize/512))

	// Drive wd_finalize directly and collect its markers + the final reply.
	before := len(enc.TXFrames())
	if _, err := mac.Call("wd_finalize"); err != nil {
		t.Fatalf("wd_finalize: %v", err)
	}
	var fm []byte
	var fReply []byte
	for _, f := range enc.TXFrames()[before:] {
		if code, ok := dbgMarkerCode(f); ok {
			fm = append(fm, code)
		} else {
			fReply = f
		}
	}
	wantMarkers(t, "valid finalize", fm, dbgFinalize, dbgFinalizeValid)
	if blk, err := tftp.ParseACK(udpPayload(t, fReply)); err != nil || blk != uint16(bdos.RecordSize/512) {
		t.Fatalf("valid finalize reply = ACK %d (err %v), want ACK %d", blk, err, bdos.RecordSize/512)
	}
}
