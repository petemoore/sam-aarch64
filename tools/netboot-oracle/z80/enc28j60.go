package z80

// enc28j60.go — host-side emulation of the Quazar Trinity Ethernet path, the
// i80 brick that turns the ENC28J60 wire I/O from "gated on hardware" into
// "host-verifiable". It models the Trinity microcontroller (port &DC select +
// bit-3 busy flag + the &DD identity-probe reply) and the ENC28J60 SPI chip
// (port &DE) accurately enough to run the *real* vendored driver,
// src/netboot/encdrv.asm (Simon Owen, simonowen/trinload), under the
// flat-memory koron-go/z80 harness.
//
// SPEC: the emulation is grounded in two authorities —
//   - the ENC28J60 datasheet (Microchip DS39662E): the SPI instruction set
//     (RCR/RBM/WCR/WBM/BFS/BFC/SRC §4.2 Table 4-1), the MAC/MII read-dummy + the
//     MII double-read (§4.2.1 Fig 4-4), the four-bank register file with the
//     0x1B-0x1F common window (§3.1 Table 3-1), the 8 KB RX/TX buffer with
//     AUTOINC pointers (§3.2, ECON2.AUTOINC), the 6-byte receive header
//     (2-byte next-pointer LE + 4-byte RSV LE, byte count in the low 16 bits,
//     §7.2.2 Fig 7-3, incl. the 4-byte CRC), EPKTCNT (0x19 bank 1) + ECON2.PKTDEC
//     (§7.2.1), and the transmit flow (ETXST/ETXND, TXRTS start, EIR.TXIF
//     complete, §7.1);
//   - the driver itself, src/netboot/encdrv.asm, which fixes the exact port
//     usage: &DC select bytes (&21 eon / &20 eoff / &28 ereset / &04 auto-null
//     off / &2F auto-null on), the one-byte SPI read-lag (OUT opcode, OUT dummy,
//     IN value — the microcontroller latches the MISO byte clocked out by the
//     most recent OUT), and the &DD identity probe (&08->'T', &09->'R').
//
// VERIFICATION SCOPE: running encdrv.asm against this emulation and asserting
// the frame bytes in/out are byte-exact IS the host verification (the same
// discipline the packet builders get against the golden vectors). It is NOT
// hardware verification — real Trinity hardware stays the final integration gate
// (trinity-capabilities.md; CLAUDE.md §5: emulation-verified != hardware-
// verified). What this models is the digital register/buffer behaviour the
// driver drives; it does not model analogue PHY/link, real TX timing, the R5
// errata silicon bug, or the receive-engine MAC filtering — it injects frames
// directly into the RX FIFO the way working silicon would after a good receive.

// Trinity port numbers (trinity-capabilities.md §2).
const (
	portTrinityCtl = 0xDC // microcontroller select; IN bit 3 = busy
	portTrinityEEP = 0xDD // EEPROM SPI; also the identity-probe reply port
	portTrinityENC = 0xDE // ENC28J60 SPI data
	portTrinitySD  = 0xDF // SD card SPI (minimal stub)
	portBorder     = 0xFE // SAM border colour — driver writes it during TX/RX
)

// Trinity microcontroller &DC select byte values (encdrv.asm; eeprom.asm).
const (
	selENCEnable  = 0x21 // eon
	selENCDisable = 0x20 // eoff
	selENCPulse   = 0x23 // epulse (disable+enable)
	selENCReset   = 0x28 // ereset (microcontroller-level ENC reset)
	selNullOff    = 0x04 // enulloff (auto-null off)
	selNullOn     = 0x2F // enullon (auto-null on)
	selProbeT     = 0x08 // identity probe: reply 'T' on &DD
	selProbeR     = 0x09 // identity probe: reply 'R' on &DD
	selEEPEnable  = 0x11 // eeprom_enable: CS-assert the flash EEPROM (eeprom.asm)
	selEEPDisable = 0x10 // eeprom_disable: CS-deassert it
)

// ENC28J60 SPI opcode encodings (datasheet Table 4-1: 3-bit op in the top bits,
// 5-bit register address in the low bits; RBM/WBM/SRC are fixed bytes).
const (
	opRCRMask = 0x00 // 000aaaaa — read control register
	opWCRMask = 0x40 // 010aaaaa — write control register
	opBFSMask = 0x80 // 100aaaaa — bit field set (ETH regs only)
	opBFCMask = 0xA0 // 101aaaaa — bit field clear (ETH regs only)
	opRBM     = 0x3A // 00111010 — read buffer memory (fixed)
	opWBM     = 0x7A // 01111010 — write buffer memory (fixed)
	opSRC     = 0xFF // 11111111 — system reset command (fixed)
	addrMask  = 0x1F
)

// ENC28J60 control-register addresses the driver touches (datasheet Table 3-1).
// The 16-bit ETH pointers occupy a low/high address pair (e.g. ERDPT =
// ERDPTL/ERDPTH at 0x00/0x01); the helpers reg16/setReg16 take the low address
// and access the high byte at addr+1, so only the low names are needed here.
//
//	0x00 ERDPTL  / 0x01 ERDPTH   — RX read pointer
//	0x02 EWRPTL  / 0x03 EWRPTH   — TX write pointer
//	0x04 ETXSTL  / 0x05 ETXSTH   — TX packet start
//	0x06 ETXNDL  / 0x07 ETXNDH   — TX packet end
//	0x08 ERXSTL  / 0x09 ERXSTH   — RX FIFO start
//	0x0A ERXNDL  / 0x0B ERXNDH   — RX FIFO end
//	0x0C ERXRDPTL/ 0x0D ERXRDPTH — RX read barrier
const (
	regERDPTL   = 0x00 // bank 0
	regEWRPTL   = 0x02
	regETXSTL   = 0x04
	regETXNDL   = 0x06
	regERXSTL   = 0x08
	regERXNDL   = 0x0A
	regERXRDPTL = 0x0C
	regEPKTCNT  = 0x19 // bank 1
	// Common registers (mirror in every bank at 0x1B-0x1F).
	regEIR   = 0x1C
	regECON2 = 0x1E
	regECON1 = 0x1F
)

// ECON1 / ECON2 / EIR bit positions used by the driver.
const (
	econ1BSEL    = 0x03 // bits 1:0 bank select
	econ1TXRTS   = 0x08 // bit 3 transmit request to send
	econ2AUTOINC = 0x80 // bit 7
	econ2PKTDEC  = 0x40 // bit 6 decrement EPKTCNT
	eirTXIF      = 0x08 // bit 3 transmit done
	eirTXERIF    = 0x02 // bit 1 transmit error
)

const (
	bufSize   = 0x2000 // 8 KB ENC28J60 buffer SRAM (0x0000-0x1FFF)
	rxStartHW = 0x0000 // driver: rx_start
	rxEndHW   = 0x19FF // driver: rx_end (6.5 KB RX); TX is rx_end+1..0x1FFF
)

// ENC28J60 is the emulated Trinity Ethernet path: the microcontroller select
// state, the ENC's four register banks, its 8 KB buffer, and a virtual wire
// (captured TX frames + injectable RX frames). Construct with NewENC28J60 and
// attach with Machine.AttachIO.
type ENC28J60 struct {
	// regs[bank][addr] holds the bank-private registers; common registers
	// (0x1B-0x1F) are stored in regs[0] and aliased across banks on access.
	regs [4][32]byte
	buf  [bufSize]byte

	// SPI transaction state. The microcontroller latches the MISO byte clocked
	// out by the most recent OUT (&DE); the next IN (&DE) returns it (the
	// one-byte read-lag, trinity-capabilities.md §3).
	encSelected bool
	autoNull    bool
	spiByteIdx  int  // bytes clocked since the current selection began
	spiCmd      byte // first byte of the transaction (the opcode)
	spiMISO     byte // latched MISO the next IN (&DE) returns (register reads)
	rbmActive   bool // inside a Read-Buffer-Memory transaction
	probeReply  byte // pending &DD identity-probe reply ('T'/'R')

	// eep is the second device on the Trinity SPI bus: the flash EEPROM the boot
	// wrappers read their MAC+IP from (eeprom.go). It shares &DC (select) and &DD
	// (data) with the identity probe; csAssert/Deassert frame its transactions.
	eep eeprom

	// lastBorder records the most recent OUT (&FE) value. The boot wrappers paint
	// a distinctive border on each outcome (red=bad config, blue=ENC init failed,
	// green=success), so a boot test can read which stage a wrapper reached.
	lastBorder    byte
	borderWritten bool

	// Virtual wire.
	txFrames [][]byte // frames the driver transmitted (control byte stripped)
	rxQueue  [][]byte // frames waiting to be delivered to drv_read

	// rxWritePos is the chip's own RX-FIFO write position — where the receive
	// engine deposits the next incoming packet. It follows the next-packet-
	// pointer chain the driver reads out of each packet header (NOT ERXRDPT,
	// which the driver sets to read_ptr-1 as the R5 errata barrier). Tracking it
	// internally is what makes multi-packet receive correct: each packet's
	// header next-pointer is where the following packet's header lands, exactly
	// as silicon writes them sequentially around the ring.
	rxWritePos int
}

// NewENC28J60 returns a freshly reset emulated Trinity Ethernet path with the
// given frames queued for reception (each a full Ethernet frame WITHOUT the FCS;
// the emulator appends a 4-byte zero CRC as silicon would store it). Inject more
// later with InjectRX.
func NewENC28J60(rxFrames ...[]byte) *ENC28J60 {
	e := &ENC28J60{}
	e.softReset()
	for _, f := range rxFrames {
		e.InjectRX(f)
	}
	return e
}

// InjectRX queues an Ethernet frame (no FCS) for the driver to receive. It is
// delivered to the RX FIFO and EPKTCNT incremented the next time the driver is
// in a position to read it; the emulator materialises it into the buffer lazily
// when the driver reads EPKTCNT.
func (e *ENC28J60) InjectRX(frame []byte) {
	cp := make([]byte, len(frame))
	copy(cp, frame)
	e.rxQueue = append(e.rxQueue, cp)
}

// TXFrames returns the frames the driver has transmitted so far, in order. Each
// is the Ethernet frame the driver placed in the TX buffer (the per-packet
// control byte at ETXST is stripped; the bytes from ETXST+1..ETXND).
func (e *ENC28J60) TXFrames() [][]byte { return e.txFrames }

// LastBorder returns the most recent OUT (&FE) value and whether any border write
// happened — the boot wrappers' outcome signal (red/blue/green).
func (e *ENC28J60) LastBorder() (byte, bool) { return e.lastBorder, e.borderWritten }

func (e *ENC28J60) softReset() {
	for b := range e.regs {
		for i := range e.regs[b] {
			e.regs[b][i] = 0
		}
	}
	for i := range e.buf {
		e.buf[i] = 0
	}
	// AUTOINC defaults set after reset (datasheet ECON2 POR). MISTAT.BUSY
	// (bank 3 reg 0x0A bit 0) reads 0 throughout, so the driver's PHY-write
	// settle poll (wr_phy_wait) exits immediately — the emulated PHY write is
	// instantaneous.
	e.regs[0][regECON2] = econ2AUTOINC
	e.encSelected = false
	e.spiByteIdx = 0
	e.rxWritePos = rxStartHW
}

// --- register access helpers (honour the common-register window) -------------

func (e *ENC28J60) bank() int { return int(e.regs[0][regECON1] & econ1BSEL) }

// regGet reads register `addr` in the current bank, mapping the 0x1B-0x1F common
// window to bank 0's storage.
func (e *ENC28J60) regGet(addr byte) byte {
	if addr >= 0x1B {
		return e.regs[0][addr]
	}
	return e.regs[e.bank()][addr]
}

func (e *ENC28J60) regSet(addr, v byte) {
	if addr >= 0x1B {
		e.regs[0][addr] = v
		return
	}
	e.regs[e.bank()][addr] = v
}

// isMACMII reports whether `addr` in the current bank is a MAC or MII register,
// which an RCR returns with a leading dummy byte (datasheet §4.2.1 Fig 4-4).
// From Table 3-1: bank 2 0x00-0x0B (MACON*/MABBIPG/MAIPG*/MACLCON*/MAMXFL*) and
// 0x12-0x19 (MICMD/MIREGADR/MIWR*/MIRD*); bank 3 0x00-0x05 (MAADR*) and 0x0A
// (MISTAT). Common registers (>=0x1B) are ETH-type (no dummy).
func (e *ENC28J60) isMACMII(addr byte) bool {
	if addr >= 0x1B {
		return false
	}
	switch e.bank() {
	case 2:
		return (addr <= 0x0B) || (addr >= 0x12 && addr <= 0x19)
	case 3:
		return (addr <= 0x05) || addr == 0x0A
	}
	return false
}

// reg16 / setReg16 read/write a little-endian 16-bit value from the bank-0 ETH
// pointer pair at addrLo (addrLo+1 = high byte).
func (e *ENC28J60) reg16(addrLo byte) uint16 {
	return uint16(e.regs[0][addrLo]) | uint16(e.regs[0][addrLo+1])<<8
}
func (e *ENC28J60) setReg16(addrLo byte, v uint16) {
	e.regs[0][addrLo] = byte(v)
	e.regs[0][addrLo+1] = byte(v >> 8)
}

// --- IODevice -----------------------------------------------------------------

// In handles a Z80 IN from a Trinity port. (The harness corrects the Z80 INI/
// IND port register before calling here, so `port` is the true C-register port
// — &DE for the RBM byte stream, &DC for a busy poll, etc.)
func (e *ENC28J60) In(port uint8) uint8 {
	switch port {
	case portTrinityCtl:
		// Busy flag (bit 3): the emulation is never busy, so the driver's
		// wait_ready loops exit immediately.
		return 0x00
	case portTrinityEEP:
		// Shared port: EEPROM read data while a flash read transaction is in its
		// data phase, otherwise the identity-probe reply ('T'/'R').
		if e.eep.inDataPhase() {
			return e.eep.miso
		}
		return e.probeReply
	case portTrinityENC:
		// Inside a Read-Buffer-Memory transaction every IN (&DE) reads and
		// auto-advances the next buffer byte: the driver clocks RBM data with
		// INI (a bare IN under the microcontroller's auto-null), so the buffer
		// byte arrives on the IN itself.
		if e.encSelected && e.rbmActive {
			return e.rbmNext()
		}
		// Otherwise the one-byte read-lag: the MISO byte latched by the most
		// recent OUT (&DE) (a control-register read's dummy clock).
		return e.spiMISO
	case portTrinitySD:
		return 0xFF // SD stub: idle MISO
	}
	return 0xFF
}

// Out handles a Z80 OUT to a Trinity port.
func (e *ENC28J60) Out(port uint8, value uint8) {
	switch port {
	case portTrinityCtl:
		e.ctlSelect(value)
	case portTrinityENC:
		e.spiClock(value)
	case portTrinityEEP:
		// Flash EEPROM SPI data clock (the boot wrappers' config read).
		e.eep.clock(value)
	case portBorder:
		// Record the border colour the boot wrappers paint on each outcome.
		e.lastBorder = value
		e.borderWritten = true
	case portTrinitySD:
		// SD data writes: no model needed here.
	}
}

// ctlSelect handles an OUT to the microcontroller select port (&DC).
func (e *ENC28J60) ctlSelect(v uint8) {
	switch v {
	case selENCEnable:
		// Asserting CS begins a fresh SPI transaction.
		e.encSelected = true
		e.spiByteIdx = 0
		e.spiMISO = 0
		e.rbmActive = false
	case selENCDisable, selENCPulse:
		e.encSelected = false
	case selENCReset:
		// Microcontroller-level ENC reset == ENC28J60 soft reset (SRC).
		e.softReset()
	case selNullOn:
		e.autoNull = true
	case selNullOff:
		e.autoNull = false
	case selProbeT:
		e.probeReply = 'T' // 0x54
	case selProbeR:
		e.probeReply = 'R' // 0x52
	case selEEPEnable:
		e.eep.csAssert() // eeprom_enable: begin a flash transaction
	case selEEPDisable:
		e.eep.csDeassert() // eeprom_disable: end it
	}
}

// ProgramTrinityNetwork lays out the EEPROM so the real eeprom.asm config read
// (find_index for "Trinity Network ", then read_chunk for the matched value)
// returns mac+ip — exactly what smoke_main/client_main read at boot. The chunk is
// placed at index entry 0, so find_index matches it as value 1 and read_chunk
// reads chunk base 0x2000. sam_mac is chunk+0 (6 bytes), sam_ip is chunk+6 (4).
func (e *ENC28J60) ProgramTrinityNetwork(mac [6]byte, ip [4]byte) {
	// Index entry 0 (addr 0): part 1, total 1, name "Trinity Network ".
	entry := make([]byte, eepIndexStride)
	entry[0] = 1 // part
	entry[1] = 1 // total
	copy(entry[2:18], trinityNetworkName)
	e.eep.write(0, entry)
	// Chunk for value 1 (addr eepChunkBase): sam_mac(6), sam_ip(4).
	chunk := make([]byte, 10)
	copy(chunk[0:6], mac[:])
	copy(chunk[6:10], ip[:])
	e.eep.write(eepChunkBase, chunk)
}

// spiClock processes one byte clocked out to the ENC over &DE and latches the
// MISO byte the microcontroller will return on the next IN (&DE). The driver's
// idiom is OUT opcode, OUT dummy(s), IN value — so the response to a read lands
// on the dummy clock(s) after the opcode.
func (e *ENC28J60) spiClock(v uint8) {
	if !e.encSelected {
		return
	}
	if e.spiByteIdx == 0 {
		// First byte of the transaction is the opcode.
		e.spiCmd = v
		e.spiByteIdx = 1
		e.spiMISO = 0 // opcode byte clocks out nothing meaningful
		// RBM data is read on the IN side (see In(portTrinityENC)); flag the
		// transaction so those INs auto-advance the buffer pointer.
		e.rbmActive = (v == opRBM)
		if v == opSRC {
			e.softReset()
		}
		return
	}

	// The opcode pattern decides the command class. RBM/WBM are fixed bytes;
	// RCR/WCR/BFS/BFC carry a 5-bit register address and are matched by their
	// top-3-bit opcode mask.
	op := e.spiCmd
	switch {
	case op == opRBM:
		// RBM data is consumed via IN, not OUT; nothing to do on a stray OUT.
	case op == opWBM:
		// Buffer write: store v at EWRPT and auto-increment.
		e.wbmWrite(v)
	case op&0xE0 == opRCRMask:
		// RCR: a leading dummy byte for MAC/MII regs, then the register value.
		addr := op & addrMask
		// drv_read polls EPKTCNT (bank 1, 0x19) to learn whether a frame is
		// waiting. Materialise the next queued RX frame into the buffer here
		// so the count it reads is accurate — modelling the chip's receive
		// engine delivering a frame into the RX FIFO.
		if addr == regEPKTCNT && e.bank() == 1 && e.regs[1][regEPKTCNT] == 0 {
			e.materialiseRX()
		}
		if e.isMACMII(addr) {
			// byteIdx 1 = dummy (garbage), byteIdx 2 = real data.
			if e.spiByteIdx == 1 {
				e.spiMISO = 0x00 // dummy
			} else {
				e.spiMISO = e.regGet(addr)
			}
		} else {
			// ETH register: data on the first dummy clock after the opcode.
			e.spiMISO = e.regGet(addr)
		}
		e.spiByteIdx++
	case op&0xE0 == opWCRMask:
		// WCR: opcode then exactly one data byte -> write the register.
		addr := op & addrMask
		e.regSet(addr, v)
		e.onRegWrite(addr)
		e.spiByteIdx++
	case op&0xE0 == opBFSMask:
		addr := op & addrMask
		e.regSet(addr, e.regGet(addr)|v)
		e.onRegWrite(addr)
		e.spiByteIdx++
	case op&0xE0 == opBFCMask:
		addr := op & addrMask
		e.regSet(addr, e.regGet(addr)&^v)
		e.onRegWrite(addr)
		e.spiByteIdx++
	default:
		e.spiByteIdx++
	}
}

// onRegWrite reacts to a control-register write that has hardware side effects
// the driver depends on.
func (e *ENC28J60) onRegWrite(addr byte) {
	// A write to ECON1 may have changed the bank or the TX request.
	if addr == regECON1 {
		if e.regs[0][regECON1]&econ1TXRTS != 0 {
			e.doTransmit()
		}
		return
	}
	if addr == regECON2 && e.regs[0][regECON2]&econ2PKTDEC != 0 {
		// PKTDEC: decrement EPKTCNT, then self-clear the bit.
		if e.regs[1][regEPKTCNT] > 0 {
			e.regs[1][regEPKTCNT]--
		}
		e.regs[0][regECON2] &^= econ2PKTDEC
	}
	// PHY writes (the driver's wr_phy_reg sets MIREGADR/MIWR then polls
	// MISTAT.BUSY) settle instantly here: MISTAT stays 0, so wr_phy_wait exits
	// at once. No explicit modelling is needed.
}

// --- buffer read / write (RBM / WBM) -----------------------------------------

// rbmNext returns the byte at ERDPT and advances the read pointer (honouring
// AUTOINC and the circular RX-FIFO wrap at ERXND -> ERXST).
func (e *ENC28J60) rbmNext() byte {
	rd := e.reg16(regERDPTL)
	v := e.buf[rd&0x1FFF]
	if e.regs[0][regECON2]&econ2AUTOINC != 0 {
		rxnd := e.reg16(regERXNDL)
		if rd == rxnd {
			rd = e.reg16(regERXSTL) // wrap to RX start
		} else if rd == 0x1FFF {
			rd = 0x0000
		} else {
			rd++
		}
		e.setReg16(regERDPTL, rd)
	}
	return v
}

// wbmWrite stores v at EWRPT and advances the write pointer (AUTOINC; wrap
// 0x1FFF -> 0x0000).
func (e *ENC28J60) wbmWrite(v byte) {
	wr := e.reg16(regEWRPTL)
	e.buf[wr&0x1FFF] = v
	if e.regs[0][regECON2]&econ2AUTOINC != 0 {
		if wr == 0x1FFF {
			wr = 0x0000
		} else {
			wr++
		}
		e.setReg16(regEWRPTL, wr)
	}
}

// --- transmit -----------------------------------------------------------------

// doTransmit captures the frame the driver staged in the TX buffer (control
// byte at ETXST, payload through ETXND) onto the virtual wire and signals
// completion the way the driver waits for it: EIR.TXIF set, TXERIF clear, TXRTS
// self-cleared. It also writes a benign 7-byte transmit status vector after
// ETXND so the driver's status read sees no late collision.
func (e *ENC28J60) doTransmit() {
	st := e.reg16(regETXSTL)
	nd := e.reg16(regETXNDL)
	// Frame = bytes (ETXST+1 .. ETXND): the byte at ETXST is the per-packet
	// control byte (tx_flags); the frame proper follows.
	frame := make([]byte, 0, int(nd-st))
	for a := int(st) + 1; a <= int(nd); a++ {
		frame = append(frame, e.buf[a&0x1FFF])
	}
	e.txFrames = append(e.txFrames, frame)

	// 7-byte transmit status vector at ETXND+1, all zero (no error, no late
	// collision) — the driver reads 8 bytes of status starting at tx_start+1
	// + length, checks EIR.TXERIF then tx_status+3 bit 5 (late collision).
	for i := 0; i < 7; i++ {
		e.buf[(int(nd)+1+i)&0x1FFF] = 0
	}

	// Completion: TXIF set, TXERIF clear; TXRTS self-clears.
	e.regs[0][regEIR] |= eirTXIF
	e.regs[0][regEIR] &^= eirTXERIF
	e.regs[0][regECON1] &^= econ1TXRTS
}

// --- receive ------------------------------------------------------------------

// materialiseRX delivers the next queued frame into the RX FIFO at the chip's
// own write position (rxWritePos), which follows the next-packet-pointer chain
// the driver reads out of each packet header — NOT ERXRDPT, which the driver
// sets to read_ptr-1 as the R5 errata read barrier. The emulator writes the
// 6-byte header (2-byte next-packet pointer LE + 4-byte RSV LE with the byte
// count in the low 16 bits, including the 4-byte CRC) then the frame + a 4-byte
// zero CRC, and increments EPKTCNT. Called lazily when the driver reads EPKTCNT
// so the frame lands where the driver's read_ptr (== the previous packet's
// next-pointer) will point. This is what makes multi-packet receive correct.
func (e *ENC28J60) materialiseRX() {
	if len(e.rxQueue) == 0 {
		return
	}
	frame := e.rxQueue[0]
	e.rxQueue = e.rxQueue[1:]

	start := e.rxWritePos
	if start < rxStartHW || start > rxEndHW {
		start = rxStartHW
	}

	// On-wire length includes the 4-byte CRC.
	rxLen := len(frame) + 4
	// Next-packet pointer: the byte just past this packet (start + 6-byte header
	// + rxLen), wrapped into the RX ring. The driver reads this out of the
	// header and uses it verbatim as its next read_ptr, so the following packet
	// must be deposited exactly there — which is what advancing rxWritePos to
	// `next` below does. (The real chip keeps the pointer even per the datasheet;
	// the driver's separate errata-odd adjustment applies only to ERXRDPT, the
	// read barrier, not to where the next packet is written.)
	next := start + 6 + rxLen
	if next > rxEndHW {
		next = rxStartHW + (next - rxEndHW - 1)
	}

	pos := start & 0x1FFF
	put := func(b byte) {
		e.buf[pos] = b
		pos = (pos + 1) & 0x1FFF
	}
	// 2-byte next-packet pointer (LE).
	put(byte(next))
	put(byte(next >> 8))
	// 4-byte receive status vector (LE): low 16 bits = received byte count
	// (incl. CRC); bit 23 (Received Ok) set in the high bytes for realism.
	put(byte(rxLen))
	put(byte(rxLen >> 8))
	put(0x80) // RSV bits 16-23: Received Ok (bit 7 of this byte = RSV bit 23)
	put(0x00) // RSV bits 24-31
	// Frame data.
	for _, b := range frame {
		put(b)
	}
	// 4-byte CRC (zeroed; the driver strips it by subtracting 4 from the count).
	for i := 0; i < 4; i++ {
		put(0)
	}

	// Advance the chip's write position to the next-packet pointer just written,
	// so the following frame's header lands where the driver's read_ptr (which
	// it set from this header) will point.
	e.rxWritePos = next
	e.regs[1][regEPKTCNT]++
}
