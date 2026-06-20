// Package sampage models SAM Coupé memory paging: 32 × 16 KB RAM pages, a 32 KB
// system ROM (ROM0 in the low half, ROM1 in the high half), and the LMPR/HMPR
// section-paging registers (ports &FA/&FB). Reads honour the live LMPR/HMPR
// mapping; ROM is read-only, so a write into a section currently mapped to ROM
// is silently dropped — exactly as on hardware (the &C000 ROM wall).
//
// Both Go Z80 harnesses import this package as their one memory model (i190):
//   - tools/netboot-oracle/z80: the netboot harness (CLAUDE.md §7: one emulation
//     layer, used by every test — the split between a flat model and a paged one
//     is what let the i87a dumper ROM-paging bug reach hardware).
//   - tools/z80-test-harness-go: the assembler harness (i190b: the inline pager
//     is replaced by this package so a paging fix reaches both harnesses
//     automatically and cannot silently diverge).
//
// Memory map (SAM Coupé Tech Manual v3.0 §6.10; docs/notes/sam-paging.md):
//
//	Section A (&0000-&3FFF): ROM0 if LMPR bit5=0, else RAM page (LMPR & 0x1F)
//	Section B (&4000-&7FFF): RAM page (LMPR & 0x1F + 1) mod 32
//	Section C (&8000-&BFFF): RAM page (HMPR & 0x1F)
//	Section D (&C000-&FFFF): ROM1 if LMPR bit6=1, else RAM page (HMPR & 0x1F + 1) mod 32
package sampage

const (
	PageSize = 16384 // one SAM page = 16 KB
	NumPages = 32    // 512 KB of RAM = 32 pages
	ROMSize  = 32768 // ROM0 (low 16 KB) + ROM1 (high 16 KB)

	PortLMPR = 0xFA // Low Memory Page Register (sections A+B)
	PortHMPR = 0xFB // High Memory Page Register (sections C+D; bits 5-7 = CLUT)

	pageMask     = 0x1F // low 5 bits of LMPR/HMPR select the page
	lmprRAMSecA  = 0x20 // LMPR bit5: 1 => RAM at section A, 0 => ROM0
	lmprROM1SecD = 0x40 // LMPR bit6: 1 => ROM1 at section D, 0 => RAM (HMPR+1)

	// FlatLMPR/FlatHMPR are the harness's flat-equivalent default config: they
	// map logical &0000-&FFFF onto four contiguous, distinct RAM pages
	// (1, 2, 3, 4), so code that never touches the paging ports sees a plain
	// contiguous 64 KB of RAM — the behaviour the netboot leaf/packet/boot
	// tests rely on. Section A = page 1 (RAM, bit5 set), B = page 2, C = page 3,
	// D = page 4. This is a deliberate synthetic config, not the SAM boot LMPR
	// (which maps ROM0 at section A); flat tests never page, so they never see
	// the difference, and the one existing test that does drive HMPR (trinload)
	// stays internally consistent because it pages every access the same way.
	FlatLMPR = 0x21
	FlatHMPR = 0x03
)

// Mem is a SAM Coupé paged address space: RAM pages, ROM, and the live LMPR/HMPR.
// The exported fields let a harness seed pages, load ROM fixtures, and inspect
// the post-run paging state directly (e.g. to assert a routine restored LMPR).
type Mem struct {
	RAM  [NumPages][PageSize]byte
	ROM  [ROMSize]byte
	LMPR uint8
	HMPR uint8
}

// New returns a Mem in the flat-equivalent default config (see FlatLMPR/FlatHMPR).
func New() *Mem {
	return &Mem{LMPR: FlatLMPR, HMPR: FlatHMPR}
}

func (m *Mem) secAPage() int { return int(m.LMPR & pageMask) }
func (m *Mem) secBPage() int { return int((m.LMPR&pageMask + 1) & pageMask) }
func (m *Mem) secCPage() int { return int(m.HMPR & pageMask) }
func (m *Mem) secDPage() int { return int((m.HMPR&pageMask + 1) & pageMask) }

// Get reads a byte through the live LMPR/HMPR mapping.
func (m *Mem) Get(addr uint16) uint8 {
	offset := int(addr & 0x3FFF)
	switch addr >> 14 {
	case 0: // section A
		if m.LMPR&lmprRAMSecA == 0 {
			return m.ROM[offset] // ROM0
		}
		return m.RAM[m.secAPage()][offset]
	case 1: // section B
		return m.RAM[m.secBPage()][offset]
	case 2: // section C
		return m.RAM[m.secCPage()][offset]
	default: // section D
		if m.LMPR&lmprROM1SecD != 0 {
			return m.ROM[PageSize+offset] // ROM1
		}
		return m.RAM[m.secDPage()][offset]
	}
}

// Set writes a byte through the live mapping; a write into a ROM-mapped section
// (ROM0 at A when LMPR bit5=0, ROM1 at D when LMPR bit6=1) is silently dropped,
// exactly as on hardware.
func (m *Mem) Set(addr uint16, value uint8) {
	offset := int(addr & 0x3FFF)
	switch addr >> 14 {
	case 0: // section A
		if m.LMPR&lmprRAMSecA == 0 {
			return // ROM0 — drop
		}
		m.RAM[m.secAPage()][offset] = value
	case 1: // section B
		m.RAM[m.secBPage()][offset] = value
	case 2: // section C
		m.RAM[m.secCPage()][offset] = value
	default: // section D
		if m.LMPR&lmprROM1SecD != 0 {
			return // ROM1 — drop
		}
		m.RAM[m.secDPage()][offset] = value
	}
}

// PortIn returns (value, true) for the paging registers, (0, false) otherwise,
// so a harness can let non-paging ports fall through to its device model.
func (m *Mem) PortIn(port uint8) (uint8, bool) {
	switch port {
	case PortLMPR:
		return m.LMPR, true
	case PortHMPR:
		return m.HMPR, true
	}
	return 0, false
}

// PortOut applies an OUT to a paging register and reports whether it was one.
// HMPR bits 5-7 are the mode-3 CLUT and are preserved (only the low 5 bits
// select the section-C/D page), matching the SAM hardware and the assembler
// emulator. Other ports are left for the harness's device model.
func (m *Mem) PortOut(port, value uint8) bool {
	switch port {
	case PortLMPR:
		m.LMPR = value
		return true
	case PortHMPR:
		m.HMPR = (m.HMPR & 0xE0) | (value & pageMask)
		return true
	}
	return false
}
