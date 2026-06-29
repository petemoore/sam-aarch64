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
// WHAT IT RESOLVES (samboot-bootblock-analysis.md §8). The capture's eeprom.bin is
// a CHUNK-ORDERED, ROTATED image: the dumper (netboot_dumper.asm) reads the EEPROM
// by chunk number (chunks 1..128), and chunk 1 lives at device address &2000 (the
// Trinity map: 64-byte index headers at device 0..&1FFF, chunk DATA from &2000). So
// file offset 0 = device &2000, and file offset F = device (F+&2000) mod &20000 (the
// 128 KB device wraps, so the dump's tail carries the index region). CAPTURE-NOTES
// and an earlier analysis pass wrongly assumed file==device, so the emulator served
// the WRONG chunk for device &2000 (a B-DOS helper library at file &2000 = device
// &4000 instead of the bootblock at file 0 = device &2000) and the boot wandered.
// deviceLinearEEPROM (below) un-rotates the capture, and the real boot then runs
// coherently: ROM fetches device &2000 = chunk 1 = the BOOTBLOCK, which loads B-DOS
// chunks 2..13 into &8000 (12 read_chunk loads) and reaches CALL &805F — the EEPROM
// auto-load Pete's card performs. (The EEPROM index even names chunk 1 "Boot Block".)
//
// THE HONESTY LINE (CLAUDE.md §5, §7). This runs the REAL patched ROM and REAL
// EEPROM through the faithful pager + EEPROM SPI model — emulation-first, no flat
// shortcut, no HOSTTEST carve-out. The capture bytes are faithful; only the address
// mapping was wrong and is now corrected. With the harness modelling EI;HALT as the
// frame interrupt resuming it (i258 — a HALT is terminal only when interrupts are
// disabled), the run no longer stops at B-DOS init's first frame-timed delay loop:
// it boots coherently all the way THROUGH B-DOS init to the ROM editor idle loop —
// Colin's full fork reaches BASIC. Emulation-verified is not hardware-verified.
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

	// Bootblock internals (disassembled from chunk 1, ORG &4000) — the coherent boot
	// sequencer the device-linear EEPROM serves at &4000.
	addrReadChunk = 0x40BD // the bootblock's read_chunk helper, CALL'd 12x (B-DOS chunks 2..13)
	addrExecDOS   = 0x409E // CALL &805F — the B-DOS entry, reached after the load loop

	// The ROM editor's keyboard-wait idle loop, reached once B-DOS init completes
	// and hands back to BASIC/the editor. With i258's faithful EI;HALT semantics the
	// boot runs all the way here (a tight spin in the &01C7..&01CB cluster) instead of
	// stopping at B-DOS init's first frame-timed HALT. Used as a deterministic StopPC
	// so the test asserts "reached BASIC" rather than running to the step cap.
	addrEditorIdle = 0x01CB

	// Boot-time paging: ROM0 at section A (LMPR bit5=0), ROM1 at section D (bit6=1),
	// page 0 — the reset map the patched ROM begins executing under.
	bootLMPR = 0x40
	bootHMPR = 0x00

	realBootStepCap = 5_000_000 // generous; the real boot reaches the EEPROM-fetch milestones well within this
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

// requirePrivateCapture returns the path to a proprietary capture (Colin Piggot's
// forked ROM / EEPROM / B-DOS 1.5t — non-redistributable, never in the repo or CI).
// It FAILS HARD if the file is absent, UNLESS SKIP_PRIVATE_TESTS=true is explicitly
// set in the environment (CI sets it, because these artifacts cannot be published).
// No silent skip: the ONLY way these tests skip is that explicit, intentional env
// var (i253 — Pete 2026-06-25, "a missing precondition must FAIL, not skip").
func requirePrivateCapture(t *testing.T, name string) string {
	t.Helper()
	if os.Getenv("SKIP_PRIVATE_TESTS") == "true" {
		t.Skipf("SKIP_PRIVATE_TESTS=true: proprietary capture %q unavailable (Colin's non-redistributable artifact)", name)
	}
	p := realCapturePath(name)
	if p == "" {
		t.Fatalf("proprietary capture %q absent under ~/sam-archive/samboot-capture/ (or $SAMBOOT_CAPTURE_DIR) and SKIP_PRIVATE_TESTS is not set — place the file or set SKIP_PRIVATE_TESTS=true", name)
	}
	return p
}

// loadRealCaptures reads the captured ROM + EEPROM. A missing proprietary capture
// is a HARD FAILURE unless SKIP_PRIVATE_TESTS=true (see requirePrivateCapture).
func loadRealCaptures(t *testing.T) (rom, eeprom []byte) {
	t.Helper()
	romPath := requirePrivateCapture(t, "rom.bin")
	eepPath := requirePrivateCapture(t, "eeprom.bin")
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

// deviceLinearEEPROM un-rotates the CAPTURED eeprom.bin into a true device-linear
// image. The netboot dumper (netboot_dumper.asm) reads the EEPROM by CHUNK NUMBER
// (chunks 1..128 via eeprom.asm read_chunk), and chunk 1 lives at device address
// &2000 — the Trinity EEPROM map has 64-byte index headers at device 0..&1FFF and
// chunk DATA from &2000 (chunk N at &2000+(N-1)*&400). So the captured file is
// ROTATED: file offset F holds device byte (F + &2000) mod &20000 (the 128 KB
// device wraps, so the last chunks of the dump carry the device's header region).
// The Trinity SPI model addresses the device LINEARLY, so the capture MUST be
// un-rotated before loading, else every device read is served the wrong chunk —
// the fidelity bug that made the real-boot trace fetch a B-DOS helper library at
// &4000 instead of the bootblock and wander. device[D] = captured[(D-&2000) mod N].
func deviceLinearEEPROM(captured []byte) []byte {
	const dataBase = 0x2000
	n := len(captured)
	out := make([]byte, n)
	for d := 0; d < n; d++ {
		out[d] = captured[((d-dataBase)%n+n)%n]
	}
	return out
}

// newRealBootMachine builds the emulator with the real ROM in the pager and the
// real EEPROM (un-rotated to device-linear) in the Trinity SPI model, at reset
// paging. This is the i190a core: the single shared SAM-emulation core booting the
// real artifacts.
func newRealBootMachine(t *testing.T, rom, eeprom []byte) (*z80h.Machine, *z80h.ENC28J60) {
	t.Helper()
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load real ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom))
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

// TestRealBootLoadsBootblockAtChunk1 is the §8 resolution: with the EEPROM capture
// un-rotated to device-linear (deviceLinearEEPROM), the real ROM fetches device
// &2000 — chunk 1, the BOOTBLOCK — into &4000 and runs it. (Without the un-rotation
// the model served the captured file's offset &2000, a B-DOS helper library, so the
// boot wandered — the EEPROM-addressing fidelity bug §8 documents.)
func TestRealBootLoadsBootblockAtChunk1(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac, _ := newRealBootMachine(t, rom, eeprom)

	var reached4000 bool
	var first3 []uint16
	if _, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap,
		Trace: func(pc uint16) {
			if pc == addrRunTarget {
				reached4000 = true
			}
			if reached4000 && len(first3) < 3 {
				first3 = append(first3, pc)
			}
		},
	}); err != nil {
		t.Fatalf("real boot run faulted: %v", err)
	}
	if !reached4000 {
		t.Fatal("boot never reached &4000 — the EEPROM fetch + JP did not run")
	}

	// The bytes fetched into &4000 are the bootblock (device &2000 = chunk 1), which
	// starts IN A,(&FA) = 0xDB 0xFA — NOT the helper library (0xE3 EX (SP),HL) the
	// un-rotated capture holds at file offset &2000. With the dumper's chunk-ordering,
	// device &2000 = captured file offset 0, so the bootblock is captured[0:].
	got := mac.Read(addrRunTarget, 4)
	if got[0] != 0xDB || got[1] != 0xFA {
		t.Errorf("&4000 = % X, want DB FA ... (IN A,(&FA) = the bootblock = device &2000 = chunk 1)", got)
	}
	if !bytes.Equal(got, eeprom[0:4]) {
		t.Errorf("&4000 = % X, want captured file offset 0 = % X (device &2000 = chunk 1 = bootblock)", got, eeprom[0:4])
	}

	// Execution begins the bootblock's save-paging prologue: IN A,(&FA) at &4000,
	// LD (nn),A at &4002, OR 64 at &4005 — not a CALL into the &5Cxx sysvar band.
	if len(first3) < 3 || first3[0] != 0x4000 || first3[1] != 0x4002 || first3[2] != 0x4005 {
		t.Errorf("execution from &4000 = %X, want &4000,&4002,&4005 (the bootblock save-LMPR prologue)", first3)
	}
}

// TestRealBootBootsToBASIC is the payoff: Colin's fork boots COHERENTLY all the way
// through B-DOS init to BASIC in the Go core. With the device-linear EEPROM the
// bootblock at &4000 runs the documented EEPROM auto-load — twelve read_chunk loads
// pull B-DOS chunks 2..13 into &8000 and it reaches CALL &805F (the B-DOS entry) —
// and then, with i258's faithful HALT semantics (a HALT only terminates the run when
// interrupts are disabled; an EI;HALT is waiting for the 50 Hz frame interrupt and
// resumes, exactly as real hardware), B-DOS init's frame-timed delay loops (e.g.
// &AB17: LD B,A / HALT / IN (&FE) / DJNZ) spin out instead of stopping the run. The
// boot completes init and hands back to the ROM editor, whose keyboard-wait idle loop
// (the &01C7..&01CB cluster) the boot then spins on — BASIC reached.
//
// Before i258 this test asserted the boot HALTED inside B-DOS init (finalPC>=&8000,
// halted=true); that was an artifact of the harness treating every HALT as terminal.
// With the fix those flip: the boot reaches the editor, so the test now asserts that
// stronger, truer claim via a deterministic StopPC at the idle loop.
func TestRealBootBootsToBASIC(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac, _ := newRealBootMachine(t, rom, eeprom)

	chunkLoads := 0
	var reachedExecDOS bool
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap,
		// Stop deterministically once the ROM editor idle loop is firmly spinning
		// (17th visit) — "reached BASIC" — rather than running to the step cap.
		StopPC:     addrEditorIdle,
		StopPCSkip: 16,
		Trace: func(pc uint16) {
			if pc == addrReadChunk {
				chunkLoads++
			}
			if pc == addrExecDOS {
				reachedExecDOS = true
			}
		},
	})
	if err != nil {
		t.Fatalf("real boot run faulted: %v", err)
	}
	t.Logf("real boot: read_chunk calls=%d, reached CALL &805F=%v, finalPC=&%04X reachedStop=%v steps=%d",
		chunkLoads, reachedExecDOS, res.PC, res.ReachedStop, res.Steps)

	// The coherent EEPROM auto-load proofs that held before i258 still hold.
	if chunkLoads != 12 {
		t.Errorf("bootblock ran %d read_chunk loads, want 12 (B-DOS chunks 2..13) — EEPROM auto-load not coherent", chunkLoads)
	}
	if !reachedExecDOS {
		t.Errorf("boot did not reach CALL &805F — the bootblock did not hand off to a loaded B-DOS")
	}

	// The NEW, stronger claim: the boot runs THROUGH B-DOS init (resuming its
	// frame-timed EI;HALT delay loops) and reaches the ROM editor idle loop — BASIC.
	// reachedStop proves we stopped at the deterministic StopPC, not at the step cap
	// or a wander; finalPC is the editor idle PC in the &0100..&05FF band.
	if !res.ReachedStop {
		t.Fatalf("boot did not reach the ROM editor idle loop &%04X within %d steps (finalPC=&%04X) — B-DOS init did not complete to BASIC",
			addrEditorIdle, res.Steps, res.PC)
	}
	if res.PC != addrEditorIdle {
		t.Errorf("finalPC=&%04X, want the editor idle PC &%04X (reached BASIC)", res.PC, addrEditorIdle)
	}
	if res.PC < 0x0100 || res.PC > 0x05FF {
		t.Errorf("finalPC=&%04X is outside the ROM editor band &0100..&05FF — not the editor idle loop", res.PC)
	}
}

// TestHWSADPagedPointerContract is the i280b-b2h (§8j/§8k) regression guard: it
// drives the REAL B-DOS 1.5t HWSAD hook prelude (&9E1F-&9E60, resident at section
// C; run here at its section-B alias &5E16 with B-DOS paged into section B) and
// proves, against Colin's actual bytes, that the prelude treats hk.hl as a PAGED
// pointer — `out (&fb)` repages SECTION C from H's top 2 bits before the SD writer
// streams the 512 source bytes from there.
//
// The contract (empirically, from the run): the page switched into section C is
// (H>>6)-1, raw, in {0,1,2} — H[7:6]==00 is the <16384 range-error path, never a
// flat/no-switch branch. So our seam's FLAT section-C pointer (BD_WRITE_BUF=&BE42,
// H=&BE -> page 1) repages section C to absolute page 1, DISPLACING B-DOS (its own
// resident page) mid-handler -> the hardware write hang (§8d/§8g: HWSAD_PRE fires,
// HWSAD_POST never does). The FIX direction this test pins: stage the source in an
// absolute page the encoding can name (0/1/2) and pass a correctly-paged hk.hl —
// the positive sub-case proves that then reads OUR buffer, not B-DOS.
func TestHWSADPagedPointerContract(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac, _ := newRealBootMachine(t, rom, eeprom)

	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap, StopPC: addrEditorIdle, StopPCSkip: 16,
	})
	if err != nil {
		t.Fatalf("real boot faulted: %v", err)
	}
	if !res.ReachedStop {
		t.Fatalf("boot did not reach the editor idle loop (finalPC=&%04X) — B-DOS not resident", res.PC)
	}
	dosPage := mac.Pager().HMPR & 0x1F

	const (
		hkA   = 0x81D9
		hkHL  = 0x81DA
		hkDE  = 0x81DC
		hkBC  = 0x81DE
		devNo = 0x780B
		devCl = 0x8135

		aliasHWSAD   = 0x9E16 - 0x4000 // &5E16
		aliasPostOut = 0x9E60 - 0x4000 // &5E60 (the `ld (&780f),hl` right after `out (&fb),a`)
	)

	// runPrelude drives the prelude with hk.hl pre-poked and a clean HMPR (= B-DOS
	// page), trapping at &9E60 to read the page the `out (&fb)` switched into section
	// C. hk.a=1 makes the device-select &8662 RET cleanly. Returns -1 if the out was
	// never reached (the H[7:6]==00 range-error path).
	runPrelude := func(hkhl uint16) int {
		mac.Pager().LMPR = (dosPage - 1) & 0x1F // B-DOS into section B; ROM0 stays at section A
		mac.Pager().HMPR = dosPage              // clean start: section C = B-DOS, every probe
		mac.Write(hkA-0x4000, []byte{1})
		mac.WriteU16LE(hkHL-0x4000, hkhl)
		mac.Write(hkDE-0x4000, []byte{0x02, 0x00})
		mac.Write(hkBC-0x4000, []byte{0, 0})
		mac.Write(devCl, []byte{0x44})
		mac.Write(devNo, []byte{1})
		outPage := -1
		if _, err := mac.RunBootFrom(aliasHWSAD, z80h.Entry{
			StepCap: 2_000_000,
			Trace: func(pc uint16) {
				if pc == aliasPostOut && outPage < 0 {
					outPage = int(mac.Pager().HMPR & 0x1F)
				}
			},
		}); err != nil {
			t.Fatalf("prelude run faulted (hk.hl=&%04X): %v", hkhl, err)
		}
		return outPage
	}

	// The decode: page = (H>>6)-1, raw to HMPR. Each non-zero top-bit pattern
	// repages section C and DISPLACES B-DOS (whose own page the flat pointer names).
	for _, c := range []struct {
		hkhl     uint16
		wantPage int
	}{
		{0x7E42, 0}, // H top=01 -> page 0
		{0xBE42, 1}, // H top=10 -> page 1  (this is our seam's BD_WRITE_BUF=&BE42)
		{0xFE42, 2}, // H top=11 -> page 2
	} {
		got := runPrelude(c.hkhl)
		if got != c.wantPage {
			t.Errorf("hk.hl=&%04X: section C <- page %d, want page %d ((H>>6)-1)", c.hkhl, got, c.wantPage)
		}
		if got == int(dosPage) {
			t.Errorf("hk.hl=&%04X: section C page %d did not change from B-DOS page %d — the repage that should hang did not happen", c.hkhl, got, dosPage)
		}
	}
	if dosPage == 1 {
		t.Fatalf("B-DOS booted into page 1, which collides with this test's assumption (the flat-&BE42 displacement) — investigate the boot paging")
	}

	// The range-error path: a section-A pointer (H[7:6]==00) never reaches the out.
	if got := runPrelude(0x3E42); got != -1 {
		t.Errorf("hk.hl=&3E42 (H[7:6]==00): out reached (page %d), want the &9E89 <16384 range-error path (no page-switch)", got)
	}

	// The FIX mechanism: a correctly-paged hk.hl reads OUR buffer. Stage a sentinel
	// in absolute page 1 at the in-page offset the prelude will read (post-switch
	// source addr for hk.hl=&BE42 is section-C &BE42 = page-1 offset &3E42), then
	// drive the prelude and confirm section C (now page 1) holds the sentinel — i.e.
	// the SD writer would stream OUR bytes, not B-DOS's.
	const srcPage, inPageOff = 1, 0xBE42 - 0x8000
	sentinel := [4]byte{0xDE, 0xAD, 0xBE, 0xEF}
	for i, b := range sentinel {
		mac.Pager().RAM[srcPage][inPageOff+i] = b
	}
	if got := runPrelude(0xBE42); got != srcPage {
		t.Fatalf("positive: section C <- page %d, want page %d", got, srcPage)
	}
	if got := mac.Read(0xBE42, 4); !bytes.Equal(got, sentinel[:]) {
		t.Errorf("positive: after paging page %d into section C, &BE42 reads % X, want the sentinel % X (a correctly-paged hk.hl must read our buffer)", srcPage, got, sentinel[:])
	}
}

// (The earlier TestRealBootChunk1CallsZeroedStreamVar / TestRealBootInitRunsBeforeChunk1
// tests were removed: they studied a B-DOS helper library that the EEPROM-addressing
// bug made the boot wander into. With the device-linear fix the boot runs the
// bootblock and never touches the &5C26 STREAMS band, so those tests no longer
// describe the real boot. See §8 of samboot-bootblock-analysis.md.)
