// rom_print_scroll_test.go — the i319a screen-wedge class, pinned in the FAITHFUL
// emulator (Colin's real ROM; private captures, skips under SKIP_PRIVATE_TESTS).
//
// THE CLASS (found on real hardware 2026-07-02): a trinload-pushed tool printing
// via RST &10 wedges the whole SAM when a CR lands with the print position at the
// screen bottom — the stock ROM enters its scroll key-wait prompt, and an
// unattended SAM has nobody to press a key. sd_push's i318 status lines crossed
// the bottom of the screen state trinload left and the tool never came up (no
// discovery reply, power-cycle to recover). The flat harness cannot see this
// (it stubs RST &10), so the ROM behaviour AND the defence are pinned here:
//
//   * TestROMPrintCRWedgesAtScreenBottom — the NEGATIVE control: enough CRs from
//     the WTKY2 state drive the real ROM into a non-returning key-wait. If this
//     ever stops wedging, the emulator's ROM/print model changed — re-examine the
//     defence before trusting it.
//   * TestROMPrintCRBudgetAfterCLS — the DEFENCE: CLSLOWER (&06B5) first, then a
//     dozen CRs, completes cleanly. This is why every pushable tool that prints
//     calls &06B5 before its first RST &10 (sd_push, list_records).
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// bootFaithfulToWTKY2 boots Colin's real ROM + B-DOS to the WTKY2 editor idle —
// the screen/channel state a trinload-era print runs against. Mirrors
// bootToEditorIdleSD without the SD seeding (no SD activity in these tests).
func bootFaithfulToWTKY2(t *testing.T) (*z80h.Machine, *z80h.ENC28J60) {
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
	enc.AttachSD(csdV2(0x001D59))
	var lastPC uint16
	lg := &[]capEv{}
	mac.AttachIO(&capIO{inner: enc, lastPC: &lastPC, log: lg})
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: 80_000_000, StopPC: wtky2Idle, StopPCSkip: 0, FrameIntPeriod: 60000,
		Trace: func(pc uint16) { lastPC = pc },
	})
	if err != nil || !res.ReachedStop {
		t.Fatalf("boot did not reach WTKY2: %v reached=%v PC=&%04X", err, res.ReachedStop, res.PC)
	}
	return mac, enc
}

// crStub writes a DI'd print stub at 0x9000 (page 1): optionally CALL &06B5
// (CLSLOWER), then print `n` CRs via RST &10, then di;halt. Returns the stub addr.
func crStub(mac *z80h.Machine, cls bool, n byte) uint16 {
	mac.Pager().HMPR = 1
	stub := uint16(0x9000)
	code := []byte{0xF3} // di
	if cls {
		code = append(code, 0xCD, 0xB5, 0x06) // call &06B5
	}
	code = append(code,
		0x06, n, // ld b,n
		0x3E, 0x0D, // loop: ld a,13
		0xD7,       // rst &10
		0x10, 0xFB, // djnz loop
		0xF3, 0x76, // di; halt
	)
	mac.Write(stub, code)
	return stub
}

// TestROMPrintCRWedgesAtScreenBottom — the negative control: 60 raw CRs (no CLS)
// from the WTKY2 state drive the real ROM into its scroll key-wait; the run hits
// the step cap without ever reaching the halt. This is the hardware wedge class.
func TestROMPrintCRWedgesAtScreenBottom(t *testing.T) {
	mac, _ := bootFaithfulToWTKY2(t)
	stub := crStub(mac, false, 60)
	res, err := mac.RunBootFrom(stub, z80h.Entry{StepCap: 30_000_000})
	if err != nil {
		t.Fatalf("stub faulted: %v (PC=&%04X)", err, res.PC)
	}
	if res.Halted {
		t.Fatalf("60 raw CRs completed WITHOUT the ROM scroll key-wait (PC=&%04X) — the emulated ROM print path changed; re-verify the i319a CLS defence still matters", res.PC)
	}
	t.Logf("pinned: raw CRs wedge in the ROM key-wait (PC=&%04X after %d steps)", res.PC, res.Steps)
}

// TestROMPrintCRBudgetAfterCLS — the defence: CLSLOWER first, then 12 CRs (more
// than any pushable tool prints per run), completes cleanly with no key-wait.
func TestROMPrintCRBudgetAfterCLS(t *testing.T) {
	mac, _ := bootFaithfulToWTKY2(t)
	stub := crStub(mac, true, 12)
	res, err := mac.RunBootFrom(stub, z80h.Entry{StepCap: 30_000_000})
	if err != nil {
		t.Fatalf("stub faulted: %v (PC=&%04X)", err, res.PC)
	}
	if !res.Halted {
		t.Fatalf("CLS + 12 CRs did NOT complete (PC=&%04X) — the CLS defence is not holding; every pushable tool's screen output is at risk of the i319a wedge", res.PC)
	}
	t.Logf("CLS + 12 CRs clean (steps=%d) — the pushable-tool print budget is safe", res.Steps)
}
