// csd_decode_colin_test.go — POC-1 for i145a: execute Colin Piggot's REAL B-DOS
// 1.5t (beta 6) SD capacity-decode + records-math Z80 code inside this Go netboot
// harness, fed synthetic CSD registers, and assert it computes the same block
// count and BDOS-record count as an independent Go reimplementation of the
// documented formula (docs/notes/trinity-sd-z80-interface.md §6).
//
// Why: the i145 SD/CSD read must be validated against Colin's hardware-proven
// driver in emulation BEFORE anything new runs on the real SAM (which can't be
// recovered if it crashes) — CLAUDE.md §7, emulation-first. This proves the
// execution mechanism (paging the DOS code page into section B so its section-B
// alias CALLs resolve) and that Colin's capacity math agrees with the spec.
//
// NO SPI: the SPI command ladder (CMD0..CMD9) is skipped entirely. We hand-fill
// the 16-byte CSD buffer and enter Colin's code at the decode/math steps, which
// read that buffer and compute. The SD-SPI model that drives the full &A623 init
// ladder is the NEXT brick (i145c); this POC stubs the card I/O ports only enough
// for the SPI-deselect tail's busy-poll to terminate.
//
// The B-DOS 1.5t binary is Colin's PROPRIETARY code: it is referenced by its
// ~/sam-archive path (resolved from $HOME, or $BDOS15T_BIN), never copied into
// the repo. When it is absent the test FAILS HARD (i253) — unless
// SKIP_PRIVATE_TESTS=true is set, the one explicit, intentional skip gate (CI sets
// it, because this proprietary artifact can never be published).
package z80_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// ---------------------------------------------------------------------------
// Address map (real B-DOS addresses, and their section-B aliases = real-&4000).
//
// The fork's DOS page executes paged into SAM section B (&4000-&7FFF), so every
// internal CALL/data access uses a section-B alias: real X (&8000-&BFFF) appears
// at X-&4000. We therefore load the whole DOS page INTO section B and run there,
// so the &65xx/&66xx/&67xx alias CALLs in the code resolve to the right bytes.
//
//	real            section-B alias (what we use)   meaning
//	&8009 (org)     &4009                            DOS load origin
//	&A736           &6736                            CSD-decode entry (reads &780F)
//	&A3D2           —                                records math (entangled w/ UI;
//	                                                 not run standalone here)
//	&80BD-&80C0     &40BD-&40C0                       32-bit block count out
//	&80C2           &40C2                            base (record-list sectors + 1)
//	&80C4/5         &40C4/5                          BD_RECORDS (last.record) out
//	&780F-&781E     (already an alias = real &B80F)  16-byte CSD buffer in
//
// ---------------------------------------------------------------------------
const (
	bdosOrgReal = 0x8009 // file offset 0 loads here on a real SAM
	secBBias    = 0x4000 // section-B alias = real address - &4000

	// Section-B-window (where we actually load/run) addresses:
	bdosOrgB     = bdosOrgReal - secBBias // &4009
	csdDecodeB   = 0xA736 - secBBias      // &6736  CSD-decode entry
	deselectTail = 0xA664 - secBBias      // &6664  SPI-deselect+RET tail (decode jumps here)

	// Records math: the base/records computation is self-contained at &A45A (the
	// preceding &A3D2-&A44C is the human-readable size-string PRINTER, pure UI that
	// descends into ROM — we skip it). &A45A does `push de; push hl; exx` then the
	// /1600 divides, so it reads the 32-bit block count from MAIN DE:HL (which the
	// harness Entry preloads). It calls only the divides &A58C/&A590 — no print, no
	// IO — and stores base@&80C2 then BD_RECORDS@&80C4. We stop at &A4AA, the first
	// print call AFTER the BD_RECORDS store.
	recordsEntryB = 0xA45A - secBBias // &645A  push de; push hl; exx; <base/records math>
	recordsStopB  = 0xA4AA - secBBias // &64AA  first print call after the BD_RECORDS store
	csdBuf        = 0x780F            // CSD buffer (already a section-B alias of real &B80F)
	blkCountAddr  = 0x80BD - secBBias // &40BD  32-bit block count (DE@&40BD hi, HL@&40BF lo)
	baseAddr      = 0x80C2 - secBBias // &40C2  base
	recordsAddr   = 0x80C4 - secBBias // &40C4  BD_RECORDS

	// scratchPrologue holds a 6-byte stub we poke to set the alternate-AF
	// "card-type" byte (A' = 3 selects the v2/SDHC decode branch, !=3 selects v1)
	// before jumping into the CSD-decode entry. It sits in the section-B window
	// above the loaded code (which ends at real &AB2D = &6B2D) and above the CSD
	// buffer, so it collides with neither.
	scratchPrologue = 0x7F00
)

// ---------------------------------------------------------------------------
// CSD construction (generic SD physical-layer spec; SD-spec v3.01 §5.3).
//
// The 16 CSD bytes are big-endian, byte[0] first. CSD_STRUCTURE = top 2 bits of
// byte[0]. We build v1.0 (CSD_STRUCTURE=00, SDSC ≤2 GB) and v2.0 (=01, SDHC/SDXC)
// registers; Colin's decode reads byte[0]&0xC0 and the C_SIZE fields below.
// ---------------------------------------------------------------------------

// csdV2 builds a 16-byte CSD v2.0 register for an SDHC/SDXC card with the given
// 22-bit C_SIZE. Per the spec, blocks = (C_SIZE+1)*1024 (512-byte blocks).
// C_SIZE occupies bits [69:48]: byte[7] low 6 bits, byte[8], byte[9].
func csdV2(cSize uint32) [16]byte {
	var c [16]byte
	c[0] = 0x40 // CSD_STRUCTURE = 01 (v2.0), rest of byte 0 zero
	// C_SIZE [69:48] -> byte7 (bits 5..0), byte8 (15..8), byte9 (7..0).
	c[7] = byte((cSize >> 16) & 0x3F)
	c[8] = byte((cSize >> 8) & 0xFF)
	c[9] = byte(cSize & 0xFF)
	return c
}

// csdV1 builds a 16-byte CSD v1.0 register for an SDSC card. The capacity fields:
//
//	READ_BL_LEN  = byte[5] & 0x0F          (bits [83:80])
//	C_SIZE       = bits [73:62]  (12-bit)  -> byte[6] low 2 bits, byte[7], byte[8] hi 2 bits
//	C_SIZE_MULT  = bits [49:47]  (3-bit)   -> byte[9] low 2 bits, byte[10] hi bit
//
// memory capacity = (C_SIZE+1) * 2^(C_SIZE_MULT+2) * 2^READ_BL_LEN bytes;
// blocks (512 B) = capacity / 512.
func csdV1(readBlLen, cSize, cSizeMult uint32) [16]byte {
	var c [16]byte
	c[0] = 0x00 // CSD_STRUCTURE = 00 (v1.0)
	c[5] = byte(readBlLen & 0x0F)
	// C_SIZE [73:62]: byte6[1:0]=C_SIZE[11:10], byte7=C_SIZE[9:2], byte8[7:6]=C_SIZE[1:0]
	c[6] = byte((cSize >> 10) & 0x03)
	c[7] = byte((cSize >> 2) & 0xFF)
	c[8] = byte((cSize & 0x03) << 6)
	// C_SIZE_MULT [49:47]: byte9[1:0]=MULT[2:1], byte10[7]=MULT[0]
	c[9] |= byte((cSizeMult >> 1) & 0x03)
	c[10] = byte((cSizeMult & 0x01) << 7)
	return c
}

// ---------------------------------------------------------------------------
// Independent Go reference for the documented formula (the oracle Colin's code
// is checked against). Mirrors trinity-sd-z80-interface.md §6 and the disassembly
// at &A736 (capacity) and &A3D2/&A45A (records).
// ---------------------------------------------------------------------------

// refBlocksV2 = (C_SIZE+1) << 10.
func refBlocksV2(cSize uint32) uint32 { return (cSize + 1) << 10 }

// refBlocksV1 = (C_SIZE+1) * 2^(C_SIZE_MULT+2) * 2^READ_BL_LEN / 512.
func refBlocksV1(readBlLen, cSize, cSizeMult uint32) uint32 {
	bytes := (uint64(cSize) + 1) << (cSizeMult + 2) << readBlLen
	return uint32(bytes / 512)
}

// refRecords mirrors the &A45A records math:
//
//	records1 = blocks / 1600
//	base     = (records1 + 32)/32 + 1                          (stored 16-bit @ &80C2)
//	usable   = blocks - base
//	records  = usable / 1600  (+1 if (usable % 1600)/10 >= 5 tracks)   (stored 16-bit @ &80C4)
//
// IMPORTANT — 16-bit BD_RECORDS (a real characteristic of Colin's code, not an
// error): the BD_RECORDS store at &A4A7 is `ld (&80C4),hl` — only the LOW 16 bits
// of the records quotient are kept (the divide's quotient high word, in the alt
// DE, is discarded at &A486 `push hl`). So a card whose record count exceeds
// 65535 (≈ >51 GB, since 65535 × 800 KB ≈ 51 GB) WRAPS. refRecordsFull returns the
// true 32-bit count; refRecords returns the 16-bit value Colin's code actually
// stores (true value mod 2^16) — that is what the running Z80 code is checked
// against. The overflow is surfaced in the test log.
func refRecordsFull(blocks uint32) (base, records uint32) {
	records1 := blocks / 1600
	listSize := (records1 + 32) / 32
	base = listSize + 1
	usable := blocks - base
	records = usable / 1600
	rem := usable % 1600
	if rem/10 >= 5 {
		records++
	}
	return base, records
}

// refRecords returns the values Colin's code stores: base and the 16-bit-truncated
// BD_RECORDS. (base is also a 16-bit store but never exceeds 16 bits for any SD
// card — even a 2 TB card is base ≈ 80000... actually base CAN exceed 16 bits for
// very large cards; for the cards under test it does not, and the divergence of
// interest is the records overflow, surfaced explicitly.)
func refRecords(blocks uint32) (base, records uint32) {
	base, full := refRecordsFull(blocks)
	return base & 0xFFFF, full & 0xFFFF
}

// ---------------------------------------------------------------------------
// Harness wiring.
// ---------------------------------------------------------------------------

// colinBdosBinPath resolves Colin's proprietary binary from $BDOS15T_BIN or the
// default ~/sam-archive path. Returns "" if neither exists (the test then skips).
func colinBdosBinPath() string {
	if p := os.Getenv("BDOS15T_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, "sam-archive", "bdos", "analysis", "extracted", "bdos15t-beta6.bin")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// sdPortStub returns "not busy" on the Trinity &DC status port (bit 3 = busy,
// here cleared) so the SPI-deselect tail's busy-poll (`in a,(&dc); and 8; ret z`)
// terminates, and 0xFF on the &DF SPI-data relay (unused — we hand-fill the CSD,
// run no real SPI). This is the minimal port model POC-1 needs; the full SD-SPI
// model that answers a real CMD9 is the next brick.
type sdPortStub struct{}

func (sdPortStub) In(port uint8) uint8 {
	if port == 0xDC {
		return 0x00 // bit 3 (busy) clear => card ready
	}
	return 0xFF
}
func (sdPortStub) Out(port uint8, value uint8) {}

// loadBdosInSectionB loads the whole DOS page into the section-B RAM window
// (&4000-&7FFF) at the section-B org &4009, so the code's section-B-alias CALLs
// and data accesses resolve. It configures the pager so logical &4000-&7FFF is a
// writable RAM page, attaches the SD port stub, and returns the machine.
//
// Mechanism (the recipe for the next stage): the pager's flat default maps
// section B (&4000-&7FFF) to RAM page 2 (LMPR=&21). We load the binary there via
// Machine.Write at the section-B org and run with RunFrom on section-B-window
// addresses. No LMPR/HMPR change is needed — section B is already RAM in the flat
// config, and the entire computation lives within &4000-&7FFF.
func loadBdosInSectionB(t *testing.T) *z80h.Machine {
	t.Helper()
	if os.Getenv("SKIP_PRIVATE_TESTS") == "true" {
		t.Skip("SKIP_PRIVATE_TESTS=true: B-DOS 1.5t binary unavailable (Colin's non-redistributable artifact)")
	}
	bin := colinBdosBinPath()
	if bin == "" {
		t.Fatalf("B-DOS 1.5t binary absent (set $BDOS15T_BIN or place at ~/sam-archive/bdos/analysis/extracted/bdos15t-beta6.bin) and SKIP_PRIVATE_TESTS is not set — place the file or set SKIP_PRIVATE_TESTS=true (proprietary, not in repo)")
	}
	code, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("read bdos bin: %v", err)
	}
	mac := z80h.New() // flat config: section B = RAM page 2
	mac.Write(bdosOrgB, code)
	mac.AttachIO(sdPortStub{})
	return mac
}

// runColinDecode hand-fills the CSD buffer with csd, sets the alternate-AF
// card-type byte (cardType: 3 => v2/SDHC branch, else v1), enters Colin's real
// CSD-decode at &A736 (section-B &6736), runs it to its RET, and returns the
// 32-bit block count it computed (read back from &80BD-&80C0).
func runColinDecode(t *testing.T, mac *z80h.Machine, csd [16]byte, cardType uint8) (blocks uint32, res z80h.CallResult) {
	t.Helper()
	mac.Write(csdBuf, csd[:])
	// 6-byte prologue: `ld a,cardType; ex af,af'; jp csdDecodeB`.
	mac.Write(scratchPrologue, []byte{
		0x3E, cardType, // ld a,cardType
		0x08,                                                 // ex af,af'
		0xC3, byte(csdDecodeB & 0xFF), byte(csdDecodeB >> 8), // jp &6736
	})
	var err error
	res, err = mac.RunFrom(scratchPrologue, z80h.Entry{})
	if err != nil {
		t.Fatalf("run Colin CSD decode: %v", err)
	}
	// The CSD-decode at &A736 RETs the 32-bit block count in registers (HL = low
	// word, DE = high word) to its caller — the &40BD/&40BF memory store happens in
	// the HDINIT caller (&A350), which we do NOT run. So read the result from the
	// registers at RET, exactly as the caller would consume it.
	blocks = uint32(res.DE)<<16 | uint32(res.HL)
	return blocks, res
}

// runColinRecords runs Colin's REAL records math (the self-contained base/records
// core at &A45A) on the given 32-bit block count, and returns the base (&80C2) and
// BD_RECORDS (&80C4) it computes. The block count is passed in MAIN DE:HL (DE high,
// HL low) — &A45A's `push de; push hl; exx` moves it into the alt registers the
// divides use — and the run stops at &A4AA (the first size-print call,
// just past the BD_RECORDS store at &A4A7). The number-printer at &992A is
// neutered to a RET (it descends into ROM print, which this section-B-only model
// has no ROM for; it is pure UI and does not affect the arithmetic).
func runColinRecords(t *testing.T, mac *z80h.Machine, blocks uint32) (base, records uint32, res z80h.CallResult) {
	t.Helper()
	// Enter at &A45A with the 32-bit block count in MAIN DE:HL (DE high, HL low).
	var err error
	res, err = mac.RunFrom(recordsEntryB, z80h.Entry{
		HL:      uint16(blocks & 0xFFFF),
		DE:      uint16(blocks >> 16),
		StopPC:  recordsStopB,
		StepCap: 2_000_000,
	})
	if err != nil {
		t.Fatalf("run Colin records math: %v", err)
	}
	b := mac.Read(baseAddr, 2)
	base = uint32(b[0]) | uint32(b[1])<<8
	r := mac.Read(recordsAddr, 2)
	records = uint32(r[0]) | uint32(r[1])<<8
	return base, records, res
}

// ---------------------------------------------------------------------------
// Tests.
// ---------------------------------------------------------------------------

// TestCSDDecodeColinV2 runs Colin's real CSD-decode on a range of v2.0/SDHC cards
// and asserts the block count matches the Go reference (C_SIZE+1)<<10.
func TestCSDDecodeColinV2(t *testing.T) {
	mac := loadBdosInSectionB(t)

	cases := []struct {
		name  string
		cSize uint32
	}{
		// C_SIZE for a v2 card: blocks = (C_SIZE+1)*1024, capacity = blocks*512 B.
		{"8MB-min", 0x00000F},   // tiny: 16384 blocks = 8 MB (smallest v2 we test)
		{"~3.7GB", 0x001D59},    // 7,694,336 blocks
		{"~30GB", 0x00F3FF},     // 63,963,136 blocks
		{"64GB-Pete", 0x01E8FF}, // ~61 GiB: 128,188,416 blocks (records overflow 16-bit)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			csd := csdV2(tc.cSize)
			blocks, res := runColinDecode(t, mac, csd, 3) // cardType 3 => v2 branch
			if !res.Halted {
				t.Fatalf("decode did not RET cleanly (PC=&%04X)", res.PC)
			}
			want := refBlocksV2(tc.cSize)
			if blocks != want {
				t.Fatalf("C_SIZE=0x%X: Colin blocks=%d, Go reference=%d", tc.cSize, blocks, want)
			}
			wantBase, wantRecords := refRecords(blocks)
			_, fullRecords := refRecordsFull(blocks)
			gotBase, gotRecords, rres := runColinRecords(t, mac, blocks)
			if !rres.ReachedStop {
				t.Fatalf("records math did not reach the store (PC=&%04X, halted=%v)", rres.PC, rres.Halted)
			}
			if gotBase != wantBase || gotRecords != wantRecords {
				t.Fatalf("C_SIZE=0x%X blocks=%d: Colin base=%d records=%d, Go reference base=%d records=%d",
					tc.cSize, blocks, gotBase, gotRecords, wantBase, wantRecords)
			}
			note := "Colin==Go"
			if fullRecords > 0xFFFF {
				note = fmt.Sprintf("Colin==Go(16-bit); TRUE records=%d OVERFLOWS the 16-bit BD_RECORDS slot -> wraps to %d", fullRecords, gotRecords)
			}
			t.Logf("C_SIZE=0x%X  CSD[7..9]=%02x %02x %02x  blocks=%d (=%d GB)  base=%d  records=%d (%s)",
				tc.cSize, csd[7], csd[8], csd[9], blocks, blocks/2/1024/1024, gotBase, gotRecords, note)
		})
	}
}

// TestCSDDecodeColinV1 runs Colin's real CSD-decode on v1.0/SDSC cards.
func TestCSDDecodeColinV1(t *testing.T) {
	mac := loadBdosInSectionB(t)

	cases := []struct {
		name                        string
		readBlLen, cSize, cSizeMult uint32
	}{
		// A 2 GB SDSC: READ_BL_LEN=10 (1024 B), C_SIZE=4095, C_SIZE_MULT=7 ->
		// (4096) * 2^9 * 2^10 = 2,147,483,648 bytes = 4,194,304 blocks.
		{"2GB-SDSC", 10, 4095, 7},
		// A 1 GB-ish SDSC: READ_BL_LEN=10, C_SIZE=3823, C_SIZE_MULT=7.
		{"1GB-SDSC", 10, 3823, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			csd := csdV1(tc.readBlLen, tc.cSize, tc.cSizeMult)
			blocks, res := runColinDecode(t, mac, csd, 2) // cardType 2 (!=3) => v1 branch
			if !res.Halted {
				t.Fatalf("decode did not RET cleanly (PC=&%04X)", res.PC)
			}
			want := refBlocksV1(tc.readBlLen, tc.cSize, tc.cSizeMult)
			if blocks != want {
				t.Fatalf("v1 READ_BL_LEN=%d C_SIZE=%d MULT=%d: Colin blocks=%d, Go reference=%d",
					tc.readBlLen, tc.cSize, tc.cSizeMult, blocks, want)
			}
			wantBase, wantRecords := refRecords(blocks)
			gotBase, gotRecords, rres := runColinRecords(t, mac, blocks)
			if !rres.ReachedStop {
				t.Fatalf("records math did not reach the store (PC=&%04X, halted=%v)", rres.PC, rres.Halted)
			}
			if gotBase != wantBase || gotRecords != wantRecords {
				t.Fatalf("v1 C_SIZE=%d: Colin base=%d records=%d, Go reference base=%d records=%d",
					tc.cSize, gotBase, gotRecords, wantBase, wantRecords)
			}
			t.Logf("v1 BL=%d C_SIZE=%d MULT=%d  blocks=%d  base=%d  records=%d (Colin==Go)",
				tc.readBlLen, tc.cSize, tc.cSizeMult, blocks, gotBase, gotRecords)
		})
	}
}
