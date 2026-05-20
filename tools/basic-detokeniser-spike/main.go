// basic-detokeniser-spike — tokenised BASIC → text via ROM emulation.
//
// Stage 1 (probe) verified: SAM's EDIT key (0x07) invokes EDKY at
// rom-disasm:0x0379, which prints the addressed line through the "R"
// channel into ELINE in source-text form. Approach A from the spec is
// viable — we drive EDIT per line and read ELINE up to the first 0x0D.
//
// Two modes (mutually exclusive — required):
//
//	--mgt <path> --filename <name>  Stage 2: read a tokenised .mgt,
//	                                memory-poke PROG, extract every
//	                                line via EDIT, write text to --out.
//	--probe                         Stage 1: hardcoded 3-line test
//	                                seeded by typing into the editor;
//	                                dumps ELINE bytes for inspection.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/koron-go/z80"
	"github.com/petemoore/samfile/v3"
	"github.com/petemoore/samfile/v3/sambasic"
)

// canonicalNumericVars is the 92-byte NumericVars area as a real SAM
// ROM SAVE produces it for a freshly-initialised program. 46 bytes of
// 0xFF (the 23 letter-pointer pairs marking "no variable defined")
// followed by PSVTAB content. Copied from the forward spike, where it
// was validated byte-for-byte against a real SAM SAVE.
var canonicalNumericVars = []byte{
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0x19, 0x00, 0x03, 0x00, 0xFF, 0xFF, 0x02, 0x08,
	0x00, 0x6F, 0x73, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x02, 0xFF, 0xFF, 0x72, 0x67, 0x00, 0x00, 0xC0,
	0x00, 0x00, 0x02, 0x08, 0x00, 0x6F, 0x73, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x02, 0xFF, 0xFF, 0x72,
	0x67, 0x00, 0x00, 0x00, 0x01, 0x00,
}

// SAM Coupé hardware emulation — same shape as the forward spike. See
// tools/basic-emulator-spike/main.go for the full prose explanation of
// paging, the FLAGS/LASTK intercept, Snapshot/Restore, etc.
type Hardware struct {
	rom  []byte
	ram  [32][16384]byte
	lmpr uint8
	hmpr uint8
	vmpr uint8

	cpu      *z80.CPU
	keyQueue []byte
}

func newHardware(rom []byte) *Hardware { return &Hardware{rom: rom} }

func (h *Hardware) resolve(addr uint16) (page uint8, isROM bool, romHalf uint8) {
	section := addr >> 14
	switch section {
	case 0:
		if h.lmpr&0x20 == 0 {
			return 0, true, 0
		}
		return h.lmpr & 0x1F, false, 0
	case 1:
		return (h.lmpr + 1) & 0x1F, false, 0
	case 2:
		return h.hmpr & 0x1F, false, 0
	case 3:
		if h.lmpr&0x40 != 0 {
			return 0, true, 1
		}
		return (h.hmpr + 1) & 0x1F, false, 0
	}
	return 0, false, 0
}

func (h *Hardware) Get(addr uint16) uint8 {
	offset := int(addr & 0x3FFF)
	page, isROM, romHalf := h.resolve(addr)
	if isROM {
		if romHalf == 0 {
			return h.rom[offset]
		}
		return h.rom[16384+offset]
	}
	v := h.ram[page][offset]
	switch addr {
	case sysFLAGS:
		if len(h.keyQueue) > 0 {
			return v | 0x20
		}
	case sysLASTK:
		if len(h.keyQueue) > 0 {
			return h.keyQueue[0]
		}
	}
	return v
}

func (h *Hardware) Set(addr uint16, value uint8) {
	offset := int(addr & 0x3FFF)
	page, isROM, _ := h.resolve(addr)
	if isROM {
		return
	}
	h.ram[page][offset] = value
	if addr == sysFLAGS && len(h.keyQueue) > 0 && value&0x20 == 0 {
		h.keyQueue = h.keyQueue[1:]
	}
}

func (h *Hardware) In(addr uint8) uint8 {
	switch addr {
	case 0xFA:
		return h.lmpr
	case 0xFB:
		return h.hmpr
	case 0xFC:
		return h.vmpr
	}
	return 0xFF
}

func (h *Hardware) Out(addr uint8, value uint8) {
	switch addr {
	case 0xFA:
		h.lmpr = value
	case 0xFB:
		h.hmpr = value
	case 0xFC:
		h.vmpr = value
	}
}

// SAM sysvars. Addresses verbatim from rom-disasm:869-900 (PROG-area
// pointers, with the "*P" page-byte companions used by REL PAGE FORM)
// and rom-disasm:1140-1143 (RAMTOPP/RAMTOP/PRAMTP/LASTPAGE).
const (
	// BASIC pointer pairs: page byte at NAMEP, 16-bit offset at NAME.
	sysSAVARSP  = 0x5A81
	sysSAVARS   = 0x5A82
	sysNUMENDP  = 0x5A84
	sysNUMEND   = 0x5A85
	sysNVARSP   = 0x5A87
	sysNVARS    = 0x5A88
	sysDATADD   = 0x5A8B
	sysWKENDP   = 0x5A8D
	sysWKEND    = 0x5A8E
	sysWORKSPP  = 0x5A90
	sysWORKSP   = 0x5A91
	sysELINEP   = 0x5A93
	sysELINE    = 0x5A94
	sysCHADP    = 0x5A96
	sysCHAD     = 0x5A97
	sysKCURP    = 0x5A99
	sysKCUR     = 0x5A9A
	sysNXTLINEP = 0x5A9C
	sysNXTLINE  = 0x5A9D
	sysPROGP    = 0x5A9F
	sysPROG     = 0x5AA0

	// Paging / RAM ceiling.
	sysLASTPAGE = 0x5CB0
	sysRAMTOPP  = 0x5CB1
	sysRAMTOP   = 0x5CB2
	sysPRAMTP   = 0x5CB4

	// ALLOCT base; 32 bytes, one per physical page (rom-disasm:1253).
	allocTableBase uint16 = 0x5100

	// Editor / keyboard sysvars (unchanged from previous block).
	sysLASTK  = 0x5C08
	sysERRNR  = 0x5C3A
	sysFLAGS  = 0x5C3B
	sysSTKEND = 0x5C65 // SAM-specific STKEND (different from Spectrum's)
)

func peekRAM(hw *Hardware, addr uint16) uint8 {
	page, isROM, _ := hw.resolve(addr)
	if isROM {
		return 0xFF
	}
	return hw.ram[page][addr&0x3FFF]
}

func peekRAM16(hw *Hardware, addr uint16) uint16 {
	return uint16(peekRAM(hw, addr)) | uint16(peekRAM(hw, addr+1))<<8
}

func pokeRAM(hw *Hardware, addr uint16, v uint8) {
	page, isROM, _ := hw.resolve(addr)
	if isROM {
		log.Fatalf("pokeRAM(%04X) lands in ROM", addr)
	}
	hw.ram[page][addr&0x3FFF] = v
}

func pokeRAM16(hw *Hardware, addr uint16, v uint16) {
	pokeRAM(hw, addr, uint8(v))
	pokeRAM(hw, addr+1, uint8(v>>8))
}

// pokeRAMPage writes directly to a specific physical RAM page,
// bypassing the LMPR/HMPR resolution that pokeRAM uses. The
// multi-page loader uses this to stage program bytes across pages
// without rotating HMPR mid-load. The masks defensively normalise
// page into 0..31 and offset into 0..0x3FFF; the loader passes
// already-normal values via the pos type but the masking is cheap.
func pokeRAMPage(hw *Hardware, page uint8, offset uint16, v uint8) {
	hw.ram[page&0x1F][offset&0x3FFF] = v
}

// setSysvarPair stores a (page, offset) in REL PAGE FORM at the given
// sysvar addresses. The page byte goes at pageAddr; the offset is
// encoded into section-C form (0x8000 | low_14_bits) and stored
// 16-bit-little-endian at offsetAddr. Per the ROM convention
// established by UNSTLEN (rom-disasm:14773-14786), the offset's top
// bit is always set when storing an address (as opposed to a length).
func setSysvarPair(hw *Hardware, pageAddr, offsetAddr uint16, p pos) {
	pokeRAM(hw, pageAddr, p.page)
	pokeRAM16(hw, offsetAddr, 0x8000|(p.offset&0x3FFF))
}

// checkFits returns nil if a program of length progLen will fit in
// physical RAM on a machine with the given PRAMTP, given the trailer
// shift (1024 bytes, matching the current loader's memmove window)
// and the ROM's required 150-byte WKEND headroom (rom-disasm:7152).
//
// PROG starts at page 1, offset 0x1CD5, so the usable region is
// pages 1..PRAMTP minus the section-A page 0 and the system area
// in page 1 below PROG.
func checkFits(progLen int, pramtp uint8) error {
	const (
		progPageStart    = uint8(1)
		progOffsetInPage = uint16(0x1CD5) // 0x9CD5 in section-C form
		trailerShiftLen  = 1024
		wkendHeadroom    = 150
	)
	totalNeeded := progLen + trailerShiftLen + wkendHeadroom
	totalAvailable := (int(pramtp)+1)*0x4000 -
		int(progPageStart)*0x4000 -
		int(progOffsetInPage)
	if totalNeeded > totalAvailable {
		return fmt.Errorf("program does not fit in BASIC pages: "+
			"len=%d shift=%d headroom=%d need=%d available=%d "+
			"(PRAMTP=%02X, pages %d..%d)",
			progLen, trailerShiftLen, wkendHeadroom,
			totalNeeded, totalAvailable, pramtp,
			progPageStart, pramtp)
	}
	return nil
}

type Snapshot struct {
	RAM       [32][16384]byte
	LMPR      uint8
	HMPR      uint8
	VMPR      uint8
	CPUStates z80.States
	HALT      bool
	Interrupt *z80.Interrupt
}

func (h *Hardware) Snapshot() Snapshot {
	return Snapshot{
		RAM: h.ram, LMPR: h.lmpr, HMPR: h.hmpr, VMPR: h.vmpr,
		CPUStates: h.cpu.States, HALT: h.cpu.HALT, Interrupt: h.cpu.Interrupt,
	}
}

func (h *Hardware) Restore(s Snapshot) {
	h.ram = s.RAM
	h.lmpr = s.LMPR
	h.hmpr = s.HMPR
	h.vmpr = s.VMPR
	h.cpu.States = s.CPUStates
	h.cpu.HALT = s.HALT
	h.cpu.Interrupt = s.Interrupt
	h.keyQueue = nil
}

func bootToMAINELP(hw *Hardware, cpu *z80.CPU, maxSteps uint64) error {
	const skipBannerPC uint16 = 0x0F75
	const skipBannerTo uint16 = 0x0F78
	const readyPC uint16 = 0x0E8A
	for step := uint64(0); step < maxSteps; step++ {
		if cpu.PC == skipBannerPC {
			cpu.PC = skipBannerTo
		}
		cpu.Step()
		if cpu.HALT {
			return fmt.Errorf("HALT at PC=%04X after %d steps before MAINELP", cpu.PC, step+1)
		}
		if cpu.PC == readyPC {
			return nil
		}
	}
	return fmt.Errorf("step budget (%d) exhausted before MAINELP (PC=%04X)", maxSteps, cpu.PC)
}

// driveAndSettle injects a byte sequence through FLAGS/LASTK and steps
// until the queue is drained plus an idle window for the ROM to react.
// Bails on HALT or step budget.
func driveAndSettle(hw *Hardware, cpu *z80.CPU, keys []byte, stepBudget uint64, idleAfterDrain uint64) error {
	hw.keyQueue = append([]byte(nil), keys...)
	pokeRAM(hw, sysERRNR, 0)

	queueDrainedAt := uint64(0)
	for i := uint64(0); i < stepBudget; i++ {
		cpu.Step()
		if cpu.HALT {
			return fmt.Errorf("HALT at PC=%04X step %d", cpu.PC, i+1)
		}
		if len(hw.keyQueue) == 0 {
			if queueDrainedAt == 0 {
				queueDrainedAt = i + 1
			}
			if i+1-queueDrainedAt >= idleAfterDrain {
				return nil
			}
		}
	}
	return fmt.Errorf("step budget (%d) exhausted (PC=%04X)", stepBudget, cpu.PC)
}

// typeLineAndCommit drives the editor through one full line via the
// FLAGS/LASTK channel and waits for ERROR2 (the "OK" unwind after
// INSERTLN). Used by --probe mode to seed PROG via the editor's normal
// codepath.
func typeLineAndCommit(hw *Hardware, cpu *z80.CPU, line string, stepBudget uint64) error {
	const error2PC = 0x37CE
	hw.keyQueue = append([]byte(line), '\r')
	pokeRAM(hw, sysERRNR, 0)
	for i := uint64(0); i < stepBudget; i++ {
		cpu.Step()
		if cpu.PC == error2PC {
			if err := peekRAM(hw, sysERRNR); err != 0 {
				return fmt.Errorf("ERRNR=0x%02X after line %q", err, line)
			}
			return nil
		}
		if cpu.HALT {
			return fmt.Errorf("HALT during line %q", line)
		}
	}
	return fmt.Errorf("step budget exhausted typing %q", line)
}

// loadProgViaPoke is Stage 2's load step. Takes the tokenised program
// section (lines + 0xFF sentinel, as produced by sambasic.File.ProgBytes)
// and arranges RAM + BASIC-area sysvars so the ROM sees the equivalent
// of a freshly-LOADed program — while preserving the editor's internal
// state (KCUR, NXTLINE, etc.) that the boot init set up.
//
// Approach: memmove all RAM bytes from old-NVARS upward by `delta`
// (= len(progBytes) - 1, since post-boot PROG..NVARS is just the 1-byte
// FF sentinel). Then bump every BASIC-area sysvar pointer (except PROG
// and DATADD) by delta. Then write progBytes at PROG.
//
// Memory layout produced (matching docs/notes/sam-basic-save-format.md):
//
//	PROG    ─── progBytes (lines + 0xFF sentinel)
//	NVARS   ─── canonical NumericVars (92 bytes, inherited from boot init)
//	NUMEND  ─── gap (512 bytes, inherited from boot init)
//	SAVARS  ─── 0 bytes saved vars (empty for fresh state)
//	ELINE/WORKSP/etc — shifted up but logically unchanged
//
// PROG itself is unchanged (sysPROG retains its fixed boot value).
func loadProgViaPoke(hw *Hardware, progBytes []byte) {
	const (
		progPageStart    = uint8(1)
		progOffsetInPage = uint16(0x1CD5) // 0x9CD5 in section-C form
		trailerShiftLen  = 1024
	)

	// Sanity check: post-boot relationship must hold. Unchanged from
	// the original loader at main.go:307-311.
	prog := peekRAM16(hw, sysPROG)
	oldNVARS := peekRAM16(hw, sysNVARS)
	if oldNVARS != prog+1 {
		log.Fatalf("expected post-boot NVARS=PROG+1, got NVARS=%04X PROG=%04X",
			oldNVARS, prog)
	}

	// Snapshot the boot-time offsets of every downstream sysvar from
	// NVARS so we can preserve those relative offsets in the new
	// layout. This is the equivalent of the original loader's "bump
	// every sysvar by delta" pattern, generalised to (page, offset).
	type sysvarSpec struct {
		pageAddr, offsetAddr uint16
		deltaFromNVARS       int
	}
	// Deltas are signed: NXTLINE in particular is BELOW NVARS at boot
	// (NXTLINE=PROG=NVARS-1), so its delta is -1. Compute as signed
	// 16-bit (cast through int16) to preserve sign across the wrap.
	signedDelta := func(addr uint16) int {
		return int(int16(peekRAM16(hw, addr) - oldNVARS))
	}
	bumpedSysvars := []sysvarSpec{
		{sysNVARSP, sysNVARS, 0},
		{sysNUMENDP, sysNUMEND, signedDelta(sysNUMEND)},
		{sysSAVARSP, sysSAVARS, signedDelta(sysSAVARS)},
		{sysWKENDP, sysWKEND, signedDelta(sysWKEND)},
		{sysWORKSPP, sysWORKSP, signedDelta(sysWORKSP)},
		{sysELINEP, sysELINE, signedDelta(sysELINE)},
		{sysCHADP, sysCHAD, signedDelta(sysCHAD)},
		{sysKCURP, sysKCUR, signedDelta(sysKCUR)},
		{sysNXTLINEP, sysNXTLINE, signedDelta(sysNXTLINE)},
	}

	// Size guard: bail before any writes if the program won't fit.
	pramtp := peekRAM(hw, sysPRAMTP)
	if err := checkFits(len(progBytes), pramtp); err != nil {
		log.Fatalf("%v", err)
	}

	// Step 1: paging-aware trailer shift. Walk backwards from
	// shiftLen-1 down to 0 so the source/dest overlap on small
	// programs doesn't clobber. Source reads use the paged peekRAM
	// (everything's in page 1's section-C window during the read);
	// dest writes use pokeRAMPage to land bytes in the correct
	// physical page regardless of HMPR state.
	progPos := pos{page: progPageStart, offset: progOffsetInPage}
	newNVARSPos := progPos.advance(len(progBytes))
	for i := trailerShiftLen - 1; i >= 0; i-- {
		b := peekRAM(hw, oldNVARS+uint16(i))
		dst := newNVARSPos.advance(i)
		pokeRAMPage(hw, dst.page, dst.offset, b)
	}

	// Step 2: write progBytes byte-by-byte starting at PROG. Done
	// AFTER the trailer shift so source/dest overlap is identical
	// to the original single-page loader.
	cur := progPos
	for _, b := range progBytes {
		pokeRAMPage(hw, cur.page, cur.offset, b)
		cur = cur.advance(1)
	}

	// Step 3: write all sysvar pairs in REL PAGE FORM. PROG itself is
	// unchanged value-wise, but we write the pair explicitly because
	// ROM boot doesn't initialise PROGP (verified against disasm).
	setSysvarPair(hw, sysPROGP, sysPROG, progPos)
	for _, s := range bumpedSysvars {
		p := newNVARSPos.advance(s.deltaFromNVARS)
		setSysvarPair(hw, s.pageAddr, s.offsetAddr, p)
	}

	// Step 4: ALLOCT + LASTPAGE + RAMTOP for any page claimed beyond
	// the boot-default 0..3. Compute the highest page used as the
	// new position of WKEND (the editor's high-water mark).
	wkendDelta := signedDelta(sysWKEND)
	wkendPos := newNVARSPos.advance(wkendDelta)
	maxPage := wkendPos.page
	for p := uint8(4); p <= maxPage; p++ {
		pokeRAM(hw, allocTableBase+uint16(p), 0x40)
	}
	if maxPage > 3 {
		pokeRAM(hw, sysLASTPAGE, maxPage)
		pokeRAM(hw, sysRAMTOPP, maxPage)
		pokeRAM16(hw, sysRAMTOP, 0xBFFF)
	}
}

// editLineAndCapture types the line# digits then sends EDIT key (0x07)
// to trigger EDKY, then reads ELINE up to the first 0x0D. Returns the
// ASCII text of the line (without the trailing CR). Caller is
// responsible for snapshot/restore around this call.
func editLineAndCapture(hw *Hardware, cpu *z80.CPU, lineNum uint16, stepBudget uint64) ([]byte, error) {
	const editKeyCode = 0x07
	keys := append([]byte(fmt.Sprintf("%d", lineNum)), editKeyCode)
	const idleAfter uint64 = 2_000_000 // generous; long lines take >>200k cycles to render
	if err := driveAndSettle(hw, cpu, keys, stepBudget, idleAfter); err != nil {
		return nil, fmt.Errorf("drive EDIT %d: %w", lineNum, err)
	}
	eline := peekRAM16(hw, sysELINE)
	worksp := peekRAM16(hw, sysWORKSP)
	// ELINE buffer extends to WORKSP-1. Cap at WORKSP-ELINE (the
	// editor's "live" buffer for this line). Fall back to 8KB if
	// WORKSP looks wrong (shouldn't normally happen).
	limit := int(worksp - eline)
	if limit <= 0 || limit > 0x4000 {
		limit = 8192
	}
	out := []byte{}
	for i := 0; i < limit; i++ {
		b := peekRAM(hw, eline+uint16(i))
		if b == 0x0D {
			return out, nil
		}
		out = append(out, b)
	}
	return nil, fmt.Errorf("no 0x0D found within %d bytes of ELINE (line %d)", limit, lineNum)
}

func readBasicBody(mgtPath, filename string) ([]byte, error) {
	disk, err := samfile.Load(mgtPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", mgtPath, err)
	}
	f, err := disk.File(filename)
	if err != nil {
		return nil, fmt.Errorf("find %q in %s: %w", filename, mgtPath, err)
	}
	if f.Header.Type != samfile.FT_SAM_BASIC {
		return nil, fmt.Errorf("file %q is type %v, want FT_SAM_BASIC", filename, f.Header.Type)
	}
	return f.Body, nil
}

func extractAllLines(hw *Hardware, cpu *z80.CPU, basFile *sambasic.File, stepBudget uint64) ([]string, error) {
	postLoadSnap := hw.Snapshot()
	lineNums := make([]uint16, 0, len(basFile.Lines))
	for _, ln := range basFile.Lines {
		lineNums = append(lineNums, ln.Number)
	}
	sort.Slice(lineNums, func(i, j int) bool { return lineNums[i] < lineNums[j] })

	out := make([]string, 0, len(lineNums))
	for _, n := range lineNums {
		if n == 0 {
			// EDKY at rom-disasm:03A1 explicitly RETs for line 0
			// (DON'T EDIT LINE ZERO). We skip — the sweep
			// comparator strips line-0 entries from samfile output
			// too so this difference doesn't show as DIFFER.
			continue
		}
		hw.Restore(postLoadSnap)
		text, err := editLineAndCapture(hw, cpu, n, stepBudget)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, string(text))
	}
	return out, nil
}

// runProbe is the Stage 1 path: seed by typing 3 known lines, then
// dump ELINE per line for inspection.
func runProbe(hw *Hardware, cpu *z80.CPU, maxSteps uint64) {
	seedLines := []string{"10 PRINT 1", "20 PRINT 2", "5 LET x=42"}
	fmt.Println("=== Seeding PROG by typing test lines ===")
	for _, ln := range seedLines {
		fmt.Printf("typing: %q\n", ln)
		if err := typeLineAndCommit(hw, cpu, ln, maxSteps); err != nil {
			log.Fatalf("type %q: %v", ln, err)
		}
	}
	dumpSysvars(hw, "after seed lines")

	postSeed := hw.Snapshot()
	for _, n := range []uint16{5, 10, 20} {
		hw.Restore(postSeed)
		fmt.Printf("\n=== EDIT line %d ===\n", n)
		text, err := editLineAndCapture(hw, cpu, n, maxSteps)
		if err != nil {
			fmt.Printf("  error: %v\n", err)
			continue
		}
		fmt.Printf("  → %q\n", string(text))
	}
}

// renderControlsForOutput escapes ALL low control bytes (<0x20) inside a
// captured line as `{N}` so the output file uses \n only as a line
// separator. Matches samfile basic-to-text faithful mode's rendering
// of control bytes — needed because SAM's R-channel passes them
// through raw, including 0x0A bytes embedded in REM text bodies.
func renderControlsForOutput(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 {
			fmt.Fprintf(&b, "{%d}", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func dumpSysvars(hw *Hardware, label string) {
	fmt.Printf("--- sysvars: %s ---\n", label)
	fmt.Printf("  PROG=%04X NVARS=%04X NUMEND=%04X SAVARS=%04X\n",
		peekRAM16(hw, sysPROG), peekRAM16(hw, sysNVARS),
		peekRAM16(hw, sysNUMEND), peekRAM16(hw, sysSAVARS))
	fmt.Printf("  ELINE=%04X WORKSP=%04X WKEND=%04X CHAD=%04X ERRNR=%02X\n",
		peekRAM16(hw, sysELINE), peekRAM16(hw, sysWORKSP),
		peekRAM16(hw, sysWKEND), peekRAM16(hw, sysCHAD),
		peekRAM(hw, sysERRNR))
}

// pos is a (physical_page, offset_within_page) coordinate used by the
// multi-page loader. Encapsulates page-boundary carry so callers don't
// repeat the arithmetic. offset stays in [0, 0x4000); page is the
// physical RAM page (0..31). Page wrap-around at 32 is not detected
// here — the size guard in loadProgViaPoke prevents it.
type pos struct {
	page   uint8
	offset uint16
}

func (p pos) advance(n int) pos {
	total := uint32(p.offset) + uint32(n)
	return pos{
		page:   p.page + uint8(total>>14),
		offset: uint16(total & 0x3FFF),
	}
}

func main() {
	romPath := flag.String("rom", "/Users/pmoore/git/simcoupe/Resource/samcoupe.rom", "path to samcoupe.rom (32KB)")
	maxSteps := flag.Uint64("steps", 5_000_000, "max instructions per phase")
	mgtPath := flag.String("mgt", "", "input .mgt file (Stage 2)")
	filename := flag.String("filename", "", "BASIC filename inside the .mgt (Stage 2)")
	outPath := flag.String("out", "", "output text file (Stage 2). If empty, write to stdout.")
	probeMode := flag.Bool("probe", false, "Stage 1: seed via typing, dump ELINE per line")
	flag.Parse()

	if !*probeMode && *mgtPath == "" {
		log.Fatal("either --probe or --mgt is required")
	}
	if *mgtPath != "" && *filename == "" {
		log.Fatal("--mgt requires --filename")
	}

	rom, err := os.ReadFile(*romPath)
	if err != nil {
		log.Fatalf("read ROM: %v", err)
	}
	if len(rom) != 32768 {
		log.Fatalf("ROM size: want 32768, got %d", len(rom))
	}

	hw := newHardware(rom)
	cpu := &z80.CPU{Memory: hw, IO: hw}
	hw.cpu = cpu
	cpu.PC = 0
	cpu.SP = 0xFFFF

	if err := bootToMAINELP(hw, cpu, *maxSteps); err != nil {
		log.Fatalf("boot: %v", err)
	}

	if *probeMode {
		runProbe(hw, cpu, *maxSteps)
		return
	}

	// Stage 2: read .mgt, parse body, memory-poke, extract.
	body, err := readBasicBody(*mgtPath, *filename)
	if err != nil {
		log.Fatalf("read body: %v", err)
	}
	basFile, err := sambasic.Parse(body)
	if err != nil {
		log.Fatalf("parse body: %v", err)
	}
	loadProgViaPoke(hw, basFile.ProgBytes())

	lines, err := extractAllLines(hw, cpu, basFile, *maxSteps)
	if err != nil {
		log.Fatalf("extract: %v", err)
	}

	var w *os.File = os.Stdout
	if *outPath != "" {
		w, err = os.Create(*outPath)
		if err != nil {
			log.Fatalf("create %s: %v", *outPath, err)
		}
		defer w.Close()
	}
	for _, ln := range lines {
		fmt.Fprintln(w, renderControlsForOutput(strings.TrimRight(ln, "\r")))
	}
	if *outPath != "" {
		fmt.Printf("Wrote %d lines to %s\n", len(lines), *outPath)
	}
}
