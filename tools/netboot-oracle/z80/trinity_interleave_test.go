package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// The Trinity microcontroller shares ONE read-back latch across the three data
// ports &DD (EEPROM) / &DE (ENC) / &DF (SD) — IN from any of them returns the last
// byte clocked by whichever peripheral the &DC MUX last selected
// (manual:IMG_20260617_162617; DISCOVERY_REPORT §7). So after a peripheral switch
// you MUST clock the newly-selected peripheral before reading the latch, else IN
// returns the PREVIOUS peripheral's stale byte ("do not interleave OUTs to
// different peripherals before reading back"). The real SAM blindly returns the
// stale byte; the model's StrictInterleave/InterleaveViolations catch it so a
// diagnostic can pinpoint the offending instruction.
//
// &DC (control/status) is exempt — it is always readable (BUSY etc.).

const (
	ctlPort = 0xDC
	encData = 0xDE
	sdData  = 0xDF
	encSel  = 0x21 // selENCEnable (eon)
	sdSel   = 0x31 // selSDManual
)

// clearBusy reads &DC (status) to let the one-SPI-byte BUSY flag elapse between
// OUTs, without touching the shared DATA latch (so it does NOT count as reading
// back a clocked data byte).
func clearBusy(enc *z80h.ENC28J60) { enc.In(ctlPort) }

// TestTrinityInterleavePositiveControl: a DELIBERATE rule violation must be caught.
// Select ENC, clock a byte to it (latch <- ENC's MISO), switch the MUX to SD, then
// read the data latch WITHOUT clocking SD first — that read returns the stale ENC
// byte, which is exactly the violation.
func TestTrinityInterleavePositiveControl(t *testing.T) {
	enc := z80h.NewENC28J60()
	enc.AttachSD(csdV2(0x001D59))

	enc.Out(ctlPort, encSel)  // MUX-select ENC
	clearBusy(enc)
	enc.Out(encData, 0x5A)    // clock a data byte to ENC -> latch written by ENC
	clearBusy(enc)
	enc.Out(ctlPort, sdSel)   // switch MUX to SD (no read-back of the ENC byte)
	clearBusy(enc)
	_ = enc.In(sdData)        // read the latch while SD selected -> STALE ENC byte

	n, msg := enc.InterleaveViolations()
	if n == 0 {
		t.Fatalf("detector did NOT fire on a deliberate ENC->SD cross-peripheral read — the rule is not enforced")
	}
	t.Logf("positive control OK: detector fired %d time(s): %s", n, msg)

	// StrictInterleave must turn the same violation into a loud fault (panic), so a
	// diagnostic can stop ON the offending instruction.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("StrictInterleave did NOT panic on the violation")
			}
		}()
		e2 := z80h.NewENC28J60()
		e2.AttachSD(csdV2(0x001D59))
		e2.StrictInterleave = true
		e2.Out(ctlPort, encSel)
		clearBusy(e2)
		e2.Out(encData, 0x5A)
		clearBusy(e2)
		e2.Out(ctlPort, sdSel)
		clearBusy(e2)
		_ = e2.In(sdData) // must panic
	}()
}

// TestTrinityInterleaveNegativeControl: a well-behaved clock-then-read on the
// selected peripheral must NOT fire (no false positives).
func TestTrinityInterleaveNegativeControl(t *testing.T) {
	enc := z80h.NewENC28J60()
	enc.AttachSD(csdV2(0x001D59))
	enc.StrictInterleave = true // would panic on any violation

	// SD: select, clock SD, read SD's own byte — the correct order.
	enc.Out(ctlPort, sdSel)
	clearBusy(enc)
	enc.Out(sdData, 0xFF)  // clock SD -> latch written by SD
	clearBusy(enc)
	_ = enc.In(sdData)     // read SD's own byte: legitimate

	// Switch to ENC, clock ENC, read ENC's byte — also correct.
	enc.Out(ctlPort, encSel)
	clearBusy(enc)
	enc.Out(encData, 0x5A)
	clearBusy(enc)
	_ = enc.In(encData)

	if n, msg := enc.InterleaveViolations(); n != 0 {
		t.Fatalf("negative control: detector falsely fired %d time(s): %s", n, msg)
	}
}
