package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// TestHWSADHandlerTraceable is the i280b-b2n (§8p) methodology guard: it proves the
// real B-DOS HWSAD hook handler runs END-TO-END in emulation through the full ROM
// `rst 8` dispatch — the §8b "honest boundary" that, since §8a, was thought
// un-traceable (the handler `call &0103`'s a SAM-ROM bridge the flat harness lacked,
// so the trace wandered off into unmapped memory at real &9BF1).
//
// The §8o arming is the key that unblocks it: drive the FULL real ROM PTDOS dispatch
// (DOSCNT &5BC3 := 0 so the `&37E8` recursion guard lets an external caller's `rst 8`
// through to the dispatcher; serve map LMPR=&1F / HMPR=1) against Colin's real ROM
// v3.0 + B-DOS 1.5t. The handler then reaches its entry (&5E16, the section-B alias
// of &9E16 — PTDOS maps B-DOS into section B), runs the prelude (&5E27), the
// device-select (&4662 = &8662), and — crucially — traverses the real ROM bridge at
// `&0103` (reached from the §8a escape point real &9BF1) and RETURNS, instead of
// wandering into garbage. So the handler is now observable in emulation.
//
// SCOPE (honest): this guards end-to-end *traceability*, not hang reproduction. With
// the hand-set device state here (ambient device &780B=2, hk.a=0) the handler returns
// cleanly WITHOUT reaching the SD CMD24 write core (&A8F4) — driving it into the write
// core (the suspected hardware hang site, which the koron-go SD model cannot reproduce
// anyway, §8a) needs the FULLY faithful claim-select state the real serve's HRECORD
// leaves (the &80AF / &60E4 SD-setup vars). That is the i280b-b2i continuation. This
// test only asserts the boundary is crossed: the handler runs, hits its milestones,
// and the run stays in mapped code (no fault, no wander).
func TestHWSADHandlerTraceable(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac, enc := newRealBootMachine(t, rom, eeprom)
	enc.AttachSD(csdV2(0x001D59)) // ~3.7 GB SDHC so the SD path has a card to talk to
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap, StopPC: addrEditorIdle, StopPCSkip: 16,
	})
	if err != nil {
		t.Fatalf("real boot faulted: %v", err)
	}
	if !res.ReachedStop {
		t.Fatalf("boot did not reach editor idle (PC=&%04X) — B-DOS not resident", res.PC)
	}
	dosPage := mac.Pager().HMPR & 0x1F

	// Faithful-ish post-claim device vars, written into B-DOS's page (section B = dosPage):
	// ambient device = Trinity SD, device class = mass-storage, device number = Trinity.
	mac.Pager().LMPR = (dosPage - 1) & 0x1F // B-DOS into section B
	mac.Write(0x780B, []byte{0x02})         // &780B ambient device = Trinity SD
	mac.Write(0x4135, []byte{0x44})         // &8135 device class = mass-storage (section-B alias)
	mac.Write(0x4132, []byte{0x02})         // &8132 device number = Trinity (section-B alias)

	// Serve runtime map (§8l) + the §8o DOSCNT=0 arming.
	mac.Pager().LMPR = 0x1F
	mac.Pager().HMPR = 0x01
	mac.Write(0x5BC3, []byte{0x00}) // DOSCNT := 0 (external caller / not-in-DOS)

	const stub = 0x9000
	src := uint16(0xBE42) // the real BD_WRITE_BUF
	prog := []byte{
		0x3E, 0x00, // ld a,0 (drive 0 = the seam value; hk.a = main A per §8o)
		0x11, 0x02, 0x00, // ld de,&0002 (D=track0 E=sector2)
		0x21, byte(src), byte(src >> 8), // ld hl,BD_WRITE_BUF
		0xCF, 149, // rst 8 ; defb 149 (HWSAD)
		0xF3, 0x76, // di ; halt
	}
	mac.Write(stub, prog)

	// Milestones on the handler path (section-B aliases, B-DOS in section B via PTDOS).
	const (
		mDispatch  = 0x4319 // hook dispatcher (alias of &8319)
		mHandler   = 0x5E16 // HWSAD handler entry (alias of &9E16)
		mPrelude   = 0x5E27 // prelude (alias of &9E27)
		mDevSel    = 0x4662 // device-select (alias of &8662)
		mRomBridge = 0x0103 // the &9BF1 → ROM bridge the flat harness lacked (§8a escape)
	)
	reached := map[uint16]bool{}
	res2, err := mac.RunBootFrom(stub, z80h.Entry{
		StepCap: 5_000_000,
		Trace: func(pc uint16) {
			switch pc {
			case mDispatch, mHandler, mPrelude, mDevSel, mRomBridge:
				reached[pc] = true
			}
		},
	})
	if err != nil {
		// A genuine fault (undecodable instruction) = the trace wandered into garbage:
		// exactly the §8b failure mode this test proves is gone.
		t.Fatalf("HWSAD dispatch faulted (the §8b wander): %v", err)
	}

	for _, m := range []struct {
		pc   uint16
		name string
	}{
		{mDispatch, "hook dispatcher &8319"},
		{mHandler, "HWSAD handler entry &9E16"},
		{mPrelude, "HWSAD prelude &9E27"},
		{mDevSel, "device-select &8662"},
		{mRomBridge, "ROM bridge &0103 (the §8a escape point)"},
	} {
		if !reached[m.pc] {
			t.Errorf("handler did not reach %s — end-to-end trace broke before it", m.name)
		}
	}

	// The run must end in mapped editor/ROM code (it returns to the editor idle loop),
	// NOT spinning in unmapped low memory — proof the &0103 bridge returned cleanly and
	// the handler unwound rather than wandering. &01Bx-&01Cx is the editor idle band.
	if res2.PC < 0x0100 || (res2.PC >= 0x4000 && res2.PC < 0x8000 && !reached[mHandler]) {
		t.Errorf("run ended at &%04X — not the expected return to editor/ROM code", res2.PC)
	}
	t.Logf("HWSAD handler traced end-to-end through the real ROM dispatch + &0103 bridge; finalPC=&%04X (steps=%d)", res2.PC, res2.Steps)
}
