// z80-test-harness-go — Go-hosted Z80 harness for the sam-aarch64
// assembler test pipeline.
//
// # Overview
//
// The harness loads the prod assembler binary (assembler-prod.bin) plus the
// encoder table (enctab.enc) and a pre-built .tbn input file, then runs the
// Z80 emulator until HALT (or timeout), capturing:
//
//   - The assembled OUT bytes (from physical pages 5+6)
//   - The printer-channel banner ("OK" / "FAIL…")
//   - The last 200 Z80 PC values before HALT
//   - A pass/fail verdict
//
// This replaces the SimCoupé round-trip (disk build + boot + run + extract)
// for each fixture, shrinking per-fixture time from ~30 s to <100 ms.
//
// # Memory layout (mirrors src/m3/assembler.asm)
//
//   Section A (&0000-&3FFF): fake ROM (RST 8 stub at &0008) or ENCTAB page
//   Section B (&4000-&7FFF): page 1 (BASIC sys page + trampoline copy)
//   Section C (&8000-&BFFF): assembler code (page 2 = HMPR_DEFAULT low-5)
//   Section D (&C000-&FFFF): scratch / stack (page 3 = HMPR_DEFAULT+1)
//
//   Physical page 4:   ENCTAB (paged into section A via LMPR_ENCTAB = &24)
//   Physical pages 5-6: OUT buffer (written by assembler)
//   Physical pages 7-12: IN .tbn (pre-deposited; read via LMPR brackets)
//
// # RST 8 / SAMDOS hook interception
//
// The SAM Coupé ROM PTDOS sits at &0008 and dispatches SAMDOS hooks.  We
// install a minimal fake ROM that:
//
//   &0008:  EX (SP),HL   ; HL = return addr (= DEFB byte address)
//           LD A,(HL)    ; A = hook code
//           INC HL       ; HL = addr after DEFB
//           EX (SP),HL   ; (SP) = addr after DEFB; HL restored
//           OUT (0xFD),A ; signal hook code to IO.Out
//           RET          ; resume at addr after DEFB
//
// IO.Out(0xFD, hookCode) dispatches to per-hook handlers.  Hooks that the
// assembler uses:
//
//   129 HGTHD — fake: populate &4B50+34 (pages) and &4B50+35-36 (length)
//               for the IN file; enctab.enc uses hardcoded constants.
//   130 HLOAD — no-op: data already pre-deposited in the target pages.
//   132 HSAVE — capture: read OUT bytes from UIFA[31..36] + pages 5-6.
//
// Port &FA = LMPR (section A+B page selector)
// Port &FB = HMPR (section C+D page selector; bits 5-7 = CLUT, preserved)
// Port &E8 = printer data; &E9 = printer strobe
// Port &FD = RST-8 hook dispatch (synthetic, handled by harness)
// Port &FE = border colour (ignored)
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/koron-go/z80"
)

const (
	// SAM Coupé page size.
	pageSize = 16384

	// Number of physical RAM pages (512 KB total on a 512 KB Coupé).
	numPages = 32

	// Physical page allocations.
	enctabPage = 4  // ENCTAB encoder table
	outBasePage = 5 // first OUT buffer page (page 5 = low zone, page 6 = high)
	inBasePage  = 7 // first IN .tbn page (up to 6 pages = pages 7..12)

	// ROM size: 32 KB (two 16 KB halves).
	romSize = 32768

	// Synthetic port: RST 8 hook dispatch.  Not a real SAM port.
	portHookDispatch = 0xFD

	// Printer ports.
	portPrintData   = 0xE8
	portPrintStrobe = 0xE9

	// Paging ports.
	portLMPR = 0xFA // &FA
	portHMPR = 0xFB // &FB

	// SAMDOS UIFA buffer address (in section B, page 1).
	uifaAddr = 0x4B00
	// SAMDOS UIFA copy at &4B50 (filled by HGTHD via txhed).
	uifaCopyAddr = 0x4B50

	// Fake ROM: our RST-8 stub lives at &0008.
	// Section A normally holds ROM0; we only need &0008 to work.
	// The rest of the ROM range is 0xFF (unused).

	// LMPR values.
	lmprDefault = 0x1F // typical boot LMPR (ROM0 in A, page 0 in B)
	lmprEnctab  = 0x24 // RAM0 bit + page 4 → section A = ENCTAB

	// HMPR default: assembler code in section C = page 2.
	hmprDefault = 0x02

	// Maximum PC-trace ring buffer size.
	pcTraceSize = 200

	// HOOK codes (samdos/src/b.s::samhk, numbered from 128).
	hookHGTHD = 129
	hookHLOAD = 130
	hookHSAVE = 132
	hookHGFLE = 158
	hookLBYT  = 159
)

// Result holds all outputs from a single harness run.
type Result struct {
	Passed         bool
	PrinterCapture string
	OutBytes       []byte
	Last200PC      []uint16
	ExitReason     string
}

// Hardware implements the koron-go/z80 Memory and IO interfaces.
// It models the relevant subset of SAM Coupé hardware needed by the M3
// assembler: 32 pages of 16 KB RAM, a 32 KB fake ROM (ROM0 at &0000, ROM1
// at &4000 within the ROM slice), LMPR/HMPR paging, printer ports, and the
// synthetic RST-8 hook dispatch port.
type Hardware struct {
	rom [romSize]byte
	ram [numPages][pageSize]byte
	lmpr uint8
	hmpr uint8

	// Printer capture: bytes written to &E8 on each strobe-high.
	printerBuf strings.Builder
	lastPrintData uint8

	// Hook dispatch callback: called by IO.Out(portHookDispatch, code).
	hookHandler func(hookCode uint8)

	// PC trace ring buffer.
	pcTrace [pcTraceSize]uint16
	pcHead  int // index of oldest entry (ring buffer)
	pcCount int // number of valid entries

	// CPU back-reference (set after construction).
	cpu *z80.CPU
}

func newHardware() *Hardware {
	h := &Hardware{
		lmpr: lmprDefault,
		hmpr: hmprDefault,
	}
	h.installFakeROM()
	return h
}

// installFakeROM writes the RST-8 intercept stub at &0008 in the fake ROM.
//
// The stub:
//   E3       EX (SP),HL   ; HL = return addr (= DEFB-hook address); (SP) = old HL
//   7E       LD A,(HL)    ; A = hook code
//   23       INC HL       ; HL = addr after DEFB
//   E3       EX (SP),HL   ; (SP) = addr after DEFB; HL restored
//   D3 FD    OUT (0xFD),A ; dispatch to hook handler via IO.Out
//   C9       RET          ; resume at addr after DEFB
//
// All other ROM bytes default to 0xFF (RST 0x38 in Z80 — never reached
// in normal flow; treated as a trap if PC lands there).
func (h *Hardware) installFakeROM() {
	// Fill with 0xFF.
	for i := range h.rom {
		h.rom[i] = 0xFF
	}
	// RST-8 stub at &0008.
	stub := []byte{
		0xE3,       // EX (SP),HL
		0x7E,       // LD A,(HL)
		0x23,       // INC HL
		0xE3,       // EX (SP),HL
		0xD3, 0xFD, // OUT (0xFD),A
		0xC9,       // RET
	}
	copy(h.rom[0x0008:], stub)
}

// resolve returns the physical memory slice byte for the given Z80 address.
// For reads, it uses the current LMPR/HMPR; for writes, same logic but ROM
// writes are silently dropped (handled in Set).
//
// Memory map (SAM Coupé Tech Manual v3.0 §6.10, verbatim from basic-emulator-spike):
//
//   Section A (0x0000-0x3FFF): ROM 0 if LMPR bit5=0; else RAM page (LMPR & 0x1F)
//   Section B (0x4000-0x7FFF): RAM page (LMPR & 0x1F + 1) mod 32; LMPR bit7=WPRAM (ignored)
//   Section C (0x8000-0xBFFF): RAM page (HMPR & 0x1F)
//   Section D (0xC000-0xFFFF): ROM 1 if LMPR bit6=1; else RAM page (HMPR & 0x1F + 1) mod 32
func (h *Hardware) resolveRead(addr uint16) (data uint8) {
	offset := int(addr & 0x3FFF)
	section := addr >> 14
	switch section {
	case 0: // Section A
		if h.lmpr&0x20 == 0 {
			return h.rom[offset] // ROM 0
		}
		return h.ram[h.lmpr&0x1F][offset]
	case 1: // Section B
		page := (h.lmpr&0x1F + 1) & 0x1F
		return h.ram[page][offset]
	case 2: // Section C
		return h.ram[h.hmpr&0x1F][offset]
	case 3: // Section D
		if h.lmpr&0x40 != 0 {
			return h.rom[16384+offset] // ROM 1
		}
		page := (h.hmpr&0x1F + 1) & 0x1F
		return h.ram[page][offset]
	}
	return 0xFF
}

// resolveWritePage returns the physical page index for a section-C or D write.
// Returns -1 for ROM (drop write).
func (h *Hardware) resolveWritePage(addr uint16) int {
	section := addr >> 14
	switch section {
	case 0:
		if h.lmpr&0x20 == 0 {
			return -1 // ROM 0 — drop
		}
		return int(h.lmpr & 0x1F)
	case 1:
		return int((h.lmpr&0x1F+1) & 0x1F)
	case 2:
		return int(h.hmpr & 0x1F)
	case 3:
		if h.lmpr&0x40 != 0 {
			return -1 // ROM 1 — drop
		}
		return int((h.hmpr&0x1F+1) & 0x1F)
	}
	return -1
}

// Get implements z80.Memory.
func (h *Hardware) Get(addr uint16) uint8 {
	return h.resolveRead(addr)
}

// Set implements z80.Memory.
func (h *Hardware) Set(addr uint16, value uint8) {
	page := h.resolveWritePage(addr)
	if page < 0 {
		return // ROM write — silently drop
	}
	h.ram[page][addr&0x3FFF] = value
}

// In implements z80.IO.
func (h *Hardware) In(port uint8) uint8 {
	switch port {
	case portLMPR:
		return h.lmpr
	case portHMPR:
		return h.hmpr
	}
	return 0xFF
}

// Out implements z80.IO.
func (h *Hardware) Out(port uint8, value uint8) {
	switch port {
	case portLMPR:
		h.lmpr = value
	case portHMPR:
		// Bits 5-7 of HMPR are mode-3 CLUT — preserve them on any write.
		// Bits 0-4 are the page number for sections C+D.
		h.hmpr = (h.hmpr & 0xE0) | (value & 0x1F)
	case portPrintData:
		h.lastPrintData = value
	case portPrintStrobe:
		// Centronics strobe: rising edge (low→high) latches the data byte.
		if value&0x01 != 0 {
			h.printerBuf.WriteByte(h.lastPrintData)
		}
	case portHookDispatch:
		// Synthetic port: dispatched by our fake ROM stub at &0008.
		if h.hookHandler != nil {
			h.hookHandler(value)
		}
	case 0xFE:
		// Border colour — ignored.
	}
}

// recordPC adds the current CPU PC to the ring buffer.
func (h *Hardware) recordPC(pc uint16) {
	if h.pcCount < pcTraceSize {
		h.pcTrace[(h.pcHead+h.pcCount)%pcTraceSize] = pc
		h.pcCount++
	} else {
		h.pcTrace[h.pcHead] = pc
		h.pcHead = (h.pcHead + 1) % pcTraceSize
	}
}

// last200PC returns the PC trace in chronological order (oldest first).
func (h *Hardware) last200PC() []uint16 {
	out := make([]uint16, h.pcCount)
	for i := range out {
		out[i] = h.pcTrace[(h.pcHead+i)%pcTraceSize]
	}
	return out
}

// peekPage returns a byte directly from a physical RAM page, bypassing
// the LMPR/HMPR resolution.  Used by the HSAVE handler to read OUT bytes
// from pages 5+6 regardless of the current paging state.
func (h *Hardware) peekPage(page int, offset int) uint8 {
	return h.ram[page][offset]
}

// pokePage writes a byte directly to a physical RAM page, bypassing the
// LMPR/HMPR resolution.  Used to pre-deposit enctab.enc, IN .tbn, etc.
func (h *Hardware) pokePage(page int, offset int, value uint8) {
	h.ram[page][offset] = value
}

// depositInPages copies data into consecutive physical pages starting at
// inBasePage (page 7).  Up to 6 pages (96 KB) are supported.
func (h *Hardware) depositInPages(data []byte) {
	for i, b := range data {
		page := inBasePage + i/pageSize
		offset := i % pageSize
		if page >= inBasePage+6 {
			panic(fmt.Sprintf("IN data exceeds 6-page (96 KB) ceiling at byte %d", i))
		}
		h.ram[page][offset] = b
	}
}

// depositPage copies data into a single physical page (offset 0).
func (h *Hardware) depositPage(page int, data []byte) {
	if len(data) > pageSize {
		panic(fmt.Sprintf("depositPage: data (%d B) exceeds pageSize", len(data)))
	}
	copy(h.ram[page][:], data)
}

// readPageBytes reads a slice of bytes from physical page storage, spanning
// page boundaries if length demands it.
func (h *Hardware) readPageBytes(startPage int, offset int, length int) []byte {
	out := make([]byte, length)
	for i := range out {
		pg := startPage + (offset+i)/pageSize
		off := (offset + i) % pageSize
		out[i] = h.ram[pg][off]
	}
	return out
}

// Run executes the assembler until HALT or timeout.
//
//   assemblerBin: contents of assembler-prod.bin (loaded at &8000)
//   enctabData:   contents of enctab.enc (pre-deposited in page 4)
//   inData:       contents of a .tbn file (pre-deposited in pages 7-12)
//   timeout:      wall-clock limit
//
// Returns a Result describing the outcome.
func Run(assemblerBin, enctabData, inData []byte, timeout time.Duration) Result {
	hw := newHardware()

	// Deposit assembler binary at section C (page 2 = HMPR_DEFAULT low-5).
	// The binary is built for org &8000 and loads into physical page 2
	// (section C) when HMPR = hmprDefault.
	hw.depositPage(2, assemblerBin)

	// Deposit enctab.enc into page 4.
	hw.depositPage(enctabPage, enctabData)

	// Deposit IN .tbn into pages 7..12.
	hw.depositInPages(inData)

	// Compute IN file geometry for HGTHD simulation.
	inLen := len(inData)
	inPages := inLen / pageSize
	inRemainder := inLen % pageSize
	if inRemainder == 0 && inLen > 0 {
		inPages--
		inRemainder = pageSize
	}
	// If inLen is exactly a multiple of pageSize, all pages are full.
	// Represent as: pages = inLen/pageSize - 1, remainder = pageSize?
	// No: SAMDOS uses C = number of FULL 16K pages, DE = bytes in the last partial page.
	// For a file of exactly N*pageSize bytes: C = N-1 (actually C could be N, DE=0).
	// From the assembler source:
	//   in_file_pages_ok: (in_file_pages) from &4B50+34
	//   in_file_len: from &4B50+35-36 (high byte bit 7 cleared)
	// And call convention: B = IN_BASE_PAGE, C = pages, DE = length_mod_16K.
	// For a 22-byte file: pages=0, length_mod_16K=22.
	inFilePages := uint8(inLen / pageSize)
	inFileLenMod16K := uint16(inLen % pageSize)
	if inLen > 0 && inFileLenMod16K == 0 {
		// Exactly a multiple: C pages of full data, DE = 0 would mean 0 bytes?
		// SAMDOS convention: length_mod_16K = 0 means full 16K. So use pages-1.
		inFilePages--
		inFileLenMod16K = pageSize
	}
	_ = inPages
	_ = inRemainder

	// HSAVE capture state.
	var capturedOut []byte

	// Install hook handler.
	hw.hookHandler = func(hookCode uint8) {
		switch hookCode {
		case hookHGTHD:
			// HGTHD: Read UIFA[1..10] to identify the file.
			// UIFA is at &4B00 in page 1 (section B always = page (LMPR_low5+1)).
			// Since we read from section B, page = (lmpr & 0x1F + 1) & 0x1F.
			// At boot, LMPR = lmprDefault = &1F, so section B = page 0.
			// During the assembler's execution after start:, section B stays as
			// the boot-LMPR's page (page 0) unless LMPR changes.
			//
			// Read name from UIFA[1..10] at the resolved address.
			uifaPage := int((hw.lmpr&0x1F + 1) & 0x1F)
			uifaOffset := uifaAddr & 0x3FFF
			name := string(hw.ram[uifaPage][uifaOffset+1 : uifaOffset+11])

			if strings.HasPrefix(name, "IN") {
				// Populate &4B50+34 (page count) and &4B50+35-36 (length, bit15 set).
				// The copy area &4B50 is also in section B (page 0 at boot).
				copyPage := uifaPage
				copyOffset := uifaCopyAddr & 0x3FFF
				// byte 34: page count
				hw.ram[copyPage][copyOffset+34] = inFilePages
				// bytes 35-36: length-mod-16K, with bit 15 set (SAMDOS `set 7,d`).
				lenWithFlag := inFileLenMod16K | 0x8000
				hw.ram[copyPage][copyOffset+35] = uint8(lenWithFlag)
				hw.ram[copyPage][copyOffset+36] = uint8(lenWithFlag >> 8)
			}
			// For enctab.enc: assembler uses hardcoded constants, HGTHD only
			// needs to not crash. No extra work needed.

		case hookHLOAD:
			// HLOAD: data already pre-deposited in physical pages.
			// The trampoline has set HMPR to the target page before this RST 8.
			// HLOAD would normally copy from disk to section C.
			// We've already placed the data there, so just return.
			// No-op.

		case hookHSAVE:
			// HSAVE: capture OUT bytes from the paged OUT buffer.
			// The assembler has populated UIFA[31..36]:
			//   UIFA[31]: start page (= outBasePage = 5)
			//   UIFA[32-33]: section-C source offset (&8000)
			//   UIFA[34]: pages count (OUT_LEN >> 14)
			//   UIFA[35-36]: remainder length (OUT_LEN & 0x3FFF)
			uifaPage := int((hw.lmpr&0x1F + 1) & 0x1F)
			uifaOffset := int(uifaAddr & 0x3FFF)
			startPage := int(hw.ram[uifaPage][uifaOffset+31])
			pagesCount := int(hw.ram[uifaPage][uifaOffset+34])
			remLo := uint16(hw.ram[uifaPage][uifaOffset+35])
			remHi := uint16(hw.ram[uifaPage][uifaOffset+36])
			remainder := int(remLo | (remHi << 8))

			// Total length = pagesCount*16384 + remainder.
			totalLen := pagesCount*pageSize + remainder
			capturedOut = hw.readPageBytes(startPage, 0, totalLen)

		case hookHGFLE:
			// HGFLE: open file for reading. Not called by prod assembler
			// (uses HGTHD+HLOAD instead). Log and continue.
			// (If somehow called, treat as no-op.)

		case hookLBYT:
			// LBYT: read one byte. Not called by prod assembler. No-op.
		}
	}

	cpu := &z80.CPU{Memory: hw, IO: hw}
	hw.cpu = cpu

	// Set initial CPU state.
	// The assembler binary is loaded at &8000 (section C, page 2).
	// Its entry point is jp &8000 (first instruction).
	cpu.PC = 0x8000
	// LMPR = lmprDefault (&1F): ROM0 in section A, page 0 in section B.
	// This is what the assembler's `in a, (250); ld (LMPR_DEFAULT_RUNTIME), a` captures.
	hw.lmpr = lmprDefault
	// HMPR = hmprDefault (2): assembler code in section C.
	hw.hmpr = hmprDefault
	// SP: assembler sets SP = &C100 in start:; the harness starts with SP = &FFFE
	// as a safe default before the assembler's `ld sp, &C100` executes.
	cpu.SP = 0xFFFE

	deadline := time.Now().Add(timeout)
	var exitReason string

	for time.Now().Before(deadline) {
		hw.recordPC(cpu.PC)
		cpu.Step()
		if cpu.HALT {
			exitReason = fmt.Sprintf("HALT at PC=%04X", cpu.PC)
			break
		}
	}
	if exitReason == "" {
		exitReason = fmt.Sprintf("timeout after %v", timeout)
	}

	printerStr := hw.printerBuf.String()
	// Strip trailing CR/LF/NUL for cleaner comparison.
	printerStr = strings.TrimRight(printerStr, "\r\n\x00")

	passed := strings.HasPrefix(printerStr, "OK") && capturedOut != nil

	return Result{
		Passed:         passed,
		PrinterCapture: printerStr,
		OutBytes:       capturedOut,
		Last200PC:      hw.last200PC(),
		ExitReason:     exitReason,
	}
}
