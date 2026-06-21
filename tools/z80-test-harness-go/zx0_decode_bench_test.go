// zx0_decode_bench_test.go — measures real ZX0 decode T-states per byte in
// the koron-go/z80 emulator.
//
// # Overview
//
// Loads the assembled dzx0_standard.bin / dzx0_turbo.bin decoders (assembled
// from testdata/*.asm) into a flat 64 KB Z80 address space, places a
// compressed block at COMPRESSED_ADDR (0x8000), decompresses to OUTPUT_ADDR
// (0xC000), and counts T-states by instrumenting each cpu.Step() with a
// before/after opcode analysis.
//
// koron-go/z80 v0.10.2 does NOT expose T-state counts — Step() executes one
// instruction with no cycle bookkeeping.  This file implements a T-state
// counter that: (a) reads the opcode at PC before each Step(), (b) reads
// relevant CPU state for data-dependent instructions (LDIR iterates at 21
// T-states except the final iteration at 16; conditional jumps/calls/rets
// take different counts depending on the flag state), (c) looks up the
// T-state cost in a table, and (d) sums across the full decode run.
//
// Method label: "instruction-level T-state table (koron-go/z80 v0.10.2)"
//
// # Correctness check
//
// The decompressed output is byte-compared against the original raw block.
// A T-state count from a decoder that produced wrong output is invalid; this
// test fails if there is any mismatch.
//
// # SAM-at-6MHz derivation
//
// 1 T-state at 6 MHz = 1/6_000_000 s ≈ 166.7 ns.
// ms_per_block = total_tstates / 6_000_000 * 1000.
// The figure is for uncontended RAM (no screen-DMA contention).  Real SAM
// RAM accesses in the screen-visible region can incur 1 extra T-state per
// M-cycle when the ULA accesses VRAM in the same page, but comment pages
// will be in non-VRAM pages so contention is unlikely in practice.
//
// # Memory layout (flat 64 KB, no SAM paging)
//
//	0x0000–0x03FF  decoder code (dzx0_mega, the largest variant, is 673 B)
//	0x8000         compressed block (source, HL on entry)
//	0xC000         output buffer (destination, DE on entry)
//	0x7FFE         initial stack pointer (for PUSH/POP / EX(SP),HL in decoder)
//
// # Reproduction
//
//	cd tools/z80-test-harness-go
//	go test -run TestZX0DecodeBench -v -count=1 .
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/koron-go/z80"
)

const (
	// Memory layout for the standalone ZX0 decoder run.
	// The decoder code is loaded at 0x0000.
	// A trampoline at 0x0400 does CALL 0x0000 ; HALT so the decoder's final
	// RET lands at the HALT — clean, portable termination with no PC-range
	// guessing.  The decoder region holds up to 1 KB (dzx0_mega is 673 B).
	zx0DecoderBase    = 0x0000 // decoder code starts here
	zx0TrampolineBase = 0x0400 // CALL 0x0000 + HALT — execution entry point
	zx0CompressedBase = 0x8000 // HL on entry: compressed data source
	zx0OutputBase     = 0xC000 // DE on entry: decompressed data destination
	zx0InitialSP      = 0x7FFE // stack grows down; safe zone 0x0500–0x7FFD

	// SAM Coupé clock frequency.
	samClockHz = 6_000_000.0
)

// flatRAM is a simple 64 KB flat memory model (no SAM paging).
type flatRAM struct {
	mem [65536]byte
}

func (m *flatRAM) Get(addr uint16) uint8    { return m.mem[addr] }
func (m *flatRAM) Set(addr uint16, v uint8) { m.mem[addr] = v }

// zx0NullIO is a no-op I/O interface (the ZX0 decoders never issue IN/OUT).
type zx0NullIO struct{}

func (zx0NullIO) In(uint8) uint8   { return 0xFF }
func (zx0NullIO) Out(uint8, uint8) {}

// tstatesForInstruction returns the T-state cost of the instruction whose
// opcode byte is at PC in mem.  cpu is the CPU state BEFORE Step() executes
// the instruction.
//
// For data-dependent instructions the pre-Step state is read to determine
// the taken/not-taken or LDIR-iteration count.
//
// Source: Zilog Z80 CPU User Manual (UM0080), Table 2.  All values are
// standard (no wait-state) counts.
func tstatesForInstruction(cpu *z80.CPU, mem *flatRAM) uint64 {
	pc := cpu.PC
	op0 := mem.Get(pc)

	switch op0 {
	case 0xED: // extended prefix
		op1 := mem.Get(pc + 1)
		switch op1 {
		case 0xB0: // LDIR — 21 T per repeat, 16 T for the final iteration
			bc := cpu.BC.U16()
			if bc <= 1 {
				return 16
			}
			return 21
		case 0xB8: // LDDR — same timing as LDIR
			bc := cpu.BC.U16()
			if bc <= 1 {
				return 16
			}
			return 21
		case 0x43, 0x53, 0x63, 0x73: // LD (nn),rr = 20 T
			return 20
		case 0x4B, 0x5B, 0x6B, 0x7B: // LD rr,(nn) = 20 T
			return 20
		case 0x40, 0x48, 0x50, 0x58, 0x60, 0x68, 0x70, 0x78: // IN r,(C) = 12 T
			return 12
		case 0x41, 0x49, 0x51, 0x59, 0x61, 0x69, 0x71, 0x79: // OUT (C),r = 12 T
			return 12
		case 0x42, 0x4A, 0x52, 0x5A, 0x62, 0x6A, 0x72, 0x7A: // SBC/ADC HL,ss = 15 T
			return 15
		case 0x44:
			return 8 // NEG = 8 T
		case 0x45:
			return 14 // RETN = 14 T
		case 0x4D:
			return 14 // RETI = 14 T
		case 0x46, 0x4E, 0x56, 0x5E, 0x66, 0x6E, 0x76, 0x7E:
			return 8 // IM x = 8 T
		case 0x47, 0x4F, 0x57, 0x5F:
			return 9 // LD A,I/R / LD I/R,A = 9 T
		case 0xA0, 0xA8:
			return 16 // LDI, LDD = 16 T
		case 0xA1, 0xA9:
			return 16 // CPI, CPD = 16 T
		case 0xB1, 0xB9: // CPIR/CPDR — data-dependent
			bc := cpu.BC.U16()
			if bc <= 1 {
				return 16
			}
			return 21
		default:
			return 8
		}

	case 0xCB: // CB prefix — bit/rotate/shift
		op1 := mem.Get(pc + 1)
		switch op1 & 0x07 {
		case 0x06: // (HL) variants
			if op1 >= 0x40 && op1 < 0x80 {
				return 12 // BIT b,(HL) = 12 T
			}
			return 15 // RLC/RRC/RL/RR/SLA/SRA/SRL/RES/SET (HL) = 15 T
		default:
			return 8 // register variants = 8 T
		}

	case 0xDD: // DD prefix (IX)
		op1 := mem.Get(pc + 1)
		if op1 == 0xCB {
			return 23 // DD CB d op = 23 T
		}
		switch op1 {
		case 0x21:
			return 14 // LD IX,nn = 14 T
		case 0x22, 0x2A:
			return 20 // LD (nn),IX / LD IX,(nn) = 20 T
		case 0x23, 0x2B:
			return 10 // INC/DEC IX = 10 T
		case 0x26, 0x2E:
			return 11 // LD IXH,n / LD IXL,n (undocumented) = 11 T
		case 0x34, 0x35:
			return 23 // INC/DEC (IX+d) = 23 T
		case 0x36:
			return 19 // LD (IX+d),n = 19 T
		case 0x46, 0x4E, 0x56, 0x5E, 0x66, 0x6E, 0x7E:
			return 19 // LD r,(IX+d) = 19 T
		case 0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x77:
			return 19 // LD (IX+d),r = 19 T
		case 0x86, 0x8E, 0x96, 0x9E, 0xA6, 0xAE, 0xB6, 0xBE:
			return 19 // ALU A,(IX+d) = 19 T
		case 0xE1:
			return 14 // POP IX = 14 T
		case 0xE3:
			return 23 // EX (SP),IX = 23 T
		case 0xE5:
			return 15 // PUSH IX = 15 T
		case 0xE9:
			return 8 // JP (IX) = 8 T
		case 0xF9:
			return 10 // LD SP,IX = 10 T
		case 0x09, 0x19, 0x29, 0x39:
			return 15 // ADD IX,pp = 15 T
		default:
			return 8
		}

	case 0xFD: // FD prefix (IY) — same timings as DD (IX)
		op1 := mem.Get(pc + 1)
		if op1 == 0xCB {
			return 23
		}
		switch op1 {
		case 0x21:
			return 14
		case 0x22, 0x2A:
			return 20
		case 0x23, 0x2B:
			return 10
		case 0x26, 0x2E:
			return 11 // LD IYH,n / LD IYL,n (undocumented) = 11 T
		case 0x34, 0x35:
			return 23
		case 0x36:
			return 19
		case 0x46, 0x4E, 0x56, 0x5E, 0x66, 0x6E, 0x7E:
			return 19
		case 0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x77:
			return 19
		case 0xE1:
			return 14
		case 0xE3:
			return 23
		case 0xE5:
			return 15
		case 0xE9:
			return 8
		case 0xF9:
			return 10
		case 0x09, 0x19, 0x29, 0x39:
			return 15
		default:
			return 8
		}

	// ── Unprefixed opcodes ──────────────────────────────────────────────

	case 0x00:
		return 4 // NOP
	case 0x76:
		return 4 // HALT

	// LD rr,nn = 10 T
	case 0x01, 0x11, 0x21, 0x31:
		return 10

	// LD (BC/DE),A / LD A,(BC/DE) = 7 T
	case 0x02, 0x12, 0x0A, 0x1A:
		return 7

	// INC/DEC rr = 6 T
	case 0x03, 0x13, 0x23, 0x33, 0x0B, 0x1B, 0x2B, 0x3B:
		return 6

	// INC r = 4 T
	case 0x04, 0x0C, 0x14, 0x1C, 0x24, 0x2C, 0x3C:
		return 4
	// INC (HL) = 11 T
	case 0x34:
		return 11

	// DEC r = 4 T
	case 0x05, 0x0D, 0x15, 0x1D, 0x25, 0x2D, 0x3D:
		return 4
	// DEC (HL) = 11 T
	case 0x35:
		return 11

	// LD r,n = 7 T
	case 0x06, 0x0E, 0x16, 0x1E, 0x26, 0x2E, 0x3E:
		return 7

	// Rotate A = 4 T
	case 0x07, 0x0F, 0x17, 0x1F:
		return 4

	// EX AF,AF' = 4 T
	case 0x08:
		return 4

	// ADD HL,rr = 11 T
	case 0x09, 0x19, 0x29, 0x39:
		return 11

	// DJNZ e = 13 T (taken, B!=1 before) / 8 T (not taken, B==1)
	case 0x10:
		if cpu.BC.Hi != 1 {
			return 13
		}
		return 8

	// JR e = 12 T (unconditional)
	case 0x18:
		return 12

	// JR NZ,e = 12 T (taken) / 7 T (not taken)
	case 0x20:
		if cpu.AF.Lo&0x40 == 0 {
			return 12
		}
		return 7
	// JR Z,e = 12 T (taken) / 7 T (not taken)
	case 0x28:
		if cpu.AF.Lo&0x40 != 0 {
			return 12
		}
		return 7
	// JR NC,e = 12 T (taken) / 7 T (not taken)
	case 0x30:
		if cpu.AF.Lo&0x01 == 0 {
			return 12
		}
		return 7
	// JR C,e = 12 T (taken) / 7 T (not taken)
	case 0x38:
		if cpu.AF.Lo&0x01 != 0 {
			return 12
		}
		return 7

	// LD (nn),HL / LD HL,(nn) = 16 T
	case 0x22, 0x2A:
		return 16

	// DAA = 4 T
	case 0x27:
		return 4

	// CPL = 4 T
	case 0x2F:
		return 4

	// LD (nn),A / LD A,(nn) = 13 T
	case 0x32, 0x3A:
		return 13

	// LD (HL),n = 10 T
	case 0x36:
		return 10

	// SCF / CCF = 4 T
	case 0x37, 0x3F:
		return 4

	// LD r,r (register-to-register) = 4 T
	case 0x40, 0x41, 0x42, 0x43, 0x44, 0x45, 0x47,
		0x48, 0x49, 0x4A, 0x4B, 0x4C, 0x4D, 0x4F,
		0x50, 0x51, 0x52, 0x53, 0x54, 0x55, 0x57,
		0x58, 0x59, 0x5A, 0x5B, 0x5C, 0x5D, 0x5F,
		0x60, 0x61, 0x62, 0x63, 0x64, 0x65, 0x67,
		0x68, 0x69, 0x6A, 0x6B, 0x6C, 0x6D, 0x6F,
		0x78, 0x79, 0x7A, 0x7B, 0x7C, 0x7D, 0x7F:
		return 4

	// LD r,(HL) = 7 T
	case 0x46, 0x4E, 0x56, 0x5E, 0x66, 0x6E, 0x7E:
		return 7

	// LD (HL),r = 7 T
	case 0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x77:
		return 7

	// ALU A,r = 4 T
	case 0x80, 0x81, 0x82, 0x83, 0x84, 0x85, 0x87,
		0x88, 0x89, 0x8A, 0x8B, 0x8C, 0x8D, 0x8F,
		0x90, 0x91, 0x92, 0x93, 0x94, 0x95, 0x97,
		0x98, 0x99, 0x9A, 0x9B, 0x9C, 0x9D, 0x9F,
		0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA7,
		0xA8, 0xA9, 0xAA, 0xAB, 0xAC, 0xAD, 0xAF,
		0xB0, 0xB1, 0xB2, 0xB3, 0xB4, 0xB5, 0xB7,
		0xB8, 0xB9, 0xBA, 0xBB, 0xBC, 0xBD, 0xBF:
		return 4

	// ALU A,(HL) = 7 T
	case 0x86, 0x8E, 0x96, 0x9E, 0xA6, 0xAE, 0xB6, 0xBE:
		return 7

	// RET cc = 11 T (taken) / 5 T (not taken)
	case 0xC0: // RET NZ
		if cpu.AF.Lo&0x40 == 0 {
			return 11
		}
		return 5
	case 0xC8: // RET Z
		if cpu.AF.Lo&0x40 != 0 {
			return 11
		}
		return 5
	case 0xD0: // RET NC
		if cpu.AF.Lo&0x01 == 0 {
			return 11
		}
		return 5
	case 0xD8: // RET C
		if cpu.AF.Lo&0x01 != 0 {
			return 11
		}
		return 5
	case 0xE0: // RET PO (PV=0)
		if cpu.AF.Lo&0x04 == 0 {
			return 11
		}
		return 5
	case 0xE8: // RET PE (PV=1)
		if cpu.AF.Lo&0x04 != 0 {
			return 11
		}
		return 5
	case 0xF0: // RET P (S=0)
		if cpu.AF.Lo&0x80 == 0 {
			return 11
		}
		return 5
	case 0xF8: // RET M (S=1)
		if cpu.AF.Lo&0x80 != 0 {
			return 11
		}
		return 5

	// POP rr = 10 T
	case 0xC1, 0xD1, 0xE1, 0xF1:
		return 10

	// JP cc,nn = 10 T (Z80 always fetches the address: same T-state cost taken/not)
	case 0xC2, 0xCA, 0xD2, 0xDA, 0xE2, 0xEA, 0xF2, 0xFA:
		return 10

	// JP nn = 10 T
	case 0xC3:
		return 10

	// CALL cc,nn = 17 T (taken) / 10 T (not taken)
	case 0xC4: // CALL NZ
		if cpu.AF.Lo&0x40 == 0 {
			return 17
		}
		return 10
	case 0xCC: // CALL Z
		if cpu.AF.Lo&0x40 != 0 {
			return 17
		}
		return 10
	case 0xD4: // CALL NC
		if cpu.AF.Lo&0x01 == 0 {
			return 17
		}
		return 10
	case 0xDC: // CALL C
		if cpu.AF.Lo&0x01 != 0 {
			return 17
		}
		return 10
	case 0xE4: // CALL PO
		if cpu.AF.Lo&0x04 == 0 {
			return 17
		}
		return 10
	case 0xEC: // CALL PE
		if cpu.AF.Lo&0x04 != 0 {
			return 17
		}
		return 10
	case 0xF4: // CALL P
		if cpu.AF.Lo&0x80 == 0 {
			return 17
		}
		return 10
	case 0xFC: // CALL M
		if cpu.AF.Lo&0x80 != 0 {
			return 17
		}
		return 10

	// PUSH rr = 11 T
	case 0xC5, 0xD5, 0xE5, 0xF5:
		return 11

	// ALU A,n = 7 T
	case 0xC6, 0xCE, 0xD6, 0xDE, 0xE6, 0xEE, 0xF6, 0xFE:
		return 7

	// RST p = 11 T
	case 0xC7, 0xCF, 0xD7, 0xDF, 0xE7, 0xEF, 0xF7, 0xFF:
		return 11

	// RET = 10 T
	case 0xC9:
		return 10

	// CALL nn = 17 T
	case 0xCD:
		return 17

	// OUT (n),A = 11 T
	case 0xD3:
		return 11

	// EXX = 4 T
	case 0xD9:
		return 4

	// IN A,(n) = 11 T
	case 0xDB:
		return 11

	// EX (SP),HL = 19 T
	case 0xE3:
		return 19

	// JP (HL) = 4 T
	case 0xE9:
		return 4

	// EX DE,HL = 4 T
	case 0xEB:
		return 4

	// DI/EI = 4 T
	case 0xF3, 0xFB:
		return 4

	// LD SP,HL = 6 T
	case 0xF9:
		return 6

	default:
		return 4 // conservative fallback
	}
}

// runZX0Decoder runs the specified decoder binary over a compressed block and
// returns the T-state count and the decompressed bytes.
//
// decoderBin: assembled decoder bytes
// compressed: ZX0-format compressed block
// origLen:    expected decompressed length (for output buffer sizing)
// maxSteps:   step limit (to detect infinite loops; each step is one Z80
//
//	instruction)
//
// Memory layout:
//
//	0x0000–0x03FF  decoder code
//	0x0400         trampoline: CD 00 00 (CALL 0x0000) + 76 (HALT)
//	0x8000         compressed block (HL on entry)
//	0xC000         output buffer (DE on entry)
//	SP = 0x7FFE    stack grows down; ample room above 0x0500
func runZX0Decoder(decoderBin, compressed []byte, origLen int, maxSteps uint64) (tstates uint64, decompressed []byte, err error) {
	mem := &flatRAM{}

	// Load decoder at 0x0000.
	if len(decoderBin) > zx0TrampolineBase {
		return 0, nil, fmt.Errorf("decoder too large (%d B) to fit below 0x%04X", len(decoderBin), zx0TrampolineBase)
	}
	copy(mem.mem[zx0DecoderBase:], decoderBin)

	// Install trampoline: CALL 0x0000 (CD 00 00) ; HALT (76)
	mem.mem[zx0TrampolineBase+0] = 0xCD // CALL nn
	mem.mem[zx0TrampolineBase+1] = 0x00 // lo(0x0000)
	mem.mem[zx0TrampolineBase+2] = 0x00 // hi(0x0000)
	mem.mem[zx0TrampolineBase+3] = 0x76 // HALT

	// Load compressed data at 0x8000 (HL on entry).
	if int(zx0CompressedBase)+len(compressed) > int(zx0OutputBase) {
		return 0, nil, fmt.Errorf("compressed block (%d B) overflows into output area", len(compressed))
	}
	copy(mem.mem[zx0CompressedBase:], compressed)

	cpu := &z80.CPU{Memory: mem, IO: zx0NullIO{}}
	cpu.PC = zx0TrampolineBase       // start at CALL 0x0000
	cpu.HL.SetU16(zx0CompressedBase) // HL = source (compressed data)
	cpu.DE.SetU16(zx0OutputBase)     // DE = destination (output)
	cpu.SP = zx0InitialSP

	var ts uint64
	var steps uint64

	for steps < maxSteps {
		if cpu.HALT {
			break
		}
		ts += tstatesForInstruction(cpu, mem)
		cpu.Step()
		steps++
	}

	if !cpu.HALT {
		return 0, nil, fmt.Errorf("ZX0 decoder exceeded step limit (%d) without HALT (PC=%04X)", maxSteps, cpu.PC)
	}

	out := make([]byte, origLen)
	copy(out, mem.mem[zx0OutputBase:int(zx0OutputBase)+origLen])
	return ts, out, nil
}

// assembleZX0Decoder runs pyz80 to assemble a decoder .asm into a .bin.
// Skips assembly if the .bin already exists.
func assembleZX0Decoder(t *testing.T, tdDir, name string) []byte {
	t.Helper()
	asmPath := filepath.Join(tdDir, name+".asm")
	binPath := filepath.Join(tdDir, name+".bin")

	if _, err := os.Stat(binPath); err != nil {
		cmd := exec.Command("pyz80", "--obj="+binPath, asmPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("pyz80 %s: %v\n%s", name, err, out)
		}
	}
	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read %s: %v", binPath, err)
	}
	return data
}

// TestZX0DecodeBench measures ZX0 standard and turbo decoder speed (T-states
// per decompressed byte) over real corpus blocks, verifying output correctness.
func TestZX0DecodeBench(t *testing.T) {
	root := repoRoot(t)
	tdDir := filepath.Join(root, "tools", "z80-test-harness-go", "testdata")

	standardBin := assembleZX0Decoder(t, tdDir, "dzx0_standard")
	turboBin := assembleZX0Decoder(t, tdDir, "dzx0_turbo")
	megaBin := assembleZX0Decoder(t, tdDir, "dzx0_mega")
	fastBin := assembleZX0Decoder(t, tdDir, "dzx0_fast")

	// Compressed blocks live at build/zx0-blocks/.
	// They are generated by 'make zx0-blocks' (see Makefile).
	blocksDir := filepath.Join(root, "build", "zx0-blocks")
	if _, err := os.Stat(blocksDir); os.IsNotExist(err) {
		t.Fatalf("no ZX0 decode blocks at %s — run 'make zx0-blocks' (needs the external 'zx0' compressor on PATH)", blocksDir)
	}

	type blockResult struct {
		blockSizeKB int
		rawLen      int
		compLen     int
		stdTStates  uint64
		turbTStates uint64
		megaTStates uint64
		fastTStates uint64
	}

	var results []blockResult

	for _, bsz := range []int{1, 2, 4, 8} {
		pattern := filepath.Join(blocksDir, fmt.Sprintf("block_%04dkb_*.zx0", bsz))
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			t.Logf("no blocks for %d KB: skipping", bsz)
			continue
		}

		for _, zx0Path := range matches {
			rawPath := zx0Path[:len(zx0Path)-4] + ".raw"

			rawData, err := os.ReadFile(rawPath)
			if err != nil {
				t.Errorf("read raw %s: %v", rawPath, err)
				continue
			}
			compData, err := os.ReadFile(zx0Path)
			if err != nil {
				t.Errorf("read zx0 %s: %v", zx0Path, err)
				continue
			}

			// Step limit: each decompressed byte costs ~50–5000 T-states,
			// each instruction ~4–21 T-states, so ~10M steps is generous
			// for an 8 KB block.
			const maxSteps = 10_000_000

			// Standard decoder.
			stdTS, stdOut, err := runZX0Decoder(standardBin, compData, len(rawData), maxSteps)
			if err != nil {
				t.Errorf("standard decoder on %s: %v", filepath.Base(zx0Path), err)
				continue
			}
			if string(stdOut) != string(rawData) {
				t.Errorf("standard decoder output MISMATCH for %s: got %d B, want %d B",
					filepath.Base(zx0Path), len(stdOut), len(rawData))
				continue
			}

			// Turbo decoder.
			turbTS, turbOut, err := runZX0Decoder(turboBin, compData, len(rawData), maxSteps)
			if err != nil {
				t.Errorf("turbo decoder on %s: %v", filepath.Base(zx0Path), err)
				continue
			}
			if string(turbOut) != string(rawData) {
				t.Errorf("turbo decoder output MISMATCH for %s: got %d B, want %d B",
					filepath.Base(zx0Path), len(turbOut), len(rawData))
				continue
			}

			// Mega decoder.
			megaTS, megaOut, err := runZX0Decoder(megaBin, compData, len(rawData), maxSteps)
			if err != nil {
				t.Errorf("mega decoder on %s: %v", filepath.Base(zx0Path), err)
				continue
			}
			if string(megaOut) != string(rawData) {
				t.Errorf("mega decoder output MISMATCH for %s: got %d B, want %d B",
					filepath.Base(zx0Path), len(megaOut), len(rawData))
				continue
			}

			// Fast decoder (spke).
			fastTS, fastOut, err := runZX0Decoder(fastBin, compData, len(rawData), maxSteps)
			if err != nil {
				t.Errorf("fast decoder on %s: %v", filepath.Base(zx0Path), err)
				continue
			}
			if string(fastOut) != string(rawData) {
				t.Errorf("fast decoder output MISMATCH for %s: got %d B, want %d B",
					filepath.Base(zx0Path), len(fastOut), len(rawData))
				continue
			}

			results = append(results, blockResult{
				blockSizeKB: bsz,
				rawLen:      len(rawData),
				compLen:     len(compData),
				stdTStates:  stdTS,
				turbTStates: turbTS,
				megaTStates: megaTS,
				fastTStates: fastTS,
			})

			stdTpB := float64(stdTS) / float64(len(rawData))
			turbTpB := float64(turbTS) / float64(len(rawData))
			megaTpB := float64(megaTS) / float64(len(rawData))
			fastTpB := float64(fastTS) / float64(len(rawData))
			stdMs := float64(stdTS) / samClockHz * 1000
			turbMs := float64(turbTS) / samClockHz * 1000
			megaMs := float64(megaTS) / samClockHz * 1000
			fastMs := float64(fastTS) / samClockHz * 1000
			t.Logf("%-38s  raw=%5d  comp=%5d  std: %6.1f T/B  %.3f ms  turbo: %6.1f T/B  %.3f ms  fast: %6.1f T/B  %.3f ms  mega: %6.1f T/B  %.3f ms",
				filepath.Base(zx0Path), len(rawData), len(compData),
				stdTpB, stdMs, turbTpB, turbMs, fastTpB, fastMs, megaTpB, megaMs)
		}
	}

	if len(results) == 0 {
		t.Fatal("no .zx0 reference blocks — run 'make zx0-blocks' (needs the external 'zx0' compressor on PATH)")
	}

	// Per-block-size summary.
	t.Logf("")
	t.Logf("── ZX0 decode speed summary (SAM 6 MHz, uncontended RAM) ──────────────")
	t.Logf("%-10s  %-10s  %-10s  %-10s  %-10s  %-10s  %-10s  %-10s  %-10s  %s",
		"BlkSize", "Std T/B", "Std ms/blk", "Turb T/B", "Turb ms/blk", "Fast T/B", "Fast ms/blk", "Mega T/B", "Mega ms/blk", "N  ratio")

	for _, bsz := range []int{1, 2, 4, 8} {
		var (
			sumStdTS  uint64
			sumTurbTS uint64
			sumMegaTS uint64
			sumFastTS uint64
			sumRaw    int
			sumComp   int
			n         int
		)
		for _, r := range results {
			if r.blockSizeKB != bsz {
				continue
			}
			sumStdTS += r.stdTStates
			sumTurbTS += r.turbTStates
			sumMegaTS += r.megaTStates
			sumFastTS += r.fastTStates
			sumRaw += r.rawLen
			sumComp += r.compLen
			n++
		}
		if n == 0 {
			continue
		}
		avgStdTpB := float64(sumStdTS) / float64(sumRaw)
		avgTurbTpB := float64(sumTurbTS) / float64(sumRaw)
		avgMegaTpB := float64(sumMegaTS) / float64(sumRaw)
		avgFastTpB := float64(sumFastTS) / float64(sumRaw)
		avgStdMs := float64(sumStdTS) / float64(n) / samClockHz * 1000
		avgTurbMs := float64(sumTurbTS) / float64(n) / samClockHz * 1000
		avgMegaMs := float64(sumMegaTS) / float64(n) / samClockHz * 1000
		avgFastMs := float64(sumFastTS) / float64(n) / samClockHz * 1000
		avgRatio := float64(sumComp) / float64(sumRaw)
		t.Logf("%-10s  %-10.1f  %-10.3f  %-10.1f  %-10.3f  %-10.1f  %-10.3f  %-10.1f  %-10.3f  n=%d  ratio=%.3f",
			fmt.Sprintf("%d KB", bsz),
			avgStdTpB, avgStdMs,
			avgTurbTpB, avgTurbMs,
			avgFastTpB, avgFastMs,
			avgMegaTpB, avgMegaMs,
			n, avgRatio)
	}
}
