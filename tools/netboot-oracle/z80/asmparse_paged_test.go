// asmparse_paged_test.go — b8d brick-1 smoke test (i48c-b8i): proves the paged
// parser arrangement end-to-end under the koron-go/z80 harness.
//
// The paged parser (build/asmparse_paged.bin, org &4000, ASMPARSE_PAGED_BUFS=1)
// lives in physical pages 8 and 9.  LMPR=&28 maps those to sections A and B:
//
//	Section A (&0000-&3FFF) = physical page 8: PARSE_RECS(&0000), LEX_SRC(&0800)
//	Section B (&4000-&7FFF) = physical page 9: code + LEX_TOKS + LEX_STRPOOL
//
// The driver (build/parse_paged_driver.bin, org &8000) is the test entry point.
// b8i_parse_paged saves boot LMPR+SP, moves SP to section D, switches LMPR to
// &28, calls parse_run (BC=src len), snapshots the results into section-C cells,
// and restores LMPR+SP before RET.
//
// TestAsmParsePagedB8i seeds both pages, drives b8i_parse_paged over a small
// known fixture (tests/core/sources/inst_reg_imm.s — one label + three
// instructions), and byte-matches the emitted PARSE_RECS stream against
// serializeIR(frontend.Translate(src)) — the same oracle the corpus test uses.
package z80_test

import (
	"bytes"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
)

const (
	pagedDriverBin   = "../../../build/parse_paged_driver.bin"
	pagedDriverMap   = "../../../build/parse_paged_driver.map"
	pagedParserBin   = "../../../build/asmparse_paged.bin"
	pagedFixtureSrc  = "../../../tests/core/sources/inst_reg_imm.s"
	pagedFixtureName = "inst_reg_imm.s"
)

// pagedLEXSRCOffset is the offset of LEX_SRC within physical page 8 (= &0800 -
// &0000).  It is an equ constant in asmlex.asm so it does not appear in the map;
// hardcoded here by the same drift-equals-silent-corruption contract as
// addrPARSE_RECS in asmparse_test.go.
const pagedLEXSRCOffset = 0x0800 // LEX_SRC: equ &0800 in asmlex.asm (ASMPARSE_PAGED_BUFS)

// TestAsmParsePagedB8i is the b8d brick-1 end-to-end smoke test.  It proves
// that the two-page parser image (pages 8+9) can be driven from a main-image
// driver via LMPR=&28 and that the emitted PARSE_RECS stream byte-matches the
// host authority over a small single-fixture corpus.
func TestAsmParsePagedB8i(t *testing.T) {
	// --- pre-conditions ---
	if _, err := os.Stat(pagedDriverBin); err != nil {
		t.Fatalf("parse-paged-driver binary not built (%s); run `make parse-paged-driver-z80`", pagedDriverBin)
	}
	if _, err := os.Stat(pagedParserBin); err != nil {
		t.Fatalf("asmparse-paged binary not built (%s); run `make asmparse-paged-z80`", pagedParserBin)
	}

	// --- load driver into the harness ---
	// z80h.Load uses the flat mapping (FlatLMPR=&21, FlatHMPR=&03), so the
	// driver bytes (org &8000) land in physical page 3 (section C).
	mac, err := z80h.Load(pagedDriverBin, pagedDriverMap)
	if err != nil {
		t.Fatalf("load parse-paged-driver: %v", err)
	}

	// --- seed physical pages 8+9 with the parser window image ---
	// The image is org'd at &4000 (section B origin).  When LMPR=&28:
	//   section A (&0000) = page 8, section B (&4000) = page 9.
	// Binary byte[0] corresponds to address &4000 in section B = page 9 offset 0.
	parserImage, err := os.ReadFile(pagedParserBin)
	if err != nil {
		t.Fatalf("read asmparse-paged binary: %v", err)
	}
	pager := mac.Pager()
	copy(pager.RAM[9][:], parserImage) // page 9 ← parser code + tables
	// page 8 large-buffer region starts at offset 0 (PARSE_RECS at &0000).
	// Source goes to offset pagedLEXSRCOffset (&0800 = LEX_SRC).

	// --- read fixture source ---
	src, err := os.ReadFile(pagedFixtureSrc)
	if err != nil {
		t.Fatalf("read fixture %s: %v", pagedFixtureSrc, err)
	}

	// --- host-side oracle ---
	f, fErr := frontend.Translate(src, pagedFixtureName)
	if fErr != nil {
		t.Fatalf("frontend.Translate(%s): %v", pagedFixtureName, fErr)
	}
	want := serializeIR(t, f)

	// --- seed source into page 8 at LEX_SRC ---
	copy(pager.RAM[8][pagedLEXSRCOffset:], src)

	// --- drive the paged parser ---
	_, callErr := mac.CallEntry("b8i_parse_paged", z80h.Entry{BC: uint16(len(src))})
	if callErr != nil {
		t.Fatalf("b8i_parse_paged: %v", callErr)
	}

	// --- assert LMPR was restored ---
	if pager.LMPR == 0x28 {
		t.Fatalf("LMPR still &28 after b8i_parse_paged: driver did not restore LMPR")
	}

	// --- read parse error flag from driver result cell (section C, HMPR-stable) ---
	parseErrAddr, symErr := mac.Sym("B8I_PARSE_ERR")
	if symErr != nil {
		t.Fatal(symErr)
	}
	if mac.Read(parseErrAddr, 1)[0] != 0 {
		t.Fatalf("B8I_PARSE_ERR non-zero: Z80 parser reported error on valid source")
	}

	// --- read record-stream byte length from B8I_RECLENGTH ---
	recLenAddr, recLenSymErr := mac.Sym("B8I_RECLENGTH")
	if recLenSymErr != nil {
		t.Fatal(recLenSymErr)
	}
	raw := mac.Read(recLenAddr, 2)
	recLen := int(raw[0]) | int(raw[1])<<8

	// --- read the emitted record stream from physical page 8, offset 0 (PARSE_RECS) ---
	got := make([]byte, recLen)
	copy(got, pager.RAM[8][0:recLen])

	// --- byte-match against the host oracle ---
	if !bytes.Equal(got, want) {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Errorf("PARSE_RECS bytes differ from serializeIR: %d B vs %d B, first divergence at offset %d (got % x..., want % x...)",
			len(got), len(want), i, tail(got, i, 8), tail(want, i, 8))
	}

	// --- record count check: B8I_RECCOUNT == len(f.Records) ---
	recCountAddr, rcErr := mac.Sym("B8I_RECCOUNT")
	if rcErr != nil {
		t.Fatal(rcErr)
	}
	rcRaw := mac.Read(recCountAddr, 2)
	gotRC := int(rcRaw[0]) | int(rcRaw[1])<<8
	if gotRC != len(f.Records) {
		t.Errorf("B8I_RECCOUNT = %d, want %d (len(f.Records))", gotRC, len(f.Records))
	}
}
