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
