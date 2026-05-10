// basic-emulator-spike runs the SAM Coupé ROM under koron-go/z80
// from a cold reset and reports where it gets to.
//
// Purpose: empirical answer to the question "how much SAM hardware
// do we have to emulate before TOKMAIN/INSERTLN can run?". We trace
// PC trajectory, port activity, paging changes, halt/crash points.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/koron-go/z80"
	"github.com/petemoore/samfile/v3"
	"github.com/petemoore/samfile/v3/sambasic"
)

// rawTokens lets us hand a pre-tokenised byte slice (e.g. produced by
// the SAM ROM's TOKMAIN) to sambasic.Line as a single opaque Token.
// sambasic.Line.Bytes() then prepends the 2-byte line number + 2-byte
// length and appends the 0x0D CR, exactly matching the on-disk SAM
// BASIC line wire format.
type rawTokens []byte

func (r rawTokens) Bytes() []byte { return []byte(r) }

// SAM Coupé memory map:
//
//   Section A: 0x0000-0x3FFF  ROM 0 by default; if LMPR bit 5 (RAM0) is HIGH then
//                             RAM page (LMPR & 0x1F) appears instead.
//   Section B: 0x4000-0x7FFF  always RAM page (LMPR_page + 1) mod 32. Bit 7 (WPRAM)
//                             write-protects this section when high (ignored here).
//   Section C: 0x8000-0xBFFF  RAM page (HMPR & 0x1F).
//   Section D: 0xC000-0xFFFF  RAM page (HMPR_page + 1) by default; if LMPR bit 6
//                             (ROM1) is HIGH then ROM 1 appears instead.
//
// Pages 0..31 each 16KB = 512KB RAM total. ROM is 32KB total (ROM 0 = lower 16KB,
// ROM 1 = upper 16KB of samcoupe.rom).
//
// Tech manual citations: LMPR bit map verified verbatim against tech-man v3-0
// p.17 (LMPR bit 5 = RAM0, bit 6 = ROM1, bit 7 = WPRAM).
// ROM disasm citation: MINITH at 0x00B0 writes LMPR=0x5F (ROM 0 stays in section A
// — bit 5 CLEAR — and ROM 1 turns ON in section D — bit 6 SET; lower 5 bits set the
// RAM page that would appear in section A if bit 5 went high later).
type Hardware struct {
	rom  []byte    // 32KB SAM ROM (ROM 0 then ROM 1)
	ram  [32][16384]byte
	lmpr uint8
	hmpr uint8
	vmpr uint8

	// Back-reference to the CPU so IO.In/Out can peek cpu.BC.Hi —
	// koron-go's IO callback only receives the low byte but for
	// SAM keyboard scanning (`IN E,(C)` with BC=row<<8 | KEYPORT)
	// we need the high byte to know which keyboard row is being
	// scanned. cpu.BC.Hi is intact at IO callback time.
	cpu *z80.CPU

	// Fake keyboard queue: when non-empty, intercept reads of FLAGS
	// (bit 5 = key-available) and LASTK to deliver our chars.
	// Bypasses the entire interrupt-driven scan-and-queue machinery.
	keyQueue []byte

	// Diagnostic counters
	flagsReads uint64
	lastkReads uint64
	flagsWrites uint64

	// Captured by extractTokenisedLine on RST 8 entry.
	lastTokenisedLine tokenisedLine

	// SAM keyboard matrix — 9 bytes, each row 8 bits, active-low.
	// Layout (rows 0-7 selected by BC.Hi bit-i LOW; row 8 always read
	// on port FE when port high byte == 0xFF):
	//   Row 0: bit 0=SHIFT, 1=Z, 2=X, 3=C, 4=V    (bits 5-7 = F1/F2/F3 keypad)
	//   Row 1: bit 0=A,     1=S, 2=D, 3=F, 4=G    (bits 5-7 = F4/F5/F6)
	//   Row 2: bit 0=Q,     1=W, 2=E, 3=R, 4=T    (bits 5-7 = F7/F8/F9)
	//   Row 3: bit 0=1,     1=2, 2=3, 3=4, 4=5
	//   Row 4: bit 0=0,     1=9, 2=8, 3=7, 4=6
	//   Row 5: bit 0=P,     1=O, 2=I, 3=U, 4=Y
	//   Row 6: bit 0=ENTER, 1=L, 2=K, 3=J, 4=H
	//   Row 7: bit 0=SPACE, 1=SYM, 2=M, 3=N, 4=B
	//   Row 8: bit 0=CTRL,  1=UP, 2=DOWN, 3=LEFT, 4=RIGHT
	//
	// Source: ~/git/simcoupe/Base/Keyboard.cpp:65 (asKeyMatrix) and
	// ~/git/simcoupe/Base/SAMIO.cpp:423-464 (port read logic).
	keyMatrix [9]uint8

	// Trace
	portWrites map[uint8]int
	portReads  map[uint8]int
	lmprWrites []uint8
	hmprWrites []uint8

	rom0Reads uint64
	rom1Reads uint64
	ramReads  uint64
	romWrites uint64 // writes to a section currently mapped to ROM (silently dropped)
	ramWrites uint64
}

func newHardware(rom []byte) *Hardware {
	hw := &Hardware{
		rom: rom,
		// Hardware reset: bit 5 (RAM0) clear so ROM 0 is visible in
		// section A — that's how PC=0 lands in ROM. bit 6 (ROM1) starts
		// clear too; MINITH sets it before JP MNINIT.
		lmpr:       0x00,
		hmpr:       0x00,
		vmpr:       0x00,
		portWrites: map[uint8]int{},
		portReads:  map[uint8]int{},
	}
	// No keys pressed: matrix all 1s (keys are active-low).
	for i := range hw.keyMatrix {
		hw.keyMatrix[i] = 0xFF
	}
	return hw
}

// resolve returns (page, isROM, romHalf). For ROM: romHalf=0 means ROM 0, 1 means ROM 1.
// Bit semantics per tech-man v3-0 p.17.
func (h *Hardware) resolve(addr uint16) (page uint8, isROM bool, romHalf uint8) {
	section := addr >> 14
	switch section {
	case 0: // section A — ROM 0 unless LMPR bit 5 (RAM0) is HIGH
		if h.lmpr&0x20 == 0 {
			return 0, true, 0
		}
		return h.lmpr & 0x1F, false, 0
	case 1: // section B — always RAM, page = section-A-page + 1 mod 32
		return (h.lmpr + 1) & 0x1F, false, 0
	case 2: // section C — RAM page from HMPR
		return h.hmpr & 0x1F, false, 0
	case 3: // section D — ROM 1 if LMPR bit 6 (ROM1) is HIGH, else RAM
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
			h.rom0Reads++
			return h.rom[offset]
		}
		h.rom1Reads++
		return h.rom[16384+offset]
	}
	h.ramReads++
	v := h.ram[page][offset]
	// Fake-keyboard intercept: when keys are queued, present
	// FLAGS bit 5 (key-available) set, and LASTK = head of queue.
	// The editor's KYIP2 path reads FLAGS, checks bit 5, reads
	// LASTK, then RES 5,(HL) — that write is caught in Set().
	switch addr {
	case sysFLAGS:
		h.flagsReads++
		if len(h.keyQueue) > 0 {
			return v | 0x20
		}
	case sysLASTK:
		h.lastkReads++
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
		h.romWrites++
		return
	}
	h.ramWrites++
	h.ram[page][offset] = value
	if addr == sysFLAGS {
		h.flagsWrites++
		// Detect "key consumed" — the editor's KYIP2 does RES 5,(HL)
		// on FLAGS after reading LASTK. A write to FLAGS with bit 5
		// clear while we have a queued key means it was just read.
		if len(h.keyQueue) > 0 && value&0x20 == 0 {
			h.keyQueue = h.keyQueue[1:]
		}
	}
}

// scanKeyMatrix implements the SAM matrix read protocol. rowSelect
// is the high byte of BC (active-low bits 0-7 = rows 0-7). bitMask
// selects which key bits to return (0x1F for KEYPORT bits 0-4, 0xE0
// for STATPORT bits 7-5). Per SimCoupé SAMIO.cpp:423-464.
func (h *Hardware) scanKeyMatrix(rowSelect uint8, bitMask uint8) uint8 {
	keys := uint8(0xFF)
	if rowSelect == 0xFF {
		// Special: bottom row (cursor keys + RCTRL) is read when
		// every row-select bit is HIGH (no normal row selected).
		keys &= h.keyMatrix[8]
	} else {
		for i := 0; i < 8; i++ {
			if rowSelect&(1<<i) == 0 {
				keys &= h.keyMatrix[i]
			}
		}
	}
	return keys & bitMask
}

func (h *Hardware) In(addr uint8) uint8 {
	h.portReads[addr]++
	switch addr {
	case 0xFA:
		return h.lmpr
	case 0xFB:
		return h.hmpr
	case 0xFC:
		return h.vmpr
	case 0xFE: // KEYPORT — bits 0-4 from selected rows; bits 5-7 from border/keyboard state
		if h.cpu == nil {
			return 0xFF
		}
		// We don't model EAR/SPEN/SOFF, so just return matrix bits
		// ORed with 1s for the unused bits.
		return h.scanKeyMatrix(h.cpu.BC.Hi, 0x1F) | 0xE0
	case 0xF9: // STATPORT — bits 5-7 from selected rows
		if h.cpu == nil {
			return 0xFF
		}
		return h.scanKeyMatrix(h.cpu.BC.Hi, 0xE0) | 0x1F
	}
	return 0xFF
}

func (h *Hardware) Out(addr uint8, value uint8) {
	h.portWrites[addr]++
	switch addr {
	case 0xFA:
		h.lmpr = value
		h.lmprWrites = append(h.lmprWrites, value)
	case 0xFB:
		h.hmpr = value
		h.hmprWrites = append(h.hmprWrites, value)
	case 0xFC:
		h.vmpr = value
	}
}

type tracePoint struct {
	step uint64
	pc   uint16
	op   uint8
}

// SAM sysvars (all in VAR2 = 0x5A00, normally visible in section B with
// LMPR=0x5F: section B = RAM page 0, so RAM page 0 offset 0x1A00 onwards).
//
// All addresses copied verbatim from rom-disasm definitions:
//
//	SAVARS=5A82  NVARS=5A88  WORKSP=5A91  ELINE=5A94  CHAD=5A97  PROG=5AA0
//	ERRNR=5C3A  NSPPC=5C44  EPPC=5C49
const (
	sysELINE  = 0x5A94 // ptr to start of edit-line buffer (2 bytes)
	sysWORKSP = 0x5A91 // ptr to workspace start = ELINE end + 1 (2 bytes)
	sysCHAD   = 0x5A97 // ptr to current char in line (2 bytes)
	sysPROG   = 0x5AA0 // ptr to start of program (2 bytes)
	sysNVARS  = 0x5A88 // ptr to start of numeric vars (2 bytes)
	sysSAVARS = 0x5A82 // ptr to saved vars (2 bytes)
	sysERRNR  = 0x5C3A // error number (1 byte)
	sysLASTK   = 0x5C08 // last key pressed / received from queue
	sysFLAGS   = 0x5C3B // FLAGS; bit 5 = "key available in LASTK"
	sysCUSCRNP = 0x5A78 // current screen page being printed to
	sysFISCRNP = 0x5C9F // first screen page (boot default)
)

// peekRAM reads a byte from the RAM page currently mapped at addr's section.
// Useful for inspecting sysvars when LMPR is in its default 0x5F state
// (section B = RAM page 0, sysvars visible at 0x4000-0x7FFF and so on).
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

// Snapshot is a complete, restorable picture of the emulated SAM at
// a single instant — every RAM page, every paging port, and every
// CPU register including the alternates and the interrupt state.
//
// In-memory snapshot/restore lets us amortise the ~30 ms cold boot
// over many line injections: boot once to MAINELP, snapshot, then
// for each input line restore + inject + extract. Each restore is a
// 512KB array copy + a struct copy = well under 100 µs.
type Snapshot struct {
	RAM       [32][16384]byte
	LMPR      uint8
	HMPR      uint8
	VMPR      uint8
	CPUStates z80.States
	HALT      bool
	Interrupt *z80.Interrupt
}

// Snapshot captures the current emulator state.
func (h *Hardware) Snapshot() Snapshot {
	return Snapshot{
		RAM:       h.ram,
		LMPR:      h.lmpr,
		HMPR:      h.hmpr,
		VMPR:      h.vmpr,
		CPUStates: h.cpu.States,
		HALT:      h.cpu.HALT,
		Interrupt: h.cpu.Interrupt,
	}
}

// Restore reverts the emulator to a previously taken snapshot.
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

// writeBasicMGT wraps a tokenised sambasic.File into a SAMDOS-bootable
// MGT image with two slots:
//
//	slot 0: samdos2 (so the disk boots)
//	slot 1: <name> as FT_SAM_BASIC (the tokenised program)
//
// If basFile.StartLine != 0 then the BASIC file is marked auto-RUN
// at that line number (per AddBasicFile's standard convention).
func writeBasicMGT(outputPath, name string, basFile *sambasic.File) error {
	samdos2, err := os.ReadFile("reference/samdos/samdos2.bin")
	if err != nil {
		return fmt.Errorf("read samdos2: %w", err)
	}
	if len(samdos2) != 10000 {
		return fmt.Errorf("samdos2: expected 10000 bytes, got %d", len(samdos2))
	}
	const samdosLoad = uint32(491529)
	disk := samfile.NewDiskImage()
	if err := disk.AddCodeFile("samdos2", samdos2, samdosLoad, 0); err != nil {
		return fmt.Errorf("AddCodeFile(samdos2): %w", err)
	}
	if err := disk.SetStartAddressPageUnusedBits("samdos2", 3); err != nil {
		return fmt.Errorf("SetStartAddressPageUnusedBits(samdos2): %w", err)
	}
	if err := disk.AddBasicFile(name, basFile); err != nil {
		return fmt.Errorf("AddBasicFile(%q): %w", name, err)
	}
	return disk.Save(outputPath)
}

// pokeRAM writes a byte to whatever page is currently mapped at addr.
func pokeRAM(hw *Hardware, addr uint16, v uint8) {
	page, isROM, _ := hw.resolve(addr)
	if isROM {
		log.Fatalf("pokeRAM(%04X) lands in ROM — paging not set up for injection", addr)
	}
	hw.ram[page][addr&0x3FFF] = v
}

func pokeRAM16(hw *Hardware, addr uint16, v uint16) {
	pokeRAM(hw, addr, uint8(v))
	pokeRAM(hw, addr+1, uint8(v>>8))
}

// samKey identifies a position on the SAM key matrix. row is 0-8;
// bit is 0-4 for main keys (read via KEYPORT/0xFE), 5-7 for modifier
// row (read via STATPORT/0xF9, but the modifier-row keys are only
// SHIFT/CTRL/SYM and live in their corresponding rows 0/7/8).
type samKey struct{ row, bit uint8 }

// asciiToSamKey maps the small set of ASCII characters used in our
// test program to their (row, bit) on the SAM matrix. Letters use
// CAPS LOCK on (SAM default) so unshifted keys produce upper-case in
// display but lower-case in the tokenisable buffer — the BASIC
// tokeniser is case-insensitive so this is fine.
var asciiToSamKey = map[byte]samKey{
	' ':  {7, 0}, // SPACE
	'\r': {6, 0}, // ENTER
	'0':  {4, 0}, '1': {3, 0}, '2': {3, 1}, '3': {3, 2}, '4': {3, 3}, '5': {3, 4},
	'6': {4, 4}, '7': {4, 3}, '8': {4, 2}, '9': {4, 1},
	'A': {1, 0}, 'B': {7, 4}, 'C': {0, 3}, 'D': {1, 2}, 'E': {2, 2},
	'F': {1, 3}, 'G': {1, 4}, 'H': {6, 4}, 'I': {5, 2}, 'J': {6, 3},
	'K': {6, 2}, 'L': {6, 1}, 'M': {7, 2}, 'N': {7, 3}, 'O': {5, 1},
	'P': {5, 0}, 'Q': {2, 0}, 'R': {2, 3}, 'S': {1, 1}, 'T': {2, 4},
	'U': {5, 3}, 'V': {0, 4}, 'W': {2, 1}, 'X': {0, 2}, 'Y': {5, 4},
	'Z': {0, 1},
}

func asciiUpper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 32
	}
	return b
}

// pressMatrix sets a key bit LOW in the matrix.
func (h *Hardware) pressMatrix(k samKey) {
	h.keyMatrix[k.row] &^= 1 << k.bit
}

// releaseMatrix sets a key bit HIGH in the matrix.
func (h *Hardware) releaseMatrix(k samKey) {
	h.keyMatrix[k.row] |= 1 << k.bit
}

// injectKeysAndRun drives the ROM through its normal keyboard input
// path by faking the LASTK / FLAGS-bit-5 channel.
//
// SAM's editor calls KYIP2 (0x050A) to read keys: if FLAGS bit 5
// (0x20) is set, it reads LASTK (0x5C08), clears the bit, and returns
// the byte. Normally the bit is set by the queue-to-LASTK transfer
// routine at 0xD51F (driven by KINTER from the line interrupt).
//
// We don't service interrupts, so we drive that channel directly:
// for each character we write LASTK and set FLAGS bit 5, then step
// the CPU until bit 5 clears (meaning the editor consumed it), then
// move on to the next character.
//
// The line interrupt handler (KINTER → queue → LASTK) is gated by
// "RET NZ if FLAGS bit 5 already set" (0xD51E), so our value never
// gets overwritten while waiting to be consumed.
//
// Stop conditions:
//   - all characters consumed AND PC re-enters KEYSCAN (0xD5BC) with
//     FLAGS bit 5 clear → line was processed, ROM is at "ready" again
//   - PC enters ERROR2 (0x37CE) → syntax / runtime error
//   - HALT, or step budget exhausted
// tokenisedLine holds a single SAM BASIC line as it appears in ELINE
// after the editor's TOKMAIN has run. lineNumber is the value parsed
// from the leading ASCII digits; tokens is the byte slice from the
// first token after the digits up to and including the 0x0D CR
// terminator. This is exactly the wire format SAM BASIC uses inside
// a saved FT_SAM_BASIC body, prefixed with 2 bytes of line number
// (big-endian) and 2 bytes of length (little-endian).
type tokenisedLine struct {
	lineNumber uint16
	tokens     []byte // includes the trailing 0x0D
}

// extractTokenisedLine reads ELINE at the moment of RST 8 (ERROR2)
// and produces the SAM BASIC wire-format representation of the line
// that was just typed. Pete's insight: we don't need INSERTLN to run
// — the editor's tokeniser has already done its job by the time the
// CR is consumed. We can build PROG bytes ourselves by collecting
// these and sorting by line number.
//
// ELINE layout at this point (example for "10 PRINT 1"):
//
//	31 30 BB 31 0E 00 00 01 00 00 0D FF
//	└─┬─┘ └─────────── tokens ──────┘  └ end marker
//	  ASCII line number digits
//
// The line number is parsed from the leading ASCII digits; the
// tokens slice starts at the first non-digit and ends at (and
// includes) the 0x0D CR. Optional leading whitespace between the
// digits and the first token is skipped — matches INSERTLN's
// behaviour at rom-disasm:10AB-10B7.
func extractTokenisedLine(hw *Hardware) (tokenisedLine, error) {
	elinePtr := peekRAM16(hw, sysELINE)
	workspPtr := peekRAM16(hw, sysWORKSP)
	if workspPtr <= elinePtr {
		return tokenisedLine{}, fmt.Errorf("ELINE invalid: ELINE=%04X WORKSP=%04X", elinePtr, workspPtr)
	}

	// Parse leading digits.
	var ln uint32
	i := uint16(0)
	for ; elinePtr+i < workspPtr; i++ {
		b := peekRAM(hw, elinePtr+i)
		if b < '0' || b > '9' {
			break
		}
		ln = ln*10 + uint32(b-'0')
		if ln > 0xFFFF {
			return tokenisedLine{}, fmt.Errorf("line number %d > 65535", ln)
		}
	}
	if i == 0 {
		return tokenisedLine{}, fmt.Errorf("ELINE starts with non-digit 0x%02X (direct command?)", peekRAM(hw, elinePtr))
	}

	// Skip a single optional space between the line number and the
	// first token (per INSERTLN behaviour).
	if elinePtr+i < workspPtr && peekRAM(hw, elinePtr+i) == ' ' {
		i++
	}

	// Tokens run from here up to and including the first 0x0D.
	tokens := []byte{}
	for ; elinePtr+i < workspPtr; i++ {
		b := peekRAM(hw, elinePtr+i)
		tokens = append(tokens, b)
		if b == 0x0D {
			return tokenisedLine{lineNumber: uint16(ln), tokens: tokens}, nil
		}
	}
	return tokenisedLine{}, fmt.Errorf("no 0x0D CR found in ELINE")
}

func injectKeysAndRun(hw *Hardware, cpu *z80.CPU, line string, stepBudget uint64, intInterval uint64) bool {
	const (
		error2PC  = 0x37CE
		idleSteps = 500_000 // after queue drains, wait for tokenise/insert/AUTOLIST
	)

	// We now skip the banner at boot via the skipBannerPC hijack in
	// the outer loop, so the ROM is already at MAINELP (the editor
	// loop) by the time injection runs. ERRNR is still 0x50 in RAM
	// (stale leftover) but the editor doesn't care — it clears its
	// own error state when reading keys. We clear ERRNR explicitly
	// below so the post-LOAD check is meaningful.
	keys := append([]byte(line), '\r')
	hw.keyQueue = append(hw.keyQueue[:0], keys...)
	pokeRAM(hw, sysERRNR, 0)

	progBefore := peekRAM16(hw, sysPROG)
	nvarsBefore := peekRAM16(hw, sysNVARS)
	fmt.Printf("    keystroke injection (FLAGS/LASTK intercept): %d chars (line %q + CR)\n",
		len(keys), line)
	fmt.Printf("    pre-injection: PROG=%04X NVARS=%04X (PROG body len=%d)\n",
		progBefore, nvarsBefore, int(nvarsBefore)-int(progBefore))

	queueWasEmpty := false
	idleStep := uint64(0)
	lastQueueLen := len(hw.keyQueue)

	for i := uint64(0); i < stepBudget; i++ {
		cpu.Step()

		// Log when the editor consumes a key.
		if curLen := len(hw.keyQueue); curLen != lastQueueLen {
			consumed := keys[len(keys)-lastQueueLen]
			fmt.Printf("    step %d: editor consumed 0x%02X (%q), %d remaining\n",
				i+1, consumed, string([]byte{consumed}), curLen)
			lastQueueLen = curLen
		}

		if cpu.PC == error2PC {
			err := peekRAM(hw, sysERRNR)
			fmt.Printf("    step %d: ERROR2 entered (ERRNR=0x%02X — \"OK\" unwind path); extracting tokens from ELINE\n",
				i+1, err)
			tl, perr := extractTokenisedLine(hw)
			if perr != nil {
				fmt.Printf("    extractTokenisedLine: %v\n", perr)
				return false
			}
			fmt.Printf("    line %d, %d tokenised bytes: ", tl.lineNumber, len(tl.tokens))
			for _, b := range tl.tokens {
				fmt.Printf("%02X ", b)
			}
			fmt.Println()
			// Stash for the caller. For now, the spike just dumps —
			// the build-disk integration that assembles PROG will
			// come once we can do this for multiple lines back-to-back.
			hw.lastTokenisedLine = tl
			return true
		}
		if cpu.HALT {
			fmt.Printf("    step %d: HALT at PC=%04X\n", i+1, cpu.PC)
			return false
		}

		if len(hw.keyQueue) == 0 {
			if !queueWasEmpty {
				queueWasEmpty = true
				idleStep = 0
				fmt.Printf("    step %d: queue drained — waiting %d steps for tokenise/insert\n",
					i+1, idleSteps)
			}
			idleStep++
			if idleStep >= idleSteps {
				err := peekRAM(hw, sysERRNR)
				progAfter := peekRAM16(hw, sysPROG)
				nvarsAfter := peekRAM16(hw, sysNVARS)
				fmt.Printf("    DONE at step %d: ERRNR=0x%02X; PROG=%04X NVARS=%04X (body len=%d)\n",
					i+1, err, progAfter, nvarsAfter, int(nvarsAfter)-int(progAfter))
				return err == 0
			}
		}
	}
	fmt.Printf("    step budget exhausted (PC=%04X, %d/%d keys consumed)\n",
		cpu.PC, len(keys)-len(hw.keyQueue), len(keys))
	return false
}

// injectAndRun is best-effort line injection.
//
// Cleanest hijack found so far: jump to MAINEXEC (0x0E84) so AUTOLIST
// and SETMIN run normally (SETMIN clears ELINE and sets WORKSP/WKEND/
// STKEND properly). Then catch PC just before CALL EDITOR at 0x0E8D —
// at that point all editor-prelude state is sane — write our line
// into the freshly-cleared ELINE, advance PC past EDITOR to 0x0E90,
// and let TOKMAIN onward run.
//
// Entry points (verified against rom-disasm v3.0):
//
//	0x0E84 MAINEXEC  : full editor iteration, calls AUTOLIST + SETMIN
//	0x0E8D           : `CALL EDITOR` site — wait here, replace ELINE,
//	                   then jump past the call
//	0x0E90           : `CALL TOKMAIN` — tokenise the line we injected
//	0x0E7A MAINEADD  : `CALL INSERTLN` — splice into PROG
//	0x0E7D           : return point after CALL INSERTLN — clean stop
//	SETMIN at 0x1D71 : writes 0x0D, 0xFF at ELINE start; sets WORKSP
//
// Stop condition: PC = 0x0E7D (INSERTLN just returned) — read ERRNR
// to know success.
func injectAndRun(hw *Hardware, cpu *z80.CPU, line string, stepBudget uint64) bool {
	const (
		mainexecPC = 0x0E84
		preEditPC  = 0x0E8D // about to CALL EDITOR
		postEditPC = 0x0E90 // CALL TOKMAIN
		stopPC     = 0x0E7D
		ispval     = 0x4F00
	)

	cpu.SP = ispval
	cpu.PC = mainexecPC
	pokeRAM(hw, sysERRNR, 0)

	fmt.Printf("    SP ← %04X; PC ← %04X (MAINEXEC); running until pre-EDITOR (%04X)\n",
		cpu.SP, cpu.PC, preEditPC)

	// Phase 1: run from MAINEXEC until we're about to call EDITOR.
	hitPre := false
	for i := uint64(0); i < stepBudget; i++ {
		cpu.Step()
		if cpu.PC == preEditPC {
			hitPre = true
			fmt.Printf("    reached pre-EDITOR at step %d; ELINE buffer is now SETMIN-clean\n", i+1)
			break
		}
		if cpu.HALT {
			fmt.Printf("    HALT before pre-EDITOR at PC=%04X (step %d)\n", cpu.PC, i+1)
			return false
		}
	}
	if !hitPre {
		fmt.Printf("    never reached pre-EDITOR (PC=%04X)\n", cpu.PC)
		return false
	}

	// Phase 2: replace SETMIN's "<0x0D><0xFF>" empty-line marker with
	// our text + CR + 0xFF; update WORKSP correspondingly. Then JP
	// past the CALL EDITOR.
	elinePtr := peekRAM16(hw, sysELINE)
	for i, b := range []byte(line) {
		pokeRAM(hw, elinePtr+uint16(i), b)
	}
	pokeRAM(hw, elinePtr+uint16(len(line)), 0x0D)
	pokeRAM(hw, elinePtr+uint16(len(line))+1, 0xFF)
	pokeRAM16(hw, sysWORKSP, elinePtr+uint16(len(line))+2)

	fmt.Printf("    ELINE@%04X ← %q (%d bytes + CR + FF); WORKSP ← %04X; PC ← %04X (post-EDITOR)\n",
		elinePtr, line, len(line), elinePtr+uint16(len(line))+2, uint16(postEditPC))
	cpu.PC = postEditPC

	// Phase 3: run TOKMAIN → LINESCAN → MAINEADD → INSERTLN, stop after.
	// Annotate the milestone returns so we can see how far we got if we
	// get stuck.
	checkpoints := map[uint16]string{
		0x3872: "entering TOKMAIN",
		0x0E93: "back from TOKMAIN",
		0x0D13: "entering LINESCAN",
		0x0E96: "back from LINESCAN",
		0x1079: "entering EVALLINO",
		0x0EBB: "back from EVALLINO",
		0x0E7A: "entering MAINEADD",
		0x10A0: "entering INSERTLN",
		0x1E1B: "entering MAKEROOM",
	}
	fmt.Printf("    phase 3 entry: LMPR=%02X HMPR=%02X SP=%04X PC=%04X first 6 ROM bytes at PC=%02X %02X %02X %02X %02X %02X\n",
		hw.lmpr, hw.hmpr, cpu.SP, cpu.PC,
		hw.Get(cpu.PC), hw.Get(cpu.PC+1), hw.Get(cpu.PC+2),
		hw.Get(cpu.PC+3), hw.Get(cpu.PC+4), hw.Get(cpu.PC+5))
	for i := uint64(0); i < stepBudget; i++ {
		cpu.Step()
		if name, ok := checkpoints[cpu.PC]; ok {
			fmt.Printf("    step %d  PC=%04X  ← %s   (SP=%04X HL=%04X BC=%04X)\n",
				i+1, cpu.PC, name, cpu.SP, cpu.HL.U16(), cpu.BC.U16())
			delete(checkpoints, cpu.PC) // only show first visit
		}
		if cpu.PC == stopPC {
			err := peekRAM(hw, sysERRNR)
			fmt.Printf("    INSERTLN returned after %d steps; ERRNR = %02X\n", i+1, err)
			return err == 0
		}
		if cpu.HALT {
			fmt.Printf("    HALT at PC=%04X (step %d)\n", cpu.PC, i+1)
			return false
		}
	}
	fmt.Printf("    step budget exhausted in phase 3 (PC=%04X SP=%04X HL=%04X BC=%04X)\n",
		cpu.PC, cpu.SP, cpu.HL.U16(), cpu.BC.U16())
	return false
}

// dumpScreen writes the current screen image to disk. SAM mode 4 (the
// boot default; VMPR=0x7E has bits 5-6 = mode 3 internally = "mode 4"
// in user terms) is 256×192 pixels at 4 bits/pixel, 24576 bytes
// linear starting at the page indicated by VMPR & 0x1F. Each byte is
// two pixels: high nibble = left, low nibble = right. Pixel value
// 0-15 indexes the CLUT.
//
//	<basename>.bin    = raw 24576 bytes of screen RAM (24K pixel data)
//	<basename>.pgm    = ASCII PGM, 256×192 grayscale — opens directly
//	                    in macOS Preview, GIMP, etc.
//	<basename>.paltab = 40-byte PALTAB sysvar block read from
//	                    SAM RAM at 0x55D8. The first 16 bytes are
//	                    the live CLUT — needed so that the loaded
//	                    image renders with the same colours the
//	                    ROM had on screen at the breakpoint.
//	                    Sources: ROM disasm L1263 (PALTAB=0x55D8),
//	                    L19535 (OTDR loop sending PALTAB[0..15] to
//	                    CLUTPORT 0xF8).
func dumpScreen(hw *Hardware, basename string) error {
	page := hw.vmpr & 0x1F
	const bytes = 24576 // 192 lines × 128 bytes/line
	raw := make([]byte, bytes)
	for i := 0; i < bytes; i++ {
		raw[i] = hw.ram[(int(page)+i/16384)%32][i%16384]
	}
	if err := os.WriteFile(basename+".bin", raw, 0644); err != nil {
		return err
	}
	const W, H = 256, 192
	pgm := make([]byte, 0, len("P5\n256 192\n255\n")+W*H)
	pgm = append(pgm, []byte("P5\n256 192\n255\n")...)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x += 2 {
			b := raw[y*128+x/2]
			pgm = append(pgm, b&0xF0)
			pgm = append(pgm, (b&0x0F)<<4)
		}
	}
	if err := os.WriteFile(basename+".pgm", pgm, 0644); err != nil {
		return err
	}
	// PALTAB = sysvar at 0x55D8 (40 bytes). Section B with LMPR=0x5F
	// gives page 0; offset = 0x55D8 - 0x4000 = 0x15D8.
	paltab := make([]byte, 40)
	for i := range paltab {
		paltab[i] = peekRAM(hw, uint16(0x55D8+i))
	}
	return os.WriteFile(basename+".paltab", paltab, 0644)
}

func dumpSysvars(hw *Hardware) {
	fmt.Println()
	fmt.Println("=== Sysvars at READY ===")
	fmt.Printf("  ELINE  = %04X   (start of edit-line buffer)\n", peekRAM16(hw, sysELINE))
	fmt.Printf("  WORKSP = %04X   (1 past end of edit-line buffer)\n", peekRAM16(hw, sysWORKSP))
	fmt.Printf("  CHAD   = %04X   (current char in line)\n", peekRAM16(hw, sysCHAD))
	fmt.Printf("  PROG   = %04X   (start of BASIC program)\n", peekRAM16(hw, sysPROG))
	fmt.Printf("  NVARS  = %04X   (start of numeric vars / end of PROG)\n", peekRAM16(hw, sysNVARS))
	fmt.Printf("  SAVARS = %04X   (start of saved vars)\n", peekRAM16(hw, sysSAVARS))
	fmt.Printf("  ERRNR  = %02X     (error number)\n", peekRAM(hw, sysERRNR))
	fmt.Printf("  CUSCRNP= %02X     (current screen page = %d)\n", peekRAM(hw, sysCUSCRNP), peekRAM(hw, sysCUSCRNP)&0x1F)
	fmt.Printf("  FISCRNP= %02X     (first screen page = %d)\n", peekRAM(hw, sysFISCRNP), peekRAM(hw, sysFISCRNP)&0x1F)

	// Dump first 32 bytes of ELINE buffer and PROG buffer to see what's there.
	elinePtr := peekRAM16(hw, sysELINE)
	progPtr := peekRAM16(hw, sysPROG)
	fmt.Printf("\n  ELINE @ %04X: ", elinePtr)
	for i := uint16(0); i < 32; i++ {
		fmt.Printf("%02X ", peekRAM(hw, elinePtr+i))
	}
	fmt.Println()
	fmt.Printf("  PROG  @ %04X: ", progPtr)
	for i := uint16(0); i < 32; i++ {
		fmt.Printf("%02X ", peekRAM(hw, progPtr+i))
	}
	fmt.Println()
}

func main() {
	romPath := flag.String("rom", "/Users/pmoore/git/simcoupe/Resource/samcoupe.rom", "path to samcoupe.rom (32KB)")
	maxSteps := flag.Uint64("steps", 5_000_000, "max instructions to execute")
	tracePath := flag.String("trace", "", "if set, write a PC trace to this file (one line per instruction)")
	traceLimit := flag.Uint64("trace-limit", 200_000, "max entries to write to trace file")
	tailWindow := flag.Uint64("tail", 1000, "capture and dump this many PCs from the end of the run")
	rangeStart := flag.Uint64("range-start", 0, "if set, dump every step in [range-start, range-end] to range file")
	rangeEnd := flag.Uint64("range-end", 0, "see range-start")
	rangePath := flag.String("range", "/tmp/sam-range.txt", "where to write the range trace")
	injectLines := flag.String("inject", "", "newline-separated BASIC lines to inject after READY is reached")
	intInterval := flag.Uint64("int-interval", 70_000, "fire an IM1 interrupt every N steps after the IRQ is enabled (0 disables); 70k ≈ one 50Hz frame at SAM's ~3.5 MHz")
	screenPath := flag.String("screen", "", "if set, dump the current screen to <screen>.bin (24KB mode-4 raw) and <screen>.pgm (grayscale)")
	inputBasicPath := flag.String("in", "", "BASIC source text file: every non-empty line becomes one tokenised entry (overrides --inject)")
	outputMgtPath := flag.String("out", "", "if set, write a bootable MGT containing samdos2 + tokenised BASIC program (FT_SAM_BASIC) named on disk as --out-name")
	outputBasName := flag.String("out-name", "prog", "filename for the BASIC program inside the output MGT")
	outputAutorun := flag.Bool("out-autorun", false, "if true, mark the tokenised BASIC file as auto-RUN so the disk boots straight into it")
	flag.Parse()

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

	// Stop when the cold boot reaches WAITKEY (0x04F0) — that's the
	// editor's wait-for-key entry. Earlier breakpoints (e.g. on the
	// first KEYSCAN at 0xD5BC) fire during init scans before the
	// editor is actually polling for a typed character.
	// Banner-skip hijack: when the boot reaches 0x0F75 (the
	// `CALL ERRHAND1` inside MAINER3), advance PC to 0x0F78
	// (the `JP MAINELP` immediately after the call). MAINER3's
	// earlier instructions (CLSLOWER at 0x0F67, SET 5,(TVFLAG)
	// at 0x0F6D, RES 7,(FLAGS) at 0x0F70) still run — those
	// are state setup the editor needs. ERRHAND1's banner print
	// + WTFK wait are the only things skipped. After the JP we
	// land in MAINELP (the editor's main loop), which is what
	// we want to break on as "READY".
	const skipBannerPC uint16 = 0x0F75
	const skipBannerTo uint16 = 0x0F78

	// MAINELP at 0x0E8A — editor inner loop after MAINER3 / MAINEXEC
	// fall-through. First entry means: state is fully initialised,
	// banner skipped, editor is about to call STRM0 → EDITOR → … →
	// poll for keypress via WAITKEY. This is the right place to
	// switch to FLAGS/LASTK injection for the actual program text.
	const readyPC uint16 = 0x0E8A
	cpu.BreakPoints = map[uint16]struct{}{readyPC: {}}
	var readyStep uint64
	var readyHit bool

	// PC histogram: which ROM regions does it spend time in?
	pcHist := map[uint16]uint64{}

	// Recent PC ring buffer (for crash diagnosis / hang inspection)
	ringSize := int(*tailWindow)
	if ringSize < 32 {
		ringSize = 32
	}
	ring := make([]tracePoint, ringSize)
	ringHead := 0

	// Optional full PC trace to file
	var traceFile *os.File
	var traceCount uint64
	if *tracePath != "" {
		traceFile, err = os.Create(*tracePath)
		if err != nil {
			log.Fatalf("create trace: %v", err)
		}
		defer traceFile.Close()
	}

	var rangeFile *os.File
	if *rangeEnd > *rangeStart {
		rangeFile, err = os.Create(*rangePath)
		if err != nil {
			log.Fatalf("create range trace: %v", err)
		}
		defer rangeFile.Close()
	}

	start := time.Now()
	var step uint64
	var lastPC uint16 = 0xFFFF
	var samePCCount int
	var stuckPC uint16
	var stuck bool
	var prevLMPR, prevHMPR uint8 = hw.lmpr, hw.hmpr
	var prevSP uint16 = cpu.SP

	// Disable interrupts policy: hardware reset has IFF1=IFF2=false, IM=0.
	// We don't service interrupts in this spike (no timer), and ROM will
	// EI eventually; but with cpu.Interrupt==nil there's nothing to fire,
	// so it's harmless.

	for step = 0; step < *maxSteps; step++ {
		pc := cpu.PC
		op := hw.Get(pc) // peek — read again inside Step, fine
		pcHist[pc&0xFF00]++
		ring[ringHead] = tracePoint{step: step, pc: pc, op: op}
		ringHead = (ringHead + 1) % ringSize
		// Event-based trace: log on LMPR/HMPR change or SP change
		// (CALL / RET / PUSH / POP). PC discontinuities alone are too
		// noisy (every JR back in a delay loop fires).
		if traceFile != nil && traceCount < *traceLimit {
			lmprChanged := hw.lmpr != prevLMPR
			hmprChanged := hw.hmpr != prevHMPR
			spChanged := cpu.SP != prevSP
			if lmprChanged || hmprChanged || spChanged {
				tag := ""
				if lmprChanged {
					tag += fmt.Sprintf("LMPR=%02X→%02X ", prevLMPR, hw.lmpr)
				}
				if hmprChanged {
					tag += fmt.Sprintf("HMPR=%02X→%02X ", prevHMPR, hw.hmpr)
				}
				if spChanged {
					tag += fmt.Sprintf("SP%+d→%04X ", int(cpu.SP)-int(prevSP), cpu.SP)
				}
				fmt.Fprintf(traceFile, "step=%d PC=%04X op=%02X  %s\n",
					step, pc, op, tag)
				traceCount++
			}
		}
		if rangeFile != nil && step >= *rangeStart && step <= *rangeEnd {
			fmt.Fprintf(rangeFile, "step=%d PC=%04X op=%02X AF=%04X BC=%04X DE=%04X HL=%04X SP=%04X LMPR=%02X HMPR=%02X\n",
				step, pc, op, cpu.AF.U16(), cpu.BC.U16(), cpu.DE.U16(), cpu.HL.U16(), cpu.SP, hw.lmpr, hw.hmpr)
		}
		_ = pc
		prevLMPR = hw.lmpr
		prevHMPR = hw.hmpr
		prevSP = cpu.SP

		// Tight-loop / hang detection: same PC for >100k steps means
		// we're either in a block instruction (LDIR clearing a page
		// is up to 16384 iterations, OTIR up to 256) or genuinely
		// stuck. 100k comfortably exceeds any single block op.
		if pc == lastPC {
			samePCCount++
			if samePCCount > 100_000 && !stuck {
				stuck = true
				stuckPC = pc
				break
			}
		} else {
			samePCCount = 0
			lastPC = pc
		}

		// Banner-skip hijack: bypass CALL ERRHAND1 inside MAINER3.
		// See block-comment near readyPC for rationale.
		if cpu.PC == skipBannerPC {
			cpu.PC = skipBannerTo
		}

		// Fire periodic line interrupts once the ROM has enabled them.
		// On real SAM the line int fires ~50Hz; we use *intInterval as
		// a step-budget proxy. Without this the editor's WAITKEY loop
		// just polls FLAGS bit 5, which only ever gets set by the
		// KEYRD2 path running INSIDE the IM1 handler (per INTS5 at
		// 0xD4DD calling KEYRD2). No interrupts → no keyboard input.
		if *intInterval > 0 && cpu.IFF1 && step > 0 && step%*intInterval == 0 {
			cpu.Interrupt = z80.IM1Interrupt()
		}

		cpu.Step()

		if cpu.HALT {
			fmt.Printf("HALT at PC=%04X after %d steps\n", cpu.PC, step+1)
			break
		}
		if !readyHit && cpu.PC == readyPC {
			readyHit = true
			readyStep = step + 1
			fmt.Printf(">>> READY: breakpoint PC=%04X hit at step %d (%.1f ms wall time, LMPR=%02X HMPR=%02X VMPR=%02X SP=%04X) <<<\n",
				readyPC, readyStep, float64(time.Since(start).Microseconds())/1000.0,
				hw.lmpr, hw.hmpr, hw.vmpr, cpu.SP)
			dumpSysvars(hw)
			if *screenPath != "" {
				if err := dumpScreen(hw, *screenPath); err != nil {
					log.Printf("dumpScreen: %v", err)
				} else {
					fmt.Printf("    screen dumped to %s.bin / %s.pgm (VMPR=%02X, page=%d)\n",
						*screenPath, *screenPath, hw.vmpr, hw.vmpr&0x1F)
				}
			}
			// Pick source-text lines: --in <file> wins over --inject.
			var sourceLines []string
			switch {
			case *inputBasicPath != "":
				data, err := os.ReadFile(*inputBasicPath)
				if err != nil {
					log.Fatalf("read --in %s: %v", *inputBasicPath, err)
				}
				for _, ln := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
					if ln = strings.TrimRight(ln, "\r"); ln != "" {
						sourceLines = append(sourceLines, ln)
					}
				}
			case *injectLines != "":
				for _, ln := range strings.Split(strings.TrimRight(*injectLines, "\n"), "\n") {
					if ln != "" {
						sourceLines = append(sourceLines, ln)
					}
				}
			default:
				break
			}
			if len(sourceLines) == 0 {
				break
			}

			// Take a snapshot of the freshly-booted editor state, then
			// restore it before each line — Pete's design: skip the
			// 30 ms cold boot for every line by replaying from the
			// post-boot state in-memory.
			snap := hw.Snapshot()
			fmt.Printf("\n>>> Snapshot taken at MAINELP — boot cost amortised across %d line(s)\n",
				len(sourceLines))
			injectStart := time.Now()
			var collected []tokenisedLine
			for _, line := range sourceLines {
				hw.Restore(snap)
				fmt.Printf("\n>>> INJECTING: %q\n", line)
				if !injectKeysAndRun(hw, cpu, line, *maxSteps-step, *intInterval) {
					fmt.Println("    (injection did not complete cleanly)")
					return
				}
				collected = append(collected, hw.lastTokenisedLine)
			}
			fmt.Printf("\n=== Collected %d tokenised line(s) in %s (avg %s/line) ===\n",
				len(collected), time.Since(injectStart),
				time.Since(injectStart)/time.Duration(len(collected)))

			// Sort by line number (SAM PROG requires ascending order)
			// and lay them out as a sambasic.File. Each line's tokens
			// include the trailing 0x0D from ELINE; strip it because
			// sambasic.Line.Bytes appends its own CR (sambasic/file.go:26).
			sort.Slice(collected, func(i, j int) bool {
				return collected[i].lineNumber < collected[j].lineNumber
			})
			basFile := &sambasic.File{}
			if *outputAutorun && len(collected) > 0 {
				basFile.StartLine = collected[0].lineNumber
			}
			for _, tl := range collected {
				body := tl.tokens
				if n := len(body); n > 0 && body[n-1] == 0x0D {
					body = body[:n-1]
				}
				basFile.Lines = append(basFile.Lines, sambasic.Line{
					Number: tl.lineNumber,
					Tokens: []sambasic.Token{rawTokens(body)},
				})
				fmt.Printf("  line %5d (%d tokens): ", tl.lineNumber, len(tl.tokens))
				for _, b := range tl.tokens {
					fmt.Printf("%02X ", b)
				}
				fmt.Println()
			}
			fmt.Printf("\nProgBytes length: %d   NVARSOffset: %d\n",
				len(basFile.ProgBytes()), basFile.NVARSOffset())

			if *outputMgtPath != "" {
				if err := writeBasicMGT(*outputMgtPath, *outputBasName, basFile); err != nil {
					log.Fatalf("writeBasicMGT: %v", err)
				}
				fmt.Printf("Wrote %s with samdos2 + %q (FT_SAM_BASIC, autorun=%v)\n",
					*outputMgtPath, *outputBasName, *outputAutorun)
			}
			break
		}

		// Progress log
		if step != 0 && step%500_000 == 0 {
			fmt.Printf("step %7d  PC=%04X  AF=%04X BC=%04X DE=%04X HL=%04X SP=%04X  LMPR=%02X HMPR=%02X\n",
				step, cpu.PC, cpu.AF.U16(), cpu.BC.U16(), cpu.DE.U16(), cpu.HL.U16(), cpu.SP,
				hw.lmpr, hw.hmpr)
		}
	}
	elapsed := time.Since(start)

	fmt.Println()
	fmt.Println("=== Final state ===")
	fmt.Printf("Stopped after %d steps in %s (%.1f Mips)\n",
		step, elapsed, float64(step)/elapsed.Seconds()/1e6)
	fmt.Printf("PC=%04X SP=%04X  AF=%04X BC=%04X DE=%04X HL=%04X\n",
		cpu.PC, cpu.SP, cpu.AF.U16(), cpu.BC.U16(), cpu.DE.U16(), cpu.HL.U16())
	fmt.Printf("IX=%04X IY=%04X  IFF1=%v IFF2=%v IM=%d\n",
		cpu.IX, cpu.IY, cpu.IFF1, cpu.IFF2, cpu.IM)
	fmt.Printf("LMPR=%02X HMPR=%02X VMPR=%02X\n", hw.lmpr, hw.hmpr, hw.vmpr)
	if stuck {
		fmt.Printf("\n>>> STUCK at PC=%04X (same PC >50 steps) <<<\n", stuckPC)
	}

	fmt.Println()
	fmt.Printf("FLAGS reads/writes: %d / %d   LASTK reads: %d\n",
		hw.flagsReads, hw.flagsWrites, hw.lastkReads)
	fmt.Println()
	fmt.Println("=== Memory traffic ===")
	fmt.Printf("ROM 0 reads:  %d\n", hw.rom0Reads)
	fmt.Printf("ROM 1 reads:  %d\n", hw.rom1Reads)
	fmt.Printf("RAM reads:    %d\n", hw.ramReads)
	fmt.Printf("RAM writes:   %d\n", hw.ramWrites)
	fmt.Printf("ROM writes:   %d (dropped)\n", hw.romWrites)

	fmt.Println()
	fmt.Println("=== Port writes ===")
	type pc struct {
		port  uint8
		count int
	}
	var pws []pc
	for p, c := range hw.portWrites {
		pws = append(pws, pc{p, c})
	}
	sort.Slice(pws, func(i, j int) bool { return pws[i].count > pws[j].count })
	for _, e := range pws {
		fmt.Printf("  &%02X (%3d): %d writes\n", e.port, e.port, e.count)
	}

	fmt.Println()
	fmt.Println("=== Port reads ===")
	var prs []pc
	for p, c := range hw.portReads {
		prs = append(prs, pc{p, c})
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].count > prs[j].count })
	for _, e := range prs {
		fmt.Printf("  &%02X (%3d): %d reads\n", e.port, e.port, e.count)
	}

	fmt.Println()
	fmt.Println("=== Paging timeline (first 16 writes) ===")
	fmt.Print("LMPR: ")
	for i, v := range hw.lmprWrites {
		if i >= 16 {
			fmt.Printf("... (%d total)", len(hw.lmprWrites))
			break
		}
		fmt.Printf("%02X ", v)
	}
	fmt.Println()
	fmt.Print("HMPR: ")
	for i, v := range hw.hmprWrites {
		if i >= 16 {
			fmt.Printf("... (%d total)", len(hw.hmprWrites))
			break
		}
		fmt.Printf("%02X ", v)
	}
	fmt.Println()

	fmt.Println()
	fmt.Println("=== PC histogram (256-byte buckets, top 20) ===")
	type pcBucket struct {
		bucket uint16
		count  uint64
	}
	var buckets []pcBucket
	for b, c := range pcHist {
		buckets = append(buckets, pcBucket{b, c})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].count > buckets[j].count })
	for i, b := range buckets {
		if i >= 20 {
			break
		}
		region := "ROM0"
		if b.bucket >= 0xC000 {
			region = "ROM1?"
		} else if b.bucket >= 0x4000 && b.bucket < 0xC000 {
			region = "RAM"
		}
		fmt.Printf("  %04X-%04X (%s): %d steps\n", b.bucket, b.bucket|0xFF, region, b.count)
	}

	fmt.Println()
	fmt.Printf("=== Last %d PCs (tail of run) ===\n", ringSize)
	// Walk ring in chronological order; skip the unwritten slots at the
	// very start of a short run.
	for i := 0; i < ringSize; i++ {
		idx := (ringHead + i) % ringSize
		tp := ring[idx]
		if tp.step == 0 && i != 0 {
			continue // unwritten slot
		}
		fmt.Printf("  step %7d  PC=%04X  op=%02X\n", tp.step, tp.pc, tp.op)
	}

	// Avoid "imported and not used" if we trim later
	_ = context.Background
}
