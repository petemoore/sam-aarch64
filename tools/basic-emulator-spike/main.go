// basic-emulator-spike — text BASIC → tokenised .mgt.
//
// Boots the SAM Coupé ROM under koron-go/z80 from a cold reset (with
// a small banner-skip hijack), reaches the editor's main loop, then
// for each input line: snapshots state, injects keystrokes via the
// FLAGS/LASTK channel, and extracts the tokenised line from ELINE on
// the editor's post-CR error-handler unwind (RST 8 at ERROR2).
// Tokenised lines are sorted by line number and packaged with samdos2
// into a bootable .mgt.
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

// rawTokens lets us hand a pre-tokenised byte slice (e.g. produced by
// the SAM ROM's TOKMAIN) to sambasic.Line as a single opaque Token.
// sambasic.Line.Bytes() then prepends the 2-byte line number + 2-byte
// length and appends the 0x0D CR, exactly matching the on-disk SAM
// BASIC line wire format.
type rawTokens []byte

func (r rawTokens) Bytes() []byte { return []byte(r) }

// canonicalNumericVars is the 92-byte NumericVars area as a real SAM
// ROM SAVE produces it for a freshly-initialised program: 46 bytes of
// 0xFF (the 23 letter-pointer pairs marking "no variable defined")
// followed by 46 bytes of PSVTAB content (the pre-saved variables
// table copied from ROM).
//
// samfile's default `make([]byte, 92)` emits 92 zeros, which crashes
// at auto-run as soon as the program touches a variable (e.g. via
// `FOR i=...`) — BASIC walks into corrupt letter pointers.
//
// Bytes extracted from `/tmp/hello-pete.mgt`, a disk Pete typed and
// SAVEd by hand on a real SAM, byte offsets 0xF04F..0xF0AA. This
// hardcode is a workaround. The architecturally clean fix is to let
// INSERTLN actually run during line injection (sidestepped today by
// extracting tokens from ELINE pre-insertion) and/or to route SAVE
// through SAMDOS under emulation — see future_dos_via_emulated_sam
// memory.
var canonicalNumericVars = []byte{
	// 46 bytes of 0xFF — letter-pointer "no var defined" sentinels
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	// 46 bytes of PSVTAB content (extracted programmatically from
	// /tmp/hello-pete.mgt, byte offsets 0xF07C..0xF0A9)
	0x19, 0x00, 0x03, 0x00, 0xFF, 0xFF, 0x02, 0x08,
	0x00, 0x6F, 0x73, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x02, 0xFF, 0xFF, 0x72, 0x67, 0x00, 0x00, 0xC0,
	0x00, 0x00, 0x02, 0x08, 0x00, 0x6F, 0x73, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x02, 0xFF, 0xFF, 0x72,
	0x67, 0x00, 0x00, 0x00, 0x01, 0x00,
}

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
	rom  []byte // 32KB SAM ROM (ROM 0 then ROM 1)
	ram  [32][16384]byte
	lmpr uint8
	hmpr uint8
	vmpr uint8

	// Back-reference to the CPU so Snapshot/Restore can capture and
	// replay register state, HALT, and pending Interrupt alongside RAM.
	cpu *z80.CPU

	// Fake keyboard queue: when non-empty, intercept reads of FLAGS
	// (bit 5 = key-available) and LASTK to deliver our chars.
	// Bypasses the entire interrupt-driven scan-and-queue machinery.
	keyQueue []byte

	// Captured by extractTokenisedLine on RST 8 entry.
	lastTokenisedLine tokenisedLine
}

func newHardware(rom []byte) *Hardware {
	// Hardware reset: bit 5 (RAM0) clear so ROM 0 is visible in
	// section A — that's how PC=0 lands in ROM. bit 6 (ROM1) starts
	// clear too; MINITH sets it before JP MNINIT.
	return &Hardware{rom: rom}
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
			return h.rom[offset]
		}
		return h.rom[16384+offset]
	}
	v := h.ram[page][offset]
	// Fake-keyboard intercept: when keys are queued, present FLAGS
	// bit 5 (key-available) set, and LASTK = head of queue. The
	// editor's KYIP2 path reads FLAGS, checks bit 5, reads LASTK,
	// then RES 5,(HL) — that write is caught in Set().
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
	// "Key consumed" — the editor's KYIP2 does RES 5,(HL) on FLAGS
	// after reading LASTK. A write to FLAGS with bit 5 clear while
	// we have a queued key means it was just read.
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

// SAM sysvars used by the spike. Addresses copied verbatim from
// rom-disasm definitions.
const (
	sysELINE  = 0x5A94 // ptr to start of edit-line buffer (2 bytes)
	sysWORKSP = 0x5A91 // ptr to workspace start = ELINE end + 1 (2 bytes)
	sysERRNR  = 0x5C3A // error number (1 byte)
	sysLASTK  = 0x5C08 // last key pressed / received from queue
	sysFLAGS  = 0x5C3B // FLAGS; bit 5 = "key available in LASTK"
)

// peekRAM reads a byte from the RAM page currently mapped at addr's section.
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

// pokeRAM writes a byte to whatever page is currently mapped at addr.
func pokeRAM(hw *Hardware, addr uint16, v uint8) {
	page, isROM, _ := hw.resolve(addr)
	if isROM {
		log.Fatalf("pokeRAM(%04X) lands in ROM — paging not set up for injection", addr)
	}
	hw.ram[page][addr&0x3FFF] = v
}

// Snapshot is a complete, restorable picture of the emulated SAM at a
// single instant — every RAM page, every paging port, and every CPU
// register including the alternates and the interrupt state.
//
// In-memory snapshot/restore lets us amortise the ~30 ms cold boot
// over many line injections: boot once to MAINELP, snapshot, then for
// each input line restore + inject + extract. Each restore is a
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
// If basFile.StartLine != 0 then the BASIC file is marked auto-RUN at
// that line number (per AddBasicFile's standard convention).
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

// tokenisedLine holds a single SAM BASIC line as it appears in ELINE
// after the editor's TOKMAIN has run. lineNumber is the value parsed
// from the leading ASCII digits; tokens is the byte slice from the
// first token after the digits up to and including the 0x0D CR
// terminator. This is exactly the wire format SAM BASIC uses inside a
// saved FT_SAM_BASIC body, prefixed with 2 bytes of line number
// (big-endian) and 2 bytes of length (little-endian).
type tokenisedLine struct {
	lineNumber uint16
	tokens     []byte // includes the trailing 0x0D
}

// extractTokenisedLine reads ELINE at the moment of RST 8 (ERROR2)
// and produces the SAM BASIC wire-format representation of the line
// that was just typed. We don't need INSERTLN to run — the editor's
// tokeniser has already done its job by the time the CR is consumed.
// We can build PROG bytes ourselves by collecting these and sorting
// by line number.
//
// ELINE layout at this point (example for "10 PRINT 1"):
//
//	31 30 BB 31 0E 00 00 01 00 00 0D FF
//	└─┬─┘ └─────────── tokens ──────┘  └ end marker
//	  ASCII line number digits
//
// The line number is parsed from the leading ASCII digits; the tokens
// slice starts at the first non-digit and ends at (and includes) the
// 0x0D CR. Optional leading whitespace between the digits and the
// first token is skipped — matches INSERTLN's behaviour at
// rom-disasm:10AB-10B7.
func extractTokenisedLine(hw *Hardware) (tokenisedLine, error) {
	elinePtr := peekRAM16(hw, sysELINE)
	workspPtr := peekRAM16(hw, sysWORKSP)
	if workspPtr <= elinePtr {
		return tokenisedLine{}, fmt.Errorf("ELINE invalid: ELINE=%04X WORKSP=%04X", elinePtr, workspPtr)
	}

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

// injectKeysAndRun drives the ROM through its normal keyboard input
// path by faking the LASTK / FLAGS-bit-5 channel.
//
// SAM's editor calls KYIP2 (0x050A) to read keys: if FLAGS bit 5
// (0x20) is set, it reads LASTK (0x5C08), clears the bit, and returns
// the byte. On real hardware the bit is set by the queue-to-LASTK
// transfer routine at 0xD51F (driven by KINTER from the line
// interrupt). In the spike we never enable interrupts; instead
// Hardware.Get intercepts reads of FLAGS/LASTK to deliver characters
// from hw.keyQueue, and Hardware.Set's RES-5-on-FLAGS detection
// advances the queue when the editor consumes a key.
//
// Returns true on success (PC reached ERROR2 with the tokenised line
// available in hw.lastTokenisedLine); false on HALT or step-budget
// exhaustion.
func injectKeysAndRun(hw *Hardware, cpu *z80.CPU, line string, stepBudget uint64) bool {
	const error2PC = 0x37CE
	hw.keyQueue = append([]byte(line), '\r')
	pokeRAM(hw, sysERRNR, 0)

	for i := uint64(0); i < stepBudget; i++ {
		cpu.Step()
		if cpu.PC == error2PC {
			tl, err := extractTokenisedLine(hw)
			if err != nil {
				return false
			}
			hw.lastTokenisedLine = tl
			return true
		}
		if cpu.HALT {
			return false
		}
	}
	return false
}

func main() {
	romPath := flag.String("rom", "/Users/pmoore/git/simcoupe/Resource/samcoupe.rom", "path to samcoupe.rom (32KB)")
	maxSteps := flag.Uint64("steps", 5_000_000, "max instructions per phase (cold boot, then per line)")
	inputBasicPath := flag.String("in", "", "BASIC source text file: every non-empty line becomes one tokenised entry")
	outputMgtPath := flag.String("out", "", "output bootable MGT path (samdos2 + tokenised BASIC as FT_SAM_BASIC)")
	outputBasName := flag.String("out-name", "auto", "filename for the BASIC program inside the output MGT (SAMDOS auto-boots only if name == \"auto\")")
	outputAutorun := flag.Bool("out-autorun", false, "mark the tokenised BASIC file as auto-RUN")
	flag.Parse()

	if *inputBasicPath == "" || *outputMgtPath == "" {
		log.Fatal("both --in and --out are required")
	}

	rom, err := os.ReadFile(*romPath)
	if err != nil {
		log.Fatalf("read ROM: %v", err)
	}
	if len(rom) != 32768 {
		log.Fatalf("ROM size: want 32768, got %d", len(rom))
	}

	data, err := os.ReadFile(*inputBasicPath)
	if err != nil {
		log.Fatalf("read --in %s: %v", *inputBasicPath, err)
	}
	var sourceLines []string
	for _, ln := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if ln = strings.TrimRight(ln, "\r"); ln != "" {
			sourceLines = append(sourceLines, ln)
		}
	}
	if len(sourceLines) == 0 {
		log.Fatalf("--in %s contained no non-empty lines", *inputBasicPath)
	}

	hw := newHardware(rom)
	cpu := &z80.CPU{Memory: hw, IO: hw}
	hw.cpu = cpu
	cpu.PC = 0
	cpu.SP = 0xFFFF

	// Banner-skip hijack: when the boot reaches 0x0F75 (the CALL
	// ERRHAND1 inside MAINER3), advance PC to 0x0F78 (the JP MAINELP
	// after the call). MAINER3's state setup (CLSLOWER at 0x0F67,
	// SET 5,(TVFLAG) at 0x0F6D, RES 7,(FLAGS) at 0x0F70) still runs;
	// only the banner print + WTFK wait are skipped.
	const skipBannerPC uint16 = 0x0F75
	const skipBannerTo uint16 = 0x0F78
	// MAINELP at 0x0E8A — editor's main loop. First entry means state
	// is fully initialised and banner skipped; the editor is now
	// polling for keypresses via WAITKEY.
	const readyPC uint16 = 0x0E8A

	// Hardware reset leaves IFF1=IFF2=false, IM=0. The ROM eventually
	// EIs, but we never set cpu.Interrupt, so no IM1 handler ever
	// runs — keyboard input is delivered entirely through the
	// FLAGS/LASTK intercept in Hardware.Get/Set.

	var step uint64
	for step = 0; step < *maxSteps; step++ {
		if cpu.PC == skipBannerPC {
			cpu.PC = skipBannerTo
		}
		cpu.Step()
		if cpu.HALT {
			log.Fatalf("HALT at PC=%04X after %d steps before reaching MAINELP", cpu.PC, step+1)
		}
		if cpu.PC == readyPC {
			break
		}
	}
	if step >= *maxSteps {
		log.Fatalf("step budget (%d) exhausted before reaching MAINELP (PC=%04X)", *maxSteps, cpu.PC)
	}

	snap := hw.Snapshot()
	var collected []tokenisedLine
	for _, line := range sourceLines {
		hw.Restore(snap)
		if !injectKeysAndRun(hw, cpu, line, *maxSteps) {
			log.Fatalf("injection failed for line: %q (PC=%04X HALT=%v)", line, cpu.PC, cpu.HALT)
		}
		collected = append(collected, hw.lastTokenisedLine)
	}

	sort.Slice(collected, func(i, j int) bool {
		return collected[i].lineNumber < collected[j].lineNumber
	})
	basFile := &sambasic.File{NumericVars: canonicalNumericVars}
	if *outputAutorun {
		basFile.StartLine = collected[0].lineNumber
	}
	for _, tl := range collected {
		// Strip the trailing 0x0D from ELINE; sambasic.Line.Bytes
		// appends its own CR (sambasic/file.go:26).
		body := tl.tokens
		if n := len(body); n > 0 && body[n-1] == 0x0D {
			body = body[:n-1]
		}
		basFile.Lines = append(basFile.Lines, sambasic.Line{
			Number: tl.lineNumber,
			Tokens: []sambasic.Token{rawTokens(body)},
		})
	}

	if err := writeBasicMGT(*outputMgtPath, *outputBasName, basFile); err != nil {
		log.Fatalf("writeBasicMGT: %v", err)
	}
	fmt.Printf("Wrote %s: %d line(s), %q as FT_SAM_BASIC, autorun=%v\n",
		*outputMgtPath, len(collected), *outputBasName, *outputAutorun)
}
