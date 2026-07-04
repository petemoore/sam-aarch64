// compact_ir_b8d_test.go — b8d brick-3 corpus proof (i48c-b8d): the paged
// chain (parse → pass1 → compact walk, real encoder) runs on Z80 and the
// serialized compact v2 .tbn output byte-matches assemble.CompactTBNBytes.
//
// The chain driver (build/b8d_chain_paged_driver.bin, org &8000,
// CHAIN_PAGED_DRIVER=1 COMPACT_WALK_REAL_ENCODER=1) extends the b8j skeleton
// chain with:
//
//   - CEMIT_BUFS_EXTERNAL=1: CEMIT_ELEMS placed at &F580-&FF7F (2560 B) in
//     section D, freeing COMPACT_DATA_RUN/COMPACT_RECS into page-8 spare.
//   - The compact_walk_inst handler calls cemit_add_inst (real ENCTAB encoding)
//     for each INST record; cemit_flush_inst writes INSN_RUN frames.
//   - compact_serialize serializes the completed compact stream to a .tbn file
//     at B8D_SER_OUT (&2F00 in page 8, length in ser_out_len).
//
// Page arrangement (critical — different from the b8j flat Load):
//
//	The default flat Load() places the binary at &8000 into pages 3+4 (section
//	C+D with FlatHMPR=&03).  But the b8d encoder needs the ENCTAB data
//	pre-loaded in physical page 4 (section A when LMPR=&24 = LMPR_ENCTAB).
//	If the binary occupies page 4, the ENCTAB load corrupts or is corrupted.
//
//	Solution: set HMPR=5, putting section C = page 5 and section D = page 6,
//	then deposit binary into pages 5+6 directly.  Page 4 is free for ENCTAB.
//
// After b8d_chain_paged returns:
//
//   - .tbn bytes are in physical page 8 at &2F00, length in ser_out_len (a
//     word in section C of the driver binary at HMPR=5, readable via mac.Read
//     at the address carried by the "ser_out_len" map symbol).
//   - Page 8 is NOT accessible via mac.Read after the driver restores LMPR
//     (section A maps back to page 1); pager.RAM[8] is always the physical page.
//
// Screened corpus: the 83-fixture set (same dirs as the b8j test), minus the
// entries in b8dKnownSkip.  b8dKnownSkip is a superset of compactPagedKnownOversize
// (b8j limits; same buffers) plus fixtures that trigger encoding paths unsupported
// by the b8d standalone driver (mrs/msr/dc/tlbi).  See b8dKnownSkip comment.
package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"sort"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	"github.com/petemoore/sam-aarch64/tools/sampage"
	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

const (
	b8dChainDriverBin = "../../../build/b8d_chain_paged_driver.bin"
	b8dChainDriverMap = "../../../build/b8d_chain_paged_driver.map"
	b8dEnctabBin      = "../../../build/enctab.enc"

	// b8dSerOutOffset is the offset within physical page 8 where the serialized
	// .tbn bytes begin (B8D_SER_OUT: equ &2F00 in chain_paged_driver.asm).
	b8dSerOutOffset = 0x2F00

	// b8dSerOutCap is the maximum .tbn byte count the page-8 output window can
	// hold: B8D_SER_OUT (&2F00) to B8D_SER_OUT_END (&3FFF) inclusive = 4096 B.
	b8dSerOutCap = 4096

	// b8dBinaryPage is the physical page used for section C (the b8d binary
	// start) when HMPR=5.  Page 4 is left free for the ENCTAB.
	b8dBinaryPage = 5

	// b8dEnctabPage is the physical page for the ENCTAB.
	b8dEnctabPage = 4

	// b8dSysregDataPage is the physical page for the sysreg lookup tables
	// (sysreg_data.bin, assembled at org &8000, visible as section C when HMPR=13).
	b8dSysregDataPage = 13
)

const b8dSysregDataBin = "../../../build/sysreg_data.bin"

// b8dKnownSkip is the reviewed exclude-list for the b8d chain.  Each entry is a
// source filename → human-readable reason.  Entries are a superset of
// compactPagedKnownOversize (b8j limits; same buffers).
var b8dKnownSkip = map[string]string{
	// --- From compactPagedKnownOversize (same underlying capacity limits) ---
	"in_long_source.s":        "source 16474 B exceeds LEX_SRC 4096 B cap (paged parser limit)",
	"inst_adrp_highorigin.s":  "sidecar ~1643 B exceeds the 768-byte COMPACT_SIDECAR cap",
	"inst_bic_imm.s":          "sidecar ~973 B exceeds the 768-byte COMPACT_SIDECAR cap",
	"inst_expr_muldiv.s":      "sidecar ~1923 B exceeds the 768-byte COMPACT_SIDECAR cap",
	"inst_ldr_literal.s":      "sidecar ~860 B exceeds the 768-byte COMPACT_SIDECAR cap",
	"inst_logical_noncanon.s": "sidecar ~1343 B exceeds the 768-byte COMPACT_SIDECAR cap",
	"inst_long_emit.s":        "sidecar ~1110 B exceeds the 768-byte COMPACT_SIDECAR cap",
	"inst_mov_setconst.s":     "sidecar ~1599 B exceeds the 768-byte COMPACT_SIDECAR cap",
	"inst_out_over32k.s":      "sidecar ~1271 B exceeds the 768-byte COMPACT_SIDECAR cap",
	"inst_quad_addr.s":        "sidecar ~1380 B exceeds the 768-byte COMPACT_SIDECAR cap",
}

var b8dKnownSkipSeen = map[string]bool{}

// newB8DMachine creates a fresh machine for a b8d fixture run.  It:
//  1. Creates a fresh machine (sampage.New() → FlatLMPR=&21, FlatHMPR=&03).
//  2. Sets HMPR=b8dBinaryPage (5) so section C = page 5 and section D = page 6,
//     leaving page 4 free for the ENCTAB.
//  3. Deposits the b8d binary into pages 5+6.
//  4. Loads the symbol table from the .map file.
func newB8DMachine(t *testing.T, binData []byte) *z80h.Machine {
	t.Helper()
	mac := z80h.New()
	pager := mac.Pager()
	pager.HMPR = b8dBinaryPage // section C = page 5, section D = page 6
	// Deposit binary into page 5 (section C) and page 6 (section D).
	firstPage := binData
	var secondPage []byte
	if len(binData) > sampage.PageSize {
		firstPage = binData[:sampage.PageSize]
		secondPage = binData[sampage.PageSize:]
	}
	copy(pager.RAM[b8dBinaryPage][:], firstPage)
	if len(secondPage) > 0 {
		copy(pager.RAM[b8dBinaryPage+1][:], secondPage)
	}
	if err := mac.LoadSymbols(b8dChainDriverMap); err != nil {
		t.Fatalf("newB8DMachine: LoadSymbols: %v", err)
	}
	return mac
}

// seedB8DChainPaged prepares a machine for one fixture run by depositing the
// ENCTAB, parser image, sysreg data, and source bytes into their physical pages.
// Page 8 starts zeroed (fresh machine per fixture), so no explicit clear is needed.
func seedB8DChainPaged(pager *sampage.Mem, enctabData, parserImage, sysregData, src []byte) {
	copy(pager.RAM[b8dEnctabPage][:], enctabData)        // page 4: raw enctab.enc
	copy(pager.RAM[9][:], parserImage)                   // page 9: parser code
	copy(pager.RAM[b8dSysregDataPage][:], sysregData)    // page 13: sysreg_data.bin (org &8000)
	copy(pager.RAM[8][pagedLEXSRCOffset:], src)          // page 8 at &0800: source text
}

// checkB8DFixture is the per-source assertion for the b8d chain.
//
// Fixtures in b8dKnownSkip are excluded early (reason recorded, seen mark set).
// For remaining fixtures, a full set of capacity pre-flight checks runs; an
// unknown oversize is a t.Fatalf (same pattern as compactPagedOversize).  Passing
// fixtures have their Z80 output compared byte-for-byte against the host oracle.
func checkB8DFixture(t *testing.T, b8dBin, enctabData, parserImage, sysregData []byte, name string, src []byte) {
	t.Helper()

	// Early exclusion: skip reviewed entries (capacity limits and unsupported
	// encoding paths).
	if _, known := b8dKnownSkip[name]; known {
		b8dKnownSkipSeen[name] = true
		return
	}

	f, err := frontend.Translate(src, name)
	if err != nil {
		t.Fatalf("%s: Translate: %v", name, err)
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		t.Fatalf("%s: host Pass1: %v", name, err)
	}
	wantRecs, wantSidecar, wantGlobals, err := assemble.Compact(f, p1)
	if err != nil {
		t.Fatalf("%s: host Compact: %v", name, err)
	}

	// Pre-flight cap checks — same thresholds as the b8j paged test.
	// Any fixture that exceeds a cap but is NOT in b8dKnownSkip is a new gap:
	// add it to the skip list (with the cap+size) or shrink the fixture.
	if n := len(f.Records); n > compactRecPCRecords {
		t.Fatalf("%s: record count %d exceeds COMPACT_REC_PC cap %d — add to b8dKnownSkip", name, n, compactRecPCRecords)
	}
	if n := z80SidecarBytes(wantSidecar); n > compactSidecarCap {
		t.Fatalf("%s: sidecar %d B exceeds COMPACT_SIDECAR cap %d — add to b8dKnownSkip", name, n, compactSidecarCap)
	}
	if n := len(wantGlobals) * 2; n > compactGlobalsCap {
		t.Fatalf("%s: globals %d B exceeds COMPACT_GLOBALS cap %d — add to b8dKnownSkip", name, n, compactGlobalsCap)
	}
	if n := z80DataRunBytes(t, wantRecs); n > compactDataRunCap {
		t.Fatalf("%s: data run %d B exceeds COMPACT_DATA_RUN cap %d — add to b8dKnownSkip", name, n, compactDataRunCap)
	}
	if n := z80RecsBytes(t, wantRecs); n > compactRecsCap {
		t.Fatalf("%s: record stream %d B exceeds COMPACT_RECS cap %d — add to b8dKnownSkip", name, n, compactRecsCap)
	}
	wantLabels, wantLocals := deriveHeaderRows(f, p1)
	screenHeaderRowOffsets(t, name, wantLabels, wantLocals)
	if n := len(wantLabels); n > compactLabelRowsCap {
		t.Fatalf("%s: label rows %d exceeds COMPACT_LABELROWS cap %d — add to b8dKnownSkip", name, n, compactLabelRowsCap)
	}
	if n := len(wantLocals); n > compactLocalRowsCap {
		t.Fatalf("%s: local rows %d exceeds COMPACT_LOCALROWS cap %d — add to b8dKnownSkip", name, n, compactLocalRowsCap)
	}
	if n := len(src); n > corpusLEXSRCCap {
		t.Fatalf("%s: source %d B exceeds LEX_SRC cap %d — add to b8dKnownSkip", name, n, corpusLEXSRCCap)
	}

	// Compute the expected .tbn bytes via the host authority.
	wantTBN, err := assemble.CompactTBNBytes(f, p1)
	if err != nil {
		t.Fatalf("%s: host CompactTBNBytes: %v", name, err)
	}
	if n := len(wantTBN); n > b8dSerOutCap {
		t.Fatalf("%s: TBN output %d B exceeds B8D_SER_OUT cap %d — add to b8dKnownSkip", name, n, b8dSerOutCap)
	}

	// Run the b8d paged chain: fresh machine per fixture.
	mac := newB8DMachine(t, b8dBin)
	pager := mac.Pager()
	seedB8DChainPaged(pager, enctabData, parserImage, sysregData, src)

	res, callErr := mac.CallEntry("b8d_chain_paged", z80h.Entry{
		BC: uint16(len(src)),
	})
	if callErr != nil {
		t.Fatalf("%s: b8d_chain_paged: %v", name, callErr)
	}
	if !res.Halted {
		t.Fatalf("%s: b8d_chain_paged did not return cleanly (PC=&%04X)", name, res.PC)
	}
	failHaltAddr, _ := mac.Sym("fail_halt")
	if res.PC == failHaltAddr {
		tagAddr, _ := mac.Sym("p1ir_fail_tag")
		tag := mac.Read(tagAddr, 1)[0]
		t.Fatalf("%s: b8d_chain_paged hit the fail trap (tag &%02X)", name, tag)
	}

	// Read ser_out_len from the driver's section-C variable.
	serOutLenAddr, err := mac.Sym("ser_out_len")
	if err != nil {
		t.Fatalf("%s: ser_out_len symbol not found: %v", name, err)
	}
	gotLen := int(binary.LittleEndian.Uint16(mac.Read(serOutLenAddr, 2)))
	if gotLen < 0 || gotLen > b8dSerOutCap {
		t.Fatalf("%s: ser_out_len=%d is out of range (cap %d)", name, gotLen, b8dSerOutCap)
	}

	// Read the .tbn bytes from physical page 8 at B8D_SER_OUT.
	gotTBN := make([]byte, gotLen)
	copy(gotTBN, pager.RAM[8][b8dSerOutOffset:b8dSerOutOffset+gotLen])

	if !bytes.Equal(gotTBN, wantTBN) {
		t.Errorf("%s: .tbn mismatch: Z80 %d bytes, host %d bytes", name, gotLen, len(wantTBN))
		// Emit context around the first byte divergence to aid debugging.
		maxShow := gotLen
		if len(wantTBN) < maxShow {
			maxShow = len(wantTBN)
		}
		for i := 0; i < maxShow; i++ {
			if gotTBN[i] != wantTBN[i] {
				lo := i - 8
				if lo < 0 {
					lo = 0
				}
				hi := i + 16
				if hi > maxShow {
					hi = maxShow
				}
				t.Errorf("%s: first diff at byte %d: got[%d:%d]=%X want[%d:%d]=%X",
					name, i, lo, hi, gotTBN[lo:hi], lo, hi, wantTBN[lo:hi])
				break
			}
		}
	}
}

// loadB8DInputs reads the four shared binaries (driver, enctab, parser, sysreg)
// once and returns them.  Fatal if any is missing (i253).
func loadB8DInputs(t *testing.T) (b8dBin, enctabData, parserImage, sysregData []byte) {
	t.Helper()
	for _, path := range []string{b8dChainDriverBin, b8dEnctabBin, pagedParserBin, b8dSysregDataBin} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("required binary not built (%s); run `make b8d-chain-paged-driver-z80 enctab asmparse-paged-z80 sysreg-data`", path)
		}
	}
	var err error
	b8dBin, err = os.ReadFile(b8dChainDriverBin)
	if err != nil {
		t.Fatalf("read b8d binary: %v", err)
	}
	enctabData, err = os.ReadFile(b8dEnctabBin)
	if err != nil {
		t.Fatalf("read enctab: %v", err)
	}
	parserImage, err = os.ReadFile(pagedParserBin)
	if err != nil {
		t.Fatalf("read parser image: %v", err)
	}
	sysregData, err = os.ReadFile(b8dSysregDataBin)
	if err != nil {
		t.Fatalf("read sysreg data: %v", err)
	}
	return
}

// TestChainPagedB8dCorpus is the b8d brick-3 corpus proof: the paged chain
// (parse → pass1 → compact walk, real encoder) over the screened fixture set,
// asserting the serialized .tbn byte-matches assemble.CompactTBNBytes.
func TestChainPagedB8dCorpus(t *testing.T) {
	b8dBin, enctabData, parserImage, sysregData := loadB8DInputs(t)

	for _, dir := range []string{"core", "format", "operands", "symbols", "paged"} {
		srcDir := "../../../tests/" + dir + "/sources"
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && len(e.Name()) >= 2 && e.Name()[len(e.Name())-2:] == ".s" {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			name := name
			path := srcDir + "/" + name
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read %s: %v", path, readErr)
			}
			t.Run(dir+"/"+name, func(t *testing.T) {
				checkB8DFixture(t, b8dBin, enctabData, parserImage, sysregData, name, src)
			})
		}
	}

	// Stale-entry guard: every skip-list entry must have been encountered in the
	// corpus (i.e., a .s file with that name exists in one of the test dirs).
	// An entry not encountered means the fixture was renamed/deleted — prune it.
	for name := range b8dKnownSkip {
		if !b8dKnownSkipSeen[name] {
			t.Errorf("b8dKnownSkip entry %q was never encountered — stale, prune it", name)
		}
	}
}

// TestChainPagedB8dSmoke is a single-fixture fast smoke test: the same
// inst_reg_imm.s fixture the other paged tests use.
func TestChainPagedB8dSmoke(t *testing.T) {
	b8dBin, enctabData, parserImage, sysregData := loadB8DInputs(t)
	src, err := os.ReadFile(pagedFixtureSrc)
	if err != nil {
		t.Fatalf("read fixture %s: %v", pagedFixtureSrc, err)
	}
	checkB8DFixture(t, b8dBin, enctabData, parserImage, sysregData, pagedFixtureName, src)
}

// TestPatchLenEscapeFixtureTriggersEscape guards that tests/core/inst_patch_len_escape.s
// still produces a patch expression >= 15 bytes — i.e. it actually exercises the
// packed patch-header length-escape (expr_len nibble 15 -> real u8) that the b8d
// corpus test byte-compares against the Z80 emit path (compact_emit.asm
// cemit_ai_hdr_esc). Without this guard a future encoder change could silently
// shrink the expression below the escape threshold: TestChainPagedB8dCorpus would
// keep passing while quietly losing the only coverage of the emit escape branch
// (the exact "only proof is code review" gap i353 closes).
func TestPatchLenEscapeFixtureTriggersEscape(t *testing.T) {
	const fixture = "../../../tests/core/sources/inst_patch_len_escape.s"
	src, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read %s: %v", fixture, err)
	}
	f, err := frontend.Translate(src, "inst_patch_len_escape.s")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		t.Fatalf("Pass1: %v", err)
	}
	tbn, err := assemble.CompactTBNBytes(f, p1)
	if err != nil {
		t.Fatalf("CompactTBNBytes: %v", err)
	}
	rf, err := format.ReadFile(tbn)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	maxExpr := 0
	for _, r := range rf.Records {
		if r.Kind != format.KindInsnRun {
			continue
		}
		for _, el := range r.Elements {
			for _, p := range el.Patches {
				if len(p.Expr) > maxExpr {
					maxExpr = len(p.Expr)
				}
			}
		}
	}
	if maxExpr < 15 {
		t.Fatalf("longest patch expr is %d bytes (< 15): the fixture no longer triggers the "+
			"packed-header length-escape — adjust it (a longer symbol-difference expression) "+
			"so it keeps pinning the emit escape branch", maxExpr)
	}
}
