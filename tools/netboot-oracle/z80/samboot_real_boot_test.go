// samboot_real_boot_test.go — i190a: boot the REAL captured patched system ROM +
// Trinity EEPROM in the netboot emulation core and trace the authentic boot, so
// the static contradiction in samboot-bootblock-analysis.md §7.4 (which the
// disassembly could NOT settle) is resolved by a running model — the gating
// prerequisite for i197c (finalize the SAMBOOT injection site) and thus the whole
// SAMBOOT strand.
//
// WHAT IT DOES. It loads Colin Piggot's PROPRIETARY captured images
// (~/sam-archive/samboot-capture/{rom.bin,eeprom.bin}) — never copied into the
// repo, referenced by path, skip-when-absent (the csd_decode_colin_test.go
// convention) — into the ONE shared SAM-emulation core (the sampage pager + the
// Trinity EEPROM SPI model), then runs the patched ROM from RESET (PC=0) exactly
// as the hardware does:
//
//   - the pager maps ROM0 at section A and ROM1 at section D at reset (LMPR &40),
//     so the patched ROM fetches its own real reset/boot code;
//   - the EEPROM model serves the real captured device bytes over the Trinity SPI
//     ports (&DC select / &DD data), so the patched ROM's boot fetch pulls Colin's
//     actual bytes;
//   - the run follows the full real path — stock ROM init → the &ED1B Trinity probe
//     (which stores the 'T' marker at &4000) → report &50 → the &0F7F fetch
//     (LMPR=&5F, EEPROM-enable, read 1024 B from device &002000 into &4000) →
//     JP &4000 — under a PC trace.
//
// WHAT IT RESOLVES (samboot-bootblock-analysis.md §7.4). The static RE found two
// facts that contradict a clean single-stage boot and could not be settled by
// disassembly: (1) the ROM loads device &002000 (chunk 1 — a B-DOS routine
// library, first byte EX (SP),HL — NOT a valid cold entry) to &4000 and JP &4000,
// while the coherent self-contained bootblock sits at device &0000 which the ROM
// path never reads; (2) B-DOS calls into a section-B (&4000-&7FFF) support library
// that nothing statically loads. The trace settles both: it confirms the real boot
// jumps to the chunk-1 routine library at &4000 (NOT the &0000 bootblock), and that
// that code immediately CALLs &5C26 — a section-B address this single-stage path
// leaves UNINITIALISED — pinning the gap as a runtime multi-stage/paging load (or a
// cross-state capture). This is exactly what i197c needs to finalize the injection
// site: the injection belongs on the ROM-loaded chunk-1 path that actually runs at
// &4000, not the dormant &0000 bootblock.
//
// THE HONESTY LINE (CLAUDE.md §5, §7). This runs the REAL patched ROM and REAL
// EEPROM through the faithful pager + EEPROM SPI model — emulation-first, no flat
// shortcut, no HOSTTEST carve-out. What it does NOT model: the SAM display/ASIC
// and the analogue side; and it deliberately does not synthesise the missing
// section-B stage (whether a runtime load populates it, or rom.bin/eeprom.bin came
// from different machine states, is the open question the trace SURFACES for i197c,
// not one this test invents an answer to). Emulation-verified is not hardware-
// verified.
package z80_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// Captured-artifact layout (CAPTURE-NOTES.txt): rom.bin = 32 KB patched system
// ROM (ROM0+ROM1); eeprom.bin = 128 KB Trinity flash device image, file offset ==
// device byte address.
const (
	realROMBytes  = 32768  // ROM0 (16 KB) + ROM1 (16 KB)
	realEEPROMMin = 0x2400 // we read through device &002000+&400; require at least that

	// The patched-ROM boot-fetch sites (samboot-bootblock-analysis.md §6.1/§7.3,
	// disassembled from the capture). Logical (post-paging) addresses.
	addrTrinityProbe = 0xED1B // ROM1: probe Trinity, store 'T' marker at &4000
	addrReportHook   = 0x0F7B // ROM0: report-&50 handler (CP &50 / JR NZ)
	addrFetchTrinity = 0x0F7F // ROM0: the Trinity branch ('T' confirmed -> EEPROM read)
	addrEEPROMReader = 0xF5DD // ROM1: the SPI read routine (opcode 3 + 3-byte addr)
	addrJP4000       = 0x0FAF // ROM0: JP &4000 — run what was fetched
	addrRunTarget    = 0x4000 // where the fetched chunk runs
	addrChunk1Call   = 0x5C26 // chunk-1's first CALL — into the unloaded section B

	// Boot-time paging: ROM0 at section A (LMPR bit5=0), ROM1 at section D (bit6=1),
	// page 0 — the reset map the patched ROM begins executing under.
	bootLMPR = 0x40
	bootHMPR = 0x00

	// EEPROM device addresses (eeprom.bin file offsets) the analysis pins.
	eepBootblock = 0x0000 // the coherent &0000 bootblock (DB FA = IN A,(&FA))
	eepChunk1    = 0x2000 // chunk 1 — the B-DOS routine library the ROM actually loads
	eepChunk1End = 0x2400 // chunk 1 + 1 KB

	realBootStepCap = 5_000_000 // generous; the real boot reaches the trap well within this
)

// realCapturePath resolves a captured artifact from $SAMBOOT_CAPTURE_DIR or the
// default ~/sam-archive/samboot-capture, returning "" if it is absent (the test
// then skips). Mirrors colinBdosBinPath() — the proprietary captures are never in
// the repo. The default directory is CAPTURE-NOTES.txt's home.
func realCapturePath(name string) string {
	if dir := os.Getenv("SAMBOOT_CAPTURE_DIR"); dir != "" {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, "sam-archive", "samboot-capture", name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// loadRealCaptures reads the captured ROM + EEPROM, skipping the test cleanly if
// either is absent (a missing proprietary capture is never a failure — i190a's
// gate is "passes, or skips when the captures are absent").
func loadRealCaptures(t *testing.T) (rom, eeprom []byte) {
	t.Helper()
	romPath := realCapturePath("rom.bin")
	eepPath := realCapturePath("eeprom.bin")
	if romPath == "" || eepPath == "" {
		t.Skip("captured ROM/EEPROM not present (set $SAMBOOT_CAPTURE_DIR or place rom.bin+eeprom.bin under ~/sam-archive/samboot-capture/) — Colin's proprietary captures, not in the repo")
	}
	var err error
	if rom, err = os.ReadFile(romPath); err != nil {
		t.Fatalf("read rom.bin: %v", err)
	}
	if eeprom, err = os.ReadFile(eepPath); err != nil {
		t.Fatalf("read eeprom.bin: %v", err)
	}
	if len(rom) != realROMBytes {
		t.Fatalf("rom.bin is %d bytes, want %d (ROM0+ROM1) — capture-handling bug", len(rom), realROMBytes)
	}
	if len(eeprom) < realEEPROMMin {
		t.Fatalf("eeprom.bin is %d bytes, want >= %d — capture too short to serve the boot fetch", len(eeprom), realEEPROMMin)
	}
	return rom, eeprom
}

// newRealBootMachine builds the emulator with the real ROM in the pager and the
// real EEPROM in the Trinity SPI model, at reset paging. This is the i190a core:
// the single shared SAM-emulation core booting the real artifacts.
func newRealBootMachine(t *testing.T, rom, eeprom []byte) (*z80h.Machine, *z80h.ENC28J60) {
	t.Helper()
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load real ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(eeprom)
	mac.AttachIO(enc)
	return mac, enc
}

// TestRealBootReachesEEPROMFetch boots the real patched ROM from reset and asserts
// the authentic boot path runs in the documented order — the proof that the
// emulator now traces the real boot, not a synthetic one. This is the i190a
// deliverable: with it, i197c can observe the boot it could not see statically.
func TestRealBootReachesEEPROMFetch(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac, _ := newRealBootMachine(t, rom, eeprom)

	// Record the first-visit order of the boot-fetch milestones along the path.
	milestones := map[uint16]string{
		addrTrinityProbe: "trinity-probe(&ED1B)",
		addrReportHook:   "report-50-handler(&0F7B)",
		addrFetchTrinity: "trinity-fetch(&0F7F)",
		addrEEPROMReader: "eeprom-reader(&F5DD)",
		addrJP4000:       "JP-&4000(&0FAF)",
		addrRunTarget:    "execute-at-&4000",
	}
	hits := map[uint16]int{}
	var order []string
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap,
		Trace: func(pc uint16) {
			if name, ok := milestones[pc]; ok {
				if hits[pc] == 0 {
					order = append(order, name)
				}
				hits[pc]++
			}
		},
	})
	if err != nil {
		t.Fatalf("real boot run faulted: %v", err)
	}
	t.Logf("real boot: steps=%d finalPC=&%04X halted=%v LMPR=&%02X (boot reached the trap)",
		res.Steps, res.PC, res.Halted, mac.Pager().LMPR)
	t.Logf("boot-fetch milestone order: %v", order)

	// Every milestone on the documented path must be visited exactly once, in order.
	want := []string{
		"trinity-probe(&ED1B)",
		"report-50-handler(&0F7B)",
		"trinity-fetch(&0F7F)",
		"eeprom-reader(&F5DD)",
		"JP-&4000(&0FAF)",
		"execute-at-&4000",
	}
	if len(order) != len(want) {
		t.Fatalf("boot visited %d milestones %v, want the %d documented ones %v", len(order), order, len(want), want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("boot milestone %d = %q, want %q (real boot diverged from samboot-bootblock-analysis.md §6.1/§7.3)", i, order[i], want[i])
		}
	}

	// The Trinity probe must have stored the 'T' marker at &4000 (else the fetch
	// branch would have exited to BASIC at &102F). After the fetch, &4000 holds the
	// fetched chunk, so this asserts the probe ran during the boot, not the marker.
	if hits[addrFetchTrinity] != 1 {
		t.Errorf("the Trinity fetch branch ran %d times, want 1 — the 'T' marker path", hits[addrFetchTrinity])
	}
}

// TestRealBootRunsChunk1NotBootblock is the §7.4 resolution: the real ROM loads
// EEPROM device &002000 (chunk 1, the B-DOS routine library) into &4000 and runs
// THAT — not the coherent &0000 bootblock. The static analysis could not tell
// which artifact executes at &4000; the running model does.
func TestRealBootRunsChunk1NotBootblock(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac, _ := newRealBootMachine(t, rom, eeprom)

	var reached4000 bool
	var firstAfter4000 []uint16
	if _, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap,
		Trace: func(pc uint16) {
			if pc == addrRunTarget {
				reached4000 = true
			}
			if reached4000 && len(firstAfter4000) < 8 {
				firstAfter4000 = append(firstAfter4000, pc)
			}
		},
	}); err != nil {
		t.Fatalf("real boot run faulted: %v", err)
	}
	if !reached4000 {
		t.Fatal("boot never reached &4000 — the EEPROM fetch + JP did not run")
	}

	// 1. The bytes fetched into &4000 must be EEPROM device &002000..&0023FF
	//    (chunk 1), byte-for-byte — proving the ROM read chunk 1, not the bootblock.
	got := mac.Read(addrRunTarget, 0x400)
	wantChunk1 := eeprom[eepChunk1:eepChunk1End]
	if !bytes.Equal(got, wantChunk1) {
		diff := 0
		for i := range got {
			if got[i] != wantChunk1[i] {
				diff++
			}
		}
		t.Fatalf("&4000..&43FF differs from EEPROM chunk 1 (&2000..&23FF) in %d/1024 bytes — the fetch did not land chunk 1", diff)
	}

	// 2. And it is NOT the &0000 bootblock: chunk 1 starts EX (SP),HL (0xE3); the
	//    bootblock starts IN A,(&FA) (0xDB 0xFA). The distinction is the whole point.
	if got[0] != 0xE3 {
		t.Errorf("&4000 first byte = 0x%02X, want 0xE3 (EX (SP),HL = chunk-1 routine library)", got[0])
	}
	if bb := eeprom[eepBootblock]; bb != 0xDB {
		t.Errorf("sanity: EEPROM &0000 first byte = 0x%02X, want 0xDB (IN A,(&FA) = the &0000 bootblock) — capture changed?", bb)
	}
	if got[0] == eeprom[eepBootblock] {
		t.Errorf("&4000 holds the &0000 bootblock, not chunk 1 — contradicts the §7.3 ROM fetch (device &002000)")
	}

	// 3. The execution at &4000 immediately runs chunk-1's prologue
	//    (EX (SP),HL; PUSH DE; CALL &5C26) — the first three PCs after &4000 are
	//    &4000, &4001, &4002 (a CALL whose target, &5C26, is in section B).
	if len(firstAfter4000) < 4 ||
		firstAfter4000[0] != 0x4000 || firstAfter4000[1] != 0x4001 || firstAfter4000[2] != 0x4002 {
		t.Fatalf("execution from &4000 = %X, want it to start &4000,&4001,&4002 (EX (SP),HL; PUSH DE; CALL)", firstAfter4000)
	}
	if firstAfter4000[3] != addrChunk1Call {
		t.Errorf("the chunk-1 prologue CALL went to &%04X, want &%04X (the section-B routine library)", firstAfter4000[3], addrChunk1Call)
	}
}

// TestRealBootSectionBUnloaded surfaces §7.4 point 2 for i197c: the chunk-1 code at
// &4000 CALLs &5C26 in section B (&4000-&7FFF), but this single-stage boot path
// never loads section B — so &5C26 is uninitialised (zero) RAM. That is the
// concrete evidence of the missing runtime multi-stage / paging load (or a
// cross-state capture) the static trace could only hypothesise. The test does NOT
// fix the gap; it pins it as an OBSERVATION for the i197c follow-up.
func TestRealBootSectionBUnloaded(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac, _ := newRealBootMachine(t, rom, eeprom)

	if _, err := mac.RunBootFrom(0x0000, z80h.Entry{StepCap: realBootStepCap}); err != nil {
		t.Fatalf("real boot run faulted: %v", err)
	}

	// &5C26 is the chunk-1 CALL target; it lives at section-B offset &1C26, outside
	// the 1 KB the ROM fetched at &4000 — so on this single-stage path it is whatever
	// uninitialised RAM held, i.e. zero. (If a future stage populated it, this byte
	// would be non-zero and the boot would continue coherently — exactly the
	// multi-stage hypothesis i197c must settle, e.g. by capturing the running card.)
	target := mac.Read(addrChunk1Call, 1)[0]
	t.Logf("chunk-1 CALL target &%04X (section B) after boot = 0x%02X (0x00 => section B was never loaded by the single-stage path)", addrChunk1Call, target)
	if target != 0x00 {
		t.Errorf("§7.4 assumption changed: &%04X = 0x%02X, expected 0x00 (an unloaded section B). If the boot now populates section B, update the §7.4 analysis and i197c.", addrChunk1Call, target)
	}
}
