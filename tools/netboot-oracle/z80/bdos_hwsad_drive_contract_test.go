package z80_test

import (
	"fmt"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// bdos_hwsad_drive_contract_test.go — i280b-b2t (§8af): the raw sector hooks
// (HRSAD/HWSAD) MUST present A = 2 (Trinity SD) to the B-DOS rwsad prelude's
// device-select (&8662), because the prelude device-selects on hk.a = the
// caller's MAIN A at the RST 8. Our serve's prior code left main A = the sector
// number, so the FIRST .mgt sector (linear 0 -> sector 1) selected A=1 = FLOPPY
// -> the un-timed FDC poll = the hardware write hang.
//
// Two complementary guards:
//   1. TestServeRawSectorHooksPresentTrinityDrive — a static byte-guard on the
//      REAL assembled serve binary: the single HWSAD/HRSAD RST 8 each loads A=2
//      (ld a,2 = &3E &02) immediately before it. Catches bdos_seam.asm regressing
//      the drive value (the rst-8 path is NETBOOT_HOSTTEST-excluded, so it cannot
//      be unit-tested via the flat seam bin — this checks the production image).
//   2. TestHWSADFaithfulDriveSelectsByMainA — a behavioural guard in the faithful
//      Continue/WTKY2 rig (the trustworthy rig; the §8h/§8s armed-dispatch harness
//      is &01CB-reboot-artifact-contaminated, §8ae): after a real RECORD 1, a raw
//      HWSAD with A=2 reaches the SD write core (CMD24) and returns clean, while
//      the pre-fix A=1 spins in the B-DOS floppy poll (the hang) and issues no
//      CMD24. This re-derives the "hk.a = MAIN A" contract faithfully.

// rstSites returns every offset in b where opcode 0xCF (RST 8) is followed by the
// inline byte `defb`.
func rstSites(b []byte, defb byte) []int {
	var out []int
	for i := 0; i+1 < len(b); i++ {
		if b[i] == 0xCF && b[i+1] == defb {
			out = append(out, i)
		}
	}
	return out
}

func TestServeRawSectorHooksPresentTrinityDrive(t *testing.T) {
	b, err := os.ReadFile(serveBootBin)
	if err != nil {
		t.Fatalf("serve boot binary not built (%v); run `make netboot-serve-boot`", err)
	}
	// HWSAD = hook 149 (&95), HRSAD = hook 160 (&A0). Each must appear exactly once
	// in the serve image and be immediately preceded by `ld a,2` (&3E &02).
	for _, h := range []struct {
		name string
		defb byte
	}{{"HWSAD", 0x95}, {"HRSAD", 0xA0}} {
		sites := rstSites(b, h.defb)
		if len(sites) != 1 {
			t.Fatalf("%s: expected exactly one `rst 8 / defb %d` site in the serve image, found %d at %v",
				h.name, h.defb, len(sites), sites)
		}
		off := sites[0]
		if off < 2 || b[off-2] != 0x3E || b[off-1] != 0x02 {
			pre := "<start>"
			if off >= 2 {
				pre = fmt.Sprintf("&%02X &%02X", b[off-2], b[off-1])
			}
			t.Fatalf("%s rst 8 at file offset 0x%X is NOT preceded by `ld a,2` (&3E &02 = BD_DRIVE_TRINITY); preceding bytes = %s. "+
				"The rwsad prelude device-selects on main A; A!=2 routes to floppy (the FDC-poll hang) instead of Trinity SD.",
				h.name, off, pre)
		}
	}
}

// --- behavioural faithful-rig guard (private captures) ---

const (
	mcHWSADEntryB = 0x5E16 // &9E16 HWSAD handler entry, section-B alias
	mcDevselB     = 0x4662 // &8662 device-select, section-B alias
	mcSDWriteB    = 0x68F4 // &A8F4 SD write core, section-B alias
	mcCMD24B      = 0x6925 // &A925 CMD24 sender, section-B alias
)

// driveRawHWSAD stages our serve's HWSAD invocation shape (ld a,drive last, then
// rst 8 / defb 149) and runs it faithfully via ContinueFrom from the post-RECORD
// editor state, preserving the live paging/registers/sysvars. Returns whether it
// reached the SD write core, whether it issued a CMD24, and the run result.
func driveRawHWSAD(t *testing.T, mac *z80h.Machine, lg *[]capEv, drive byte) (reachedWriteCore, sawCMD24 bool, res z80h.CallResult) {
	t.Helper()
	// Source buffer in absolute page 1 at &3E42 so the prelude's out(&fb) repage of
	// (H>>6)-1 = 1 reads HL=&BE42 from there (the §8k paged-pointer contract).
	buf := make([]byte, 512)
	for i := range buf {
		buf[i] = byte(0xC0 + (i & 0x1F))
	}
	sH := mac.Pager().HMPR
	mac.Pager().HMPR = 1
	mac.Write(0xBE42, buf)
	mac.Pager().HMPR = sH

	const stub = 0x9000
	mac.Write(stub, []byte{
		0x11, 0x02, 0x00, // ld de,&0002 (D=track0, E=sector2; sector value is irrelevant now)
		0x21, 0x42, 0xBE, // ld hl,&BE42 (BD_WRITE_BUF, page-1 paged pointer)
		0x3E, drive, // ld a,drive  (the value the prelude reads as hk.a = main A)
		0xCF, 149, // rst 8 ; defb 149 (HWSAD)
		0xC9, // ret
	})
	from := len(*lg)
	var err error
	res, err = mac.ContinueFrom(stub, z80h.Entry{
		StepCap: 8_000_000, FrameIntPeriod: 60000,
		Trace: func(pc uint16) {
			switch pc {
			case mcSDWriteB:
				reachedWriteCore = true
			}
		},
	})
	if err != nil {
		t.Fatalf("ContinueFrom(HWSAD drive=%d) faulted: %v", drive, err)
	}
	frames, _ := decodeFrames(*lg, from)
	for _, f := range frames {
		if f.cmd == 24 {
			sawCMD24 = true
		}
	}
	return reachedWriteCore, sawCMD24, res
}

func TestHWSADFaithfulDriveSelectsByMainA(t *testing.T) {
	// FIX: A=2 (Trinity SD) reaches the write core and issues CMD24, returning clean.
	mac, lg, _ := bootToEditorIdleSD(t)
	editorRunLine(t, mac, "RECORD 1")
	reached, cmd24, res := driveRawHWSAD(t, mac, lg, 2)
	if !reached {
		t.Errorf("HWSAD with A=2 (Trinity SD) did not reach the SD write core &A8F4 (finalPC=&%04X)", res.PC)
	}
	if !cmd24 {
		t.Errorf("HWSAD with A=2 issued no CMD24 (SD write) — the fixed drive value must reach the write core")
	}
	if !res.Halted {
		t.Errorf("HWSAD with A=2 did not return cleanly (halted=%v finalPC=&%04X steps=%d) — expected a clean RET to the trap",
			res.Halted, res.PC, res.Steps)
	}

	// PRE-FIX BUG: A=1 (the first .mgt sector value our old code left in main A)
	// selects the floppy and spins in the B-DOS FDC poll — never reaching the write
	// core, issuing no CMD24, never returning. This is the hardware write hang.
	mac2, lg2, _ := bootToEditorIdleSD(t)
	editorRunLine(t, mac2, "RECORD 1")
	reached1, cmd241, res1 := driveRawHWSAD(t, mac2, lg2, 1)
	if reached1 {
		t.Errorf("HWSAD with A=1 (floppy) unexpectedly reached the SD write core — the device-select-by-main-A model is wrong")
	}
	if cmd241 {
		t.Errorf("HWSAD with A=1 issued a CMD24 — the floppy branch must not write to SD")
	}
	if res1.Halted {
		t.Errorf("HWSAD with A=1 returned cleanly (it must hang in the floppy poll: that is the bug the A=2 fix avoids)")
	}
	t.Logf("contract confirmed: A=2 -> write core + CMD24 + clean RET; A=1 -> floppy poll hang (no write core, no CMD24, no RET). "+
		"hk.a = the caller's MAIN A; our serve now loads A=2 before the rst 8 (finalPC A=1 spin=&%04X)", res1.PC)
}
