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
// # Memory layout (mirrors src/assembler.asm)
//
//	Section A (&0000-&3FFF): fake ROM (RST 8 stub at &0008) or ENCTAB page
//	Section B (&4000-&7FFF): page 1 (BASIC sys page + trampoline copy)
//	Section C (&8000-&BFFF): assembler code (page 2 = HMPR_DEFAULT low-5)
//	Section D (&C000-&FFFF): scratch / stack (page 3 = HMPR_DEFAULT+1)
//
//	Physical page 4:   ENCTAB (paged into section A via LMPR_ENCTAB = &24)
//	Physical pages 5-6: OUT buffer (written by assembler)
//	Physical pages 7-12: IN .tbn (pre-deposited; read via LMPR brackets)
//
// # RST 8 / SAMDOS hook interception
//
// The SAM Coupé ROM PTDOS sits at &0008 and dispatches SAMDOS hooks.  We
// install a minimal fake ROM that:
//
//	&0008:  EX (SP),HL   ; HL = return addr (= DEFB byte address)
//	        LD A,(HL)    ; A = hook code
//	        INC HL       ; HL = addr after DEFB
//	        EX (SP),HL   ; (SP) = addr after DEFB; HL restored
//	        OUT (0xFD),A ; signal hook code to IO.Out
//	        RET          ; resume at addr after DEFB
//
// IO.Out(0xFD, hookCode) dispatches to per-hook handlers.  Hooks that the
// assembler uses:
//
//	129 HGTHD — fake: populate &4B50+34 (pages) and &4B50+35-36 (length)
//	            for the IN file and for enctab.enc (i7 phase A: load_enctab
//	            now reads the DIFA header instead of a hardcoded constant).
//	130 HLOAD — no-op: data already pre-deposited in the target pages.
//	132 HSAVE — capture: read OUT bytes from UIFA[31..36] + pages 5-6.
//
// # SAMDOS file-I/O error longjmp (i183)
//
// A file-I/O hook that fails on real SAMDOS does not return: it calls derr
// (samdos/src/d.s:430), which longjmps via the (hksp) handler vector —
// `ld sp,(hksp); ret` into an installed handler, or (when (hksp)=0, the value
// the dispatcher leaves on entry) prints the error and pops into BASIC's error
// path (a crash for the assembler's di/halt loop).  The harness models this so
// i25's (hksp) handler can be verified in emulation:
//
//   - An unserved HGTHD file (e.g. "sd13" with -sysreg-data omitted), or an
//     injected HGTHD/HSAVE failure (Config.FailHGTHD / Config.FailHSAVE), is
//     treated as file-not-found rather than a silent no-op.
//   - On failure the harness reads the emulated (hksp) vector at
//     Config.HkspAddr (when non-zero, honouring current paging).  A non-zero
//     vector longjmps into the installed handler (SP := (hksp); the RST-8
//     stub's trailing RET then pops the handler entry from [(hksp)] — exactly
//     derr's `ld sp,(hksp); ret`).  A zero vector takes the default path,
//     surfaced as a clean halt whose ExitReason names the failing file — at
//     the true point of failure, not a cryptic downstream &0038 garbage trap.
//
// The exact (hksp)/SAMDOS-bank-paging fidelity is the design dimension shared
// with i25 (the assembler-side handler); HkspAddr is configurable precisely so
// i25 can point it at whatever address its eventual install mechanism uses.
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
	enctabPage = 4 // ENCTAB encoder table
	inBasePage = 7 // first IN .tbn page (up to 6 pages = pages 7..12)

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

	// Fault diagnostics (populated on abnormal exit — trap or timeout).
	// FaultRegs is a snapshot of the CPU register file at the point the
	// run ended; Steps is the total instruction count executed.
	FaultRegs RegSnapshot
	Steps     uint64

	// UnservedFiles lists SAMDOS file names the assembler asked for via
	// HGTHD that the harness could neither serve (no registered NamedFile)
	// nor account for as a known pre-deposited file (enctab.enc / IN) — plus
	// any names with an injected HGTHD failure (Config.FailHGTHD).  Each is a
	// file-not-found that triggers the modelled (hksp) longjmp: with no
	// handler installed it halts the run with a cause-naming ExitReason (e.g.
	// forgetting -sysreg-data, so page 13's sysreg matcher would be empty).
	// Populated in dispatch order, deduplicated.
	UnservedFiles []string
}

// RegSnapshot captures the Z80 register file plus paging state at a point
// in time.  Used for post-mortem analysis of a trap / hang.
type RegSnapshot struct {
	PC, SP             uint16
	AF, BC, DE, HL     uint16
	IX, IY             uint16
	AF_, BC_, DE_, HL_ uint16
	LMPR, HMPR         uint8
}

func (r RegSnapshot) String() string {
	return fmt.Sprintf(
		"PC=%04X SP=%04X AF=%04X BC=%04X DE=%04X HL=%04X IX=%04X IY=%04X "+
			"AF'=%04X BC'=%04X DE'=%04X HL'=%04X LMPR=%02X HMPR=%02X",
		r.PC, r.SP, r.AF, r.BC, r.DE, r.HL, r.IX, r.IY,
		r.AF_, r.BC_, r.DE_, r.HL_, r.LMPR, r.HMPR)
}

// NamedFile is a SAMDOS file the harness can serve via HGTHD+HLOAD.  The
// content is copied into TargetPage when the assembler issues an HLOAD with
// HMPR mapping TargetPage into section C (the COMET trampoline idiom).
type NamedFile struct {
	Name       string // SAMDOS catalogue name, up to 10 chars (no padding needed)
	Content    []byte
	TargetPage int // physical page the assembler will load this file into
	// LoadOffset is the offset within TargetPage where the content lands
	// (the HLOAD destination's &3FFF residue).  Zero for every payload
	// that loads at the bottom of the section-C window (&8000); &0400
	// for the zx0 payload, which loads at &8400 beside sysreg_data.
	// Used by the belt-and-braces pre-deposit; the HLOAD emulation
	// itself honours the caller's live HL.
	LoadOffset int
}

// Hardware implements the koron-go/z80 Memory and IO interfaces.
// It models the relevant subset of SAM Coupé hardware needed by the M3
// assembler: 32 pages of 16 KB RAM, a 32 KB fake ROM (ROM0 at &0000, ROM1
// at &4000 within the ROM slice), LMPR/HMPR paging, printer ports, and the
// synthetic RST-8 hook dispatch port.
type Hardware struct {
	rom  [romSize]byte
	ram  [numPages][pageSize]byte
	lmpr uint8
	hmpr uint8

	// Printer capture: bytes written to &E8 on each strobe-high.
	printerBuf    strings.Builder
	lastPrintData uint8

	// Hook dispatch callback: called by IO.Out(portHookDispatch, code).
	hookHandler func(hookCode uint8)

	// PC trace ring buffer.
	pcTrace [pcTraceSize]uint16
	pcHead  int // index of oldest entry (ring buffer)
	pcCount int // number of valid entries

	// Named-file registry for HGTHD/HLOAD.  Keyed by the 10-char SAMDOS
	// name (trailing spaces trimmed).  The "current" file is selected by
	// HGTHD (which reads the name from UIFA) and consumed by the next
	// HLOAD.
	files       map[string]*NamedFile
	currentFile *NamedFile

	// unservedFiles records, in dispatch order, the names HGTHD asked for
	// that the harness could neither serve from the registry nor account
	// for as a known pre-deposited legacy file (enctab.enc / IN).  Each is a
	// file-not-found that triggers the modelled (hksp) longjmp (triggerFileIOError).
	unservedFiles []string
	unservedSeen  map[string]bool

	// File-I/O error-longjmp model (i183).  hkspAddr is the logical address
	// of the emulated (hksp) handler vector (0 = none installed → default
	// halt).  failHGTHD / failHSAVE force injected file-I/O failures.  When a
	// hook fails, triggerFileIOError either longjmps into the installed
	// handler or records pendingFault to halt the run with a naming message.
	hkspAddr           uint16
	failHGTHD          map[string]bool
	failHSAVE          bool
	strictFileNotFound bool
	pendingFault       string

	// Optional windowed PC trace.  When traceHi>traceLo, every step whose
	// PC lies in [traceLo,traceHi) is appended (in order, unbounded) to
	// windowTrace as a snapshot.  Lets us see the exact path through a
	// specific routine without the noise of inner copy/fill loops.
	traceLo, traceHi uint16
	windowTrace      []RegSnapshot

	// Trigger-PC capture (see Config.TrigPC).
	trigPC   uint16
	trigHit  bool
	trigRegs RegSnapshot
	// trigDumpAddrs: logical addresses to snapshot (via Get, honouring
	// current paging) when trigPC is first reached.  Results in trigDump.
	trigDumpAddrs []uint16
	trigDumpLen   int
	trigDump      map[uint16][]byte
	trigBT        []uint16
	trigStep      uint64

	// CPU back-reference (set after construction).
	cpu *z80.CPU
}

// snapshot captures the current CPU register file + paging state.
func (h *Hardware) snapshot() RegSnapshot {
	c := h.cpu
	if c == nil {
		return RegSnapshot{LMPR: h.lmpr, HMPR: h.hmpr}
	}
	return RegSnapshot{
		PC:   c.PC,
		SP:   c.SP,
		AF:   c.AF.U16(),
		BC:   c.BC.U16(),
		DE:   c.DE.U16(),
		HL:   c.HL.U16(),
		IX:   c.IX,
		IY:   c.IY,
		AF_:  c.Alternate.AF.U16(),
		BC_:  c.Alternate.BC.U16(),
		DE_:  c.Alternate.DE.U16(),
		HL_:  c.Alternate.HL.U16(),
		LMPR: h.lmpr,
		HMPR: h.hmpr,
	}
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
//
//	E3       EX (SP),HL   ; HL = return addr (= DEFB-hook address); (SP) = old HL
//	7E       LD A,(HL)    ; A = hook code
//	23       INC HL       ; HL = addr after DEFB
//	E3       EX (SP),HL   ; (SP) = addr after DEFB; HL restored
//	D3 FD    OUT (0xFD),A ; dispatch to hook handler via IO.Out
//	C9       RET          ; resume at addr after DEFB
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
		0xC9, // RET
	}
	copy(h.rom[0x0008:], stub)
}

// resolve returns the physical memory slice byte for the given Z80 address.
// For reads, it uses the current LMPR/HMPR; for writes, same logic but ROM
// writes are silently dropped (handled in Set).
//
// Memory map (SAM Coupé Tech Manual v3.0 §6.10):
//
//	Section A (0x0000-0x3FFF): ROM 0 if LMPR bit5=0; else RAM page (LMPR & 0x1F)
//	Section B (0x4000-0x7FFF): RAM page (LMPR & 0x1F + 1) mod 32; LMPR bit7=WPRAM (ignored)
//	Section C (0x8000-0xBFFF): RAM page (HMPR & 0x1F)
//	Section D (0xC000-0xFFFF): ROM 1 if LMPR bit6=1; else RAM page (HMPR & 0x1F + 1) mod 32
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
		return int((h.lmpr&0x1F + 1) & 0x1F)
	case 2:
		return int(h.hmpr & 0x1F)
	case 3:
		if h.lmpr&0x40 != 0 {
			return -1 // ROM 1 — drop
		}
		return int((h.hmpr&0x1F + 1) & 0x1F)
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

// depositInPages copies data into consecutive physical pages starting at
// inBasePage (page 7), up to the 6-page (96 KB) IN ceiling.
//
// For a full-comment .tbn (i40 / i51) the file can exceed 96 KB; only the
// first 96 KB are deposited here (the assembler-facing prefix fits in that
// window).  The HGTHD/HLOAD path (hookHGTHD / hookHLOAD) serves the
// registered "IN" file and the two-phase load in load_in_file copies only
// the prefix bytes into the IN pages — so the pre-deposit just needs to
// seed the initial head that the first HLOAD writes.  Any bytes past the
// ceiling are not deposited; they remain in the registered NamedFile and
// the HLOAD handler copies only what the caller's C/DE registers request.
func (h *Hardware) depositInPages(data []byte) {
	const inMaxBytes = 6 * pageSize // 96 KB ceiling
	if len(data) > inMaxBytes {
		data = data[:inMaxBytes]
	}
	for i, b := range data {
		page := inBasePage + i/pageSize
		offset := i % pageSize
		h.ram[page][offset] = b
	}
}

// depositPage copies data into a single physical page (offset 0).
func (h *Hardware) depositPage(page int, data []byte) {
	h.depositPageAt(page, 0, data)
}

// depositPageAt copies data into a single physical page starting at the
// given offset (the &3FFF residue of the payload's HLOAD destination —
// e.g. &0400 for the zx0 payload loading at &8400).
func (h *Hardware) depositPageAt(page, offset int, data []byte) {
	if offset+len(data) > pageSize {
		panic(fmt.Sprintf("depositPageAt: offset %d + data (%d B) exceeds pageSize", offset, len(data)))
	}
	copy(h.ram[page][offset:], data)
}

// depositPagesFrom copies data into consecutive physical pages starting at
// startPage, splitting on 16 KB boundaries.  Mirrors how SAM maps a binary
// org'd at &8000 across sections C+D (and beyond) under HMPR: the first
// 16 KB land in startPage (section C = &8000..&BFFF), the next 16 KB in
// startPage+1 (section D = &C000..&FFFF auto-paged by HMPR+1), etc.  Used
// for the BUILD_TESTS ("test") assembler variant, whose binary exceeds one
// 16 KB page (it ends around &BA89 = ~15 KB post-budget-relief, but the
// general splitter keeps working if it grows back across &C000).
func (h *Hardware) depositPagesFrom(startPage int, data []byte) {
	for off := 0; off < len(data); off += pageSize {
		end := off + pageSize
		if end > len(data) {
			end = len(data)
		}
		copy(h.ram[startPage+off/pageSize][:], data[off:end])
	}
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
//	assemblerBin: contents of assembler-prod.bin (loaded at &8000)
//	enctabData:   contents of enctab.enc (pre-deposited in page 4)
//	inData:       contents of a .tbn file (pre-deposited in pages 7-12)
//	timeout:      wall-clock limit
//
// Returns a Result describing the outcome.
func Run(assemblerBin, enctabData, inData []byte, timeout time.Duration) Result {
	return RunWithFiles(assemblerBin, enctabData, inData, nil, timeout)
}

// RunWithFiles is like Run but additionally serves a set of named SAMDOS
// files via HGTHD+HLOAD.  This is what the BUILD_TESTS variant needs: at
// boot it HLOADs "test_mem" (page 13) and "p14" (page 14) before running
// the self-test suite.  Each NamedFile's content is also pre-deposited into
// its TargetPage so that even if the HLOAD path differs, the data is present.
func RunWithFiles(assemblerBin, enctabData, inData []byte, files []NamedFile, timeout time.Duration) Result {
	r, _, _ := RunConfig(Config{
		AssemblerBin: assemblerBin, EnctabData: enctabData, InData: inData,
		Files: files, Timeout: timeout,
	})
	return r
}

// Config bundles all inputs + optional diagnostics knobs for a harness run.
type Config struct {
	AssemblerBin, EnctabData, InData []byte
	Files                            []NamedFile
	Timeout                          time.Duration

	// TraceLo/TraceHi: if TraceHi>TraceLo, capture an ordered register
	// snapshot at every step whose PC is in [TraceLo,TraceHi).
	TraceLo, TraceHi uint16

	// TrigPC: if non-zero, capture FirstTrig (the register snapshot the
	// first time PC == TrigPC) and TrigBacktrace (the 200-PC ring at that
	// moment).  Useful for "who jumped into fail?".
	TrigPC uint16

	// TrigDumpAddrs/TrigDumpLen: logical addresses to read (honouring the
	// paging state at the trigger) for TrigDumpLen bytes each, captured the
	// moment TrigPC is reached.  Surfaces in TrigResult.Dump.
	TrigDumpAddrs []uint16
	TrigDumpLen   int

	// File-I/O error-longjmp model (i183) — see the package doc.
	//
	// HkspAddr is the logical address of the emulated SAMDOS (hksp)
	// error-handler vector.  When zero (the default), no handler is installed
	// and every file-I/O error takes the default-halt path — matching the
	// real dispatcher, which zeros (hksp) on each hook entry, and the current
	// assembler, which installs no handler.  When non-zero, a file-I/O error
	// reads the 2-byte LE value at this address (honouring current paging); a
	// non-zero value models derr's `ld sp,(hksp); ret` longjmp into the
	// installed handler.
	HkspAddr uint16

	// FailHGTHD forces HGTHD to report file-not-found for these SAMDOS names
	// even when the file is registered/served — lets a test exercise the
	// error path (and i25's handler) without physically omitting a payload.
	FailHGTHD map[string]bool

	// FailHSAVE forces the HSAVE hook to report an I/O error (disk full /
	// name conflict), triggering the same derr longjmp as a read failure.
	FailHSAVE bool

	// StrictFileNotFound makes an unknown/unserved HGTHD file (one the harness
	// was never given) a faithful file-not-found error — the (hksp) longjmp,
	// or a cause-naming halt with no handler — instead of the legacy silent
	// no-op.  Default false: the legacy behaviour is preserved, because many
	// decode-focused tests deliberately boot the prod assembler with a minimal
	// file set (its unconditional d15/sd13/zx013 loads are harmless no-ops for
	// fixtures that need neither the disassembler nor the sysreg tables).  An
	// injected FailHGTHD/FailHSAVE failure always errors regardless of this flag.
	StrictFileNotFound bool
}

// TrigResult holds the state captured when Config.TrigPC was first reached.
type TrigResult struct {
	Hit        bool
	Regs       RegSnapshot
	Backtrace  []uint16
	StepAtTrig uint64
	Dump       map[uint16][]byte
}

// RunConfig is the full-control entry point.  It returns the Result plus the
// windowed PC trace (empty unless Config.TraceHi>Config.TraceLo).
func RunConfig(cfg Config) (Result, []RegSnapshot, TrigResult) {
	hw := newHardware()
	hw.traceLo, hw.traceHi = cfg.TraceLo, cfg.TraceHi
	hw.trigPC = cfg.TrigPC
	hw.trigDumpAddrs = cfg.TrigDumpAddrs
	hw.trigDumpLen = cfg.TrigDumpLen
	hw.hkspAddr = cfg.HkspAddr
	hw.failHGTHD = cfg.FailHGTHD
	hw.failHSAVE = cfg.FailHSAVE
	hw.strictFileNotFound = cfg.StrictFileNotFound
	res := runOn(hw, cfg.AssemblerBin, cfg.EnctabData, cfg.InData, cfg.Files, cfg.Timeout)
	return res, hw.windowTrace, TrigResult{
		Hit:        hw.trigHit,
		Regs:       hw.trigRegs,
		Backtrace:  hw.trigBT,
		StepAtTrig: hw.trigStep,
		Dump:       hw.trigDump,
	}
}

func runOn(hw *Hardware, assemblerBin, enctabData, inData []byte, files []NamedFile, timeout time.Duration) Result {

	// Register named files and pre-deposit their content into the target
	// pages (belt-and-braces: HLOAD also copies, but pre-deposit guarantees
	// presence regardless of HLOAD ordering).
	hw.files = make(map[string]*NamedFile)
	for i := range files {
		f := files[i]
		hw.files[f.Name] = &files[i]
		if f.TargetPage >= 0 && f.TargetPage < numPages {
			hw.depositPageAt(f.TargetPage, f.LoadOffset, f.Content)
		}
	}

	// Auto-register the IN file ("IN" in the SAMDOS catalogue) so that a
	// re-HLOAD of IN actually re-deposits the .tbn into pages 7.. — exactly
	// as real SAMDOS re-reads it from disk.  Without this, the BUILD_TESTS
	// reader self-test (which stamps a synthetic blob over IN page 7) would
	// leave page 7 corrupted, because main_assemble's load_in_file HLOAD
	// would be a no-op.  (Registered only if the caller hasn't already
	// supplied an explicit "IN".)
	if _, ok := hw.files["IN"]; !ok && len(inData) > 0 {
		inFile := &NamedFile{Name: "IN", Content: inData, TargetPage: inBasePage}
		hw.files["IN"] = inFile
	}

	// Register "enctab.enc" so that HGTHD populates &4B50+34/35 with its
	// true length.  After i7 phase A, load_enctab reads the DIFA header
	// just like load_payload_generic, so the harness must supply it.
	// Pre-deposit is still done below (belt-and-braces); HLOAD remains a
	// no-op because the data is already in page 4.
	if _, ok := hw.files["enctab.enc"]; !ok && len(enctabData) > 0 {
		enctabFile := &NamedFile{Name: "enctab.enc", Content: enctabData, TargetPage: enctabPage}
		hw.files["enctab.enc"] = enctabFile
	}

	// Deposit assembler binary starting at section C (page 2 = HMPR_DEFAULT
	// low-5).  The binary is built for org &8000; its first 16 KB load into
	// physical page 2 (section C) when HMPR = hmprDefault, and any overflow
	// past &C000 spills into page 3 (section D) and beyond — exactly how SAM
	// maps it under HMPR/HMPR+1.  The BUILD_TESTS test variant exceeds one
	// 16 KB page, so we must split across consecutive pages rather than
	// panic in depositPage.
	hw.depositPagesFrom(2, assemblerBin)

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
			rawName := string(hw.ram[uifaPage][uifaOffset+1 : uifaOffset+11])
			name := strings.TrimRight(rawName, " ")

			// Look up the named file in the registry; this becomes the
			// "current file" the next HLOAD will deposit.  HGTHD also
			// populates the DIFA geometry at &4B50+34..36 (page count +
			// length-mod-16K with bit 15 set) exactly as real SAMDOS does.
			//
			// Fallback for the legacy IN path: if no registered file
			// matches but the name starts with "IN", use the pre-deposited
			// inData geometry computed above.  This keeps the prod harness
			// behaviour byte-identical for callers using the old Run().
			copyPage := uifaPage
			copyOffset := uifaCopyAddr & 0x3FFF
			injectedFail := hw.failHGTHD[name]
			if f, ok := hw.files[name]; ok && !injectedFail {
				hw.currentFile = f
				flen := len(f.Content)
				pages := uint8(flen / pageSize)
				lenMod := uint16(flen % pageSize)
				if flen > 0 && lenMod == 0 {
					pages--
					lenMod = pageSize
				}
				hw.ram[copyPage][copyOffset+34] = pages
				lenWithFlag := lenMod | 0x8000
				hw.ram[copyPage][copyOffset+35] = uint8(lenWithFlag)
				hw.ram[copyPage][copyOffset+36] = uint8(lenWithFlag >> 8)
			} else if strings.HasPrefix(name, "IN") && !injectedFail {
				hw.currentFile = nil
				hw.ram[copyPage][copyOffset+34] = inFilePages
				lenWithFlag := inFileLenMod16K | 0x8000
				hw.ram[copyPage][copyOffset+35] = uint8(lenWithFlag)
				hw.ram[copyPage][copyOffset+36] = uint8(lenWithFlag >> 8)
			} else if name == "enctab.enc" && !injectedFail {
				// Defensive dead path: enctab.enc is auto-registered whenever
				// enctabData is non-empty (the only real configuration), so it
				// is served above.  Reaching here means the caller passed empty
				// enctab data; keep the legacy no-op rather than longjmp.
				hw.currentFile = nil
			} else {
				// FILE NOT FOUND — an unknown file the harness was never given
				// (e.g. "sd13" / sysreg_data with -sysreg-data omitted), or an
				// injected failure (Config.FailHGTHD).  On real SAMDOS gtfle
				// fails and the hook calls derr, which longjmps via (hksp).
				//
				// Always record the name for diagnostics.  The faithful longjmp
				// fires when the failure is injected, or when the caller opts
				// into StrictFileNotFound.  Otherwise preserve the legacy silent
				// no-op (the page stays zero/garbage), so the many decode-focused
				// tests that boot the prod assembler with a minimal file set keep
				// working — their unconditional d15/sd13/zx013 loads are harmless
				// for fixtures that exercise neither the disassembler nor the
				// sysreg tables.
				if hw.unservedSeen == nil {
					hw.unservedSeen = make(map[string]bool)
				}
				if !hw.unservedSeen[name] {
					hw.unservedSeen[name] = true
					hw.unservedFiles = append(hw.unservedFiles, name)
				}
				hw.currentFile = nil
				if injectedFail || hw.strictFileNotFound {
					verb := "HGTHD file-not-found"
					if injectedFail {
						verb = "HGTHD injected failure"
					}
					hw.triggerFileIOError(fmt.Sprintf("%s: %q", verb, name), true)
				}
			}

		case hookHLOAD:
			// HLOAD: copy the current file (selected by the preceding HGTHD)
			// into the physical page currently mapped at section C, at the
			// destination the caller supplies in HL.  The COMET trampoline
			// sets HMPR := target page and HL := the section-C destination
			// (&8000 for every payload except the zx0 payload's &8400)
			// before the RST 8, so HLOAD writes from section C = physical
			// page (HMPR & 0x1F) at offset (HL & &3FFF).  HL is live here:
			// the fake-ROM RST-8 stub restores it before the OUT dispatch.
			//
			// This is faithful to HLOAD's *effect*.  When currentFile is nil
			// (legacy IN / enctab path), the data was pre-deposited into the
			// fixed physical pages, so this is a no-op — preserving the old
			// harness behaviour exactly.
			if hw.currentFile != nil {
				// Deposit across consecutive physical pages starting at the
				// section-C page (HMPR & 0x1F) + the HL offset, mirroring
				// SAMDOS's ctas auto-paging which advances HMPR every
				// &C000-boundary crossing (samdos/src/c.s:354-369).  This
				// makes a re-HLOAD of a file faithfully restore ALL its
				// pages — critical for the BUILD_TESTS reader self-test,
				// which clobbers IN page 7 and relies on main_assemble's
				// load_in_file re-reading the real IN file from disk to
				// restore it.
				//
				// Honour the caller's requested byte count from the live
				// C and DE registers (trampoline_body in src/trampoline.asm
				// passes C/DE unmodified from the caller to the RST 8; B is
				// consumed for HMPR before the RST 8).  Contract:
				//   C  = pages count  (samdos-file-io.md trampoline contract)
				//   DE = length mod 16K (with SAMDOS marker bit already
				//        cleared by the Z80 caller before the TRAMPOLINE_DST
				//        call — load_in_file masks bit 7 of D via `and &7F`)
				// n = C*16384 + DE.  For a two-phase prefix load this is
				// smaller than len(content), so we copy only the prefix bytes;
				// a >96 KB full file would otherwise wrap dstPage into
				// unrelated physical pages.  For normal whole-file loads n ==
				// len(content) (HGTHD deposits the exact file length into DIFA
				// and the caller passes it unchanged), so min(n, len(content))
				// is a no-op for those callers.
				reqC := int(hw.cpu.BC.Lo)
				reqDE := int(hw.cpu.DE.U16())
				n := reqC*pageSize + reqDE
				content := hw.currentFile.Content
				if n < len(content) {
					content = content[:n]
				}
				dstPage := int(hw.hmpr & 0x1F)
				dstOff := int(hw.cpu.HL.U16()) & 0x3FFF
				for i, b := range content {
					pg := (dstPage + (dstOff+i)/pageSize) & 0x1F
					hw.ram[pg][(dstOff+i)%pageSize] = b
				}
				hw.currentFile = nil
			}

		case hookHSAVE:
			// Injected HSAVE failure (Config.FailHSAVE): model the real
			// disk-full / name-conflict error, which longjmps via derr →
			// (hksp) exactly like a failed read (samdos-file-io.md
			// "Critical caveat").  Take the longjmp/default-halt before any
			// OUT capture so the failure surfaces as such.
			if hw.failHSAVE {
				hw.triggerFileIOError("HSAVE injected failure (disk full / name conflict)", false)
				break
			}

			// HSAVE: capture OUT bytes from the paged OUT buffer.
			// The assembler has populated UIFA[31..36]:
			//   UIFA[31]: start page (= 5)
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
	var steps uint64

	// Trap detection: if the CPU enters the ROM 0xFF fill region (the fake
	// ROM is all 0xFF except the &0008 stub), it has jumped to garbage.  The
	// RST 38 (0xFF opcode) spin at &0038 is the canonical "fell into garbage"
	// symptom.  Detect a sustained spin there and bail out fast with a
	// register snapshot — far more useful than burning the full timeout.
	trapSpin := 0
	const trapSpinThreshold = 64

	for time.Now().Before(deadline) {
		hw.recordPC(cpu.PC)
		if hw.traceHi > hw.traceLo && cpu.PC >= hw.traceLo && cpu.PC < hw.traceHi {
			hw.windowTrace = append(hw.windowTrace, hw.snapshot())
		}
		if hw.trigPC != 0 && cpu.PC == hw.trigPC && !hw.trigHit {
			hw.trigHit = true
			hw.trigRegs = hw.snapshot()
			hw.trigBT = hw.last200PC()
			hw.trigStep = steps
			if len(hw.trigDumpAddrs) > 0 {
				hw.trigDump = make(map[uint16][]byte)
				for _, a := range hw.trigDumpAddrs {
					b := make([]byte, hw.trigDumpLen)
					for i := 0; i < hw.trigDumpLen; i++ {
						b[i] = hw.Get(a + uint16(i))
					}
					hw.trigDump[a] = b
				}
			}
		}
		// Detect the &0038 garbage-trap spin (PC stuck in fake-ROM 0xFF land).
		if cpu.PC == 0x0038 {
			trapSpin++
			if trapSpin >= trapSpinThreshold {
				exitReason = fmt.Sprintf(
					"TRAP: PC spinning at &0038 (jumped into 0xFF fake-ROM) after %d steps", steps)
				if hint := hw.unservedFileHint(); hint != "" {
					exitReason += "; " + hint
				}
				break
			}
		} else {
			trapSpin = 0
		}
		cpu.Step()
		steps++
		if hw.pendingFault != "" {
			// A file-I/O hook took the default error path (no (hksp) handler
			// installed).  Stop here — before the synthetic RST-8 stub's
			// trailing RET would resume the assembler — so the failure
			// surfaces at its true cause rather than as a downstream trap.
			exitReason = hw.pendingFault
			break
		}
		if cpu.HALT {
			exitReason = fmt.Sprintf("HALT at PC=%04X", cpu.PC)
			break
		}
	}
	if exitReason == "" {
		exitReason = fmt.Sprintf("timeout after %v (%d steps)", timeout, steps)
		if hint := hw.unservedFileHint(); hint != "" {
			exitReason += "; " + hint
		}
	}

	faultRegs := hw.snapshot()

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
		FaultRegs:      faultRegs,
		Steps:          steps,
		UnservedFiles:  hw.unservedFiles,
	}
}

// triggerFileIOError models SAMDOS's derr longjmp (samdos/src/d.s:430) for a
// failed file-I/O hook.  It reads the emulated (hksp) handler vector and
// either longjmps into the installed handler or records a default-halt fault:
//
//   - hkspAddr != 0 and the 2-byte LE value there != 0: an installed handler.
//     Set SP := (hksp); the synthetic RST-8 stub's trailing RET then pops the
//     handler entry from [(hksp)] into PC — reproducing `ld sp,(hksp); ret`.
//   - otherwise (no handler — the dispatcher zeros (hksp) on entry and the
//     current assembler installs none): derr1's default path, which on real
//     hardware prints the error and pops into BASIC's error handler (a crash
//     for the assembler's di/halt loop).  Surface it as a clean halt whose
//     ExitReason names the cause.
//
// reason is the cause text for the default-halt message; includeHint adds the
// unserved-file remedy (relevant for file-not-found, not for an HSAVE failure).
func (h *Hardware) triggerFileIOError(reason string, includeHint bool) {
	var hksp uint16
	if h.hkspAddr != 0 {
		lo := uint16(h.Get(h.hkspAddr))
		hi := uint16(h.Get(h.hkspAddr + 1))
		hksp = lo | hi<<8
	}
	if hksp != 0 {
		// Installed handler — longjmp.  The stub's RET (executed after this
		// hook returns) pops [(hksp)] into PC.
		h.cpu.SP = hksp
		return
	}
	msg := "SAMDOS file-I/O error longjmp ((hksp)=0, default error path): " + reason
	if includeHint {
		if hint := h.unservedFileHint(); hint != "" {
			msg += "; " + hint
		}
	}
	h.pendingFault = msg
}

// unservedFileHint returns a human-readable diagnostic naming any SAMDOS
// files HGTHD requested that the harness could not serve, with the common
// remedy.  Empty string when every requested file was served.
func (h *Hardware) unservedFileHint() string {
	if len(h.unservedFiles) == 0 {
		return ""
	}
	remedy := ""
	for _, n := range h.unservedFiles {
		if n == "sd13" {
			// The prod assembler unconditionally HLOADs sd13 (sysreg
			// lookup data) into page 13 at boot — see
			// src/loader.asm::load_page13_payload + the unconditional
			// call in src/assembler.asm.  Without it the page-13 sysreg
			// matcher is empty and the first sysreg/dc/tlbi/pstate
			// operand traps.
			remedy = " (supply build/sysreg_data.bin via -sysreg-data: " +
				"the prod assembler always HLOADs \"sd13\" into page 13 at boot)"
		}
	}
	return fmt.Sprintf("HGTHD requested file(s) the harness could not serve: %v%s; "+
		"each is a silent HLOAD no-op (leaving the page empty) unless "+
		"StrictFileNotFound is set, where it longjmps via (hksp)",
		h.unservedFiles, remedy)
}
