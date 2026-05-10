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
)

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
	return &Hardware{
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
	return h.ram[page][offset]
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
	}
	// Default for unknown ports: float-high (0xFF) is what an unconnected
	// Z80 IN bus typically sees. SAM keyboard scan returns 0xFF for no
	// keys pressed, which is what we want anyway.
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
	cpu.PC = 0
	cpu.SP = 0xFFFF

	// Stop as soon as cold boot reaches KEYSCAN at 0xD5BC for the first
	// time — that's the BASIC ready idle loop (ROM disasm line 19838).
	const readyPC uint16 = 0xD5BC
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

		cpu.Step()

		if cpu.HALT {
			fmt.Printf("HALT at PC=%04X after %d steps\n", cpu.PC, step+1)
			break
		}
		if !readyHit && cpu.PC == readyPC {
			readyHit = true
			readyStep = step + 1
			fmt.Printf(">>> READY: KEYSCAN entered at step %d (%.1f ms wall time, LMPR=%02X HMPR=%02X VMPR=%02X SP=%04X) <<<\n",
				readyStep, float64(time.Since(start).Microseconds())/1000.0,
				hw.lmpr, hw.hmpr, hw.vmpr, cpu.SP)
			dumpSysvars(hw)
			if *injectLines == "" {
				break
			}
			// Inject each line in turn. After each, run until the
			// "INSERTLN returned" breakpoint fires.
			for _, line := range strings.Split(strings.TrimRight(*injectLines, "\n"), "\n") {
				fmt.Printf("\n>>> INJECTING: %q\n", line)
				if !injectAndRun(hw, cpu, line, *maxSteps-step) {
					fmt.Println("    (injection did not complete cleanly — see PC/ERRNR above)")
					break
				}
				dumpSysvars(hw)
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
