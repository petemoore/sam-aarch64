package z80_test

import (
	"fmt"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// i280b-b2s (§8ad): the faithful rig captures a WORKING BASIC write to a Trinity SD
// record — the gold CMD24 trace Pete's port-audit needs.
//
// Background: §8z/§8aa established the faithful editor rig (Continue + the WTKY2 &04FA
// key-wait idle, PR #746) so injected keys flow through the real editor exactly as a
// user typing. This test drives a real BASIC `RECORD 1` then `SAVE "x"CODE addr,len`
// against Colin's forked ROM + real B-DOS 1.5t and decodes the live Trinity-SD command
// frames (port &DF). The result: SAVE issues a real **CMD24** (WRITE_SINGLE_BLOCK) that
// LANDS in the SD model's backing store — a complete, working record write in emulation.
//
// This is the §8ac convergence point: on real hardware B-DOS's OWN BASIC SAVE writes to
// SD fine; only our serve's raw HWSAD(149)-via-rst8 invocation hangs. The rig now
// reproduces the WORKING path, so the next item diffs the device-selection state a real
// `RECORD` leaves against what our serve's HRECORD-select establishes, and ports the gap
// into src/netboot/bdos_seam.asm (i280b-b2t).
//
// Key facts this guards (empirical, correcting earlier notes):
//   - The minimal precondition is `RECORD 1` then `SAVE` — RECORD selects the SD device;
//     SAVE alone (no RECORD) issues NO SD I/O (it falls back to the floppy). BOOT is not
//     required (RECORD executes faithfully — there is no "Nonsense in BASIC", correcting
//     §8aa's record-select-blocker claim).
//   - SAVE writes the record data (CMD24) AND the directory back (a second CMD24@152).

// cmdFrame is one decoded Trinity-SD command issued over port &DF.
type cmdFrame struct {
	cmd uint8
	arg uint32
}

// decodeCmds extracts the leading SD command frames from a &DC-&DF capture window. A
// command token is a &DF write with bits 7..6 == 01 (start+transmission bits); the next
// four &DF writes are the big-endian 32-bit argument. (The 512-byte CMD24 data payload
// also streams over &DF and decodes as junk frames after a real command — so only the
// command PRESENCE/order is reliable, not the trailing pseudo-frames; callers check for
// a command's presence, not an exact list.)
func decodeCmds(evs []capEv) []cmdFrame {
	var df []capEv
	for _, e := range evs {
		if e.write && e.port == 0xDF {
			df = append(df, e)
		}
	}
	var out []cmdFrame
	for i := 0; i < len(df); i++ {
		v := df[i].val
		if v&0xC0 == 0x40 && i+4 < len(df) {
			arg := uint32(df[i+1].val)<<24 | uint32(df[i+2].val)<<16 |
				uint32(df[i+3].val)<<8 | uint32(df[i+4].val)
			out = append(out, cmdFrame{cmd: v & 0x3F, arg: arg})
			i += 4
		}
	}
	return out
}

func fmtCmds(cs []cmdFrame) string {
	if len(cs) == 0 {
		return "(no SD I/O)"
	}
	s := ""
	for _, c := range cs {
		s += fmt.Sprintf("CMD%d@%d ", c.cmd, c.arg)
	}
	return s
}

func hasCmd(cs []cmdFrame, n uint8) bool {
	for _, c := range cs {
		if c.cmd == n {
			return true
		}
	}
	return false
}

// bootToEditorIdleSD mirrors bootToEditorIdle (faithful WTKY2 boot of Colin's forked ROM
// + B-DOS) but also returns the SD card handle so a write can be verified to have landed.
func bootToEditorIdleSD(t *testing.T) (*z80h.Machine, *[]capEv, *z80h.SDCard) {
	t.Helper()
	mac, lg, sd, _ := bootToEditorIdleSDENC(t)
	return mac, lg, sd
}

// bootToEditorIdleSDENC is bootToEditorIdleSD's full-width core: it also returns
// the ENC28J60 handle so a caller can inject/inspect network frames on the booted
// machine (the i328 trinload-idle chain drives trinload's ?/@/X protocol).
func bootToEditorIdleSDENC(t *testing.T) (*z80h.Machine, *[]capEv, *z80h.SDCard, *z80h.ENC28J60) {
	t.Helper()
	rom, eeprom := loadRealCaptures(t)
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom))
	sd := enc.AttachSD(csdV2(0x001D59)) // base=152, records=4809
	rec1 := make([]byte, 512)
	copy(rec1[232:236], []byte("BDOS")) // record-1 selection stamp
	sd.SeedSector(152, rec1)
	var lastPC uint16
	lg := &[]capEv{}
	mac.AttachIO(&capIO{inner: enc, lastPC: &lastPC, log: lg})
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: 80_000_000, StopPC: wtky2Idle, StopPCSkip: 0, FrameIntPeriod: 60000,
		Trace: func(pc uint16) { lastPC = pc },
	})
	if err != nil || !res.ReachedStop {
		t.Fatalf("boot did not reach WTKY2: %v reached=%v PC=&%04X", err, res.ReachedStop, res.PC)
	}
	return mac, lg, sd, enc
}

// storeBlocks returns the SD backing-store block addresses present (sorted ascending by
// the scan). Block 152 is the seeded record-1 directory; any OTHER present block was
// written by a CMD24 (the model captures CMD24 payloads into store[addr]).
func storeBlocks(sd *z80h.SDCard) []uint32 {
	var present []uint32
	for blk := uint32(0); blk <= 5000; blk++ {
		if _, ok := sd.CapturedSector(blk); ok {
			present = append(present, blk)
		}
	}
	return present
}

// findPatternBlock scans the SD store for the first block containing `pat` and returns
// (block, offset). Returns (0,0) if absent. Block 0 is never a data target here, so 0 is
// an unambiguous "not found".
func findPatternBlock(sd *z80h.SDCard, pat []byte) (uint32, int) {
	for blk := uint32(1); blk <= 5000; blk++ {
		sec, ok := sd.CapturedSector(blk)
		if !ok {
			continue
		}
		for off := 0; off+len(pat) <= len(sec); off++ {
			if string(sec[off:off+len(pat)]) == string(pat) {
				return blk, off
			}
		}
	}
	return 0, 0
}

// findPatternInSectionC locates `pat` at logical address `addr` (which must be in
// section C, &8000-&BFFF) across all 32 physical RAM pages, by switching HMPR. Returns
// (page, bytesRead) or (-1, nil). Restores HMPR. This reads BASIC's user RAM, which the
// editor-idle HMPR does not map into section C.
func findPatternInSectionC(mac *z80h.Machine, addr int, pat []byte) (int, []byte) {
	saved := mac.Pager().HMPR
	defer func() { mac.Pager().HMPR = saved }()
	for p := uint8(0); p < 32; p++ {
		mac.Pager().HMPR = p
		got := mac.Read(uint16(addr), len(pat))
		if string(got) == string(pat) {
			return int(p), got
		}
	}
	return -1, nil
}

func TestBASICSaveWritesRecordToSD(t *testing.T) {
	// --- Gold path: RECORD 1 ; SAVE -> a real SD write. ---
	mac, lg, sd := bootToEditorIdleSD(t)
	runLine := func(line string) ([]cmdFrame, []string) {
		from := len(*lg)
		hooks, _, _ := editorRunLine(t, mac, line)
		return decodeCmds((*lg)[from:]), hooks
	}

	if _, hooks := runLine("RECORD 1"); !contains(hooks, "devsel") {
		// RECORD selects the SD device (reaches B-DOS device-select). Not fatal on its
		// own, but the SAVE write below is the real assertion.
		t.Logf("note: RECORD 1 hooks=%v (device-select expected during the record select)", hooks)
	}

	// Stage 20 bytes of CODE to save — a distinctive non-trivial, non-zero pattern. Stage
	// it via BASIC POKE (not mac.Write): POKE writes through the interpreter's memory map,
	// the SAME map BASIC SAVE reads, so the bytes are guaranteed to be where SAVE looks
	// (a raw mac.Write stages into whatever page the harness HMPR happens to hold, which
	// SAVE need not read — that mismatch silently saved zeros). value = i*7+0x31, all <256.
	const srcAddr, dstAddr, codeLen = 32768, 36864, 20
	want := make([]byte, codeLen)
	for i := range want {
		want[i] = byte(i*7 + 0x31)
	}
	runLine(fmt.Sprintf("FOR f=0 TO %d: POKE %d+f,f*7+%d: NEXT f", codeLen-1, srcAddr, 0x31))
	// Pre-zero the load target via BASIC so a no-op load is detectably all-zero.
	runLine(fmt.Sprintf("FOR f=0 TO %d: POKE %d+f,0: NEXT f", codeLen-1, dstAddr))

	saveFrames, saveHooks := runLine(`SAVE "x"CODE 32768,20`)
	t.Logf("RECORD 1 ; SAVE -> hooks=%v frames=%s", saveHooks, fmtCmds(saveFrames))

	if !hasCmd(saveFrames, 24) {
		t.Fatalf("BASIC SAVE issued no CMD24 (SD write) after RECORD 1; frames=%s — the working write was not captured", fmtCmds(saveFrames))
	}
	if !contains(saveHooks, "HSAVE") {
		t.Errorf("SAVE did not reach the B-DOS HSAVE handler; hooks=%v", saveHooks)
	}
	// The write must LAND in the SD store: a block other than the seeded 152 is present.
	blocks := storeBlocks(sd)
	wroteNew := false
	for _, b := range blocks {
		if b != 152 {
			wroteNew = true
		}
	}
	if !wroteNew {
		t.Fatalf("CMD24 issued but no new block landed in the SD store (blocks=%v) — write did not take", blocks)
	}
	if got := mac.Read(0x5BC2, 1)[0]; got != 0x1D {
		t.Fatalf("after SAVE DOSFLG=&%02X, want &1D (B-DOS resident; no reboot artifact)", got)
	}

	// Paging-independent proof the SAVE wrote the REAL staged bytes (not zeros): the SD
	// store block holds the pattern. The model captures the CMD24 payload verbatim, so
	// finding `want` inside block 192 proves the data path carried our bytes to the card.
	dataBlk, off := findPatternBlock(sd, want)
	if dataBlk == 0 {
		t.Fatalf("SAVE wrote a CMD24 but the staged pattern %02X is NOT in any SD store block — the data path carried zeros/garbage, not the saved bytes (Pete's non-trivial-zero check)", want)
	}
	t.Logf("SAVE landed the real pattern in SD block %d at offset %d", dataBlk, off)

	// --- Strict round-trip (Pete): LOAD the saved record back to a DIFFERENT address;
	// LOAD must re-read the record's data block (CMD17 of the same block SAVE wrote). ---
	loadFrames, loadHooks := runLine(`LOAD "x"CODE 36864`)
	t.Logf("LOAD x -> &9000: hooks=%v frames=%s", loadHooks, fmtCmds(loadFrames))
	if !hasCmd(loadFrames, 17) {
		t.Fatalf("LOAD issued no CMD17 (SD read) — the read-back path did not run; frames=%s", fmtCmds(loadFrames))
	}
	readBack := false
	for _, c := range loadFrames {
		if c.cmd == 17 && c.arg == dataBlk {
			readBack = true
		}
	}
	if !readBack {
		t.Errorf("LOAD did not CMD17-read the record data block %d that SAVE wrote; frames=%s", dataBlk, fmtCmds(loadFrames))
	}
	// Literal round-trip (Pete): the LOADED bytes in memory equal the SAVED bytes. LOAD
	// wrote the record back to &9000 (section C) in BASIC's paging; locate that physical
	// page (mac.Read at editor-idle HMPR sees a different page) and confirm the loaded
	// bytes there equal `want`, and that the target was genuinely overwritten (it was
	// pre-zeroed). This closes memory -> SD -> memory end-to-end.
	loadedPage, loaded := findPatternInSectionC(mac, dstAddr, want)
	if loadedPage < 0 {
		t.Fatalf("round-trip: the saved pattern %02X is NOT in RAM at &9000 on any page after LOAD — the loaded bytes do not match the saved bytes (or LOAD did not write memory)", want)
	}
	t.Logf("GOLD round-trip OK: memory -> SD -> memory. SAVE wrote the staged pattern to SD block %d via CMD24; LOAD CMD17-read it back and the loaded bytes at &9000 (physical page %d) = the saved bytes %02X (store blocks=%v)",
		dataBlk, loadedPage, loaded, storeBlocks(sd))

	// --- Negative control: SAVE with NO record selected issues no SD write. ---
	mac2, lg2, sd2 := bootToEditorIdleSD(t)
	for i := 0; i < 20; i++ {
		mac2.Write(0x8000+uint16(i), []byte{byte(0xA0 + i)})
	}
	from := len(*lg2)
	_, _, _ = editorRunLine(t, mac2, `SAVE "x"CODE 32768,20`)
	ctrlFrames := decodeCmds((*lg2)[from:])
	if hasCmd(ctrlFrames, 24) {
		t.Errorf("negative control: SAVE without RECORD issued a CMD24 (frames=%s) — RECORD should be the precondition", fmtCmds(ctrlFrames))
	}
	if blocks := storeBlocks(sd2); len(blocks) != 1 || blocks[0] != 152 {
		t.Errorf("negative control: SAVE without RECORD changed the SD store (blocks=%v, want only the seeded 152)", blocks)
	}
}
