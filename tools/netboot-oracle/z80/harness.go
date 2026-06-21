// Package z80 is a Z80 harness for verifying the SAM-side netboot routines
// (src/netboot/*.asm) against the netboot-oracle golden vectors. It loads a
// pyz80-assembled .bin into a SAM Coupé paged address space (the sampage pager:
// LMPR/HMPR page mapping + ROM write-protect — ONE memory model, no flat model
// beside it; CLAUDE.md §7), runs a named routine under koron-go/z80 until it
// returns, and exposes the memory so a test can byte-compare the emitted packet
// against the captured ground truth. The default config is flat-equivalent
// (contiguous RAM across &0000-&FFFF), so leaf/packet tests that never page see
// a plain 64 KB space; the paths that DO page (trinload's HMPR push, the SAMBOOT
// dumper's ROM reads) get faithful relocation + ROM write-protect.
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
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80/sampage"
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

// SAM keyboard sysvars intercepted by the key-injection stub (i138).
//
// LASTK (0x5C08): last key pressed. SAM Tech Man p.88, confirmed against ROM
// disasm docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt line 1083. The
// Tech Man OCR misprints FLAGS as 503BH at line 4102 — 0x5C3B is authoritative
// (every ROM `LD HL,FLAGS` assembles to 21 3B 5C).
//
// FLAGS (0x5C3B): status flags. Bit 5 (0x20) = key-available. ROM disasm line
// 1090 for the address; KYIP2 lines 1786-1791 for the bit-5 semantics. The SAM
// ROM interrupt sets SET 5,(FLAGS) and LD (LASTK),A (ROM disasm lines 19679-19688).
//
// Precedent: the c0f62fa BASIC-emulation spike uses the same FLAGS/LASTK intercept
// (tools/basic-emulator-spike/main.go, Hardware.Get/Set, ~lines 147-176).
const (
	sysLASTK = 0x5C08 // last key pressed (SAM Tech Man p.88; ROM disasm :1083)
	sysFLAGS = 0x5C3B // status flags; bit 5 (0x20) = key-available (ROM disasm :1090, KYIP2 :1786-1791)
)

// mem is a SAM Coupé paged Z80 address space implementing z80.Memory and z80.IO.
// All RAM/ROM access goes through one model — the sampage pager (LMPR/HMPR page
// mapping + ROM write-protect) — so there is no flat memory model living beside
// the paged one (CLAUDE.md §7; the split is what let the i87a dumper ROM-paging
// bug reach hardware). Port I/O for the paging registers (&FA/&FB) is handled by
// the pager; everything else is delegated to an optional IODevice (nil => inert,
// reads return 0xFF).
//
// By default the pager is in its flat-equivalent config (sampage.New): logical
// &0000-&FFFF maps to four contiguous RAM pages, so leaf-routine and packet
// tests that load code anywhere and write freely behave exactly as a flat 64 KB
// space — they never touch the paging ports, so they never observe the paging.
//
// A boot test can additionally mark the top of memory as read-only via
// romActive+romBase to model the SAM at boot, where &C000-&FFFF is ROM1 until
// the program pages RAM in there: writes at/above romBase are then dropped (as
// on hardware), so an over-size boot image — whose tail would land above &BFFF —
// is not loaded into RAM and a call into that region runs zeros, reproducing the
// real-hardware crash instead of silently working. (The dumper crash repro uses
// the pager's own LMPR-driven ROM1 mapping instead; romActive is the simpler
// fixed overlay the boot wrappers were built against.)
//
// keyQueue holds injected keypresses (InjectKeys). When non-empty, reads of
// sysFLAGS return FLAGS|0x20 (key-available) and reads of sysLASTK return the
// head of the queue. A write to sysFLAGS that clears bit 5 (the Z80 KYIP2
// poll's `RES 5,(FLAGS)` consume step) advances the queue by one.
type mem struct {
	pager     *sampage.Mem // the one memory model: LMPR/HMPR paging + ROM write-protect
	io        IODevice
	cpu       *z80.CPU // back-reference, for the INI/IND port correction in In
	romActive bool     // when true, addr >= romBase is read-only ROM (boot model)
	romBase   uint16   // first ROM address (e.g. 0xC000 for ROM1 at boot)
	keyQueue  []byte   // injected keypresses (i138 keyboard-sysvar stub)
}

// peek/poke are the single funnel for every RAM/ROM access: they route through
// the pager so direct memory reads/writes honour the live LMPR/HMPR mapping.
func (m *mem) peek(addr uint16) uint8 { return m.pager.Get(addr) }

func (m *mem) poke(addr uint16, value uint8) {
	if m.romActive && addr >= m.romBase {
		return // boot ROM overlay: the write is dropped, exactly as on hardware
	}
	m.pager.Set(addr, value)
}

func (m *mem) Get(addr uint16) uint8 {
	// Keyboard-sysvar intercept (i138): when keys are queued, present FLAGS
	// bit 5 (key-available) set and LASTK = head of queue.  The inlined KYIP2
	// poll (key_read_test.asm) reads FLAGS, checks bit 5, reads LASTK, then
	// RES 5,(FLAGS) — the write is caught in Set() to advance the queue.
	// When the queue is empty we fall through to the plain RAM read, so FLAGS
	// bit 5 is whatever the program last stored (no fabricated key).
	if len(m.keyQueue) > 0 {
		switch addr {
		case sysFLAGS:
			return m.peek(addr) | 0x20
		case sysLASTK:
			return m.keyQueue[0]
		}
	}
	return m.peek(addr)
}

func (m *mem) Set(addr uint16, value uint8) {
	m.poke(addr, value)
	// "Key consumed": the inlined KYIP2 poll does RES 5,(HL) on FLAGS after
	// reading LASTK. A write to FLAGS with bit 5 clear while a key is queued
	// means the head key was just consumed — advance the queue.
	if addr == sysFLAGS && len(m.keyQueue) > 0 && value&0x20 == 0 {
		m.keyQueue = m.keyQueue[1:]
	}
}

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
	// Apply the INI/IND block-input port correction FIRST: koron-go passes the B
	// loop-counter as the port on those, so the raw `port` here can be any value
	// (including &FA/&FB) mid-loop. Only after correcting to the true port (C) is
	// it safe to test for a paging register — otherwise a packet-read INI whose
	// counter passes through &FA/&FB would wrongly read LMPR/HMPR instead of the
	// device byte.
	if m.cpu != nil && m.isBlockInputPort(port) {
		port = m.cpu.BC.Lo
	}
	// The paging registers (&FA/&FB) are authoritative from the pager — the
	// dumper does `in a,(HMPR)` (a DB-form IN, never a block input) to read its
	// push page P.
	if v, ok := m.pager.PortIn(port); ok {
		return v
	}
	if m.io == nil {
		return 0xff
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
	if m.peek(pc-2) != 0xED {
		return false
	}
	switch m.peek(pc - 1) {
	case 0xA2, 0xB2, 0xAA, 0xBA: // INI, INIR, IND, INDR
		// Only correct when B (the koron-go port) actually differs from C; if
		// the driver were genuinely reading port C and B==C this is a no-op.
		return port == m.cpu.BC.Hi
	}
	return false
}

func (m *mem) Out(port uint8, value uint8) {
	// Apply the paging registers (&FA/&FB) to the pager so LMPR/HMPR writes
	// actually remap pages. We still forward to the device too: the ENC28J60
	// model records HMPR writes (LastHMPR) for the trinload test, and the pager
	// ignores every non-paging port, so a single funnel serves both.
	m.pager.PortOut(port, value)
	if m.io != nil {
		m.io.Out(port, value)
	}
}

// Machine is a loaded routine binary plus its symbol map, ready to run.
type Machine struct {
	m       *mem
	symbols map[string]uint16

	// rstHandlers models SAMDOS/B-DOS RST-vector hooks the flat harness has no
	// ROM for. Keyed by the RST target address (e.g. &0008 for RST 8): when the
	// run loop finds PC at a registered target, it pops the return address the RST
	// pushed and invokes the handler, which reads any inline operand byte(s) and
	// returns the PC to resume at. Nil until a handler is attached (e.g.
	// AttachBDOS, bdos_store.go) — every existing test runs with no handler.
	rstHandlers map[uint16]func(cpu *z80.CPU, mac *Machine, retAddr uint16) uint16
}

// setRSTHandler registers fn as the handler for the RST whose target is addr
// (e.g. &0008 for the B-DOS RST 8 hook dispatch). See rstHandlers.
func (mac *Machine) setRSTHandler(addr uint16, fn func(cpu *z80.CPU, mac *Machine, retAddr uint16) uint16) {
	if mac.rstHandlers == nil {
		mac.rstHandlers = map[uint16]func(*z80.CPU, *Machine, uint16) uint16{}
	}
	mac.rstHandlers[addr] = fn
}

// New returns an empty machine: a zeroed 64 KB space with no symbols. Used by
// the cycle-counting anchors to stage hand-assembled opcode bytes and RunFrom
// them.
func New() *Machine {
	return &Machine{m: &mem{pager: sampage.New()}, symbols: map[string]uint16{}}
}

// LoadAt is Load with an explicit load origin (org). trinload assembles to
// &6000, not the &8000 the netboot routines use, so its test loads it here.
// Flat all-RAM (no boot ROM model): trinload pages RAM into the top 32K via HMPR
// at runtime, so &8000-&FFFF is RAM for it, not the boot-time ROM1.
func LoadAt(binPath, mapPath string, org uint16) (*Machine, error) {
	code, err := os.ReadFile(binPath)
	if err != nil {
		return nil, fmt.Errorf("z80: read bin: %w", err)
	}
	if int(org)+len(code) > 0x10000 {
		return nil, fmt.Errorf("z80: bin of %d bytes overflows from &%04X", len(code), org)
	}
	machine := &Machine{m: &mem{pager: sampage.New()}, symbols: map[string]uint16{}}
	for i, b := range code {
		machine.m.poke(org+uint16(i), b)
	}
	syms, err := parseMap(mapPath)
	if err != nil {
		return nil, err
	}
	machine.symbols = syms
	return machine, nil
}

// Load reads a pyz80 .bin (assembled at &8000) into a fresh 64 KB space and
// parses its mapfile (ADDR=NAME text, one per line) for name->address lookup.
func Load(binPath, mapPath string) (*Machine, error) {
	return LoadAt(binPath, mapPath, loadOrg)
}

// LoadBoot loads a bootable netboot .bin the way the SAM does at boot, modelling
// the memory map the flat Load ignores: the image is placed from &8000, but
// &romBase-&FFFF is ROM1 (read-only) at boot, so only the bytes that land below
// romBase are written into RAM — the tail above is dropped, exactly as on
// hardware (the boot loader cannot write ROM1). Running a boot wrapper
// (client_main / smoke_main) under this loader therefore exercises the real boot
// path AND reproduces the over-size crash (a call into the un-loaded tail runs the
// zero-filled ROM region and wanders) that the flat all-RAM Load silently hides.
//
// romBase is typically 0xC000 (ROM1 at boot). The ROM region is left zero-filled
// (the cheap first cut: it catches the over-size class and lets the wrapper's
// own code/data below romBase run; loading real ROM contents so the wrapper's ROM
// calls — RST 16 print etc. — also run is a later refinement).
func LoadBoot(binPath, mapPath string, romBase uint16) (*Machine, error) {
	code, err := os.ReadFile(binPath)
	if err != nil {
		return nil, fmt.Errorf("z80: read boot bin: %w", err)
	}
	if loadOrg+len(code) > 0x10000 {
		return nil, fmt.Errorf("z80: boot bin of %d bytes overflows from &%04X", len(code), loadOrg)
	}
	machine := &Machine{
		m:       &mem{pager: sampage.New(), romActive: true, romBase: romBase},
		symbols: map[string]uint16{},
	}
	// Copy only the bytes that land in RAM (below romBase); the tail above romBase
	// hits ROM1 and is never written, as on hardware (poke drops it via romActive).
	for i, b := range code {
		addr := loadOrg + i
		if addr >= int(romBase) {
			break
		}
		machine.m.poke(uint16(addr), b)
	}

	syms, err := parseMap(mapPath)
	if err != nil {
		return nil, err
	}
	machine.symbols = syms
	return machine, nil
}

// LoadPaged loads an &8000-org'd .bin into the single physical RAM page selected
// by hmpr (its low 5 bits) and parses its map, with LMPR/HMPR set to the given
// values. It is the paged counterpart of Load for code that must run under a
// specific paging state — the trinload-pushed dumper, which lives at section C
// with HMPR = the push page P (trinload's X packet does `out (HMPR),P; jp &8000`).
//
// Because section C (&8000-&BFFF) is exactly one 16 KB page, an &8000-org'd image
// is placed at offset 0 of that page (logical &8000+i maps to section-C offset i),
// so the whole image must fit in one page; New/Load are the flat-config loaders
// for everything else. The caller seeds ROM fixtures, scratch pages, and other
// pages via Pager() before running.
func LoadPaged(binPath, mapPath string, lmpr, hmpr uint8) (*Machine, error) {
	code, err := os.ReadFile(binPath)
	if err != nil {
		return nil, fmt.Errorf("z80: read paged bin: %w", err)
	}
	if len(code) > sampage.PageSize {
		return nil, fmt.Errorf("z80: paged bin of %d bytes exceeds one 16 KB section-C page", len(code))
	}
	pager := sampage.New()
	pager.LMPR = lmpr
	pager.HMPR = hmpr
	copy(pager.RAM[int(hmpr&0x1F)][:], code) // &8000-org'd => section-C page, offset 0
	syms, err := parseMap(mapPath)
	if err != nil {
		return nil, err
	}
	return &Machine{m: &mem{pager: pager}, symbols: syms}, nil
}

// Pager exposes the SAM paging model so a test can seed RAM pages, load ROM
// fixtures, and inspect the post-run paging state (e.g. assert a routine
// restored LMPR or clobbered a page). It is the harness's one memory model.
func (mac *Machine) Pager() *sampage.Mem { return mac.m.pager }

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
		mac.m.poke(addr+uint16(i), b)
	}
}

// WriteU16LE writes a 16-bit value little-endian (the Z80 native order) — used
// for pointer/length parameter fields the routine reads with `ld hl,(addr)`.
func (mac *Machine) WriteU16LE(addr, value uint16) {
	mac.m.poke(addr, byte(value))
	mac.m.poke(addr+1, byte(value>>8))
}

// Read returns n bytes of memory starting at addr.
func (mac *Machine) Read(addr uint16, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = mac.m.peek(addr + uint16(i))
	}
	return out
}

// AttachIO plugs a port-mapped peripheral (e.g. the emulated Trinity in
// enc28j60.go) into the machine. Subsequent IN/OUT instructions are routed to
// it. Pass nil to detach.
func (mac *Machine) AttachIO(dev IODevice) {
	mac.m.io = dev
}

// InjectKeys appends keys to the keyboard queue (i138 keyboard-sysvar stub).
// Each byte is delivered to the Z80 via the LASTK/FLAGS sysvar intercept: while
// the queue is non-empty, reads of FLAGS (0x5C3B) return FLAGS|0x20
// (key-available) and reads of LASTK (0x5C08) return the head of the queue; a
// write to FLAGS that clears bit 5 (the inlined KYIP2 consume step) pops the
// head. Mirrors the c0f62fa BASIC-emulation spike key-queue design.
func (mac *Machine) InjectKeys(keys []byte) {
	mac.m.keyQueue = append(mac.m.keyQueue, keys...)
}

// PendingKeys returns the number of injected keys still waiting to be consumed.
// Zero means all injected keys have been read and consumed by the Z80 program
// (each RES 5,(FLAGS) pops one). Used in tests to assert the queue drains.
func (mac *Machine) PendingKeys() int {
	return len(mac.m.keyQueue)
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
	// StopPC, when non-zero, stops the run when execution reaches this address
	// after skipping the first StopPCSkip occurrences. Used to detect a return to
	// a routine's entry: run from `start` with StopPC=start, StopPCSkip=1 stops on
	// the SECOND arrival at `start` (the first is the initial entry) — i.e. when a
	// pushed program RETs back into trinload. Zero disables it.
	StopPC     uint16
	StopPCSkip int
}

// CallResult is what a routine returns to the harness.
type CallResult struct {
	BC          uint16 // the BC register at RET (routines return a length in BC)
	HL          uint16 // the HL register at RET (byte-level leaves return a value in HL)
	A           uint8  // the A register at RET (routines returning a flag/byte use A)
	Steps       uint64
	TStates     uint64 // total Z80 cycles executed (cycle-exact; see tstates.go)
	PC          uint16 // the PC where the run stopped (the spin site if it did not halt)
	Halted      bool   // true if the run reached a HALT / RET-to-trap; false if it span out
	ReachedStop bool   // true if the run stopped at Entry.StopPC (not a halt/cap)
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
	return mac.run(entry, pc, in, true)
}

// RunBoot runs a bootable entry point (client_main / smoke_main / server_main)
// the way the SAM does at boot — but, unlike CallEntry, it does NOT treat the
// step cap as a hard error. A boot wrapper either halts (its `di; halt` on a
// fail/success border) or loops forever (a server/serve loop, or a HANG). So
// RunBoot returns a CallResult either way: res.Halted distinguishes a clean halt
// from a spin, and res.PC is the address it stopped/span at — the diagnostic that
// localises a boot-path hang. (Pair it with a device's TXFrames/LastBorder to see
// how far the wrapper got.) It still errors on a genuine fault (bad symbol,
// undecodable instruction).
func (mac *Machine) RunBoot(entry string, in Entry) (CallResult, error) {
	pc, err := mac.Sym(entry)
	if err != nil {
		return CallResult{}, err
	}
	return mac.run(entry, pc, in, false)
}

// RunFrom runs from a raw entry address (no symbol lookup) to the HALT trap,
// with HL/BC/DE preloaded from `in`. It is the cycle-counting primitive the
// per-instruction T-state anchors use: stage opcode bytes at an address, RunFrom
// it, and read the measured TStates. The run setup is identical to CallEntry
// (SP, HALT trap, step cap), so the timing is directly comparable.
func (mac *Machine) RunFrom(addr uint16, in Entry) (CallResult, error) {
	return mac.run(fmt.Sprintf("&%04X", addr), addr, in, true)
}

// run is the shared run loop: it sets SP to a safe stack top, pushes the
// HALT-trap return address, plants the HALT there, points PC at `pc`, and steps
// until the trap (the routine's RET landing on it) or the step cap, accumulating
// steps and T-states. `name` only labels error messages. When capIsError is true
// (the leaf-routine callers) a step-cap is a hard error — a runaway routine is a
// bug; when false (RunBoot) the cap is a normal outcome (a forever-loop / hang)
// returned as a CallResult with Halted=false.
func (mac *Machine) run(name string, pc uint16, in Entry, capIsError bool) (CallResult, error) {
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
	mac.m.poke(0x6FFE, byte(haltTrap&0xff))
	mac.m.poke(0x6FFF, byte(haltTrap>>8))
	mac.m.poke(haltTrap, 0x76) // HALT opcode

	cap := uint64(maxSteps)
	if in.StepCap != 0 {
		cap = in.StepCap
	}
	var steps, tstates uint64
	halted := false
	stopVisits := 0
	reachedStop := false
	for {
		if in.StopPC != 0 && cpu.PC == in.StopPC {
			stopVisits++
			if stopVisits > in.StopPCSkip {
				reachedStop = true
				break
			}
		}
		if cpu.PC == haltTrap {
			halted = true
			break
		}
		// A SAMDOS/B-DOS RST hook: the RST already pushed its return address (the
		// inline hook-code byte). Pop it, let the handler read the inline operand(s)
		// + apply the side effect, and resume where it returns — so the flat harness
		// runs hook-dispatching code (e.g. the client write-out) it has no ROM for.
		if h, ok := mac.rstHandlers[cpu.PC]; ok {
			ret := uint16(mac.m.peek(cpu.SP)) | uint16(mac.m.peek(cpu.SP+1))<<8
			cpu.SP += 2
			cpu.PC = h(cpu, mac, ret)
			steps++
			if steps >= cap {
				if capIsError {
					return CallResult{}, fmt.Errorf("z80: routine %q did not return after %d steps (PC=&%04X)", name, steps, cpu.PC)
				}
				break
			}
			continue
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
			halted = true
			break
		}
		if steps >= cap {
			if capIsError {
				return CallResult{}, fmt.Errorf("z80: routine %q did not return after %d steps (PC=&%04X)", name, steps, cpu.PC)
			}
			break // RunBoot: a spin/forever-loop is a normal outcome
		}
	}
	return CallResult{
		BC: cpu.BC.U16(), HL: cpu.HL.U16(), A: cpu.AF.Hi,
		Steps: steps, TStates: tstates, PC: cpu.PC, Halted: halted,
		ReachedStop: reachedStop,
	}, nil
}
