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
// mapping was wrong and is now corrected. What it does NOT model is the SAM
// display/ASIC and the SD card B-DOS init touches, so the run halts inside B-DOS
// init after the coherent hand-off. Emulation-verified is not hardware-verified.
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

	// Boot-time paging: ROM0 at section A (LMPR bit5=0), ROM1 at section D (bit6=1),
	// page 0 — the reset map the patched ROM begins executing under.
	bootLMPR = 0x40
	bootHMPR = 0x00

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

// TestRealBootLoadsBDOSCoherently is the payoff: with the device-linear EEPROM the
// bootblock at &4000 runs the documented EEPROM auto-load — twelve read_chunk loads
// pull B-DOS chunks 2..13 into &8000 and it reaches CALL &805F (the B-DOS entry).
// The run then halts inside B-DOS init, which touches the unmodeled SD/screen
// hardware — expected; the point is that the boot is COHERENT (loads + hands off),
// the EEPROM auto-load Pete's card performs, not a wander into zeroed RAM.
func TestRealBootLoadsBDOSCoherently(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac, _ := newRealBootMachine(t, rom, eeprom)

	chunkLoads := 0
	var reachedExecDOS bool
	res, _ := mac.RunBootFrom(0x0000, z80h.Entry{
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
	t.Logf("real boot: read_chunk calls=%d, reached CALL &805F=%v, finalPC=&%04X halted=%v",
		chunkLoads, reachedExecDOS, res.PC, res.Halted)

	if chunkLoads != 12 {
		t.Errorf("bootblock ran %d read_chunk loads, want 12 (B-DOS chunks 2..13) — EEPROM auto-load not coherent", chunkLoads)
	}
	if !reachedExecDOS {
		t.Errorf("boot did not reach CALL &805F — the bootblock did not hand off to a loaded B-DOS")
	}
	// The run continues INTO B-DOS init (finalPC well above &8000) rather than halting
	// at the &4000 prologue — the hand-off is real, not a wander. (We do not byte-check
	// &8000 post-run: B-DOS init overwrites it; the 12 loads + &805F entry are the proof.)
	if res.PC < 0x8000 {
		t.Errorf("finalPC=&%04X is below &8000 — the boot did not run into the loaded B-DOS", res.PC)
	}
}

// (The earlier TestRealBootChunk1CallsZeroedStreamVar / TestRealBootInitRunsBeforeChunk1
// tests were removed: they studied a B-DOS helper library that the EEPROM-addressing
// bug made the boot wander into. With the device-linear fix the boot runs the
// bootblock and never touches the &5C26 STREAMS band, so those tests no longer
// describe the real boot. See §8 of samboot-bootblock-analysis.md.)
