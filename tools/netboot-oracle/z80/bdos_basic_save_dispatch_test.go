package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// §8z-correction (i280b-b2s): a faithful BASIC-command rig.
//
// The prior rig (bdos_save_capture_wip_test.go) drove commands by injecting keys
// then RESUMING the run at addrEditorIdle=&01CB. But &01CB is the ROM's HLJPI
// `JP (HL)` trampoline, NOT an editor idle loop, and RunBootFrom resets SP to a
// synthetic stack each call — so resuming there did not run the editor: it jumped
// to &0000 (cold reset) and ran the power-on RAM test (MNINIT &EBAE; the LDIR at
// &EBC9 "CLEAR A PAGE"), which zeroed DOSFLG (&5BC2), then re-booted B-DOS. Every
// RECORD/SAVE/DOSFLG observation in §8z was that reboot loop, not command
// execution — so the "page 31 / system-var page clobbered" root cause was an
// artifact of the broken resume point, not a real paging-fidelity gap.
//
// The faithful idle/resume point is WTKY2 (&04FA) — the editor's key-wait spin
// (CALL INPUTAD / RET C / JR Z,WTKY2; SAM ROM v3.0 disasm) — combined with the
// harness Continue() primitive, which resumes the SAME CPU (PC, SP, registers,
// IFF) in place rather than re-entering with a reset stack. With that, the
// injected keys flow through the real editor -> TOKMAIN -> LINESCAN -> the command
// interpreter, exactly as a user typing at the keyboard.
//
// This test asserts the corrected facts:
//  1. The boot reaches the genuine editor key-wait idle (WTKY2 &04FA), DOSFLG=&1D.
//  2. A plain BASIC command (PRINT) executes end-to-end (reaches LINERUN) and
//     DOSFLG stays &1D (no reboot — the §8z artifact is gone).
//  3. BASIC `SAVE "x"CODE addr,len` dispatches faithfully to the DOS SAVE hook
//     (HSAVE, code 132): it tokenises, syntax-checks, executes, and reaches the
//     B-DOS HSAVE handler via the ROM PTDOS dispatch. (It then errors "Missing
//     disc" / "Invalid device" because the current device is not a selected
//     Trinity record — selecting one needs B-DOS's BASIC keyword RECORD/DEVICE,
//     which are command-table extensions at &8277, not base-ROM keywords, and are
//     not recognised in this boot snapshot — tracked separately. The gold CMD24
//     write trace is gated on that faithful record-select, NOT on the SAVE
//     dispatch, which this proves works.)
const wtky2Idle = 0x04FA

func bootToEditorIdle(t *testing.T) (*z80h.Machine, *[]capEv) {
	t.Helper()
	rom, eeprom := loadRealCaptures(t)
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom))
	sd := enc.AttachSD(csdV2(0x001D59))
	rec1 := make([]byte, 512)
	copy(rec1[232:236], []byte("BDOS"))
	sd.SeedSector(152, rec1)

	var lastPC uint16
	lg := &[]capEv{}
	mac.AttachIO(&capIO{inner: enc, lastPC: &lastPC, log: lg})
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: 80_000_000, StopPC: wtky2Idle, StopPCSkip: 0, FrameIntPeriod: 60000,
		Trace: func(pc uint16) { lastPC = pc },
	})
	if err != nil || !res.ReachedStop {
		t.Fatalf("boot did not reach editor key-wait idle WTKY2 (&04FA): %v reached=%v PC=&%04X",
			err, res.ReachedStop, res.PC)
	}
	return mac, lg
}

// editorRunLine injects a line + Enter and Continues the editor in place until it
// idles again at WTKY2 (queue drained). Returns the landmark names reached and the
// DOS hook/error codes passed to PTDOS, plus the final ERRNR.
func editorRunLine(t *testing.T, mac *z80h.Machine, line string) (hooks []string, hookCodes []uint8, errnr uint8) {
	t.Helper()
	landmarks := map[uint16]string{
		0x380B: "PTDOS", 0x0D2F: "LINERUN",
		0x5D54: "HSAVE", 0x4662: "devsel",
		0x68F4: "SDwrite", 0x6925: "CMD24",
	}
	mac.InjectKeys(append([]byte(line), 0x0D))
	seen := map[uint16]bool{}
	r, err := mac.Continue(z80h.Entry{
		StepCap: 40_000_000, FrameIntPeriod: 60000,
		StopPC: wtky2Idle, StopPCSkip: 200,
		Trace: func(pc uint16) {
			if n, ok := landmarks[pc]; ok && !seen[pc] {
				seen[pc] = true
				hooks = append(hooks, n)
			}
			if pc == 0x380E && len(hookCodes) < 40 { // PTDOS &380E: LD E,A (hook/error code)
				hookCodes = append(hookCodes, mac.LiveRegs().A)
			}
		},
	})
	if err != nil {
		t.Fatalf("editor Continue for %q faulted: %v", line, err)
	}
	if !r.ReachedStop {
		t.Fatalf("editor did not return to idle after %q (PC=&%04X) — line not fully processed", line, r.PC)
	}
	if mac.PendingKeys() != 0 {
		t.Fatalf("editor left %d keys unconsumed after %q — line not fully entered", mac.PendingKeys(), line)
	}
	return hooks, hookCodes, mac.Read(0x5C3A, 1)[0]
}

func TestBASICSaveDispatchesToHSAVE(t *testing.T) {
	mac, _ := bootToEditorIdle(t)

	if got := mac.Read(0x5BC2, 1)[0]; got != 0x1D {
		t.Fatalf("at editor idle DOSFLG(&5BC2)=&%02X, want &1D (B-DOS resident)", got)
	}

	// (2) A plain command executes; DOSFLG stays resident (no reboot artifact).
	hooks, _, errnr := editorRunLine(t, mac, "PRINT 1")
	if !contains(hooks, "LINERUN") {
		t.Errorf("PRINT 1 did not reach LINERUN (command interpreter); hooks=%v", hooks)
	}
	if errnr != 0x00 {
		t.Errorf("PRINT 1 ERRNR=&%02X, want &00 (OK)", errnr)
	}
	if got := mac.Read(0x5BC2, 1)[0]; got != 0x1D {
		t.Fatalf("after PRINT 1 DOSFLG=&%02X, want &1D — DOSFLG was lost (the §8z reboot artifact must NOT recur)", got)
	}

	// (3) SAVE dispatches to the DOS SAVE hook (HSAVE=132) and reaches the B-DOS
	// HSAVE handler. Stage the code first.
	for i := 0; i < 20; i++ {
		mac.Write(0x8000+uint16(i), []byte{byte(0xA0 + i)})
	}
	hooks, hookCodes, _ := editorRunLine(t, mac, `SAVE "x"CODE 32768,20`)
	if !contains(hooks, "LINERUN") {
		t.Errorf("SAVE did not reach LINERUN — keyword not tokenised/executed; hooks=%v", hooks)
	}
	if !containsU8(hookCodes, 132) {
		t.Errorf("SAVE did not pass DOS hook code 132 (HSAVE) to PTDOS; codes=%v", hookCodes)
	}
	if !contains(hooks, "HSAVE") {
		t.Errorf("SAVE did not reach the B-DOS HSAVE handler (&9D54); hooks=%v", hooks)
	}
	if got := mac.Read(0x5BC2, 1)[0]; got != 0x1D {
		t.Fatalf("after SAVE DOSFLG=&%02X, want &1D — DOSFLG lost (§8z reboot artifact)", got)
	}
	t.Logf("BASIC SAVE dispatches faithfully: LINERUN -> HSAVE(132) -> B-DOS HSAVE handler; DOSFLG resident throughout (the §8z 'page-31 lost' root cause was a broken-resume artifact, now fixed via WTKY2+Continue). hooks=%v", hooks)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func containsU8(s []uint8, v uint8) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
