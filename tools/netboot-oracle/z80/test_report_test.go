// test_report_test.go — emulation check of the shared tr_terminate primitive
// (src/netboot/test_report.asm, item i228): a pushed test ends with `jp
// tr_terminate`, which reads the unmapped emulation-detect port (&7F) and either
// di;halts (emulation) or RETs to trinload (real hardware), recording the branch
// in TR_TERM_MODE. Both branches are exercised here (the hardware branch via the
// harness's SetEmuDetectHardware, which makes &7F read 0xFF as the floating bus
// does). tr_terminate lives in test_report.asm, included by every test payload;
// eeprom_roundtrip.bin carries it, so this drives it there.
package z80_test

import "testing"

const (
	trModeEmu = 0xE0 // TR_MODE_EMU in test_report.asm
	trModeHW  = 0xA0 // TR_MODE_HW
)

// TestTerminatorEmulationHalts: with the detect port reading the emulator marker
// (the default), tr_terminate takes the emulation branch — di;halt, in-payload.
func TestTerminatorEmulationHalts(t *testing.T) {
	mac, _ := loadEEPROMRoundtrip(t)
	modeAddr, err := mac.Sym("TR_TERM_MODE")
	if err != nil {
		t.Fatalf("sym TR_TERM_MODE: %v", err)
	}

	res, err := mac.Call("tr_terminate")
	if err != nil {
		t.Fatalf("call tr_terminate: %v", err)
	}
	if !res.Halted {
		t.Fatalf("tr_terminate did not halt")
	}
	if got := mac.Read(modeAddr, 1)[0]; got != trModeEmu {
		t.Errorf("TR_TERM_MODE = &%02X, want &%02X (EMU)", got, trModeEmu)
	}
	// di;halt stops inside the payload (org &8000+), it did not return out.
	if res.PC < 0x8000 {
		t.Errorf("stopped at PC=&%04X (below the payload) — it returned rather than halting in place", res.PC)
	}
}

// TestTerminatorHardwareReturns: with the detect port forced to 0xFF (what the
// real SAM floats), tr_terminate takes the hardware branch — RET, back out to the
// caller (trinload on real hardware). In the Call harness the RET lands on the
// pushed HALT trap (below the payload), so res.Halted stays true; the mode byte +
// the return-out PC prove the RET path ran.
func TestTerminatorHardwareReturns(t *testing.T) {
	mac, _ := loadEEPROMRoundtrip(t)
	mac.SetEmuDetectHardware(true)
	modeAddr, err := mac.Sym("TR_TERM_MODE")
	if err != nil {
		t.Fatalf("sym TR_TERM_MODE: %v", err)
	}

	res, err := mac.Call("tr_terminate")
	if err != nil {
		t.Fatalf("call tr_terminate: %v", err)
	}
	if got := mac.Read(modeAddr, 1)[0]; got != trModeHW {
		t.Errorf("TR_TERM_MODE = &%02X, want &%02X (HW)", got, trModeHW)
	}
	// The hardware branch RETs, so control left the payload (the harness's HALT
	// trap sits below &8000); had it di;halted it would have stopped in-payload.
	if res.PC >= 0x8000 {
		t.Errorf("stopped at PC=&%04X (inside the payload) — it halted rather than returning", res.PC)
	}
}
