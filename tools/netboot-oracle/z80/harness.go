// Package z80 is a tiny flat-memory Z80 harness for verifying the SAM-side
// netboot routines (src/netboot/*.asm) against the netboot-oracle golden
// vectors. It loads a pyz80-assembled .bin into a 64 KB address space, runs a
// named routine under koron-go/z80 until it returns, and exposes the memory so
// a test can byte-compare the emitted packet against the captured ground truth.
//
// This is the host-verifiable half of the Z80 netboot port: a routine like
// build_udp_frame is pure arithmetic + memory writes, so running it and
// comparing its output buffer to the golden frame proves the port faithful —
// the same byte-for-byte check the Go authority gets (oracle_test.go).
//
// The ENC28J60 wire I/O is host-verifiable too (i80): enc28j60.go emulates the
// Trinity Ethernet path (the microcontroller's port-&DC select/busy + &DD
// identity probe and the ENC28J60 SPI chip on port &DE) so the *real* vendored
// driver (src/netboot/encdrv.asm) runs under this harness with frame bytes
// in/out asserted byte-exact (enc28j60_test.go). What remains NOT host-
// verifiable: an end-to-end Pi boot + real-silicon TX/RX timing — gated on real
// Trinity hardware (the final integration gate; plan §6.2). Emulation-verified
// is not hardware-verified.
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

// IODevice models a port-mapped peripheral. The packet-builder routines do no
// port I/O, so by default the harness has no device and In/Out are inert; the
// i80 Trinity emulation (enc28j60.go) plugs one in to drive the real ENC28J60
// driver (encdrv.asm) host-side.
type IODevice interface {
	In(port uint8) uint8
	Out(port uint8, value uint8)
}

// mem is a flat 64 KB Z80 address space implementing z80.Memory and z80.IO. Port
// I/O is delegated to an optional IODevice (nil => inert, reads return 0xFF).
type mem struct {
	ram [0x10000]byte
	io  IODevice
	cpu *z80.CPU // back-reference, for the INI/IND port correction in In
}

func (m *mem) Get(addr uint16) uint8        { return m.ram[addr] }
func (m *mem) Set(addr uint16, value uint8) { m.ram[addr] = value }

// In returns the byte read from a port. It corrects a Z80 spec-conformance
// quirk in koron-go/z80 v0.10.2 before delegating: the block-input
// instructions INI/INIR/IND/INDR pass the B register as the port
// (cpu.IO.In(cpu.BC.Hi)), whereas the Z80 addresses port C on those — only the
// `IN A,(n)`/`IN r,(C)` forms and OUTI/OTIR use the right register. The
// difference matters here: the Trinity driver's bulk-buffer read (encdrv.asm
// rd_buf_lp) loads B with the byte count and clocks data out of port C (&DE)
// with INI; left uncorrected, the device would see the loop counter as the
// port. We detect an INI-family instruction by inspecting the two opcode bytes
// just executed and substitute the true port (C = cpu.BC.Lo). This corrects the
// emulator at our own boundary for every device, with no ambiguity.
func (m *mem) In(port uint8) uint8 {
	if m.io == nil {
		return 0xff
	}
	if m.cpu != nil && m.isBlockInputPort(port) {
		port = m.cpu.BC.Lo
	}
	return m.io.In(port)
}

// isBlockInputPort reports whether the IN currently being serviced is an
// INI/INIR/IND/INDR (ED A2 / ED B2 / ED AA / ED BA), whose koron-go port is the
// (wrong) B register. At the IO-call point the CPU's PC has advanced past the
// 2-byte opcode, so it sits two bytes after the ED prefix.
func (m *mem) isBlockInputPort(port uint8) bool {
	pc := m.cpu.PC
	if pc < 2 {
		return false
	}
	if m.ram[pc-2] != 0xED {
		return false
	}
	switch m.ram[pc-1] {
	case 0xA2, 0xB2, 0xAA, 0xBA: // INI, INIR, IND, INDR
		// Only correct when B (the koron-go port) actually differs from C; if
		// the driver were genuinely reading port C and B==C this is a no-op.
		return port == m.cpu.BC.Hi
	}
	return false
}

func (m *mem) Out(port uint8, value uint8) {
	if m.io != nil {
		m.io.Out(port, value)
	}
}

// Machine is a loaded routine binary plus its symbol map, ready to run.
type Machine struct {
	m       *mem
	symbols map[string]uint16
}

// New returns an empty machine: a zeroed 64 KB space with no symbols. Used by
// the cycle-counting anchors to stage hand-assembled opcode bytes and RunFrom
// them.
func New() *Machine {
	return &Machine{m: &mem{}, symbols: map[string]uint16{}}
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

// AttachIO plugs a port-mapped peripheral (e.g. the emulated Trinity in
// enc28j60.go) into the machine. Subsequent IN/OUT instructions are routed to
// it. Pass nil to detach.
func (mac *Machine) AttachIO(dev IODevice) {
	mac.m.io = dev
}

// Entry holds the register values a routine reads on entry. The ENC28J60 driver
// takes HL -> MAC / packet buffer and BC = length (for drv_write); the
// packet-builder routines read their inputs from memory parameter blocks and
// ignore these. Zero values leave the register undisturbed for callers that do
// not need them (HL=0/BC=0 are harmless to those routines).
type Entry struct {
	HL uint16
	BC uint16
	DE uint16
	// A preloads the accumulator (A = AF.Hi). Most routines take their inputs in
	// HL/BC/DE or a parameter block, but byte-level leaves (e.g. x25519 mul8) take
	// an operand in A, so register-level tests need to drive it.
	A uint8
	// StepCap overrides the default maxSteps runaway guard for this call. Zero
	// uses maxSteps. The multi-precision crypto routines (e.g. the X25519 field
	// inversion / Montgomery ladder, tens of millions of byte-ops) legitimately
	// run far past the default; they pass a higher cap.
	StepCap uint64
}

// CallResult is what a routine returns to the harness.
type CallResult struct {
	BC      uint16 // the BC register at RET (routines return a length in BC)
	HL      uint16 // the HL register at RET (byte-level leaves return a value in HL)
	A       uint8  // the A register at RET (routines returning a flag/byte use A)
	Steps   uint64
	TStates uint64 // total Z80 cycles executed (cycle-exact; see tstates.go)
}

// Call runs the routine named `entry` to its RET with zeroed entry registers.
// Inputs must already be written into the routine's parameter block via
// Write/WriteU16LE.
func (mac *Machine) Call(entry string) (CallResult, error) {
	return mac.CallEntry(entry, Entry{})
}

// CallEntry runs the routine named `entry` to its RET, with HL/BC/DE preloaded
// from `in`. It sets SP to a safe stack top, pushes the HALT-trap return
// address, plants a HALT there, points PC at the entry, and steps until the
// HALT (the routine's RET landing on the trap) or the step cap.
//
// The ENC28J60 driver entry points read these registers (HL -> MAC for
// drv_init; HL -> packet buffer and BC = length for drv_write/drv_read), so
// CallEntry is how the i80 emulation tests drive the real driver.
func (mac *Machine) CallEntry(entry string, in Entry) (CallResult, error) {
	pc, err := mac.Sym(entry)
	if err != nil {
		return CallResult{}, err
	}
	return mac.run(entry, pc, in)
}

// RunFrom runs from a raw entry address (no symbol lookup) to the HALT trap,
// with HL/BC/DE preloaded from `in`. It is the cycle-counting primitive the
// per-instruction T-state anchors use: stage opcode bytes at an address, RunFrom
// it, and read the measured TStates. The run setup is identical to CallEntry
// (SP, HALT trap, step cap), so the timing is directly comparable.
func (mac *Machine) RunFrom(addr uint16, in Entry) (CallResult, error) {
	return mac.run(fmt.Sprintf("&%04X", addr), addr, in)
}

// run is the shared run loop: it sets SP to a safe stack top, pushes the
// HALT-trap return address, plants the HALT there, points PC at `pc`, and steps
// until the trap (the routine's RET landing on it) or the step cap, accumulating
// steps and T-states. `name` only labels error messages.
func (mac *Machine) run(name string, pc uint16, in Entry) (CallResult, error) {
	cpu := &z80.CPU{Memory: mac.m, IO: mac.m}
	mac.m.cpu = cpu // for the INI/IND port correction in mem.In
	cpu.PC = pc
	cpu.HL.SetU16(in.HL)
	cpu.BC.SetU16(in.BC)
	cpu.DE.SetU16(in.DE)
	cpu.AF.Hi = in.A
	// Stack just below the trap; push the trap as the return address so the
	// routine's RET returns to it.
	cpu.SP = 0x6FFE
	mac.m.ram[0x6FFE] = byte(haltTrap & 0xff)
	mac.m.ram[0x6FFF] = byte(haltTrap >> 8)
	mac.m.ram[haltTrap] = 0x76 // HALT opcode

	cap := uint64(maxSteps)
	if in.StepCap != 0 {
		cap = in.StepCap
	}
	var steps, tstates uint64
	for {
		if cpu.PC == haltTrap {
			break
		}
		// Cost the instruction from the live CPU state BEFORE stepping it, so
		// conditional branches and repeating block ops are timed against the
		// flags / loop counters as they stand on entry to the instruction.
		t, err := instrTStates(mac.m, cpu)
		if err != nil {
			return CallResult{}, fmt.Errorf("z80: routine %q: %w", name, err)
		}
		tstates += uint64(t)
		cpu.Step()
		steps++
		if cpu.HALT {
			break
		}
		if steps >= cap {
			return CallResult{}, fmt.Errorf("z80: routine %q did not return after %d steps (PC=&%04X)", name, steps, cpu.PC)
		}
	}
	return CallResult{BC: cpu.BC.U16(), HL: cpu.HL.U16(), A: cpu.AF.Hi, Steps: steps, TStates: tstates}, nil
}
