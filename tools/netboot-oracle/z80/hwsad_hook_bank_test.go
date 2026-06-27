package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// TestHWSADHookBankContract is the i280b-b2i (§8o) fix-gating measurement: it runs
// a `rst 8 / defb 149` (HWSAD) through the FULL real ROM PTDOS dispatch — the
// genuine &0008 → ERROR2(&37CE) → PTDOS(&380B) → &4200 → dispatcher(&8319) chain,
// against Colin's real ROM v3.0 + B-DOS 1.5t — with the caller's MAIN and ALTERNATE
// register banks set to DISTINGUISHABLE sentinels, then reads which bank the
// dispatcher saved into hk.a (&81D9) / hk.hl (&81DA) / hk.de (&81DC).
//
// The result (the contract): the dispatcher saves the caller's MAIN bank. The
// ROM/B-DOS swap chain is exactly two EXX (ROM &0009 + dispatcher &8321) and two
// EX AF,AF' (ROM &37D4 + dispatcher &8322) — even counts, so HL/DE/A net back to
// main at the saves. This REFUTES §8n's hypothesis that hk.hl might come from a
// bank our seam never sets: our seam's `ld hl,BD_WRITE_BUF` (main HL = &BE42)
// DOES reach hk.hl — sentinelB proves it (main HL=&BE42 → hk.hl=&BE42). It also
// refutes the §8h/#730 inference that hk.a comes from A'; hk.a = main A (§8c's
// "main A=2 had no effect" is explained by the hang being downstream, §8j/§8n,
// not by hk.a's bank). So the prelude addressing is correct (re-confirming §8l's
// no-displacement conclusion via an independent route) and the data-phase hang is
// downstream in the SD write core, not in the hk.hl page-pointer handling.
//
// Faithful arming (matches the serve runtime, not the editor-idle snapshot):
//   - LMPR=&1F (ROM0 in section A so rst8→ROM &0008; system page in section B so
//     DOSFLG/DOSCNT read true), HMPR=1 (serve's own page in section C; §8l).
//   - DOSCNT (&5BC3) := 0. The ROM recursion guard at &37E8 (`ld a,(DOSCNT); rrca;
//     jr c,NORMERR`) routes to NORMERR — NO hook dispatch — when bit0 is set. The
//     editor-idle snapshot has DOSCNT=1; an EXTERNAL caller invoking a DOS hook
//     (BASIC, or our serve) runs with DOSCNT=0. Hardware corroborates: HWSAD_PRE
//     fires on the real serve (§8g/§8l), which only happens if the rst8 reaches
//     the handler — i.e. DOSCNT=0 at the serve's rst8.
//   - DOSFLG (&5BC2) is already &1D (DOS-resident) from boot.
func TestHWSADHookBankContract(t *testing.T) {
	type cfg struct {
		name           string
		mainHL, mainDE uint16
		altHL, altDE   uint16
		mainA, altA    uint8
	}
	// Two configs with disjoint sentinels: if both report the MAIN value, the
	// dispatcher provably tracks the main bank (not a fixed/coincidental value).
	// sentinelB's main HL is our actual seam pointer &BE42.
	run := func(t *testing.T, c cfg) {
		rom, eeprom := loadRealCaptures(t)
		mac, _ := newRealBootMachine(t, rom, eeprom)
		res, err := mac.RunBootFrom(0x0000, z80h.Entry{
			StepCap: realBootStepCap, StopPC: addrEditorIdle, StopPCSkip: 16,
		})
		if err != nil {
			t.Fatalf("[%s] real boot faulted: %v", c.name, err)
		}
		if !res.ReachedStop {
			t.Fatalf("[%s] boot did not reach editor idle (PC=&%04X) — B-DOS not resident", c.name, res.PC)
		}

		mac.Pager().LMPR = 0x1F
		mac.Pager().HMPR = 0x01
		if got := mac.Read(0x5BC2, 1)[0]; got != 0x1D {
			t.Fatalf("[%s] DOSFLG &5BC2 = &%02X, want &1D (DOS-resident)", c.name, got)
		}
		mac.Write(0x5BC3, []byte{0x00}) // DOSCNT := 0 (external-caller / not-in-DOS state)

		// Stub in section C (page 1 under HMPR=1 — no overlap with B-DOS at page 29).
		// Set main HL/DE/A and alternate HL'/DE'/A' to the config's sentinels, then
		// `rst 8 / defb 149`. The trailing `di; halt` is the post-hook landing (the
		// hook itself hangs downstream; we trap at the saves long before that).
		const stub = 0x9000
		prog := []byte{
			0x21, byte(c.mainHL), byte(c.mainHL >> 8), // ld hl,mainHL
			0x11, byte(c.mainDE), byte(c.mainDE >> 8), // ld de,mainDE
			0xD9,                                    // exx
			0x21, byte(c.altHL), byte(c.altHL >> 8), // ld hl,altHL
			0x11, byte(c.altDE), byte(c.altDE >> 8), // ld de,altDE
			0xD9,         // exx (back to the main bank)
			0x3E, c.altA, // ld a,altA
			0x08,          // ex af,af'   (A' = altA)
			0x3E, c.mainA, // ld a,mainA
			0xCF, 149, // rst 8 ; defb 149 (HWSAD)
			0xF3, 0x76, // di ; halt
		}
		mac.Write(stub, prog)

		// PTDOS pages B-DOS into SECTION B, so the dispatcher &8319 and its hk saves
		// run at their section-B aliases &4319/&4323-&4331, and the hk vars &81xx are
		// read at &41xx (section B = B-DOS page here). Trap at &4331 (alias of &8331),
		// after hk.a/hk.hl/hk.de/hk.bc are stored, before the swap-back.
		const aliasDispatch = 0x4319
		const bankAt = 0x4331
		var hkA uint8
		var hkHL, hkDE uint16
		var sawDispatch, saved bool
		if _, err := mac.RunBootFrom(stub, z80h.Entry{
			StepCap: 3_000_000,
			Trace: func(pc uint16) {
				switch pc {
				case aliasDispatch:
					sawDispatch = true
				case bankAt:
					if !saved {
						saved = true
						hkA = mac.Read(0x41D9, 1)[0]
						hkHL = uint16(mac.Read(0x41DA, 1)[0]) | uint16(mac.Read(0x41DB, 1)[0])<<8
						hkDE = uint16(mac.Read(0x41DC, 1)[0]) | uint16(mac.Read(0x41DD, 1)[0])<<8
					}
				}
			},
		}); err != nil {
			t.Fatalf("[%s] dispatch run faulted: %v", c.name, err)
		}

		// The measurement is only valid if the genuine dispatch ran to the saves.
		if !sawDispatch || !saved {
			t.Fatalf("[%s] rst8 did not reach the B-DOS hook dispatcher saves (sawDispatch=%v saved=%v) — arming is wrong, measurement void",
				c.name, sawDispatch, saved)
		}

		if hkA != c.mainA {
			t.Errorf("[%s] hk.a=&%02X — want main A=&%02X (got alt A'=&%02X => dispatcher reads the ALTERNATE bank)", c.name, hkA, c.mainA, c.altA)
		}
		if hkHL != c.mainHL {
			t.Errorf("[%s] hk.hl=&%04X — want main HL=&%04X (got alt HL'=&%04X => §8n bank hypothesis, our seam ld hl would NOT reach hk.hl)", c.name, hkHL, c.mainHL, c.altHL)
		}
		if hkDE != c.mainDE {
			t.Errorf("[%s] hk.de=&%04X — want main DE=&%04X (got alt DE'=&%04X)", c.name, hkDE, c.mainDE, c.altDE)
		}
		t.Logf("[%s] dispatcher saved MAIN bank: hk.a=&%02X hk.hl=&%04X hk.de=&%04X", c.name, hkA, hkHL, hkDE)
	}

	for _, c := range []cfg{
		{"sentinelA", 0x9400, 0x2222, 0x3333, 0x4444, 0x00, 0xAA},
		{"sentinelB", 0xBE42, 0x1357, 0x8642, 0x9753, 0x07, 0x55}, // main HL = the real seam BD_WRITE_BUF
	} {
		c := c
		t.Run(c.name, func(t *testing.T) { run(t, c) })
	}
}
