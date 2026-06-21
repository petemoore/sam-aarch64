package z80

// eeprom.go — host-side emulation of the Quazar Trinity flash EEPROM (a Microchip
// 25LC1024, 128 KB), the second device on the Trinity SPI bus (the first is the
// ENC28J60, enc28j60.go). It models the SPI READ and page-WRITE protocol of the
// *real* vendored library src/netboot/eeprom.asm (Colin Piggot's, from
// simonowen/trinload), so the netboot BOOT WRAPPERS (smoke_main / client_main /
// server_main) — which read the SAM's MAC + IP from the "Trinity Network " flash
// chunk at boot — run end-to-end under the harness instead of being excluded as
// un-emulatable, and so the destructive write routines (write_chunk / write_index)
// run in emulation before any real hardware flash (i221, gating i135c).
//
// Without this, the boot wrappers were carved out behind `ifndef NETBOOT_HOSTTEST`
// and shipped straight to hardware (the exact bypass CLAUDE.md rule 7 forbids).
//
// SPEC — grounded in the driver itself (eeprom.asm is the authority for the wire
// protocol it expects):
//   - The EEPROM is selected by a microcontroller select write to port &DC:
//     &11 = enable/CS-assert (eeprom_enable), &10 = disable/CS-deassert
//     (eeprom_disable). Data flows over port &DD. The busy poll (&DC bit 3) is
//     handled by the shared ENC28J60.In (always not-busy), so wait_ready exits.
//   - A READ transaction is: opcode 0x03, then a 3-byte address (MSB first), then
//     a stream of dummy clocks each returning the next byte (auto-increment). The
//     driver clocks each data byte with `OUT (&DD),x` then reads it with `INI`
//     (IN &DD); the microcontroller's one-byte read-lag means the byte clocked by
//     an OUT is returned by the following IN (mirrors the ENC's spiMISO latch).
//   - Address scaling differs by routine but lands in one flat store: find_index
//     sends [0,H,L] -> addr = HL (index entries at N*64), read_chunk sends
//     [H,L,0] -> addr = HL<<8 (chunks at (28+value*4)<<8; value 1 -> 0x2000).
//     ProgramTrinityNetwork lays the index entry + chunk at exactly those offsets.
//   - A WRITE transaction is: WREN (opcode 0x06) to set the write-enable latch,
//     then opcode 0x02 + a 3-byte address (write_index sends [0,H,L], write_256
//     sends [H,L,0] — the same scaling as the matching read) + the data bytes
//     clocked in with OUTI. The 256-byte page-write address counter wraps within
//     the page; the latch self-clears when the write completes. write_chunk is four
//     page-aligned write_256 calls (1 KB = 4 pages).
//
// VERIFICATION SCOPE: running eeprom.asm against this model and confirming the
// boot wrapper reads back the programmed MAC/IP IS the host verification — the
// known-good smoke_main is the control (it reads the same chunk and works on real
// hardware), so a faithful model reproduces its success. It is NOT hardware
// verification (CLAUDE.md §5): it models the digital SPI read/write behaviour the
// driver drives, not the analogue flash timing.
//
// WRITE path (i221): the model also implements the 25LC1024 page-WRITE command so
// the real write_chunk / write_index routines (eeprom.asm) run in emulation before
// any destructive hardware flash (i135c). Faithful to the datasheet, NOT bent to
// the driver: a WRITE is ignored unless a preceding WREN set the write-enable latch
// (so a missing WREN is a real failure), and the page-write address counter wraps
// within the 256-byte page. The driver polls the Trinity bridge's busy bit
// (wait_ready, &DC bit 3 — always ready here) and uses a fixed software write_delay
// rather than the EEPROM's RDSR WIP bit, so no WIP state machine is modeled; the
// write commits when the driver de-asserts CS.

// EEPROM address layout the vendored eeprom.asm read protocol implies.
const (
	eepIndexStride = 64     // index entries are 64 bytes: part, total, name(16), desc(46)
	eepChunkBase   = 0x2000 // chunk for value 1 (get_chunk: (28+1*4)<<8); +0x400 per value
)

// 25LC1024 device geometry (the Trinity flash part, per
// docs/notes/samboot-bootblock-analysis.md): a 1 Mbit (128 KB) SPI EEPROM with a
// 17-bit address space and a 256-byte page-write buffer. The driver sends a 3-byte
// (24-bit) address MSB-first; the unused top 7 bits are don't-care, so the
// effective write address wraps modulo the device size.
const (
	eepDeviceSize = 131072 // 128 KB: addresses 0x00000..0x1FFFF (A16..A0)
	eepPageSize   = 256    // the page-write address counter wraps within this boundary
)

// EEPROM SPI command bytes the vendored eeprom.asm drives (25LC1024 datasheet).
const (
	eepCmdWrite = 0x02 // page write — programs the data phase (gated by the WEL)
	eepCmdRead  = 0x03 // read, auto-incrementing across the whole array
	eepCmdWRDI  = 0x04 // reset the write-enable latch
	eepCmdWREN  = 0x06 // set the write-enable latch (must precede every write)
)

// trinityNetworkName is the 16-byte flash chunk name the boot wrappers search for
// (cl_chunk_name / smoke_chunk_name): "Trinity Network " (note the trailing space
// padding it to exactly 16 bytes).
const trinityNetworkName = "Trinity Network "

// eeprom is the SPI EEPROM read state. It is embedded in ENC28J60 (the two share
// the Trinity bus); ENC28J60.In/Out/ctlSelect dispatch the &DD data + &DC select
// to these methods.
type eeprom struct {
	store    []byte // flat backing store; reads past the end return 0
	selected bool   // CS asserted (between &DC 0x11 and 0x10)
	byteIdx  int    // bytes clocked since CS assert (0=opcode, 1..3=address)
	addr     int    // current read/write address (auto-increments in the data phase)
	miso     byte   // byte latched by the most recent data-phase clock (read-lag)
	opcode   byte   // the current transaction's command byte (latched at byteIdx 0)
	wel      bool   // write-enable latch: a WRITE is ignored unless WREN (0x06) set it
	wrote    bool   // a byte was programmed this transaction (clears the WEL on CS-deassert)

	// writeFault, when set, makes programByte drop every data byte even with the
	// WEL armed — a simulated dead write path (bad SPI, stuck CS, failed program
	// cycle). It is the negative control for write-then-verify tests: such a test
	// must report FAIL when writes silently do not stick, proving it can actually
	// fail rather than always pass. Off by default (the faithful chip programs).
	writeFault bool
}

// ensure grows the backing store to at least n bytes (zero-filled).
func (p *eeprom) ensure(n int) {
	if len(p.store) < n {
		grown := make([]byte, n)
		copy(grown, p.store)
		p.store = grown
	}
}

// write places data at a flat store address (used by the Program* helpers).
func (p *eeprom) write(addr int, data []byte) {
	p.ensure(addr + len(data))
	copy(p.store[addr:], data)
}

// csAssert begins a fresh SPI transaction (driver: OUT &DC,0x11 eeprom_enable).
func (p *eeprom) csAssert() {
	p.selected = true
	p.byteIdx = 0
	p.addr = 0
	p.opcode = 0
	p.wrote = false
}

// csDeassert ends the transaction (driver: OUT &DC,0x10 eeprom_disable). A
// completed page-write self-clears the write-enable latch (25LC1024: the WEL
// resets at the end of the internal write cycle) — modeled here, where the
// driver's write_delay lets that cycle finish.
func (p *eeprom) csDeassert() {
	p.selected = false
	if p.opcode == eepCmdWrite && p.wrote {
		p.wel = false
	}
}

// clock processes one byte clocked out over &DD: the command byte, the 3 address
// bytes (MSB first), then data-phase clocks. For a READ each data-phase clock
// latches store[addr] (read-lag) so the following IN &DD returns it; for a WRITE
// each data-phase clock programs store[addr] (page-bounded). WREN/WRDI are bare
// one-byte commands that toggle the write-enable latch.
func (p *eeprom) clock(v byte) {
	if !p.selected {
		return
	}
	switch p.byteIdx {
	case 0: // command byte
		p.opcode = v
		switch v {
		case eepCmdWREN:
			p.wel = true // arm the write-enable latch
		case eepCmdWRDI:
			p.wel = false // disarm it
		}
		p.byteIdx = 1
	case 1: // address [23:16]
		p.addr = int(v) << 16
		p.byteIdx = 2
	case 2: // address [15:8]
		p.addr |= int(v) << 8
		p.byteIdx = 3
	case 3: // address [7:0] — address complete; data phase begins
		p.addr |= int(v)
		p.byteIdx = 4
	default: // data phase
		switch p.opcode {
		case eepCmdRead: // latch store[addr] for the next IN, auto-increment
			p.miso = p.readAt(p.addr)
			p.addr++
		case eepCmdWrite:
			p.programByte(v)
		}
		p.byteIdx++
	}
}

// programByte writes one data-phase byte of a page-write (command 0x02). It is a
// no-op unless the write-enable latch is set — on the 25LC1024 a WRITE with WEL
// clear is ignored entirely, the faithfulness property that makes a missing WREN
// a real failure rather than a silently-accepted write. The address counter
// increments only within the 256-byte page: the low 8 bits wrap while the page
// bits hold, exactly as the device's page buffer does, so a write past the page
// boundary overwrites the start of the same page. The 17-bit device address wraps
// modulo the device size (the command's top 7 address bits are don't-care).
func (p *eeprom) programByte(v byte) {
	if !p.wel || p.writeFault {
		return
	}
	a := p.addr & (eepDeviceSize - 1)
	p.ensure(a + 1)
	p.store[a] = v
	p.wrote = true
	p.addr = (p.addr &^ (eepPageSize - 1)) | ((p.addr + 1) & (eepPageSize - 1))
}

// inDataPhase reports whether an IN &DD should return EEPROM data (vs the ENC's
// identity-probe reply). True once at least one data-phase clock has latched a
// byte.
func (p *eeprom) inDataPhase() bool { return p.selected && p.byteIdx > 4 }

// readAt returns the store byte at addr, or 0 past the end (an unprogrammed cell).
func (p *eeprom) readAt(addr int) byte {
	if addr < 0 || addr >= len(p.store) {
		return 0
	}
	return p.store[addr]
}
