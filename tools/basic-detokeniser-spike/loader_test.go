package main

import (
	"testing"
)

func TestPosAdvance_StaysInSamePage(t *testing.T) {
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x100)
	want := pos{page: 1, offset: 0x1DD5}
	if got != want {
		t.Errorf("advance(0x100): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_CrossesPage1To2AtExactBoundary(t *testing.T) {
	// page 1 holds 0x4000 - 0x1CD5 = 0x232B bytes from PROG.
	// Advancing exactly 0x232B from (1, 0x1CD5) lands on (2, 0x0000).
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x232B)
	want := pos{page: 2, offset: 0x0000}
	if got != want {
		t.Errorf("advance(0x232B): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_CrossesPage1To2WithRemainder(t *testing.T) {
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x232C) // one past the boundary
	want := pos{page: 2, offset: 0x0001}
	if got != want {
		t.Errorf("advance(0x232C): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_SpansMultiplePages(t *testing.T) {
	// 50000 bytes from (1, 0x1CD5):
	//   page 1 absorbs 0x4000 - 0x1CD5 = 0x232B = 9003 bytes
	//   page 2 absorbs 0x4000 = 16384 bytes (cumulative 25387)
	//   page 3 absorbs 0x4000 = 16384 bytes (cumulative 41771)
	//   page 4 absorbs 50000 - 41771 = 8229 = 0x2025 bytes
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(50000)
	want := pos{page: 4, offset: 0x2025}
	if got != want {
		t.Errorf("advance(50000): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_ZeroIsIdentity(t *testing.T) {
	p := pos{page: 7, offset: 0x1234}
	got := p.advance(0)
	if got != p {
		t.Errorf("advance(0): got %+v, want %+v", got, p)
	}
}

func TestPokeRAMPage_WritesToCorrectPhysicalPage(t *testing.T) {
	hw := &Hardware{}
	// Default LMPR=0, HMPR=0 — would map section C to page 0. The
	// helper must ignore that and write to page 7 regardless.
	pokeRAMPage(hw, 7, 0x1234, 0xAB)
	if got := hw.ram[7][0x1234]; got != 0xAB {
		t.Errorf("hw.ram[7][0x1234] = %02X, want 0xAB", got)
	}
	// Spot-check: page 0 same offset is untouched.
	if got := hw.ram[0][0x1234]; got != 0x00 {
		t.Errorf("hw.ram[0][0x1234] = %02X, want 0x00 (other pages untouched)", got)
	}
}

func TestPokeRAMPage_MasksPageAndOffset(t *testing.T) {
	hw := &Hardware{}
	// Page 0x25 has bit 5 set — should mask to 0x05 (within 32 pages).
	// Offset 0x8000 has section-C bit — should mask to 0x0000.
	pokeRAMPage(hw, 0x25, 0x8000, 0xCD)
	if got := hw.ram[5][0]; got != 0xCD {
		t.Errorf("hw.ram[5][0] = %02X, want 0xCD (page/offset masked)", got)
	}
}

func TestSetSysvarPair_WritesPageByteAndSectionCOffset(t *testing.T) {
	hw := &Hardware{}
	hw.lmpr = 0
	hw.hmpr = 1 // sysvars at 0x5A** live in section B = page 1 with LMPR=0

	setSysvarPair(hw, sysNVARSP, sysNVARS, pos{page: 4, offset: 0x1234})

	// Page byte at sysNVARSP (0x5A87) should be 4.
	if got := peekRAM(hw, sysNVARSP); got != 4 {
		t.Errorf("NVARSP = %02X, want 04", got)
	}
	// 16-bit offset at sysNVARS (0x5A88) should be 0x8000 | 0x1234 = 0x9234.
	if got := peekRAM16(hw, sysNVARS); got != 0x9234 {
		t.Errorf("NVARS = %04X, want 9234 (section-C form of 0x1234)", got)
	}
}

func TestSetSysvarPair_ZeroOffsetGetsSectionCBit(t *testing.T) {
	hw := &Hardware{}
	hw.lmpr = 0
	hw.hmpr = 1

	setSysvarPair(hw, sysSAVARSP, sysSAVARS, pos{page: 2, offset: 0})

	if got := peekRAM(hw, sysSAVARSP); got != 2 {
		t.Errorf("SAVARSP = %02X, want 02", got)
	}
	// Offset 0 → section-C-form 0x8000 (NOT 0x0000).
	if got := peekRAM16(hw, sysSAVARS); got != 0x8000 {
		t.Errorf("SAVARS = %04X, want 8000", got)
	}
}

func TestCheckFits_FitsOn512K(t *testing.T) {
	// Exact max on 512K (PRAMTP=0x1F): available = 32*0x4000 - 0x4000
	// - 0x1CD5 = 500523 bytes; subtract trailer 1024 + headroom 150 =
	// 499349 max program. 480 KB is safely under that.
	err := checkFits(480*1024, 0x1F)
	if err != nil {
		t.Errorf("480 KB on PRAMTP=0x1F: unexpected error: %v", err)
	}
}

func TestCheckFits_FitsOn256K(t *testing.T) {
	// 200 KB on a 256 K (PRAMTP=0x0F) machine fits.
	err := checkFits(200*1024, 0x0F)
	if err != nil {
		t.Errorf("200 KB on PRAMTP=0x0F: unexpected error: %v", err)
	}
}

func TestCheckFits_ExceedsOn256K(t *testing.T) {
	// 400 KB on a 256 K machine does not fit.
	err := checkFits(400*1024, 0x0F)
	if err == nil {
		t.Errorf("400 KB on PRAMTP=0x0F: expected error, got nil")
	}
}

func TestCheckFits_ExceedsOn512K(t *testing.T) {
	// 600 KB on a 512 K machine does not fit (limit is ~488 KB).
	err := checkFits(600*1024, 0x1F)
	if err == nil {
		t.Errorf("600 KB on PRAMTP=0x1F: expected error, got nil")
	}
}

// postBootState returns a Hardware initialised to the post-boot state
// loadProgViaPoke expects: LMPR=0, HMPR=1, PRAMTP=0x1F (512K), and
// every BASIC sysvar pair set to its canonical post-boot value with
// PROG = 0x9CD5. Mirrors what running the ROM boot sequence would
// produce, without needing the ROM image.
func postBootState() *Hardware {
	hw := &Hardware{}
	hw.lmpr = 0
	hw.hmpr = 1
	hw.vmpr = 0 // screen page doesn't matter for loader tests

	// PROG (always at 0x9CD5 = page 1, offset 0x1CD5).
	pokeRAM(hw, sysPROGP, 1)
	pokeRAM16(hw, sysPROG, 0x9CD5)
	// NVARS = PROG + 1 (the 0xFF end-of-program sentinel slot).
	pokeRAM(hw, sysNVARSP, 1)
	pokeRAM16(hw, sysNVARS, 0x9CD6)
	// NUMEND = NVARS + 92.
	pokeRAM(hw, sysNUMENDP, 1)
	pokeRAM16(hw, sysNUMEND, 0x9D32)
	// SAVARS = NUMEND + 512.
	pokeRAM(hw, sysSAVARSP, 1)
	pokeRAM16(hw, sysSAVARS, 0x9F32)
	// ELINE = SAVARS (no saved string vars in a fresh state).
	pokeRAM(hw, sysELINEP, 1)
	pokeRAM16(hw, sysELINE, 0x9F32)
	// WORKSP = ELINE + 1.
	pokeRAM(hw, sysWORKSPP, 1)
	pokeRAM16(hw, sysWORKSP, 0x9F33)
	// WKEND = WORKSP.
	pokeRAM(hw, sysWKENDP, 1)
	pokeRAM16(hw, sysWKEND, 0x9F33)
	// CHAD, KCUR, NXTLINE — track ELINE in fresh state.
	pokeRAM(hw, sysCHADP, 1)
	pokeRAM16(hw, sysCHAD, 0x9F32)
	pokeRAM(hw, sysKCURP, 1)
	pokeRAM16(hw, sysKCUR, 0x9F32)
	pokeRAM(hw, sysNXTLINEP, 1)
	pokeRAM16(hw, sysNXTLINE, 0x9CD5)

	// Physical RAM ceiling: 512K = pages 0..31.
	pokeRAM(hw, sysPRAMTP, 0x1F)
	// BASIC owns pages 0..3 at boot.
	pokeRAM(hw, sysLASTPAGE, 0x03)
	pokeRAM(hw, sysRAMTOPP, 0x03)
	pokeRAM16(hw, sysRAMTOP, 0xBFFF)
	// ALLOCT: pages 0..3 marked "IN USE, CONTEXT 0".
	for p := uint8(0); p <= 3; p++ {
		pokeRAM(hw, allocTableBase+uint16(p), 0x40)
	}

	// Stage canonicalNumericVars at NVARS (matches ROM boot's CLRSR init).
	nvars := peekRAM16(hw, sysNVARS)
	for i, b := range canonicalNumericVars {
		pokeRAM(hw, nvars+uint16(i), b)
	}
	// 512-byte gap above NVARS+92 is zero-initialised by virtue of hw.ram
	// starting zero.

	return hw
}

// snapshotRAMRange captures hw.ram[page][offset:offset+n] into a slice
// for byte-level assertions in tests.
func snapshotRAMRange(hw *Hardware, page uint8, offset, n int) []byte {
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		p := pos{page: page, offset: uint16(offset)}.advance(i)
		out[i] = hw.ram[p.page&0x1F][p.offset&0x3FFF]
	}
	return out
}

// A minimal valid tokenised BASIC program: one line numbered 10
// containing "PRINT 1", followed by the 0xFF end-of-program sentinel.
// Per docs/notes/sam-basic-save-format.md, each line is
// lineNumBE(2) + lineLenLE(2) + tokenised_body + 0x0D, where lineLen
// counts (tokenised_body + 0x0D) — here "F0 20 31 0D" = 4 bytes.
//
//	line 10:  00 0A    04 00    F0 20 31    0D
//	end:      FF
//
// Total 9 bytes.
var smallProgram = []byte{
	0x00, 0x0A, // line number 10 (big-endian)
	0x04, 0x00, // lineLen = 4 (little-endian): covers "F0 20 31 0D"
	0xF0,       // PRINT token
	0x20, 0x31, // " 1"
	0x0D, // line terminator
	0xFF, // end-of-program sentinel
}

// makeProgram synthesises a tokenised BASIC body roughly bodyLen bytes
// long, structured as many `<lineNum> REM xxx...` lines so the total
// reaches the target size. Used to exercise the multi-page loader.
func makeProgram(t *testing.T, bodyLen int) []byte {
	t.Helper()
	const remPadPerLine = 200 // 200 bytes of REM payload per line
	var prog []byte
	lineNum := uint16(10)
	for len(prog) < bodyLen-1 {
		// Line: lineNum_BE(2) + lineLen_LE(2) + 0xEA + payload + 0x0D
		// lineLen counts payload + 0x0D = remPadPerLine + 2 bytes.
		payload := make([]byte, remPadPerLine)
		for i := range payload {
			payload[i] = byte('A' + (i % 26))
		}
		line := []byte{
			byte(lineNum >> 8), byte(lineNum & 0xFF),
			byte(remPadPerLine + 2), 0x00,
			0xEA, // REM token
		}
		line = append(line, payload...)
		line = append(line, 0x0D)
		prog = append(prog, line...)
		lineNum += 10
	}
	prog = append(prog, 0xFF) // end-of-program sentinel
	return prog
}

func TestLoadProg_MultiPage_40K_ProgramSpansThreePages(t *testing.T) {
	hw := postBootState()

	// 40 KB target body (makeProgram rounds up; actual size will be
	// 40000-40300ish). Layout:
	//   page 1 from offset 0x1CD5 holds the first 0x232B = 9003 bytes
	//   page 2 from offset 0      holds the next 0x4000 = 16384 bytes
	//   page 3 from offset 0      holds the remainder (~14600 bytes)
	body := makeProgram(t, 40000)
	if len(body) < 40000 {
		t.Fatalf("makeProgram produced %d bytes, want >= 40000", len(body))
	}
	if len(body) > 0x232B+0x4000+0x4000 {
		t.Fatalf("makeProgram produced %d bytes which would overflow into page 4 — test assumes pages 1..3 only", len(body))
	}

	loadProgViaPoke(hw, body)

	// First 0x232B bytes of program live in page 1 from offset 0x1CD5.
	firstChunk := snapshotRAMRange(hw, 1, 0x1CD5, 0x232B)
	for i, b := range firstChunk {
		if b != body[i] {
			t.Fatalf("page 1 byte %d (program offset %d): got %02X, want %02X",
				i, i, b, body[i])
		}
	}

	// Next 0x4000 bytes live in page 2 from offset 0.
	secondChunk := snapshotRAMRange(hw, 2, 0, 0x4000)
	for i, b := range secondChunk {
		if b != body[0x232B+i] {
			t.Fatalf("page 2 byte %d: got %02X, want %02X (program offset %d)",
				i, b, body[0x232B+i], 0x232B+i)
		}
	}

	// Remainder of program in page 3.
	remaining := len(body) - 0x232B - 0x4000
	if remaining > 0 {
		thirdChunk := snapshotRAMRange(hw, 3, 0, remaining)
		for i, b := range thirdChunk {
			if b != body[0x232B+0x4000+i] {
				t.Fatalf("page 3 byte %d: got %02X, want %02X",
					i, b, body[0x232B+0x4000+i])
			}
		}
	}

	// NVARS lives just past the program — its page byte should be 3
	// (since program crossed into page 3) and offset should reflect
	// the remainder.
	expectedNVARSPos := pos{page: 1, offset: 0x1CD5}.advance(len(body))
	if got := peekRAM(hw, sysNVARSP); got != expectedNVARSPos.page {
		t.Errorf("NVARSP: got %02X, want %02X (program ends in page %d)",
			got, expectedNVARSPos.page, expectedNVARSPos.page)
	}
	wantNVARS := uint16(0x8000) | (expectedNVARSPos.offset & 0x3FFF)
	if got := peekRAM16(hw, sysNVARS); got != wantNVARS {
		t.Errorf("NVARS: got %04X, want %04X", got, wantNVARS)
	}

	// canonicalNumericVars relocated to new NVARS position (across page
	// boundary if needed).
	for i, b := range canonicalNumericVars {
		p := expectedNVARSPos.advance(i)
		got := hw.ram[p.page&0x1F][p.offset&0x3FFF]
		if got != b {
			t.Errorf("vars byte %d at (page=%d, off=%04X): got %02X, want %02X",
				i, p.page, p.offset, got, b)
		}
	}
}

func TestLoadProg_SmallProgram_SysvarPairsBumpedByDelta(t *testing.T) {
	hw := postBootState()

	// Capture pre-load NVARS/NUMEND/SAVARS/NXTLINE for delta math.
	preNVARS := peekRAM16(hw, sysNVARS)
	preNUMEND := peekRAM16(hw, sysNUMEND)
	preSAVARS := peekRAM16(hw, sysSAVARS)
	preNXTLINE := peekRAM16(hw, sysNXTLINE)

	loadProgViaPoke(hw, smallProgram)

	delta := uint16(len(smallProgram)) - 1 // current loader's delta convention

	// New NVARS = PROG + len = 0x9CD5 + 9 = 0x9CDE (len = 9 bytes).
	if got, want := peekRAM16(hw, sysNVARS), preNVARS+delta; got != want {
		t.Errorf("NVARS after load: got %04X, want %04X (= preNVARS %04X + delta %d)",
			got, want, preNVARS, delta)
	}
	if got, want := peekRAM16(hw, sysNUMEND), preNUMEND+delta; got != want {
		t.Errorf("NUMEND: got %04X, want %04X", got, want)
	}
	if got, want := peekRAM16(hw, sysSAVARS), preSAVARS+delta; got != want {
		t.Errorf("SAVARS: got %04X, want %04X", got, want)
	}
	// NXTLINE has a signed delta of -1 (at boot NXTLINE = PROG = NVARS - 1).
	// After load it should land at the byte before NVARS, which is the 0xFF
	// end-of-program sentinel of the program.
	if got, want := peekRAM16(hw, sysNXTLINE), preNXTLINE+delta; got != want {
		t.Errorf("NXTLINE: got %04X, want %04X (= preNXTLINE %04X + delta %d)",
			got, want, preNXTLINE, delta)
	}

	// Page bytes all 1 (small program, everything in page 1).
	for name, addr := range map[string]uint16{
		"NVARSP": sysNVARSP, "NUMENDP": sysNUMENDP, "SAVARSP": sysSAVARSP,
		"WKENDP": sysWKENDP, "WORKSPP": sysWORKSPP, "ELINEP": sysELINEP,
		"CHADP": sysCHADP, "KCURP": sysKCURP, "NXTLINEP": sysNXTLINEP,
		"PROGP": sysPROGP,
	} {
		if got := peekRAM(hw, addr); got != 1 {
			t.Errorf("%s after load: got %02X, want 01 (small program stays in page 1)", name, got)
		}
	}

	// Program bytes are at PROG (= page 1, offset 0x1CD5).
	gotProg := snapshotRAMRange(hw, 1, 0x1CD5, len(smallProgram))
	for i, b := range gotProg {
		if b != smallProgram[i] {
			t.Errorf("program byte %d: got %02X, want %02X", i, b, smallProgram[i])
		}
	}

	// canonicalNumericVars relocated to new NVARS position.
	newNVARS := peekRAM16(hw, sysNVARS)
	gotVars := snapshotRAMRange(hw, 1, int(newNVARS&0x3FFF), len(canonicalNumericVars))
	for i, b := range gotVars {
		if b != canonicalNumericVars[i] {
			t.Errorf("vars byte %d: got %02X, want %02X", i, b, canonicalNumericVars[i])
		}
	}

	// ALLOCT untouched beyond the boot 0..3 (no new pages claimed).
	for p := uint8(4); p <= 0x1F; p++ {
		if got := peekRAM(hw, allocTableBase+uint16(p)); got != 0 {
			t.Errorf("ALLOCT[%d]: got %02X, want 00 (small program shouldn't claim pages)", p, got)
		}
	}
	if got := peekRAM(hw, sysLASTPAGE); got != 3 {
		t.Errorf("LASTPAGE: got %02X, want 03 (no extension for small program)", got)
	}
}
