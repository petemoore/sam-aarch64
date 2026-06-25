// netboot_fetch_boot_test.go — i122c: host-verification of the PXE-style
// fetch-and-boot orchestration (src/netboot/netboot_client.asm client_fetch_boot
// + client_finalize), the capstone composing the four i122 bricks: i82 (TFTP
// fetch loop) + i122b (raw-record streaming write) + i114c (disk-record validate)
// + i122a (boot-a-record).
//
// Two groups:
//
//  1. client_finalize (the validate-gates-boot decision) — stream an image into a
//     record via the REAL sink, then call client_finalize and assert it boots the
//     record iff the image validates (size == 819200 AND BDOS stamp@232). Covers
//     valid → boot, wrong size → reject, missing stamp → reject.
//
//  2. client_fetch_boot (the whole boot wrapper, CLAUDE.md §7) — RunBoot the real
//     bootable entry end-to-end: EEPROM identity read, drv_init/link, ARP, RRQ,
//     OACK, the DATA/ACK loop streaming the body straight into the scratch record
//     (the sink routing), then client_finalize. Exercises every line of the
//     wrapper in emulation. The valid→boot decision and the full-record sink scale
//     are covered by group 1 + the raw_record_sink tests (see the test's own note
//     on why the full 819,200-byte VALID stream isn't driven through the slow ENC).
//
// THE HONESTY LINE (CLAUDE.md §5): the harness models the digital dispatch — HWSAD
// sector writes, the HRSAD readback, and ALHK as a captured boot event that
// RETURNS (on hardware ALHK never returns — it boots the AUTO file). So in
// emulation a valid run reaches client_fetch_boot's post-boot halt too; the
// boot is asserted via store.Boots(), not the border. Emulation-verified is not
// hardware-verified.
package z80_test

import (
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// fbValidImage builds a RecordSize (819,200-byte) disk image with the BDOS stamp
// at offset 232 (so sector 0 validates) and a position-dependent fill elsewhere.
func fbValidImage() []byte {
	img := make([]byte, bdos.RecordSize)
	for i := range img {
		img[i] = byte(i*31 + 7)
	}
	copy(img[bdos.BDOSStampOffset:bdos.BDOSStampOffset+4], []byte("BDOS"))
	return img
}

// ---------------------------------------------------------------------------
// Group 1 — client_finalize (validate-gates-boot), streamed through the sink.
// ---------------------------------------------------------------------------

// fbStreamAndFinalize loads a fresh boot binary, attaches a BDOS store + card,
// HRECORD-selects record, streams img through the REAL raw_record_sink (so
// RRS_TOTAL and the on-card sectors are set exactly as a fetch would set them),
// points BD_BOOT_RECORD at the record, then calls client_finalize. It returns the
// store so the caller can assert the boot decision.
func fbStreamAndFinalize(t *testing.T, img []byte, record int) (*z80h.Machine, *z80h.BDOSStore) {
	t.Helper()
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Fatalf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	store.AttachCard(card)
	mac.AttachBDOS(store)

	if _, err := mac.CallEntry("bdos_select_record", z80h.Entry{A: byte(record)}); err != nil {
		t.Fatalf("bdos_select_record(%d): %v", record, err)
	}
	if _, err := mac.Call("raw_record_sink_reset"); err != nil {
		t.Fatalf("raw_record_sink_reset: %v", err)
	}
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

	mac.Write(symAddr(t, mac, "BD_BOOT_RECORD"), []byte{byte(record)})
	if _, err := mac.Call("client_finalize"); err != nil {
		t.Fatalf("client_finalize: %v", err)
	}
	return mac, store
}

func TestClientFinalizeBootsValidImage(t *testing.T) {
	const record = 6
	mac, store := fbStreamAndFinalize(t, fbValidImage(), record)

	if got := mac.Read(symAddr(t, mac, "CLIENT_BOOT_RESULT"), 1)[0]; got != 1 {
		t.Errorf("CLIENT_BOOT_RESULT = %d, want 1 (valid image accepted)", got)
	}
	boots := store.Boots()
	if len(boots) != 1 || boots[0] != record {
		t.Fatalf("Boots() = %v, want [%d] (the scratch record booted)", boots, record)
	}
}

func TestClientFinalizeRejectsWrongSize(t *testing.T) {
	const record = 6
	img := fbValidImage()[:bdos.RecordSize-512] // one sector short: size != 819200
	mac, store := fbStreamAndFinalize(t, img, record)

	if got := mac.Read(symAddr(t, mac, "CLIENT_BOOT_RESULT"), 1)[0]; got != 0 {
		t.Errorf("CLIENT_BOOT_RESULT = %d, want 0 (wrong size rejected)", got)
	}
	if boots := store.Boots(); len(boots) != 0 {
		t.Fatalf("Boots() = %v, want [] (a wrong-size image must NOT boot)", boots)
	}
}

func TestClientFinalizeRejectsMissingStamp(t *testing.T) {
	const record = 6
	img := fbValidImage()
	copy(img[bdos.BDOSStampOffset:bdos.BDOSStampOffset+4], []byte("XXXX")) // right size, no BDOS stamp
	mac, store := fbStreamAndFinalize(t, img, record)

	if got := mac.Read(symAddr(t, mac, "CLIENT_BOOT_RESULT"), 1)[0]; got != 0 {
		t.Errorf("CLIENT_BOOT_RESULT = %d, want 0 (missing stamp rejected)", got)
	}
	if boots := store.Boots(); len(boots) != 0 {
		t.Fatalf("Boots() = %v, want [] (a stamp-less image must NOT boot)", boots)
	}
}

// ---------------------------------------------------------------------------
// Group 2 — client_fetch_boot end-to-end (the whole boot wrapper, §7).
// ---------------------------------------------------------------------------

var (
	fbSamMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44} // the SAM's EEPROM identity
	fbSamIP  = [4]byte{192, 168, 0, 44}
	fbSrvMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01} // the TFTP server (cl_server_ip = 192.168.0.1)
)

const (
	fbSrvTID    = 40136
	fbClientTID = 30574 // client_setup's hardcoded ephemeral port
	fbBlksize   = 1428
)

// fbDataFrame / fbOackFrame / fbArpReply build the wire frames the server sends
// the SAM, addressed to the EEPROM identity (fbSamMAC/fbSamIP) from 192.168.0.1.
func fbArpReply() []byte {
	return frame.BuildARPReply(fbSrvMAC, fbSamMAC, frame.IPv4{192, 168, 0, 1}, frame.IPv4(fbSamIP))
}
func fbOackFrame() []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: fbSamMAC, SrcMAC: fbSrvMAC, SrcIP: frame.IPv4{192, 168, 0, 1}, DstIP: frame.IPv4(fbSamIP),
		SrcPort: fbSrvTID, DstPort: fbClientTID,
		Payload: tftp.BuildOACK([]tftp.Option{{Name: "blksize", Value: "1428"}, {Name: "tsize", Value: "819200"}, {Name: "timeout", Value: "2"}}),
	})
}
func fbDataFrame(block uint16, payload []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: fbSamMAC, SrcMAC: fbSrvMAC, SrcIP: frame.IPv4{192, 168, 0, 1}, DstIP: frame.IPv4(fbSamIP),
		SrcPort: fbSrvTID, DstPort: fbClientTID,
		Payload: tftp.BuildDATA(block, payload),
	})
}

// fbQueueTransfer queues the whole server side of the fetch into the ENC: the ARP
// reply, the OACK (blksize 1428), then the image split into 1428-byte DATA blocks
// with a short final block ending the transfer.
func fbQueueTransfer(enc *z80h.ENC28J60, img []byte) {
	enc.InjectRX(fbArpReply())
	enc.InjectRX(fbOackFrame())
	block := uint16(1)
	for off := 0; off < len(img); off += fbBlksize {
		end := off + fbBlksize
		if end > len(img) {
			end = len(img)
		}
		enc.InjectRX(fbDataFrame(block, img[off:end]))
		block++
	}
	// If the image is an exact multiple of the blksize, the transfer ends on a
	// zero-byte final block. RecordSize (819200) is not a multiple of 1428, so the
	// last block above is already short — no extra empty block needed.
}

// fbRunFetchBoot loads the boot binary the faithful way (flat all-RAM: section D is
// RAM at boot, where client_boot's i145b CSD overlay lives — see the romBaseBoot
// note in netboot_boot_test.go), programs the EEPROM identity, attaches the ENC + a
// BDOS store/card, points cl_boot_record at the scratch record, queues the transfer,
// and RunBoots client_fetch_boot.
func fbRunFetchBoot(t *testing.T, img []byte, record int) (*z80h.Machine, *z80h.BDOSStore, z80h.CallResult) {
	t.Helper()
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Fatalf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(fbSamMAC, fbSamIP)
	mac.AttachIO(enc)
	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	store.AttachCard(card)
	mac.AttachBDOS(store)

	mac.Write(symAddr(t, mac, "cl_boot_record"), []byte{byte(record)})
	fbQueueTransfer(enc, img)

	res, err := mac.RunBoot("client_fetch_boot", z80h.Entry{StepCap: 5_000_000})
	if err != nil {
		t.Fatalf("RunBoot client_fetch_boot: %v", err)
	}
	return mac, store, res
}

// TestClientFetchBootWrapperEndToEnd is the §7 integration check: the REAL
// bootable client_fetch_boot runs every line in emulation — EEPROM identity read,
// drv_init/link, ARP, RRQ, OACK (blksize 1428), the DATA/ACK loop streaming the
// body STRAIGHT into the scratch record via CLIENT_SINK_MODE=1 (the i122b sink,
// HWSAD per sector), then client_finalize (flush + validate + boot decision).
//
// It drives a multi-block-but-sub-record image, which the wrapper streams to the
// record (proving the sink ROUTING in the real boot path) and then REJECTS at
// validation (size != 819,200) — the corrupt-disk-record guard, end-to-end.
//
// WHY NOT a full 819,200-byte VALID run through this wrapper: the vendored ENC
// SPI driver is bit-banged, ~375 emulated steps per received byte, so a full
// record over the wire is ~300M steps (~40s) — impractical for a fast test, and
// it adds NO new logic coverage: the receive loop is block-count-independent
// (i82's netboot_client_test verifies multi-block receive), the sink is exercised
// at the full 819,200-byte / 1600-sector scale by TestRawRecordSinkMatchesGo and
// the finalize valid→boot decision by TestClientFinalizeBootsValidImage (both
// stream a real full image through the sink). This wrapper run covers the glue
// those two don't: the EEPROM→ARP→RRQ→OACK→sink-routing→finalize wiring.
func TestClientFetchBootWrapperEndToEnd(t *testing.T) {
	const record = 2
	img := fbValidImage()[:fbBlksize*3+10] // 3 full blocks + a short final → completes fast
	mac, store, res := fbRunFetchBoot(t, img, record)
	t.Logf("client_fetch_boot: halted=%v PC=&%04X steps=%d", res.Halted, res.PC, res.Steps)

	// The whole transfer ran to completion in the real boot wrapper.
	if got := mac.Read(symAddr(t, mac, "XFER_DONE"), 1)[0]; got != 1 {
		t.Fatalf("XFER_DONE = %d, want 1 (the transfer did not complete)", got)
	}

	// The sink ROUTING fired in the real boot path: every body byte was streamed
	// into the scratch record sector-by-sector (not into STAGING). After finish,
	// the image occupies ceil(len/512) sectors, zero-padded.
	wantSectors := (len(img) + bdos.SectorSize - 1) / bdos.SectorSize
	writes := store.SectorWrites()
	if len(writes) != wantSectors {
		t.Fatalf("SectorWrites() = %d, want %d (the body must stream into the record)", len(writes), wantSectors)
	}
	for i, w := range writes {
		if w.Record != record {
			t.Fatalf("sector[%d] written to record %d, want the scratch record %d", i, w.Record, record)
		}
		if w.LinearSec != i {
			t.Fatalf("sector[%d] linear = %d, want %d (consecutive from 0)", i, w.LinearSec, i)
		}
	}
	// Reconstruct the streamed bytes from the record and confirm fidelity (the
	// final sector is zero-padded past the image tail).
	var got []byte
	for _, w := range writes {
		got = append(got, w.Data[:]...)
	}
	want := make([]byte, wantSectors*bdos.SectorSize)
	copy(want, img)
	if string(got) != string(want) {
		t.Fatalf("streamed-into-record bytes != the zero-padded image (%d bytes)", len(got))
	}

	// A sub-record image is not a valid disk record → rejected, NOT booted.
	if got := mac.Read(symAddr(t, mac, "CLIENT_BOOT_RESULT"), 1)[0]; got != 0 {
		t.Fatalf("CLIENT_BOOT_RESULT = %d, want 0 (a sub-record image must be rejected)", got)
	}
	if boots := store.Boots(); len(boots) != 0 {
		t.Fatalf("Boots() = %v, want [] (a rejected image must NOT boot)", boots)
	}
}
