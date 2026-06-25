// sdcard_flush_internal_test.go — i245 regression: the SD model enforces the
// documented leading-&FF flush byte that precedes every SD-SPI command frame
// (trinity-sd-z80-interface.md §3/§5; Colin sd.cmd &A7FA; MMCSD cmd9). A command
// whose opcode is not preceded by an idle/flush clock de-syncs the modeled card,
// so no valid response / no &FE token follows — reproducing the i145g all-zeros
// bug a flush-less driver hits on real silicon. Before this enforcement the model
// framed flush-less and flush-prefixed commands identically, which is exactly why
// the i145g bug passed every host test yet failed on hardware.
//
// Internal test (package z80) so it can drive the SDCard command state machine
// directly via out()/sdSelect()/sdReset().
package z80

import "testing"

// newSelectedSD returns a configured SD card freshly bracketed (&04 -> sdReset)
// and SD-selected (&31), ready to receive a command frame.
func newSelectedSD() *SDCard {
	s := &SDCard{}
	s.SetCSD(CSDForV2(0x1000)) // any v2 CSD; we only assert R1/de-sync, not capacity
	s.sdReset()                // the &04 all-deselect bracket: clears flush/de-sync state
	s.sdSelect(selSDManual)    // &31 SD select (manual mode)
	return s
}

// sendCmd clocks the 6 command-frame bytes (opcode|0x40, 4 arg, CRC) out the &DF
// data port, then one R1-poll dummy &FF, and returns the byte the following IN &DF
// would read (s.miso).
func sendCmd(s *SDCard, opcode byte) byte {
	s.out(opcode)
	s.out(0x00)
	s.out(0x00)
	s.out(0x00)
	s.out(0x00)
	s.out(0x95) // CRC (ignored in SPI mode except CMD0/CMD8)
	s.out(0xFF) // R1-poll dummy clock -> latches the response byte
	return s.miso
}

// TestSDFlushRequired_Accepted: a command preceded by the leading &FF flush frames
// normally and returns its R1 (CMD0 -> 0x01 idle). The card is NOT de-synced.
func TestSDFlushRequired_Accepted(t *testing.T) {
	s := newSelectedSD()
	s.out(0xFF) // the documented leading flush (Ncc sync) before the opcode
	r1 := sendCmd(s, 0x40) // CMD0 GO_IDLE_STATE
	if s.deSynced {
		t.Fatal("a flush-prefixed command must NOT de-sync the card")
	}
	if r1 != 0x01 {
		t.Errorf("CMD0 R1 = %#02x, want 0x01 (idle) — the flush-prefixed command should be framed", r1)
	}
}

// TestSDFlushRequired_MissingDesyncs: a command sent with NO leading flush (the
// i145g bug — opcode immediately after select) de-syncs the card. Every read is
// idle &FF and the card stays de-synced for the rest of the transaction, so a
// subsequent CMD9 would never emit its &FE token (all-zeros CSD).
func TestSDFlushRequired_MissingDesyncs(t *testing.T) {
	s := newSelectedSD()
	r1 := sendCmd(s, 0x40) // CMD0 with NO preceding flush
	if !s.deSynced {
		t.Fatal("a flush-less command must de-sync the modeled card (the i145g catch)")
	}
	if r1 != sdIdleMISO {
		t.Errorf("de-synced R1 read = %#02x, want 0xFF (idle) — no valid response while de-synced", r1)
	}
	// Stays de-synced: even a correctly-flushed CMD9 now yields no &FE data token.
	s.out(0xFF) // flush
	csdReads := make([]byte, 0, 4)
	for i := 0; i < 4; i++ {
		csdReads = append(csdReads, sendCmd(s, 0x49)) // CMD9 SEND_CSD
	}
	for i, b := range csdReads {
		if b == sdDataTok {
			t.Errorf("CMD9 read %d returned the &FE data token while de-synced — the card should stay mute", i)
		}
	}
}

// TestSDFlushRequired_FirstCmdAfterWakeFrames: the &38-wake settle poll clocks
// idle bytes before the first command, so CMD0 frames without an explicit flush —
// matching the real ladder (the settle reads ARE the Ncc clocks for CMD0).
func TestSDFlushRequired_FirstCmdAfterWakeFrames(t *testing.T) {
	s := &SDCard{}
	s.SetCSD(CSDForV2(0x1000))
	s.sdReset()
	s.sdSelect(selSDManual) // &31
	s.sdSelect(selSDInit)   // &38 wake -> arms the 1-byte init-type response
	s.sdSelect(selSDManual) // &31 reselect
	// settle poll: read the init-type byte, then idle &FF clocks (these provide the
	// Ncc flush for CMD0).
	s.out(0xFF) // consumes init-type
	s.out(0xFF) // idle clock -> flushBeforeCmd
	r1 := sendCmd(s, 0x40)
	if s.deSynced {
		t.Fatal("CMD0 after the &38 settle poll must frame (the settle reads are its Ncc clocks)")
	}
	if r1 != 0x01 {
		t.Errorf("post-wake CMD0 R1 = %#02x, want 0x01", r1)
	}
}
