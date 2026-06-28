// asmparse_corpus_test.go — corpus-level proof of the Z80 parser front-end
// against the host authority (i48c-b8g).
//
// TestAsmParseCorpus drives parse_run (src/asmparse.asm) over the 93-fixture
// corpus (tests/{core,format,operands,symbols,paged}/sources/*.s) and asserts
// that the emitted PARSE_RECS byte range is byte-identical to serializeIR of
// the host frontend.Translate output — the first corpus-level proof of the Z80
// parser front-end.
//
// The asmparse.bin is built under ASMPARSE_CORPUS_BUFS=1 (alongside
// ASMPARSE_STANDALONE=1), which relocates the large buffers to low RAM:
//   - LEX_TOKS:  equ &0100 (1024-token capacity, 14336 B, ends &3900)
//   - PARSE_RECS: equ &3A00 (10752 B capacity, ends &6400)
// The two relocated addresses are hardcoded below (addrPARSE_RECS and
// addrLEXTOKS) per the "equ not in mapfile, drift = silent corruption"
// contract from pass1_ir_test.go:35-39.
//
// Buffer-capacity guards are host-side, computed from the fixture, and loud
// (i253): an oversize fixture not in parseKnownOversize is a t.Fatalf; a stale
// list entry is a t.Errorf at end of sweep.
package z80_test

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
)

// addrLEXTOKS is the relocated LEX_TOKS address under ASMPARSE_CORPUS_BUFS.
// LEX_TOKS is an `equ` constant in src/asmlex.asm, absent from the pyz80
// mapfile; a drift here vs. the .asm is a silent corruption.
const addrLEXTOKS uint16 = 0x0100

// Capacity constants matching the ASMPARSE_CORPUS_BUFS definitions in
// src/asmlex.asm and src/asmparse.asm. These are the limits the Z80 binary
// enforces; a fixture that exceeds one belongs in parseKnownOversize.
const (
	corpusLEXSRCCap   = 4096  // LEX_SRC capacity in bytes
	corpusLEXTOKSCap  = 1024  // LEX_TOKS capacity in tokens (× 14 B each)
	corpusSYMNamesCap = 2048  // SYM_NAMES capacity in bytes
	corpusRECSCap     = 10752 // PARSE_RECS capacity in bytes
)

// parseKnownOversize is the reviewed exclude-list of corpus fixtures that
// exceed one of the buffer caps above (name -> reason with observed size). A
// NEW oversize fixture absent from the list is a t.Fatalf; a stale entry is a
// t.Errorf after the sweep. See i253 (no-silent-skips).
var parseKnownOversize = map[string]string{
	"in_long_source.s": "source 16474 B exceeds LEX_SRC 4096 B cap and IR ~16484 B exceeds PARSE_RECS 10752 B cap (comment-heavy paged fixture)",
}

var parseOversizeSeen = map[string]bool{}

// TestAsmParseCorpus is the corpus-level proof of the Z80 parser: drives
// parse_run over all corpus fixtures and byte-matches the emitted PARSE_RECS
// against serializeIR(frontend.Translate(src)).
func TestAsmParseCorpus(t *testing.T) {
	mac := loadAsmparse(t)

	for _, dir := range []string{"core", "format", "operands", "symbols", "paged"} {
		srcDir := "../../../tests/" + dir + "/sources"
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			// A missing test directory is not an error (some tiers optional).
			continue
		}
		for _, e := range entries {
			if e.IsDir() || len(e.Name()) < 2 || e.Name()[len(e.Name())-2:] != ".s" {
				continue
			}
			name := e.Name()
			path := srcDir + "/" + name
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read %s: %v", path, readErr)
			}
			t.Run(dir+"/"+name, func(t *testing.T) {
				checkParseCorpusFixture(t, mac, name, src)
			})
		}
	}

	// Stale-entry guard: every exclude-list entry must have been encountered.
	for name := range parseKnownOversize {
		if !parseOversizeSeen[name] {
			t.Errorf("parseKnownOversize entry %q was never encountered — stale, prune it", name)
		}
	}
}

// checkParseCorpusFixture runs one fixture through the host front-end and the
// Z80 parser, asserts the emitted byte stream is identical, and enforces the
// capacity guards with the reviewed exclude-list.
func checkParseCorpusFixture(t *testing.T, mac *z80h.Machine, name string, src []byte) {
	t.Helper()

	// Host side: translate to symbolic IR.
	f, err := frontend.Translate(src, name)
	if err != nil {
		t.Fatalf("%s: frontend.Translate: %v", name, err)
	}
	want := serializeIR(t, f)

	// --- Capacity guards (host-side, computed, loud) ---
	// 1. Source length vs LEX_SRC.
	if len(src) > corpusLEXSRCCap {
		oversize(t, name, fmt.Sprintf("source %d B exceeds LEX_SRC %d B cap", len(src), corpusLEXSRCCap))
		return
	}
	// 2. IR wire length vs PARSE_RECS.
	if len(want) > corpusRECSCap {
		oversize(t, name, fmt.Sprintf("IR %d B exceeds PARSE_RECS %d B cap", len(want), corpusRECSCap))
		return
	}
	// 3. Token count vs LEX_TOKS.
	toks, tokOK := refLex(src)
	if !tokOK {
		t.Fatalf("%s: refLex reported a lexical error on corpus source", name)
	}
	if len(toks) > corpusLEXTOKSCap {
		oversize(t, name, fmt.Sprintf("token count %d exceeds LEX_TOKS %d-token cap", len(toks), corpusLEXTOKSCap))
		return
	}
	// 4. Symbol-table bytes vs SYM_NAMES (format: len:1,name[] per entry + 1-byte sentinel).
	symBytes := 1 // sentinel
	for _, n := range f.Names {
		symBytes += 1 + len(n)
	}
	if symBytes > corpusSYMNamesCap {
		oversize(t, name, fmt.Sprintf("symbol-table %d B exceeds SYM_NAMES %d B cap", symBytes, corpusSYMNamesCap))
		return
	}

	// --- Z80 side ---
	srcAddr, srcAddrErr := mac.Sym("LEX_SRC")
	if srcAddrErr != nil {
		t.Fatal(srcAddrErr)
	}
	mac.Write(srcAddr, src)
	res, callErr := mac.CallEntry("parse_run", z80h.Entry{BC: uint16(len(src))})
	if callErr != nil {
		t.Fatalf("%s: parse_run: %v", name, callErr)
	}
	errFlagAddr, errFlagAddrErr := mac.Sym("PARSE_ERR")
	if errFlagAddrErr != nil {
		t.Fatal(errFlagAddrErr)
	}
	if mac.Read(errFlagAddr, 1)[0] != 0 {
		t.Fatalf("%s: PARSE_ERR set by Z80 parser (Z80 parse failed on valid source)", name)
	}

	// Read the emitted record stream via PARSE_RECPTR (a defs label, in .map).
	ptrAddr, ptrErr := mac.Sym("PARSE_RECPTR")
	if ptrErr != nil {
		t.Fatal(ptrErr)
	}
	ptrBytes := mac.Read(ptrAddr, 2)
	writePtr := uint16(ptrBytes[0]) | uint16(ptrBytes[1])<<8
	length := int(writePtr) - int(addrPARSE_RECS)
	if length < 0 {
		t.Fatalf("%s: negative record length (PARSE_RECPTR=%#x, PARSE_RECS=%#x)", name, writePtr, addrPARSE_RECS)
	}
	var got []byte
	if length > 0 {
		got = mac.Read(addrPARSE_RECS, length)
	}

	// Direct byte comparison against serializeIR.
	if !bytes.Equal(got, want) {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Errorf("%s: PARSE_RECS bytes differ from serializeIR: %d B vs %d B, first divergence at offset %d (got % x..., want % x...)",
			name, len(got), len(want), i, tail(got, i, 8), tail(want, i, 8))
	}

	// Record count: Z80 PARSE_RECN must equal the number of records the host
	// front-end produced.
	if int(res.BC) != len(f.Records) {
		t.Errorf("%s: Z80 record count = %d, want %d", name, res.BC, len(f.Records))
	}
}

// oversize handles a fixture that exceeds a buffer capacity. If the fixture is
// in parseKnownOversize the test is logged and skipped (reviewed exclusion per
// i253); otherwise it is a t.Fatalf.
func oversize(t *testing.T, name, reason string) {
	t.Helper()
	if listed, ok := parseKnownOversize[name]; ok {
		parseOversizeSeen[name] = true
		t.Logf("%s: oversize — skipping (reviewed: %s; listed as: %s)", name, reason, listed)
		return
	}
	t.Fatalf("%s: %s — add to parseKnownOversize with explanation", name, reason)
}
