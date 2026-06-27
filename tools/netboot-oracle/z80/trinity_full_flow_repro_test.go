//go:build trinityboot

// trinity_full_flow_repro_test.go — the FAITHFUL emulation reproduction Pete asked
// for (2026-06-30): boot exactly as the real SAM does and run trinload from an SD
// record under authentic Z80 + real ROM + real B-DOS, with NO Go mock.
//
// The chain (all real, nothing stubbed):
//
//	PC=0 -> Colin's forked ROM (rom.bin)
//	     -> our forked EEPROM bootloader in chunk 1 (trinity-autoboot/bootloader.bin)
//	     -> real Colin B-DOS 1.5t (chunks 2..13 loaded from the captured EEPROM)
//	     -> no "SAMBOOT Config" chunk -> auto-boot fallback to RECORD 3
//	     -> real B-DOS HRECORD-select + ALHK loads & runs the record's AUTO file
//	     -> trinload (org &6000) reaches its ENC packet listen loop (read_loop)
//
// This is the rig the previous sessions never had: every prior serve/WRQ test used
// the AttachBDOS Go mock (which never runs real B-DOS), and the closest real-ROM
// tests stopped at the record-boot dispatch (&42FF) without seeding a trinload
// record or running ALHK. With trinload genuinely running here we can drive the
// serve push + cj.mgt WRQ against REAL B-DOS hooks and TRACE whether the on-hardware
// HRECORD-select crash (screen read `WFabcde×13+GS`: 13 good find reads, then the
// HRECORD select never returns) reproduces in emulation.
//
//	Run: cd ~/git/trinity-autoboot && make
//	     cd ~/git/sam-aarch64/tools/netboot-oracle && \
//	       SKIP_PRIVATE_TESTS=false go test ./z80/ -tags trinityboot -run TrinityFullFlow -v
//
// Private deps (rom.bin/eeprom.bin captures + the forked bootloader) keep this
// local-only; the `trinityboot` tag excludes it from CI.
package z80_test

import (
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// trinloadMgt is Simon Owen's bootable trinload disk — a valid 819200-byte SAM
// disk whose AUTO file runs trinload. This is what record 3 holds on Pete's card.
const trinloadMgt = "/home/pmoore/git/trinload/trinload.mgt"

// trinload load addresses (from ~/git/trinload/trinload.map).
const (
	trinloadStart    = 0x6000 // start
	trinloadReadLoop = 0x6060 // read_loop — the ENC packet listen loop (trinload is live + idle here)
)

// seedRecordFromMgt writes a full 819200-byte .mgt image into SD record `record`
// as 1600 contiguous 512-byte sectors at LBA csdBase + 1600*(record-1) + i, matching
// the B-DOS record stride (bdos_store.go: base + 1600*(n-1) + linearSec).
func seedRecordFromMgt(t *testing.T, sd *z80h.SDCard, csdBase uint32, record int, mgt []byte) {
	t.Helper()
	if len(mgt) != 819200 {
		t.Fatalf("disk image is %d bytes, want exactly 819200 (80*10*2*512)", len(mgt))
	}
	// B-DOS addresses a record's sectors SIDE-MAJOR: conv.de computes
	// linear = side*800 + 10*track + (sector-1) (ANALYSIS.md §75; "10 x track +
	// sector-1, side 2 via bit 7"). A .mgt image is TRACK-MAJOR with the two sides
	// interleaved per track: mgtSector = track*20 + side*10 + (sector-1). So a raw
	// byte-for-byte copy puts the directory (linear 0) in the right place but every
	// file's data sectors in the wrong one. Re-order .mgt -> B-DOS record order here.
	// (TRINLOAD_REORDER=0 to seed RAW and observe the mismatch.)
	raw := os.Getenv("TRINLOAD_REORDER") == "0"
	for L := 0; L < 1600; L++ {
		mgtSec := L
		if !raw {
			side := L / 800
			rem := L % 800
			track := rem / 10
			sector := rem % 10
			mgtSec = track*20 + side*10 + sector
		}
		lba := csdBase + uint32(1600*(record-1)) + uint32(L)
		sd.SeedSector(lba, mgt[512*mgtSec:512*mgtSec+512])
	}
}

// TestTrinityFullFlowReachesTrinload boots the real firmware stack from PC=0 and
// asserts that the auto-boot of record 3 (trinload, seeded into the SD model) runs
// trinload all the way to its ENC listen loop. This is the foundation: it proves the
// emulator can reach the exact runtime state the real SAM is in when we push a serve.
func TestTrinityFullFlowReachesTrinload(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	boot := loadTrinityBoot(t)
	mgt, err := os.ReadFile(trinloadMgt)
	if err != nil {
		t.Fatalf("read trinload.mgt: %v", err)
	}

	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR

	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(trinityDevice(t, eeprom, boot)) // forked bootloader in chunk 1, no config chunk
	sd := enc.AttachSD(csdV2(0x001D59)) // base=152, records=4809 (matches the real card)
	// EXPERIMENT (2026-06-30): real B-DOS get.label rejects a record whose first dir
	// entry lacks the "BDOS" 4-byte ID at byte 232 ('Invalid record', rep81;
	// bdos15t-beta6.annotated.dis &8DC2 get.label). The plain ~/git/trinload/
	// trinload.mgt has 00 00 00 00 there. Stamp it to test whether the missing stamp
	// is why ALHK bails. (TRINLOAD_STAMP=0 to disable and see the unstamped behaviour.)
	if os.Getenv("TRINLOAD_STAMP") != "0" {
		copy(mgt[232:236], []byte("BDOS"))
	}
	seedRecordFromMgt(t, sd, 152, 3, mgt) // record 3 = trinload
	// ESC not held: keyMatrix defaults to 0xFF -> the config-driven auto-boot path,
	// which (no config chunk) falls back to BOOT_RECORD 3.

	// Log every &DC-&DF command so we can extract the CMD17 (read) / CMD24 (write)
	// LBAs B-DOS issues during the auto-boot — this tells us whether the device
	// mounts, whether record 3's directory (LBA 3352) is read, and whether ALHK
	// reads into record 3's data range at all.
	var lastPC uint16
	lg := &[]capEv{}
	mac.AttachIO(&capIO{inner: enc, lastPC: &lastPC, log: lg})

	const mBdosBootRecord = 0x42FF // bootloader: bdos_boot_record dispatch (HRECORD + ALHK)
	hits := map[uint16]int{mBdosBootRecord: 0, trinloadStart: 0, trinloadReadLoop: 0}
	res, runErr := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap:        80_000_000, // generous: boot + B-DOS init + ALHK sector loads of trinload
		FrameIntPeriod: 20000,      // B-DOS init's EI;HALT frame-delay loops need the 50Hz tick
		StopPC:         trinloadReadLoop,
		Trace: func(pc uint16) {
			lastPC = pc
			if _, ok := hits[pc]; ok {
				hits[pc]++
			}
		},
	})
	t.Logf("full-flow: bdos_boot_record=%d trinload_start=%d read_loop=%d finalPC=&%04X reachedStop=%v steps=%d err=%v",
		hits[mBdosBootRecord], hits[trinloadStart], hits[trinloadReadLoop], res.PC, res.ReachedStop, res.Steps, runErr)

	// Extract CMD17/CMD24 LBAs from the &DF (SD) command stream. SD command frames
	// are 6 consecutive &DF OUT bytes: [0x40|cmd, a3, a2, a1, a0, crc]; the data-phase
	// dummy clocks are &DF INs (and &FF/&FE OUTs, which have bit6 clear or both top
	// bits set), so parse over OUT-only events and key on (val & 0xC0)==0x40.
	rec3lo, rec3hi := uint32(152+1600*2), uint32(152+1600*3-1) // 3352..4951
	dc := *lg
	var dfOut []capEv
	for i := range dc {
		if dc[i].write && dc[i].port == 0xDF {
			dfOut = append(dfOut, dc[i])
		}
	}
	var reads, writes []uint32
	for i := 0; i+4 < len(dfOut); i++ {
		v := dfOut[i].val
		if v&0xC0 != 0x40 {
			continue
		}
		lba := uint32(dfOut[i+1].val)<<24 | uint32(dfOut[i+2].val)<<16 | uint32(dfOut[i+3].val)<<8 | uint32(dfOut[i+4].val)
		switch v & 0x3F {
		case 17:
			reads = append(reads, lba)
		case 24:
			writes = append(writes, lba)
		}
	}
	inRec3 := func(ls []uint32) (n int, sample []uint32) {
		for _, l := range ls {
			if l >= rec3lo && l <= rec3hi {
				n++
				if len(sample) < 8 {
					sample = append(sample, l)
				}
			}
		}
		return
	}
	nr3, sr3 := inRec3(reads)
	readSample := reads
	if len(readSample) > 16 {
		readSample = readSample[:16]
	}
	t.Logf("SD trace: %d CMD17 reads (first16=%v), %d CMD24 writes; reads in record-3 range [%d..%d]: %d (sample=%v)",
		len(reads), readSample, len(writes), rec3lo, rec3hi, nr3, sr3)
	t.Logf("  read LBA 152 (mount/dir)? %v; read LBA %d (record-3 dir)? %v", containsU32(reads, 152), rec3lo, containsU32(reads, rec3lo))

	// Every &DF (SD card) event with its PC — the SD is only touched via &DF, so
	// this is the COMPLETE SD interaction across the whole boot. Reveals the HRECORD
	// init ladder / range-check bail / address computation directly. (Also count the
	// &DC selects that gate the SD: &30 deselect, &31 select, &38 init.)
	var dfEvents []capEv
	for i := range dc {
		if dc[i].port == 0xDF {
			dfEvents = append(dfEvents, dc[i])
		}
	}
	t.Logf("TOTAL &DF (SD) events across boot: %d", len(dfEvents))
	dumpDF := func(label string, from, to int) {
		t.Logf("  %s [%d..%d]:", label, from, to)
		for i := from; i < to && i < len(dfEvents); i++ {
			e := dfEvents[i]
			dir := "IN "
			if e.write {
				dir = "OUT"
			}
			t.Logf("    [%d] pc=&%04X %s &DF = &%02X", i, e.pc, dir, e.val)
		}
	}
	dumpDF("HEAD", 0, 60)
	if len(dfEvents) > 120 {
		dumpDF("TAIL", len(dfEvents)-60, len(dfEvents))
	}
	// Distinct PCs touching &DF (which routine(s) drive the SD).
	pcset := map[uint16]int{}
	for _, e := range dfEvents {
		pcset[e.pc]++
	}
	t.Logf("  distinct &DF PCs: %d", len(pcset))
	for pc, n := range pcset {
		if n >= 20 {
			t.Logf("    pc=&%04X x%d", pc, n)
		}
	}

	if hits[mBdosBootRecord] == 0 {
		t.Fatalf("never reached bdos_boot_record (&42FF) — the bootloader did not dispatch the record auto-boot")
	}
	if hits[trinloadStart] == 0 {
		t.Fatalf("trinload (&6000) never reached — real B-DOS ALHK did not load+run record 3's AUTO file (finalPC=&%04X)", res.PC)
	}
	if !res.ReachedStop {
		t.Fatalf("trinload started but never reached its ENC listen loop &%04X within %d steps (finalPC=&%04X)",
			trinloadReadLoop, res.Steps, res.PC)
	}
	t.Logf("SUCCESS: trinload is running and idle at its ENC listen loop — the faithful boot reproduction works")
}
