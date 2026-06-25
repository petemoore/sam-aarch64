// samboot_bootblock_test.go — i229 host-verification of the combined patched
// bootblock (the real SAMBOOT splice). Two groups, both honest to the q50 LIMITED
// verify scope (docs/plans/i229-combined-bootblock.md):
//
//	(A) DECISION LOGIC — run `inject_decision` (entering past the real-B-DOS-init
//	    CALL &805F, which does not return in the Go core) against the gated+relocated
//	    eeprom.asm reader + the RST-8 ALHK dispatch, across the 5 config cases. This
//	    closes the "did the SAMBOOT_BOOTBLOCK gating break the reader" gap: the
//	    reader's chunk buffer + index storage live in RAM scratch (&E000) in the
//	    bootblock build, and this proves they still find + decode the config and
//	    dispatch the boot.
//
//	(B) SPLICED RESET CHAIN — the i229 headline. Boot the REAL captured patched ROM
//	    + Trinity EEPROM from reset, but with chunk 1 replaced by the SPLICED image
//	    (Splice(eeprom[0:1024], build/samboot_bootblock.bin)). Assert the bootblock
//	    still loads B-DOS coherently (12 read_chunk loads), the splice site &409E
//	    (now CALL inject) is reached, `inject` at &415E runs, and &805F is reached
//	    VIA inject — proving the 3-byte splice did not break the loader and routes
//	    B-DOS init through our code. The run then halts inside B-DOS init (finalPC >=
//	    &8000), expected; the emulator is not taught past it (q50 decision 3 / i232).
//
// THE HONESTY LINE (CLAUDE.md §5/§7): the post-&805F in-chain config read, the real
// ALHK record boot, the stripe pixels, and the hardware-safety of the SAMBOOT_SCRATCH
// RAM address are the i230 hardware test (Pete present). Emulation-verified is not
// hardware-verified. The proprietary capture is referenced by path and FAILS HARD
// when absent (unless SKIP_PRIVATE_TESTS=true) — no silent skip (i253).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/samboot"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	sambootBootblockBinPath = "../../../build/samboot_bootblock.bin"
	sambootBootblockMapPath = "../../../build/samboot_bootblock.map"

	// The bootblock assembles at this org (&415E, the free space). The decision-logic
	// test loads the bytes there (LoadAt) so the map symbols match the loaded code.
	sambootBootblockOrg = 0x415E

	// ProgramNamedChunk slot for the config (found by name, so any 1-based slot works).
	sambootBootblockChunkValue = 5

	// addrInject is `inject` (&415E); the spliced &409E CALLs it, then inject CALLs
	// &805F. addrExecDOS (&409E, the splice site) and addrReadChunk (&40BD) are
	// defined in samboot_real_boot_test.go (same package).
	addrInject = 0x415E
)

// bootblockResult is what running inject_decision yields: which record (if any) the
// harness captured an ALHK boot against, and the HRECORD-selected record.
type bootblockResult struct {
	boots    []int
	selected int
}

// runBootblockDecision loads samboot_bootblock at its real org (&415E), attaches the
// EEPROM (config read) + the BDOS store (RST 8 boot capture), optionally programs the
// config chunk, runs inject_decision (skipping the real CALL &805F), and returns what
// was captured. programChunk==nil leaves the config chunk absent (the find_index miss).
func runBootblockDecision(t *testing.T, programChunk []byte) bootblockResult {
	t.Helper()
	if _, err := os.Stat(sambootBootblockBinPath); err != nil {
		t.Fatalf("samboot_bootblock binary not built (%s); run `make netboot-samboot-inject`", sambootBootblockBinPath)
	}
	mac, err := z80h.LoadAt(sambootBootblockBinPath, sambootBootblockMapPath, sambootBootblockOrg)
	if err != nil {
		t.Fatalf("load samboot_bootblock: %v", err)
	}

	enc := z80h.NewENC28J60()
	mac.AttachIO(enc)
	store := z80h.NewBDOSStore()
	mac.AttachBDOS(store)

	if programChunk != nil {
		if len(samboot.ChunkName) != 16 {
			t.Fatalf("samboot.ChunkName %q is %d bytes, want 16", samboot.ChunkName, len(samboot.ChunkName))
		}
		enc.ProgramNamedChunk(sambootBootblockChunkValue, samboot.ChunkName, programChunk)
	}

	// Enter at inject_decision so the real B-DOS init (CALL &805F) is skipped —
	// &805F does not return in the Go core (q50 decision 3); the decision+dispatch
	// from inject_decision onward is what this group verifies.
	if _, err := mac.CallEntry("inject_decision", z80h.Entry{}); err != nil {
		t.Fatalf("call inject_decision: %v", err)
	}
	return bootblockResult{boots: store.Boots(), selected: store.Selected()}
}

// TestBootblockDecisionAutoBoot: an auto-boot config for record 7 drives
// inject_decision to HRECORD-select + ALHK-boot record 7.
func TestBootblockDecisionAutoBoot(t *testing.T) {
	const rec = 7
	got := runBootblockDecision(t, samboot.Boot(rec).Encode())
	if got.selected != rec {
		t.Errorf("Selected() = %d, want %d (HRECORD inside bdos_boot_record)", got.selected, rec)
	}
	if len(got.boots) != 1 || got.boots[0] != rec {
		t.Errorf("Boots() = %v, want [%d] (one ALHK fire against the configured record)", got.boots, rec)
	}
}

// TestBootblockDecisionSecondRecord: a different record number (0x12) is read from
// the config and booted — the dispatch is not hard-wired to record 7.
func TestBootblockDecisionSecondRecord(t *testing.T) {
	const rec = 0x12
	got := runBootblockDecision(t, samboot.Boot(rec).Encode())
	if len(got.boots) != 1 || got.boots[0] != rec {
		t.Errorf("Boots() = %v, want [%d]", got.boots, rec)
	}
}

// TestBootblockDecisionNoneMode: a mode=none config falls through (RET to restore:)
// — NO boot captured, no HRECORD select.
func TestBootblockDecisionNoneMode(t *testing.T) {
	got := runBootblockDecision(t, samboot.None().Encode())
	if len(got.boots) != 0 {
		t.Errorf("Boots() = %v, want empty (mode=none falls through, no auto-boot)", got.boots)
	}
	if got.selected != -1 {
		t.Errorf("Selected() = %d, want -1 (no HRECORD select on the fall-through path)", got.selected)
	}
}

// TestBootblockDecisionAbsentChunk: with no "SAMBOOT Config  " chunk programmed,
// samboot_read_config returns no-auto-boot, so inject_decision falls through.
func TestBootblockDecisionAbsentChunk(t *testing.T) {
	got := runBootblockDecision(t, nil /* leave the config chunk absent */)
	if len(got.boots) != 0 {
		t.Errorf("Boots() = %v, want empty (absent config -> fall through)", got.boots)
	}
}

// TestBootblockDecisionBadVersion: an unrecognised config version is treated as
// no-auto-boot even when the mode byte says auto-boot.
func TestBootblockDecisionBadVersion(t *testing.T) {
	data := samboot.Boot(9).Encode()
	data[0] = 0xFF // corrupt the version byte (chunk+0)
	got := runBootblockDecision(t, data)
	if len(got.boots) != 0 {
		t.Errorf("Boots() = %v, want empty (bad version -> no auto-boot)", got.boots)
	}
}

// splicedDeviceEEPROM builds the device-linear EEPROM from the capture, then replaces
// device &2000..&23FF (= chunk 1, the bootblock) with the SPLICED image (the captured
// bootblock with the 3-byte CALL inject + the assembled inject blob). The splice runs
// in-Go (samboot.Splice) so the test is self-contained — no shell-out, no committed
// proprietary bytes. captured[0:1024] is chunk 1 (file offset 0 = device &2000; see
// deviceLinearEEPROM).
func splicedDeviceEEPROM(t *testing.T, captured, inject []byte) []byte {
	t.Helper()
	spliced, err := samboot.Splice(captured[:samboot.ChunkSize], inject)
	if err != nil {
		t.Fatalf("splice chunk 1: %v", err)
	}
	dev := deviceLinearEEPROM(captured)
	const chunk1Device = 0x2000
	copy(dev[chunk1Device:chunk1Device+samboot.ChunkSize], spliced)
	return dev
}

// loadInjectBlob reads the assembled inject (build/samboot_bootblock.bin). A missing
// build artifact is a HARD FAILURE (it is always buildable via `make
// netboot-samboot-inject`) — the i253 no-silent-skip rule.
func loadInjectBlob(t *testing.T) []byte {
	t.Helper()
	inject, err := os.ReadFile(sambootBootblockBinPath)
	if err != nil {
		t.Fatalf("inject blob %s not built; run `make netboot-samboot-inject`: %v", sambootBootblockBinPath, err)
	}
	return inject
}

// TestSplicedBootLoadsBDOSCoherently is the i229 headline: with chunk 1 replaced by
// the SPLICED image, the real patched ROM boots from reset, the bootblock runs its
// EEPROM auto-load (12 read_chunk loads pull B-DOS chunks 2..13), the splice site
// &409E (now CALL inject) is reached, `inject` at &415E runs, and &805F is reached
// VIA inject. The run then halts inside B-DOS init (finalPC >= &8000) — expected; the
// emulator is not taught past it (q50 decision 3). Mirrors
// TestRealBootLoadsBDOSCoherently but proves the splice did not break the loader.
func TestSplicedBootLoadsBDOSCoherently(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	inject := loadInjectBlob(t)

	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load real ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(splicedDeviceEEPROM(t, eeprom, inject))
	mac.AttachIO(enc)

	// Stop at the FIRST arrival at &805F (StopPCSkip=0). The q50-scoped claim is
	// "the bootblock loads chunk-1 coherently UP TO &805F via inject"; we must not
	// run past B-DOS init (q50 decision 3 / i232). The screenless Go core cannot
	// render the line-interrupt stripes inject now builds, so left to run past &805F
	// the unmodelled ISR re-enters the boot — exactly the region we are told not to
	// assert. Stopping at the first &805F captures precisely the in-scope behaviour:
	// the 12 chunk loads + the splice + inject + the hand-off to B-DOS init.
	chunkLoads := 0
	var reachedSpliceSite, reachedInject bool
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap,
		StopPC:  0x805F, // the B-DOS entry inject CALLs
		Trace: func(pc uint16) {
			switch pc {
			case addrReadChunk:
				chunkLoads++
			case addrExecDOS: // &409E — the splice site (now CALL inject)
				reachedSpliceSite = true
			case addrInject: // &415E — inject
				reachedInject = true
			}
		},
	})
	if err != nil {
		t.Fatalf("spliced boot run faulted: %v", err)
	}
	t.Logf("spliced boot: read_chunk=%d spliceSite(&409E)=%v inject(&415E)=%v stoppedAt&805F=%v finalPC=&%04X",
		chunkLoads, reachedSpliceSite, reachedInject, res.ReachedStop, res.PC)

	if !res.ReachedStop || res.PC != 0x805F {
		t.Errorf("boot did not reach &805F via inject (stopped=%v, finalPC=&%04X) — the bootblock did not hand off to B-DOS init through the splice", res.ReachedStop, res.PC)
	}
	if chunkLoads != 12 {
		t.Errorf("bootblock ran %d read_chunk loads before &805F, want 12 (B-DOS chunks 2..13) — the splice broke the loader", chunkLoads)
	}
	if !reachedSpliceSite {
		t.Error("the splice site &409E was never reached — the bootblock load loop did not complete")
	}
	if !reachedInject {
		t.Error("inject (&415E) was never reached — the CALL inject splice did not transfer control")
	}

	// The stripe redraw is emulation-verified, not just present: inject built the
	// LINICOLS line-colour table (&5600) before &805F, exactly as the &ED1B RAINBOW
	// SCREEN lays it out — 4-byte entries {scan_lo, 0, colour, colour}, scan stepping
	// +11 from 0, an &FF terminator after scan >= 166. (The pixels themselves are an
	// i230 hardware-confirm item; here we assert the table the ISR renders from.)
	assertLinicolsStripeTable(t, mac)
}

// assertLinicolsStripeTable reads the LINICOLS line-colour table at &5600 and checks
// the &ED1B RAINBOW SCREEN layout inject builds: entry 0's scan byte is 0, each
// 4-byte entry's scan byte steps +11, and an &FF terminator appears at the entry
// whose scan would reach >= 166 (the loop's `cp 166 / jr c` bound). This makes the
// stripe redraw emulation-verified (the table is built), distinct from the pixels
// (hardware, i230).
func assertLinicolsStripeTable(t *testing.T, mac *z80h.Machine) {
	t.Helper()
	const linicols = 0x5600
	// The loop emits one 4-byte entry per scan value 0, 11, 22, ... while scan < 166,
	// then writes the &FF terminator. scan values: 0,11,...,154,165 -> 165 < 166 so
	// 165 gets an entry, next would be 176 >= 166 -> terminator. That is 16 entries
	// (0..165 step 11) then the terminator at offset 16*4 = 64.
	scan := 0
	off := 0
	for scan < 166 {
		got := mac.Read(uint16(linicols+off), 4)
		if int(got[0]) != scan {
			t.Errorf("LINICOLS[%d].scan = %d, want %d (&ED1B builds scan stepping +11 from 0)", off/4, got[0], scan)
		}
		if got[1] != 0 {
			t.Errorf("LINICOLS[%d].scan_hi = %d, want 0 (PAL MEM ZERO)", off/4, got[1])
		}
		if got[2] != got[3] {
			t.Errorf("LINICOLS[%d] alt colour %02X != main colour %02X (&ED1B sets alt = main)", off/4, got[3], got[2])
		}
		scan += 11
		off += 4
	}
	if term := mac.Read(uint16(linicols+off), 1)[0]; term != 0xFF {
		t.Errorf("LINICOLS terminator at +%d = %02X, want FF (the &ED1B list terminator)", off, term)
	}
}

// TestSplicedChunk1PatchBytes is the control assertion: the spliced chunk-1 patches
// EXACTLY the 3 bytes at &409E to `CALL inject` (CD 5E 41 = CALL &415E), and leaves
// restore: (&40A1) and the screen tail (&40A9) byte-identical to the original capture
// — proving the splice is the minimal 3-byte change q50 settled, not a wider rewrite.
func TestSplicedChunk1PatchBytes(t *testing.T) {
	_, eeprom := loadRealCaptures(t)
	inject := loadInjectBlob(t)

	spliced, err := samboot.Splice(eeprom[:samboot.ChunkSize], inject)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}

	// &409E (file &9E) = CALL inject (CD <lo> <hi>, inject = &415E).
	wantCall := []byte{0xCD, byte(samboot.InjectLogical & 0xFF), byte(samboot.InjectLogical >> 8)}
	if got := spliced[samboot.SpliceOffset : samboot.SpliceOffset+3]; !bytes.Equal(got, wantCall) {
		t.Errorf("&409E = % X, want % X (CALL inject @&415E)", got, wantCall)
	}

	// restore: (&40A1 / file &A1) + the screen tail are byte-identical to the capture.
	if got, orig := spliced[0xA1:0x15E], eeprom[0xA1:0x15E]; !bytes.Equal(got, orig) {
		t.Errorf("restore:/screen tail (&40A1..&415D) changed by the splice — must be byte-identical to the capture")
	}
	// The body before the splice site is also untouched.
	if got, orig := spliced[0:samboot.SpliceOffset], eeprom[0:samboot.SpliceOffset]; !bytes.Equal(got, orig) {
		t.Error("bootblock body before &409E changed by the splice — only the 3-byte CALL may change")
	}
}
