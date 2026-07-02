//go:build trinityboot

// trinload_first_boot_faithful_test.go — the i331 rig: the FULL real power-on
// chain to trinload idle, then a boot_record push from THAT state.
//
// The armed-editor rig (trinload_idle_faithful_test.go) reaches trinload idle
// via bootToEditorIdleSD + a directly-staged boot_record. This rig instead runs
// the REAL first-boot chain — reset -> patched ROM -> EEPROM bootblock (the
// trinity-autoboot bootloader in chunk 1) -> B-DOS init -> the no-config
// fallback decision -> the bootloader's bdos_boot_record(3) -> ALHK -> trinload
// — and then drives the boot-record.py push ('@'/'X' frames) from that idle.
//
// WHAT THE RIG SETTLED (i331, 2026-07-02). The hardware re-shot had suggested a
// first-boot-residue dispatch divergence: a boot_record push for record 3 from
// real trinload idle "returned instantly" (no SD boot). This rig DISPROVES the
// dispatch-divergence hypothesis: from real first-boot trinload idle, a pushed
// boot_record for the record-2 sentinel loads AND executes exactly as in the
// armed rig (the test below pins that). What IS broken — reproduced here with
// the sector deposits and live stack frames traced interleaving — is
// re-booting RECORD 3 (trinload itself) from the pushed context: trinload's
// image loads at &6000 and its 11 KB span COVERS &7FF0-&7FFF, the band the
// ROM/B-DOS record-boot continuation uses as its working stack (SP descends
// from &8000 through the hook dispatch and the ROM1 load-continuation). The
// deposit of file offsets &1FF0+ lands ON the continuation's live stack
// frames while pushes/pops overwrite freshly-deposited image bytes; control
// then pops image bytes as return addresses and wanders into a stable orbit
// (polling the &DC mux forever, deaf to frames) — hardware-consistent with the
// observed instant-return-and-keep-answering. Any image whose load range
// avoids &7FE0-&7FFF (the record-2 sentinel at &6000+32 B; the i332 serve
// record vessel at &8000, which loads into pages 1-2 through section C) boots
// fine, so the i319 feature-image class is unaffected. Do NOT use "re-boot
// record 3" as a hardware liveness probe — it cannot work from the pushed
// context; probe with a non-overlapping record instead.
//
// LOCAL cross-repo test (the `trinityboot` build tag; NOT in CI): needs the
// sibling bootloader (`cd ~/git/trinity-autoboot && make`) and the proprietary
// captures (requirePrivateCapture). Run:
//
//	cd tools/netboot-oracle && go test ./z80/ -tags trinityboot -run FirstBoot -v
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// TestBootRecordFromRealFirstBootTrinloadIdle drives: power-on reset -> real
// ROM -> our bootloader (chunk 1, no config -> fallback record 3) -> B-DOS ->
// bdos_boot_record(3) -> trinload read_loop; then a '?' liveness probe; then
// the boot-record.py push ('@'/'X') of build/boot_record.bin targeting the
// seeded sentinel record 2 — the i328 hardware scenario from the REAL
// first-boot idle instead of the armed-editor one.
func TestBootRecordFromRealFirstBootTrinloadIdle(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	boot := loadTrinityBoot(t)

	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(trinityDevice(t, eeprom, boot)) // chunk 1 = our bootloader; no config chunk -> fallback record 3

	sd := enc.AttachSD(csdV2(0x001D59))
	rec1 := make([]byte, 512)
	copy(rec1[232:236], []byte("BDOS")) // record-1 selection stamp (as the real card carries)
	sd.SeedSector(152, rec1)
	trinBin := mustReadFile(t, trinloadBin, "make netboot-trinload")
	seedRecordFromMGT(sd, 3, composeAutoBootMGT(t, "AUTOtrin.O", trinBin), "trinload")
	seedRecordFromMGT(sd, 2, sentinelAutoImage(t), "sentinel")
	seedRecordList(sd, map[int]string{1: "rec1", 2: "sentinel", 3: "trinload"})

	var lastPC uint16
	lg := &[]capEv{}
	mac.AttachIO(&capIO{inner: enc, lastPC: &lastPC, log: lg})

	// --- Phase 1: the WHOLE first boot, one continuous run: reset -> ROM ->
	// bootblock -> B-DOS init -> decision -> fallback -> bdos_boot_record(3)
	// -> ALHK loads trinload -> trinload read_loop.
	from1 := len(*lg)
	res1, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: 200_000_000, FrameIntPeriod: 60000,
		StopPC: trinloadReadLoop,
		Trace:  func(pc uint16) { lastPC = pc },
	})
	if err != nil {
		t.Fatalf("phase 1 (power-on -> trinload) faulted: %v (PC=&%04X)", err, res1.PC)
	}
	dir1, body1, out1 := classifyReads(cmd17Since(lg, from1), 3)
	t.Logf("phase 1: finalPC=&%04X reachedStop=%v steps=%d record-3 reads dir=%d body=%d outside=%d LMPR=&%02X HMPR=&%02X",
		res1.PC, res1.ReachedStop, res1.Steps, dir1, body1, out1, mac.Pager().LMPR, mac.Pager().HMPR)
	if !res1.ReachedStop {
		t.Fatalf("the real first boot did not reach trinload's read_loop (&%04X): finalPC=&%04X halted=%v",
			trinloadReadLoop, res1.PC, res1.Halted)
	}
	if body1 == 0 {
		t.Fatal("phase 1 read no record-3 BODY sectors — trinload cannot have been loaded from the card")
	}

	// trinload's adopted identity (settings buffer in page 0).
	var samMAC [6]byte
	var samIP [4]byte
	copy(samMAC[:], mac.Pager().RAM[0][trinloadChunkVar-0x4000:])
	copy(samIP[:], mac.Pager().RAM[0][trinloadChunkVar-0x4000+6:])
	t.Logf("phase 1: trinload idle from the REAL first boot; identity MAC=% X IP=%d.%d.%d.%d",
		samMAC[:], samIP[0], samIP[1], samIP[2], samIP[3])

	// --- Phase 2: liveness — '?' answered '!' (the hardware campaign's probe).
	txBefore := len(enc.TXFrames())
	enc.InjectRX(trinFrameTo(samMAC, samIP, []byte{'?'}))
	if _, err := mac.ContinueFrom(res1.PC, z80h.Entry{StepCap: 4_000_000, FrameIntPeriod: 60000}); err != nil {
		t.Fatalf("phase 2 (discovery) faulted: %v", err)
	}
	if got := countDiscoveryReplies(enc.TXFrames()[txBefore:]); got != 1 {
		t.Fatalf("phase 2: %d '!' replies, want 1 — trinload idle not live", got)
	}
	t.Logf("phase 2: trinload answered discovery ('!')")

	// --- Phase 3: the boot-record.py push from TRUE first-boot trinload idle:
	// '@' blocks + 'X' for build/boot_record.bin, BOOT_CFG_RECORD patched to
	// the sentinel record 2 (an image that does NOT overlap the record-boot
	// continuation's &7FFx stack band — see the file header for why record 3
	// itself cannot be pushed-booted).
	if err := mac.LoadSymbols(bootRecordMap); err != nil {
		t.Fatalf("load boot_record symbols: %v", err)
	}
	cfgAddr, err := mac.Sym("BOOT_CONFIG")
	if err != nil {
		t.Fatalf("BOOT_CONFIG symbol: %v", err)
	}
	brCode := append([]byte(nil), mustReadFile(t, bootRecordBin, "make netboot-boot-record")...)
	cfgOff := int(cfgAddr) - 0x8000
	if brCode[cfgOff] != 0x5A {
		t.Fatalf("boot_record config magic at %#x is %#x, want 0x5A", cfgOff, brCode[cfgOff])
	}
	brCode[cfgOff+1] = 2 // BOOT_CFG_RECORD = the sentinel record
	const page = 1
	for off := 0; off < len(brCode); off += 1024 {
		end := off + 1024
		if end > len(brCode) {
			end = len(brCode)
		}
		hdr := []byte{'@', page, byte(off), byte(off >> 8)}
		enc.InjectRX(trinFrameTo(samMAC, samIP, append(hdr, brCode[off:end]...)))
	}
	enc.InjectRX(trinFrameTo(samMAC, samIP, []byte{'X', page, 0x00, 0x80}))

	mac.Pager().RAM[1][sentinelMarkerOff] = 0
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

	// The healthy signature: the selected record's body read from the card
	// and its sentinel executed to the halt — proving the pushed boot_record
	// dispatch works from REAL first-boot trinload idle exactly as from the
	// armed-editor idle (no first-boot dispatch divergence).
	if body3 == 0 {
		t.Errorf("phase 3 read no record-2 BODY sectors — the pushed boot did not load the selected record")
	}
	if !res3.Halted {
		t.Errorf("phase 3 did not halt at the sentinel (finalPC=&%04X)", res3.PC)
	}
	if marker != sentinelMarker {
		t.Errorf("sentinel marker = &%02X, want &%02X — record 2's AUTO file did not run", marker, sentinelMarker)
	}
}
