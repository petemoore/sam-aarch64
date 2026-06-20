package z80

// sdcard.go — host-side emulation of the SD card on the Quazar Trinity SPI bus
// (the third device, after the ENC28J60 in enc28j60.go and the flash EEPROM in
// eeprom.go). It models enough of the SD-SPI command state machine to run the
// *real*, hardware-proven SD init ladder from Colin Piggot's B-DOS 1.5t (beta 6)
// Trinity fork (&A623) end-to-end under this harness — the emulation-first
// discipline (CLAUDE.md rule 7): validate the emulation against code we KNOW
// runs on real Trinity hardware before any fresh SD code runs in it.
//
// SPEC — grounded in two authorities, the doc and the driver:
//   - docs/notes/trinity-sd-z80-interface.md (the i193 port authority): the &DC
//     SD select bytes (§2), the &DF SPI byte mechanics + one-byte read-lag +
//     auto-null bulk mode (§3), the init ladder (§4: the &38 wake then
//     CMD0/8/55+ACMD41/1/58/59/9/16), and the CMD9 CSD read via the &FE
//     data-token helper (§6).
//   - the driver's actual disassembly (bdos15t-beta6.annotated.dis &A623-&A7F8):
//     when the model and the doc disagree, the disassembly wins — the model
//     returns exactly the bytes Colin's real polls break on. Two findings where
//     the running code is more specific than the doc:
//       (a) The &38 microcontroller-wake poll at &A643-&A64B reads sd.in until it
//           returns &FF (`call sd.in; inc a; jr z`) — i.e. the card answers the
//           wake by going to the SPI-idle MISO-high state (&FF), NOT by returning
//           a non-&FF byte. (The doc §4 step 2 says "poll until != &FF", which is
//           the textbook CMD0-response poll; the &38 *wake* poll here breaks on
//           &FF. Doc-improvement finding — see the return note below.)
//       (b) The whole init ladder runs under the &31 manual select; the &3F
//           auto-null select is used ONLY by sd.cmd-with-address (&A81F), the
//           sector read/write path, which the init ladder never calls. So every
//           response read in the ladder is a manual sd.in (dummy &FF OUT then
//           IN &DF) — the one-byte lag is the only timing that matters here.
//
// VERIFICATION SCOPE: running Colin's &A623 ladder against this model and
// confirming the CSD it streams back via CMD9 decodes to the right block/record
// count IS the host verification (sd_init_colin_test.go). It is NOT hardware
// verification (CLAUDE.md §5): it models the digital SPI command/response
// behaviour the driver drives, not analogue card timing or the data read/write
// (CMD17/24) path. The card is INERT until a CSD is configured, so every test
// that does not attach one sees the pre-existing &DF=0xFF idle stub unchanged.

// SD card select bytes on port &DC (trinity-sd-z80-interface.md §2). These are
// SD-specific and must not disturb the ENC path that shares &DC.
const (
	selSDManual  = 0x31 // SD CS asserted, manual mode (Z80 supplies its own dummy &FF per byte)
	selSDDeselct = 0x30 // SD deselect
	selSDInit    = 0x38 // microcontroller "SD init" wake command
	selSDAutoNul = 0x3F // SD CS asserted, auto-null mode (microcontroller injects the dummy)
	// selNullOff (0x04) is the all-deselect/auto-null-off bracket; it is already an
	// ENC constant (enc28j60.go) and is handled there — it also resets the SD card.
)

const (
	sdIdleMISO = 0xFF // SPI-idle MISO (card not driving): a flushed/idle read returns &FF
	sdDataTok  = 0xFE // start-of-data token preceding a data block (CSD read, sector read)
	sdCmdFrame = 6    // an SD-SPI command frame is 6 bytes: opcode|0x40, 4 arg bytes, CRC
)

// SDCard models the SD-SPI command state machine on ports &DC (select) / &DF
// (data). It is owned by ENC28J60 (the two share the Trinity bus); ENC28J60
// dispatches the &DC SD selects and the &DF data byte to its methods. A
// zero-value SDCard is INERT: configured=false makes every &DF read fall back to
// the idle &FF stub, so existing tests that never attach a CSD are unaffected.
type SDCard struct {
	configured bool     // true once SetCSD installs a card; gates all SD activity
	csd        [16]byte // the 16 CSD register bytes CMD9 streams back
	// isV2 reports whether this card is a v2.0+ (SDHC/SDXC, high-capacity) card,
	// derived from CSD_STRUCTURE (CSD byte 0 top two bits == 01). It drives the
	// CMD8 / ACMD41-HCS / CMD58-OCR responses so the driver follows its real
	// SDv2-vs-SDv1 branch: a v1 card must REJECT CMD8 (R1 illegal-command) and
	// report a CCS-clear OCR (byte addressing), exactly as silicon does, or the
	// driver mis-decodes the capacity.
	isV2 bool

	selected bool // SD CS asserted (between &DC &31/&3F and &30/&04)
	autoNull bool // &3F auto-null mode: the microcontroller supplies the per-byte dummy
	woken    bool // the &38 microcontroller-wake step has run for this transaction

	// Command accumulation. cmdBuf collects the 6-byte command frame; cmdLen counts
	// bytes collected. inCmd is true while collecting (a command-start byte —
	// (b&0xC0)==0x40 — opens it; a leading flush &FF does not).
	inCmd  bool
	cmdBuf [sdCmdFrame]byte
	cmdLen int

	// Response stream. After a command frame completes, resp holds the bytes the
	// subsequent reads return (R1, then any R3/R7 trailer or the &FE-token+CSD
	// stream), and respPos indexes the next. Reads past the end return &FF (idle).
	resp    []byte
	respPos int

	// miso is the byte the next IN &DF returns: the one clocked by the most recent
	// OUT &DF (the microcontroller's one-byte read-lag, mirroring eeprom.miso /
	// enc28j60.spiMISO). In auto-null mode a bare IN advances the stream itself.
	miso byte
}

// SetCSD installs the 16 CSD register bytes the card reports via CMD9 and arms
// the model (configured=true). Until this is called the card is inert.
func (s *SDCard) SetCSD(csd [16]byte) {
	s.csd = csd
	// CSD_STRUCTURE == 01 (byte0 bits 7:6) => v2.0 high-capacity card.
	s.isV2 = (csd[0] & 0xC0) == 0x40
	s.configured = true
}

// CSDForV2 builds a 16-byte CSD v2.0 register (SDHC/SDXC) for the given 22-bit
// C_SIZE: blocks = (C_SIZE+1)*1024 512-byte blocks. CSD_STRUCTURE = 01 (byte0 bit
// 6). C_SIZE occupies bits [69:48]: byte7 low 6 bits, byte8, byte9 (SD physical-
// layer spec v3.01 §5.3.3). Mirrors the csdV2 builder in csd_decode_colin_test.go
// so the model and the POC-1 oracle construct identical registers.
func CSDForV2(cSize uint32) [16]byte {
	var c [16]byte
	c[0] = 0x40 // CSD_STRUCTURE = 01 (v2.0)
	c[7] = byte((cSize >> 16) & 0x3F)
	c[8] = byte((cSize >> 8) & 0xFF)
	c[9] = byte(cSize & 0xFF)
	return c
}

// CSDForV1 builds a 16-byte CSD v1.0 register (SDSC) from READ_BL_LEN, C_SIZE and
// C_SIZE_MULT: capacity = (C_SIZE+1) * 2^(C_SIZE_MULT+2) * 2^READ_BL_LEN bytes
// (SD spec v3.01 §5.3.2). CSD_STRUCTURE = 00. Mirrors csdV1 in
// csd_decode_colin_test.go.
func CSDForV1(readBlLen, cSize, cSizeMult uint32) [16]byte {
	var c [16]byte
	c[0] = 0x00 // CSD_STRUCTURE = 00 (v1.0)
	c[5] = byte(readBlLen & 0x0F)
	c[6] = byte((cSize >> 10) & 0x03)
	c[7] = byte((cSize >> 2) & 0xFF)
	c[8] = byte((cSize & 0x03) << 6)
	c[9] |= byte((cSizeMult >> 1) & 0x03)
	c[10] = byte((cSizeMult & 0x01) << 7)
	return c
}

// sdSelect handles an OUT to the &DC select port that targets the SD card. It
// returns true if v was an SD-specific select (so ENC28J60.ctlSelect leaves the
// ENC state alone). selNullOff (&04) is shared and handled by ctlSelect; we also
// treat it as an SD deselect+reset there.
func (s *SDCard) sdSelect(v uint8) bool {
	if !s.configured {
		return false
	}
	switch v {
	case selSDManual:
		// Assert CS, manual mode. Begins/continues a transaction; reset the
		// command accumulator so a fresh command frame is collected. (The wake
		// poll re-selects &31 between &38 and the first command; each select
		// cleanly restarts command framing.)
		s.selected = true
		s.autoNull = false
		s.resetCmd()
		return true
	case selSDAutoNul:
		s.selected = true
		s.autoNull = true
		s.resetCmd()
		return true
	case selSDInit:
		// Microcontroller "SD init" wake: the &A643 poll then reads sd.in until it
		// returns &FF (the card settling to SPI-idle MISO-high). Arm that by making
		// the post-wake reads return &FF (the default idle). Mark woken so the very
		// next manual read is idle &FF, breaking Colin's `inc a; jr z` poll.
		s.woken = true
		s.resetCmd()
		s.resp = nil
		s.respPos = 0
		s.miso = sdIdleMISO
		return true
	case selSDDeselct:
		s.selected = false
		return true
	}
	return false
}

// sdReset clears all per-transaction state (selNullOff / &04 all-deselect bracket,
// handled by ctlSelect). The configured CSD survives a bus reset.
func (s *SDCard) sdReset() {
	s.selected = false
	s.autoNull = false
	s.woken = false
	s.resetCmd()
	s.resp = nil
	s.respPos = 0
	s.miso = sdIdleMISO
}

func (s *SDCard) resetCmd() {
	s.inCmd = false
	s.cmdLen = 0
}

// out processes one byte clocked to the SD card over &DF (a manual-mode sd.out).
// It either accumulates a command-frame byte or, in the response/idle phase,
// advances the response stream and latches the byte the next IN &DF returns (the
// one-byte read-lag: a dummy &FF write clocks out the next response byte).
func (s *SDCard) out(v uint8) {
	if !s.configured || !s.selected {
		return
	}
	// While collecting a command frame, every byte is part of it.
	if s.inCmd {
		s.cmdBuf[s.cmdLen] = v
		s.cmdLen++
		if s.cmdLen == sdCmdFrame {
			s.completeCommand()
		}
		// During command collection the card's MISO stays idle-high (it has not yet
		// computed a response); the host ignores these reads anyway.
		s.miso = sdIdleMISO
		return
	}
	// Not mid-command: a byte whose top two bits are 01 (opcode|0x40) opens a new
	// command frame; anything else (a flush/dummy &FF) clocks the response stream.
	if v&0xC0 == 0x40 {
		s.inCmd = true
		s.cmdBuf[0] = v
		s.cmdLen = 1
		s.miso = sdIdleMISO
		return
	}
	// Dummy/flush clock in the response (or idle) phase: latch the next response
	// byte for the following IN (the read-lag).
	s.miso = s.nextResp()
}

// in returns the byte for an IN &DF. In manual mode it returns the latched
// read-lag byte (set by the most recent out). In auto-null mode the
// microcontroller supplies the dummy, so a bare IN both advances and returns the
// next response byte. When inert/deselected it returns the idle &FF stub —
// preserving the pre-existing portTrinitySD behaviour for tests with no card.
func (s *SDCard) in() uint8 {
	if !s.configured || !s.selected {
		return sdIdleMISO
	}
	if s.autoNull {
		return s.nextResp()
	}
	return s.miso
}

// nextResp pops the next byte from the response stream, or the idle &FF when the
// stream is exhausted (the card releases MISO high between responses).
func (s *SDCard) nextResp() uint8 {
	if s.respPos < len(s.resp) {
		b := s.resp[s.respPos]
		s.respPos++
		return b
	}
	return sdIdleMISO
}

// completeCommand decodes the just-collected 6-byte command frame and arms the
// response stream the following reads will return. Opcodes are the &40|cmd values
// the ladder sends (trinity-sd-z80-interface.md §4 / dis &A623-&A736).
func (s *SDCard) completeCommand() {
	op := s.cmdBuf[0]
	s.resp = nil
	s.respPos = 0
	switch op {
	case 0x40: // CMD0 GO_IDLE_STATE -> R1 = 0x01 (idle)
		s.resp = []byte{0x01}
	case 0x48: // CMD8 SEND_IF_COND
		if s.isV2 {
			// SDv2: R1=0x01(idle) + 4-byte R7 trailer echoing VHS(0x01)+pattern(0xAA).
			// The driver verifies the echo (dis &A686: sd.in→D, sd.in→E, sbc hl,de vs
			// 0x01AA) then sets its SDv2 flag (A' |= 0x40 at &A693).
			s.resp = []byte{0x01, 0x00, 0x00, 0x01, 0xAA}
		} else {
			// SDv1/MMC: CMD8 is illegal — R1 has the illegal-command bit (bit2) set
			// (0x05 = idle | illegal-command). The driver's `dec a; jr nz` at &A683
			// sees R1!=1 and branches to ACMD41 WITHOUT setting the SDv2 flag, so the
			// later CMD58 is skipped and the v1 CSD decode branch is taken.
			s.resp = []byte{0x05}
		}
	case 0x77: // CMD55 APP_CMD -> R1 (still idle, bit0 set) so the ACMD path continues
		s.resp = []byte{0x01}
	case 0x69: // ACMD41 SD_SEND_OP_COND -> R1 = 0x00 (init complete, bit0 clear = ready)
		s.resp = []byte{0x00}
	case 0x41: // CMD1 SEND_OP_COND (MMC fallback) -> R1 = 0x00 (ready), if ever reached
		s.resp = []byte{0x00}
	case 0x7A: // CMD58 READ_OCR -> R1=0x00 + 4 OCR bytes; OCR bit30 = CCS (SDHC=block addr)
		// dis &A6F2: R1 read, then sd.in→D (OCR[31:24]), sd.in→E, then `add a,a`
		// twice — the first guards power-up-done (OCR bit31, must be set), the second
		// tests CCS (OCR bit30). Only SDv2 cards reach CMD58 (v1 skips it at &A6E3),
		// so isV2 is always true here; CCS set => block addressing for SDHC.
		top := byte(0x80) // power-up done (bit31); CCS clear
		if s.isV2 {
			top = 0xC0 // power-up done (bit31) + CCS (bit30): block addressing
		}
		s.resp = []byte{0x00, top, 0xFF, 0x80, 0x00}
	case 0x7B: // CMD59 CRC_ON_OFF -> R1 = 0x00
		s.resp = []byte{0x00}
	case 0x49: // CMD9 SEND_CSD -> R1=0x00, then the &FE data token, 16 CSD bytes, 2 CRC bytes
		// dis &A711: sd.cmd sends CMD9 and polls R1 (must be 0x00, non-carry,
		// non-zero check is `jp nz`/`jp c` to fail — so R1 top bit clear AND R1==0).
		// Then &A721 calls the token-read helper &A7D3: poll sd.in for &FE (≤256),
		// then read 18 bytes into &780F. So after R1 the stream is &FE + 16 CSD + 2 CRC.
		resp := make([]byte, 0, 1+1+18)
		resp = append(resp, 0x00)      // R1 ready
		resp = append(resp, sdDataTok) // &FE start-of-data token
		resp = append(resp, s.csd[:]...)
		resp = append(resp, 0x00, 0x00) // 2 dummy CRC bytes (CRC off / ignored in SPI mode)
		s.resp = resp
	case 0x50: // CMD16 SET_BLOCKLEN 512 -> R1 = 0x00
		s.resp = []byte{0x00}
	default:
		// Any other command: return a benign ready R1 so an unmodelled probe does
		// not wedge a poll. (The init ladder sends only the opcodes above.)
		s.resp = []byte{0x00}
	}
	s.inCmd = false
	s.cmdLen = 0
}
