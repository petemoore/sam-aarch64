// trinload_idle_faithful_test.go — the i328 full-chain faithful rig and the
// regression gate for the boot_record ROM1-continuation fix.
//
// THE i328 MECHANISM (authority: bdos15t-beta6.annotated.dis; reproduced and
// fix-validated in this rig): ALHK (B-DOS 1.5t HAUTO, real &9DBD) does NOT load
// or run anything — it scans the SELECTED record's directory for an AUTO* file,
// stages its UIFA/DIFA at &4B00/&4B50, and returns E=1 through the ROM hook
// exit, which vectors into the ROM1 LOAD continuation at &E274+ for the actual
// body load + jump. That continuation needs ROM1 mapped at section D (LMPR bit
// 6). A trinload-pushed caller runs with bit 6 clear, so pre-fix the vector
// executed RAM garbage at &E274 — on the real SAM a crash + re-autoboot, seen
// as boot_record "bouncing back to trinload" ~10 s later. bdos_boot_record now
// maps ROM1 (OR &40) before the RST 8; these tests hold that contract.
//
// TestBootRecordFromTrinloadIdle drives the WHOLE real chain on the captured
// ROM + B-DOS 1.5t + SD model: a seeded record 3 (trinload, composed from
// build/trinload.bin exactly as sd_push lays a .mgt on the card) is booted by
// the fixed boot_record from the armed pushed-program state; trinload comes up
// to its read_loop and answers a '?' discovery; boot_record (config-patched to
// record 2) is then pushed through trinload's own '@'/'X' receive path — what
// tools/trinload-push/boot-record.py does — and the second boot must land in
// record 2: its sentinel AUTO file runs (marker + halt), with the CMD17 bands
// proving WHICH record's body was read at each phase.
//
// TestBootRecordALHKWithoutROM1IsTheBounce is the negative control: the same
// HRECORD+ALHK fired with LMPR bit 6 CLEAR must show the pre-fix signature —
// the search runs but no body sector is ever read and the sentinel never runs.
// If the OR &40 is ever dropped from bdos_boot_record, the positive test fails;
// this one pins WHY.
//
// Gated on the proprietary captures; skips only under SKIP_PRIVATE_TESTS
// (i253). Emulation-verified is not hardware-verified (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	// trinload landmarks (build/trinload.map; the record image is composed from
	// build/trinload.bin, so these are OUR binary's addresses, org &6000 = physical
	// page 1... loaded by B-DOS to physical page 0 offset &2000, visible at &6000
	// under the LMPR=&1F system map).
	trinloadReadLoop = 0x6060 // the idle poll loop
	trinloadChunkVar = 0x666F // settings buffer: sam_mac = +0, sam_ip = +6

	// Record-band geometry on the csdV2(0x001D59) card (base=152, 1600 sectors per
	// record): record N's body band is [base+1600*(N-1), base+1600*N).
	recSectors = 1600

	// The sentinel AUTO file's marker: written to logical &A100 (section C =
	// exec-page 1, physical page 1 offset &2100) just before its di;halt.
	sentinelMarker    = 0xA5
	sentinelMarkerOff = 0x2100
)

// composeAutoBootMGT builds a minimal 800 KB .mgt (track-major, 80 cyl x 2 sides
// x 10 sectors) in the dist trinload.mgt shape: slot 0 a samdos2 placeholder
// entry (B-DOS is resident, so the auto-boot never loads it — the body stays
// zero), slot 1 an AUTO* CODE file carrying `payload`, load &6000 (StartPage 0 —
// B-DOS 1.5t loads it RAW into physical page 0 offset &2000), auto-exec via the
// dir entry at exec-page 1 / &A000. Field layout per
// docs/notes/test-mgt-byte-layout.md + the dist AUTOtrin.O entry, byte-for-byte:
// the body-header cache (+&D3) stays zero and the body header's exec-marker
// bytes are 00 00, so B-DOS reads the header from the first body sector exactly
// as it does on the real card.
func composeAutoBootMGT(t *testing.T, autoName string, payload []byte) []byte {
	t.Helper()
	return composeCodeAutoMGT(t, autoName, payload, 0x6000)
}

// composeCodeAutoMGT is the any-load-address generalization: `load` is the
// logical load address in system-map terms (&6000, &8000, ...), giving
// StartPage = (load>>14)-1 (the physical page whose section-C mapping holds the
// load address), the &8000-form page offset (load&0x3FFF)|0x8000, and the
// exec-page convention StartPage+1 with the same offset form.
func composeCodeAutoMGT(t *testing.T, autoName string, payload []byte, load uint16) []byte {
	t.Helper()
	if len(autoName) > 10 {
		t.Fatalf("auto filename %q longer than 10 chars", autoName)
	}
	name10 := autoName + "          "[:10-len(autoName)]
	startPage := byte(load>>14) - 1
	offForm := (load & 0x3FFF) | 0x8000
	lenMod := len(payload) & 0x3FFF
	pages := byte(len(payload) >> 14)
	execPage := startPage + 1

	img := make([]byte, 819200)

	// Slot 0: samdos2 placeholder (type CODE, 20 sectors at T4S1..T5S10, no exec).
	s0 := img[0x000:0x100]
	s0[0x00] = 0x13
	copy(s0[0x01:0x0B], "samdos2   ")
	s0[0x0B], s0[0x0C] = 0x00, 20 // sector count, big-endian
	s0[0x0D], s0[0x0E] = 4, 1     // first sector T4S1
	s0[0x0F], s0[0x10], s0[0x11] = 0xFF, 0xFF, 0x0F
	s0[0xF0], s0[0xF1] = 0x10, 0x27 // LengthMod16K = 10000
	s0[0xF2], s0[0xF3], s0[0xF4] = 0xFF, 0xFF, 0xFF

	// Slot 1: the AUTO CODE file at T6S1...
	nSec := (9 + len(payload) + 509) / 510
	s1 := img[0x100:0x200]
	s1[0x00] = 0x13
	copy(s1[0x01:0x0B], name10)
	s1[0x0B], s1[0x0C] = byte(nSec>>8), byte(nSec) // big-endian
	s1[0x0D], s1[0x0E] = 6, 1                      // first sector T6S1
	for i := 0; i < nSec; i++ {
		track, sector := 6+i/10, 1+i%10
		bit := track*10 + (sector - 1) - 40
		s1[0x0F+bit/8] |= 1 << (bit % 8)
	}
	s1[0xEC] = startPage                              // StartAddressPage
	s1[0xED], s1[0xEE] = byte(offForm), byte(offForm>>8) // StartAddressPageOffset, &8000-form
	s1[0xEF] = pages                                  // full 16K pages
	s1[0xF0], s1[0xF1] = byte(lenMod), byte(lenMod>>8) // LengthMod16K
	s1[0xF2] = execPage                               // ExecAddrDiv16K
	s1[0xF3], s1[0xF4] = byte(offForm), byte(offForm>>8) // ExecAddrMod16K, &8000-form

	// Body: 9-byte header + payload, chained 510 bytes per sector from T6S1.
	body := make([]byte, 0, 9+len(payload))
	body = append(body,
		0x13,
		byte(lenMod), byte(lenMod>>8), // LengthMod16K
		byte(offForm), byte(offForm>>8), // PageOffset, &8000-form
		0x00, 0x00, // exec marker (non-&FF: auto-exec armed, dist shape)
		pages,     // Pages
		startPage, // StartPage
	)
	body = append(body, payload...)
	for i := 0; i < nSec; i++ {
		track, sector := 6+i/10, 1+i%10
		off := track*10240 + (sector-1)*512 // side 0
		lo, hi := i*510, (i+1)*510
		if hi > len(body) {
			hi = len(body)
		}
		copy(img[off:], body[lo:hi])
		if i < nSec-1 {
			nt, ns := 6+(i+1)/10, 1+(i+1)%10
			img[off+510], img[off+511] = byte(nt), byte(ns)
		}
	}
	return img
}

// sentinelAutoImage composes a record image whose AUTO file writes
// sentinelMarker to &A100 (physical page 1 offset sentinelMarkerOff via the
// exec-time section C) and halts — the "did the second boot land?" probe.
func sentinelAutoImage(t *testing.T) []byte {
	t.Helper()
	sentinel := make([]byte, 32)
	copy(sentinel, []byte{0x3E, sentinelMarker, 0x32, 0x00, 0xA1, 0xF3, 0x76}) // ld a,&A5 ; ld (&A100),a ; di ; halt
	return composeAutoBootMGT(t, "AUTOsent", sentinel)
}

// seedRecordFromMGT lands a .mgt on the SD model at record `rec` exactly as
// sd_push does on the real card: the "BDOS" validity stamp patched at body
// sector 0 +232, the record label at +210, and every sector reordered
// track-major -> SIDE-major (sideMajorRecordLinear, the i315/i294 mapping).
// All-zero sectors are skipped — the model reads unseeded sectors as zeros.
func seedRecordFromMGT(sd *z80h.SDCard, rec int, mgt []byte, label string) {
	img := append([]byte(nil), mgt...)
	copy(img[232:236], "BDOS")
	copy(img[210:220], label+"          "[:10-len(label)])
	base := uint32(faithCSDBase + recSectors*(rec-1))
	zero := make([]byte, 512)
	for m := 0; m < recSectors; m++ {
		sec := img[m*512 : (m+1)*512]
		if !bytes.Equal(sec, zero) {
			sd.SeedSector(base+uint32(sideMajorRecordLinear(m)), sec)
		}
	}
}

// seedRecordList populates the on-card record list (16-byte entries from LBA 1)
// with names for the given records, as a used card carries.
func seedRecordList(sd *z80h.SDCard, names map[int]string) {
	sec := make([]byte, 512)
	for rec, name := range names {
		copy(sec[(rec-1)*16:], name)
	}
	sd.SeedSector(1, sec)
}

// trinFrameTo builds a UDP frame to trinload's port 0xEDB0 addressed to the
// MAC/IP trinload adopted from the captured EEPROM settings chunk.
func trinFrameTo(dstMAC [6]byte, dstIP [4]byte, payload []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC:  frame.MAC(dstMAC),
		SrcMAC:  frame.MAC(mask.ClientMAC),
		SrcIP:   frame.IPv4(mask.ClientIP),
		DstIP:   frame.IPv4(dstIP),
		SrcPort: 40000,
		DstPort: 0xEDB0,
		Payload: payload,
	})
}

// classifyReads buckets CMD17 read addresses: the DIRECTORY band of record rec
// (its first 40 record-linear sectors: tracks 0-3 side 0) vs its BODY band (the
// rest) vs anything outside the record.
func classifyReads(reads []uint32, rec int) (dir, body, outside int) {
	lo := uint32(faithCSDBase + recSectors*(rec-1))
	hi := lo + recSectors
	for _, a := range reads {
		switch {
		case a >= lo && a < lo+40:
			dir++
		case a >= lo+40 && a < hi:
			body++
		default:
			outside++
		}
	}
	return
}

// cmd17Since extracts the CMD17 read addresses issued after log index `from`.
func cmd17Since(lg *[]capEv, from int) []uint32 {
	frames, _ := decodeFrames(*lg, from)
	var reads []uint32
	for _, f := range frames {
		if f.cmd == 17 {
			reads = append(reads, f.addr)
		}
	}
	return reads
}

// stageBootRecord loads build/boot_record.bin into page 1 at &8000 with
// BOOT_CFG_RECORD patched to `record` (what boot-record.py does by file offset
// before the push) and returns the patched bytes plus the boot_record_main
// address. Symbols come from build/boot_record.map.
func stageBootRecord(t *testing.T, mac *z80h.Machine, record byte) ([]byte, uint16) {
	t.Helper()
	code := append([]byte(nil), mustReadFile(t, bootRecordBin, "make netboot-boot-record")...)
	if err := mac.LoadSymbols(bootRecordMap); err != nil {
		t.Fatalf("load boot_record symbols: %v", err)
	}
	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("boot_record symbol %q absent from %s", name, bootRecordMap)
		}
		return a
	}
	cfgOff := int(sym("BOOT_CONFIG")) - 0x8000
	if code[cfgOff] != 0x5A {
		t.Fatalf("boot_record config magic at file offset %#x is %#x, want 0x5A", cfgOff, code[cfgOff])
	}
	code[cfgOff+1] = record
	sH := mac.Pager().HMPR
	mac.Pager().HMPR = 1
	mac.Write(0x8000, code)
	mac.Pager().HMPR = sH
	return code, sym("boot_record_main")
}

func TestBootRecordFromTrinloadIdle(t *testing.T) {
	mac, lg, sd, enc := bootToEditorIdleSDENC(t)

	// Seed the card: record 3 = trinload (the real card's auto-boot record),
	// record 2 = the sentinel target for the second boot.
	trinBin := mustReadFile(t, trinloadBin, "make netboot-trinload")
	seedRecordFromMGT(sd, 3, composeAutoBootMGT(t, "AUTOtrin.O", trinBin), "trinload")
	seedRecordFromMGT(sd, 2, sentinelAutoImage(t), "sentinel")
	seedRecordList(sd, map[int]string{1: "rec1", 2: "sentinel", 3: "trinload"})

	// --- Phase 1: the first boot — the fixed boot_record, config-patched to
	// record 3, run from the armed pushed-program state (page 1, DOSCNT=0,
	// LMPR=&1F/HMPR=1): the exact context the real launcher's push creates.
	// The machine boot ended long ago in real time; expire the PIC settle
	// window the boot's SD traffic armed (a per-run clock artifact otherwise).
	enc.SettlePIC()
	const page = 1
	_, brMain := stageBootRecord(t, mac, 3)
	armServeDispatch(mac, page)
	mac.Pager().HMPR = page

	from1 := len(*lg)
	res1, err := mac.ContinueFrom(brMain, z80h.Entry{
		StepCap: 80_000_000, FrameIntPeriod: 60000,
		StopPC: trinloadReadLoop,
	})
	if err != nil {
		t.Fatalf("phase 1 (boot_record -> record 3) faulted: %v (PC=&%04X)", err, res1.PC)
	}
	dir1, body1, out1 := classifyReads(cmd17Since(lg, from1), 3)
	t.Logf("phase 1: finalPC=&%04X reachedStop=%v steps=%d record-3 reads dir=%d body=%d outside=%d",
		res1.PC, res1.ReachedStop, res1.Steps, dir1, body1, out1)
	if !res1.ReachedStop {
		t.Fatalf("boot_record(3) did not reach trinload's read_loop (&%04X): finalPC=&%04X halted=%v — the first boot broke before trinload idle",
			trinloadReadLoop, res1.PC, res1.Halted)
	}
	if body1 == 0 {
		t.Errorf("phase 1 read no record-3 BODY sectors — trinload cannot have been loaded from the card")
	}
	// The ALHK load landed trinload at physical page 0 offset &2000 (StartPage 0,
	// loaded raw), running at &6000 under LMPR=&1F. Compare only the pure-code
	// prefix (&6000-&62FF): the running trinload has already mutated its own data
	// region (settings buffer, packet vars) further in.
	if got := mac.Pager().RAM[0][0x2000:0x2300]; !bytes.Equal(got, trinBin[:0x300]) {
		t.Fatalf("trinload code at page 0 offset &2000 differs from build/trinload.bin (first 8: got % X want % X)",
			got[:8], trinBin[:8])
	}

	// trinload's adopted identity, read back from its settings buffer.
	var samMAC [6]byte
	var samIP [4]byte
	copy(samMAC[:], mac.Pager().RAM[0][trinloadChunkVar-0x4000:])
	copy(samIP[:], mac.Pager().RAM[0][trinloadChunkVar-0x4000+6:])
	t.Logf("phase 1: trinload idle; identity MAC=% X IP=%d.%d.%d.%d", samMAC[:], samIP[0], samIP[1], samIP[2], samIP[3])

	// --- Phase 2: liveness — a '?' discovery must be answered '!' (the probe
	// the real campaign used to see the bounce).
	txBefore := len(enc.TXFrames())
	enc.InjectRX(trinFrameTo(samMAC, samIP, []byte{'?'}))
	if _, err := mac.ContinueFrom(res1.PC, z80h.Entry{StepCap: 4_000_000, FrameIntPeriod: 60000}); err != nil {
		t.Fatalf("phase 2 (discovery) faulted: %v", err)
	}
	if got := countDiscoveryReplies(enc.TXFrames()[txBefore:]); got != 1 {
		t.Fatalf("phase 2: %d '!' replies to the '?' discovery, want 1 — trinload idle is not live on the wire", got)
	}
	t.Logf("phase 2: trinload answered discovery ('!')")

	// --- Phase 3: push boot_record for the sentinel record 2 through trinload's
	// own '@'/'X' path (byte-for-byte what boot-record.py sends), and run the
	// second boot from true trinload idle — the i328 hardware scenario.
	brCode := append([]byte(nil), mustReadFile(t, bootRecordBin, "make netboot-boot-record")...)
	cfgOff := func() int {
		a, err := mac.Sym("BOOT_CONFIG")
		if err != nil {
			t.Fatalf("BOOT_CONFIG symbol: %v", err)
		}
		return int(a) - 0x8000
	}()
	brCode[cfgOff+1] = 2 // BOOT_CFG_RECORD = the sentinel record
	for off := 0; off < len(brCode); off += 1024 {
		end := off + 1024
		if end > len(brCode) {
			end = len(brCode)
		}
		hdr := []byte{'@', page, byte(off), byte(off >> 8)}
		enc.InjectRX(trinFrameTo(samMAC, samIP, append(hdr, brCode[off:end]...)))
	}
	enc.InjectRX(trinFrameTo(samMAC, samIP, []byte{'X', page, 0x00, 0x80}))

	mac.Pager().RAM[1][sentinelMarkerOff] = 0 // clean slate for the marker
	from3 := len(*lg)
	res3, err := mac.ContinueFrom(res1.PC, z80h.Entry{
		StepCap: 80_000_000, FrameIntPeriod: 60000,
	})
	if err != nil {
		t.Fatalf("phase 3 (pushed boot_record -> record 2) faulted: %v (PC=&%04X)", err, res3.PC)
	}
	dir3, body3, out3 := classifyReads(cmd17Since(lg, from3), 2)
	marker := mac.Pager().RAM[1][sentinelMarkerOff]
	t.Logf("phase 3: finalPC=&%04X halted=%v steps=%d record-2 reads dir=%d body=%d outside=%d sentinelMarker=&%02X",
		res3.PC, res3.Halted, res3.Steps, dir3, body3, out3, marker)

	// The second boot must land in the SELECTED record 2: its body read from the
	// card and its sentinel executed to the halt.
	if body3 == 0 {
		t.Errorf("phase 3 read no record-2 BODY sectors — the second boot did not load the selected record (the i328 bounce class)")
	}
	if !res3.Halted {
		t.Errorf("phase 3 did not halt at the sentinel (finalPC=&%04X) — the second boot did not execute record 2's AUTO file", res3.PC)
	}
	if marker != sentinelMarker {
		t.Errorf("sentinel marker = &%02X, want &%02X — record 2's AUTO file did not run", marker, sentinelMarker)
	}
}

// TestBootRecordALHKWithoutROM1IsTheBounce pins the i328 mechanism: HRECORD +
// ALHK fired with LMPR bit 6 CLEAR (no ROM1 at section D — the raw pushed
// context) stages the AUTO file and returns E=1, but the ROM hook exit's vector
// to the ROM1 continuation lands in unmapped RAM, so no body sector is ever
// read and the sentinel never runs — the crash that re-autoboots the real SAM.
func TestBootRecordALHKWithoutROM1IsTheBounce(t *testing.T) {
	mac, lg, sd, _ := bootToEditorIdleSDENC(t)
	seedRecordFromMGT(sd, 2, sentinelAutoImage(t), "sentinel")
	seedRecordList(sd, map[int]string{1: "rec1", 2: "sentinel"})

	const page = 1
	// A raw HRECORD(2)+ALHK stub — bdos_boot_record WITHOUT its ROM1 mapping.
	stub := []byte{
		0x21, 0x02, 0x00, // ld hl,2
		0xAF,       // xor a
		0xCF, 0x9C, // rst 8 ; defb 156 (HRECORD)
		0xCF, 0x88, // rst 8 ; defb 136 (ALHK)
		0xF3, 0x76, // di ; halt (never reached: the vector crashes first)
	}
	armServeDispatch(mac, page)
	mac.Pager().HMPR = page
	mac.Write(0x9000, stub)
	mac.Pager().RAM[1][sentinelMarkerOff] = 0

	from := len(*lg)
	res, err := mac.ContinueFrom(0x9000, z80h.Entry{StepCap: 20_000_000, FrameIntPeriod: 60000})
	if err != nil {
		t.Fatalf("stub run faulted: %v (PC=&%04X)", err, res.PC)
	}
	dir, body, outside := classifyReads(cmd17Since(lg, from), 2)
	marker := mac.Pager().RAM[1][sentinelMarkerOff]
	t.Logf("no-ROM1 ALHK: finalPC=&%04X halted=%v record-2 reads dir=%d body=%d outside=%d marker=&%02X",
		res.PC, res.Halted, dir, body, outside, marker)

	if dir == 0 {
		t.Errorf("ALHK issued no record-2 directory reads — the search itself did not run (mechanism drifted; re-derive)")
	}
	if body != 0 {
		t.Errorf("record-2 BODY sectors were read (%d) without ROM1 mapped — the continuation ran anyway; the i328 mechanism no longer holds", body)
	}
	if marker == sentinelMarker {
		t.Errorf("the sentinel ran without ROM1 mapped — the i328 mechanism no longer holds")
	}
}
