package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// TestTrinityIdentString verifies the emulated microcontroller returns the full
// 8-byte IDENT string "TRI v1.1" for select commands &08..&0F (manual "Trinity -
// Ident"). The 4th char is a SPACE (verified from the source scan). The real driver
// (chk_trinity) only reads &08/&09, but the full string is modelled for fidelity.
func TestTrinityIdentString(t *testing.T) {
	enc := z80h.NewENC28J60()
	var got [8]byte
	for i := 0; i < 8; i++ {
		enc.Out(0xDC, byte(0x08+i)) // select Nth IDENT char
		got[i] = enc.In(0xDD)       // read it back over the shared data port
	}
	if want := "TRI v1.1"; string(got[:]) != want {
		t.Errorf("IDENT string = %q, want %q", string(got[:]), want)
	}
	// The first two chars are what chk_trinity actually gates on.
	if got[0] != 'T' || got[1] != 'R' {
		t.Errorf("IDENT[0:2] = %q %q, want 'T' 'R' (the chk_trinity presence gate)", got[0], got[1])
	}
}

// TestTrinityStatusRegister verifies IN &DC returns the documented %1100BWFE Status
// Register: the fixed top nibble (bits 7,6 = 1; bits 5,4 = 0) is always present, busy
// (bit 3) is always clear, and a configured card adds FLASH (bit 1) + WRITE (bit 2).
func TestTrinityStatusRegister(t *testing.T) {
	enc := z80h.NewENC28J60()

	// No card: fixed top nibble only.
	noCard := enc.In(0xDC)
	if noCard != 0xC0 {
		t.Errorf("IN &DC (no card) = 0x%02X, want 0xC0 (%%1100_0000: fixed bits 7,6 set, FLASH/busy clear)", noCard)
	}
	if noCard&0x80 == 0 || noCard&0x40 == 0 {
		t.Errorf("IN &DC = 0x%02X: fixed bits 7,6 must be set (%%1100BWFE)", noCard)
	}
	if noCard&0x08 != 0 {
		t.Errorf("IN &DC = 0x%02X: BUSY (bit 3) must be clear — the model never stalls", noCard)
	}

	// Configured card: + FLASH (bit 1, present) + WRITE (bit 2, sense-inverted writable).
	var csd [16]byte
	csd[0] = 0x40 // CSD v2 (any valid value; SetCSD just marks the card configured)
	enc.SD().SetCSD(csd)
	withCard := enc.In(0xDC)
	if withCard != 0xC6 {
		t.Errorf("IN &DC (card) = 0x%02X, want 0xC6 (0xC0 | FLASH bit1 | WRITE bit2)", withCard)
	}
	if withCard&0x02 == 0 {
		t.Errorf("IN &DC (card) = 0x%02X: FLASH (bit 1, card present) must be set", withCard)
	}
}

// TestTrinityBusyGate covers the &DC bit 3 BUSY model (gap b, manual:16,22): an OUT
// to a Trinity port raises BUSY for one SPI-byte time; a status read (the canonical
// wait_ready poll) clears it; an OUT issued while still busy — with NO intervening
// status read — is silently DROPPED. With no harness T-state cursor (direct
// Out/In calls, tNow stays 0) two back-to-back OUTs both see tNow=0, so the second
// lands inside the busy window from the first and is dropped.
//
// The test proves the gate has TEETH by its CONSEQUENCE, not just by reading the
// status bit: a CMD0 frame with one busy-dropped byte stays incomplete (only 5 of 6
// bytes land), so the card never computes R1 and a subsequent read returns idle
// 0xFF; the SAME frame sent fully busy-polled completes and returns R1 = 0x01.
func TestTrinityBusyGate(t *testing.T) {
	var csd [16]byte
	csd[0] = 0x40

	// (1) A CMD0 frame (0x40,0,0,0,0,0x95) with its 2nd byte issued WHILE BUSY — no
	// status read between byte 0 and byte 1 — so byte 1 is dropped: 5 bytes land, the
	// frame never completes.
	dropped := z80h.NewENC28J60()
	dropped.AttachSD(csd)
	dropped.Out(0xDC, 0x31) // select SD (raises BUSY)
	dropped.In(0xDC)        // poll clears BUSY
	dropped.Out(0xDF, 0xFF) // leading flush — the required Ncc sync before a command (i245)
	dropped.In(0xDC)        // poll clears BUSY -> the opener lands
	dropped.Out(0xDF, 0x40) // CMD0 opener (raises BUSY)
	dropped.Out(0xDF, 0x00) // 2nd byte issued WHILE BUSY -> DROPPED (no poll between)
	for _, b := range []byte{0x00, 0x00, 0x00, 0x95} {
		dropped.In(0xDC) // poll (clears BUSY) before each remaining byte
		dropped.Out(0xDF, b)
	}
	// Read R1: the frame got only 5 bytes, so completeCommand never fired and the
	// card stays idle -> 0xFF.
	dropped.In(0xDC)
	dropped.Out(0xDF, 0xFF) // dummy clock
	dropped.In(0xDC)
	if r1 := dropped.In(0xDF); r1 != 0xFF {
		t.Errorf("busy-dropped CMD0: R1 = 0x%02X, want 0xFF (the dropped byte left the frame "+
			"incomplete, so no R1 was computed — proves the busy gate dropped the OUT)", r1)
	}

	// (2) The SAME CMD0, fully busy-polled (a status read before every OUT) — every
	// byte lands, the frame completes, R1 = 0x01 (idle).
	polled := z80h.NewENC28J60()
	polled.AttachSD(csd)
	polled.Out(0xDC, 0x31)
	polled.In(0xDC)
	polled.In(0xDC)        // poll
	polled.Out(0xDF, 0xFF) // leading flush — the required Ncc sync before a command (i245)
	for _, b := range []byte{0x40, 0x00, 0x00, 0x00, 0x00, 0x95} {
		polled.In(0xDC) // canonical wait_ready before each OUT
		polled.Out(0xDF, b)
	}
	polled.In(0xDC)
	polled.Out(0xDF, 0xFF)
	polled.In(0xDC)
	if r1 := polled.In(0xDF); r1 != 0x01 {
		t.Errorf("fully-polled CMD0: R1 = 0x%02X, want 0x01 (the frame completed — the gate "+
			"does NOT drop a correctly-polled OUT)", r1)
	}
}

// TestTrinitySharedReadLatch covers gap c (manual:4,8,34,125): IN &DD/&DE/&DF all
// alias ONE shared read-back latch. After the IDENT select (&08) latches 'T', a
// read of a DIFFERENT data port (&DE or &DF) returns the same latched byte — the
// aliasing trap, not a per-port latch.
func TestTrinitySharedReadLatch(t *testing.T) {
	enc := z80h.NewENC28J60()
	enc.Out(0xDC, 0x08) // IDENT char 0 -> latch 'T'
	if got := enc.In(0xDE); got != 'T' {
		t.Errorf("IN &DE after IDENT &08 = 0x%02X, want 'T' (the shared latch, not a per-port one)", got)
	}
	enc.Out(0xDC, 0x09) // IDENT char 1 -> latch 'R'
	if got := enc.In(0xDF); got != 'R' {
		t.Errorf("IN &DF after IDENT &09 = 0x%02X, want 'R' (shared latch aliasing)", got)
	}
}

// TestTrinityENCINT covers gap 5 (manual:19): the ENC's pending-interrupt state is
// surfaced on &DC bit 0. A queued RX frame (a packet waiting) makes ENCINT read set.
func TestTrinityENCINT(t *testing.T) {
	enc := z80h.NewENC28J60()
	if enc.In(0xDC)&0x01 != 0 {
		t.Errorf("ENCINT set with no RX queued; want clear")
	}
	enc.InjectRX([]byte{0x02, 0x54, 0x52, 0x49, 0x4e, 0xbc, 0, 0, 0, 0, 0, 0, 0x08, 0x06})
	if enc.In(0xDC)&0x01 == 0 {
		t.Errorf("ENCINT clear with an RX frame queued; want set (bit 0, manual:19)")
	}
}

// TestTrinitySDInitReturnCode covers gap 6 (manual:74-77): the &38 SD-init wake
// places the documented 0/1/2 return code on the read latch BEFORE the &FF settle.
// A configured SD card reports 2; the &FF settle follows so Colin's poll still breaks.
func TestTrinitySDInitReturnCode(t *testing.T) {
	enc := z80h.NewENC28J60()
	var csd [16]byte
	csd[0] = 0x40
	enc.AttachSD(csd)
	enc.Out(0xDC, 0x31)
	enc.In(0xDC)
	enc.Out(0xDC, 0x38) // SD-init wake
	enc.In(0xDC)
	// First post-wake read = the init return code (2 = SD).
	enc.Out(0xDF, 0xFF)
	enc.In(0xDC)
	if code := enc.In(0xDF); code != 2 {
		t.Errorf("&38 init return code = %d, want 2 (SD; manual:74-77)", code)
	}
	// Subsequent reads settle to &FF (Colin's wake poll breaks on this).
	enc.Out(0xDF, 0xFF)
	enc.In(0xDC)
	if settle := enc.In(0xDF); settle != 0xFF {
		t.Errorf("&38 settle byte = 0x%02X, want 0xFF", settle)
	}
}

// TestTrinityWriteProtect covers gap 7 (manual:17; bdos:42): a write-protected card
// reads &DC bit 2 CLEAR (sense-inverted), so the driver's CPL/AND 4 WP gate aborts.
func TestTrinityWriteProtect(t *testing.T) {
	enc := z80h.NewENC28J60()
	var csd [16]byte
	csd[0] = 0x40
	enc.AttachSD(csd)
	if enc.In(0xDC)&0x04 == 0 {
		t.Errorf("writable card: &DC bit 2 must be SET (sense-inverted)")
	}
	enc.SD().SetWriteProtect(true)
	if enc.In(0xDC)&0x04 != 0 {
		t.Errorf("write-protected card: &DC bit 2 must be CLEAR (the WP-abort path)")
	}
}

// TestTrinityPushPopReadByte covers gap 11 (manual:37,38): &02 saves the pending
// read-byte and &03 restores it (the one-deep ISR slot).
func TestTrinityPushPopReadByte(t *testing.T) {
	enc := z80h.NewENC28J60()
	enc.Out(0xDC, 0x08) // latch 'T'
	if enc.In(0xDD) != 'T' {
		t.Fatal("setup: latch should hold 'T'")
	}
	enc.Out(0xDC, 0x02) // PUSH 'T'
	enc.In(0xDC)        // busy-poll (the canonical clear before the next select)
	enc.Out(0xDC, 0x09) // latch 'R' (clobbers the live latch)
	if enc.In(0xDD) != 'R' {
		t.Fatal("setup: latch should now hold 'R'")
	}
	enc.Out(0xDC, 0x03) // POP -> restore 'T'
	if got := enc.In(0xDD); got != 'T' {
		t.Errorf("after PUSH 'T' / POP = 0x%02X, want 'T' (the saved read-byte)", got)
	}
}

// TestTrinityMUXDeselect covers gap d (manual:27): selecting one peripheral
// deselects the others (one PIC, one active chip-select — not three independent CS
// lines). It proves the mutual exclusion by CONSEQUENCE: an EEPROM read that
// streams its data while the EEPROM is selected stops streaming the moment the SD
// is selected — the EEPROM CS was dropped, so a further data clock returns the SD's
// idle byte, NOT the next EEPROM byte.
func TestTrinityMUXDeselect(t *testing.T) {
	mac := [6]byte{0xA1, 0xB2, 0xC3, 0xD4, 0xE5, 0xF6}
	var ip [4]byte
	var csd [16]byte
	csd[0] = 0x40

	// startEEPRead selects the EEPROM and clocks the READ opcode + 3-byte address 0
	// (the "Trinity Network " index entry: byte 0 = part = 1), leaving the model in
	// the EEPROM data phase so the next dummy-clock+IN yields entry byte 0.
	startEEPRead := func(enc *z80h.ENC28J60) {
		enc.Out(0xDC, 0x11) // select EEPROM
		enc.In(0xDC)
		for _, b := range []byte{0x03, 0x00, 0x00, 0x00} { // READ opcode + addr 0
			enc.Out(0xDD, b)
			enc.In(0xDC)
		}
	}
	// readEEPByte clocks one EEPROM data byte (dummy &DD OUT, poll, IN &DD).
	readEEPByte := func(enc *z80h.ENC28J60) byte {
		enc.Out(0xDD, 0x00)
		enc.In(0xDC)
		return enc.In(0xDD)
	}

	// (1) Control: while the EEPROM stays selected, the read streams its bytes.
	control := z80h.NewENC28J60()
	control.ProgramTrinityNetwork(mac, ip)
	control.AttachSD(csd)
	startEEPRead(control)
	if b0 := readEEPByte(control); b0 != 0x01 {
		t.Fatalf("control EEPROM read byte 0 = 0x%02X, want 0x01 (part=1) — setup wrong", b0)
	}

	// (2) MUX switch: select the EEPROM, start the same read, then select the SD.
	// The EEPROM is now deselected; a data clock no longer returns the EEPROM stream
	// — it returns the SD's idle byte (0xFF), proving the EEPROM CS was dropped.
	switched := z80h.NewENC28J60()
	switched.ProgramTrinityNetwork(mac, ip)
	switched.AttachSD(csd)
	startEEPRead(switched)
	switched.Out(0xDC, 0x31) // select SD -> EEPROM deselected by the MUX
	switched.In(0xDC)
	switched.Out(0xDF, 0xFF) // a clock now targets the SD, not the EEPROM
	switched.In(0xDC)
	if got := switched.In(0xDF); got == 0x01 {
		t.Errorf("after MUX-selecting the SD, a read still returned the EEPROM stream byte " +
			"0x01 — the EEPROM was NOT deselected (the MUX mutual-exclusion failed)")
	} else if got != 0xFF {
		t.Errorf("after MUX-selecting the SD, IN &DF = 0x%02X, want 0xFF (the SD's idle byte; "+
			"the EEPROM stream must NOT continue)", got)
	}
}
