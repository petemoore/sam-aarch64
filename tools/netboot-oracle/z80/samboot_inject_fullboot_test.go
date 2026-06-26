// samboot_inject_fullboot_test.go — i260 feasibility probe: how far does the SPLICED
// faithful inject run in the Go core AFTER &805F returns?
//
// The i229 inject design assumed the post-&805F code (CLSLOWER, the POMSG banner, the
// READKEY WTFK, the teardown) was "hardware-only by position" because &805F did not
// return in the Go core. The i257 EI;HALT-resume fix CHANGED that: Colin's fork now
// boots through B-DOS init to BASIC in the Go core (TestRealBootBootsToBASIC). So the
// spliced inject's post-&805F code is now REACHED in emulation — this probe measures
// exactly how far it runs, which is the precondition for the i260 no-auto-boot RAM-diff
// regression (boot the spliced inject to the editor sync PC, diff vs Colin's fork).
//
// It is a DIAGNOSTIC trace (t.Logf), not a pass/fail gate, while we establish what the
// core can run; the assertions it does make are conservative (the milestones already
// proven elsewhere). The proprietary capture is required (loadRealCaptures FAILs hard
// when absent unless SKIP_PRIVATE_TESTS=true — i253, no silent skip).
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// Milestones along the faithful no-auto-boot inject path (+ read_chunk, the
// bootblock load-loop helper, as the early sanity marker).
const (
	mReadChunk  = 0x40BD // read_chunk (12 B-DOS chunk loads — the bootblock load loop)
	mSplice     = 0x409E // CALL inject (the splice site)
	mInject     = 0x415E // inject entry (stripes build)
	mExecDOS    = 0x805F // B-DOS init
	mCLSLOWER   = 0x06B5 // CLSLOWER (lower-screen select, then teardown)
	mPOMSG      = 0x3DB4 // POMSG (banner print)
	mDecision   = 0x41A6 // inject_decision (config read done)
	mWTFK       = 0x41AB // inject_wtfk (READKEY any-key loop)
	mREADKEY    = 0x1CB1 // READKEY
	mColinTail  = 0x40A1 // restore: — Colin's verbatim teardown tail (no-auto-boot RET target)
	mBASICexit  = 0x102F // JP &102F — Colin's tail exit to BASIC
	mEditorIdle = 0x01CB // editor idle loop (BASIC reached)
)

var injectOrder = []injectMilestone{
	{"readChunk(&40BD)", mReadChunk}, {"splice(&409E)", mSplice}, {"inject(&415E)", mInject},
	{"execDOS(&805F)", mExecDOS}, {"CLSLOWER(&06B5)", mCLSLOWER}, {"POMSG(&3DB4)", mPOMSG},
	{"decision(&41A6)", mDecision}, {"WTFK(&41AB)", mWTFK}, {"READKEY(&1CB1)", mREADKEY},
	{"colinTail(&40A1)", mColinTail}, {"BASICexit(&102F)", mBASICexit}, {"editorIdle(&01CB)", mEditorIdle},
}

func TestSplicedInjectFullBootTrace(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	inject := loadInjectBlob(t)
	dev := splicedDeviceEEPROM(t, eeprom, inject)
	traceSplicedBoot(t, rom, dev, 0)
}

// TestWhichRegisterInitNeeds resolves the one open conflict (2026-06-26): does
// B-DOS init (&805F) need A=0 specifically, or BC/DE/HL? Init's code is
// `LD HL,&5ADA / LD (HL),A / INC L / JR NZ` — a clear loop that writes A to
// &5ADA..&5AFF, so it needs A=0 and ignores BC/DE/HL (HL is overwritten). We
// PROVE it by perturbation: restore ONLY A around the stripes (expect clean boot
// to &40A1), vs restore ALL-BUT-A (expect a loop because A is garbage -> init
// zero-fills with garbage). Splice = JP inject at &409E.
func TestWhichRegisterInitNeeds(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	stripes := loadInjectBlob(t)[0:28]

	cases := []struct {
		name       string
		pre, post  []byte // register save before stripes / restore after
		wantReach  bool   // reach Colin's tail &40A1 cleanly?
	}{
		{"restore-only-A", []byte{0xF5}, []byte{0xF1}, true},                                 // push af / pop af
		{"restore-all-but-A", []byte{0xC5, 0xD5, 0xE5}, []byte{0xE1, 0xD1, 0xC1}, false},     // push bc/de/hl / pop hl/de/bc (A left clobbered)
	}
	for _, c := range cases {
		var inj []byte
		inj = append(inj, c.pre...)
		inj = append(inj, stripes...)
		inj = append(inj, c.post...)
		inj = append(inj, 0xCD, 0x5F, 0x80) // call &805F
		inj = append(inj, 0xC3, 0xA1, 0x40) // jp &40A1

		dev := deviceLinearEEPROM(eeprom)
		const chunk1Dev = 0x2000
		dev[chunk1Dev+0x9E] = 0xC3 // JP inject (&415E)
		dev[chunk1Dev+0x9F] = 0x5E
		dev[chunk1Dev+0xA0] = 0x41
		copy(dev[chunk1Dev+0x15E:], inj)

		mac := z80h.New()
		if err := mac.LoadROMImage(rom); err != nil {
			t.Fatalf("%s: load ROM: %v", c.name, err)
		}
		mac.Pager().LMPR = bootLMPR
		mac.Pager().HMPR = bootHMPR
		enc := z80h.NewENC28J60()
		enc.LoadEEPROMImage(dev)
		mac.AttachIO(enc)

		reachedTail := false
		injectEntries := 0
		mac.InjectKeys([]byte{'x'})
		_, err := mac.RunBootFrom(0x0000, z80h.Entry{
			StepCap: 6_000_000, FrameIntPeriod: 20000, StopPC: 0x40A1,
			Trace: func(pc uint16) {
				if pc == 0x415E {
					injectEntries++
				}
				if pc == 0x40A1 {
					reachedTail = true
				}
			},
		})
		if err != nil {
			t.Fatalf("%s: run: %v", c.name, err)
		}
		t.Logf("%-18s reached&40A1=%v injectEntries=%d (want reach=%v)", c.name, reachedTail, injectEntries, c.wantReach)
		if reachedTail != c.wantReach {
			t.Errorf("%s: reached &40A1 = %v, want %v — A-needed hypothesis violated", c.name, reachedTail, c.wantReach)
		}
	}
}

// TestRegisterPreservedWrapper is the proof of the register-preservation redesign
// (Pete's structure): splice &409E to `JP inject`; inject = push af/bc/de/hl ;
// <build stripes> ; pop hl/de/bc/af ; call &805F ; jp &40A1. The push/pop hands
// B-DOS init the EXACT register state Colin's bootblock established at &409E, so it
// runs identically and returns to &40A1 -> BASIC. We hand-assemble the minimal inject
// (real stripe bytes wrapped in push/pop) so the proof is independent of the asm
// build, then trace that it boots clean to &40A1 with NO loop.
func TestRegisterPreservedWrapper(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	stripeBytes := loadInjectBlob(t)[0:28] // the stripe routine (everything before CALL &805F)

	// Hand-assemble inject: push af,bc,de,hl ; <stripes> ; pop hl,de,bc,af ; call &805F ; jp &40A1.
	var inj []byte
	inj = append(inj, 0xF5, 0xC5, 0xD5, 0xE5) // push af / bc / de / hl
	inj = append(inj, stripeBytes...)         // build the rainbow stripes (clobbers af/bc/de/hl)
	inj = append(inj, 0xE1, 0xD1, 0xC1, 0xF1) // pop hl / de / bc / af  (restore Colin's pre-&805F state)
	inj = append(inj, 0xCD, 0x5F, 0x80)       // call &805F  (B-DOS init)
	inj = append(inj, 0xC3, 0xA1, 0x40)       // jp &40A1    (Colin's tail -> BASIC)

	dev := deviceLinearEEPROM(eeprom)
	const (
		chunk1Dev    = 0x2000
		spliceDevOff = chunk1Dev + 0x9E  // &409E
		injectDevOff = chunk1Dev + 0x15E // &415E
	)
	if dev[spliceDevOff] != 0xCD || dev[spliceDevOff+1] != 0x5F || dev[spliceDevOff+2] != 0x80 {
		t.Fatalf("splice site &409E = % X, want CD 5F 80", dev[spliceDevOff:spliceDevOff+3])
	}
	dev[spliceDevOff] = 0xC3   // JP inject (C3 5E 41)
	dev[spliceDevOff+1] = 0x5E // &415E lo
	dev[spliceDevOff+2] = 0x41 // &415E hi
	copy(dev[injectDevOff:], inj)

	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load real ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(dev)
	mac.AttachIO(enc)

	const (
		mInjectE = 0x415E
		mRestore = 0x40A1
		mNSPPC   = 0x40B1
		mBASICjp = 0x102F
	)
	mark := []injectMilestone{
		{"inject(&415E)", mInjectE}, {"DOSinit(&805F)", mExecDOS}, {"restore:(&40A1)", mRestore},
		{"NSPPCpoke(&40B1)", mNSPPC}, {"JP&102F", mBASICjp}, {"editorIdle(&01CB)", mEditorIdle},
	}
	firstStep := map[uint16]int{}
	count := map[uint16]int{}
	var trail []string
	step := 0
	mac.InjectKeys([]byte{'x'})
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap:        8_000_000,
		FrameIntPeriod: 20000,
		StopPC:         mBASICjp, // stop at Colin's JP &102F (BASIC reached, tail ran)
		Trace: func(pc uint16) {
			step++
			if isMilestone(mark, pc) {
				if _, seen := firstStep[pc]; !seen {
					firstStep[pc] = step
					for _, m := range mark {
						if m.pc == pc {
							trail = append(trail, m.name)
						}
					}
				}
				count[pc]++
			}
		},
	})
	if err != nil {
		t.Fatalf("register-preserved wrapper run faulted: %v", err)
	}
	t.Logf("register-preserved wrapper — milestone trail: %v", trail)
	for _, m := range mark {
		if s, ok := firstStep[m.pc]; ok {
			t.Logf("  %-18s first@step %-9d hits=%d", m.name, s, count[m.pc])
		} else {
			t.Logf("  %-18s NOT REACHED", m.name)
		}
	}
	t.Logf("final: PC=&%04X reachedStop=%v steps=%d", res.PC, res.ReachedStop, res.Steps)
	if firstStep[mRestore] == 0 || count[mInjectE] != 1 {
		t.Errorf("FIX NOT PROVEN: &40A1 reached=%v, inject entered %d times (want 1, no loop)", firstStep[mRestore] != 0, count[mInjectE])
	} else {
		t.Logf("FIX PROVEN: stripes built, regs restored, &805F ran once, reached &40A1 -> BASIC, no loop")
	}
}

// TestJpVsCall805F is the hypothesis test: the inject deviates from Colin by calling
// &805F one frame deeper (CALL inject -> inject CALL &805F), and B-DOS init's return
// lands wrong (&4D02 wander + reboot loop). If we instead JP &805F (the inject runs
// at the SAME stack depth Colin used — CALL inject at &409E already pushed &40A1), the
// boot should reach Colin's tail &40A1 -> JP &102F -> BASIC cleanly, no wander. We test
// this by patching the one opcode byte CD->C3 at &417A (CALL &805F -> JP &805F) in the
// loaded image — no asm rebuild — and tracing the bootblock tail.
func TestJpVsCall805F(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	inject := loadInjectBlob(t)
	dev := splicedDeviceEEPROM(t, eeprom, inject)

	// Keep the stripe build, but turn its CALL &805F (&417A = device &217A) into a JP
	// (no extra frame). Run with the frame interrupt OFF: if stripes+jp is CLEAN with
	// no frame int but loops with it, the corruption is the armed-rainbow × interrupt
	// interaction (emulation line-int fidelity gap); if it still loops, the stripe
	// memory writes themselves clash with B-DOS init.
	const callSiteDevOff = 0x217A
	if dev[callSiteDevOff] != 0xCD || dev[callSiteDevOff+1] != 0x5F || dev[callSiteDevOff+2] != 0x80 {
		t.Fatalf("device &%04X = % X, want CD 5F 80 (CALL &805F)", callSiteDevOff, dev[callSiteDevOff:callSiteDevOff+3])
	}
	dev[callSiteDevOff] = 0xC3 // CALL -> JP, keep stripes

	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load real ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(dev)
	mac.AttachIO(enc)

	const (
		mRestore = 0x40A1
		mNSPPC   = 0x40B1
		mBASICjp = 0x102F
	)
	mark := []injectMilestone{
		{"inject(&415E)", mInject}, {"DOSinit(&805F)", mExecDOS}, {"restore:(&40A1)", mRestore},
		{"NSPPCpoke(&40B1)", mNSPPC}, {"JP&102F", mBASICjp}, {"editorIdle(&01CB)", mEditorIdle},
	}
	firstStep := map[uint16]int{}
	count := map[uint16]int{}
	var trail []string
	step := 0
	mac.InjectKeys([]byte{'x'})
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap:        8_000_000,
		FrameIntPeriod: 0, // ISOLATION: stripes armed, but NO frame interrupt fires
		StopPC:         0x0514, // real BASIC sync point
		Trace: func(pc uint16) {
			step++
			if isMilestone(mark, pc) {
				if _, seen := firstStep[pc]; !seen {
					firstStep[pc] = step
					for _, m := range mark {
						if m.pc == pc {
							trail = append(trail, m.name)
						}
					}
				}
				count[pc]++
			}
		},
	})
	if err != nil {
		t.Fatalf("jp-&805F run faulted: %v", err)
	}
	t.Logf("JP &805F (no extra frame) — milestone trail: %v", trail)
	for _, m := range mark {
		if s, ok := firstStep[m.pc]; ok {
			t.Logf("  %-18s first@step %-9d hits=%d", m.name, s, count[m.pc])
		} else {
			t.Logf("  %-18s NOT REACHED", m.name)
		}
	}
	t.Logf("final: PC=&%04X halted=%v reachedStop=%v steps=%d", res.PC, res.Halted, res.ReachedStop, res.Steps)
	if firstStep[mRestore] == 0 {
		t.Error("HYPOTHESIS NOT CONFIRMED: &40A1 (Colin's tail) still not reached even with JP &805F")
	} else {
		t.Logf("HYPOTHESIS CONFIRMED: JP &805F reaches Colin's tail &40A1 — the extra CALL frame was the deviation")
	}
}

// TestColinTailReachability is the decisive measurement: in the UNSPLICED Colin
// boot, does CALL &805F (B-DOS init) RETURN to the bootblock tail (restore: &40A1,
// and the NSPPC/TVDATA poke sites &40B1/&40B6 that i258 proved execute)? Or does
// B-DOS init transfer to BASIC and never return? This resolves the contradiction
// between i258 (pokes execute → returns) and the disasm reading (init.svar→mode→
// JP &015A → editor, never returns). Measure, don't argue.
func TestColinTailReachability(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load real ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom)) // UNSPLICED — real Colin bootblock
	mac.AttachIO(enc)

	const (
		mExecCall  = 0x409E // Colin's CALL &805F
		mRestore   = 0x40A1 // restore: — the bootblock teardown tail (return target if &805F RETs)
		mNSPPC     = 0x40B1 // LD (&5C44),A — i258's NSPPC poke site
		mTVDATA    = 0x40B6 // LD (&5BBE),A — i258's TVDATA poke site
		mBASICjp   = 0x102F // Colin's JP &102F to BASIC
		mKeyConsume = 0x0514 // the editor key-consume sync PC (real BASIC, after 'x' injected)
	)
	mark := []injectMilestone{
		{"CALL&805F(&409E)", mExecCall}, {"DOSinit(&805F)", mExecDOS}, {"restore:(&40A1)", mRestore},
		{"NSPPCpoke(&40B1)", mNSPPC}, {"TVDATApoke(&40B6)", mTVDATA}, {"JP&102F", mBASICjp},
		{"editorIdle(&01CB)", mEditorIdle}, {"keyConsume(&0514)", mKeyConsume},
	}
	mac.InjectKeys([]byte{'x'}) // LASTK injection (Pete: the harness bypasses key-scan)
	firstStep := map[uint16]int{}
	count := map[uint16]int{}
	var trail []string
	step := 0
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap:        8_000_000,
		FrameIntPeriod: 0, // ISOLATION: stripes armed, but NO frame interrupt fires // faithful: run the real 50 Hz frame-int handler (like i258)
		StopPC:         mKeyConsume, // run to REAL BASIC (i258's sync point), not a transient &01CB
		StopPCSkip:     0,
		Trace: func(pc uint16) {
			step++
			if isMilestone(mark, pc) {
				if _, seen := firstStep[pc]; !seen {
					firstStep[pc] = step
					for _, m := range mark {
						if m.pc == pc {
							trail = append(trail, m.name)
						}
					}
				}
				count[pc]++
			}
		},
	})
	if err != nil {
		t.Fatalf("unspliced Colin boot faulted: %v", err)
	}
	t.Logf("UNSPLICED Colin boot (FrameIntPeriod=20000, run to &0514) — bootblock-tail reachability:")
	t.Logf("  milestone trail: %v", trail)
	for _, m := range mark {
		if s, ok := firstStep[m.pc]; ok {
			t.Logf("  %-18s first@step %-9d hits=%d", m.name, s, count[m.pc])
		} else {
			t.Logf("  %-18s NOT REACHED", m.name)
		}
	}
	t.Logf("  final PC=&%04X reachedStop=%v steps=%d", res.PC, res.ReachedStop, res.Steps)
	// The decisive assertion: if the NSPPC poke site (&40B1, AFTER CALL &805F) is
	// reached, then &805F RETURNED to the bootblock tail — and i258 is right.
	if firstStep[mNSPPC] != 0 && firstStep[mExecDOS] != 0 && firstStep[mNSPPC] < firstStep[mExecDOS] {
		t.Errorf("NSPPC poke reached BEFORE &805F — unexpected ordering")
	}
}

func traceSplicedBoot(t *testing.T, rom, dev []byte, frameInt uint64) {
	t.Helper()
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load real ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(dev)
	mac.AttachIO(enc)

	count := map[uint16]int{}
	firstStep := map[uint16]int{}
	var trail []string
	step := 0
	keyHeld := false

	// Capture distinct PCs in the bootblock/inject band (&4000..&7FFF) AFTER the first
	// &805F, to see where B-DOS init's return lands: the inject's post-&805F banner
	// (~&4184), the bootblock tail (&40A1), or a wander.
	dosSeen := false
	var postLow []uint16
	lastLow := uint16(0xFFFF)

	// Run to a high step cap with NO early &01CB stop (B-DOS init passes through the
	// editor region transiently before it RETURNS to the inject's post-&805F code).
	// Inject a key into LASTK so the inject's WTFK READKEY loop can fall through.
	mac.InjectKeys([]byte{' '})
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap:        12_000_000,
		FrameIntPeriod: frameInt,
		Trace: func(pc uint16) {
			step++
			if isMilestone(injectOrder, pc) {
				if _, seen := firstStep[pc]; !seen {
					firstStep[pc] = step
					for _, m := range injectOrder {
						if m.pc == pc {
							trail = append(trail, m.name)
						}
					}
				}
				count[pc]++
			}
			if pc == mExecDOS {
				dosSeen = true
			}
			if dosSeen && pc >= 0x4000 && pc < 0x8000 && pc != lastLow && len(postLow) < 60 {
				postLow = append(postLow, pc)
				lastLow = pc
			}
			if pc == mWTFK && !keyHeld {
				mac.InjectKeys([]byte{' '}) // top up LASTK in case the first key was consumed earlier
				keyHeld = true
			}
		},
	})
	if err != nil {
		t.Fatalf("spliced full-boot run faulted (frameInt=%d): %v", frameInt, err)
	}

	t.Logf("=== SPLICED inject, frameInt=%d (run to cap, no early stop) ===", frameInt)
	t.Logf("milestone trail (first-touch order): %v", trail)
	for _, m := range injectOrder {
		if s, ok := firstStep[m.pc]; ok {
			t.Logf("  %-18s first@step %-9d hits=%d", m.name, s, count[m.pc])
		} else {
			t.Logf("  %-18s NOT REACHED", m.name)
		}
	}
	t.Logf("final: PC=&%04X halted=%v reachedStop=%v steps=%d LMPR=&%02X",
		res.PC, res.Halted, res.ReachedStop, res.Steps, mac.Pager().LMPR)
	var low []string
	for _, pc := range postLow {
		low = append(low, fmtHex(pc))
	}
	t.Logf("post-&805F distinct PCs in &4000..&7FFF (where init returns): %v", low)
}

func fmtHex(v uint16) string {
	const d = "0123456789ABCDEF"
	return "&" + string([]byte{d[v>>12&0xF], d[v>>8&0xF], d[v>>4&0xF], d[v&0xF]})
}

type injectMilestone struct {
	name string
	pc   uint16
}

func isMilestone(order []injectMilestone, pc uint16) bool {
	for _, m := range order {
		if m.pc == pc {
			return true
		}
	}
	return false
}
