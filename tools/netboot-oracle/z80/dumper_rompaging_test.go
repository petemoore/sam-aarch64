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
//   - TestDumperReadROM1ClobbersScratchPage0 REPRODUCES the i87a crash in
//     emulation (the whole point of i181): with the push page P=1 — the value
//     trinload used for the capture (TestTrinloadPushRunReturn pushes to page 1) —
//     dumper_read_rom1 picks scratch page P-1 = page 0, ldirs 16 KB of ROM1 into
//     it (clobbering low memory) and never restores LMPR (remapping section B
//     away from the trinload that pushed it). This is a CHARACTERIZATION test: it
//     asserts the bug is PRESENT, locking the defect the harness can now see.
//     i188 redesigns the dumper (a genuinely-free scratch page, restored paging,
//     a clean RET to trinload) and FLIPS these assertions to the correct
//     behaviour. Per CLAUDE.md §5 the test stays green on main by asserting what
//     the code does today; the fix is the separate i188 change.
//
// Emulation-verified is not hardware-verified (CLAUDE.md §5): these prove the
// paging logic; the captured ROM bytes themselves are still hardware-gated
// (i87a/i87b), and i190a will load the real extracted ROM in place of these
// synthetic fixtures.
package z80_test

import (
	"bytes"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80/sampage"
)

const (
	// The REAL dumper build (no NETBOOT_HOSTTEST) — the one trinload pushes, with
	// dumper_read_rom0/rom1 + dumper_main compiled in. `make netboot-dumper-trinload`.
	dumperTLBin = "../../../build/netboot_dumper_trinload.bin"
	dumperTLMap = "../../../build/netboot_dumper_trinload.map"

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
		t.Skipf("real dumper not built (%s); run `make netboot-dumper-trinload`", dumperTLBin)
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

// TestDumperReadROM1ClobbersScratchPage0 reproduces the i87a rom1 crash in
// emulation. See the file header: this CHARACTERIZES the present bug (scratch
// page P-1 = page 0, LMPR not restored); i188 flips it.
func TestDumperReadROM1ClobbersScratchPage0(t *testing.T) {
	// Entry LMPR 0x22: section A = page 2, section B = page 3. Section B is the
	// &6000 window the trinload that pushed us lives in, so "trinload's page" is
	// page 3 here; we will show the bug leaves section B mapped elsewhere.
	const entryLMPR = 0x22
	mac := loadDumperPaged(t, entryLMPR)

	rom1 := distinctFill(1)
	copy(mac.Pager().ROM[sampage.PageSize:2*sampage.PageSize], rom1) // ROM1 = high 16 KB

	// Seed page 0 — the page the buggy scratch choice (P-1 = 0) will land on —
	// with a stand-in for the SAM's live low memory / trinload state, so its
	// destruction is observable.
	page0Sentinel := distinctFill(99)
	copy(mac.Pager().RAM[0][:], page0Sentinel)

	// Stop exactly at the routine's final RET (the byte before dr_save_lmpr),
	// before it pops a return address off a stack whose section B the routine
	// just remapped — so we inspect the damage cleanly, without the wandering RET
	// executing further and corrupting the post-state.
	retAddr := symAddr(t, mac, "dr_save_lmpr") - 1
	if op := mac.Read(retAddr, 1)[0]; op != 0xC9 {
		t.Fatalf("expected RET (0xC9) at &%04X (dr_save_lmpr-1), got 0x%02x", retAddr, op)
	}
	res, err := mac.RunBoot("dumper_read_rom1", z80h.Entry{StopPC: retAddr})
	if err != nil {
		t.Fatalf("run dumper_read_rom1: %v", err)
	}
	if !res.ReachedStop {
		t.Fatalf("did not reach the rom1 RET (PC=&%04X, steps=%d)", res.PC, res.Steps)
	}

	// BUG 1 — scratch page collision: the routine ldir'd ROM1 into page 0, the
	// SAM's low memory, destroying what was there. A correct dumper (i188) picks
	// a genuinely-free scratch page, leaving page 0 untouched.
	page0 := mac.Pager().RAM[0][:sampage.PageSize]
	if bytes.Equal(page0, page0Sentinel) {
		t.Errorf("page 0 was NOT clobbered — the i87a scratch-page bug did not reproduce (P-1=0 collision expected)")
	}
	if !bytes.Equal(page0, rom1) {
		t.Errorf("page 0 was overwritten but not with ROM1 — unexpected scratch behaviour")
	}

	// BUG 2 — LMPR not restored, and left at &00 (a COMPOUND defect): the routine
	// never restores the entry LMPR, AND the value it does leave is wrong. It
	// stashes the intended post-copy LMPR (&20) in C (`ld c,a`), then does
	// `ld bc,REGION_BYTES; ldir` — the ldir counts BC down to 0, clobbering C — so
	// the final `ld a,c; out (&FA),a` writes &00, not &20. Result: section A=ROM0
	// (bit5=0) and section B=page 1, the trinload-at-&6000 window remapped away
	// from trinload entirely. i188 (free scratch page, registers not clobbered by
	// the copy, entry LMPR restored) flips this to LMPR == entryLMPR.
	if got := mac.Pager().LMPR; got == entryLMPR {
		t.Errorf("LMPR was restored to the entry value &%02X — the i87a no-restore bug did not reproduce", entryLMPR)
	} else if got != 0x00 {
		t.Errorf("LMPR = &%02X after rom1, want &00 (the ldir-clobbered-C scratch restore)", got)
	}
	entrySecB := int(entryLMPR&0x1F+1) & 0x1F
	bugSecB := int(mac.Pager().LMPR&0x1F+1) & 0x1F
	if bugSecB == entrySecB {
		t.Errorf("section B still maps page %d — the trinload-window remap did not reproduce", entrySecB)
	}
	t.Logf("i87a crash reproduced: page 0 clobbered with ROM1; LMPR &%02X->&%02X (section B page %d->%d, away from trinload)",
		entryLMPR, mac.Pager().LMPR, entrySecB, bugSecB)
}
