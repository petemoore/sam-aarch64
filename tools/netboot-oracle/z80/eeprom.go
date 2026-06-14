package z80

// eeprom.go — host-side emulation of the Quazar Trinity flash EEPROM, the second
// device on the Trinity SPI bus (the first is the ENC28J60, enc28j60.go). It
// models enough of the SPI READ protocol to run the *real* vendored config
// reader, src/netboot/eeprom.asm (Colin Piggot's library from simonowen/trinload),
// so the netboot BOOT WRAPPERS (smoke_main / client_main / server_main) — which
// read the SAM's MAC + IP from the "Trinity Network " flash chunk at boot — run
// end-to-end under the harness instead of being excluded as un-emulatable.
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
//
// VERIFICATION SCOPE: running eeprom.asm against this model and confirming the
// boot wrapper reads back the programmed MAC/IP IS the host verification — the
// known-good smoke_main is the control (it reads the same chunk and works on real
// hardware), so a faithful model reproduces its success. It is NOT hardware
// verification (CLAUDE.md §5): it models the digital SPI read behaviour the driver
// drives, not the analogue flash timing or the write/erase path.

// EEPROM address layout the vendored eeprom.asm read protocol implies.
const (
	eepIndexStride = 64     // index entries are 64 bytes: part, total, name(16), desc(46)
	eepChunkBase   = 0x2000 // chunk for value 1 (get_chunk: (28+1*4)<<8); +0x400 per value
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
	addr     int    // current read address (auto-increments in the data phase)
	miso     byte   // byte latched by the most recent data-phase clock (read-lag)
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
}

// csDeassert ends the transaction (driver: OUT &DC,0x10 eeprom_disable).
func (p *eeprom) csDeassert() { p.selected = false }

// clock processes one byte clocked out over &DD: the opcode, the 3 address bytes
// (MSB first), then data-phase dummy clocks. A data-phase clock latches the byte
// at the current address (read-lag) and auto-increments, so the following IN &DD
// returns it.
func (p *eeprom) clock(v byte) {
	if !p.selected {
		return
	}
	switch p.byteIdx {
	case 0: // opcode (0x03 = read); no model needed beyond advancing
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
	default: // data phase: latch store[addr] for the next IN, auto-increment
		p.miso = p.readAt(p.addr)
		p.addr++
		p.byteIdx++
	}
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
