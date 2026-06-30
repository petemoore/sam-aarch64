// dumper_rompaging_test.go — i181: the SAMBOOT dumper's ROM-paging reads, run
// under the netboot emulator's NEW single paged memory model (sampage). Until
// i181 the netboot harness was flat and did not act on LMPR/HMPR, so the
// dumper's `ifndef NETBOOT_HOSTTEST` ROM-paging path (dumper_read_rom0 /
// dumper_read_rom1) could not be exercised host-side — it shipped straight to
// hardware with only `VERIFY ON HARDWARE` comments, and dumper_read_rom1 crashed
// the real SAM during the i87a rom1 capture (2026-06-21). These tests load the
// REAL (non-NETBOOT_HOSTTEST) dumper — the build trinload pushes, with the ROM
// path compiled IN — into the paged emulator as if trinload had pushed it to
// page P, and:
//
//   - TestDumperReadROM0CopiesToStage proves the now-runnable rom0 read works:
//     ROM0 is readable through section A, STAGE (section D) is writable RAM, the
//     ldir copies it, and LMPR is saved/restored so the routine RETs cleanly.
//
//   - TestDumperReadROM1StagesToStage verifies the i188 fix of the i87a crash.
//     The crash: with push page P=1, the old dumper picked scratch page P-1 = 0,
//     ldir'd ROM1 into the SAM's low memory, and (its copy clobbering C) left LMPR
//     at &00 — section B remapped off the trinload that pushed it. The fix reads
//     ROM1 into STAGE's OWN page (P+1, provably free — the EEPROM/rom0 captures
//     stage there successfully) via section A, restores the entry LMPR from memory
//     before the RET, and serves via the default SRC_PTR. The test asserts the
//     correct behaviour: page 0 untouched, LMPR restored to the entry value, ROM1
//     correctly staged in STAGE, and a clean RET — i.e. the i87a defect cannot
//     recur. (This is the green descendant of i181's characterization test, flipped
//     once i188 landed the fix it had locked.)
//
// Emulation-verified is not hardware-verified (CLAUDE.md §5): these prove the
// paging logic; the captured ROM bytes themselves are still hardware-gated
// (i87a/i87b). The real extracted ROM + EEPROM now boot in this same core via
// samboot_real_boot_test.go (i190a), the boot-trace prerequisite for i197c.
package z80_test

import (
	"bytes"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	"github.com/petemoore/sam-aarch64/tools/sampage"
)

const (
	// The dumper build — the one trinload pushes, with dumper_read_rom0/rom1 +
	// dumper_main compiled in. Since i231b-b4e there is a single build (no
	// NETBOOT_HOSTTEST carve-out); `make netboot-dumper`.
	dumperTLBin = "../../../build/netboot_dumper.bin"
	dumperTLMap = "../../../build/netboot_dumper.map"

	// trinload's X packet does `out (HMPR),P; jp &8000`, so the dumper runs at
	// section C with HMPR = the push page P. For the i87a capture P was 1
	// (TestTrinloadPushRunReturn pushes to page 1) — the value that makes the
	// rom1 scratch page P-1 collide with page 0.
	dumperPushPage = 1

	stageBytes = 16384 // one served region; STAGE = &C000 (sampage section D)
)

// distinctFill builds a recognisable 16 KB pattern with a per-region seed, so a
// clobber/copy is unambiguous (not a constant fill that could match by accident).
func distinctFill(seed int) []byte {
	b := make([]byte, stageBytes)
	for i := range b {
		b[i] = byte((i*7 + seed*131 + 3) & 0xff)
	}
	return b
}

// loadDumperPaged loads the real dumper into the paged emulator as trinload would
// have: code at section C (HMPR = push page P), with the given entry LMPR.
func loadDumperPaged(t *testing.T, lmpr uint8) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(dumperTLBin); err != nil {
		t.Fatalf("real dumper not built (%s); run `make netboot-dumper`", dumperTLBin)
	}
	mac, err := z80h.LoadPaged(dumperTLBin, dumperTLMap, lmpr, dumperPushPage)
	if err != nil {
		t.Fatalf("LoadPaged dumper: %v", err)
	}
	return mac
}

// TestDumperReadROM0CopiesToStage proves the rom0 ROM-paging read works under the
// pager: ROM0 reads through section A, STAGE (section D RAM) takes the copy, and
// the saved/restored LMPR lets it RET clean.
func TestDumperReadROM0CopiesToStage(t *testing.T) {
	// Entry LMPR 0x24: section A = page 4, section B = page 5 (the stack). HMPR=1
	// puts the dumper code at section C = page 1 and STAGE (section D) at page 2,
	// so the stack page (5) is clear of STAGE (2) — the rom0 ldir cannot trample
	// the return address.
	mac := loadDumperPaged(t, 0x24)
	rom0 := distinctFill(0)
	copy(mac.Pager().ROM[0:sampage.PageSize], rom0) // ROM0 = low 16 KB

	res, err := mac.CallEntry("dumper_read_rom0", z80h.Entry{})
	if err != nil {
		t.Fatalf("call dumper_read_rom0: %v", err)
	}
	if !res.Halted {
		t.Fatalf("dumper_read_rom0 did not RET cleanly (PC=&%04X) — LMPR save/restore broken", res.PC)
	}
	// STAGE = &C000 = section D = page HMPR+1 = page 2. It must now hold ROM0.
	const stagePage = dumperPushPage + 1
	got := mac.Pager().RAM[stagePage][:sampage.PageSize]
	if !bytes.Equal(got, rom0) {
		for i := range rom0 {
			if got[i] != rom0[i] {
				t.Fatalf("STAGE byte %d = 0x%02x, want 0x%02x (ROM0 not copied through paging)", i, got[i], rom0[i])
			}
		}
	}
}

// TestDumperReadROM1StagesToStage verifies the i188 fix: rom1 stages ROM1 into
// STAGE via STAGE's own (free) page, leaves page 0 + the entry LMPR + section B
// untouched, and RETs cleanly — the i87a crash cannot recur. See the file header.
func TestDumperReadROM1StagesToStage(t *testing.T) {
	// Entry LMPR 0x24: section A = page 4, section B = page 5 (= "trinload"'s
	// window, which must survive), HMPR=1 -> section C = dumper code (page 1),
	// section D = STAGE (page 2 = P+1). The fix's scratch is page 2 (STAGE's own
	// page), distinct from page 0 (low memory) and page 5 (trinload).
	const (
		entryLMPR = 0x24
		stagePage = dumperPushPage + 1 // P+1 = 2
	)
	mac := loadDumperPaged(t, entryLMPR)

	rom1 := distinctFill(1)
	copy(mac.Pager().ROM[sampage.PageSize:2*sampage.PageSize], rom1) // ROM1 = high 16 KB

	// Seed page 0 (the SAM's low memory — what the i87a bug destroyed) so we can
	// assert the fix never touches it.
	page0Sentinel := distinctFill(99)
	copy(mac.Pager().RAM[0][:], page0Sentinel)

	res, err := mac.CallEntry("dumper_read_rom1", z80h.Entry{})
	if err != nil {
		t.Fatalf("call dumper_read_rom1: %v", err)
	}
	if !res.Halted {
		t.Fatalf("dumper_read_rom1 did not RET cleanly (PC=&%04X) — paging not restored", res.PC)
	}

	// ROM1 staged into STAGE (page P+1), served via the default SRC_PTR.
	if got := mac.Pager().RAM[stagePage][:sampage.PageSize]; !bytes.Equal(got, rom1) {
		for i := range rom1 {
			if got[i] != rom1[i] {
				t.Fatalf("STAGE byte %d = 0x%02x, want 0x%02x (ROM1 not staged correctly)", i, got[i], rom1[i])
			}
		}
	}

	// Page 0 (low memory) UNTOUCHED — the i87a scratch-page collision is gone.
	if got := mac.Pager().RAM[0][:sampage.PageSize]; !bytes.Equal(got, page0Sentinel) {
		t.Errorf("page 0 was modified — the i87a low-memory clobber recurred (scratch must be STAGE's page %d, not page 0)", stagePage)
	}

	// Entry LMPR restored — section B still maps trinload's page, so the dumper's
	// RET to trinload lands cleanly (the i87a no-restore / ldir-clobbered-C defect
	// is gone).
	if got := mac.Pager().LMPR; got != entryLMPR {
		t.Errorf("LMPR = &%02X after rom1, want the entry value &%02X (paging not restored)", got, entryLMPR)
	}
}

// TestDumperServeROM1RoundTrip is the end-to-end emulation proof: drive a real
// bare-RRQ TFTP transfer of rom1.bin through serve_serve_once with the
// dumper_refresh_region hook armed (which invokes the fixed dumper_read_rom1),
// and assert the streamed DATA reconstructs the programmed ROM1 fixture
// byte-for-byte. This exercises the FULL path that crashed on hardware — the
// ROM-paging read + the hook dispatch + the serve loop streaming STAGE — in the
// emulator, the verification the i87a capture lacked (CLAUDE.md §7). It reuses
// the EEPROM round-trip machinery (streamRegion etc.); the only difference is the
// trinload build (ROM path compiled in) loaded paged, and ROM1 as the source.
func TestDumperServeROM1RoundTrip(t *testing.T) {
	mac := loadDumperPaged(t, 0x24)
	rom1 := distinctFill(1)
	copy(mac.Pager().ROM[sampage.PageSize:2*sampage.PageSize], rom1) // ROM1 = high 16 KB

	fillDumperConfig(t, mac) // CONFIG_* + STORE/SRC_TABLE (rom1.bin -> STAGE)
	enc := z80h.NewENC28J60()
	initDumperDriver(t, mac, enc)

	got, blocks := streamRegion(t, mac, enc, "rom1.bin")
	if !bytes.Equal(got, rom1) {
		for i := range rom1 {
			if got[i] != rom1[i] {
				t.Fatalf("rom1.bin byte %d = 0x%02x, want 0x%02x (ROM1 not served correctly)", i, got[i], rom1[i])
			}
		}
	}
	// 16 KB at 512-byte blocks = 32 full + 1 zero-length terminator.
	if want := stageBytes/512 + 1; blocks != want {
		t.Errorf("rom1.bin transfer took %d DATA blocks, want %d", blocks, want)
	}
	// And page 0 (low memory) must still be pristine after a full serve.
	var zero [sampage.PageSize]byte
	if !bytes.Equal(mac.Pager().RAM[0][:], zero[:]) {
		t.Errorf("page 0 was modified during the rom1 serve — the i87a clobber recurred")
	}
}

// dumperAllRegions is the dumper's full region set, in dump_store_tmpl order: rom0,
// rom1, then eep0..eep7 — DUMP_REGION_COUNT (10) regions. Serving each once is what
// completes the headless dump.
var dumperAllRegions = []string{
	"rom0.bin", "rom1.bin",
	"eep0.bin", "eep1.bin", "eep2.bin", "eep3.bin",
	"eep4.bin", "eep5.bin", "eep6.bin", "eep7.bin",
}

// stageDumperRegionSources programs every region's backing store so a full serve of
// each region succeeds: ROM0/ROM1 fixtures (read by the paging ldir) and the 8
// EEPROM regions' 16 chunks each (read by dumper_read_eeprom_region). The bytes are
// distinct per region so a mis-served region would be caught by streamRegion's own
// round-trip checks elsewhere; here we only need the reads to succeed and mark.
func stageDumperRegionSources(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	copy(mac.Pager().ROM[0:sampage.PageSize], distinctFill(0))                   // ROM0 -> rom0.bin
	copy(mac.Pager().ROM[sampage.PageSize:2*sampage.PageSize], distinctFill(1))  // ROM1 -> rom1.bin
	for region := 0; region < 8; region++ {                                      // eep0..eep7
		programRegionChunks(enc, region, distinctRegionBytes())
	}
}

// TestDumperHeadlessAutoRETAfterAllRegions is the i192 headline check: after EVERY
// region has been served once over the wire, dm_serve_loop RETs to trinload with NO
// Esc keypress — the headless single dump-and-return. It loads the REAL trinload
// build (the one with dm_serve_loop + dumper_main compiled in) paged as trinload
// pushes it, serves all 10 regions through the real serve_serve_once dispatcher
// (each marking its served flag in dumper_refresh_region), then runs dm_serve_loop
// and asserts a clean RET (res.Halted = the loop reached trinload's pushed return
// trap) with the keyboard matrix at its default 0xFF (no key held). This is the
// emulation proof that a remote operator gets the SAM back with no keypress.
func TestDumperHeadlessAutoRETAfterAllRegions(t *testing.T) {
	mac := loadDumperPaged(t, 0x24)
	fillDumperConfig(t, mac)
	enc := z80h.NewENC28J60()
	initDumperDriver(t, mac, enc)
	stageDumperRegionSources(t, mac, enc)

	// Before any serve, the loop must NOT auto-RET: dumper_all_served is false, so
	// with no Esc held the loop spins on the empty wire (CallEntry surfaces a
	// non-returning routine as an error). This proves the auto-RET is gated on
	// completion, not fabricated.
	if _, err := mac.CallEntry("dm_serve_loop", z80h.Entry{StepCap: 200_000}); err == nil {
		t.Fatal("dm_serve_loop RETd before any region was served — the auto-RET is not gated on completion")
	}

	// Serve every region once over the wire. Each full transfer drives
	// dumper_refresh_region at rrq_hit, which marks that region's served flag.
	for _, name := range dumperAllRegions {
		got, blocks := streamRegion(t, mac, enc, name)
		if len(got) != stageBytes {
			t.Fatalf("%s: streamed %d bytes, want %d", name, len(got), stageBytes)
		}
		if want := stageBytes/512 + 1; blocks != want {
			t.Errorf("%s: transfer took %d DATA blocks, want %d", name, blocks, want)
		}
	}

	// dumper_all_served must now report complete: CY set (A != 0 after `scf`+ret in
	// the all-served path). CallEntry returns the flags via res — assert via the
	// flag bytes directly for an unambiguous check.
	flags := mac.Read(symAddr(t, mac, "dump_served_flags"), 10)
	for i, f := range flags {
		if f != 1 {
			t.Fatalf("dump_served_flags[%d] = %d after serving all regions, want 1 — region %q not marked",
				i, f, dumperAllRegions[i])
		}
	}

	// The headless exit: with the keyboard at default 0xFF (NO Esc), dm_serve_loop
	// must RET to trinload because every region is served. res.Halted = the loop
	// reached the pushed HALT trap (trinload's return address) — a clean RET.
	if mac.PendingKeys() != 0 {
		t.Fatalf("unexpected pending keys (%d) — the auto-RET must need no keypress", mac.PendingKeys())
	}
	res, err := mac.CallEntry("dm_serve_loop", z80h.Entry{StepCap: 200_000})
	if err != nil {
		t.Fatalf("call dm_serve_loop after all regions served: %v", err)
	}
	if !res.Halted {
		t.Fatalf("dm_serve_loop did NOT RET after all regions served (PC=&%04X, steps=%d) — headless auto-RET broken",
			res.PC, res.Steps)
	}
}

// TestDumperEscStillExitsBeforeComplete proves the manual abort is preserved: with
// regions still outstanding (the auto-RET path NOT taken), holding Esc at the
// keyboard still RETs to trinload at the top of dm_serve_loop — the operator can
// always bail out early. This guards that adding the auto-RET did not remove the
// Esc exit.
func TestDumperEscStillExitsBeforeComplete(t *testing.T) {
	mac := loadDumperPaged(t, 0x24)
	fillDumperConfig(t, mac)
	enc := z80h.NewENC28J60()
	initDumperDriver(t, mac, enc)
	stageDumperRegionSources(t, mac, enc)

	// No region served yet -> dumper_all_served is false; the ONLY exit is Esc.
	mac.PressEsc(true)
	res, err := mac.CallEntry("dm_serve_loop", z80h.Entry{StepCap: 200_000})
	if err != nil {
		t.Fatalf("call dm_serve_loop with Esc held: %v", err)
	}
	if !res.Halted {
		t.Fatalf("dm_serve_loop did not RET with Esc held (PC=&%04X) — the manual abort regressed", res.PC)
	}
}
