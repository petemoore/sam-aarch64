// disasm_oracle_test.go — TDD oracle for the standalone Z80 disassembler.
//
// Drives build/disasm.bin (the src/disasm.asm disassembler, assembled
// standalone at org &8000) inside the koron-go/z80 emulator and compares
// its (mnemonic, operands) output word-by-word against the Go disassembler
// oracle (aarch64dec.DecodeAt) over the real GNU release corpus
// (tests/m6/release/release.img, 21752 bytes = 5438 words).
//
// # Why this exists
//
// src/disasm.asm is ported family-by-family (strand-B PR-4); the Go oracle
// already decodes the full aarch64 subset and round-trips release.s.  This
// test drives the Z80 disassembler toward equivalence: it prints the exact
// match ratio plus a per-Go-mnemonic mismatch breakdown — the worklist for
// the next family to port, biggest first — and enforces a ratchet floor
// (matchFloor below) that each landed family raises.  It is green on main
// throughout the port and fails only on a regression below the floor; when
// the floor reaches nWords the Z80 disassembler is proven equivalent to the
// Go oracle.  TestDisasmSelfTest additionally runs the on-page boot
// self-test (entry &8003) and asserts BC=0.
//
// # disasm_entry ABI (src/disasm.asm lines 40-100)
//
//	entry = &8000.  Input: BC = high 16 bits of the 32-bit word (bits
//	31..16), IX = low 16 bits (bits 15..0).  On return two null-terminated
//	strings have been written into the section-B comm buffer:
//	  DISASM_COMM_MNEM (&7E99) — mnemonic, e.g. "nop", ".inst"
//	  DISASM_COMM_OPS  (&7EA3) — operands, e.g. "", "0xd2800000"
//
// In a flat 64 KB RAM emulator those fixed addresses are just RAM.
//
// # pc/base convention
//
// The release corpus is treated as linked at base 0 (matching how
// tools/run-disasm-roundtrip.sh [2c/3] runs `aarch64dec -asm` via
// WriteAsm(..., base=0, ...)).  So for word index i, pc = i*4 = byte offset.
// The Z80 stub is pc-agnostic (it only sees BC:IX), so pc only feeds the Go
// oracle's PC-relative decoders.
package main

import (
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/koron-go/z80"
	aarch64dec "github.com/petemoore/sam-aarch64/tools/aarch64dec"
)

// Comm-buffer addresses (src/disasm_comm.inc — single source of truth).
const (
	disasmEntry         = 0x8000
	disasmSelfTestEntry = 0x8003 // DISASM_SELF_TEST_ENTRY (jump-table slot)
	disasmCommMnem      = 0x7E99
	disasmCommOps       = 0x7EA3
)

// flatMem is a flat 64 KB RAM implementing z80.Memory + z80.IO.  The
// standalone disasm.bin executes only its own code and touches only RAM, so
// no paging / ROM / hooks are needed — unlike the SAM Hardware in harness.go.
type flatMem struct {
	ram [0x10000]byte
}

func (m *flatMem) Get(addr uint16) uint8        { return m.ram[addr] }
func (m *flatMem) Set(addr uint16, value uint8) { m.ram[addr] = value }
func (m *flatMem) In(port uint8) uint8          { return 0xFF }
func (m *flatMem) Out(port uint8, value uint8)  {}

// readCString reads a null-terminated string starting at addr.
func (m *flatMem) readCString(addr uint16) string {
	var b []byte
	for {
		c := m.ram[addr]
		if c == 0 {
			break
		}
		b = append(b, c)
		addr++
		if len(b) > 64 { // safety cap — comm buffers are <= 26 bytes
			break
		}
	}
	return string(b)
}

// runZ80Disasm loads disasmBin at &8000 into a fresh flat memory, sets
// BC = high16, IX = low16, and runs disasm_entry to completion, returning
// the (mnemonic, operands) strings it wrote to the comm buffer.
//
// Return detection: a sentinel return address 0x0000 is pushed before entry;
// the routine's terminating `ret` pops it into PC, so we step until PC==0
// (or until a step cap, to guard against runaway code).
func runZ80Disasm(disasmBin []byte, word uint32) (mnem, ops string, err error) {
	m := &flatMem{}
	copy(m.ram[disasmEntry:], disasmBin)

	cpu := &z80.CPU{Memory: m, IO: m}

	const sentinel = 0x0000
	cpu.SP = 0x7D00 // below the comm buffer (&7E99); push lands at &7CFE/&7CFF
	// Push sentinel return address so the routine's final `ret` jumps to it.
	cpu.SP -= 2
	m.ram[cpu.SP] = byte(sentinel & 0xFF)
	m.ram[cpu.SP+1] = byte(sentinel >> 8)

	cpu.BC.SetU16(uint16(word >> 16)) // BC = high 16 bits
	cpu.IX = uint16(word & 0xFFFF)    // IX = low 16 bits
	const iySentinel = 0xA5A5
	cpu.IY = iySentinel // must be preserved per the paged_call ABI
	cpu.PC = disasmEntry

	const maxSteps = 100000
	for steps := 0; steps < maxSteps; steps++ {
		cpu.Step()
		if cpu.PC == sentinel {
			// disasm_entry's contract: "Preserves: BC, IX, IY" (paged_call
			// ABI).  The production bytes->text caller decodes in a loop and
			// relies on it, so enforce it on every word.
			if got := cpu.BC.U16(); got != uint16(word>>16) {
				return "", "", fmt.Errorf("ABI violation: BC not preserved (got %#04x, want %#04x; word %#08x)", got, word>>16, word)
			}
			if cpu.IX != uint16(word&0xFFFF) {
				return "", "", fmt.Errorf("ABI violation: IX not preserved (got %#04x, want %#04x; word %#08x)", cpu.IX, word&0xFFFF, word)
			}
			if cpu.IY != iySentinel {
				return "", "", fmt.Errorf("ABI violation: IY not preserved (got %#04x, want %#04x; word %#08x)", cpu.IY, iySentinel, word)
			}
			return m.readCString(disasmCommMnem), m.readCString(disasmCommOps), nil
		}
		if cpu.HALT {
			return "", "", fmt.Errorf("HALT at PC=%04X (word=%#08x)", cpu.PC, word)
		}
	}
	return "", "", fmt.Errorf("step cap %d exceeded (PC=%04X, word=%#08x)", maxSteps, cpu.PC, word)
}

// TestDisasmSelfTest runs run_disasm_self_test (the boot self-test reached
// via the &8003 jump-table slot) standalone in the emulator and asserts it
// returns BC=0 (success).  This is the ONLY local check of that routine:
// at SAM boot it runs via paged_call under BUILD_TESTS, and a non-zero BC
// fail-tag would halt the assembler in the SimCoupé CI gate.  Verifying it
// here (no SimCoupé / no Docker needed) catches a broken self-test before
// CI does.  Keep its fixtures in lock-step with the ported families.
func TestDisasmSelfTest(t *testing.T) {
	disasmBin, err := os.ReadFile("../../build/disasm.bin")
	if err != nil {
		t.Fatalf("read build/disasm.bin (build it with `pyz80 --obj=build/disasm.bin src/disasm.asm`): %v", err)
	}
	m := &flatMem{}
	copy(m.ram[disasmEntry:], disasmBin)
	cpu := &z80.CPU{Memory: m, IO: m}
	cpu.SP = 0x7D00
	cpu.SP -= 2
	m.ram[cpu.SP] = 0
	m.ram[cpu.SP+1] = 0 // sentinel return address 0x0000
	cpu.PC = disasmSelfTestEntry

	const maxSteps = 2_000_000
	for steps := 0; steps < maxSteps; steps++ {
		cpu.Step()
		if cpu.PC == 0 {
			if bc := cpu.BC.U16(); bc != 0 {
				t.Fatalf("run_disasm_self_test FAILED: BC=%#04x (fail tag %#02x). "+
					"A self-test fixture mismatches its expected string; this would halt "+
					"the assembler at boot in the SimCoupé CI gate.", bc, bc&0xFF)
			}
			t.Logf("run_disasm_self_test PASS (BC=0) — boot self-test verified standalone")
			return
		}
		if cpu.HALT {
			t.Fatalf("run_disasm_self_test HALTed at PC=%04X", cpu.PC)
		}
	}
	t.Fatalf("run_disasm_self_test step cap %d exceeded (PC=%04X)", maxSteps, cpu.PC)
}

// goOracle returns the canonical (mnem, operands) the Go disassembler emits
// for word at pc, including the ok==false → ".inst" / "%#08x" fallback that
// WriteAsm uses (tools/aarch64dec/asm.go:74-76 and disasm.go).
func goOracle(pc uint64, word uint32) (mnem, ops string) {
	m, o, ok := aarch64dec.DecodeAt(pc, word)
	if !ok {
		return ".inst", fmt.Sprintf("%#08x", word)
	}
	return m, o
}

func TestDisasmOracle(t *testing.T) {
	disasmBin, err := os.ReadFile("../../build/disasm.bin")
	if err != nil {
		t.Fatalf("read build/disasm.bin (build it with `pyz80 --obj=build/disasm.bin src/disasm.asm` from repo root): %v", err)
	}
	corpus, err := os.ReadFile("../../tests/m6/release/release.img")
	if err != nil {
		t.Fatalf("read tests/m6/release/release.img: %v", err)
	}
	if len(corpus)%4 != 0 {
		t.Fatalf("corpus length %d is not a multiple of 4", len(corpus))
	}
	nWords := len(corpus) / 4

	type mismatch struct {
		pc              uint64
		word            uint32
		goMnem, goOps   string
		z80Mnem, z80Ops string
	}
	var samples []mismatch
	mismatchByMnem := map[string]int{}
	matches := 0
	nopMatched := false
	instMatched := 0

	for i := 0; i < nWords; i++ {
		off := i * 4
		word := uint32(corpus[off]) | uint32(corpus[off+1])<<8 |
			uint32(corpus[off+2])<<16 | uint32(corpus[off+3])<<24
		pc := uint64(off) // base 0: pc == byte offset

		goMnem, goOps := goOracle(pc, word)

		z80Mnem, z80Ops, rerr := runZ80Disasm(disasmBin, word)
		if rerr != nil {
			t.Fatalf("Z80 run failed at word %d (pc=%#x word=%#08x): %v", i, pc, word, rerr)
		}

		if goMnem == z80Mnem && goOps == z80Ops {
			matches++
			if word == 0xd503201f {
				nopMatched = true
			}
			if z80Mnem == ".inst" {
				instMatched++
			}
			continue
		}

		mismatchByMnem[goMnem]++
		if len(samples) < 20 {
			samples = append(samples, mismatch{pc, word, goMnem, goOps, z80Mnem, z80Ops})
		}
	}

	pct := 100.0 * float64(matches) / float64(nWords)

	t.Logf("=== Z80 disasm oracle vs Go aarch64dec — release corpus ===")
	t.Logf("corpus: tests/m6/release/release.img  (%d bytes = %d words)", len(corpus), nWords)
	t.Logf("MATCH RATIO: %d/%d = %.1f%%   (mismatches: %d)", matches, nWords, pct, nWords-matches)
	t.Logf("sanity: NOP (0xd503201f) matched=%v ; .inst data words matched=%d", nopMatched, instMatched)

	// Per-Go-mnemonic mismatch breakdown (the TDD progress signal: which
	// instruction families to port next, biggest first).
	type kv struct {
		mnem  string
		count int
	}
	var breakdown []kv
	for k, v := range mismatchByMnem {
		breakdown = append(breakdown, kv{k, v})
	}
	sort.Slice(breakdown, func(a, b int) bool {
		if breakdown[a].count != breakdown[b].count {
			return breakdown[a].count > breakdown[b].count
		}
		return breakdown[a].mnem < breakdown[b].mnem
	})
	t.Logf("--- mismatches by Go mnemonic (top 20) ---")
	for i, e := range breakdown {
		if i >= 20 {
			break
		}
		t.Logf("    %-10s %d", e.mnem, e.count)
	}

	t.Logf("--- first %d sample mismatches ---", len(samples))
	for _, s := range samples {
		t.Logf("    pc=%#x word=%#08x  go=[%s|%s]  z80=[%s|%s]",
			s.pc, s.word, s.goMnem, s.goOps, s.z80Mnem, s.z80Ops)
	}

	// Ratchet floor.  The Z80 disassembler is ported family-by-family
	// (strand-B PR-4); each increment must raise — and may never lower —
	// the count of words that match the Go oracle exactly.  The ultimate
	// target is matches == nWords (5438); when we reach it the
	// disassembler is proven equivalent to the Go oracle that already
	// round-trips release.s.
	//
	// RAISE THIS FLOOR whenever a family lands (the test prints the new
	// ratio).  Keeping it a floor — not a flat "== nWords" — lets the
	// test stay green on main throughout the port while still failing
	// loudly on any regression below the families already ported.
	const matchFloor = 2254 // NOP + .inst data + udf + move-wide (movz/movn/movk/mov)

	if matches < matchFloor {
		t.Errorf("REGRESSION: Z80 disasm matches Go oracle on only %d/%d words (= %.1f%%), "+
			"below the ratchet floor of %d. A previously-ported family stopped matching; "+
			"see the breakdown above.", matches, nWords, pct, matchFloor)
	}
	if matches < nWords {
		t.Logf("TDD progress: %d/%d words match (%.1f%%); %d still fall through to .inst. "+
			"Port the next family (biggest in the breakdown above) and raise matchFloor.",
			matches, nWords, pct, nWords-matches)
	}
}
