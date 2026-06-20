// sd_record_io_colin_test.go — i145h validation: run Colin Piggot's REAL,
// hardware-proven B-DOS 1.5t (beta 6) sector I/O — his CMD24 write core
// (hd.svb-t &A918 + write tail &A86B) and his CMD17 read core (hd.ldb-t &A999) —
// against the modeled SD-SPI card's new CMD17/CMD24 sector path (sdcard.go), and
// prove a sector ROUND-TRIPS: 512 bytes written through Colin's real CMD24 code
// into the model's backing store, then read back through his real CMD17 code,
// must equal the bytes written. This is the emulation-first gate (CLAUDE.md
// rule 7): validate the write model against code we KNOW runs on real Trinity
// hardware before any fresh write code touches the unrecoverable real SAM.
//
// LEVEL REACHED — the "raw sender-driven" tier of the task's fallback, but at the
// hd.svb-t/hd.ldb-t BLOCK routines (one above the bare &A81F sender): we poke the
// 32-bit sector address into the seek immediates &A836/&A843 (exactly as the seek
// path &A16B does, trinity-sd-z80-interface.md §7) rather than driving the full
// record-selection (&A0A2) + seek (&A16B) math + IX/page setup, which is entangled
// with the directory/BAM/page-switch layer. Everything from the CMD24/CMD17
// command frame down — the &A81F sender, the &FE token, the 510-byte OUTI/INI data
// loops, the 2-byte tail, the dummy CRC, the data-response token, and the busy
// wait — is Colin's REAL code, so the SD-SPI sector model is validated against the
// hardware-proven driver's exact byte sequence. (The record->sector MATH is
// already validated separately by runColinRecords in sd_init_colin_test.go; here
// we prove the SPI DATA path the math feeds.)
//
// The B-DOS 1.5t binary is Colin's PROPRIETARY code, referenced by path, never
// copied into the repo; the test skips when it is absent ($BDOS15T_BIN).
package z80_test

import (
	"bytes"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// Section-B-window addresses for Colin's real sector-I/O routines (real - &4000).
const (
	hdSvbB  = 0xA918 - secBBias // &6918  hd.svb-t   : CMD24 write core (WP, token, 510xOUTI)
	hdTailB = 0xA86B - secBBias // &686B  write tail : 2xOUTI, 2 CRC, data-response, busy
	hdLdbB  = 0xA999 - secBBias // &6999  hd.ldb-t   : CMD17 read core (token wait, 510xINI)

	// The seek immediates the sector address is poked into (trinity-sd-z80-interface.md
	// §5/§7): &A835 `ld hl,nn` operand = &A836 (low) / &A837 (high) for the HIGH
	// address word; &A842 `ld hl,nn` operand = &A843 / &A844 for the LOW word. The
	// sender clocks H then L of each (big-endian on the wire). Section-B aliases:
	seekHiImmB = 0xA836 - secBBias // &6836  HIGH word immediate (LE: low byte here, high at +1)
	seekLoImmB = 0xA843 - secBBias // &6843  LOW  word immediate

	hdWpDVarB = 0x80C8 - secBBias // &40C8  hd.wp write-protect DVAR (must be 0 to allow writes)

	// A 512-byte source/dest buffer in the section-B window, above the loaded code
	// (which ends ~real &AB2D = &6B2D) and clear of the &780F CSD buffer / &7F00
	// scratch prologue used by the other Colin tests. It must also clear the
	// harness stack: RunFrom sets SP = &6FFE and pushes the HALT-trap return there,
	// and the prologue's nested CALLs grow the stack just below &7000 — so &7000 is
	// NOT safe (a buffer there is clobbered mid-write). &7400 sits clear of both the
	// stack and the code. &7400..&75FF is free RAM.
	sectorBufB = 0x7400
	// A scratch prologue that sets HL=buffer, C=&DF, then runs Colin's real CMD24
	// write core + write tail back-to-back (preserving C/HL across the two CALLs,
	// which separate RunFroms would not), then RETs. Sits clear of the buffer.
	writeProEntry = 0x7E00
)

// pokeSeekAddr writes the 32-bit sector address into the seek immediates exactly
// as Colin's seek path does (the self-modifying &A836/&A843 LD HL,nn operands).
// addr is the value the CMD24/CMD17 command frame will carry (block number for an
// SDHC card; the model keys its backing store by this verbatim value).
func pokeSeekAddr(mac *z80h.Machine, addr uint32) {
	// HIGH word (bits 31..16): the sender sends H=(&A837), L=(&A836); LD HL,nn is
	// little-endian so operand byte0 (&A836) is L, byte1 (&A837) is H.
	mac.Write(seekHiImmB, []byte{byte((addr >> 16) & 0xFF), byte((addr >> 24) & 0xFF)})
	// LOW word (bits 15..0): L=(&A843), H=(&A844).
	mac.Write(seekLoImmB, []byte{byte(addr & 0xFF), byte((addr >> 8) & 0xFF)})
}

// runColinWrite drives Colin's REAL CMD24 write of the 512-byte pattern at HL
// (sectorBufB) to the sector addressed by the poked seek immediates. It runs the
// write core (hd.svb-t &A918) and the write tail (&A86B) back-to-back in one
// RunFrom via a tiny prologue, so C (=&DF) and HL stay live across both — mirroring
// how hd.sbuf-t/&hd.svbk-t chain them. Returns the run result (Halted on clean RET).
func runColinWrite(t *testing.T, mac *z80h.Machine) z80h.CallResult {
	t.Helper()
	// Prologue: ld hl,sectorBufB; ld c,&df; call hd.svb-t; call write-tail; ret.
	mac.Write(writeProEntry, []byte{
		0x21, byte(sectorBufB & 0xFF), byte(sectorBufB >> 8), // ld hl,sectorBufB
		0x0E, 0xDF, // ld c,&df
		0xCD, byte(hdSvbB & 0xFF), byte(hdSvbB >> 8), // call hd.svb-t (&6918)
		0xCD, byte(hdTailB & 0xFF), byte(hdTailB >> 8), // call write-tail (&686B)
		0xC9, // ret
	})
	res, err := mac.RunFrom(writeProEntry, z80h.Entry{StepCap: 2_000_000})
	if err != nil {
		t.Fatalf("run Colin CMD24 write: %v", err)
	}
	return res
}

// runColinRead drives Colin's REAL CMD17 read (hd.ldb-t &A999) of the sector
// addressed by the poked seek immediates into the buffer at HL (sectorBufB). The
// read core sets its own C=&DF and counts, so it runs directly with HL preloaded.
func runColinRead(t *testing.T, mac *z80h.Machine) z80h.CallResult {
	t.Helper()
	res, err := mac.RunFrom(hdLdbB, z80h.Entry{HL: sectorBufB, StepCap: 2_000_000})
	if err != nil {
		t.Fatalf("run Colin CMD17 read: %v", err)
	}
	return res
}

// TestSDRecordIORoundTripColin proves the sector round-trip: a known 512-byte
// pattern written through Colin's REAL CMD24 path into the model, then read back
// through his REAL CMD17 path, equals the pattern. Both the model's captured
// sector AND the bytes Colin's read code landed in RAM are checked.
func TestSDRecordIORoundTripColin(t *testing.T) {
	const cSize = 0x001D59 // ~3.7 GB SDHC (block-addressed) — same card as the init test
	csd := csdV2(cSize)
	mac := loadBdosInSectionB(t) // skips if the binary is absent
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csd)
	mac.AttachIO(enc)

	// Choose a sector address the way the record/seek path would: record 5's base
	// is record#*1600 + (base+1); we use a representative absolute sector. The
	// model keys its backing store by the verbatim CMD frame address, so any value
	// the seek immediates carry is fine — the round-trip proves the SPI path, and
	// the address-derivation is validated independently (runColinRecords).
	const sector uint32 = 5*1600 + 1234 // a plausible in-record sector

	// hd.wp must be clear or hd.svb-t's WP check aborts to the error path.
	mac.Write(hdWpDVarB, []byte{0x00})

	// A recognisable 512-byte pattern (not all-equal, so a stuck/duplicated byte is
	// caught): byte i = (i*7 + 0x5A) XOR (i>>3).
	pattern := make([]byte, 512)
	for i := range pattern {
		pattern[i] = byte((i*7+0x5A)^(i>>3)) & 0xFF
	}

	// --- WRITE: poke the address, load the pattern, run Colin's real CMD24. ---
	pokeSeekAddr(mac, sector)
	mac.Write(sectorBufB, pattern)
	wres := runColinWrite(t, mac)
	if !wres.Halted {
		t.Fatalf("CMD24 write did not RET cleanly (PC=&%04X, halted=%v) — likely the data-response/busy handshake stalled",
			wres.PC, wres.Halted)
	}

	// The model's backing store must hold exactly the 512 bytes Colin's CMD24 wrote.
	captured, ok := sd.CapturedSector(sector)
	if !ok {
		t.Fatalf("model captured no sector at addr %d — CMD24 write phase never committed", sector)
	}
	if !bytes.Equal(captured, pattern) {
		t.Fatalf("model-captured sector != written pattern:\n  first diff at %d (got 0x%02X want 0x%02X)",
			firstDiff(captured, pattern), captured[firstDiff(captured, pattern)], pattern[firstDiff(captured, pattern)])
	}
	t.Logf("CMD24: Colin's real write core+tail captured 512 bytes into the model at sector %d", sector)

	// --- READ-BACK: clear the buffer, run Colin's real CMD17 of the same sector. ---
	mac.Write(sectorBufB, make([]byte, 512)) // zero the buffer so a no-op read is visible
	rres := runColinRead(t, mac)
	if !rres.Halted {
		t.Fatalf("CMD17 read did not RET cleanly (PC=&%04X) — likely the &FE token wait stalled", rres.PC)
	}
	readBack := mac.Read(sectorBufB, 512)

	// Colin's CMD17 INI loop lands 510 bytes (254+256, per hd.ldb-t &A999); the full
	// 512 streamed by the model covers them. Compare the bytes Colin's code actually
	// landed against the pattern — these are the sector that round-tripped through
	// the model's CMD24-write + CMD17-read using Colin's REAL SPI code.
	if d := firstDiff(readBack, pattern); d >= 0 {
		// Tolerate only a trailing-2-byte shortfall (the 510-vs-512 INI count): every
		// byte Colin's read loop transferred must match. A mismatch in the first 510
		// is a real model/driver disagreement and fails.
		if d < 510 {
			t.Fatalf("CMD17 read-back != written pattern at byte %d: got 0x%02X want 0x%02X\n  read[%d..]=% 02x\n  want[%d..]=% 02x",
				d, readBack[d], pattern[d], d, tailSlice(readBack, d), d, tailSlice(pattern, d))
		}
		t.Logf("CMD17: first 510 bytes round-trip; bytes %d..511 not transferred by hd.ldb-t's 510-INI count (expected)", d)
	} else {
		t.Logf("CMD17: all 512 bytes round-trip identically")
	}
	t.Logf("ROUND-TRIP PASS: sector %d written via Colin's real CMD24 and read back via his real CMD17 through the model", sector)
}

// TestSDRecordReadSeededColin proves the CMD17 read path independently: pre-seed a
// sector in the model (no CMD24 involved), then read it with Colin's real CMD17 —
// the bytes Colin's code lands must equal the seed. This isolates the read model
// from the write model so a round-trip pass cannot be a write/read bug cancelling.
func TestSDRecordReadSeededColin(t *testing.T) {
	const cSize = 0x001D59
	csd := csdV2(cSize)
	mac := loadBdosInSectionB(t)
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csd)
	mac.AttachIO(enc)

	const sector uint32 = 42
	seed := make([]byte, 512)
	for i := range seed {
		seed[i] = byte(0xA5 ^ (i & 0xFF))
	}
	sd.SeedSector(sector, seed)

	pokeSeekAddr(mac, sector)
	mac.Write(sectorBufB, make([]byte, 512))
	rres := runColinRead(t, mac)
	if !rres.Halted {
		t.Fatalf("CMD17 read of seeded sector did not RET cleanly (PC=&%04X)", rres.PC)
	}
	readBack := mac.Read(sectorBufB, 512)
	if d := firstDiff(readBack, seed); d >= 0 && d < 510 {
		t.Fatalf("seeded CMD17 read != seed at byte %d: got 0x%02X want 0x%02X", d, readBack[d], seed[d])
	}
	t.Logf("CMD17 seeded read: Colin's real read of a pre-seeded sector matches (first 510 bytes)")
}

// firstDiff returns the index of the first differing byte, or -1 if equal up to
// the shorter length (and lengths match).
func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}

func tailSlice(b []byte, from int) []byte {
	end := from + 6
	if end > len(b) {
		end = len(b)
	}
	return b[from:end]
}
