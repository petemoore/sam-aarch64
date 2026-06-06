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
// test asserts full per-word equivalence and prints the exact match ratio
// plus a per-Go-mnemonic mismatch breakdown — the worklist for the next
// family, biggest first.  It is developed on the PR-4 feature branch and
// fails (TDD red) until the port is complete; that is fine because the
// harness is never a CI gate and the branch is not merged until it reaches
// 100% (so main never sees a red test — no ratchet needed).  When green,
// the Z80 disassembler is proven equivalent to the Go oracle, so both
// run-disasm-roundtrip.sh gates pass for it too.  It also asserts the
// paged_call ABI (BC/IX/IY preserved) on every word.  TestDisasmSelfTest
// additionally runs the on-page boot self-test (entry &8003) → BC=0.
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
	disasmCommPC        = 0x7EBD // DISASM_COMM_PC (8-byte LE instruction PC)
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
// BC = high16, IX = low16, writes pc (8 bytes LE) into DISASM_COMM_PC, and
// runs disasm_entry to completion, returning the (mnemonic, operands)
// strings it wrote to the comm buffer.
//
// pc feeds the PC-relative families (b/bl/b.cc/cbz/cbnz/tbz/tbnz/adr/adrp/
// ldr-literal); it arrives via the memory slot, not a register, so the
// BC/IX/IY ABI assertions below are unchanged (IY stays a preserved
// sentinel).  This mirrors the Go oracle's DecodeAt(pc, word).
//
// Return detection: a sentinel return address 0x0000 is pushed before entry;
// the routine's terminating `ret` pops it into PC, so we step until PC==0
// (or until a step cap, to guard against runaway code).
func runZ80Disasm(disasmBin []byte, word uint32, pc uint64) (mnem, ops string, err error) {
	m := &flatMem{}
	copy(m.ram[disasmEntry:], disasmBin)

	// Write the instruction PC as an 8-byte little-endian value into the
	// section-B comm slot the decoder reads for PC-relative targets.
	for i := 0; i < 8; i++ {
		m.ram[disasmCommPC+i] = byte(pc >> (8 * uint(i)))
	}

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
// Reads build/disasm-test.bin — the TEST variant (-D BUILD_TESTS=1), which
// is the only build that carries the &8003 self-test entry + fixtures.  The
// shipped prod build/disasm.bin strips them.
func TestDisasmSelfTest(t *testing.T) {
	disasmBin, err := os.ReadFile("../../build/disasm-test.bin")
	if err != nil {
		t.Fatalf("read build/disasm-test.bin (build it with `pyz80 -D BUILD_TESTS=1 --obj=build/disasm-test.bin src/disasm.asm`): %v", err)
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

// TestDisasmNonCanonicalLogicalImm asserts the Z80 disassembler declines a
// non-canonical logical-immediate word the same way the Go decoder does,
// emitting `.inst 0xNNNNNNNN` rather than a canonicalised orr/and/eor.
//
// 0x32200013 is a non-canonical `orr w19, w0, #0x1` (esize=32, immr=32 ==
// esize).  The Z80 decodeBitMasks port (strand-B PR-4) rejects immr >= esize
// for the same reason the Go side does (ARM ARM C4.1.64 computes immr MOD
// esize, so decoding-then-re-encoding would change the raw bits).  Pairs the
// Go aarch64dec regression tests (tools/aarch64dec/slots_test.go:
// TestDecode_LogicalImm_NonCanonical_RejectsToInst) on the Z80 side.
//
// Reads the PROD build/disasm.bin (the shipped decoder).
func TestDisasmNonCanonicalLogicalImm(t *testing.T) {
	disasmBin, err := os.ReadFile("../../build/disasm.bin")
	if err != nil {
		t.Fatalf("read build/disasm.bin (build it with `pyz80 --obj=build/disasm.bin src/disasm.asm` from repo root): %v", err)
	}
	const word = 0x32200013
	mnem, ops, rerr := runZ80Disasm(disasmBin, word, 0)
	if rerr != nil {
		t.Fatalf("Z80 run failed for non-canonical word %#08x: %v", word, rerr)
	}
	if mnem != ".inst" || ops != fmt.Sprintf("%#08x", uint32(word)) {
		t.Errorf("Z80 disasm of non-canonical %#08x: got [%s|%s]; want [.inst|%#08x] "+
			"(non-canonical logical-immediate must decline to .inst, not canonicalise)",
			uint32(word), mnem, ops, uint32(word))
	}
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

// Reads build/disasm.bin — the PROD variant (no BUILD_TESTS flag), which is
// the binary actually shipped on production disks.  Verifying the shipped
// decoder (not the test build) is the point: this is the 100% gate.
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

		z80Mnem, z80Ops, rerr := runZ80Disasm(disasmBin, word, pc)
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

	// This test asserts FULL equivalence: every release.img word must
	// decode identically to the Go oracle.  It is developed entirely on
	// the strand-B PR-4 feature branch and fails (TDD red) until the port
	// is complete — which is fine, because the harness is never a CI gate
	// and the branch is not merged to main until the port reaches 100%.
	// (No ratchet / "allowed failure rate": a feature branch can stay red
	// until it passes — see the per-Go-mnemonic breakdown above for the
	// remaining worklist.)  When this is green the Z80 disassembler is
	// proven equivalent to the Go oracle that already round-trips
	// release.s, so both run-disasm-roundtrip.sh gates pass for it too.
	if matches != nWords {
		t.Errorf("Z80 disasm matches the Go oracle on %d/%d words (%.1f%%); %d still "+
			"differ (mostly fall through to .inst). Port the next family — biggest in "+
			"the breakdown above — until this reaches 100%%.", matches, nWords, pct, nWords-matches)
	}
}
