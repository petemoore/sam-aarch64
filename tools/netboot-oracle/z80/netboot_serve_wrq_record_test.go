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
// HWSAD dispatch under AttachBDOS, so BDOSStore.SectorWrites() captures the
// writes. The frame replies + sector writes + BD_REC_VALID are asserted against
// the serve.Responder Go authority (Config.DiskRecordPush), which routes the body
// through the same bdos.RawSink + bdos.ValidateDiskRecord.
//
// List-sector reads and writes use real SPI CMD17/CMD24 (NETBOOT_REAL_LISTREAD=1):
// the SD model (sdcard.go) has list sectors seeded at card-absolute LBAs so CMD17
// reads return the expected record-list sectors, and CMD24 writes are captured via
// SDCard.CapturedSector. The HWSAD (record data write) hook still goes through
// BDOSStore.SectorWrites, and the HRECORD hook (bdos_select_record) still uses
// BDOSStore.Selected — only list-sector I/O moved to real SPI.
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
	"strings"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/serve"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// sdSlotEntry returns the 16-byte list entry for record n (1-based) from the
// SD model's captured sector at LBA listSec. Returns nil if no CMD24 write was
// captured for that LBA.
func sdSlotEntry(sd *z80h.SDCard, listSec, n int) []byte {
	sec, ok := sd.CapturedSector(uint32(listSec))
	if !ok {
		return nil
	}
	off := (n - 1) * 16
	return sec[off : off+16]
}

// sdSlotName returns the trimmed B-DOS record name for record n from the SD
// model's captured LBA listSec. Free records (slot[0]&0x7F == 0) return "".
// The full 16-byte entry is used (B-DOS record names occupy the whole 16-byte
// slot, with bit 7 masked and trailing spaces trimmed — matching applyListWrite).
func sdSlotName(sd *z80h.SDCard, listSec, n int) string {
	entry := sdSlotEntry(sd, listSec, n)
	if entry == nil || entry[0]&0x7F == 0 {
		return ""
	}
	name := make([]byte, 16)
	for i := range name {
		name[i] = entry[i] & 0x7F
	}
	return strings.TrimRight(string(name), " ")
}

// loadServeRecordPush loads the serve BOOT binary, attaches a BDOS store + card,
// fills CONFIG + inits the driver, models the card record list, seeds the SD model,
// and returns the machine, the ENC, the store, the SD card, and the matching Go
// authority with DiskRecordPush enabled. freeRecord is the record
// bdos_find_free_record will return (set up as the only free slot among `records`);
// pass freeRecord == 0 to drive the no-free-record case (every record named).
func loadServeRecordPush(t *testing.T, records, freeRecord int) (*z80h.Machine, *z80h.ENC28J60, *z80h.BDOSStore, *z80h.SDCard, *serve.Responder) {
	return loadServeRecordPushBin(t, serveBootBin, serveBootMap, records, freeRecord)
}

// loadServeRecordPushBin is loadServeRecordPush parameterized by binary path, so a
// variant build (e.g. the i271 NETBOOT_DEBUG serve, netboot_serve_dbg_test.go) can
// reuse the identical SD + BDOS + record-list setup.
func loadServeRecordPushBin(t *testing.T, bin, mapf string, records, freeRecord int) (*z80h.Machine, *z80h.ENC28J60, *z80h.BDOSStore, *z80h.SDCard, *serve.Responder) {
	t.Helper()
	mac, err := z80h.Load(bin, mapf)
	if err != nil {
		t.Fatalf("serve boot binary not built (%v); run `make netboot-serve-boot`", err)
	}
	// CONFIG + driver init reuse the host-test demo helpers; they write by symbol,
	// which the boot map exports too. No served files are needed for a WRQ push
	// (resolve is RRQ-only); fillServeConfig fills the SAM's identity into CONFIG_*
	// so serve_serve_once wraps replies to the right endpoint.
	// Use 0xD000 (above the binary tail ~&C2EB) to avoid overwriting sdc_init_ladder.
	fillServeConfig(t, mac, []demoFile{{"hello.txt", makeFile(10), 0xD000}})

	enc := z80h.NewENC28J60()
	// Attach SD before initServeDriver so serve_main's CSD read path is ready.
	sd := enc.AttachSD(csdV2(1))
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

	// Seed SD list sectors from the card model. records <= 8 in all callers,
	// so only list sector 1 (LBA 1) is needed (records 1..32 → sector 1).
	maxListSec := (records + 31) / 32
	for ls := 1; ls <= maxListSec; ls++ {
		sec := card.ListSector(ls)
		sd.SeedSector(uint32(ls), sec[:])
	}
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("csd_set_bd_records: %v", err)
	}
	// Boot-faithful re-arm: the CSD read disturbed the ENC RX path (i249); serve_main
	// re-arms here before its serve loop (netboot_serve.asm:1391-1402), so the host
	// test must too, or serve_serve_once receives nothing.
	reArmENCRX(t, mac)
	mac.WriteU16LE(symAddr(t, mac, "BD_RECORDS"), uint16(records))

	cfg := serve.Config{ServerMAC: demoServerMAC, ServerIP: demoServerIP, ServerTID: demoServerTID, DiskRecordPush: true}
	goRef := serve.New(cfg, tftp.MapStore{}, func(string) tftp.Source { return tftp.ByteSource(nil) })
	goRef.SetFreeRecordAvailable(freeRecord != 0)
	return mac, enc, store, sd, goRef
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
	mac, enc, store, _, goRef := loadServeRecordPush(t, records, freeRecord)

	// A small image: two full 512-byte blocks + a short final tail → three DATA
	// blocks, ending on a short one (and far under the ENC RX ring's frame budget).
	const blksize = 512
	img := make([]byte, 2*blksize+200)
	for i := range img {
		img[i] = byte(i*13 + 5)
	}

	// 1. Bare WRQ (`tftp put` default) → ACK-0 handshake.
	wrq := demoWRQ("trinity-sam-disks/upload.mgt", nil)
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

// TestServeWRQFlatFilePush is the i121c FlatFile-class E2E gate, mirroring
// TestServeWRQRecordPushWireRouting but for the DEFAULT (non-prefixed) storage class:
// a WRQ filename WITHOUT the "trinity-sam-disks/" prefix classifies as FlatFile
// (bdos.Classify), so the body is flat-accumulated into WRQ_STAGING and, on the short
// final block, HSAVE'd into the claimed free record as a plain file (wd_finalize_flat
// → bdos_save_hook + bdos_claim_record) — no 819,200-byte disk-record validation. The
// transfer is driven through the REAL serve_serve_once dispatch over the ENC, every
// reply asserted byte-for-byte against the Go authority's finalizeFlat, and the HSAVE
// asserted via the harness BDOSStore.Saves (record, dotted-suffix-dropped name, the
// staged bytes), with a final ACK (not ERROR) on the short block.
func TestServeWRQFlatFilePush(t *testing.T) {
	const records, freeRecord = 8, 4
	mac, enc, store, _, goRef := loadServeRecordPush(t, records, freeRecord)

	// A small file that fits the flat staging window: one full 512-byte block + a
	// short 200-byte tail → two DATA blocks, ending short.
	const blksize = 512
	file := make([]byte, blksize+200)
	for i := range file {
		file[i] = byte(i*23 + 9)
	}
	name := "notes.dat" // NON-prefixed → FlatFile (the default class)

	// 1. Bare WRQ (`tftp put` default) → ACK-0 handshake; the free record is claimed.
	wrq := demoWRQ(name, nil)
	eqFrame(t, "flat WRQ → ACK-0", serveDemo(t, mac, enc, wrq), goRef.OnFrame(wrq))

	// 2. DATA blocks 1..N → an ACK per block; the final reply on the short block.
	var final []byte
	block := uint16(1)
	for off := 0; off < len(file); off += blksize {
		end := off + blksize
		if end > len(file) {
			end = len(file)
		}
		dataFrame := demoData(block, file[off:end])
		got := serveDemo(t, mac, enc, dataFrame)
		eqFrame(t, "flat DATA → reply", got, goRef.OnFrame(dataFrame))
		final = got
		block++
	}

	// The HSAVE landed: exactly one BDOSSave, into the claimed free record, with the
	// dotted-suffix-dropped name and the right byte count. (BDOSSave carries the UIFA
	// fields — record/name/size/addr — not the bytes; the staged bytes are read from
	// WRQ_STAGING below, the source the HSAVE reads.)
	saves := store.Saves()
	if len(saves) != 1 {
		t.Fatalf("Saves() = %d, want 1 (one flat-file HSAVE)", len(saves))
	}
	s := saves[0]
	if s.Record != freeRecord {
		t.Errorf("HSAVE record = %d, want %d (the claimed free record)", s.Record, freeRecord)
	}
	if s.Name != "notes" {
		t.Errorf("HSAVE name = %q, want %q (dotted suffix dropped by bdos_name_to_uifa)", s.Name, "notes")
	}
	if s.Size != uint32(len(file)) {
		t.Errorf("HSAVE size = %d, want %d (the staged byte count)", s.Size, len(file))
	}

	// The bytes the HSAVE reads (its WRQ_STAGING source) equal the pushed file.
	z80Staged := mac.Read(wrqStagingAddr, len(file))
	if !bytes.Equal(z80Staged, file) {
		t.Errorf("WRQ_STAGING != pushed file\n  z80 %x...\n  want %x...",
			z80Staged[:min(16, len(z80Staged))], file[:min(16, len(file))])
	}

	// The final reply is an ACK (opcode 4) of the final block, not an ERROR — a flat
	// file is never content-rejected (design §6.5).
	pay := udpPayload(t, final)
	if tftp.Opcode(pay) != tftp.OpACK {
		t.Fatalf("final reply opcode = %d, want ACK(%d) — got %x", tftp.Opcode(pay), tftp.OpACK, pay)
	}
	if blk, err := tftp.ParseACK(pay); err != nil || blk != block-1 {
		t.Fatalf("final ACK block = %d (err %v), want %d (the final block)", blk, err, block-1)
	}
}

// TestServeWRQFlatFileNoFree is the FlatFile no-free-record gate, mirroring
// TestServeWRQRecordPushNoFreeRecord but for a non-prefixed (FlatFile) name: every
// record is named, so the WRQ is rejected with ERROR(3) at the handshake, the receiver
// is NOT armed, and no HSAVE runs — matching the Go authority.
func TestServeWRQFlatFileNoFree(t *testing.T) {
	const records, freeRecord = 4, 0 // 0 = no free slot; all 4 named
	mac, enc, store, _, goRef := loadServeRecordPush(t, records, freeRecord)

	wrq := demoWRQ("notes.dat", nil) // NON-prefixed → FlatFile
	got := serveDemo(t, mac, enc, wrq)
	eqFrame(t, "flat no-free WRQ → ERROR(3)", got, goRef.OnFrame(wrq))

	pay := udpPayload(t, got)
	if tftp.Opcode(pay) != tftp.OpERROR {
		t.Fatalf("no-free reply opcode = %d, want ERROR(%d) — got %x", tftp.Opcode(pay), tftp.OpERROR, pay)
	}
	if code, _, err := tftp.ParseError(pay); err != nil || code != tftp.ErrDiskFull {
		t.Fatalf("no-free reply = ERROR code %d (err %v), want %d", code, err, tftp.ErrDiskFull)
	}

	// The receiver was NOT armed and nothing was HSAVE'd.
	if active := mac.Read(symAddr(t, mac, "WRQ_RECV_ACTIVE"), 1)[0]; active != 0 {
		t.Errorf("WRQ_RECV_ACTIVE = %d, want 0 (no receiver armed when no record is free)", active)
	}
	if sv := store.Saves(); len(sv) != 0 {
		t.Fatalf("Saves() = %d, want 0 (nothing HSAVE'd when no record is free)", len(sv))
	}
}

// TestServeWRQRecordPushNoFreeRecord drives the all-records-full case through the
// real dispatch: every record is named, so bdos_find_free_record returns 0, the WRQ
// is rejected with ERROR(3, "no free record") at the handshake, the receiver is NOT
// armed, no DATA is accepted, and zero sectors are written (a named record is never
// touched — the shared-resource invariant). The reply matches the Go authority.
func TestServeWRQRecordPushNoFreeRecord(t *testing.T) {
	const records, freeRecord = 4, 0 // 0 = no free slot; all 4 named
	mac, enc, store, _, goRef := loadServeRecordPush(t, records, freeRecord)

	wrq := demoWRQ("trinity-sam-disks/upload.mgt", nil)
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
	mac, enc, store, _, _ := loadServeRecordPush(t, 8, record)

	// Arm a push exactly the way handle_wrq does on a bare WRQ + a successful claim:
	// drive the WRQ handshake through the real dispatch (on the already-inited ENC)
	// so CLIENT_* is learned (wd_finalize wraps the reply to it), the record is
	// selected, the sink reset, and the receiver armed in sink mode.
	wrq := demoWRQ("trinity-sam-disks/upload.mgt", nil)
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

// TestServeWRQRecordPushWireRingWrap is the emulation-first gate for the push
// path PAST the ENC RX-ring wrap boundary (the gap TestServeWRQRecordPushWireRouting
// left open: its 3-block image is ~1.2 KB, smaller than the 6.5 KB RX ring, so it
// never wraps). A real disk-image `tftp put` wraps the ring dozens of times; this
// drives enough full 512-byte DATA blocks through the REAL serve_serve_once + the
// emulated ENC to wrap the ring SEVERAL times, asserting every block ACKs and the
// streamed sectors match the image byte-for-byte. It is what proves the wire
// receive path is faithful across wraps — without it, the path Pete runs on
// hardware would be un-emulated past the wrap, and a wrap-mishandling bug would
// strand his SAM (no recovery).
//
// Wrap arithmetic: a 512-byte DATA block is 558 bytes on the wire and ~568 bytes
// in the RX ring (558 + 4-byte CRC + 6-byte ENC packet header); the RX ring is
// 6.5 KB (rx_start..rx_end = 0x0000..0x19FF), so ~11.7 frames fill one wrap. The
// blockCount below is sized to wrap the ring well past 3 times. The image is kept
// sub-record (so the run stays ~ms, not the ~40 s a full 1600-block 819,200-byte
// record costs over the bit-banged SPI) and ends on a short final block, so the
// validate-gates-reply decision is the already-proven invalid->ERROR(3) path; the
// point here is ring-wrap fidelity, not the validation reply.
func TestServeWRQRecordPushWireRingWrap(t *testing.T) {
	const records, freeRecord = 8, 4
	mac, enc, store, _, goRef := loadServeRecordPush(t, records, freeRecord)

	// 60 full 512-byte blocks (~5.1 ring wraps) + a short 200-byte tail block to
	// end the transfer. 60 blocks >> the ~35 needed for 3 wraps, with margin.
	const blksize = 512
	const blockCount = 60
	img := make([]byte, blockCount*blksize+200)
	for i := range img {
		img[i] = byte(i*37 + 11) // position-dependent: a wrap-misread shows as a mismatch
	}

	// 1. Bare WRQ -> ACK-0 handshake.
	wrq := demoWRQ("trinity-sam-disks/ringwrap.mgt", nil)
	eqFrame(t, "WRQ -> ACK-0", serveDemo(t, mac, enc, wrq), goRef.OnFrame(wrq))

	// 2. Every DATA block -> an ACK per block, byte-for-byte vs the Go authority.
	//    The ENC RX-ring write position advances cumulatively across all 61 calls,
	//    so frame ~12 onward is being deposited into a wrapped ring region.
	var final []byte
	block := uint16(1)
	for off := 0; off < len(img); off += blksize {
		end := off + blksize
		if end > len(img) {
			end = len(img)
		}
		dataFrame := demoData(block, img[off:end])
		got := serveDemo(t, mac, enc, dataFrame)
		eqFrame(t, "DATA -> reply", got, goRef.OnFrame(dataFrame))
		final = got
		block++
	}

	// The body streamed into the claimed record, sector by sector, intact across
	// every wrap. A wrap-misread anywhere would corrupt one of these sectors.
	z80Writes := store.SectorWrites()
	assertRecordSectors(t, z80Writes, img, freeRecord)

	// Authority cross-check: the Go RawSink (driven by the same DATA stream) emitted
	// the identical sectors and total — the wire path matches the reference.
	goWrites, goTotal, goDone, _ := goRef.WRQPushOutcome()
	if !goDone {
		t.Fatal("Go authority did not mark the disk-record push complete")
	}
	if len(z80Writes) != len(goWrites) {
		t.Fatalf("Z80 emitted %d sectors, Go authority %d", len(z80Writes), len(goWrites))
	}
	for i := range goWrites {
		if z80Writes[i].LinearSec != goWrites[i].LinearSec || !bytes.Equal(z80Writes[i].Data[:], goWrites[i].Data[:]) {
			t.Fatalf("sector[%d] != Go authority (a ring-wrap misread)", i)
		}
	}
	if goTotal != len(img) {
		t.Errorf("Go authority streamed %d bytes, want %d", goTotal, len(img))
	}

	// The sub-record image is rejected at validation -> final reply ERROR(3),
	// matching the Go authority above; the wrap fidelity is the assertion that
	// matters here.
	assertErrorDiskFull(t, final)
}

// loadServeRecordPushFree loads the serve BOOT binary like loadServeRecordPush, but
// seeds the card so EXACTLY the records in `free` (1-based) read as unnamed/free and
// all others (1..records) read as named. The Go authority is seeded with the same
// free-record sequence via SetFreeRecords so its claim model advances in lockstep
// with the Z80 (each valid push claims the head). This backs the i121g claim tests:
// after a push claims a record (writes its list entry via CMD24), the SD model
// update makes bdos_find_free_record return the NEXT free record on the following push.
func loadServeRecordPushFree(t *testing.T, records int, free []int) (*z80h.Machine, *z80h.ENC28J60, *z80h.BDOSStore, *z80h.SDCard, *z80h.CardModel, *serve.Responder) {
	t.Helper()
	mac, err := z80h.Load(serveBootBin, serveBootMap)
	if err != nil {
		t.Fatalf("serve boot binary not built (%v); run `make netboot-serve-boot`", err)
	}
	// Use 0xD000 (above the binary tail ~&C2EB) to avoid overwriting sdc_init_ladder.
	fillServeConfig(t, mac, []demoFile{{"hello.txt", makeFile(10), 0xD000}})

	enc := z80h.NewENC28J60()
	// Attach SD before initServeDriver so serve_main's CSD read path is ready.
	sd := enc.AttachSD(csdV2(1))
	initServeDriver(t, mac, enc)

	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	store.AttachCard(card)
	mac.AttachBDOS(store)

	freeSet := map[int]bool{}
	for _, n := range free {
		freeSet[n] = true
	}
	for n := 1; n <= records; n++ {
		if freeSet[n] {
			continue // leave free (all-zero list entry)
		}
		card.SetRecordName(n, "INUSE")
	}

	// Seed SD list sectors from the card model.
	maxListSec := (records + 31) / 32
	for ls := 1; ls <= maxListSec; ls++ {
		sec := card.ListSector(ls)
		sd.SeedSector(uint32(ls), sec[:])
	}
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("csd_set_bd_records: %v", err)
	}
	// Boot-faithful re-arm: the CSD read disturbed the ENC RX path (i249); serve_main
	// re-arms here before its serve loop (netboot_serve.asm:1391-1402), so the host
	// test must too, or serve_serve_once receives nothing.
	reArmENCRX(t, mac)
	mac.WriteU16LE(symAddr(t, mac, "BD_RECORDS"), uint16(records))

	// The i121g claim tests assert a LOWEST-first record advance (3 then 4, the
	// finder's original behaviour). Patch the serve config block to the lowest-free
	// strategy (the default is now highest-free, i121h) so those expectations hold;
	// the placement-strategy tests (TestServeWRQRecordPushStrategy) re-patch the byte
	// for the highest/explicit variants.
	mac.Write(symAddr(t, mac, "SERVE_CFG_STRATEGY"), []byte{uint8(serve.StrategyLowestFree)})

	cfg := serve.Config{ServerMAC: demoServerMAC, ServerIP: demoServerIP, ServerTID: demoServerTID, DiskRecordPush: true, Strategy: serve.StrategyLowestFree}
	goRef := serve.New(cfg, tftp.MapStore{}, func(string) tftp.Source { return tftp.ByteSource(nil) })
	goRef.SetFreeRecords(free)
	return mac, enc, store, sd, card, goRef
}

// runFullPush drives one complete disk-record push on a SHARED machine (so a claim
// from an earlier push is visible to a later one via the SD model): the WRQ
// handshake for `name` through the real dispatch (which runs wrq_claim_record →
// bdos_find_free_record against the current SD state), the whole image streamed
// through the sink, and wd_finalize, returning the Z80 final reply frame. It ALSO
// drives the Go authority (`goRef`) through the same WRQ + DATA frames via OnFrame
// (pure Go, no bit-banged ENC, so feeding all 1600 blocks is cheap), so the
// authority's claim model advances in lockstep — the authority-vs-Z80 comparison.
// Reusing the machine across calls is what lets the two-push test observe the free
// record advance after the first claim.
func runFullPush(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, goRef *serve.Responder, name string, img []byte) []byte {
	t.Helper()

	// Drive the Go authority through the wire frames (WRQ handshake + every 512-byte
	// DATA block, ending on a short final block); this is what populates goRef.Claims.
	const blksize = 512
	goRef.OnFrame(demoWRQ(name, nil))
	block := uint16(1)
	for off := 0; off < len(img); off += blksize {
		end := off + blksize
		if end > len(img) {
			end = len(img)
		}
		goRef.OnFrame(demoData(block, img[off:end]))
		block++
	}
	// If the image is an exact multiple of blksize, a real client sends a final empty
	// DATA block to signal end-of-transfer; mirror it so the authority finalizes.
	if len(img)%blksize == 0 {
		goRef.OnFrame(demoData(block, nil))
	}

	// Drive the Z80 side: WRQ handshake through the real dispatch, the image streamed
	// through the sink at full scale, then wd_finalize.
	if got := serveDemo(t, mac, enc, demoWRQ(name, nil)); got == nil {
		t.Fatal("WRQ handshake sent nothing")
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
	mac.WriteU16LE(symAddr(t, mac, "WRQ_ACKED"), 1600)

	before := len(enc.TXFrames())
	if _, err := mac.Call("wd_finalize"); err != nil {
		t.Fatalf("wd_finalize: %v", err)
	}
	tx := enc.TXFrames()
	if len(tx) != before+1 {
		t.Fatalf("wd_finalize transmitted %d frames, want 1", len(tx)-before)
	}
	return tx[len(tx)-1]
}

// TestServeWRQRecordPushClaimsDifferentRecords is the DECISIVE i121g batch test:
// pushing two valid disk images in succession lands them on DIFFERENT records,
// because the first push CLAIMS its record (writes the record-list name entry via
// CMD24) so bdos_find_free_record advances to the next free record for the second
// push. This is Pete's primary use case — pushing many disk images, each claiming
// its own slot — and without the claim the second push would overwrite the first.
// The claim list-write is asserted via the SD model's captured CMD24 sector (which
// record was marked used, which filename-derived name), and the per-push sector
// writes confirm the two images landed in different records.
func TestServeWRQRecordPushClaimsDifferentRecords(t *testing.T) {
	const records = 8
	free := []int{3, 4} // records 3 then 4 are the two free slots, in order
	mac, enc, store, sd, _, goRef := loadServeRecordPushFree(t, records, free)

	// First push: a distinct valid image with a filename whose 10-char B-DOS name is
	// "firstdisk" (the dotted .mgt suffix dropped, "trinity-sam-disks/" prefix stripped).
	img1 := recordValidImage()
	for i := range img1 {
		img1[i] = byte(i*5 + 1)
	}
	copy(img1[bdos.BDOSStampOffset:bdos.BDOSStampOffset+4], []byte("BDOS"))
	final1 := runFullPush(t, mac, enc, goRef, "trinity-sam-disks/firstdisk.mgt", img1)
	if blk, err := tftp.ParseACK(udpPayload(t, final1)); err != nil || blk != 1600 {
		t.Fatalf("push 1 final reply = ACK %d (err %v), want ACK 1600 (valid image)", blk, err)
	}

	// The first push claimed record 3 (the head of `free`) — assert the CMD24 write
	// to list sector 1 (LBA 1, which holds records 1..32).
	claimedRec1 := 3
	listSec1 := 1
	entry1 := sdSlotEntry(sd, listSec1, claimedRec1)
	if entry1 == nil {
		t.Fatalf("after push 1: no CMD24 write to list sector LBA %d (claim not written)", listSec1)
	}
	if entry1[0]&0x7F == 0 {
		t.Errorf("push 1 claimed record %d reads FREE (entry[0]=%#x), want NAMED", claimedRec1, entry1[0])
	}
	name1 := sdSlotName(sd, listSec1, claimedRec1)
	if name1 != "firstdisk" {
		t.Errorf("push 1 claim name = %q, want %q (filename-derived, 16-char record-name field)", name1, "firstdisk")
	}
	// The SD model now reads record 3 as NAMED — the whole point.
	if entry1[0]&0x7F == 0 {
		t.Errorf("record 3 still reads FREE after the claim (entry[0]=%#x)", entry1[0])
	}

	// Second push: a different valid image, different filename.
	img2 := recordValidImage()
	for i := range img2 {
		img2[i] = byte(i*9 + 2)
	}
	copy(img2[bdos.BDOSStampOffset:bdos.BDOSStampOffset+4], []byte("BDOS"))
	// A filename whose stem is 15 chars — within the full 16-char record-name field
	// (i195), so it is kept whole: "seconddiskimage" (the .mgt suffix dropped).
	final2 := runFullPush(t, mac, enc, goRef, "trinity-sam-disks/seconddiskimage.mgt", img2)
	if blk, err := tftp.ParseACK(udpPayload(t, final2)); err != nil || blk != 1600 {
		t.Fatalf("push 2 final reply = ACK %d (err %v), want ACK 1600 (valid image)", blk, err)
	}

	// The DECISIVE observation: the second push advanced to record 4 (NOT 3) — the
	// first record was claimed and is no longer free, so the second push did not
	// overwrite it.
	claimedRec2 := 4
	entry2 := sdSlotEntry(sd, listSec1, claimedRec2)
	if entry2 == nil {
		t.Fatalf("after push 2: no CMD24 write to list sector LBA %d for record %d", listSec1, claimedRec2)
	}
	if claimedRec2 == claimedRec1 {
		t.Fatalf("both pushes claimed the same record %d — the claim did NOT mark the first record used", claimedRec1)
	}
	name2 := sdSlotName(sd, listSec1, claimedRec2)
	if name2 != "seconddiskimage" { // 15 chars, within the 16-char record-name field
		t.Errorf("push 2 claim name = %q, want %q (16-char record-name field)", name2, "seconddiskimage")
	}

	// The two images landed in DIFFERENT records: every sector of push 1 targets
	// record 3, every sector of push 2 targets record 4.
	writes := store.SectorWrites()
	const sectorsPerImage = 1600
	if len(writes) != 2*sectorsPerImage {
		t.Fatalf("SectorWrites() = %d, want %d (two full-record pushes)", len(writes), 2*sectorsPerImage)
	}
	for i := 0; i < sectorsPerImage; i++ {
		if writes[i].Record != 3 {
			t.Fatalf("push-1 sector[%d] record = %d, want 3", i, writes[i].Record)
		}
	}
	for i := sectorsPerImage; i < 2*sectorsPerImage; i++ {
		if writes[i].Record != 4 {
			t.Fatalf("push-2 sector[%d] record = %d, want 4 (a DIFFERENT record — no overwrite)", i, writes[i].Record)
		}
	}

	// Authority cross-check: the Go authority claimed the same two records, in order,
	// with the same names — the claim model advanced in lockstep.
	claims := goRef.Claims()
	if len(claims) != 2 {
		t.Fatalf("Go authority Claims() = %d, want 2", len(claims))
	}
	if claims[0].Record != 3 || claims[1].Record != 4 {
		t.Errorf("Go authority claimed records %d, %d; want 3, 4", claims[0].Record, claims[1].Record)
	}
	if claims[0].Name != "firstdisk" || claims[1].Name != "seconddiskimage" {
		t.Errorf("Go authority claim names %q, %q; want %q, %q", claims[0].Name, claims[1].Name, "firstdisk", "seconddiskimage")
	}
}

// TestServeWRQRecordPushClaimOnlyOnValid proves the claim is gated on validation: an
// INVALID push (wrong size) does NOT claim its record — no CMD24 list-write — so the
// record stays free, and a FOLLOWING valid push REUSES that same record (it was
// never marked used). This is the correctness half of the safety model: a rejected
// image must leave the slot free for the next good push, never half-claim it.
func TestServeWRQRecordPushClaimOnlyOnValid(t *testing.T) {
	const records = 8
	free := []int{5} // a single free slot; both pushes target it
	mac, enc, store, sd, _, goRef := loadServeRecordPushFree(t, records, free)

	// Push 1: an INVALID image (one sector short → wrong size). It streams into the
	// claimed record 5, fails validation, and must NOT claim (no CMD24 list-write).
	bad := recordValidImage()[:bdos.RecordSize-bdos.SectorSize]
	final1 := runFullPush(t, mac, enc, goRef, "trinity-sam-disks/broken.mgt", bad)
	assertErrorDiskFull(t, final1)
	if v := mac.Read(symAddr(t, mac, "BD_REC_VALID"), 1)[0]; v != 0 {
		t.Errorf("BD_REC_VALID = %d after the bad push, want 0", v)
	}
	// Record 5 still reads FREE in the SD model — no CMD24 list-write for a reject.
	listSec1 := 1
	if entry := sdSlotEntry(sd, listSec1, 5); entry != nil && entry[0]&0x7F != 0 {
		t.Errorf("an invalid push wrote a claim entry for record 5 (entry[0]=%#x); it must stay free", entry[0])
	}

	// Push 2: a VALID image. bdos_find_free_record still returns record 5 (it was
	// never claimed), so the good image REUSES that slot — and now claims it.
	good := recordValidImage()
	final2 := runFullPush(t, mac, enc, goRef, "trinity-sam-disks/good.mgt", good)
	if blk, err := tftp.ParseACK(udpPayload(t, final2)); err != nil || blk != 1600 {
		t.Fatalf("push 2 final reply = ACK %d (err %v), want ACK 1600", blk, err)
	}
	// After the valid push, the SD model must show record 5 as NAMED.
	entry5 := sdSlotEntry(sd, listSec1, 5)
	if entry5 == nil {
		t.Fatalf("after the valid reuse: no CMD24 write to list sector LBA %d (only the valid push claims)", listSec1)
	}
	if entry5[0]&0x7F == 0 {
		t.Errorf("after the valid push record 5 still reads FREE (entry[0]=%#x)", entry5[0])
	}
	name5 := sdSlotName(sd, listSec1, 5)
	if name5 != "good" {
		t.Errorf("claim name = %q, want %q", name5, "good")
	}

	// The two images landed in the same record (5) because the bad push left it free.
	writes := store.SectorWrites()
	if len(writes) == 0 {
		t.Fatalf("no sector writes captured")
	}
	// Find where the valid push's sectors start (after the bad push's sectors).
	badSecs := (len(bad) + bdos.SectorSize - 1) / bdos.SectorSize
	if len(writes) < badSecs {
		t.Fatalf("SectorWrites() = %d, expected at least %d (bad push sectors)", len(writes), badSecs)
	}
	for i := badSecs; i < len(writes); i++ {
		if writes[i].Record != 5 {
			t.Fatalf("valid-push sector[%d] record = %d, want 5 (reused the slot the bad push left free)", i, writes[i].Record)
		}
	}

	// Authority cross-check: the Go authority claimed exactly once, record 5.
	claims := goRef.Claims()
	if len(claims) != 1 || claims[0].Record != 5 {
		t.Fatalf("Go authority Claims() = %+v, want one claim of record 5", claims)
	}
}

// patchStrategy writes the placement strategy (and, for the explicit strategy, the
// record word) into the serve config block by symbol — exactly how the i121d host
// launcher will patch it before launch. It also points the Go authority's Config at
// the same strategy via a fresh Responder, returned for the authority-vs-Z80 cross-
// check. The card seeding (which records read free) is already done by the caller.
func patchStrategy(t *testing.T, mac *z80h.Machine, strategy uint8, explicit int) {
	t.Helper()
	mac.Write(symAddr(t, mac, "SERVE_CFG_STRATEGY"), []byte{strategy})
	if strategy == uint8(serve.StrategyExplicit) {
		mac.WriteU16LE(symAddr(t, mac, "SERVE_CFG_RECORD"), uint16(explicit))
	}
}

// goRefWithStrategy builds the Go authority Responder matching a strategy + free
// set, for the authority-vs-Z80 record-placement cross-check.
func goRefWithStrategy(strategy serve.PlacementStrategy, explicit int, free []int) *serve.Responder {
	cfg := serve.Config{
		ServerMAC: demoServerMAC, ServerIP: demoServerIP, ServerTID: demoServerTID,
		DiskRecordPush: true, Strategy: strategy, ExplicitRecord: explicit,
	}
	g := serve.New(cfg, tftp.MapStore{}, func(string) tftp.Source { return tftp.ByteSource(nil) })
	g.SetFreeRecords(free)
	return g
}

// TestServeWRQRecordPushStrategy is the i121h placement-strategy gate: with several
// records free, the record a valid push lands in is chosen by the config block's
// strategy byte (patched by symbol, as the i121d host launcher will). It asserts the
// chosen record via the SD model's captured CMD24 list-write AND the per-sector
// writes (every sector targets that record), cross-checked against the Go authority.
// Each sub-test drives the FULL push through the real dispatch + wd_finalize on its
// own machine.
func TestServeWRQRecordPushStrategy(t *testing.T) {
	const records = 8
	free := []int{3, 4} // two free slots make the high/low choice observable

	// assertSinglePush drives one valid push and asserts it claimed `wantRecord` and
	// streamed every sector into it. goRef is driven in lockstep for the cross-check.
	assertSinglePush := func(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, store *z80h.BDOSStore, sd *z80h.SDCard, goRef *serve.Responder, wantRecord int) {
		t.Helper()
		img := recordValidImage()
		final := runFullPush(t, mac, enc, goRef, "trinity-sam-disks/disk.mgt", img)
		if blk, err := tftp.ParseACK(udpPayload(t, final)); err != nil || blk != 1600 {
			t.Fatalf("final reply = ACK %d (err %v), want ACK 1600 (valid image)", blk, err)
		}
		// Check the CMD24 claim: list sector 1 (LBA 1) holds the entry for record wantRecord.
		listSec1 := 1
		claimEntry := sdSlotEntry(sd, listSec1, wantRecord)
		if claimEntry == nil {
			t.Fatalf("no CMD24 write to list sector LBA %d for record %d", listSec1, wantRecord)
		}
		if claimEntry[0]&0x7F == 0 {
			t.Errorf("claimed record %d reads FREE (entry[0]=%#x), want NAMED", wantRecord, claimEntry[0])
		}
		writes := store.SectorWrites()
		if len(writes) != 1600 {
			t.Fatalf("SectorWrites() = %d, want 1600 (one full-record push)", len(writes))
		}
		for i, w := range writes {
			if w.Record != wantRecord {
				t.Fatalf("sector[%d] record = %d, want %d", i, w.Record, wantRecord)
			}
		}
		// Authority cross-check: the Go authority claimed the same record.
		claims := goRef.Claims()
		if len(claims) != 1 || claims[0].Record != wantRecord {
			t.Fatalf("Go authority Claims() = %+v, want one claim of record %d", claims, wantRecord)
		}
	}

	t.Run("default (un-patched config) bakes highest-free", func(t *testing.T) {
		// A freshly-loaded boot binary, with NO config patch, bakes strategy 0
		// (highest-free) — the i121h default per manifest design §4 decision 4. (The
		// shared loadServeRecordPushFree helper patches lowest-free for the i121g claim
		// tests, so this baked-default check loads via the plain single-free loader,
		// which never touches the config block.)
		mac, _, _, _, _ := loadServeRecordPush(t, records, 4)
		if s := mac.Read(symAddr(t, mac, "SERVE_CFG_STRATEGY"), 1)[0]; s != uint8(serve.StrategyHighestFree) {
			t.Fatalf("baked SERVE_CFG_STRATEGY = %d, want %d (highest-free default)", s, serve.StrategyHighestFree)
		}
		if m := mac.Read(symAddr(t, mac, "SERVE_CONFIG"), 1)[0]; m != 0x5A {
			t.Errorf("baked SERVE_CONFIG magic = %#x, want 0x5A", m)
		}
	})

	t.Run("highest-free (strategy 0) → record 4", func(t *testing.T) {
		mac, enc, store, sd, _, _ := loadServeRecordPushFree(t, records, free)
		patchStrategy(t, mac, uint8(serve.StrategyHighestFree), 0)
		goRef := goRefWithStrategy(serve.StrategyHighestFree, 0, free)
		assertSinglePush(t, mac, enc, store, sd, goRef, 4)
	})

	t.Run("lowest-free (strategy 1) → record 3", func(t *testing.T) {
		mac, enc, store, sd, _, _ := loadServeRecordPushFree(t, records, free)
		patchStrategy(t, mac, uint8(serve.StrategyLowestFree), 0)
		goRef := goRefWithStrategy(serve.StrategyLowestFree, 0, free)
		assertSinglePush(t, mac, enc, store, sd, goRef, 3)
	})

	t.Run("explicit (strategy 2) free record 4 → record 4", func(t *testing.T) {
		mac, enc, store, sd, _, _ := loadServeRecordPushFree(t, records, free)
		patchStrategy(t, mac, uint8(serve.StrategyExplicit), 4)
		goRef := goRefWithStrategy(serve.StrategyExplicit, 4, free)
		assertSinglePush(t, mac, enc, store, sd, goRef, 4)
	})
}

// TestServeWRQRecordPushStrategyExplicitTaken proves the explicit strategy rejects a
// push aimed at an ALREADY-NAMED record: the named record is never touched (the
// shared-resource invariant), the WRQ is answered ERROR(3) at the handshake, and
// nothing is armed or written — matching the Go authority.
func TestServeWRQRecordPushStrategyExplicitTaken(t *testing.T) {
	const records = 8
	free := []int{3, 4} // record 7 is NOT free (named)
	mac, enc, store, _, _, _ := loadServeRecordPushFree(t, records, free)
	patchStrategy(t, mac, uint8(serve.StrategyExplicit), 7) // explicit a named record
	goRef := goRefWithStrategy(serve.StrategyExplicit, 7, free)

	wrq := demoWRQ("trinity-sam-disks/disk.mgt", nil)
	got := serveDemo(t, mac, enc, wrq)
	eqFrame(t, "explicit-taken WRQ → ERROR(3)", got, goRef.OnFrame(wrq))

	pay := udpPayload(t, got)
	if tftp.Opcode(pay) != tftp.OpERROR {
		t.Fatalf("explicit-taken reply opcode = %d, want ERROR(%d) — got %x", tftp.Opcode(pay), tftp.OpERROR, pay)
	}
	if code, _, err := tftp.ParseError(pay); err != nil || code != tftp.ErrDiskFull {
		t.Fatalf("explicit-taken reply = ERROR code %d (err %v), want %d", code, err, tftp.ErrDiskFull)
	}
	// The receiver was NOT armed, and nothing was written (the named record untouched).
	if active := mac.Read(symAddr(t, mac, "WRQ_RECV_ACTIVE"), 1)[0]; active != 0 {
		t.Errorf("WRQ_RECV_ACTIVE = %d, want 0 (no receiver armed when the explicit record is taken)", active)
	}
	if w := store.SectorWrites(); len(w) != 0 {
		t.Fatalf("SectorWrites() = %d, want 0 (the named explicit record is never touched)", len(w))
	}
}

// TestServeE2EComputedBDRecordsRecordPush is the i145d full-E2E emulation test —
// the experiment whose ABSENCE let the BD_RECORDS=0 hardware failure ship (every
// `tftp put` rejected with "no free record" because nothing read the card's record
// count). It runs the REAL serve_main boot path with a modelled Trinity SD card, so
// BD_RECORDS is COMPUTED from the card's CSD (i145b, NOT injected by the test), then
// pushes a valid 819,200-byte .mgt over WRQ and asserts it is found, claimed, and
// streamed into the highest free record located USING that computed count.
//
// This is the join the prior tests left untested: csd_to_bd_records_test.go proved
// the CSD->BD_RECORDS decode in isolation, and the other WRQ tests in this file
// proved the push by INJECTING BD_RECORDS — so the csd_set_bd_records ->
// bdos_find_free_record -> push chain was never exercised as one piece until here.
//
// Practicality: the full 819,200-byte body streams through the sink
// (raw_record_sink_leaf) rather than 1600 bit-banged wire DATA blocks (the wire
// receive path + ring wraps are proven by TestServeWRQRecordPushWireRouting /
// WireRingWrap). Emulation-verified is not hardware-verified (CLAUDE.md §5): the
// real SD CSD read + the real `tftp put` stay gated on Pete's Trinity.
func TestServeE2EComputedBDRecordsRecordPush(t *testing.T) {
	mac, err := z80h.Load(serveBootBin, serveBootMap)
	if err != nil {
		t.Fatalf("serve boot binary not built (%v); run `make netboot-serve-boot`", err)
	}

	// Model a Trinity: ENC + the EEPROM identity serve_main reads (the DEMO identity,
	// so the demoWRQ/serveDemo helpers address serve_main's configured endpoint) + an
	// SD card whose CSD decodes to a small record count (cheap free-record scan).
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(demoServerMAC, demoServerIP)
	const cSize = 0x0F // CSD v2.0; ~16384 blocks -> a handful of records
	sd := enc.AttachSD(csdV2(cSize))
	mac.AttachIO(enc)

	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	store.AttachCard(card)
	mac.AttachBDOS(store)

	// The record count the Z80 must COMPUTE from the CSD (the i145e-validated Go ref).
	_, want := refRecords(refBlocksV2(cSize))
	nrec := int(want)
	if nrec < 2 {
		t.Fatalf("test CSD yields %d records; need >=2 to leave a free one", nrec)
	}
	// Name records 1..nrec-1; leave the highest (nrec) free, so the default
	// highest-free strategy claims record nrec.
	wantRecord := nrec
	for r := 1; r < nrec; r++ {
		card.SetRecordName(r, "INUSE")
	}

	// Seed SD list sectors from the card model so CMD17 list reads serve the
	// correct record-list data.
	maxListSec := (nrec + 31) / 32
	for ls := 1; ls <= maxListSec; ls++ {
		sec := card.ListSector(ls)
		sd.SeedSector(uint32(ls), sec[:])
	}

	// Zero BD_RECORDS so a non-zero result can ONLY come from serve_main computing it.
	mac.WriteU16LE(symAddr(t, mac, "BD_RECORDS"), 0)

	// Boot serve_main: EEPROM read -> provision_demo -> csd_set_bd_records -> drv_init
	// -> serve loop (spins to the step cap; the CSD read completes early).
	if _, err := mac.RunBoot("serve_main", z80h.Entry{StepCap: bootStepCap}); err != nil {
		t.Fatalf("RunBoot serve_main: %v", err)
	}

	// (1) BD_RECORDS was COMPUTED from the CSD over the boot path — not injected.
	bd := mac.Read(symAddr(t, mac, "BD_RECORDS"), 2)
	got := uint16(bd[0]) | uint16(bd[1])<<8
	if uint32(got) != want || got == 0 {
		t.Fatalf("BD_RECORDS = %d, want %d (computed from CSD C_SIZE=0x%X) — the boot CSD read failed",
			got, want, cSize)
	}

	// (2) Push a valid 819,200-byte image over WRQ. The handshake must find + select
	// the highest free record using the COMPUTED BD_RECORDS.
	goRef := serve.New(
		serve.Config{ServerMAC: demoServerMAC, ServerIP: demoServerIP, ServerTID: demoServerTID, DiskRecordPush: true},
		tftp.MapStore{}, func(string) tftp.Source { return tftp.ByteSource(nil) })
	goRef.SetFreeRecordAvailable(true)

	wrq := demoWRQ("trinity-sam-disks/upload.mgt", nil)
	eqFrame(t, "WRQ -> ACK-0", serveDemo(t, mac, enc, wrq), goRef.OnFrame(wrq))
	if sel := store.Selected(); sel != wantRecord {
		t.Fatalf("claimed record = %d, want %d (the highest free record found via the COMPUTED BD_RECORDS=%d)",
			sel, wantRecord, got)
	}

	// Stream the full valid image through the sink at 819,200-byte scale (the path the
	// wire feeds, without the slow bit-banged ENC per block).
	img := recordValidImage()
	for off := 0; off < len(img); {
		end := off + rrsSliceMax
		if end > len(img) {
			end = len(img)
		}
		mac.Write(rrsScratch, img[off:end])
		if _, err := mac.CallEntry("raw_record_sink_leaf", z80h.Entry{HL: rrsScratch, BC: uint16(end - off)}); err != nil {
			t.Fatalf("raw_record_sink_leaf @%d: %v", off, err)
		}
		off = end
	}
	mac.WriteU16LE(symAddr(t, mac, "WRQ_ACKED"), 1600)

	before := len(enc.TXFrames())
	if _, err := mac.Call("wd_finalize"); err != nil {
		t.Fatalf("wd_finalize: %v", err)
	}
	tx := enc.TXFrames()
	if len(tx) != before+1 {
		t.Fatalf("wd_finalize transmitted %d frames, want 1", len(tx)-before)
	}
	final := tx[len(tx)-1]

	// (3) Valid 819,200-byte image -> BD_REC_VALID, 1600 sectors into wantRecord, a
	// final ACK, and the record CLAIMED (its list-name entry written via CMD24).
	if v := mac.Read(symAddr(t, mac, "BD_REC_VALID"), 1)[0]; v != 1 {
		t.Errorf("BD_REC_VALID = %d, want 1 (a valid 819200-byte BDOS image)", v)
	}
	assertRecordSectors(t, store.SectorWrites(), img, wantRecord)
	pay := udpPayload(t, final)
	if tftp.Opcode(pay) != tftp.OpACK {
		t.Fatalf("final reply opcode = %d, want ACK — got %x", tftp.Opcode(pay), pay)
	}
	if blk, err := tftp.ParseACK(pay); err != nil || blk != 1600 {
		t.Fatalf("final ACK block = %d (err %v), want 1600", blk, err)
	}
	// Confirm the claim was written via CMD24 (list sector for wantRecord).
	listSec := (wantRecord-1)/32 + 1
	claimEntry := sdSlotEntry(sd, listSec, wantRecord)
	if claimEntry == nil || claimEntry[0]&0x7F == 0 {
		t.Error("no CMD24 claim write captured — wd_finalize did not claim the record")
	}
	t.Logf("i145d E2E OK: BD_RECORDS=%d COMPUTED from CSD; valid push found+claimed+stored record %d (1600 sectors)",
		got, wantRecord)
}

// TestServeDiskPushTrinloadDeployable is the i194 emulation gate for the trinload-
// pushable "disk-record push" deployable END-TO-END: it loads the SAME binary the
// morning deploy pushes (netboot_serve_boot.bin — org &8000, entry &8000, the only
// build carrying the WRQ record-push path behind NETBOOT_HOSTTEST==0), drives a small
// multi-block .mgt over the REAL serve_serve_once WRQ dispatch into a CLAIMED FREE
// record (sector writes captured per block), and then drives the i194 clean-exit
// (sv_exit_to_trinload) and asserts it quiesces the shared &DC controller (the busy-
// poll milestones execute) and RETs cleanly to trinload's pushed return address.
//
// This is what proves the DEPLOYABLE — not a piece of it — does the disk-record push
// in the model: the binary loaded here is byte-for-byte the one trinpush-serve.py
// pushes (the Makefile netboot-serve-trinload target IS this binary). Emulation-
// verified is not hardware-verified (CLAUDE.md §5): the real ENC silicon, the wire
// `tftp put`, and whether a real PIC resumes chk_trinity after the quiesce stay gated
// on Pete's Trinity. The clean-exit quiesce is DESIGNED, hardware-unverified — this
// test proves only that the deselect + bounded busy-poll + settle path EXECUTES and
// the RET still reaches trinload (it does not strand the SAM on the way out).
func TestServeDiskPushTrinloadDeployable(t *testing.T) {
	const records, freeRecord = 8, 4
	mac, enc, store, _, goRef := loadServeRecordPush(t, records, freeRecord)

	// A small multi-block image: two full 512-byte blocks + a short tail → three DATA
	// blocks ending short. (The full-record validate→ACK decision is proven by
	// TestServeWRQRecordPushValidatesFull; the point here is the deployable routes a
	// pushed image into a FREE record over the real WRQ dispatch, then exits cleanly.)
	const blksize = 512
	img := make([]byte, 2*blksize+128)
	for i := range img {
		img[i] = byte(i*17 + 3)
	}

	// 1. Bare WRQ (`tftp put` default) → ACK-0; the free record is claimed + armed.
	wrq := demoWRQ("trinity-sam-disks/morning.mgt", nil)
	eqFrame(t, "deployable WRQ → ACK-0", serveDemo(t, mac, enc, wrq), goRef.OnFrame(wrq))
	if active := mac.Read(symAddr(t, mac, "WRQ_RECV_ACTIVE"), 1)[0]; active != 1 {
		t.Fatalf("WRQ_RECV_ACTIVE = %d after the WRQ, want 1 (the push must arm against the free record)", active)
	}

	// 2. DATA blocks 1..N → an ACK per block; the body streams into the claimed record.
	block := uint16(1)
	for off := 0; off < len(img); off += blksize {
		end := off + blksize
		if end > len(img) {
			end = len(img)
		}
		dataFrame := demoData(block, img[off:end])
		eqFrame(t, "deployable DATA → reply", serveDemo(t, mac, enc, dataFrame), goRef.OnFrame(dataFrame))
		block++
	}

	// The body landed in the CLAIMED FREE record (record 4), sector by sector — the
	// disk-record push the deployable exists to do.
	z80Writes := store.SectorWrites()
	assertRecordSectors(t, z80Writes, img, freeRecord)
	for _, w := range z80Writes {
		if w.Record != freeRecord {
			t.Fatalf("a sector targeted record %d, not the claimed FREE record %d", w.Record, freeRecord)
		}
	}

	// 3. The i194 CLEAN EXIT: drive sv_exit_to_trinload (the single RET-to-trinload
	// point both serve exits jump to). CallEntry pushes the HALT trap as the return
	// address — exactly how trinload pushes its `start` — so a clean RET lands on the
	// trap (res.Halted). A Trace records the quiesce milestones so we can assert the
	// &DC deselect + bounded BUSY-poll actually executed before the RET (not skipped).
	visited := map[string]bool{}
	milestones := map[uint16]string{}
	for _, n := range []string{"sv_exit_to_trinload", "sv_q_busy", "sv_q_settled", "sv_q_settle"} {
		if a, err := mac.Sym(n); err == nil {
			milestones[a] = n
		}
	}
	if _, ok := milestonesHas(milestones, "sv_exit_to_trinload"); !ok {
		t.Fatal("sv_exit_to_trinload symbol absent — the i194 quiesce exit is not in the binary")
	}
	res, err := mac.CallEntry("sv_exit_to_trinload", z80h.Entry{
		StepCap: 1_000_000, // a healthy controller clears BUSY in ~1 poll; the cap only bounds a stuck one
		Trace: func(pc uint16) {
			if n, ok := milestones[pc]; ok {
				visited[n] = true
			}
		},
	})
	if err != nil {
		t.Fatalf("CallEntry sv_exit_to_trinload: %v", err)
	}
	// (a) It RETd cleanly to trinload's pushed return address (landed on the HALT trap).
	if !res.Halted {
		t.Fatalf("sv_exit_to_trinload did not RET (PC=&%04X steps=%d) — the quiesce never reached trinload; "+
			"a stuck busy-poll would strand the SAM on exit", res.PC, res.Steps)
	}
	// (b) The quiesce path executed: the &DC busy-poll loop and the settle ran. With
	// the model's BUSY raised by the deselect OUT, the first IN &DC reads BUSY set, so
	// the loop body (sv_q_busy) runs before it clears, then sv_q_settled/settle run.
	if !visited["sv_q_busy"] {
		t.Error("sv_q_busy was never visited — the &DC BUSY-poll did not run (the quiesce was skipped)")
	}
	if !visited["sv_q_settled"] && !visited["sv_q_settle"] {
		t.Error("neither sv_q_settled nor sv_q_settle was visited — the post-deselect settle did not run")
	}
	t.Logf("i194 deployable OK: pushed %d-byte .mgt → %d sectors into FREE record %d; "+
		"sv_exit_to_trinload quiesced &DC + RET to trinload in %d steps (clean exit DESIGNED, hardware-unverified)",
		len(img), len(z80Writes), freeRecord, res.Steps)
}

// milestonesHas reports whether the milestone map contains the named symbol.
func milestonesHas(m map[uint16]string, name string) (uint16, bool) {
	for a, n := range m {
		if n == name {
			return a, true
		}
	}
	return 0, false
}

// TestServeWRQHandshakeWaitsForLink is the i280b-b2d emulation gate: after the WRQ
// claim's serve_rearm_enc (whose ereset drops the 10BASE-T link on real silicon, §8f),
// the handshake reply must NOT be transmitted until the link is back up — otherwise it is
// silently dropped (no egress) and curl never hand-shakes. srv_send_tbuf now calls
// drv_wait_link (i127) before every reply. This models the link DOWN past the point the
// handshake would naturally fire and asserts the reply still reaches the wire, held until
// link-up. Without the drv_wait_link gate the reply fires into the link-down window and the
// model drops it (serveDemo then sees 0 frames and fails) — so this guards the fix. Mirrors
// TestEEPROMRoundTripWaitsForLink (the proactive-client i127 case).
func TestServeWRQHandshakeWaitsForLink(t *testing.T) {
	const records, freeRecord = 8, 4
	wrq := demoWRQ("trinity-sam-disks/morning.mgt", nil)

	// Baseline (link up immediately): learn the op count at which the handshake reply
	// naturally transmits.
	mac0, enc0, _, _, _ := loadServeRecordPush(t, records, freeRecord)
	if serveDemo(t, mac0, enc0, wrq) == nil {
		t.Fatal("baseline: the bare WRQ produced no handshake reply")
	}
	natTX := enc0.FirstTXOps()
	if natTX <= 0 {
		t.Fatalf("baseline transmitted no frame (FirstTXOps=%d)", natTX)
	}

	// Hold the link down until well past that point, straddling the natural handshake TX.
	mac, enc, _, _, _ := loadServeRecordPush(t, records, freeRecord)
	linkUp := natTX + 5000
	enc.SetLinkUpAfterOps(linkUp)

	reply := serveDemo(t, mac, enc, wrq) // serveDemo fails if the reply was dropped (no frame)
	if reply == nil {
		t.Fatal("WRQ handshake reply dropped in the link-down window — drv_wait_link missing/ineffective")
	}
	if tx := enc.FirstTXOps(); tx < linkUp {
		t.Errorf("handshake TX at op %d, before link-up at %d — drv_wait_link did not hold the reply", tx, linkUp)
	}
}
