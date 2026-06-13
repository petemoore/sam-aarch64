// Package z80 is a tiny flat-memory Z80 harness for verifying the SAM-side
// netboot routines (src/netboot/*.asm) against the netboot-oracle golden
// vectors. It loads a pyz80-assembled .bin into a 64 KB address space, runs a
// named routine under koron-go/z80 until it returns, and exposes the memory so
// a test can byte-compare the emitted packet against the captured ground truth.
//
// This is the host-verifiable half of the Z80 netboot port: a routine like
// build_udp_frame is pure arithmetic + memory writes, so running it and
// comparing its output buffer to the golden frame proves the port faithful —
// the same byte-for-byte check the Go authority gets (oracle_test.go). The wire
// transmission (ENC28J60 I/O) and an end-to-end Pi boot are NOT host-verifiable
// and stay gated on i80 / real Trinity (plan §6.2).
package z80

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/koron-go/z80"
)

// loadOrg is the org address every netboot routine .bin assembles to (&8000,
// matching the standalone-payload idiom used elsewhere in the project).
const loadOrg = 0x8000

// haltTrap is the sentinel return address. Before calling a routine we push it
// as the return address and plant a HALT opcode there, so the routine's final
// RET lands on a HALT and ends the run deterministically. It sits well clear of
// the loaded code and of section C.
const haltTrap = 0x7000

// maxSteps caps a run so a runaway routine fails fast instead of hanging.
const maxSteps = 5_000_000

// mem is a flat 64 KB Z80 address space implementing z80.Memory and z80.IO.
// The netboot packet-builder routines do no port I/O, so In/Out are inert.
type mem struct {
	ram [0x10000]byte
}

func (m *mem) Get(addr uint16) uint8       { return m.ram[addr] }
func (m *mem) Set(addr uint16, value uint8) { m.ram[addr] = value }
func (m *mem) In(port uint8) uint8          { return 0xff }
func (m *mem) Out(port uint8, value uint8)  {}

// Machine is a loaded routine binary plus its symbol map, ready to run.
type Machine struct {
	m       *mem
	symbols map[string]uint16
}

// Load reads a pyz80 .bin (assembled at &8000) into a fresh 64 KB space and
// parses its mapfile (ADDR=NAME text, one per line) for name->address lookup.
func Load(binPath, mapPath string) (*Machine, error) {
	code, err := os.ReadFile(binPath)
	if err != nil {
		return nil, fmt.Errorf("z80: read bin: %w", err)
	}
	if loadOrg+len(code) > 0x10000 {
		return nil, fmt.Errorf("z80: bin of %d bytes overflows from &%04X", len(code), loadOrg)
	}
	machine := &Machine{m: &mem{}, symbols: map[string]uint16{}}
	copy(machine.m.ram[loadOrg:], code)

	syms, err := parseMap(mapPath)
	if err != nil {
		return nil, err
	}
	machine.symbols = syms
	return machine, nil
}

// parseMap reads a pyz80 mapfile: lines of the form "ADDR=NAME" with ADDR in
// uppercase hex. Lines without an '=' are ignored.
func parseMap(path string) (map[string]uint16, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("z80: read map: %w", err)
	}
	defer f.Close()

	out := map[string]uint16{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		addr, err := strconv.ParseUint(line[:eq], 16, 16)
		if err != nil {
			continue
		}
		out[line[eq+1:]] = uint16(addr)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("z80: scan map: %w", err)
	}
	return out, nil
}

// Sym returns the address of a named symbol, or an error if it is absent — a
// missing symbol is a build/wiring bug, not a soft miss.
func (mac *Machine) Sym(name string) (uint16, error) {
	addr, ok := mac.symbols[name]
	if !ok {
		return 0, fmt.Errorf("z80: symbol %q not in map", name)
	}
	return addr, nil
}

// Write copies bytes into memory at addr (e.g. to fill a routine's parameter
// block before calling it).
func (mac *Machine) Write(addr uint16, data []byte) {
	for i, b := range data {
		mac.m.ram[addr+uint16(i)] = b
	}
}

// WriteU16LE writes a 16-bit value little-endian (the Z80 native order) — used
// for pointer/length parameter fields the routine reads with `ld hl,(addr)`.
func (mac *Machine) WriteU16LE(addr, value uint16) {
	mac.m.ram[addr] = byte(value)
	mac.m.ram[addr+1] = byte(value >> 8)
}

// Read returns n bytes of memory starting at addr.
func (mac *Machine) Read(addr uint16, n int) []byte {
	out := make([]byte, n)
	copy(out, mac.m.ram[addr:int(addr)+n])
	return out
}

// CallResult is what a routine returns to the harness.
type CallResult struct {
	BC    uint16 // the BC register at RET (routines return a length in BC)
	Steps uint64
}

// Call runs the routine named `entry` to its RET. It sets SP to a safe stack
// top, pushes the HALT-trap return address, plants a HALT there, points PC at
// the entry, and steps until the HALT (the routine's RET landing on the trap)
// or the step cap. Inputs must already be written into the routine's parameter
// block via Write/WriteU16LE.
func (mac *Machine) Call(entry string) (CallResult, error) {
	pc, err := mac.Sym(entry)
	if err != nil {
		return CallResult{}, err
	}

	cpu := &z80.CPU{Memory: mac.m, IO: mac.m}
	cpu.PC = pc
	// Stack just below the trap; push the trap as the return address so the
	// routine's RET returns to it.
	cpu.SP = 0x6FFE
	mac.m.ram[0x6FFE] = byte(haltTrap & 0xff)
	mac.m.ram[0x6FFF] = byte(haltTrap >> 8)
	mac.m.ram[haltTrap] = 0x76 // HALT opcode

	var steps uint64
	for {
		if cpu.PC == haltTrap {
			break
		}
		cpu.Step()
		steps++
		if cpu.HALT {
			break
		}
		if steps >= maxSteps {
			return CallResult{}, fmt.Errorf("z80: routine %q did not return after %d steps (PC=&%04X)", entry, steps, cpu.PC)
		}
	}
	return CallResult{BC: cpu.BC.U16(), Steps: steps}, nil
}
