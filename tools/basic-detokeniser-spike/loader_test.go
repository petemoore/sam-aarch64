package main

import (
	"testing"
)

func TestPosAdvance_StaysInSamePage(t *testing.T) {
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x100)
	want := pos{page: 1, offset: 0x1DD5}
	if got != want {
		t.Errorf("advance(0x100): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_CrossesPage1To2AtExactBoundary(t *testing.T) {
	// page 1 holds 0x4000 - 0x1CD5 = 0x232B bytes from PROG.
	// Advancing exactly 0x232B from (1, 0x1CD5) lands on (2, 0x0000).
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x232B)
	want := pos{page: 2, offset: 0x0000}
	if got != want {
		t.Errorf("advance(0x232B): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_CrossesPage1To2WithRemainder(t *testing.T) {
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x232C) // one past the boundary
	want := pos{page: 2, offset: 0x0001}
	if got != want {
		t.Errorf("advance(0x232C): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_SpansMultiplePages(t *testing.T) {
	// 50000 bytes from (1, 0x1CD5):
	//   page 1 absorbs 0x4000 - 0x1CD5 = 0x232B = 9003 bytes
	//   page 2 absorbs 0x4000 = 16384 bytes (cumulative 25387)
	//   page 3 absorbs 0x4000 = 16384 bytes (cumulative 41771)
	//   page 4 absorbs 50000 - 41771 = 8229 = 0x2025 bytes
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(50000)
	want := pos{page: 4, offset: 0x2025}
	if got != want {
		t.Errorf("advance(50000): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_ZeroIsIdentity(t *testing.T) {
	p := pos{page: 7, offset: 0x1234}
	got := p.advance(0)
	if got != p {
		t.Errorf("advance(0): got %+v, want %+v", got, p)
	}
}

func TestPokeRAMPage_WritesToCorrectPhysicalPage(t *testing.T) {
	hw := &Hardware{}
	// Default LMPR=0, HMPR=0 — would map section C to page 0. The
	// helper must ignore that and write to page 7 regardless.
	pokeRAMPage(hw, 7, 0x1234, 0xAB)
	if got := hw.ram[7][0x1234]; got != 0xAB {
		t.Errorf("hw.ram[7][0x1234] = %02X, want 0xAB", got)
	}
	// Spot-check: page 0 same offset is untouched.
	if got := hw.ram[0][0x1234]; got != 0x00 {
		t.Errorf("hw.ram[0][0x1234] = %02X, want 0x00 (other pages untouched)", got)
	}
}

func TestPokeRAMPage_MasksPageAndOffset(t *testing.T) {
	hw := &Hardware{}
	// Page 0x25 has bit 5 set — should mask to 0x05 (within 32 pages).
	// Offset 0x8000 has section-C bit — should mask to 0x0000.
	pokeRAMPage(hw, 0x25, 0x8000, 0xCD)
	if got := hw.ram[5][0]; got != 0xCD {
		t.Errorf("hw.ram[5][0] = %02X, want 0xCD (page/offset masked)", got)
	}
}

func TestSetSysvarPair_WritesPageByteAndSectionCOffset(t *testing.T) {
	hw := &Hardware{}
	hw.lmpr = 0
	hw.hmpr = 1 // sysvars at 0x5A** live in section B = page 1 with LMPR=0

	setSysvarPair(hw, sysNVARSP, sysNVARS, pos{page: 4, offset: 0x1234})

	// Page byte at sysNVARSP (0x5A87) should be 4.
	if got := peekRAM(hw, sysNVARSP); got != 4 {
		t.Errorf("NVARSP = %02X, want 04", got)
	}
	// 16-bit offset at sysNVARS (0x5A88) should be 0x8000 | 0x1234 = 0x9234.
	if got := peekRAM16(hw, sysNVARS); got != 0x9234 {
		t.Errorf("NVARS = %04X, want 9234 (section-C form of 0x1234)", got)
	}
}

func TestSetSysvarPair_ZeroOffsetGetsSectionCBit(t *testing.T) {
	hw := &Hardware{}
	hw.lmpr = 0
	hw.hmpr = 1

	setSysvarPair(hw, sysSAVARSP, sysSAVARS, pos{page: 2, offset: 0})

	if got := peekRAM(hw, sysSAVARSP); got != 2 {
		t.Errorf("SAVARSP = %02X, want 02", got)
	}
	// Offset 0 → section-C-form 0x8000 (NOT 0x0000).
	if got := peekRAM16(hw, sysSAVARS); got != 0x8000 {
		t.Errorf("SAVARS = %04X, want 8000", got)
	}
}

func TestCheckFits_FitsOn512K(t *testing.T) {
	// Exact max on 512K (PRAMTP=0x1F): available = 32*0x4000 - 0x4000
	// - 0x1CD5 = 500523 bytes; subtract trailer 1024 + headroom 150 =
	// 499349 max program. 480 KB is safely under that.
	err := checkFits(480*1024, 0x1F)
	if err != nil {
		t.Errorf("480 KB on PRAMTP=0x1F: unexpected error: %v", err)
	}
}

func TestCheckFits_FitsOn256K(t *testing.T) {
	// 200 KB on a 256 K (PRAMTP=0x0F) machine fits.
	err := checkFits(200*1024, 0x0F)
	if err != nil {
		t.Errorf("200 KB on PRAMTP=0x0F: unexpected error: %v", err)
	}
}

func TestCheckFits_ExceedsOn256K(t *testing.T) {
	// 400 KB on a 256 K machine does not fit.
	err := checkFits(400*1024, 0x0F)
	if err == nil {
		t.Errorf("400 KB on PRAMTP=0x0F: expected error, got nil")
	}
}

func TestCheckFits_ExceedsOn512K(t *testing.T) {
	// 600 KB on a 512 K machine does not fit (limit is ~488 KB).
	err := checkFits(600*1024, 0x1F)
	if err == nil {
		t.Errorf("600 KB on PRAMTP=0x1F: expected error, got nil")
	}
}
