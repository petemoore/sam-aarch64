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
	"github.com/petemoore/sam-aarch64/tools/sampage"
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

// portKeyMatrix is the SAM keyboard-matrix status port. A keyboard read selects a
// row by writing the row mask to the high byte of the port address (`ld a,<row>;
// in a,(&f9)`) and reads the row's key bits back, active-low (a pressed key reads
// 0). The trinload/dumper Esc poll selects row &F7 and tests bit 5 (Esc), RETing
// when it reads 0. The harness models only the status byte returned on &f9 reads
// (keyMatrix, default 0xFF = no key pressed); the row select is ignored, which is
// faithful for the single-key Esc poll the netboot loops use. SAM Tech Man §"the
// keyboard"; trinload.asm read_loop :89-92.
const portKeyMatrix = 0xF9

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
	pager *sampage.Mem // the one memory model: LMPR/HMPR paging + ROM write-protect
	io    IODevice
	cpu   *z80.CPU // back-reference, for the INI/IND port correction in In
	// tstateCursor is the running Z80 T-state total as of the START of the
	// instruction currently executing. The run loop updates it before each
	// cpu.Step(); an IODevice that implements tStateClocked reads it (via the
	// hand-off in In/Out) to model time-based hardware state such as the Trinity
	// PIC's one-SPI-byte BUSY flag (enc28j60.go gap b). 0 for Call paths that do
	// not maintain it (the device falls back to read-cleared BUSY there).
	tstateCursor uint64
	romActive    bool   // netboot-local fixed-overlay: when true, addr >= romBase is read-only (boot model), layered over the shared pager
	romBase      uint16 // first ROM address (e.g. 0xC000 for ROM1 at boot)
	keyQueue     []byte // injected keypresses (i138 keyboard-sysvar stub)
	keyMatrix    uint8  // byte returned on port &f9 reads (active-low keyboard matrix)

	// detectHardware drives the runtime emulation-vs-hardware probe (i228): a
	// test binary INs emuDetectPort (&7F, an unmapped SAM port) to tell where it
	// runs — on real hardware the floating bus reads 0xFF (confirmed: all probed
	// ports read 0xFF), so the emulator returns a DISTINCT marker (0x00) instead.
	// Default false => the port reads the marker (this IS the emulator). A test
	// sets it true to make the port read 0xFF, exercising the hardware (RET-to-
	// trinload) branch of the terminator. See test_report.asm tr_terminate.
	detectHardware bool

	// access, when non-nil, is invoked on every RAM/ROM data read and write (not
	// instruction fetches per se — Get is the z80.Memory read path, so fetches call
	// it too; callers filter by address). It is the analysis hook used to PROVE
	// which addresses a boot reads/writes (fork-vs-stock ROM study). write=false is
	// a read returning val; write=true is a store of val. Nil for every normal test.
	access func(addr uint16, write bool, val uint8)
}

// SetAccessTrace installs an access hook fired on every data read/write through
// the memory model. Used by the fork-analysis harness to prove address usage.
func (mac *Machine) SetAccessTrace(fn func(addr uint16, write bool, val uint8)) {
	mac.m.access = fn
}

const (
	emuDetectPort   uint8 = 0x7F // unmapped SAM port the i228 probe reads
	emuDetectMarker uint8 = 0x00 // value the emulator returns there (hardware: 0xFF)
)

// peek/poke are the single funnel for every RAM/ROM access: they route through
// the pager so direct memory reads/writes honour the live LMPR/HMPR mapping.
func (m *mem) peek(addr uint16) uint8 { return m.pager.Get(addr) }

func (m *mem) poke(addr uint16, value uint8) {
	if m.romActive && addr >= m.romBase {
		return // boot ROM overlay: the write is dropped, exactly as on hardware
	}
	m.pager.Set(addr, value)
}

// readPhysical reads n bytes from physical RAM starting at the given page (0-31)
// and in-page offset, walking forward across consecutive pages (the SAM's linear
// physical address space: page p offset 16383 is followed by page p+1 offset 0).
// It bypasses LMPR/HMPR paging — the bytes come from the named physical page
// regardless of the live mapping, as HSAVE's UIFA names the source page directly.
// The read is clamped to the end of RAM (32×16 KB); any bytes past it read as zero.
func (m *mem) readPhysical(page byte, offset uint16, n int) []byte {
	out := make([]byte, n)
	base := int(page)*sampage.PageSize + int(offset)
	for i := 0; i < n; i++ {
		pa := base + i
		pg := pa / sampage.PageSize
		if pg >= sampage.NumPages {
			break // past the end of RAM; remaining bytes stay zero
		}
		out[i] = m.pager.RAM[pg][pa%sampage.PageSize]
	}
	return out
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
	v := m.peek(addr)
	if m.access != nil {
		m.access(addr, false, v)
	}
	return v
}

func (m *mem) Set(addr uint16, value uint8) {
	m.poke(addr, value)
	if m.access != nil {
		m.access(addr, true, value)
	}
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
	// Emulation-detect port (&7F, i228): a test binary INs it to tell emulation
	// from real hardware. Real hardware floats the bus high (0xFF); the emulator
	// returns a distinct marker (0x00) so the terminator can branch. detectHardware
	// flips it to 0xFF so a test can exercise the hardware (RET) branch.
	//
	// The marker is tied to the full address &007F — i.e. read via IN A,(C) with
	// B=0 (BC.Hi==0). On real hardware the floating-bus value is address-dependent
	// (the probe characterized &007F == &FF), so a read with a non-zero high byte
	// is a DIFFERENT, uncharacterized port; modelling that here means a regression
	// to the DB-form IN A,(&7F) (which puts A on the high address lines) no longer
	// silently gets the marker — the gap that froze the SAM on the first run.
	if port == emuDetectPort && (m.cpu == nil || m.cpu.BC.Hi == 0) {
		if m.detectHardware {
			return 0xFF
		}
		return emuDetectMarker
	}
	// Keyboard-matrix status port (&f9): the trinload/dumper/serve Esc poll reads
	// it (`ld a,<row>; in a,(&f9); bit 5,a`). Return the modelled matrix byte
	// (default 0xFF = no key pressed, active-low), so the Esc poll falls through
	// "not pressed" by default and a test sets keyMatrix to assert the Esc exit.
	if port == portKeyMatrix {
		return m.keyMatrix
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
	if c, ok := m.io.(tStateClocked); ok {
		c.SetTState(m.tstateCursor)
	}
	return m.io.In(port)
}

// tStateClocked is an optional IODevice capability: a device that models
// time-based hardware state (e.g. the Trinity PIC's one-SPI-byte BUSY flag) reads
// the running T-state cursor handed in just before each In/Out so it can decide
// whether enough time has elapsed. Devices that do not need timing simply omit it.
type tStateClocked interface {
	SetTState(t uint64)
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
		if c, ok := m.io.(tStateClocked); ok {
			c.SetTState(m.tstateCursor)
		}
		m.io.Out(port, value)
	}
}

// Machine is a loaded routine binary plus its symbol map, ready to run.
type Machine struct {
	m       *mem
	symbols map[string]uint16

	// rstHandlers models DOS RST-vector hooks the flat harness has no
	// ROM for. Keyed by the RST target address (e.g. &0008 for RST 8): when the
	// run loop finds PC at a registered target, it pops the return address the RST
	// pushed and invokes the handler, which reads any inline operand byte(s) and
	// returns the PC to resume at. Nil until a handler is attached (e.g.
	// AttachBDOS, bdos_store.go) — every existing test runs with no handler.
	rstHandlers map[uint16]func(cpu *z80.CPU, mac *Machine, retAddr uint16) uint16

	// cont holds the CPU state from the most recent run so Continue can resume the
	// SAME machine — PC, SP, all registers, IFF — in place, exactly where it
	// stopped (at a StopPC). This is the faithful "carry on executing" primitive:
	// unlike RunBootFrom, which resets SP and PC to a synthetic entry, Continue
	// preserves the live call chain, so a routine that stopped mid-flow (e.g. the
	// editor idling at WTKY2 &04FA, blocked on a key) resumes correctly once the
	// precondition changes (e.g. InjectKeys primes the key queue). Nil until run.
	cont *z80.CPU
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
	return &Machine{m: &mem{pager: sampage.New(), keyMatrix: 0xFF}, symbols: map[string]uint16{}}
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
	machine := &Machine{m: &mem{pager: sampage.New(), keyMatrix: 0xFF}, symbols: map[string]uint16{}}
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
		m:       &mem{pager: sampage.New(), romActive: true, romBase: romBase, keyMatrix: 0xFF},
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
	return &Machine{m: &mem{pager: pager, keyMatrix: 0xFF}, symbols: syms}, nil
}

// Pager exposes the SAM paging model so a test can seed RAM pages, load ROM
// fixtures, and inspect the post-run paging state (e.g. assert a routine
// restored LMPR or clobbered a page). It is the harness's one memory model.
func (mac *Machine) Pager() *sampage.Mem { return mac.m.pager }

// Regs is a snapshot of the live Z80 registers, for inspecting CPU state inside a
// Trace callback (which only carries PC). The register file is otherwise opaque.
type Regs struct{ A, B, C, D, E, H, L uint8 }

// LiveRegs returns the live CPU registers during/after a run. Valid inside a Trace
// callback and after a run returns; nil-safe (returns zero Regs) before any run.
func (mac *Machine) LiveRegs() Regs {
	c := mac.m.cpu
	if c == nil {
		return Regs{}
	}
	return Regs{
		A: c.AF.Hi, B: c.BC.Hi, C: c.BC.Lo, D: c.DE.Hi, E: c.DE.Lo,
		H: c.HL.Hi, L: c.HL.Lo,
	}
}

// LiveSP returns the live stack pointer during/after a run. Valid inside a
// Trace callback and after a run returns; nil-safe (returns 0 before any run).
func (mac *Machine) LiveSP() uint16 {
	if mac.m.cpu == nil {
		return 0
	}
	return mac.m.cpu.SP
}

// LiveAltRegs returns the live ALTERNATE CPU registers (AF'/BC'/DE'/HL') during/
// after a run. Valid inside a Trace callback and after a run returns; nil-safe.
// Needed to observe routines that compute in the alternate set after an EXX —
// e.g. B-DOS 1.5t's records math (&A58C divide reads its 32-bit dividend from the
// alt DE':HL'), the i295 overflow trace.
func (mac *Machine) LiveAltRegs() Regs {
	c := mac.m.cpu
	if c == nil {
		return Regs{}
	}
	a := c.Alternate
	return Regs{
		A: a.AF.Hi, B: a.BC.Hi, C: a.BC.Lo, D: a.DE.Hi, E: a.DE.Lo,
		H: a.HL.Hi, L: a.HL.Lo,
	}
}

// LoadROMImage copies a real 32 KB SAM system-ROM image into the pager's ROM
// (ROM0 = low 16 KB at logical &0000 when section A maps ROM; ROM1 = high 16 KB
// at logical &C000 when section D maps ROM). This is the i190a entry for the
// REAL captured patched system ROM (~/sam-archive/samboot-capture/rom.bin): with
// it loaded, a boot trace fetches the patched ROM's actual reset/report-50 code
// (the Trinity boot fetch at &0F7F → &F5DD) instead of a synthetic fixture. The
// image must be exactly sampage.ROMSize (32768) bytes — a short or long image is
// a capture-handling bug, not something to silently pad.
func (mac *Machine) LoadROMImage(image []byte) error {
	if len(image) != sampage.ROMSize {
		return fmt.Errorf("z80: ROM image is %d bytes, want %d (ROM0+ROM1)", len(image), sampage.ROMSize)
	}
	copy(mac.m.pager.ROM[:], image)
	return nil
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

// LoadSymbols merges a pyz80 mapfile's symbols into the machine's symbol table, so
// Sym() resolves them. Used when a .bin is poked into an already-constructed machine
// (e.g. a trinload-pushed program loaded into a RAM page of a real-ROM boot rig) and
// its symbols must be callable by name without rebuilding the Machine. Existing entries
// with the same name are overwritten.
func (mac *Machine) LoadSymbols(mapPath string) error {
	syms, err := parseMap(mapPath)
	if err != nil {
		return err
	}
	if mac.symbols == nil {
		mac.symbols = map[string]uint16{}
	}
	for k, v := range syms {
		mac.symbols[k] = v
	}
	return nil
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

// SetEmuDetectHardware makes the emulation-detect port (&7F, i228) read 0xFF —
// the value real hardware floats — instead of the emulator marker (0x00), so a
// test can exercise the hardware (RET-to-trinload) branch of tr_terminate. Off
// by default (the port reads the marker, since this is the emulator).
func (mac *Machine) SetEmuDetectHardware(b bool) {
	mac.m.detectHardware = b
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

// PressEsc models the operator holding Esc at the SAM keyboard: it clears bit 5
// of the keyboard-matrix status byte (active-low), so the next `in a,(&f9); bit
// 5,a` poll reads 0 (pressed). The trinload/dumper/serve Esc-to-exit poll then
// RETs to trinload. Pass false to release (matrix back to 0xFF = no key pressed).
func (mac *Machine) PressEsc(pressed bool) {
	if pressed {
		mac.m.keyMatrix &^= 0x20 // clear bit 5 (Esc), active-low
	} else {
		mac.m.keyMatrix |= 0x20
	}
}

// SetKeyMatrix sets the raw byte returned on port &f9 reads (active-low keyboard
// matrix; 0xFF = no key pressed). PressEsc is the convenience for the Esc bit.
func (mac *Machine) SetKeyMatrix(b uint8) { mac.m.keyMatrix = b }

// PendingKeys returns the number of injected keys still waiting to be consumed.
// Zero means all injected keys have been read and consumed by the Z80 program
// (each RES 5,(FLAGS) pops one). Used in tests to assert the queue drains.
func (mac *Machine) PendingKeys() int {
	return len(mac.m.keyQueue)
}

// StubReturn makes a CALL to addr behave as an immediate RET in the screenless
// harness: when execution reaches addr the return address the CALL pushed is
// popped and execution resumes there, without running addr's body. It is the
// CALL-target analogue of AttachPrintRecorder/AttachBDOS — for a ROM display
// routine (e.g. CLSLOWER &06B5) that a test path reaches but is not exercising:
// the routine's screen effect is hardware-only (i230), the test verifies the
// surrounding logic. Registering it is harmless for paths that never reach addr.
func (mac *Machine) StubReturn(addr uint16) {
	mac.setRSTHandler(addr, func(cpu *z80.CPU, mac *Machine, retAddr uint16) uint16 {
		return retAddr
	})
}

// ModelReadkey installs a behavioural model of the ROM READKEY routine (&1CB1)
// for the screenless harness: it consumes one injected key (InjectKeys) and
// returns it the way the real ROM does — A = key, NZ, CY when a key is queued;
// A = 0, Z, NC when none. (ROM v3.0 disasm &1CB1: TWOKSC then XOR A / RET for
// "no key — Z,NC", or KYVL / AND A / SCF for "got key — NZ,CY".) READKEY itself
// RST-30s into the paged TWOKSC, which the flat core cannot run, so this faithful
// model lets a WTFK any-key loop (`call &1CB1 / jr z`) terminate in emulation
// once a key has been injected.
func (mac *Machine) ModelReadkey() {
	mac.setRSTHandler(0x1CB1, func(cpu *z80.CPU, mac *Machine, retAddr uint16) uint16 {
		if len(mac.m.keyQueue) > 0 {
			cpu.AF.Hi = mac.m.keyQueue[0]       // A = key code
			mac.m.keyQueue = mac.m.keyQueue[1:] // consume it
			cpu.AF.Lo &^= uint8(z80.FlagZ)      // NZ — a key is ready
			cpu.AF.Lo |= uint8(z80.FlagC)       // CY — "got key"
		} else {
			cpu.AF.Hi = 0                  // A = 0
			cpu.AF.Lo |= uint8(z80.FlagZ)  // Z — no key
			cpu.AF.Lo &^= uint8(z80.FlagC) // NC
		}
		return retAddr
	})
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
	// Trace, when non-nil, is called with the CPU's PC immediately before each
	// instruction is stepped — the observation hook the i190a boot-trace uses to
	// follow the real patched-ROM → EEPROM-fetch → JP &4000 control flow through
	// the captured ROM/EEPROM and record which artifact actually executes (the
	// samboot-bootblock-analysis.md §7.4 contradiction i197c must resolve). It is
	// called for the RST-hook fast path too (with the RST target PC). Keep it
	// cheap: it runs once per instruction.
	Trace func(pc uint16)

	// FrameIntPeriod, when non-zero, makes the run fire a maskable frame
	// interrupt every FrameIntPeriod executed instructions, modelling the SAM's
	// 50 Hz line/frame interrupt. It is delivered through the real CPU interrupt
	// path (koron processInterrupt): when IFF1 is set the CPU pushes PC and
	// vectors to the ROM's handler (&0038 in IM1, or the IM2 vector), so the
	// genuine &0038 KEYSCAN/clock handler RUNS — populating LASTK/FLAGS, advancing
	// FRAMES, etc. Zero (every existing caller) leaves the run interrupt-free,
	// preserving prior behaviour exactly. Used by the samboot-statediff diagnostic
	// to boot the stock v3.0 ROM through its real frame-driven editor wait (whose
	// idle relies on the interrupt populating the keyboard sysvars), so it reaches
	// the SAME editor FLAGS-poll idle as Colin's fork — the precondition for a
	// meaningful RAM diff at a shared sync PC. The minimal i257 EI;HALT-resume
	// (advance past HALT) is independent and still applies between interrupts.
	FrameIntPeriod uint64
}

// CallResult is what a routine returns to the harness.
type CallResult struct {
	BC          uint16 // the BC register at RET (routines return a length in BC)
	DE          uint16 // the DE register at RET (32-bit-result routines return a word here)
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

// RunBootFrom is the raw-address counterpart of RunBoot: it runs from `addr`
// (no symbol lookup) but, like RunBoot, treats the step cap as a normal outcome
// (res.Halted=false, res.PC = the spin/wander site) rather than a hard error. It
// is the i190a boot-trace primitive — start the real patched ROM at reset (PC 0)
// or at the Trinity report-50 fetch (&0F7F) and follow it through the captured
// EEPROM into whatever runs at &4000, where the boot may halt, RET to the trap,
// or wander; all three are observations, not test failures (the failure mode is
// only a genuine fault — undecodable instruction). Pair with Entry.Trace to
// record the PC trail.
func (mac *Machine) RunBootFrom(addr uint16, in Entry) (CallResult, error) {
	return mac.run(fmt.Sprintf("&%04X", addr), addr, in, false)
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
	return mac.runLoop(name, cpu, in, capIsError)
}

// Continue resumes the machine from the CPU state left by the previous run/Continue
// — same PC, SP, registers, and interrupt state — and steps under the new Entry
// (a fresh StopPC/StepCap/Trace). It is the faithful "carry on executing" primitive
// for multi-step interactions where the call chain must survive across stops (e.g.
// the editor idling at WTKY2 then resuming once keys are queued). It errors if no
// prior run has established CPU state.
func (mac *Machine) Continue(in Entry) (CallResult, error) {
	if mac.cont == nil {
		return CallResult{}, fmt.Errorf("z80: Continue with no prior run to resume")
	}
	mac.m.cpu = mac.cont
	return mac.runLoop("continue", mac.cont, in, false)
}

// ContinueFrom resumes the machine from the CPU state left by the previous
// run/Continue — preserving every register (main AND alternate), the interrupt
// state, and the live paging (LMPR/HMPR) — but redirects PC to `addr` and pushes
// the HALT-trap as the return address (so the routine's final RET lands on the
// trap). It is the faithful "call this injected machine code in the booted state"
// primitive: unlike run()/RunBootFrom(), it does NOT reset SP to a synthetic stack
// or zero the registers — the editor-idle / post-RECORD device state (sysvars, the
// selected record, the alternate bank) survives intact. Use it to invoke our
// serve's raw machine-code HWSAD sequence faithfully (i280b-b2t), avoiding the
// §8ae &01CB reboot-artifact contamination of the RunBootFrom(stub) harness.
//
// The injected code must be staged by the caller and must end in a RET (it returns
// to the planted HALT trap). SP is the live editor stack; the trap address is
// pushed onto it, so the code sees a normal call frame.
//
// ContinueFrom is a CALL primitive, not a resume: to resume a run that stopped
// at StepCap/StopPC, use Continue. Chaining ContinueFrom(prev.PC) LOOKS like a
// resume but pushes the trap word onto the live stack each time — if the
// previous run stopped mid-interrupt-handler (or mid any routine about to pop),
// the inserted word shifts every subsequent pop/RET one slot and the unwind
// lands on the halt trap (the netboot_server_faithful_test first-draft wedge).
func (mac *Machine) ContinueFrom(addr uint16, in Entry) (CallResult, error) {
	if mac.cont == nil {
		return CallResult{}, fmt.Errorf("z80: ContinueFrom with no prior run to resume")
	}
	cpu := mac.cont
	mac.m.cpu = cpu
	cpu.SP -= 2
	mac.m.poke(cpu.SP, byte(haltTrap&0xff))
	mac.m.poke(cpu.SP+1, byte(haltTrap>>8))
	mac.m.poke(haltTrap, 0x76) // HALT opcode
	cpu.PC = addr
	return mac.runLoop("continueFrom", cpu, in, false)
}

func (mac *Machine) runLoop(name string, cpu *z80.CPU, in Entry, capIsError bool) (CallResult, error) {
	cap := uint64(maxSteps)
	if in.StepCap != 0 {
		cap = in.StepCap
	}
	var steps, tstates uint64
	halted := false
	stopVisits := 0
	reachedStop := false
	var sinceInt uint64 // instructions since the last frame interrupt was raised
	for {
		// Frame interrupt (opt-in via Entry.FrameIntPeriod): raise a maskable INT
		// every FrameIntPeriod instructions. koron's Step() consumes cpu.Interrupt
		// only when IFF1 is set (a maskable INT is held off while interrupts are
		// disabled, exactly as on hardware), pushing PC and vectoring to the ROM
		// handler (&0038 in IM1). We arm it here and let Step() deliver-or-ignore;
		// if ignored (IFF1=0) it stays armed until the ROM does EI. This runs the
		// genuine &0038 KEYSCAN/clock handler so LASTK/FLAGS/FRAMES are populated.
		if in.FrameIntPeriod != 0 {
			sinceInt++
			if sinceInt >= in.FrameIntPeriod && cpu.Interrupt == nil {
				// Data carries the data-bus byte the CPU latches at INT ack; it is
				// used only by IM2 (vector = I:byte&0xFE) and IM0, and ignored by
				// IM1. The SAM floats the bus high (0xFF) during the frame INT ack,
				// so the IM2 vector is read from I:0xFE — the ROM's IM2 table entry.
				cpu.Interrupt = &z80.Interrupt{Type: z80.IMType, Data: []uint8{0xFF}}
				sinceInt = 0
			}
		}
		if in.Trace != nil {
			in.Trace(cpu.PC)
		}
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
		// A DOS RST hook: the RST already pushed its return address (the
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
		mac.m.tstateCursor = tstates // T-state total at the start of this instruction
		tstates += uint64(t)
		cpu.Step()
		steps++
		if cpu.HALT {
			if !cpu.IFF1 {
				halted = true
				break
			}
			// EI; HALT — waiting for the frame interrupt. On real hardware the
			// 50 Hz frame INT resumes it; model that so frame-timed delay loops
			// (e.g. B-DOS init's &AB17) spin out instead of stopping the run.
			// koron's oopHALT rewinds PC to the HALT opcode, so step past it.
			cpu.HALT = false
			cpu.PC++
		}
		if steps >= cap {
			if capIsError {
				return CallResult{}, fmt.Errorf("z80: routine %q did not return after %d steps (PC=&%04X)", name, steps, cpu.PC)
			}
			break // RunBoot: a spin/forever-loop is a normal outcome
		}
	}
	// Persist the CPU so Continue can resume this exact state in place.
	mac.cont = cpu
	return CallResult{
		BC: cpu.BC.U16(), DE: cpu.DE.U16(), HL: cpu.HL.U16(), A: cpu.AF.Hi,
		Steps: steps, TStates: tstates, PC: cpu.PC, Halted: halted,
		ReachedStop: reachedStop,
	}, nil
}
