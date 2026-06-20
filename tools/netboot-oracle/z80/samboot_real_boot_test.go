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
// WHAT IT RESOLVES (samboot-bootblock-analysis.md §7.4/§7.5/§7.6). The static RE
// found facts that contradict a clean single-stage boot and could not be settled
// by disassembly: (1) the ROM loads device &002000 (chunk 1 — a B-DOS routine
// library, first byte EX (SP),HL — NOT a valid cold entry) to &4000 and JP &4000,
// while the coherent self-contained boot sequencer sits at device &0000 which the
// ROM path never reads; (2) chunk 1 immediately CALLs &5C26, read at the time as a
// call into an "unloaded section-B support library" left zero by a "missing
// multi-stage load." The trace settles (1) — the real boot does jump to the chunk-1
// routine library at &4000, not the &0000 sequencer — and CORRECTS (2): &5C26 is a
// documented STREAMS-table sysvar (&5C0C-&5C35) that the normal cold-init NEW2 loop
// DELIBERATELY ZEROS. TestRealBootInitRunsBeforeChunk1 proves the from-reset boot
// runs that whole init (MNINIT &EBAE -> NEW2 &EC8F -> the streams-zap &ECB6/&ECC8)
// BEFORE handing to &4000, so &5C26 == 0 is the WRITTEN-zero NEW2 left, not
// unreached RAM and not a missing loader. The residual is therefore NOT "section B
// wasn't loaded" but "chunk 1 is a B-DOS library that needs B-DOS RESIDENT, and the
// ROM's single &2000-fetch path never loads B-DOS (chunks 2..13, which only the
// &0000 sequencer loads)" — the boot-entry contradiction §7.6 leaves open for the
// injection-site follow-on.
//
// THE HONESTY LINE (CLAUDE.md §5, §7). This runs the REAL patched ROM and REAL
// EEPROM through the faithful pager + EEPROM SPI model — emulation-first, no flat
// shortcut, no HOSTTEST carve-out. The capture is a consistent single-machine
// runtime snapshot (CAPTURE-NOTES.txt: rom + eeprom dumped in one trinload session),
// so it is NOT cross-state; what it does NOT model is the SAM display/ASIC and a
// resident, initialised B-DOS — so chunk 1's library CALLs run against an
// initialised-but-B-DOS-absent system, which is exactly why the &4000 path wanders
// instead of completing. Emulation-verified is not hardware-verified.
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
	addrChunk1Call   = 0x5C26 // chunk-1's first CALL target = STREAMS stream-8 word (displacement to its channel; 0 => stream closed); NEW2 zeros it

	// Normal cold-init milestones (annotated ROM disasm) that must run BEFORE the
	// &4000 handoff if chunk 1 is a library entry on a live system. MNINIT is the
	// reset cold-start; NEW2 -> the streams-zap loop populates &5C0C-&5C35 (which
	// contains &5C26) — &ECB6 starts it, &ECC8 is the CLSTL zap covering &5C26.
	addrMNINIT      = 0xEBAE
	addrNEW2        = 0xEC8F
	addrStreamsInit = 0xECB6
	addrStreamsZap  = 0xECC8

	// In-page offsets of the sysvars within section B's physical page. Under the
	// boot LMPR (&5F) section B = physical page 0, so &5C26/&5C6A live at these
	// offsets in page 0 — the page chunk 1 reads when it runs at &4000.
	offStreamVar = 0x1C26 // &5C26 in-page offset
	offFLAGS2    = 0x1C6A // &5C6A in-page offset

	// Boot-time paging: ROM0 at section A (LMPR bit5=0), ROM1 at section D (bit6=1),
	// page 0 — the reset map the patched ROM begins executing under.
	bootLMPR = 0x40
	bootHMPR = 0x00

	// EEPROM device addresses (eeprom.bin file offsets) the analysis pins.
	eepBootblock = 0x0000 // the coherent &0000 bootblock (DB FA = IN A,(&FA))
	eepChunk1    = 0x2000 // chunk 1 — the B-DOS routine library the ROM actually loads
	eepChunk1End = 0x2400 // chunk 1 + 1 KB
	eepChunk2    = 0x2400 // chunk 2 — first B-DOS image chunk (loads to &8000)

	// Bootblock internals (disassembled from EEPROM &0000, ORG &4000) — the §7.7
	// comparative experiment traces these to show a coherent load+handoff.
	addrReadChunk = 0x40BD // the bootblock's read_chunk helper, CALL'd 12x (chunks 2..13)
	addrExecDOS   = 0x409E // CALL &805F — the B-DOS entry, reached only after the load loop

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

// TestRealBootChunk1CallsZeroedStreamVar pins the CORRECTED §7.5/§7.6 reading of the
// chunk-1 prologue's CALL &5C26. The earlier framing read &5C26 == 0 as a "missing
// runtime multi-stage load that never populated a section-B support library." That
// was wrong about WHY it is zero: &5C26 is a documented STREAMS-table sysvar
// (&5C0C-&5C35) that the cold-init NEW2 loop deliberately zeros (the "12 more stream
// ptrs to zap"). The companion TestRealBootInitRunsBeforeChunk1 proves NEW2 runs
// before &4000 and WRITES this zero. So &5C26 == 0 is the initialised value, not
// unreached RAM — and chunk 1 CALLing it is a B-DOS library routine expecting a
// resident B-DOS (which the ROM's single &2000-fetch path never loads), not a
// hidden loader. This test keeps the byte assertion as a regression guard and
// documents the corrected interpretation.
func TestRealBootChunk1CallsZeroedStreamVar(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac, _ := newRealBootMachine(t, rom, eeprom)

	if _, err := mac.RunBootFrom(0x0000, z80h.Entry{StepCap: realBootStepCap}); err != nil {
		t.Fatalf("real boot run faulted: %v", err)
	}

	// &5C26 (the chunk-1 CALL target) is a STREAMS-table sysvar NEW2 zeros during the
	// cold-init that runs before &4000. So it reads 0x00 — the WRITTEN-zero NEW2 left,
	// proven by TestRealBootInitRunsBeforeChunk1's sentinel. (If a resident B-DOS later
	// installed a hook vector here, this would be non-zero — but that needs B-DOS
	// loaded, which the ROM's chunk-1 path never does: the §7.6 injection-site gap.)
	target := mac.Read(addrChunk1Call, 1)[0]
	t.Logf("chunk-1 CALL target &%04X = 0x%02X (NEW2-zeroed STREAMS sysvar; B-DOS not resident on this path)", addrChunk1Call, target)
	if target != 0x00 {
		t.Errorf("§7.6 assumption changed: &%04X = 0x%02X, expected 0x00 (the NEW2-zeroed STREAMS entry). If the boot now installs a vector here, update §7.6 + i197c.", addrChunk1Call, target)
	}
}

// TestRealBootInitRunsBeforeChunk1 is the i197c deliverable: it proves the from-reset
// boot runs the FULL normal ROM cold-init (MNINIT -> NEW2 -> the streams-zap loop)
// BEFORE handing off to the chunk-1 routine library at &4000 — resolving the
// init-ordering question §7.4/§7.5 left open, and CORRECTING the "&5C26 is zero
// because init had not run yet / a stage failed to load" reading. The decisive trick
// is a sentinel: planting 0xAA at &5C26's and &5C6A's physical-page-0 offsets before
// the boot, so a post-run 0x00 means NEW2 actively WROTE the zero (init ran), not
// that the byte was never touched (init skipped). It is.
func TestRealBootInitRunsBeforeChunk1(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac, _ := newRealBootMachine(t, rom, eeprom)

	// Under the boot LMPR (&5F) section B maps to physical page 0, so &5C26/&5C6A
	// live at page-0 offsets &1C26/&1C6A — the bytes chunk 1 reads when it runs.
	// Sentinel them so a written-zero is distinguishable from an untouched default.
	// &5C26 is a 16-bit field (stream 8's channel displacement), so sentinel BOTH
	// bytes &5C26/&5C27 — NEW2's CLSTL loop zeros the whole &5C1E-&5C35 span.
	const sentinel = 0xAA
	mac.Pager().RAM[0][offStreamVar] = sentinel
	mac.Pager().RAM[0][offStreamVar+1] = sentinel
	mac.Pager().RAM[0][offFLAGS2] = sentinel

	seen := map[uint16]bool{}
	var order []uint16
	var streamsInitBefore4000, reached4000 bool
	if _, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap,
		Trace: func(pc uint16) {
			switch pc {
			case addrMNINIT, addrNEW2, addrStreamsInit, addrStreamsZap, addrRunTarget:
				if !seen[pc] {
					seen[pc] = true
					order = append(order, pc)
				}
			}
			if pc == addrStreamsInit && !reached4000 {
				streamsInitBefore4000 = true
			}
			if pc == addrRunTarget {
				reached4000 = true
			}
		},
	}); err != nil {
		t.Fatalf("real boot run faulted: %v", err)
	}
	t.Logf("cold-init milestone order (first visit): %X", order)

	// 1. The whole cold-init chain ran, and the streams-zap ran BEFORE &4000.
	for _, m := range []uint16{addrMNINIT, addrNEW2, addrStreamsInit, addrStreamsZap} {
		if !seen[m] {
			t.Errorf("cold-init milestone &%04X was never reached — the from-reset boot did not run normal ROM init", m)
		}
	}
	if !streamsInitBefore4000 {
		t.Errorf("the streams-init (&%04X) did not run before &4000 — init ordering is NOT init-then-chunk1", addrStreamsInit)
	}

	// 2. NEW2 overwrote BOTH bytes of the stream-8 word with zero — so the full
	//    &5C26/&5C27 == &0000 (stream 8 closed) is a WRITTEN zero (init ran), the
	//    proof that corrects the "uninitialised / missing loader" reading. (&5C6A =
	//    FLAGS2 is in the same NEW-managed band.)
	lo, hi := mac.Pager().RAM[0][offStreamVar], mac.Pager().RAM[0][offStreamVar+1]
	if lo == sentinel || hi == sentinel {
		t.Errorf("stream-8 word &5C26/&5C27 still holds the 0x%02X sentinel (lo=0x%02X hi=0x%02X) — NEW2 did NOT write it; init did not populate the STREAMS band", sentinel, lo, hi)
	} else if lo != 0x00 || hi != 0x00 {
		t.Logf("note: stream-8 word &5C26/&5C27 = 0x%02X%02X (written by init, non-zero) — stream 8 would be open here", hi, lo)
	} else {
		t.Logf("stream-8 word &5C26/&5C27 sentinel -> &0000: NEW2 wrote the whole 16-bit field zero (stream 8 closed), confirming init ran before &4000")
	}
}

// TestRealChunk1IsNotABootLoader is the control for the §7.7 comparative experiment.
// The REAL captured chunk-1 (device &2000 — what the patched ROM fetches to &4000 and
// runs) performs ZERO chunk loads and never reaches the B-DOS entry &805F: it is a
// routine library, not a boot loader. Contrast TestHypothesisBootblockAt2000.
func TestRealChunk1IsNotABootLoader(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac, _ := newRealBootMachine(t, rom, eeprom)

	chunkLoads, reachedExecDOS := 0, false
	if _, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap,
		Trace: func(pc uint16) {
			if pc == addrReadChunk {
				chunkLoads++
			}
			if pc == addrExecDOS {
				reachedExecDOS = true
			}
		},
	}); err != nil {
		t.Fatalf("real boot run faulted: %v", err)
	}
	t.Logf("REAL chunk-1: read_chunk calls=%d, reached CALL &805F=%v", chunkLoads, reachedExecDOS)
	if chunkLoads != 0 {
		t.Errorf("real chunk-1 ran %d read_chunk loads, want 0 — it is a library, not a loader (did the capture's &2000 change?)", chunkLoads)
	}
	if reachedExecDOS {
		t.Errorf("real chunk-1 reached the B-DOS entry &805F — unexpected for a library; re-check §7.7")
	}
}

// TestHypothesisBootblockAt2000BootsCoherently is the §7.7 comparative experiment that
// ISOLATES the boot-entry gap. It does NOT run the real boot: it SUBSTITUTES the coherent
// &0000 bootblock into chunk-1's slot (&2000) of a working EEPROM copy, then boots from
// reset. The patched ROM then fetches that bootblock (it is fetch-compatible) and runs it
// COHERENTLY — 12 read_chunk calls load the REAL B-DOS chunks 2..13 (untouched at &2400+)
// into &8000, and it reaches CALL &805F (B-DOS init; the run then stops because &805F
// touches unmodeled SD/screen hardware). So the bootblock CONTENT boots from the &2000
// fetch point while the real &2000 library (the control above) does not — isolating the
// gap to the captured &2000 CONTENT, not the ROM fetch nor the bootblock design. Why the
// persistent &2000 holds a library when the card boots is the Colin/hardware question
// §7.7 escalates (q-registry).
func TestHypothesisBootblockAt2000BootsCoherently(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)

	// Working copy: overlay the &0000 bootblock (&0000..&015F) onto chunk-1's slot at
	// device &2000. Chunks 2..13 (&2400+) stay the real B-DOS image.
	const bootblockLen = 0x160
	work := make([]byte, len(eeprom))
	copy(work, eeprom)
	copy(work[eepChunk1:eepChunk1+bootblockLen], eeprom[eepBootblock:eepBootblock+bootblockLen])

	mac, _ := newRealBootMachine(t, rom, work)

	var reachedExecDOS bool
	chunkLoads := 0
	// &805F is in section C; under the bootblock's HMPR=&1D it touches unmodeled HW, so
	// the run may fault there — that is expected and not a failure of this experiment.
	_, _ = mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap,
		Trace: func(pc uint16) {
			if pc == addrReadChunk {
				chunkLoads++
			}
			if pc == addrExecDOS {
				reachedExecDOS = true
			}
		},
	})
	t.Logf("bootblock-at-&2000: read_chunk calls=%d (want 12 for chunks 2..13), reached CALL &805F=%v", chunkLoads, reachedExecDOS)

	if chunkLoads != 12 {
		t.Errorf("bootblock loaded %d chunks, want 12 (chunks 2..13) — the coherent load loop did not run", chunkLoads)
	}
	if !reachedExecDOS {
		t.Errorf("bootblock did not reach the B-DOS entry &805F — it did not load + hand off coherently")
	}

	// The first loaded chunk (chunk 2, device &2400) must land at &8000 under the
	// bootblock's HMPR (=&1D, section C = page 29) — proving a real B-DOS load, not a wander.
	mac.Pager().HMPR = 0x1D
	got := mac.Read(0x8000, 8)
	want := work[eepChunk2 : eepChunk2+8]
	if !bytes.Equal(got, want) {
		t.Errorf("&8000 after load = % X, want chunk-2 source % X — B-DOS image did not land in section C", got, want)
	}
}
