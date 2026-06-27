package z80_test

import (
	"fmt"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// i280b-b2q (§8r re-aim): drive the serve's own write path — HRECORD(156) then
// HWSAD(149)/HSAVE(132) — through the §8o-armed real-ROM PTDOS dispatch, with the
// §8r mount var-set poked in, and observe whether it reaches the B-DOS SD write
// core (&A8F4, the CMD24 sender) or diverges earlier (the §8r "SAVE issues no SD
// I/O" blocker). This is the capture half of Pete's capture-and-diff plan: it
// drives the write via the HOOK path (what the serve uses), NOT the BASIC SAVE
// command (which §8r found falls back to floppy via the default-device routing).
//
// Under the §8o serve runtime map (LMPR=&1F, HMPR=1, B-DOS in section B via
// PTDOS), the real &9xxx/&Axxx B-DOS addresses appear at their section-B aliases
// (subtract &4000): HWSAD handler &9E16->&5E16, HRECORD &9FAB->&5FAB, HSAVE
// &9D54->&5D54, device-select &8662->&4662, the SD write core &A8F4->&68F4, and
// the CMD24 sender &A925->&6925.

const (
	mcDispatch  = 0x4319 // hook dispatcher (&8319)
	mcHRECORD   = 0x5FAB // HRECORD handler entry (&9FAB)
	mcHSAVE     = 0x5D54 // HSAVE handler entry (&9D54)
	mcHWSAD     = 0x5E16 // HWSAD handler entry (&9E16)
	mcPrelude   = 0x5E27 // HWSAD prelude (&9E27)
	mcDevSel    = 0x4662 // device-select (&8662)
	mcRomBridge = 0x0103 // the &9BF1 -> ROM bridge (§8a escape point)
	mcWriteCore = 0x68F4 // the SD write core (&A8F4) — the CMD24 sender's caller
	mcCMD24     = 0x6925 // the CMD24 frame send (&A925)
)

var writeCoreLandmarks = []struct {
	pc   uint16
	name string
}{
	{mcDispatch, "dispatcher &8319"},
	{mcHRECORD, "HRECORD &9FAB"},
	{mcHSAVE, "HSAVE &9D54"},
	{mcHWSAD, "HWSAD &9E16"},
	{mcPrelude, "HWSAD prelude &9E27"},
	{mcDevSel, "device-select &8662"},
	{mcRomBridge, "ROM bridge &0103"},
	{mcWriteCore, "SD write core &A8F4"},
	{mcCMD24, "CMD24 send &A925"},
}

// bootWriteCoreMachine boots Colin's real ROM + B-DOS 1.5t with a Trinity SD card
// (csdV2(0x001D59): base=152, records=4809) whose record 1 is "BDOS"-stamped, pokes
// the full §8r mount var-set into B-DOS's resident page, then selects record 1 via
// the BASIC `RECORD 1` command (which §8r proved genuinely selects + persists — the
// faithful claim-select state, not hand-poked). Returns the machine + port log.
//
// The §8o dispatch returns to the EDITOR idle loop, not to the caller's stub — so a
// stub can only drive ONE `rst 8` hook per run. To drive HRECORD *then* HWSAD we
// establish the record select the real way (BASIC RECORD) and then §8o-dispatch the
// single write hook against that genuine selected state.
func bootWriteCoreMachine(t *testing.T) (*z80h.Machine, *z80h.ENC28J60, *[]capEv) {
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
	copy(rec1[210:220], []byte("TRINITY1  "))
	copy(rec1[232:236], []byte("BDOS"))
	sd.SeedSector(152, rec1)

	var lastPC uint16
	lg := &[]capEv{}
	mac.AttachIO(&capIO{inner: enc, lastPC: &lastPC, log: lg})
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap, StopPC: addrEditorIdle, StopPCSkip: 16,
		Trace: func(pc uint16) { lastPC = pc },
	})
	if err != nil || !res.ReachedStop {
		t.Fatalf("boot failed: %v reached=%v PC=&%04X", err, res.ReachedStop, res.PC)
	}
	dosPage := mac.Pager().HMPR & 0x1F

	// §8r mount var-set, poked into B-DOS's resident page (physical RAM, so it
	// survives the §8o paging change). Satisfies HRECORD's range-checks.
	var blocks uint32 = 7694336
	pk := func(a uint16, b []byte) {
		saved := mac.Pager().HMPR
		mac.Pager().HMPR = dosPage
		mac.Write(a, b)
		mac.Pager().HMPR = saved
	}
	pk16 := func(a uint16, v uint16) { pk(a, []byte{byte(v), byte(v >> 8)}) }
	pk(0x80BD, []byte{byte(blocks), byte(blocks >> 8), byte(blocks >> 16), byte(blocks >> 24)})
	pk16(0x80C2, 152)        // base
	pk16(0x80C4, 4809)       // last.record
	pk16(0x80C6, 1)          // record.no
	pk(0x80C8, []byte{0x00}) // hd.wp

	// Genuine record select (§8r fact 3): RECORD 1 runs the self-heal init ladder +
	// CMD17 block-152 read and the select persists. This leaves the real claim-select
	// device state (the &780B ambient / device class+number / SD-setup vars) that the
	// §8p HWSAD-traceable test had to hand-set — here it is established faithfully.
	sawCMD0, cmd17 := runCmdLine(mac, lg, "RECORD 1")
	if !sawCMD0 || !containsU32(cmd17, 152) {
		t.Fatalf("RECORD 1 did not self-heal/select (sawCMD0=%v cmd17=%v)", sawCMD0, cmd17)
	}
	if got := rdDOS16(mac, dosPage, 0x80C4); got != 4809 {
		t.Fatalf("RECORD 1 select did not persist: last.record=%d, want 4809", got)
	}

	// §8o arming: serve runtime map + DOSCNT=0 (external caller, recursion guard off).
	mac.Pager().LMPR = 0x1F
	mac.Pager().HMPR = 0x01
	mac.Write(0x5BC3, []byte{0x00})
	return mac, enc, lg
}

// decodeFrames decodes the CMD frames from &DF OUTs in log[from:], returning the
// (cmd, addr, pc) triples and whether any CMD24 (write) was issued.
type sdFrame struct {
	cmd  uint8
	addr uint32
	pc   uint16
}

func fmtPCs(pcs []uint16) string {
	s := ""
	for i, p := range pcs {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%04X", p)
	}
	return s
}

func decodeFrames(log []capEv, from int) (frames []sdFrame, sawWrite bool) {
	var df []capEv
	for i := from; i < len(log); i++ {
		if log[i].write && log[i].port == 0xDF {
			df = append(df, log[i])
		}
	}
	for i := 0; i < len(df); i++ {
		v := df[i].val
		if v&0xC0 == 0x40 && i+4 < len(df) {
			addr := uint32(df[i+1].val)<<24 | uint32(df[i+2].val)<<16 |
				uint32(df[i+3].val)<<8 | uint32(df[i+4].val)
			cmd := v & 0x3F
			frames = append(frames, sdFrame{cmd, addr, df[i].pc})
			if cmd == 24 {
				sawWrite = true
			}
			i += 4
		}
	}
	return frames, sawWrite
}

// driveHookSeq runs the stub from `stub` (already written) and traces the write-core
// landmarks + records the SD command frames issued during the run.
func driveHookSeq(t *testing.T, mac *z80h.Machine, log *[]capEv, stub uint16, label string) (reached map[uint16]bool, frames []sdFrame, sawWrite bool, hkA byte) {
	t.Helper()
	from := len(*log)
	reached = map[uint16]bool{}
	const ringSz = 48
	var ring [ringSz]uint16
	var ringN int
	devSelStep := -1
	var stepCtr int
	// Capture the PC trace from the device-select onward (the divergence window):
	// where the FDC-vs-SD dispatch decides whether to proceed to the write core.
	var postDevSel []uint16
	const postDevSelCap = 400
	res, err := mac.RunBootFrom(stub, z80h.Entry{
		StepCap: 8_000_000,
		Trace: func(pc uint16) {
			ring[ringN%ringSz] = pc
			ringN++
			stepCtr++
			if devSelStep >= 0 && len(postDevSel) < postDevSelCap {
				postDevSel = append(postDevSel, pc)
			}
			for _, m := range writeCoreLandmarks {
				if pc == m.pc {
					reached[pc] = true
					if m.pc == mcDevSel && devSelStep < 0 {
						devSelStep = stepCtr
					}
				}
			}
		},
	})
	frames, sawWrite = decodeFrames(*log, from)
	t.Logf("== %s ==", label)
	for _, m := range writeCoreLandmarks {
		mark := " "
		if reached[m.pc] {
			mark = "x"
		}
		t.Logf("   [%s] %s", mark, m.name)
	}
	// hk.a (&81D9) + the SD-claimed flag (&80AF), as B-DOS sees them — under the §8o
	// map B-DOS is in section B, so they read at their section-B aliases.
	hkA = mac.Read(0x41D9, 1)[0]
	sdClaimed := mac.Read(0x40AF, 1)[0]
	t.Logf("   finalPC=&%04X err=%v steps=%d  SD frames=%d sawWrite=%v devSelStep=%d  hk.a(&81D9)=&%02X &80AF=&%02X", res.PC, err, res.Steps, len(frames), sawWrite, devSelStep, hkA, sdClaimed)
	// Dump the tail ring (the spin loop body, in visit order) when the cap was hit.
	if res.Steps >= 8_000_000 {
		start := 0
		if ringN > ringSz {
			start = ringN - ringSz
		}
		var tail []uint16
		for i := start; i < ringN; i++ {
			tail = append(tail, ring[i%ringSz])
		}
		t.Logf("   spin tail (last %d PCs): %s", len(tail), fmtPCs(tail))
	}
	if len(postDevSel) > 0 {
		t.Logf("   post-devsel trace (%d PCs from &8662): %s", len(postDevSel), fmtPCs(postDevSel))
	}
	for _, f := range frames {
		tag := ""
		if f.cmd == 24 {
			tag = "  <<< CMD24 WRITE"
		}
		fmt.Printf("    CMD%-2d addr=%d (0x%X) @PC=&%04X%s\n", f.cmd, f.addr, f.addr, f.pc, tag)
	}
	return reached, frames, sawWrite, hkA
}

// TestHWSADReachesWriteCore (i280b-b2q, §8s) drives the serve's HWSAD(149) hook
// through the §8o-armed real-ROM PTDOS dispatch, against a GENUINE BASIC `RECORD 1`
// select (the faithful claim-select state, last.record=4809 persisting per §8r),
// and localizes where the write path diverges. This is the capture half of Pete's
// capture-and-diff plan, run via the HOOK path the serve uses (not BASIC SAVE).
//
// The §8s findings this guards (see docs/notes/trinity-sd-z80-interface.md §8s):
//  1. The rig reaches the HWSAD handler (&9E16) -> prelude (&9E27) -> device-select
//     (&8662) end-to-end — the §8o dispatch + genuine RECORD select machinery works.
//  2. The path then DIVERGES at device-select into the &8680 -> &9A8B abort
//     (B-DOS's error reporter), issuing NO SD command and returning to the editor.
//     This reproduces the §8r SAVE/HSAVE blocker via the hook path: the write core
//     (&A8F4) and CMD24 are NOT reached.
//  3. The divergence cause is precisely hk.a: device-select does `cp 1 / cp 2 / jr
//     nz &8680`, and hk.a arrives as 0 (neither floppy=1 nor Trinity-SD=2). hk.a is
//     fed from the ALTERNATE accumulator A' by the dispatcher (&8321 exx / &8322 ex
//     af,af' / &8323 ld (&81D9),a) — but across the external rst-8 entry the ROM
//     path resets the alternate set, so a caller's A' does NOT reach hk.a (asserted:
//     hk.a=0 for BOTH A'=0 and A'=2). This reframes §8b's "force A=2" (which set
//     MAIN A — a proven no-op, the dispatcher never reads it): A' is not a usable
//     lever across this path either.
//
// So reaching the write core needs the device-select to see hk.a=2 (Trinity SD) AND
// the &80AF SD-claimed flag set (the &8677 second gate) — neither of which a genuine
// RECORD select + external HWSAD dispatch establishes here. That is the b2q
// continuation. This test ASSERTS the stable methodology (reaches device-select,
// hk.a=0) and the current divergence (no write core); it flips deliberately when the
// fix that resolves the hk.a/&80AF gate lands.
func TestHWSADReachesWriteCore(t *testing.T) {
	src := uint16(0xBE42)
	const stub = 0x9000
	stubFor := func(aPrime byte) []byte {
		return []byte{
			0x3E, aPrime, // ld a,aPrime
			0x08,             // ex af,af'      ; shadow A = aPrime (the caller's hk.a attempt)
			0x11, 0x02, 0x00, // ld de,&0002 (D=track0 E=sector2)
			0x21, byte(src), byte(src >> 8), // ld hl,BD_WRITE_BUF
			0xCF, 149, // rst 8 ; defb 149 (HWSAD)
			0xF3, 0x76, // di ; halt
		}
	}

	// Fresh boot per A' value (the first dispatch corrupts B-DOS state if reused).
	for _, ap := range []byte{0x00, 0x02} {
		mac, _, log := bootWriteCoreMachine(t)
		buf := make([]byte, 512)
		for i := range buf {
			buf[i] = byte(0xA0 + (i & 0x1F))
		}
		mac.Write(src, buf)
		mac.Write(stub, stubFor(ap))
		label := fmt.Sprintf("RECORD 1 (BASIC) -> HWSAD (hook), A'=%d", ap)
		reached, _, sawWrite, hkA := driveHookSeq(t, mac, log, stub, label)

		// (1) methodology: the rig drives the hook path to device-select.
		for _, m := range []struct {
			pc   uint16
			name string
		}{
			{mcDispatch, "hook dispatcher &8319"},
			{mcHWSAD, "HWSAD handler &9E16"},
			{mcPrelude, "HWSAD prelude &9E27"},
			{mcDevSel, "device-select &8662"},
		} {
			if !reached[m.pc] {
				t.Errorf("A'=%d: handler did not reach %s — the §8o hook-path rig broke", ap, m.name)
			}
		}
		// (3) hk.a arrives as 0 regardless of the caller's A' (the dispatch-path reset).
		if hkA != 0 {
			t.Errorf("A'=%d: hk.a(&81D9)=&%02X, want &00 (the external rst-8 path resets A'; §8s)", ap, hkA)
		}
		// (2) the write path diverges before the SD write core / CMD24 (the §8r blocker
		// reproduced via the hook path). When the b2q fix lands this assertion flips —
		// update it then; it is the open-blocker characterization, not a target.
		if reached[mcWriteCore] || sawWrite {
			t.Errorf("A'=%d: HWSAD reached the SD write core (writeCore=%v CMD24=%v) — the §8r blocker no longer reproduces; update this guard and §8s for the fix",
				ap, reached[mcWriteCore], sawWrite)
		} else {
			t.Logf("A'=%d: §8s confirmed — reaches device-select, hk.a=0, diverges to the &9A8B abort, no SD write", ap)
		}
	}
}
