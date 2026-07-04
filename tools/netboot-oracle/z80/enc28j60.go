package z80

import "fmt"

// Trinity-HW provenance (i273): this model is DERIVED FROM the SAM/Colin
// authority recorded in tools/trinity-authority-ledger.txt; it is never itself
// the authority for real Trinity hardware behaviour (CLAUDE.md rule 8 /
// feedback_port_diff_authority_first).
//
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
// driver drives, through ONE shared microcontroller (the i235 hardware-parity
// refactor): a peripheral-select MUX, ONE global auto-null mode, a real one-SPI-
// byte BUSY flag, and ONE shared read-back latch the three data ports alias — plus
// the ENC receive filter (ERXFCON), ENCINT on &DC bit 0, the &38 0/1/2 init code,
// configurable write-protect, the &23 CS pulse, and the &02/&03 PUSH/POP slot. It
// does not model analogue PHY/link, the exact &28 50µs settle, the R5 errata
// silicon bug, or LED colour — those are Genuinely-unspecified or cosmetic
// (docs/specs/trinity-emulation-fidelity.md).

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
	selIdentBase  = 0x08 // IDENT string read: &08..&0F return the 8 chars of trinityIdent
	selProbeT     = 0x08 // &08 = 1st IDENT char 'T' (the driver's chk_trinity reads this)
	selProbeR     = 0x09 // &09 = 2nd IDENT char 'R'
	selIdentTop   = 0x0F // &0F = 8th IDENT char
	selEEPEnable  = 0x11 // eeprom_enable: CS-assert the flash EEPROM (eeprom.asm)
	selEEPDisable = 0x10 // eeprom_disable: CS-deassert it
	selEEPNullOn  = 0x1F // EEPROM auto-null on (manual:40; %00011111 — EEPROM select-with-autonull)
	selPushByte   = 0x02 // PUSH stored read-byte (manual:37; ISR save of the pending IN byte)
	selPopByte    = 0x03 // POP stored read-byte (manual:38; restore)
)

// peripheral identifies which of the three SPI devices the shared microcontroller
// has currently MUX-selected (manual:27; gap d). Selecting one deselects the
// others — hardware is one PIC on one SPI bus with a single active chip-select,
// not three independent CS lines.
type peripheral int

const (
	periphNone peripheral = iota // nothing selected (post-&04 / power-on)
	periphEEP                    // EEPROM (&11 / &1F)
	periphENC                    // ENC28J60 (&21 / &2F)
	periphSD                     // SD/flash (&31 / &3F)
)

func (p peripheral) String() string {
	switch p {
	case periphEEP:
		return "EEPROM"
	case periphENC:
		return "ENC"
	case periphSD:
		return "SD"
	default:
		return "none"
	}
}

// statusBusy is &DC bit 3 (BUSY): the PIC raises it for one SPI byte after each
// OUT to &DC/&DD/&DE/&DF, and it clears on the next IN (&DC) status read
// (manual:16,20,21,22,111,124; gap b). While set, a further OUT is silently
// dropped (manual:22) — so a driver that omits the busy-poll between two OUTs
// loses the second, reproducing the real failure instead of silently passing.
const statusBusy = 0x08

// busyByteTStates is the nominal one-SPI-byte BUSY duration in Z80 T-states (gap b).
// The manual gives no figure ("momentarily busy", manual:111), so this is a nominal
// value, not a fabricated precise timing (the "genuinely unspecified" fence). It is
// chosen larger than a single OUT (C),r (12 T) so two BACK-TO-BACK OUTs with no
// intervening poll drop the second (the missing-busy-poll failure), yet small
// enough that the canonical poll (IN &DC, which also clears BUSY explicitly) and
// any real inter-OUT code gap let it clear.
const busyByteTStates = 16

// sdInitSettleTStates bounds the post-&38 PIC settle window (gap b/#35). The
// manual gives ONE settle figure — ~50µs after a heavy controller operation
// (manual:70,112,132; the &28 ENC reset shares it) — and nothing finer; the exact
// per-operation duration is Genuinely-unspecified (trinity-emulation-fidelity.md
// §"Genuinely unspecified"). At the SAM's ~6 MHz CPU clock 50µs is ~300 T-states
// (6e6 * 50e-6 = 300). We use a conservative 4× margin (1200 T-states) so the
// window comfortably covers the documented settle plus any modelling slack, yet is
// VASTLY shorter than the real elapsed time between a faithful boot's SD-init and
// its next identity probe (measured ~192k Z80 instructions in the full B-DOS
// auto-load path, i288). Beyond this budget the PIC has settled, so the identity
// read-back is honoured (FRESH) — silicon settles in real time and never latches
// "unsettled" forever, which is what a real SAM proves by booting through exactly
// this sequence. Within the budget a probe issued before settling still reads
// stale, preserving the i242/i287 back-to-back catch.
const sdInitSettleTStates = 1200

// statusENCINT is &DC bit 0 (ENCINT): mirrors the ENC's pending-interrupt state
// (manual:19,99,100; gap 5). The supported v1.1 polling path reads it here.
const statusENCINT = 0x01

// trinityIdent is the 8-byte firmware IDENT string the microcontroller returns over
// &DD for select commands &08..&0F (manual "Trinity - Ident"). The 4th char is a
// SPACE (verified from the source scan IMG_20260617_162601). chk_trinity only reads
// the first two ('T','R'); the full string is modelled for fidelity.
const trinityIdent = "TRI v1.1"

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
	regERXFCON  = 0x18 // bank 1 — receive filter control (gap 8)
	regEPKTCNT  = 0x19 // bank 1
	// Common registers (mirror in every bank at 0x1B-0x1F).
	regEIR   = 0x1C
	regECON2 = 0x1E
	regECON1 = 0x1F
)

// ERXFCON receive-filter bits (datasheet §8.1, Table 8-1; gap 8). The driver does
// not write ERXFCON, so it stays at the POR default the model installs on reset.
const (
	erxfcUCEN = 0x80                         // unicast: accept frames whose dest MAC == MAADR
	erxfcBCEN = 0x01                         // broadcast: accept FF:FF:FF:FF:FF:FF
	erxfcMCEN = 0x02                         // multicast: accept frames with the group bit set
	erxfcPOR  = erxfcUCEN | erxfcBCEN | 0x20 /*CRCEN*/ // 0xA1 datasheet power-on default
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

	// --- shared microcontroller (PIC) state (gaps a-d) ---------------------------
	//
	// Real Trinity is ONE PIC on ONE SPI bus, not three independent devices. These
	// fields model the shared controller; the per-device engines (eep/sd and the ENC
	// register/buffer model below) are the SPI back-ends it multiplexes onto.
	//
	//   selPeriph    — which peripheral is MUX-selected (gap d): selecting one
	//                  deselects the others.
	//   autoNullMode — ONE global auto-null flag (gap a), with autoNullTarget the
	//                  peripheral it auto-clocks; set by &1F/&2F/&3F, cleared only
	//                  by &04. Mutually exclusive across peripherals.
	//   busy         — &DC bit 3 (gap b): set on every OUT to &DC/&DD/&DE/&DF,
	//                  cleared on the next IN (&DC). An OUT while busy is dropped.
	//   lastClockedIn — ONE shared read-back latch (gap c): every IN &DD/&DE/&DF
	//                  returns this byte, written by whichever peripheral last
	//                  clocked one. Reproduces the manual's two-OUT-two-IN aliasing
	//                  trap (manual:4,8,34,125).
	//   savedReadByte/savedReadValid — the &02/&03 PUSH/POP one-deep ISR slot
	//                  (manual:37,38).
	selPeriph        peripheral
	autoNullMode     bool
	autoNullTarget   peripheral
	lastClockedIn    byte
	lastClockedValid bool       // a peripheral has clocked at least one byte since reset
	lastClockedBy    peripheral // which peripheral last wrote the shared read-back latch
	savedReadByte    byte
	savedReadValid   bool

	// Interleave-rule enforcement (manual:IMG_20260617_162617; DISCOVERY_REPORT §7):
	// the three data ports &DD/&DE/&DF alias ONE shared microcontroller read latch.
	// After a peripheral MUX switch you must clock the NEWLY-selected peripheral
	// before reading the latch, else IN returns the PREVIOUS peripheral's stale byte
	// ("do not interleave OUTs to different peripherals before reading back"). The
	// real SAM blindly returns the stale byte; with StrictInterleave set the model
	// records the violation (count + a human description) so a diagnostic can catch
	// the exact instruction (the harness correlates the PC). Default off so existing
	// tests are unaffected. &DC (control/status) is exempt — it is always readable.
	StrictInterleave   bool
	interleaveViols    int
	interleaveFirstMsg string

	// BUSY timing (gap b). tNow is the running Z80 T-state cursor handed in by the
	// harness (SetTState) just before each In/Out. An OUT to &DC/&DD/&DE/&DF raises
	// BUSY for one SPI-byte time: busyUntilT = tNow + busyByteTStates. BUSY reads set
	// while tNow < busyUntilT; an OUT issued while still busy is dropped (manual:22).
	// An IN (&DC) status poll also releases it early (the canonical wait_ready clears
	// it). With no T-state cursor (Call paths, tNow stays 0) BUSY is purely
	// read-cleared, so those paths are unaffected.
	tNow       uint64
	busyUntilT uint64

	// StuckBusy, when true, models a wedged controller whose BUSY bit never clears
	// (the real-hardware hang mode). Off by default; a test sets it to prove the
	// SD-path busy-wait is bounded. See isBusy.
	StuckBusy bool

	// sdInitSettling models the real-silicon divergence behind the i242 drv_init
	// failure: the heavy &38 SD-init leaves the shared PIC settling, so a subsequent
	// ENC identity-select (&08..&0F, used only by chk_trinity, which does NOT
	// busy-poll) is not honoured and the identity read returns stale — chk_trinity
	// sees != 'TR' and reports the board missing (drv_init -> BC=0 -> blue
	// pm_fail_init). It makes any SD-before-ENC drv_init ordering bug FAIL in
	// emulation instead of only on hardware (CLAUDE.md rule 7).
	//
	// ALWAYS-ON (i244): every &38 SD-init sets it, so EVERY test exercises the post-
	// &38 settle and an SD-before-ENC ordering bug is a build-time catch — it was
	// opt-in until serve_main was reordered (csd_set_bd_records before drv_init crashed
	// a real SAM, 2026-06-24) and the client write path was confirmed clean. The
	// CORRECT order (drv_init's chk_trinity against a quiescent PIC, before any SD
	// work) never trips it: the ENC reset in drv_init's RX-arm body (&28) clears the
	// flag, and after the startup SD read the boot wrapper re-arms the ENC
	// (enc_rx_reestablish, which resets too).
	//
	// BOUNDED SETTLE (i289): the flag clears on EITHER of two events — an &28 ENC
	// reset (the driver-driven quiesce, faithful) OR the elapse of a real settle
	// window (sdInitSettleTStates from the &38). The latch is NOT indefinite: real
	// silicon settles in ~50µs and never stays "unsettled" forever (Pete's real SAM
	// boots through this exact &38-then-no-&28 sequence). settleUntilT is the T-state
	// deadline (e.tNow at the &38 + sdInitSettleTStates); once e.tNow reaches it the
	// identity probe reads FRESH again. Without this bound the flag latched forever in
	// a faithful full boot (B-DOS's auto-load does the &38 with NO following &28 —
	// ~192k instructions, 0 resets, before the next probe; i288), manufacturing a
	// FALSE stale-read failure that does NOT happen on hardware. Affects ONLY the
	// identity probe — SD/EEPROM/ENC-data paths are untouched, so the SD read/write
	// tests (which run drv_init first) are unaffected.
	sdInitSettling bool
	settleUntilT   uint64

	// rxDisarmed models the ONE thing that disturbs the ENC28J60's autonomous receive
	// engine: an ENC reset (&28 ereset). It is NOT set by an ordinary SD device mux-
	// select. This is the authority-grounded correction of the earlier "any SD select
	// disarms RX" model (i293).
	//
	// Trinity-HW provenance (i273): the authority is the ENC28J60 datasheet
	// (~/git/notes/pxe/Microchip ENC28J60.pdf) and Simon Owen's driver
	// (~/git/trinload/encdrv.asm), diffed line-by-line:
	//   - RX is enabled ONCE, by drv_init setting ECON1.RXEN (encdrv.asm:91; datasheet
	//     §7.2 "Receiving Packets": "Enable reception by setting ECON1.RXEN"). RXEN is an
	//     internal ENC register bit; once set, "packets which pass the current filter ...
	//     will be written into the receive buffer" (datasheet ECON1.RXEN description) —
	//     the RX hardware fills the circular FIFO AUTONOMOUSLY, with no Z80 involvement
	//     (datasheet §3.2.1 RECEIVE BUFFER: "a circular FIFO buffer managed by hardware
	//     ... the hardware will automatically write the next byte ... the receive
	//     hardware will never write outside the boundaries of the FIFO").
	//   - The Trinity &DC byte selects which SPI peripheral the Z80 TALKS TO; it cannot
	//     touch the ENC chip's internal ECON1.RXEN. drv_read (encdrv.asm:99) only sets
	//     banks, reads EPKTCNT, and streams the buffer — it NEVER re-enables RX, because
	//     RX was never disabled by reading. eon/eoff (encdrv.asm:396/401) toggle ONLY the
	//     &DC mux-select byte (%00100001 / %00100000) — harmless to RXEN. The SD driver
	//     (src/netboot/sd_csd.asm) drives ONLY the SD byte path (sdc_out/sdc_in); it
	//     issues no ECON1 write and no &28, so an SD CMD17/CMD24 leaves RXEN untouched and
	//     the ENC keeps receiving into its ring across the SD transaction.
	//   - The ONLY &DC op that disturbs RX is &28 ereset (encdrv.asm:411 -> the ENC SRC
	//     soft reset), called solely at init/exit (drv_init:29, drv_exit:257) — never per
	//     access. A reset re-runs the whole RX-arm body, so it both clears and re-arms.
	// So the faithful model: SD selects do NOT disarm RX; only &28 does (and &28 also
	// re-arms). enc_rx_reestablish (which begins with &28) therefore re-arms — but a
	// program that omits a per-block re-arm is NOT penalised, because the SD op never
	// disarmed RX in the first place (the authority above). The earlier "serve dies after
	// the first SD read" model perturbed RX on every SD select; that was a guess about a
	// failure mode, not grounded in the datasheet/driver, and over-penalised a correct
	// no-re-arm program. While set (only after a literal &28 with no following arm),
	// materialiseRX delivers nothing (EPKTCNT stays 0 -> drv_read returns BC=0).
	//
	// (The SEPARATE sdInitSettling &38-settle model — the i242/i287 SD-before-ENC
	// drv_init ordering catch — is a DIFFERENT, legitimate mechanism and is unaffected.)
	rxDisarmed bool

	// SPI transaction state for the ENC SPI back-end. The microcontroller latches
	// the MISO byte clocked out by the most recent OUT (&DE); the next IN (&DE)
	// returns it via lastClockedIn (the one-byte read-lag, trinity-capabilities.md §3).
	spiByteIdx int  // bytes clocked since the current selection began
	spiCmd     byte // first byte of the transaction (the opcode)
	spiMISO    byte // ENC SPI MISO the most recent OUT (&DE) clocked out (register reads)
	rbmActive  bool // inside a Read-Buffer-Memory transaction
	probeReply byte // pending &DD identity-probe reply ('T'/'R')

	// LED state (gap 12, manual:57-64,113-121). The PIC's %11fedcba LED-twinkle band
	// (&C0..&FF) is cosmetic — no driver reads it back — so the model accepts and
	// records the last LED command for a fidelity trace rather than ignoring the
	// write outright (which would mis-route a &C0..&FF byte through the select switch).
	lastLED    byte
	ledWritten bool

	// SD deselect-tail observation (gap 10, bdos:40). The proven SD transaction close
	// is the ordered &DC/&DF sequence &30 -> (dummy &DF write) -> &30 -> &04. The
	// controller tracks the close as it happens; lastSDCloseProper records whether
	// the most recent SD deselect followed that exact order, sdCloseStep walks it.
	// This is an OBSERVABLE for a fidelity test (LastSDCloseProper), not an enforced
	// gate — enforcing a hard failure would break the probe's shorter (&30 -> &04)
	// tail, which is hardware-proven to work for a read-only transaction.
	sdCloseStep        int
	lastSDCloseProper  bool
	sdCloseObservedAny bool

	// eep is the second device on the Trinity SPI bus: the flash EEPROM the boot
	// wrappers read their MAC+IP from (eeprom.go). It shares &DC (select) and &DD
	// (data) with the identity probe; csAssert/Deassert frame its transactions.
	eep eeprom

	// sd is the third device on the Trinity SPI bus: the SD card (sdcard.go),
	// sharing &DC (select) with the SD &31/&30/&38/&3F select bytes and &DF (data).
	// It is INERT until a CSD is configured (AttachSD), so tests with no card see
	// the pre-existing &DF=0xFF idle stub unchanged.
	sd SDCard

	// lastBorder records the most recent OUT (&FE) value. The boot wrappers paint
	// a distinctive border on each outcome (red=bad config, blue=ENC init failed,
	// green=success), so a boot test can read which stage a wrapper reached.
	lastBorder    byte
	borderWritten bool

	// lastHMPR records the most recent OUT (&FB) value (the HMPR page trinload
	// selects in its @/X handlers). The flat harness does not relocate on it (no
	// paging model), but recording it verifies trinload parsed and applied the page
	// byte. True paging fidelity stays hardware-gated.
	lastHMPR    byte
	hmprWritten bool

	// PHY link-up model (i127, hardware-confirmed 2026-06-19). On real silicon the
	// ENC28J60's 10BASE-T link is down for a while after init (link-pulse
	// detection; the datasheet gives no duration and the chip has no auto-
	// negotiation), and a transmit issued before the link is up is SILENTLY lost
	// (TXRTS clears + TXIF sets, but the bytes never egress — modelled in
	// doTransmit). ops counts SPI port accesses as a proxy for elapsed time; the
	// link is up once ops reaches linkUpAfterOps. Default 0 = up immediately (the
	// behaviour every pre-i127 test relies on); a test sets it >0 to model the
	// delay and exercise both the wait-for-link path and the silent-wire drop.
	ops            int
	linkUpAfterOps int
	// firstTXOps is the value of ops when the first frame actually egressed (-1
	// until then). It is the op-count at which a proactive transmitter put its
	// first frame on the wire — exactly the moment an un-fixed client (no
	// drv_wait_link) would transmit — so a test can pin the link-down window to
	// straddle it (FirstTXOps). It survives SetLinkUpAfterOps resets so it can be
	// read after a full run.
	firstTXOps int

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
	e := &ENC28J60{firstTXOps: -1}
	e.softReset()
	for _, f := range rxFrames {
		e.InjectRX(f)
	}
	return e
}

// AttachSD configures the emulated SD card with the given 16 CSD register bytes
// and arms the SD-SPI model: from this point the &DC SD selects (&31/&30/&38/&3F)
// and the &DF data port drive a real command/response state machine (sdcard.go),
// so Colin's B-DOS init ladder can run CMD0..CMD9 against it. Without this call
// the card stays inert and &DF returns the idle &FF stub (every pre-existing test
// is unaffected). Returns the *SDCard for any further configuration.
func (e *ENC28J60) AttachSD(csd [16]byte) *SDCard {
	e.sd.SetCSD(csd)
	return &e.sd
}

// SD returns the embedded SD card model (for tests that want to read its state).
func (e *ENC28J60) SD() *SDCard { return &e.sd }

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

// LastHMPR returns the most recent OUT (&FB) value (the RAM page trinload's @/X
// handlers selected) and whether any HMPR write happened. The flat harness does
// not relocate on it (no paging model), but recording it verifies trinload parsed
// and applied the page byte. True paging fidelity stays hardware-gated.
func (e *ENC28J60) LastHMPR() (byte, bool) { return e.lastHMPR, e.hmprWritten }

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
	// ERXFCON power-on default (datasheet §8.1: UCEN+CRCEN+BCEN = 0xA1; gap 8). The
	// driver does not write ERXFCON, so the receive filter stays at this default:
	// unicast-to-our-MAC + broadcast. materialiseRX honours it.
	e.regs[1][regERXFCON] = erxfcPOR
	e.spiByteIdx = 0
	e.rbmActive = false
	e.rxWritePos = rxStartHW
}

// rxFilterPass reports whether a received frame passes the ENC's ERXFCON receive
// filter (gap 8, datasheet §8.1; manual:57,95,96). ERXFCON==0 is sniffer/
// promiscuous mode (accept all, manual:96). Otherwise a frame is accepted if it is
// broadcast (BCEN), multicast (MCEN, group bit set), or unicast to our MAADR
// (UCEN). The MAADR bytes are stored by the driver in the order [4,5,2,3,0,1]
// (trinload:§1b), so our MAC = regs[3]{0x04,0x05,0x02,0x03,0x00,0x01}.
func (e *ENC28J60) rxFilterPass(frame []byte) bool {
	f := e.regs[1][regERXFCON]
	if f == 0 {
		return true // sniffer mode: receive everything
	}
	if len(frame) < 6 {
		return false
	}
	dst := frame[:6]
	isBroadcast := dst[0] == 0xFF && dst[1] == 0xFF && dst[2] == 0xFF &&
		dst[3] == 0xFF && dst[4] == 0xFF && dst[5] == 0xFF
	if f&erxfcBCEN != 0 && isBroadcast {
		return true
	}
	if f&erxfcMCEN != 0 && dst[0]&0x01 != 0 && !isBroadcast {
		return true // group (multicast) bit set
	}
	if f&erxfcUCEN != 0 {
		our := [6]byte{
			e.regs[3][0x04], e.regs[3][0x05], e.regs[3][0x02],
			e.regs[3][0x03], e.regs[3][0x00], e.regs[3][0x01],
		}
		if dst[0] == our[0] && dst[1] == our[1] && dst[2] == our[2] &&
			dst[3] == our[3] && dst[4] == our[4] && dst[5] == our[5] {
			return true
		}
	}
	return false
}

// --- shared microcontroller (PIC) helpers (gaps a-d) -------------------------

// selectPeripheral is the MUX (gap d): selecting one peripheral deselects the
// others. It records the active peripheral and drives the per-device CS so the
// SPI back-ends frame their transactions correctly.
func (e *ENC28J60) selectPeripheral(p peripheral) {
	if e.selPeriph == p {
		return
	}
	// Deselect whatever was active (CS-deassert the old back-end).
	switch e.selPeriph {
	case periphEEP:
		e.eep.csDeassert()
	case periphENC:
		// ENC CS-deassert ends the current SPI command (resets the byte counter).
		e.spiByteIdx = 0
		e.rbmActive = false
	case periphSD:
		e.sd.deselect()
	}
	e.selPeriph = p
}

// encSelected reports whether the ENC is the MUX-selected peripheral.
func (e *ENC28J60) encSelected() bool { return e.selPeriph == periphENC }

// autoNullFor reports whether the global auto-null mode is on and targets p
// (gap a). Only the selected peripheral can be the auto-null target.
func (e *ENC28J60) autoNullFor(p peripheral) bool {
	return e.autoNullMode && e.autoNullTarget == p
}

// SetTState receives the running Z80 T-state cursor just before each In/Out (the
// harness's tStateClocked hook). It drives the time-based BUSY model (gap b). The
// harness restarts the cursor at 0 for each run (Call/RunBoot); a cursor that jumps
// BACKWARDS means a fresh run began, so any in-flight BUSY window is stale — clear
// it (the PIC has long since finished the prior run's last byte).
//
// The identity-probe settle window survives the run boundary ONLY while still
// open in the old timeline — that is the i242 back-to-back catch (SD traffic at
// the end of one run, drv_init at the start of the next; on hardware those are
// microseconds apart). A window that had ALREADY elapsed before the boundary is
// spent: carrying its absolute deadline into the new (smaller) timeline would
// re-arm it for the whole next run, pinning chk_trinity stale in a way real
// silicon cannot (the i327 faithful-boot artifact — B-DOS's boot-time SD
// traffic ends ~192k instructions before the boot run stops at WTKY2, yet a
// later pushed tool's drv_init read the leftover deadline as still-pending).
func (e *ENC28J60) SetTState(t uint64) {
	if t < e.tNow {
		e.busyUntilT = 0
		if e.sdInitSettling {
			if e.tNow >= e.settleUntilT {
				e.sdInitSettling = false // already elapsed in the old timeline: spent
			} else {
				// Still open at the boundary: re-base the absolute deadline onto
				// the new (smaller) timeline, preserving ONLY the remaining budget
				// (settleUntilT - tNow). Carrying the old absolute deadline would
				// re-arm the window for the whole next run — pinning the next
				// run's probes stale (the i327 artifact in a narrower shape, PR
				// 820 §3 residual). t + remaining keeps the microseconds-apart
				// i242 back-to-back catch without the whole-run overhang.
				e.settleUntilT = t + (e.settleUntilT - e.tNow)
			}
		}
	}
	e.tNow = t
}

// raiseBusy marks the PIC busy for one SPI-byte time after an OUT to
// &DC/&DD/&DE/&DF (gap b): busyUntilT = now + busyByteTStates.
func (e *ENC28J60) raiseBusy() { e.busyUntilT = e.tNow + busyByteTStates }

// isBusy reports whether the one-SPI-byte BUSY window is still open at the current
// T-state cursor (gap b). On Call paths with no T-state cursor (tNow stays 0) BUSY
// is released by clearBusy on the first IN, so isBusy reflects only the explicit
// flag there.
//
// StuckBusy models a wedged Trinity controller whose BUSY bit (&DC bit 3) NEVER
// clears — the real-hardware failure mode that hung the SAM (a per-byte busy-poll
// with no timeout spins forever). It lets a test prove the SD-path busy-wait is
// now bounded: against a stuck controller the probe must terminate, not spin.
func (e *ENC28J60) isBusy() bool { return e.StuckBusy || e.tNow < e.busyUntilT }

// clearBusy releases BUSY (the canonical wait_ready poll: IN &DC clears it, and any
// IN lets a byte-time elapse).
func (e *ENC28J60) clearBusy() { e.busyUntilT = 0 }

// clockedIn records the byte a peripheral just clocked through the controller —
// the ONE shared read-back latch every IN &DD/&DE/&DF returns (gap c).
func (e *ENC28J60) clockedIn(b byte) {
	e.lastClockedIn = b
	e.lastClockedValid = true
	e.lastClockedBy = e.selPeriph // which peripheral wrote the shared read latch
}

// spiMISOByte returns the ENC SPI MISO byte the most recent OUT (&DE) clocked out
// (a control-register read's data lands here on the dummy clock after the opcode).
func (e *ENC28J60) spiMISOByte() byte { return e.spiMISO }

// LastLED returns the most recent LED-twinkle command (%11fedcba, &C0..&FF) and
// whether any was issued. The LED band is cosmetic (no driver reads it back), so
// this is for a fidelity trace only (gap 12, manual:57-64).
func (e *ENC28J60) LastLED() (byte, bool) { return e.lastLED, e.ledWritten }

// LastSDCloseProper reports whether an SD deselect-tail close has been observed and
// whether the most recent one followed the proven ordered sequence (gap 10,
// bdos:40): &30 -> dummy &DF write -> &30 -> &04. It is an OBSERVABLE for a fidelity
// test, not an enforced gate (a shorter &30 -> &04 close still works for a read).
func (e *ENC28J60) LastSDCloseProper() (proper, observed bool) {
	return e.lastSDCloseProper, e.sdCloseObservedAny
}

// trackSDClose walks the proven SD deselect-tail state machine (gap 10): the
// &DC-write order &30, then (onData=true) a dummy &DF write, then &30, then &04.
// onDC marks a &DC select write; onData=false (from clockData) marks a &DF write.
// A correct full sequence sets lastSDCloseProper; any out-of-order step resets it.
func (e *ENC28J60) trackSDClose(v uint8, onDC bool) {
	if onDC {
		switch {
		case v == selSDDeselct && e.sdCloseStep == 0: // first &30
			e.sdCloseStep = 1
		case v == selSDDeselct && e.sdCloseStep == 2: // second &30 (after the dummy)
			e.sdCloseStep = 3
		case v == selNullOff && e.sdCloseStep == 3: // closing &04
			e.lastSDCloseProper = true
			e.sdCloseObservedAny = true
			e.sdCloseStep = 0
		case v == selNullOff && e.sdCloseStep != 3:
			// A bare &04 close (no preceding &30/dummy/&30): observed but not proper.
			if e.sdCloseStep != 0 {
				e.lastSDCloseProper = false
				e.sdCloseObservedAny = true
			}
			e.sdCloseStep = 0
		default:
			// Any other &DC write mid-close aborts the tracked tail.
			if v != selSDDeselct && v != selNullOff {
				e.sdCloseStep = 0
			}
		}
		return
	}
	// A &DF data write: only valid as step 1->2 (the single dummy clock between the
	// two &30 deselects).
	if e.sdCloseStep == 1 {
		e.sdCloseStep = 2
	}
}

// ctlStatus is the &DC Trinity Status Register byte (%1100BWFE): fixed top nibble
// 0xC0, then bit3 BUSY (gap b), bit2 WRITE, bit1 FLASH (card present), bit0 ENCINT
// (gap 5). The SD model supplies the card-present + write-protect bits; the ENC
// supplies ENCINT (its EIR interrupt-pending state surfaced on the status byte).
func (e *ENC28J60) ctlStatus() uint8 {
	s := e.sd.ctlStatus()
	if e.isBusy() {
		s |= statusBusy
	} else {
		s &^= statusBusy
	}
	if e.encINTPending() {
		s |= statusENCINT
	}
	return s
}

// encINTPending reports whether the ENC has an interrupt pending the v1.1 polling
// path reads on &DC bit 0 (gap 5, manual:19,99,100). The ENC's EIR (common reg
// 0x1C) holds the interrupt flags; an interrupt is pending when any unmasked EIR
// flag is set. The driver polls this rather than using the (unfitted v1.1) NMI/INT
// jumper. We surface PKTIF (a frame waiting in the RX FIFO) and the EIR bits the
// model already tracks (TXIF/TXERIF), gated by EIE.INTIE + the per-source enables —
// but since the driver's supported path simply tests "any pending", we report the
// raw EIR-nonzero OR a queued/materialised RX packet.
func (e *ENC28J60) encINTPending() bool {
	// EIR flags the model sets (TXIF on transmit-complete, etc.).
	if e.regs[0][regEIR] != 0 {
		return true
	}
	// A received packet waiting to be read (EPKTCNT>0) drives PKTIF on real silicon;
	// surface it so the polling path sees RX interrupts. A queued-but-not-yet-
	// materialised frame also counts (it will raise EPKTCNT on the next read).
	return e.regs[1][regEPKTCNT] > 0 || len(e.rxQueue) > 0
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
	e.ops++
	switch port {
	case portTrinityCtl:
		// Trinity Status Register (%1100BWFE): fixed top nibble 0xC0, then bit3 BUSY,
		// bit2 WRITE, bit1 FLASH (card present), bit0 ENCINT. Sample BUSY for THIS read
		// (so a status poll sees it set the first time, like the real one-SPI-byte
		// flag), then clear it: any IN lets a byte-time elapse, releasing BUSY (gap b).
		s := e.ctlStatus()
		e.clearBusy()
		return s
	case portTrinityEEP, portTrinityENC, portTrinitySD:
		// All three data ports alias ONE shared read-back latch (gap c, manual:4,8,34,
		// 125): IN &DD/&DE/&DF returns the last byte clocked through the controller by
		// whichever peripheral was selected. In auto-null mode a bare IN on the
		// selected peripheral also clocks the NEXT byte (the PIC injects the dummy),
		// so the latch advances; in manual mode it returns the byte the prior OUT
		// latched (the one-byte read-lag). Any IN also clears BUSY (a byte-time
		// elapses), so only two BACK-TO-BACK OUTs with no intervening read drop the
		// second (manual:22) — the missing-busy-poll failure the model reproduces.
		v := e.readDataPort()
		e.clearBusy()
		return v
	}
	return 0xFF
}

// readDataPort serves an IN from any of the three aliased data ports (&DD/&DE/&DF).
// It returns the single shared read-back latch (gap c). When the selected
// peripheral is in auto-null mode, the bare IN first clocks the next byte through
// that peripheral (the PIC-supplied dummy), advancing the latch.
func (e *ENC28J60) readDataPort() uint8 {
	switch e.selPeriph {
	case periphENC:
		if e.rbmActive {
			// RBM bulk read: each IN &DE clocks-and-returns the next buffer byte,
			// auto-advancing ERDPT (datasheet AUTOINC). The driver streams this with
			// INI under &2F auto-null, then turns auto-null off (&04) and reads the
			// final byte with one bare INI — both are RBM reads, so both advance.
			e.clockedIn(e.rbmNext())
		}
	case periphSD:
		if e.autoNullFor(periphSD) {
			e.clockedIn(e.sd.autoClock())
		}
	case periphEEP:
		if e.autoNullFor(periphEEP) {
			e.clockedIn(e.eep.autoClock())
		}
	}
	if !e.lastClockedValid {
		return sdIdleMISO // nothing clocked yet: SPI-idle MISO-high
	}
	// Interleave-rule check (gap: shared read latch): the data IN returns the latch.
	// If the currently-selected peripheral did NOT clock that byte (a different real
	// peripheral last wrote it, with no fresh clock of THIS peripheral since the MUX
	// switch), this read returns a STALE foreign byte — the "don't read back across a
	// peripheral switch without re-clocking" violation. The auto-null branches above
	// re-clock the selected peripheral first, so they never trip this; only a manual
	// IN immediately after a switch (no intervening data OUT to the new peripheral)
	// does. The real SAM returns the stale byte regardless; in StrictInterleave mode
	// we record it (and the harness can fault on the offending instruction).
	if (e.selPeriph == periphENC || e.selPeriph == periphSD || e.selPeriph == periphEEP) &&
		e.lastClockedBy != periphNone && e.lastClockedBy != e.selPeriph {
		e.interleaveViols++
		if e.interleaveFirstMsg == "" {
			e.interleaveFirstMsg = fmt.Sprintf("data IN while %s selected but latch last written by %s (stale cross-peripheral read; byte=&%02X)",
				e.selPeriph, e.lastClockedBy, e.lastClockedIn)
		}
		if e.StrictInterleave {
			panic(fmt.Sprintf("trinity interleave-rule violation: %s", e.interleaveFirstMsg))
		}
	}
	return e.lastClockedIn
}

// InterleaveViolations returns how many stale cross-peripheral data reads have
// occurred (the shared-read-latch rule, manual:IMG_20260617_162617) and the first
// one's description. Zero => the driver always clocks-then-reads (rule respected).
func (e *ENC28J60) InterleaveViolations() (int, string) {
	return e.interleaveViols, e.interleaveFirstMsg
}

// Out handles a Z80 OUT to a Trinity port.
func (e *ENC28J60) Out(port uint8, value uint8) {
	e.ops++
	switch port {
	case portTrinityCtl:
		// A command/select OUT marks the PIC busy for one SPI byte (gap b). If it is
		// still busy from a prior OUT (no intervening status read / byte-time), this
		// OUT is silently dropped (manual:22) — reproducing a missing busy-poll.
		if e.isBusy() {
			return
		}
		e.ctlSelect(value)
		e.raiseBusy()
	case portTrinityEEP, portTrinityENC, portTrinitySD:
		// A data OUT clocks one SPI byte to whichever peripheral is MUX-selected (gap
		// d). Dropped while busy (gap b). The selected back-end produces the MISO byte
		// the next IN returns via the shared latch (gap c).
		if e.isBusy() {
			return
		}
		if port == portTrinitySD {
			e.trackSDClose(value, false) // the dummy &DF write in the SD close (gap 10)
		}
		e.clockData(value)
		e.raiseBusy()
	case portBorder:
		// Record the border colour the boot wrappers paint on each outcome.
		e.lastBorder = value
		e.borderWritten = true
	case 0xFB:
		// HMPR (page select); flat harness records it but doesn't relocate — page fidelity is hardware-gated
		e.lastHMPR = value
		e.hmprWritten = true
	}
}

// clockData routes one data-port OUT byte to the MUX-selected peripheral's SPI
// back-end and latches the MISO it clocks out into the shared read-back latch
// (gap c/d). An OUT to a data port while nothing is selected is ignored (the PIC
// has no peripheral to relay to).
func (e *ENC28J60) clockData(value uint8) {
	switch e.selPeriph {
	case periphENC:
		e.spiClock(value)
		e.clockedIn(e.spiMISOByte())
	case periphEEP:
		e.eep.clock(value)
		e.clockedIn(e.eep.miso)
	case periphSD:
		e.sd.out(value)
		e.clockedIn(e.sd.miso)
		// The manual's ~50µs settle applies "after a heavy controller
		// operation": while the &38-armed window is open, continued SD traffic
		// keeps the PIC in it, so the deadline anchors at the END of the
		// transaction's last byte, not at the opening &38 (i327). The i242
		// hardware failure is a probe issued right after the SD *traffic*; an
		// early-boot transaction whose traffic ended ~192k instructions before
		// the next probe must not pin it stale.
		if e.sdInitSettling {
			e.settleUntilT = e.tNow + sdInitSettleTStates
		}
	}
}

// ctlSelect handles an OUT to the microcontroller select port (&DC). It drives the
// one shared PIC: the peripheral-select MUX (gap d), the one global auto-null mode
// (gap a), the IDENT/probe reply, the &02/&03 PUSH/POP read-byte slot, the ENC
// CS-pulse / reset, and the LED-twinkle accept-and-ignore band.
func (e *ENC28J60) ctlSelect(v uint8) {
	e.trackSDClose(v, true) // observe the SD deselect-tail ordering (gap 10)
	switch {
	case v == selNullOff: // &04 — auto-null OFF (the neutral bracket)
		// The manual phrases &04 as "auto-null off / all peripherals deselected", but
		// both hardware-proven drivers issue &04 while a peripheral stays addressable:
		// the ENC driver reads ONE more buffer byte after enulloff=&04 (encdrv.asm
		// rd_buf_mem tail), and the SD deselect tail issues an explicit &30 BEFORE the
		// &04 close (bdos:40). So &04 clears the auto-null MODE; the chip-select MUX is
		// changed only by an explicit select/deselect (&10/&20/&30 or a new select).
		// The SD per-transaction state is reset here only when SD was the auto-null
		// target's bracket (the init ladder's opening &04) — handled via sdReset, which
		// is a no-op for an idle card.
		e.autoNullMode = false
		e.autoNullTarget = periphNone
		if e.selPeriph != periphSD {
			e.sd.sdReset()
		}
		return
	case v == selPushByte: // &02 — PUSH the pending read-byte (ISR save; manual:37)
		e.savedReadByte = e.lastClockedIn
		e.savedReadValid = e.lastClockedValid
		return
	case v == selPopByte: // &03 — POP the saved read-byte (manual:38)
		if e.savedReadValid {
			e.lastClockedIn = e.savedReadByte
			e.lastClockedValid = true
		}
		return
	case v >= selIdentBase && v <= selIdentTop: // &08..&0F — IDENT string read
		// If the PIC is still settling from a heavy &38 SD-init, this identity-select
		// is not honoured (the PIC is busy with SD work) and the read-back latch keeps
		// its prior value — chk_trinity, which does not busy-poll, reads stale and fails.
		// This is the i242 real-silicon divergence; the correct order (drv_init before
		// any SD) never hits it. See sdInitSettling.
		//
		// The settle is BOUNDED (i289): once the settle window has elapsed the PIC has
		// settled, so clear the flag and honour the probe (FRESH). Within the window it
		// still reads stale, preserving the i242/i287 back-to-back catch.
		if e.sdInitSettling {
			if e.tNow < e.settleUntilT {
				return
			}
			e.sdInitSettling = false
		}
		// Latch the Nth char of the IDENT string into the shared read-back latch; the
		// driver's chk_trinity reads it on the next IN &DD (gap c routes it there).
		e.probeReply = trinityIdent[v-selIdentBase]
		e.clockedIn(e.probeReply)
		return
	case v == selEEPEnable: // &11 — EEPROM CS-assert (MUX-select EEPROM)
		e.selectPeripheral(periphEEP)
		e.eep.csAssert()
		return
	case v == selEEPDisable: // &10 — EEPROM CS-deassert
		if e.selPeriph == periphEEP {
			e.selectPeripheral(periphNone)
		} else {
			e.eep.csDeassert()
		}
		return
	case v == selEEPNullOn: // &1F — EEPROM auto-null on (MUX-select EEPROM, set the global mode)
		e.selectPeripheral(periphEEP)
		e.eep.csAssert()
		e.autoNullMode = true
		e.autoNullTarget = periphEEP
		return
	case v == selENCEnable: // &21 — ENC CS-assert (MUX-select ENC, fresh transaction)
		e.selectPeripheral(periphENC)
		e.spiByteIdx = 0
		e.spiMISO = 0
		e.rbmActive = false
		return
	case v == selENCDisable: // &20 — ENC CS-deassert
		if e.selPeriph == periphENC {
			e.selectPeripheral(periphNone)
		}
		return
	case v == selENCPulse: // &23 — ENC CS PULSE: de-assert then re-assert (gap 9, manual:69,71,130)
		// A real /CS pulse ends the current ENC command (resets the byte counter) and
		// immediately re-asserts CS ready for the next — the datasheet end-of-command.
		e.spiByteIdx = 0
		e.rbmActive = false
		e.spiMISO = 0
		e.selectPeripheral(periphENC)
		return
	case v == selENCReset: // &28 — ENC reset (SRC); 50µs settle is a blind DJNZ on the Z80 side
		e.softReset()
		e.sdInitSettling = false // the ENC reset quiesces the PIC's post-SD-init settle (i242; one of two clears, the other being the bounded settle window — i289)
		e.rxDisarmed = false     // &28 ereset is the one op that disturbs RX, and it re-arms it; drv_init / enc_rx_reestablish begin with this reset (encdrv.asm:29/411)
		// CS state is unaffected by an internal reset; keep ENC selected if it was.
		return
	case v == selNullOn: // &2F — ENC auto-null on (the global mode, ENC target)
		e.selectPeripheral(periphENC)
		e.autoNullMode = true
		e.autoNullTarget = periphENC
		return
	case e.sd.sdSelect(v): // &31/&30/&38/&3F — SD selects (only when a card is configured)
		switch v {
		case selSDManual:
			e.selectPeripheral(periphSD)
			e.autoNullMode = false
			e.autoNullTarget = periphNone
			// An SD device mux-select does NOT disturb the ENC's autonomous RX (RXEN is
			// an internal ENC register the &DC byte cannot touch; the RX FIFO fills
			// independently of the SPI mux — see the rxDisarmed field comment / the
			// ENC28J60 datasheet + encdrv.asm authority). So leave rxDisarmed alone.
		case selSDAutoNul:
			e.selectPeripheral(periphSD)
			e.autoNullMode = true
			e.autoNullTarget = periphSD
			// As selSDManual: an SD mux-select leaves the ENC RX engine armed.
		case selSDInit:
			e.selectPeripheral(periphSD)
			// Always-on (i244): the heavy &38 SD-init leaves the PIC settling, so a
			// chk_trinity identity probe issued WITHIN the settle window reads stale.
			// Every test now models this, making an SD-before-ENC drv_init ordering
			// bug a build-time catch (see sdInitSettling). The window is BOUNDED
			// (i289): it closes after sdInitSettleTStates, so the PIC settles in real
			// time rather than latching "unsettled" until an &28 that may never come.
			// (This is the IDENTITY-PROBE settle — a different mechanism from RX arming;
			// the &38 SD-init does NOT disarm the ENC's autonomous RX, so rxDisarmed is
			// left alone here too.)
			e.sdInitSettling = true
			e.settleUntilT = e.tNow + sdInitSettleTStates
		case selSDDeselct:
			if e.selPeriph == periphSD {
				e.selPeriph = periphNone // CS high; do not re-deselect (sdSelect already cleared)
			}
		}
		return
	case v >= 0xC0: // %11fedcba — LED Twinkle band: accept and ignore (gap 12, manual:57-64)
		// Cosmetic LED segment writes; no read-back depends on them. Record the last
		// LED command so a fidelity trace can see it, but produce no SPI effect.
		e.lastLED = v
		e.ledWritten = true
		return
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

// ProgramTrinityNetworkFull lays out the COMPLETE 27-byte "Trinity Network "
// settings record (gap 13, manual:87): MAC@0, IP@6, Gateway@10, Subnet@14, DNS@18,
// Secondary-DNS@22, Use-DHCP@26 (0=no/1=yes). The boot wrappers read only MAC+IP
// (trinload:§4c), so this is for fidelity completeness — a consumer that reads the
// full settings record (a future config tool) sees the real layout. The index
// entry is laid out exactly as ProgramTrinityNetwork's so find_index still matches.
func (e *ENC28J60) ProgramTrinityNetworkFull(mac [6]byte, ip, gateway, subnet, dns, dns2 [4]byte, useDHCP bool) {
	entry := make([]byte, eepIndexStride)
	entry[0] = 1
	entry[1] = 1
	copy(entry[2:18], trinityNetworkName)
	e.eep.write(0, entry)

	chunk := make([]byte, 27)
	copy(chunk[0:6], mac[:])
	copy(chunk[6:10], ip[:])
	copy(chunk[10:14], gateway[:])
	copy(chunk[14:18], subnet[:])
	copy(chunk[18:22], dns[:])
	copy(chunk[22:26], dns2[:])
	if useDHCP {
		chunk[26] = 1
	}
	e.eep.write(eepChunkBase, chunk)
}

// ProgramNamedChunk lays out a NAMED EEPROM chunk so the real eeprom.asm
// find_index (for `name`, part=1 total=1) returns its chunk number and a
// subsequent read_chunk reads `data` — the general form of
// ProgramTrinityNetwork, used by samboot_config_test.go to place the
// "SAMBOOT Config  " BIOS config chunk. value is the 1-based chunk number to give
// it: the index entry lands at slot value-1 (addr (value-1)*64), so find_index
// matches it as `value`, and read_chunk reads chunk base eepChunkBase +
// (value-1)*1024 (== ProgramChunk(value, data)). name should be 16 bytes (it is
// copied into the 16-byte name field; longer is truncated, shorter is space-
// padding the caller must include). data may be shorter than 1024 (the rest stays
// zero, modelling unprogrammed cells).
func (e *ENC28J60) ProgramNamedChunk(value int, name string, data []byte) {
	// Index entry at slot value-1: part 1, total 1, then the 16-byte name.
	entry := make([]byte, eepIndexStride)
	entry[0] = 1 // part
	entry[1] = 1 // total
	copy(entry[2:18], name)
	e.eep.write((value-1)*eepIndexStride, entry)
	// Chunk data at this value's flat base.
	e.ProgramChunk(value, data)
}

// LoadEEPROMImage replaces the flash EEPROM backing store with a verbatim raw
// image — the whole captured device, byte 0 = device address 0 (the EEPROM SPI
// READ protocol addresses the flat store directly; eeprom.go). This is the i190a
// entry: load the REAL captured ~/sam-archive/samboot-capture/eeprom.bin so the
// real patched-ROM boot fetch (which reads device address &002000 into &4000)
// pulls Colin's actual bytes, not a synthetic chunk. The image is copied, so the
// caller's slice is not retained. Pair with Machine.Pager().ROM = rom.bin to run
// the real boot end-to-end (samboot_real_boot_test.go).
func (e *ENC28J60) LoadEEPROMImage(image []byte) {
	e.eep.store = append([]byte(nil), image...)
}

// EEPROMImage returns a copy of the flash EEPROM backing store — the device as it
// stands now, byte 0 = device address 0. It is the read-side counterpart of
// LoadEEPROMImage: a test takes a backup snapshot before a destructive write_chunk,
// then asserts the write changed the right bytes and that restoring the snapshot
// (LoadEEPROMImage) brings the originals back — the i135c backup/restore rehearsal
// run in emulation before any real flash. The store is copied, so the caller may
// keep it across further writes.
func (e *ENC28J60) EEPROMImage() []byte {
	return append([]byte(nil), e.eep.store...)
}

// SetEEPROMWriteFault forces the flash EEPROM to silently drop every programmed
// byte (a simulated dead write path), or restores normal programming. It is the
// negative control for a write-then-read-back-and-verify test: with the fault on,
// the read-back must differ from what was written, so the test reports FAIL —
// proving the test can fail rather than always pass. See eeprom.writeFault.
func (e *ENC28J60) SetEEPROMWriteFault(on bool) {
	e.eep.writeFault = on
}

// ProgramChunk lays a 1 KB data chunk at the flat EEPROM address eeprom.asm's
// read_chunk reads for chunk number n (the value passed in `value`). get_chunk
// maps n to flat address (28 + n*4)<<8, so chunk n lives n*1024 bytes above
// value 1's base (eepChunkBase = 0x2000); chunks are contiguous in value order.
// This is the EEPROM-region counterpart to ProgramTrinityNetwork: dumper_test.go
// uses it to preload the 16 chunks of a region (value n*16+1 .. n*16+16 for
// region n) so a read of that region streams back exactly these bytes. data may
// be shorter than 1024 (the rest stays zero, modelling unprogrammed cells).
func (e *ENC28J60) ProgramChunk(n int, data []byte) {
	addr := (28 + n*4) << 8 // == eepChunkBase + (n-1)*chunkBytes
	e.eep.write(addr, data)
}

// SetLinkUpAfterOps models the PHY link coming up only after `n` SPI port
// accesses (a proxy for the post-init link-pulse-detection delay): until then,
// a read of PHSTAT2.LSTAT reports the link DOWN and a transmit issued is silently
// dropped (doTransmit). n=0 (default) keeps the link up from the start (every
// pre-i127 test relies on that). It resets the op counter.
//
// Scope note (i127): the emulator models both the LSTAT *read* (the datasheet-
// confirmed link-status mechanism a proactive transmitter must poll before its
// first send) and the *transmit drop while link-down* (the silent wire). The
// latter was held back until the fix was confirmed on real Trinity — rec 13 ran
// the full network path and failed only at the B-DOS write-out, vs the old
// dead-silent client (i127, 2026-06-19) — so the emulator now encodes a
// hardware-confirmed cause, not a guess. What stays hardware-gated is the link-up
// *timing*: the emulator never claims a specific post-init delay (CLAUDE.md §5).
func (e *ENC28J60) SetLinkUpAfterOps(n int) { e.linkUpAfterOps = n; e.ops = 0 }

// linkUp reports whether the modelled PHY link is up yet (ops past the threshold).
func (e *ENC28J60) linkUp() bool { return e.ops >= e.linkUpAfterOps }

// FirstTXOps returns the op-count at which the first frame egressed, or -1 if
// none has. It marks the moment a proactive transmitter put its first frame on
// the wire — the op-count at which an un-fixed client (no drv_wait_link) would
// transmit — letting a test set a link-down window that straddles it.
func (e *ENC28J60) FirstTXOps() int { return e.firstTXOps }

// miiReadByte returns the byte an RCR of a MAC/MII register yields, modelling the
// MII path to the PHY registers for the one register the driver reads: PHSTAT2
// (PHY addr 0x11). A PHY read goes via MIREGADR (the PHY addr) + MIRDL/MIRDH (the
// 16-bit result); we map a read of MIRDH (bank 2, 0x19) for PHSTAT2 to LSTAT
// (bit 10 -> high byte bit 2 = 0x04) reflecting linkUp(); MIRDL (0x18) low byte
// is 0. All other MAC/MII reads pass through to the stored register.
func (e *ENC28J60) miiReadByte(addr byte) byte {
	if e.bank() == 2 && (addr == 0x18 || addr == 0x19) { // MIRDL / MIRDH
		if e.regs[2][0x14] == 0x11 { // MIREGADR == PHSTAT2
			if addr == 0x19 && e.linkUp() {
				return 0x04 // MIRDH bit 2 == PHSTAT2 bit 10 (LSTAT)
			}
			return 0x00
		}
	}
	return e.regGet(addr)
}

// spiClock processes one byte clocked out to the ENC over &DE and latches the
// MISO byte the microcontroller will return on the next IN (&DE). The driver's
// idiom is OUT opcode, OUT dummy(s), IN value — so the response to a read lands
// on the dummy clock(s) after the opcode.
func (e *ENC28J60) spiClock(v uint8) {
	if !e.encSelected() {
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
			// byteIdx 1 = dummy (garbage), byteIdx 2 = real data. MIRDL/MIRDH
			// map to the PHY register named by MIREGADR (miiReadByte).
			if e.spiByteIdx == 1 {
				e.spiMISO = 0x00 // dummy
			} else {
				e.spiMISO = e.miiReadByte(addr)
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
//
// PHY link-up gate (i127, hardware-confirmed 2026-06-19): a transmit issued
// while the 10BASE-T link is still down is SILENTLY lost — the datasheet says
// TXRTS clears and TXIF sets (the driver's status poll sees a clean transmit),
// but the bytes never egress. So when the link is down we run the full
// completion signalling below (the driver believes it succeeded) yet do NOT
// append the frame to the wire. This is what makes the un-fixed proactive
// transmitter (ARP-before-link-up, no drv_wait_link) reproduce the silent wire
// Pete saw on real hardware, and the fixed client (drv_wait_link first) recover.
func (e *ENC28J60) doTransmit() {
	st := e.reg16(regETXSTL)
	nd := e.reg16(regETXNDL)
	if e.linkUp() {
		// Frame = bytes (ETXST+1 .. ETXND): the byte at ETXST is the per-packet
		// control byte (tx_flags); the frame proper follows.
		frame := make([]byte, 0, int(nd-st))
		for a := int(st) + 1; a <= int(nd); a++ {
			frame = append(frame, e.buf[a&0x1FFF])
		}
		e.txFrames = append(e.txFrames, frame)
		if e.firstTXOps < 0 {
			e.firstTXOps = e.ops
		}
	}

	// 7-byte transmit status vector at ETXND+1, all zero (no error, no late
	// collision) — the driver reads 8 bytes of status starting at tx_start+1
	// + length, checks EIR.TXERIF then tx_status+3 bit 5 (late collision).
	// Written regardless of link state: on real silicon the transmit logic still
	// runs and posts a clean status; only the egress is lost.
	for i := 0; i < 7; i++ {
		e.buf[(int(nd)+1+i)&0x1FFF] = 0
	}

	// Completion: TXIF set, TXERIF clear; TXRTS self-clears. These fire even with
	// the link down (the datasheet behaviour that hides the loss from the driver).
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
	// Deliver nothing only while RX is genuinely disarmed — i.e. between an &28 ereset
	// and its RXEN re-arm. An ordinary SD transaction does NOT disarm RX (the &DC mux
	// byte cannot touch the ENC's internal RXEN; the RX FIFO fills autonomously — see
	// the rxDisarmed field comment / the ENC28J60 datasheet + encdrv.asm authority), so
	// a correct program that serves across SD reads/writes WITHOUT a per-block re-arm
	// keeps receiving here. (i293 corrects the earlier "any SD select disarms RX" model.)
	if e.rxDisarmed {
		return
	}
	// Apply the ERXFCON receive filter (gap 8): a frame whose dest MAC fails the
	// configured filter is dropped by the receive engine before it ever reaches the
	// FIFO (manual:95 "sees but does not receive"). Skip filtered frames; deliver the
	// first that passes. ERXFCON==0 (sniffer) passes everything (manual:96).
	for len(e.rxQueue) > 0 && !e.rxFilterPass(e.rxQueue[0]) {
		e.rxQueue = e.rxQueue[1:]
	}
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

	// The receive engine writes into the RX FIFO, a circular buffer delimited by
	// ERXST..ERXND (here rxStartHW..rxEndHW), NOT the full 8 KB SRAM: the byte
	// after ERXND wraps back to ERXST (datasheet §7.2.2; the same boundary the
	// driver's read side honours in rbmNext). Wrapping at 0x1FFF (the SRAM end)
	// instead would let a frame straddling ERXND spill into the TX region and
	// desync the reader, which wraps at ERXND — so wrap here at ERXND too.
	pos := start
	if pos < rxStartHW || pos > rxEndHW {
		pos = rxStartHW
	}
	put := func(b byte) {
		e.buf[pos] = b
		if pos == rxEndHW {
			pos = rxStartHW
		} else {
			pos++
		}
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
