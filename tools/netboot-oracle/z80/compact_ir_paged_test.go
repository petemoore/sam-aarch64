// compact_ir_paged_test.go — b8d brick-2 corpus proof (i48c-b8j): the paged
// chain (parse → pass1 → compact walk, skeleton INST arm) runs entirely on Z80
// over the paged arrangement and matches the b8b non-encoder outputs.
//
// The chain driver (build/chain_paged_driver.bin, org &8000, CHAIN_PAGED_DRIVER=1)
// includes the compact-core walk (which includes the pass1-ir walk) with two
// paged-context adjustments:
//
//	PASS1_IR_BUF: equ &0000 — aliases the PARSE_RECS page-8 window address
//	(section A when LMPR=&28 = page 8); the IR is read through the live window
//	instead of a flat defs buffer.
//
//	COMPACT_LABELROWS: equ &1200, COMPACT_LOCALROWS: equ &1500 — relocated into
//	page-8 spare (after PARSE_RECS &0000, LEX_SRC &0800, SYM_NAMES &1000);
//	the flat low-RAM addresses (&5000/&5400) fall inside the page-9 parser
//	window when LMPR=&28.
//
// After b8j_chain_paged returns:
//   - Compact sidecar/globals/recs + all count cells are in section D (page 4,
//     HMPR-stable) — readable via mac.Read at their usual &F000+ addresses.
//   - Header label/local rows are in physical page 8 at &1200/&1500 — NOT
//     accessible via mac.Read (section A = page 1 after LMPR restore); read
//     directly from pager.RAM[8].
//
// Screened corpus: the 83-fixture set that is the intersection of all source
// dirs minus parseKnownOversize and compactKnownOversize (defined in their
// respective test files).  The same reviewed exclude-list pattern applies:
// an unknown oversize fixture is a t.Fatalf; a stale entry is a t.Errorf.
// The comparisons are the four b8b non-encoder assertions: sidecar rows exact,
// globals exact, parseCompactView record view, and header label/local rows exact.
package z80_test

import (
	"encoding/binary"
	"os"
	"sort"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	"github.com/petemoore/sam-aarch64/tools/sampage"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
)

const (
	chainDriverBin = "../../../build/chain_paged_driver.bin"
	chainDriverMap = "../../../build/chain_paged_driver.map"
)

// Page-8 offsets for the paged label/local row buffers (COMPACT_LABELROWS and
// COMPACT_LOCALROWS, relocated from &5000/&5400 to page-8 spare by
// CHAIN_PAGED_DRIVER=1 in src/test_compact_ir.asm).  These are equ constants
// in the assembly; a drift here vs. the .asm is a silent corruption.
const (
	page8LabelRowsOffset = 0x1200 // COMPACT_LABELROWS: equ &1200
	page8LocalRowsOffset = 0x1500 // COMPACT_LOCALROWS: equ &1500
)

// loadChainPaged loads the chain paged driver binary and returns a fresh machine.
// Fails hard if the binary has not been built yet (i253: missing precondition
// must t.Fatal, never skip).
func loadChainPaged(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(chainDriverBin); err != nil {
		t.Fatalf("chain-paged-driver binary not built (%s); run `make chain-paged-driver-z80`", chainDriverBin)
	}
	mac, err := z80h.Load(chainDriverBin, chainDriverMap)
	if err != nil {
		t.Fatalf("load chain-paged-driver: %v", err)
	}
	return mac
}

// seedChainPaged prepares a freshly-loaded machine for one fixture run:
//   - seeds pages 8+9 with the parser window image (parserImage, the bytes of
//     build/asmparse_paged.bin which is org'd at &4000 = page 9 section B origin);
//   - writes src into page 8 at offset &0800 (LEX_SRC).
//
// page 8 is zeroed implicitly (sampage.New() zero-initialises all pages), so no
// explicit clear of the PARSE_RECS / SYM_NAMES / row-buffer region is needed
// between runs when a fresh machine is loaded per fixture.
func seedChainPaged(mac *z80h.Machine, parserImage, src []byte) {
	pager := mac.Pager()
	copy(pager.RAM[9][:], parserImage)           // page 9: parser code + LEX_TOKS + LEX_STRPOOL
	copy(pager.RAM[8][pagedLEXSRCOffset:], src)  // page 8 at &0800: source text (LEX_SRC)
}

// z80PagedHeaderRows reads the header label/local rows from physical page 8
// (offsets page8LabelRowsOffset and page8LocalRowsOffset) via pager.RAM[8].
// These are NOT accessible via mac.Read after the driver restores LMPR (section
// A maps back to page 1, not page 8); pager.RAM[8] is always the physical page.
// The count cells (compact_labelrows_count / compact_localrows_count) are in
// section D (page 4) and are read via mac.Read as usual.
func z80PagedHeaderRows(t *testing.T, mac *z80h.Machine, pager *sampage.Mem) ([]format.LabelRow, []format.LocalRow) {
	t.Helper()
	nLabels := int(binary.LittleEndian.Uint16(mac.Read(uint16(addrCompactLabelRowsCount), 2)))
	labels := make([]format.LabelRow, 0, nLabels)
	off := page8LabelRowsOffset
	for i := 0; i < nLabels; i++ {
		b := pager.RAM[8][off : off+6]
		labels = append(labels, format.LabelRow{
			NameID: binary.LittleEndian.Uint16(b),
			Offset: int64(binary.LittleEndian.Uint32(b[2:])),
		})
		off += 6
	}
	nLocals := int(binary.LittleEndian.Uint16(mac.Read(uint16(addrCompactLocalRowsCount), 2)))
	locals := make([]format.LocalRow, 0, nLocals)
	off = page8LocalRowsOffset
	for i := 0; i < nLocals; i++ {
		b := pager.RAM[8][off : off+5]
		locals = append(locals, format.LocalRow{
			Digit:  b[0],
			Offset: int64(binary.LittleEndian.Uint32(b[1:])),
		})
		off += 5
	}
	return labels, locals
}

// checkChainPagedFixture is the per-source assertion for the paged chain:
// Translate → host Pass1 → host Compact (expected), and Z80 b8j_chain_paged
// (got), compared on the four b8b non-encoder outputs: sidecar rows, globals,
// parseCompactView record view, and header label/local rows.
//
// Capacity guards match those of checkCompactFixture (same reviewed exclude-list,
// same hardware buffer sizes).  An unknown oversize fixture is a t.Fatalf; a
// stale entry is a t.Errorf at sweep end.
func checkChainPagedFixture(t *testing.T, parserImage []byte, name string, src []byte) {
	t.Helper()
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

	// Cap screens: same thresholds as checkCompactFixture — these reflect real
	// on-SAM buffer sizes, so an oversize fixture is a hardware limit, not a test
	// gap.  The paged arrangement does NOT resize any buffer (the plan confirmed:
	// max IR ~1000 B, toks 167, src ~1236 B over the 83 screened fixtures).
	if n := len(f.Records); n > compactRecPCRecords {
		if compactPagedOversize(t, name, "records", n, compactRecPCRecords, "COMPACT_REC_PC") {
			return
		}
	}
	if n := z80SidecarBytes(wantSidecar); n > compactSidecarCap {
		if compactPagedOversize(t, name, "sidecar bytes", n, compactSidecarCap, "COMPACT_SIDECAR") {
			return
		}
	}
	if n := len(wantGlobals) * 2; n > compactGlobalsCap {
		if compactPagedOversize(t, name, "globals bytes", n, compactGlobalsCap, "COMPACT_GLOBALS") {
			return
		}
	}
	if n := z80DataRunBytes(t, wantRecs); n > compactDataRunCap {
		if compactPagedOversize(t, name, "data run bytes", n, compactDataRunCap, "COMPACT_DATA_RUN") {
			return
		}
	}
	if n := z80RecsBytes(t, wantRecs); n > compactRecsCap {
		if compactPagedOversize(t, name, "record stream bytes", n, compactRecsCap, "COMPACT_RECS") {
			return
		}
	}
	wantLabels, wantLocals := deriveHeaderRows(f, p1)
	screenHeaderRowOffsets(t, name, wantLabels, wantLocals)
	if n := len(wantLabels); n > compactLabelRowsCap {
		if compactPagedOversize(t, name, "label rows", n, compactLabelRowsCap, "COMPACT_LABELROWS") {
			return
		}
	}
	if n := len(wantLocals); n > compactLocalRowsCap {
		if compactPagedOversize(t, name, "local rows", n, compactLocalRowsCap, "COMPACT_LOCALROWS") {
			return
		}
	}

	// Also screen against the paged parser caps (same as the b8i/corpus paged test).
	if n := len(src); n > corpusLEXSRCCap {
		if compactPagedOversize(t, name, "source bytes", n, corpusLEXSRCCap, "LEX_SRC(paged)") {
			return
		}
	}

	// Run the paged chain: fresh machine + seeded pages for each fixture.
	mac := loadChainPaged(t)
	seedChainPaged(mac, parserImage, src)

	res, callErr := mac.CallEntry("b8j_chain_paged", z80h.Entry{BC: uint16(len(src))})
	if callErr != nil {
		t.Fatalf("%s: b8j_chain_paged: %v", name, callErr)
	}
	if !res.Halted {
		t.Fatalf("%s: b8j_chain_paged did not return cleanly (PC=&%04X)", name, res.PC)
	}
	failHaltAddr, _ := mac.Sym("fail_halt")
	if res.PC == failHaltAddr {
		tagAddr, _ := mac.Sym("p1ir_fail_tag")
		tag := mac.Read(tagAddr, 1)[0]
		t.Fatalf("%s: b8j_chain_paged hit the fail trap (tag &%02X)", name, tag)
	}

	pager := mac.Pager()

	// 1) Sidecar rows — read via mac.Read (section D, HMPR-stable after LMPR restore).
	gotSidecar := z80Sidecar(t, mac)
	compareSidecar(t, name, gotSidecar, wantSidecar)

	// 2) Globals — read via mac.Read.
	gotGlobals := z80Globals(t, mac)
	if !uint16SlicesEqual(gotGlobals, wantGlobals) {
		t.Errorf("%s: globals = %v, host Compact = %v", name, gotGlobals, wantGlobals)
	}

	// 3) Record stream — non-encoder view: block sequence + LitData bytes +
	// pass-through directive bytes + instruction element counts.
	gotView := parseCompactView(t, z80Recs(t, mac))
	wantView := parseCompactView(t, wantRecs)
	compareView(t, name, gotView, wantView)

	// 4) Header label/local rows — read from pager.RAM[8] (relocated to &1200/&1500).
	gotLabels, gotLocals := z80PagedHeaderRows(t, mac, pager)
	if len(gotLabels) != len(wantLabels) {
		t.Errorf("%s: %d label rows, host headerRows %d", name, len(gotLabels), len(wantLabels))
	} else {
		for i := range wantLabels {
			if gotLabels[i] != wantLabels[i] {
				t.Errorf("%s: label row[%d] = {id %d, off %d}, host {id %d, off %d}",
					name, i, gotLabels[i].NameID, gotLabels[i].Offset, wantLabels[i].NameID, wantLabels[i].Offset)
			}
		}
	}
	if len(gotLocals) != len(wantLocals) {
		t.Errorf("%s: %d local rows, host headerRows %d", name, len(gotLocals), len(wantLocals))
	} else {
		for i := range wantLocals {
			if gotLocals[i] != wantLocals[i] {
				t.Errorf("%s: local row[%d] = {digit %d, off %d}, host {digit %d, off %d}",
					name, i, gotLocals[i].Digit, gotLocals[i].Offset, wantLocals[i].Digit, wantLocals[i].Offset)
			}
		}
	}
}

// compactPagedKnownOversize is the reviewed exclude-list for the paged chain.
// It is the union of parseKnownOversize (paged parser limits) and
// compactKnownOversize (compact-core buffer limits); the paged arrangement adds
// no new buffer constraints (the plan confirmed the PR-831 layout needs no
// resizing for the 83-fixture screened set).  Maintained separately from
// compactKnownOversize so stale-entry guards are independent.
var compactPagedKnownOversize = map[string]string{
	// From parseKnownOversize (LEX_SRC / PARSE_RECS too large):
	"in_long_source.s": "source 16474 B exceeds LEX_SRC 4096 B cap (paged parser limit)",
	// From compactKnownOversize (COMPACT_SIDECAR too large):
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

var compactPagedOversizeSeen = map[string]bool{}

// compactPagedOversize is the single decision point for a paged-chain fixture
// that exceeds one of the caps.  A REVIEWED entry in compactPagedKnownOversize
// is recorded as seen and the caller returns (documented limit, not a test gap).
// An unknown oversize fixture is a t.Fatalf.  Returns true iff the caller should
// return.
func compactPagedOversize(t *testing.T, name, what string, got, capBytes int, capName string) bool {
	t.Helper()
	if _, known := compactPagedKnownOversize[name]; known {
		compactPagedOversizeSeen[name] = true
		return true
	}
	t.Fatalf("%s: %s %d exceeds the %d-byte %s cap and is NOT in compactPagedKnownOversize — review and add it (with the cap+size) or shrink the fixture", name, what, got, capBytes, capName)
	return false
}

// TestChainPagedB8jCorpus is the b8d brick-2 corpus proof: the paged chain
// (parse → pass1 → compact walk, skeleton INST arm) over the 83-fixture
// screened set, asserting the b8b non-encoder outputs match the host authority.
func TestChainPagedB8jCorpus(t *testing.T) {
	// Pre-conditions: both the chain driver and the paged parser image must be built.
	if _, err := os.Stat(chainDriverBin); err != nil {
		t.Fatalf("chain-paged-driver binary not built (%s); run `make chain-paged-driver-z80`", chainDriverBin)
	}
	if _, err := os.Stat(pagedParserBin); err != nil {
		t.Fatalf("asmparse-paged binary not built (%s); run `make asmparse-paged-z80`", pagedParserBin)
	}

	parserImage, err := os.ReadFile(pagedParserBin)
	if err != nil {
		t.Fatalf("read asmparse-paged binary: %v", err)
	}

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
			name := name // capture for t.Run closure
			path := srcDir + "/" + name
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read %s: %v", path, readErr)
			}
			t.Run(dir+"/"+name, func(t *testing.T) {
				checkChainPagedFixture(t, parserImage, name, src)
			})
		}
	}

	// Stale-entry guard: every exclude-list entry must have been encountered.
	for name := range compactPagedKnownOversize {
		if !compactPagedOversizeSeen[name] {
			t.Errorf("compactPagedKnownOversize entry %q was never encountered — stale, prune it", name)
		}
	}
}

// TestChainPagedB8jSmoke is a single-fixture fast smoke test: the same
// inst_reg_imm.s fixture TestAsmParsePagedB8i uses, confirming the chain phases
// 0-2 work end-to-end on a small known input before the full corpus runs.
func TestChainPagedB8jSmoke(t *testing.T) {
	if _, err := os.Stat(chainDriverBin); err != nil {
		t.Fatalf("chain-paged-driver binary not built (%s); run `make chain-paged-driver-z80`", chainDriverBin)
	}
	if _, err := os.Stat(pagedParserBin); err != nil {
		t.Fatalf("asmparse-paged binary not built (%s); run `make asmparse-paged-z80`", pagedParserBin)
	}
	parserImage, err := os.ReadFile(pagedParserBin)
	if err != nil {
		t.Fatalf("read asmparse-paged binary: %v", err)
	}
	src, err := os.ReadFile(pagedFixtureSrc)
	if err != nil {
		t.Fatalf("read fixture %s: %v", pagedFixtureSrc, err)
	}
	checkChainPagedFixture(t, parserImage, pagedFixtureName, src)
}
