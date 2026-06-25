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
	sdSectorSz = 512  // a data block is one 512-byte sector
	// sdWriteAccept is the data-response token a CMD24 write returns once the 512
	// data bytes are clocked in: the driver reads it as `in a,(&df); and &1e; sub 4`
	// (hd.svb-t tail &A89B-&A8A1), so any value whose `& &1E == &04` is "accepted"
	// (&04 = data accepted, &05 = its idle-bit-set sibling — both map to &04). We
	// return &05, matching the doc's "accepted == &04/&05".
	sdWriteAccept = 0x05
	// sdWriteBusy / sdWriteDone are the busy-poll bytes after the data-response: the
	// tail busy-waits `in a,(&df); inc a; jr z` (&A8AB-&A8AE) — it loops while the
	// card drives a non-&FF (busy) MISO and breaks when MISO releases to &FF (done).
	sdWriteBusy = 0x00
	sdWriteDone = 0xFF
	// sdWriteBusyReads is how many busy (&00) bytes the model returns before
	// releasing to &FF — a couple, to exercise the driver's busy loop without
	// approaching its 65536 cap.
	sdWriteBusyReads = 2
)

// CMD17/CMD24 opcodes (SD-SPI: command number | 0x40), block-addressed (SDHC) or
// byte-addressed (SDv1) per the seek path's <<9 (trinity-sd-z80-interface.md §7).
const (
	cmdReadSingle  = 0x51 // CMD17 READ_SINGLE_BLOCK
	cmdWriteSingle = 0x58 // CMD24 WRITE_SINGLE_BLOCK
)

// sdPhase tracks the post-command data phase of a CMD17 read or CMD24 write. The
// init-ladder commands (CMD0..CMD16) leave the model in phaseIdle and use only the
// resp-stream/read-lag machinery; only the sector commands enter a data phase.
type sdPhase int

const (
	phaseIdle       sdPhase = iota // no data phase: R1/response stream only (init ladder, CMD9)
	phaseWriteToken                // CMD24: awaiting the &FE start-of-data token from the driver
	phaseWriteData                 // CMD24: capturing the 512 data bytes the driver clocks out
	phaseWriteCRC                  // CMD24: swallowing the 2 trailing CRC bytes after the data
	phaseWriteResp                 // CMD24: returning the data-response token, then the busy poll
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

	// writeProtect, when set, makes &DC bit 2 read CLEAR so the driver's
	// sense-inverted WP gate (`IN &DC; CPL; AND 4`, hd.svb-t &A91B) flags the card
	// write-protected and aborts the write (gap 7, manual:17,102; bdos:42,43,46). A
	// writable card (the default) reads bit 2 SET. SetWriteProtect toggles it.
	writeProtect bool

	selected bool // SD CS asserted (between &DC &31/&3F and &30/&04)
	woken    bool // the &38 microcontroller-wake step has run for this transaction
	// initType is the &38 SD-init return code the documented BASIC/alt-driver path
	// reads (gap 6, manual:74-77): 0 = absent/fail, 1 = MMC, 2 = SD. Colin's ladder
	// ignores it (it polls for the &FF settle instead), so it is exposed via the
	// shared read-back latch only after a &38 wake.
	initType byte

	// Command accumulation. cmdBuf collects the 6-byte command frame; cmdLen counts
	// bytes collected. inCmd is true while collecting (a command-start byte —
	// (b&0xC0)==0x40 — opens it; a leading flush &FF does not).
	inCmd  bool
	cmdBuf [sdCmdFrame]byte
	cmdLen int

	// Leading-flush enforcement (i245). The documented command frame is
	// [&FF flush][opcode][4 arg][CRC] — a flush/idle clock precedes EVERY command
	// opcode (trinity-sd-z80-interface.md §3/§5; Colin sd.cmd &A7FA; MMCSD cmd9).
	// flushBeforeCmd records that a true idle flush (NOT a response-stream poll
	// byte) was clocked since the last response was consumed / since &31 select.
	// A command whose opcode arrives with flushBeforeCmd == false is malformed: it
	// de-syncs the card (deSynced, sticky until the next transaction), so every
	// following read is idle &FF and no &FE data token ever arrives — reproducing
	// the i145g all-zeros bug a flush-less driver hits on real silicon.
	flushBeforeCmd bool
	deSynced       bool

	// Response stream. After a command frame completes, resp holds the bytes the
	// subsequent reads return (R1, then any R3/R7 trailer or the &FE-token+CSD
	// stream), and respPos indexes the next. Reads past the end return &FF (idle).
	resp    []byte
	respPos int

	// miso is the byte the next IN &DF returns: the one clocked by the most recent
	// OUT &DF (the microcontroller's one-byte read-lag, mirroring eeprom.miso /
	// enc28j60.spiMISO). In auto-null mode a bare IN advances the stream itself.
	miso byte

	// --- CMD17/CMD24 sector data path (i145h) ---

	// store is the sector backing store: a 512-byte sector keyed by its block index
	// (CMD17/CMD24 address, after the seek's byte-vs-block <<9 has been applied — so
	// for an SDHC card the key is the sector number, for an SDv1 card it is the byte
	// address, exactly as the driver pokes it into the &A836/&A843 immediates). A
	// CMD24 write captures into store[addr]; a CMD17 read streams store[addr] back.
	store map[uint32][]byte

	// addr is the 32-bit address parsed from the most recent CMD17/CMD24 frame
	// (big-endian: arg bytes [1..4]); it keys store for the data phase that follows.
	addr uint32

	// phase is the current sector data-phase state (phaseIdle outside a CMD17/24
	// data phase). writeBuf accumulates the CMD24 payload; writeIdx counts captured
	// data bytes; crcLeft counts the trailing CRC bytes still to swallow; busyLeft
	// counts the busy (&00) reads still to emit before the write-done (&FF) release.
	phase    sdPhase
	writeBuf []byte
	writeIdx int
	crcLeft  int
	busyLeft int
	respStep int // walks the CMD24 post-data 3-read handshake (gap, data-response, busy)
}

// SetCSD installs the 16 CSD register bytes the card reports via CMD9 and arms
// the model (configured=true). Until this is called the card is inert.
func (s *SDCard) SetCSD(csd [16]byte) {
	s.csd = csd
	// CSD_STRUCTURE == 01 (byte0 bits 7:6) => v2.0 high-capacity card.
	s.isV2 = (csd[0] & 0xC0) == 0x40
	s.configured = true
	// &38 SD-init return code (gap 6, manual:74-77): 2 = SD, 1 = MMC. A configured
	// card is an SD (v1 or v2); MMC cards (which fail CMD8 AND use CMD1) are not the
	// shape SetCSD installs, so a configured card reports 2.
	s.initType = 2
}

// SetWriteProtect marks the card write-protected (or writable). When protected,
// &DC bit 2 reads CLEAR so the driver's sense-inverted WP gate (`IN &DC; CPL; AND
// 4`) aborts a CMD24 write (gap 7, manual:17,102; bdos:42,43,46). Default writable.
func (s *SDCard) SetWriteProtect(on bool) { s.writeProtect = on }

// SeedSector pre-loads a 512-byte sector into the backing store at block address
// addr, so a subsequent CMD17 read of that address streams it back. A short slice
// is zero-padded; a long one is truncated. Used by the read-path test to plant a
// known pattern the driver then reads. (The card need not be configured to seed —
// SetCSD arms the SPI machinery; seeding is independent backing-store setup.)
func (s *SDCard) SeedSector(addr uint32, data []byte) {
	if s.store == nil {
		s.store = make(map[uint32][]byte)
	}
	sec := make([]byte, sdSectorSz)
	copy(sec, data)
	s.store[addr] = sec
}

// CapturedSector returns the 512 bytes the backing store holds at block address
// addr (a CMD24 write target, or a SeedSector), and whether a sector exists there.
func (s *SDCard) CapturedSector(addr uint32) ([]byte, bool) {
	if s.store == nil {
		return nil, false
	}
	sec, ok := s.store[addr]
	return sec, ok
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
// returns true if v was an SD-specific select. The MUX + global auto-null mode are
// owned by the shared controller (ENC28J60.ctlSelect); this only manages the SD's
// per-transaction state (CS, command framing, the &38 wake). selNullOff (&04) is
// the shared all-deselect bracket, handled by ctlSelect, which calls sdReset.
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
		s.resetCmd()
		return true
	case selSDAutoNul:
		s.selected = true
		s.resetCmd()
		return true
	case selSDInit:
		// Microcontroller "SD init" wake (gap 6). Colin's &A643 poll reads sd.in until
		// it returns &FF (the card settling to SPI-idle MISO-high), breaking his
		// `inc a; jr z`. The documented 0/1/2 return code (manual:74-77) is delivered
		// FIRST (one byte), then the &FF settle follows — so a 0/1/2 consumer reads
		// the code while Colin's poll skips it (initType != &FF) and breaks on the &FF.
		s.selected = true
		s.woken = true
		s.resetCmd()
		s.resp = []byte{s.initType}
		s.respPos = 0
		s.miso = sdIdleMISO
		s.phase = phaseIdle
		return true
	case selSDDeselct:
		s.selected = false
		return true
	}
	return false
}

// deselect drops SD CS (the controller MUX-selecting another peripheral). The
// configured CSD and backing store survive; only the in-flight transaction ends.
func (s *SDCard) deselect() { s.selected = false }

// ctlStatus is the &DC Trinity Status Register byte the SD contributes (the shared
// controller adds BUSY and ENCINT). Per the manual ("Trinity Status Register"), IN
// &DC returns %1100BWFE: bits 7,6 FIXED 1, bits 5,4 FIXED 0, then bit3 BUSY / bit2
// WRITE / bit1 FLASH (card inserted) / bit0 ENCINT. The base is 0xC0. When a card
// is configured it adds bit 1 (present) and bit 2 (write status, SENSE-INVERTED:
// the driver reads it as `CPL / AND 4`, so a WRITABLE card reads bit 2 SET and a
// WRITE-PROTECTED card reads it CLEAR — hd.svb-t WP gate &A91B; gap 7).
func (s *SDCard) ctlStatus() uint8 {
	if !s.configured {
		return 0xC0 // no card: fixed top nibble only (FLASH=0)
	}
	st := uint8(0xC0 | 0x02) // present
	if !s.writeProtect {
		st |= 0x04 // writable (sense-inverted: bit 2 SET == writable)
	}
	return st
}

// sdReset clears all per-transaction state (selNullOff / &04 all-deselect bracket,
// handled by ctlSelect). The configured CSD survives a bus reset.
func (s *SDCard) sdReset() {
	s.selected = false
	s.woken = false
	s.resetCmd()
	s.resp = nil
	s.respPos = 0
	s.miso = sdIdleMISO
	s.phase = phaseIdle
	// Fresh transaction bracket (&04): the first command must be preceded by an
	// idle/flush clock again, and any prior de-sync is cleared.
	s.flushBeforeCmd = false
	s.deSynced = false
}

// autoClock serves a bare IN &DF under &3F auto-null mode: the microcontroller
// supplies the per-byte dummy clock, so the read both advances and returns the next
// byte. It mirrors the manual-mode out(dummy)+in pair but in one step.
func (s *SDCard) autoClock() uint8 {
	if !s.configured || !s.selected {
		return sdIdleMISO
	}
	if s.phase == phaseWriteResp {
		return s.inWriteResp()
	}
	return s.nextResp()
}

func (s *SDCard) resetCmd() {
	s.inCmd = false
	s.cmdLen = 0
}

// frameAddr extracts the 32-bit address argument from the current command frame
// (bytes 1..4, big-endian — the order &A81F clocks out: HIGH word H,L then LOW
// word H,L). For an SDHC card this is the block (sector) number; for an SDv1 card
// the seek path has already <<9-shifted it to a byte address (§7). Either way it
// is the verbatim key into the backing store, matching how the driver addresses.
func (s *SDCard) frameAddr() uint32 {
	return uint32(s.cmdBuf[1])<<24 | uint32(s.cmdBuf[2])<<16 |
		uint32(s.cmdBuf[3])<<8 | uint32(s.cmdBuf[4])
}

// out processes one byte clocked to the SD card over &DF (a manual-mode sd.out).
// It either accumulates a command-frame byte or, in the response/idle phase,
// advances the response stream and latches the byte the next IN &DF returns (the
// one-byte read-lag: a dummy &FF write clocks out the next response byte).
func (s *SDCard) out(v uint8) {
	if !s.configured || !s.selected {
		return
	}
	// A CMD24 write data phase claims every OUT byte (token, 512 data, 2 CRC) — a
	// data byte may have its top two bits == 01, so it must be handled BEFORE the
	// command-start detection below, or a payload byte would be mistaken for a new
	// command frame. (CMD17 reads have no OUT data phase — they are bare INs.)
	//
	// Special case: if the response stream has unread bytes (e.g. CMD24's R1
	// response), a dummy-OUT in a non-idle phase is still serving the response
	// rather than the write data phase — let nextResp advance it first. This models
	// the manual-mode driver (bd_list_write_hw): sdc_cmd polls R1 via a dummy OUT +
	// IN immediately after CMD24's completeCommand sets phaseWriteToken, so those
	// dummy OUTs must serve R1 (not go to outDataPhase) or the poll loops forever.
	// Only after R1 is exhausted do subsequent OUTs enter the data phase.
	if s.phase != phaseIdle {
		if s.respPos < len(s.resp) {
			// Response bytes still pending (the CMD24 R1 poll): advance the stream.
			s.miso = s.nextResp()
			return
		}
		if s.phase == phaseWriteResp {
			// The post-data handshake is read manually (dummy OUT &FF + IN): latch the
			// next handshake byte (gap/data-response/busy) for the following IN &DF.
			s.miso = s.inWriteResp()
			return
		}
		s.outDataPhase(v)
		// Capturing the write data block: the host ignores MISO here (it is mid-OUTI),
		// but keep the latch at idle-high so a stray read is benign.
		s.miso = sdIdleMISO
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
	if s.respPos >= len(s.resp) {
		// A TRUE idle/flush clock — no response is pending, so this is the leading
		// Ncc flush a command needs (i245), not an R1/token poll byte (those happen
		// while respPos < len(resp) and must NOT satisfy the flush requirement).
		s.flushBeforeCmd = true
	}
	s.miso = s.nextResp()
}

// outDataPhase consumes one OUT byte during a CMD24 write data phase. The driver
// (hd.svb-t &A933-&A953 + tail &A86B-) sends: a leading dummy &FF, the &FE
// start-of-data token, the 512 payload bytes (OUTI loop), then 2 dummy CRC bytes.
// We swallow the dummy, latch on the token, capture the 512 payload into the
// backing store at the addressed sector, swallow the CRC, then arm the
// data-response + busy poll that the following INs read.
func (s *SDCard) outDataPhase(v uint8) {
	switch s.phase {
	case phaseWriteToken:
		// Awaiting the &FE token. The leading dummy &FF (&A933) is ignored; the &FE
		// (&A93D) opens the data block. (Any non-&FE byte before the token is a
		// flush/dummy and is skipped — faithful to the driver only ever sending &FF.)
		if v == sdDataTok {
			s.writeBuf = make([]byte, sdSectorSz)
			s.writeIdx = 0
			s.phase = phaseWriteData
		}
	case phaseWriteData:
		s.writeBuf[s.writeIdx] = v
		s.writeIdx++
		if s.writeIdx == sdSectorSz {
			// All 512 captured: commit to the backing store at the addressed block.
			if s.store == nil {
				s.store = make(map[uint32][]byte)
			}
			sec := make([]byte, sdSectorSz)
			copy(sec, s.writeBuf)
			s.store[s.addr] = sec
			s.crcLeft = 2 // 2 trailing dummy CRC bytes (&A881/&A88B dec a; out)
			s.phase = phaseWriteCRC
		}
	case phaseWriteCRC:
		s.crcLeft--
		if s.crcLeft == 0 {
			// CRC done: the next INs walk the gap/data-response/busy handshake.
			s.busyLeft = sdWriteBusyReads
			s.respStep = 0
			s.phase = phaseWriteResp
		}
	}
}

// inWriteResp returns the CMD24 post-data handshake the driver polls. The tail
// (&A893-&A8AE) does it in three reads: a throwaway IN (&A893, the 1-byte gap
// after the CRC — its result is discarded), THEN the data-response token (&A89B,
// read as `and &1e; sub 4`, accepted == &04), THEN the busy poll (&A8AB onward,
// `inc a; jr z` — loops while the card drives non-&FF, breaks on the &FF release).
// So: read 1 = idle gap byte; read 2 = the &05 data-response; reads 3.. = busy
// (&00) then write-done (&FF). Returning the response on read 1 instead would make
// the driver's SECOND read (the actual check) see busy and fail to "accepted".
func (s *SDCard) inWriteResp() uint8 {
	// respStep walks the three-read handshake: 0 = the throwaway gap byte (&A893);
	// 1 = the data-response token (&A89B, the one the driver checks); 2.. = the busy
	// poll (&A8AB), busy (&00) for busyLeft reads then write-done (&FF).
	switch {
	case s.respStep == 0:
		s.respStep++
		return sdIdleMISO // gap byte before the data-response (discarded by the driver)
	case s.respStep == 1:
		s.respStep++
		return sdWriteAccept // the data-response token the driver checks (and &1e == 4)
	case s.busyLeft > 0:
		s.busyLeft--
		return sdWriteBusy
	default:
		// Done: release MISO high (breaks the `inc a; jr z` busy loop). Leave the
		// phase set so any extra polls also read &FF; the deselect tail &A8D7 then
		// resets us via &30/&04.
		return sdWriteDone
	}
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

	// Leading-flush enforcement (i245): this command's opcode must have been
	// preceded by a true idle/flush clock. If not, the real card never frames it —
	// model that as a sticky de-sync: arm no response, so every following read is
	// idle &FF, the R1 poll times out, and CMD9's &FE token never arrives (the
	// i145g all-zeros symptom). flushBeforeCmd is consumed here for the next command.
	missedFlush := !s.flushBeforeCmd
	s.flushBeforeCmd = false
	if missedFlush {
		s.deSynced = true
	}
	if s.deSynced {
		// no valid response while de-synced (until the next &04/&31 bracket), but
		// still clear the command-frame state so the next byte doesn't overflow cmdBuf.
		s.inCmd = false
		s.cmdLen = 0
		return
	}

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
	case cmdReadSingle: // CMD17 READ_SINGLE_BLOCK -> R1=0x00, then &FE token + 512 + 2 CRC
		// dis hd.ldb-t &A999: send CMD17 (&51) via &A81F (which consumes R1 in the
		// sender's poll, so R1=0x00 here lets `jp nz` pass), then poll &DF for the
		// &FE token, then INI 510 + 2-byte tail under auto-null. So the stream after
		// the sender's R1 is &FE + the 512 sector bytes + 2 CRC, streamed by nextResp
		// exactly as CMD9's CSD read is. An un-seeded sector reads back as zeros.
		s.addr = s.frameAddr()
		sec, ok := s.store[s.addr]
		if !ok {
			sec = make([]byte, sdSectorSz)
		}
		resp := make([]byte, 0, 1+1+sdSectorSz+2)
		resp = append(resp, 0x00)      // R1 ready (consumed by the &A81F sender poll)
		resp = append(resp, sdDataTok) // &FE start-of-data token
		resp = append(resp, sec...)    // 512 sector bytes
		resp = append(resp, 0x00, 0x00)
		s.resp = resp
	case cmdWriteSingle: // CMD24 WRITE_SINGLE_BLOCK -> R1=0x00, then the OUT data phase
		// dis hd.svb-t &A918: send CMD24 (&58) via &A81F (R1 consumed in its poll),
		// then the driver clocks out a dummy, the &FE token, 512 data bytes (OUTI),
		// 2 CRC, and reads the data-response + busy. We arm R1=0x00 for the sender's
		// poll and enter the write data phase so the following OUTs are captured.
		s.addr = s.frameAddr()
		s.resp = []byte{0x00} // R1 ready (consumed by the &A81F sender poll)
		s.phase = phaseWriteToken
		s.respStep = 0
		s.busyLeft = 0
		s.crcLeft = 0
	default:
		// Any other command: return a benign ready R1 so an unmodelled probe does
		// not wedge a poll. (The init ladder sends only the opcodes above.)
		s.resp = []byte{0x00}
	}
	s.inCmd = false
	s.cmdLen = 0
}
