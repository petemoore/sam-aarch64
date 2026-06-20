// netboot_serve_wrq_record_test.go — i121f: host-verification of the serve unit's
// WRQ "disk-record push" path (src/netboot/netboot_serve.asm handle_wrq +
// handle_data + wd_finalize). A pushed .mgt disk image is streamed over TFTP WRQ
// into a free Trinity record (raw 512-byte sectors via the i122 raw_record_sink),
// validated as a Trinity disk record on the final block (size == 819,200 AND the
// BDOS stamp@232), and answered with the final ACK on success or ERROR(3) on a bad
// image. No free record → ERROR(3, "no free record") and nothing armed.
//
// These tests load the serve BOOT binary (netboot_serve_boot.bin — the only build
// carrying the bdos_seam.asm RST 8 hooks + raw_record_sink.asm, behind
// NETBOOT_HOSTTEST==0) and drive the REAL serve_serve_once dispatcher + the real
// HWSAD/HRSAD dispatch under AttachBDOS, so BDOSStore.SectorWrites() captures the
// writes. The frame replies + sector writes + BD_REC_VALID are asserted against
// the serve.Responder Go authority (Config.DiskRecordPush), which routes the body
// through the same bdos.RawSink + bdos.ValidateDiskRecord.
//
// Two tiers, mirroring netboot_fetch_boot_test.go's split (the vendored ENC SPI is
// bit-banged, so a full 819,200-byte image over the wire is impractically many
// frames):
//
//   - WIRE ROUTING through serve_serve_once — a small multi-block image proves the
//     WRQ handshake, the per-block DATA→ACK cadence, the sink routing into the
//     claimed record, and the final-block reply decision (here ERROR(3): a
//     sub-record image is invalid). The no-free-record ERROR(3) handshake is also
//     driven through the real dispatch.
//   - FULL-RECORD VALIDATE-GATES-REPLY — the body is streamed at 819,200-byte scale
//     straight through the sink (raw_record_sink_leaf, the path the wire feeds) and
//     wd_finalize is then driven to assert the valid→ACK / invalid→ERROR(3)
//     decision and BD_REC_VALID. (The sink is independently proven at full
//     1600-sector scale by TestRawRecordSinkMatchesGo.)
//
// THE HONESTY LINE (CLAUDE.md §5): the HWSAD handler models the digital dispatch
// (which record, which sector coordinates, which bytes), not a real SD write, and
// the real ENC28J60 silicon + a wire `tftp put` on real hardware stay gated on
// real Trinity. Emulation-verified is not hardware-verified.
package z80_test

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/serve"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// loadServeRecordPush loads the serve BOOT binary, attaches a BDOS store + card,
// fills CONFIG + inits the driver, models the card record list, and returns the
// machine, the ENC, the store, the card, and the matching Go authority with
// DiskRecordPush enabled. freeRecord is the record bdos_find_free_record will
// return (set up as the only free slot among `records`); pass freeRecord == 0 to
// drive the no-free-record case (every record named).
func loadServeRecordPush(t *testing.T, records, freeRecord int) (*z80h.Machine, *z80h.ENC28J60, *z80h.BDOSStore, *serve.Responder) {
	t.Helper()
	mac, err := z80h.Load(serveBootBin, serveBootMap)
	if err != nil {
		t.Skipf("serve boot binary not built (%v); run `make netboot-serve-boot`", err)
	}
	// CONFIG + driver init reuse the host-test demo helpers; they write by symbol,
	// which the boot map exports too. No served files are needed for a WRQ push
	// (resolve is RRQ-only); fillServeConfig fills the SAM's identity into CONFIG_*
	// so serve_serve_once wraps replies to the right endpoint.
	fillServeConfig(t, mac, []demoFile{{"hello.txt", makeFile(10), demoSrcOrgA}})

	enc := z80h.NewENC28J60()
	initServeDriver(t, mac, enc)

	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	store.AttachCard(card)
	mac.AttachBDOS(store)

	// Model the card record list: name every record except freeRecord, so
	// bdos_find_free_record returns freeRecord. freeRecord == 0 → all named.
	for n := 1; n <= records; n++ {
		if n == freeRecord {
			continue // leave free (all-zero list entry)
		}
		card.SetRecordName(n, "INUSE")
	}
	mac.WriteU16LE(symAddr(t, mac, "BD_RECORDS"), uint16(records))

	cfg := serve.Config{ServerMAC: demoServerMAC, ServerIP: demoServerIP, ServerTID: demoServerTID, DiskRecordPush: true}
	goRef := serve.New(cfg, tftp.MapStore{}, func(string) tftp.Source { return tftp.ByteSource(nil) })
	goRef.SetFreeRecordAvailable(freeRecord != 0)
	return mac, enc, store, goRef
}

// recordValidImage builds a RecordSize (819,200-byte) image with the BDOS stamp at
// offset 232 (so sector 0 validates) and a position-dependent fill elsewhere.
func recordValidImage() []byte {
	img := make([]byte, bdos.RecordSize)
	for i := range img {
		img[i] = byte(i*31 + 7)
	}
	copy(img[bdos.BDOSStampOffset:bdos.BDOSStampOffset+4], []byte("BDOS"))
	return img
}

// assertRecordSectors checks the captured HWSAD sector writes cover the expected
// record at consecutive linear indices 0..N-1 with the zero-padded image bytes.
func assertRecordSectors(t *testing.T, writes []z80h.SectorWrite, img []byte, record int) {
	t.Helper()
	wantSectors := (len(img) + bdos.SectorSize - 1) / bdos.SectorSize
	if len(writes) != wantSectors {
		t.Fatalf("SectorWrites() = %d, want %d", len(writes), wantSectors)
	}
	var got []byte
	for i, w := range writes {
		if w.Record != record {
			t.Fatalf("sector[%d] record = %d, want the claimed record %d", i, w.Record, record)
		}
		if w.LinearSec != i {
			t.Fatalf("sector[%d] linear = %d, want %d (consecutive from 0)", i, w.LinearSec, i)
		}
		got = append(got, w.Data[:]...)
	}
	want := make([]byte, wantSectors*bdos.SectorSize)
	copy(want, img) // tail of the final sector stays zero (the finish zero-pad)
	if !bytes.Equal(got, want) {
		t.Fatalf("streamed-into-record bytes != the zero-padded image (%d bytes)", len(got))
	}
}

// TestServeWRQRecordPushWireRouting drives a small multi-block image through the
// REAL serve_serve_once dispatcher over the ENC: the WRQ handshake, every DATA→ACK,
// the sink routing into the claimed free record (HWSAD per sector), and the
// final-block reply decision. The image is sub-record (not 819,200 bytes), so it is
// rejected at validation and the final reply is ERROR(3) in place of the final ACK
// — proving the invalid→ERROR(3) decision end-to-end through the dispatch. Every
// reply is asserted byte-for-byte against the Go authority.
func TestServeWRQRecordPushWireRouting(t *testing.T) {
	const records, freeRecord = 8, 4
	mac, enc, store, goRef := loadServeRecordPush(t, records, freeRecord)

	// A small image: two full 512-byte blocks + a short final tail → three DATA
	// blocks, ending on a short one (and far under the ENC RX ring's frame budget).
	const blksize = 512
	img := make([]byte, 2*blksize+200)
	for i := range img {
		img[i] = byte(i*13 + 5)
	}

	// 1. Bare WRQ (`tftp put` default) → ACK-0 handshake.
	wrq := demoWRQ("upload.mgt", nil)
	eqFrame(t, "WRQ → ACK-0", serveDemo(t, mac, enc, wrq), goRef.OnFrame(wrq))

	// 2. DATA blocks 1..N → an ACK per block; the final reply on the short block.
	var final []byte
	block := uint16(1)
	for off := 0; off < len(img); off += blksize {
		end := off + blksize
		if end > len(img) {
			end = len(img)
		}
		dataFrame := demoData(block, img[off:end])
		got := serveDemo(t, mac, enc, dataFrame)
		eqFrame(t, "DATA → reply", got, goRef.OnFrame(dataFrame))
		final = got

		// After block 1, replay it: a duplicate is re-ACKed without re-streaming.
		if block == 1 {
			dup := demoData(1, img[:blksize])
			eqFrame(t, "duplicate DATA 1 → re-ACK", serveDemo(t, mac, enc, dup), goRef.OnFrame(dup))
		}
		block++
	}

	// The body streamed into the claimed record, sector by sector.
	z80Writes := store.SectorWrites()
	assertRecordSectors(t, z80Writes, img, freeRecord)

	// Authority-vs-Z80: the Go RawSink (driven by the same WRQ DATA stream through
	// goRef.OnFrame above) emitted the identical sectors. The Go side carries no
	// Record field (the HRECORD selection supplies it at HWSAD time), so compare the
	// linear index + bytes.
	goWrites, goTotal, goDone, goValid := goRef.WRQPushOutcome()
	if !goDone {
		t.Fatal("Go authority did not mark the disk-record push complete")
	}
	if len(z80Writes) != len(goWrites) {
		t.Fatalf("Z80 emitted %d sectors, Go authority %d", len(z80Writes), len(goWrites))
	}
	for i := range goWrites {
		if z80Writes[i].LinearSec != goWrites[i].LinearSec {
			t.Errorf("sector[%d] linear = %d, Go authority %d", i, z80Writes[i].LinearSec, goWrites[i].LinearSec)
		}
		if !bytes.Equal(z80Writes[i].Data[:], goWrites[i].Data[:]) {
			t.Errorf("sector[%d] bytes != Go authority", i)
		}
	}
	if goTotal != len(img) {
		t.Errorf("Go authority streamed %d bytes, want %d", goTotal, len(img))
	}
	if goValid {
		t.Error("Go authority validated a sub-record image (must be invalid)")
	}

	// A sub-record image is invalid → BD_REC_VALID == 0, final reply ERROR(3).
	if v := mac.Read(symAddr(t, mac, "BD_REC_VALID"), 1)[0]; v != 0 {
		t.Errorf("BD_REC_VALID = %d, want 0 (a sub-record image must be rejected)", v)
	}
	pay := udpPayload(t, final)
	if tftp.Opcode(pay) != tftp.OpERROR {
		t.Fatalf("final reply opcode = %d, want ERROR(%d) — got %x", tftp.Opcode(pay), tftp.OpERROR, pay)
	}
	if code, _, err := tftp.ParseError(pay); err != nil || code != tftp.ErrDiskFull {
		t.Fatalf("final reply = ERROR code %d (err %v), want %d", code, err, tftp.ErrDiskFull)
	}
}

// TestServeWRQRecordPushNoFreeRecord drives the all-records-full case through the
// real dispatch: every record is named, so bdos_find_free_record returns 0, the WRQ
// is rejected with ERROR(3, "no free record") at the handshake, the receiver is NOT
// armed, no DATA is accepted, and zero sectors are written (a named record is never
// touched — the shared-resource invariant). The reply matches the Go authority.
func TestServeWRQRecordPushNoFreeRecord(t *testing.T) {
	const records, freeRecord = 4, 0 // 0 = no free slot; all 4 named
	mac, enc, store, goRef := loadServeRecordPush(t, records, freeRecord)

	wrq := demoWRQ("upload.mgt", nil)
	got := serveDemo(t, mac, enc, wrq)
	eqFrame(t, "no-free WRQ → ERROR(3)", got, goRef.OnFrame(wrq))

	pay := udpPayload(t, got)
	if tftp.Opcode(pay) != tftp.OpERROR {
		t.Fatalf("no-free reply opcode = %d, want ERROR(%d) — got %x", tftp.Opcode(pay), tftp.OpERROR, pay)
	}
	if code, _, err := tftp.ParseError(pay); err != nil || code != tftp.ErrDiskFull {
		t.Fatalf("no-free reply = ERROR code %d (err %v), want %d", code, err, tftp.ErrDiskFull)
	}

	// The receiver was NOT armed: a following DATA draws no reply and writes nothing.
	if active := mac.Read(symAddr(t, mac, "WRQ_RECV_ACTIVE"), 1)[0]; active != 0 {
		t.Errorf("WRQ_RECV_ACTIVE = %d, want 0 (no receiver armed when no record is free)", active)
	}
	if r := serveDemo(t, mac, enc, demoData(1, makeFile(512))); r != nil {
		t.Errorf("a DATA block after a no-free WRQ drew a reply: %x", r)
	}
	if w := store.SectorWrites(); len(w) != 0 {
		t.Fatalf("SectorWrites() = %d, want 0 (nothing written when no record is free)", len(w))
	}
}

// streamFullRecordAndFinalize arms a disk-record push on the boot binary, streams
// img straight through the sink at full scale via raw_record_sink_leaf (the path
// the wire feeds, but without the slow bit-banged ENC for every block), sets up the
// final-block state, and drives wd_finalize. It returns the transmitted final reply
// frame. This exercises the validate-gates-reply decision (wd_finalize) at the full
// 819,200-byte / 1600-sector scale; the wire routing into wd_finalize is covered by
// TestServeWRQRecordPushWireRouting. The pattern mirrors netboot_fetch_boot_test's
// fbStreamAndFinalize (stream via the sink, then call the finalize entry).
func streamFullRecordAndFinalize(t *testing.T, img []byte, record int) (*z80h.Machine, *z80h.BDOSStore, []byte) {
	t.Helper()
	mac, enc, store, _ := loadServeRecordPush(t, 8, record)

	// Arm a push exactly the way handle_wrq does on a bare WRQ + a successful claim:
	// drive the WRQ handshake through the real dispatch (on the already-inited ENC)
	// so CLIENT_* is learned (wd_finalize wraps the reply to it), the record is
	// selected, the sink reset, and the receiver armed in sink mode.
	wrq := demoWRQ("upload.mgt", nil)
	if got := serveDemo(t, mac, enc, wrq); got == nil {
		t.Fatal("WRQ handshake sent nothing")
	}

	// Stream the whole image through the sink at full scale (the chunking does not
	// change the emitted sectors — the sink re-blocks a byte stream).
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

	// Set the final-block state wd_finalize relies on: WRQ_ACKED is the last block
	// (wd_finalize reloads it to ACK on success). The exact value only has to be the
	// block wd_finalize ACKs; the validation reads RRS_TOTAL, set by the sink above.
	mac.WriteU16LE(symAddr(t, mac, "WRQ_ACKED"), 1600)

	before := len(enc.TXFrames())
	if _, err := mac.Call("wd_finalize"); err != nil {
		t.Fatalf("wd_finalize: %v", err)
	}
	tx := enc.TXFrames()
	if len(tx) != before+1 {
		t.Fatalf("wd_finalize transmitted %d frames, want 1", len(tx)-before)
	}
	return mac, store, tx[len(tx)-1]
}

// TestServeWRQRecordPushValidatesFull drives the full-record validate-gates-reply
// decision at 819,200-byte scale: a valid image → BD_REC_VALID == 1, the streamed
// record covers all 1600 sectors, and wd_finalize replies with the final ACK; a
// wrong-size and a stamp-less image → BD_REC_VALID == 0 and wd_finalize replies
// ERROR(3, "invalid disk record").
func TestServeWRQRecordPushValidatesFull(t *testing.T) {
	t.Run("valid 819200-byte image → ACK", func(t *testing.T) {
		const record = 4
		mac, store, final := streamFullRecordAndFinalize(t, recordValidImage(), record)

		if v := mac.Read(symAddr(t, mac, "BD_REC_VALID"), 1)[0]; v != 1 {
			t.Errorf("BD_REC_VALID = %d, want 1 (a valid 819200-byte BDOS image)", v)
		}
		// All 1600 sectors of the image landed in the claimed record.
		assertRecordSectors(t, store.SectorWrites(), recordValidImage(), record)

		pay := udpPayload(t, final)
		if tftp.Opcode(pay) != tftp.OpACK {
			t.Fatalf("final reply opcode = %d, want ACK(%d) — got %x", tftp.Opcode(pay), tftp.OpACK, pay)
		}
		if blk, err := tftp.ParseACK(pay); err != nil || blk != 1600 {
			t.Fatalf("final ACK block = %d (err %v), want 1600", blk, err)
		}
	})

	t.Run("wrong size → ERROR(3)", func(t *testing.T) {
		const record = 4
		img := recordValidImage()[:bdos.RecordSize-bdos.SectorSize] // one sector short
		mac, _, final := streamFullRecordAndFinalize(t, img, record)

		if v := mac.Read(symAddr(t, mac, "BD_REC_VALID"), 1)[0]; v != 0 {
			t.Errorf("BD_REC_VALID = %d, want 0 (wrong size rejected)", v)
		}
		assertErrorDiskFull(t, final)
	})

	t.Run("missing BDOS stamp → ERROR(3)", func(t *testing.T) {
		const record = 4
		img := recordValidImage()
		copy(img[bdos.BDOSStampOffset:bdos.BDOSStampOffset+4], []byte("XXXX")) // right size, no stamp
		mac, _, final := streamFullRecordAndFinalize(t, img, record)

		if v := mac.Read(symAddr(t, mac, "BD_REC_VALID"), 1)[0]; v != 0 {
			t.Errorf("BD_REC_VALID = %d, want 0 (missing stamp rejected)", v)
		}
		assertErrorDiskFull(t, final)
	})
}

func assertErrorDiskFull(t *testing.T, frame []byte) {
	t.Helper()
	pay := udpPayload(t, frame)
	if tftp.Opcode(pay) != tftp.OpERROR {
		t.Fatalf("final reply opcode = %d, want ERROR(%d) — got %x", tftp.Opcode(pay), tftp.OpERROR, pay)
	}
	if code, _, err := tftp.ParseError(pay); err != nil || code != tftp.ErrDiskFull {
		t.Fatalf("final reply = ERROR code %d (err %v), want %d", code, err, tftp.ErrDiskFull)
	}
}
