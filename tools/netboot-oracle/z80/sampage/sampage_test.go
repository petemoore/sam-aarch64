package sampage

import "testing"

// TestFlatConfigIsContiguousRAM proves the default config maps logical
// &0000-&FFFF onto four distinct contiguous RAM pages, so a byte written at any
// address reads back at that address (the flat-equivalent behaviour the netboot
// tests rely on) and the four sections land in pages 1..4.
func TestFlatConfigIsContiguousRAM(t *testing.T) {
	m := New()
	if m.LMPR != FlatLMPR || m.HMPR != FlatHMPR {
		t.Fatalf("New() config LMPR=%02X HMPR=%02X, want %02X/%02X", m.LMPR, m.HMPR, FlatLMPR, FlatHMPR)
	}
	// Every section is writable RAM and round-trips.
	for _, addr := range []uint16{0x0000, 0x3FFF, 0x4000, 0x7FFF, 0x8000, 0xBFFF, 0xC000, 0xFFFF} {
		m.Set(addr, 0xA5)
		if got := m.Get(addr); got != 0xA5 {
			t.Errorf("&%04X round-trip = %02X, want A5", addr, got)
		}
	}
	// Sections map to pages 1,2,3,4 respectively.
	want := map[uint16]int{0x0000: 1, 0x4000: 2, 0x8000: 3, 0xC000: 4}
	for addr, page := range want {
		m = New()
		m.Set(addr, 0x5A)
		if m.RAM[page][addr&0x3FFF] != 0x5A {
			t.Errorf("&%04X did not land in physical page %d", addr, page)
		}
	}
}

// TestHMPRRemapsSectionC proves an OUT to HMPR relocates section C to the
// selected page — the capability the flat model lacked.
func TestHMPRRemapsSectionC(t *testing.T) {
	m := New()
	m.RAM[7][0x0010] = 0xEE // seed page 7
	if !m.PortOut(PortHMPR, 7) {
		t.Fatal("PortOut(HMPR) returned false")
	}
	if got := m.Get(0x8010); got != 0xEE {
		t.Errorf("after HMPR=7, &8010 = %02X, want EE (page 7)", got)
	}
	if v, ok := m.PortIn(PortHMPR); !ok || v&pageMask != 7 {
		t.Errorf("PortIn(HMPR) = %02X (%v), want low5=7", v, ok)
	}
}

// TestHMPRPreservesCLUTBits confirms HMPR bits 5-7 (mode-3 CLUT) survive a page
// write, matching hardware (and the assembler emulator).
func TestHMPRPreservesCLUTBits(t *testing.T) {
	m := New()
	m.HMPR = 0xE0 // all CLUT bits set, page 0
	m.PortOut(PortHMPR, 0x05)
	if m.HMPR != 0xE5 {
		t.Errorf("HMPR = %02X after page write, want E5 (CLUT preserved, page 5)", m.HMPR)
	}
}

// TestROM0ReadOnly proves section A reads ROM0 and drops writes when LMPR bit5=0.
func TestROM0ReadOnly(t *testing.T) {
	m := New()
	m.ROM[0x0123] = 0x42
	m.LMPR &^= lmprRAMSecA // bit5=0 -> ROM0 at section A
	if got := m.Get(0x0123); got != 0x42 {
		t.Errorf("ROM0 read &0123 = %02X, want 42", got)
	}
	m.Set(0x0123, 0xFF) // must be dropped (ROM)
	if m.ROM[0x0123] != 0x42 {
		t.Errorf("ROM0 was written through (%02X) — writes must drop", m.ROM[0x0123])
	}
}

// TestROM1ReadOnly proves section D reads ROM1 and drops writes when LMPR bit6=1
// — the &C000 wall the i87a dumper read through.
func TestROM1ReadOnly(t *testing.T) {
	m := New()
	m.ROM[PageSize+0x0044] = 0x99 // ROM1 byte at &C044
	m.LMPR |= lmprROM1SecD        // bit6=1 -> ROM1 at section D
	if got := m.Get(0xC044); got != 0x99 {
		t.Errorf("ROM1 read &C044 = %02X, want 99", got)
	}
	m.Set(0xC044, 0x00) // must be dropped
	if m.ROM[PageSize+0x0044] != 0x99 {
		t.Errorf("ROM1 was written through — writes must drop")
	}
}

// TestSectionBFollowsLMPRPlusOne and section D follows HMPR+1, the SAM's paired-
// page rule that makes the dumper's P-1 scratch choice collide with page 0.
func TestSectionBFollowsLMPRPlusOne(t *testing.T) {
	m := New()
	m.LMPR = lmprRAMSecA | 4 // RAM at A, page 4 -> section B = page 5
	m.Set(0x4000, 0x11)
	if m.RAM[5][0] != 0x11 {
		t.Errorf("section B with LMPR page 4 did not map page 5")
	}
	m.HMPR = 6 // section C = page 6 -> section D = page 7
	m.Set(0xC000, 0x22)
	if m.RAM[7][0] != 0x22 {
		t.Errorf("section D with HMPR page 6 did not map page 7")
	}
}
