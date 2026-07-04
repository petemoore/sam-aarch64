// prep_chain_paged_test.go — i31b-b4b: the on-SAM preprocessor wired in front
// of the b8d assemble chain, proven end-to-end in the paged Z80 harness.
//
// The combined driver (build/prep_chain_paged_driver.bin, entry prep_chain_paged)
// runs prep_run in a THIRD paged window (the prep image in physical pages 10/11,
// LMPR=&2A) then hands the expanded text to b8d_chain_paged (parser window pages
// 8/9, LMPR=&28) — the b8d two-image pattern extended with the prep window, both
// composed into the driver via dual --importfile. See chain_paged_driver.asm's
// prep_chain_paged header for the memory model.
//
// Page arrangement (mirrors the b8d test, plus the prep window):
//
//	page 4     ENCTAB (enctab.enc)                 — LMPR=&24 during encode
//	pages 5/6  combined driver (section C/D)        — HMPR=5
//	pages 8/9  parser window (asmparse_paged.bin)   — LMPR=&28 (chain phase)
//	pages 10/11 prep window (asmprep_paged.bin)     — LMPR=&2A (prep phase)
//	page 13    sysreg lookup tables (sysreg_data.bin)
//
// Two gates per fixture:
//  1. expanded text (page-10 PREP_OUT, length PC_EXP_LEN) byte-equals
//     frontend.Preprocess;
//  2. the .tbn (page-8 B8D_SER_OUT, length ser_out_len) byte-equals
//     assemble.CompactTBNBytes over the PREPROCESSED source.
package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	"github.com/petemoore/sam-aarch64/tools/sampage"
	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
)

const (
	prepChainDriverBin = "../../../build/prep_chain_paged_driver.bin"
	prepChainDriverMap = "../../../build/prep_chain_paged_driver.map"
	prepPagedImageBin  = "../../../build/asmprep_paged.bin"
	prepPagedImageMap  = "../../../build/asmprep_paged.map"

	// The prep window's physical pages (LMPR=&2A): section A = prep buffers,
	// section B = prep code. See PC_PREP_LMPR in chain_paged_driver.asm.
	prepChainPrepBufPage   = 10 // section A: PREP_OUT/PREP_SRC/PREP_FILES
	prepChainPrepCodePage  = 11 // section B: prep code + PREP_PATH/PREP_NFILES/…

	// The expanded-text ceiling: the LEX_SRC window the chain reads from
	// (page-8 &0800..&0FFF). Matches PC_LEXSRC_CAP in the driver.
	prepChainLexSrcCap = 2048
)

// newPrepChainMachine deposits the combined driver into pages 5/6 (section C/D
// at HMPR=5, leaving page 4 free for ENCTAB), exactly like newB8DMachine but for
// the prep_chain driver binary + map.
func newPrepChainMachine(t *testing.T, driverBin []byte) *z80h.Machine {
	t.Helper()
	mac := z80h.New()
	pager := mac.Pager()
	pager.HMPR = b8dBinaryPage // section C = page 5, section D = page 6
	firstPage := driverBin
	var secondPage []byte
	if len(driverBin) > sampage.PageSize {
		firstPage = driverBin[:sampage.PageSize]
		secondPage = driverBin[sampage.PageSize:]
	}
	copy(pager.RAM[b8dBinaryPage][:], firstPage)
	if len(secondPage) > 0 {
		copy(pager.RAM[b8dBinaryPage+1][:], secondPage)
	}
	if err := mac.LoadSymbols(prepChainDriverMap); err != nil {
		t.Fatalf("newPrepChainMachine: LoadSymbols: %v", err)
	}
	return mac
}

// loadPrepChainInputs reads every binary the combined driver needs, up front
// (before any host-oracle t.Chdir). Fatal if any is missing (i253: no silent
// skip).
func loadPrepChainInputs(t *testing.T) (driverBin, parserImage, prepImage, enctabData, sysregData []byte) {
	t.Helper()
	paths := []string{prepChainDriverBin, pagedParserBin, prepPagedImageBin, b8dEnctabBin, b8dSysregDataBin}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("required binary not built (%s); run `make prep-chain-paged-driver-z80 asmparse-paged-z80 asmprep-paged-z80 enctab sysreg-data`", p)
		}
	}
	read := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		return b
	}
	return read(prepChainDriverBin), read(pagedParserBin), read(prepPagedImageBin), read(b8dEnctabBin), read(b8dSysregDataBin)
}

// prepChainFixture is one end-to-end case: raw preprocessor-bearing source plus
// the include files it references.
type prepChainFixture struct {
	name  string
	src   string
	path  string
	files incFiles
}

func checkPrepChainFixture(t *testing.T, in prepChainFixture,
	driverBin, parserImage, prepImage, enctabData, sysregData []byte,
	prepSyms map[string]uint16) {
	t.Helper()

	// --- Build the machine + load symbols BEFORE the host oracle's t.Chdir. ---
	mac := newPrepChainMachine(t, driverBin)
	pager := mac.Pager()

	// --- Host oracle (chdirs into a temp dir with the include files). ---
	wantExpanded, err := prepGoInc(t, []byte(in.src), in.path, in.files, nil)
	if err != nil {
		t.Fatalf("%s: host Preprocess: %v", in.name, err)
	}
	if len(wantExpanded) > prepChainLexSrcCap {
		t.Fatalf("%s: expanded text %d B exceeds the LEX_SRC cap %d — shrink the fixture",
			in.name, len(wantExpanded), prepChainLexSrcCap)
	}
	f, err := frontend.Translate(wantExpanded, in.path)
	if err != nil {
		t.Fatalf("%s: Translate(expanded): %v", in.name, err)
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		t.Fatalf("%s: host Pass1: %v", in.name, err)
	}
	wantTBN, err := assemble.CompactTBNBytes(f, p1)
	if err != nil {
		t.Fatalf("%s: host CompactTBNBytes: %v", in.name, err)
	}
	if len(wantTBN) > b8dSerOutCap {
		t.Fatalf("%s: .tbn %d B exceeds B8D_SER_OUT cap %d — shrink the fixture",
			in.name, len(wantTBN), b8dSerOutCap)
	}

	// --- Seed physical pages (in-memory; safe after chdir). ---
	copy(pager.RAM[b8dEnctabPage][:], enctabData)        // page 4: ENCTAB
	copy(pager.RAM[9][:], parserImage)                   // page 9: parser code (section B, LMPR=&28)
	copy(pager.RAM[prepChainPrepCodePage][:], prepImage) // page 11: prep code (section B, LMPR=&2A)
	copy(pager.RAM[b8dSysregDataPage][:], sysregData)    // page 13: sysreg tables

	// prep buffers live in page 10 (section A when LMPR=&2A) at the PREP_*
	// offsets; the section-B PREP_PATH/PREP_NFILES/PREP_NINCDIRS live in page 11.
	prepCodeOff := func(name string) int {
		a, ok := prepSyms[name]
		if !ok {
			t.Fatalf("%s: symbol %q not in prep map", in.name, name)
		}
		if a < 0x4000 {
			t.Fatalf("%s: symbol %q at &%04X is not section B", in.name, name, a)
		}
		return int(a) - 0x4000
	}
	copy(pager.RAM[prepChainPrepBufPage][pagedPREPSRCOffset:], []byte(in.src))               // page 10 @ PREP_SRC
	copy(pager.RAM[prepChainPrepCodePage][prepCodeOff("PREP_PATH"):], append([]byte(in.path), 0)) // page 11 @ PREP_PATH

	var ft []byte
	n := 0
	for name, content := range in.files {
		if len(name) > 255 {
			t.Fatalf("%s: include name too long: %q", in.name, name)
		}
		ft = append(ft, byte(len(name)))
		ft = append(ft, name...)
		ft = append(ft, byte(len(content)&0xff), byte(len(content)>>8))
		ft = append(ft, content...)
		n++
	}
	if pagedPREPFILESOffset+len(ft) > 0x4000 {
		t.Fatalf("%s: PREP_FILES table (%d B) overflows the page-10 window", in.name, len(ft))
	}
	copy(pager.RAM[prepChainPrepBufPage][pagedPREPFILESOffset:], ft) // page 10 @ PREP_FILES
	pager.RAM[prepChainPrepCodePage][prepCodeOff("PREP_NFILES")] = byte(n)
	pager.RAM[prepChainPrepCodePage][prepCodeOff("PREP_NINCDIRS")] = 0

	// --- Run the combined driver. ---
	res, callErr := mac.CallEntry("prep_chain_paged", z80h.Entry{BC: uint16(len(in.src))})
	if callErr != nil {
		t.Fatalf("%s: prep_chain_paged: %v", in.name, callErr)
	}
	if !res.Halted {
		t.Fatalf("%s: prep_chain_paged did not return cleanly (PC=&%04X)", in.name, res.PC)
	}
	failHaltAddr, _ := mac.Sym("fail_halt")
	if res.PC == failHaltAddr {
		tagAddr, _ := mac.Sym("p1ir_fail_tag")
		tag := mac.Read(tagAddr, 1)[0]
		t.Fatalf("%s: prep_chain_paged hit the fail trap (tag &%02X: &e0=prep err, &e1=over-cap, &f1=parse)",
			in.name, tag)
	}

	// --- Gate 1: expanded text (page-10 PREP_OUT, length PC_EXP_LEN). ---
	expLenAddr, err := mac.Sym("PC_EXP_LEN")
	if err != nil {
		t.Fatalf("%s: PC_EXP_LEN symbol not found: %v", in.name, err)
	}
	expLen := int(binary.LittleEndian.Uint16(mac.Read(expLenAddr, 2)))
	gotExpanded := make([]byte, expLen)
	copy(gotExpanded, pager.RAM[prepChainPrepBufPage][pagedPREPOUTOffset:pagedPREPOUTOffset+expLen])
	if !bytes.Equal(gotExpanded, wantExpanded) {
		t.Errorf("%s: expanded-text mismatch: Z80 %d B, host %d B\n Z80=%q\nhost=%q",
			in.name, expLen, len(wantExpanded), gotExpanded, wantExpanded)
	}

	// --- Gate 2: .tbn (page-8 B8D_SER_OUT, length ser_out_len). ---
	serLenAddr, err := mac.Sym("ser_out_len")
	if err != nil {
		t.Fatalf("%s: ser_out_len symbol not found: %v", in.name, err)
	}
	gotLen := int(binary.LittleEndian.Uint16(mac.Read(serLenAddr, 2)))
	if gotLen < 0 || gotLen > b8dSerOutCap {
		t.Fatalf("%s: ser_out_len=%d out of range (cap %d)", in.name, gotLen, b8dSerOutCap)
	}
	gotTBN := make([]byte, gotLen)
	copy(gotTBN, pager.RAM[8][b8dSerOutOffset:b8dSerOutOffset+gotLen])
	if !bytes.Equal(gotTBN, wantTBN) {
		t.Errorf("%s: .tbn mismatch: Z80 %d B, host %d B", in.name, gotLen, len(wantTBN))
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
				t.Errorf("%s: first .tbn diff at byte %d: got%X want%X", in.name, i, gotTBN[lo:hi], wantTBN[lo:hi])
				break
			}
		}
	}
}

// prepChainFixtures: small preprocessor-bearing sources that exercise the wired
// prep→chain path. Each expands to plain aarch64 instructions (plus cpp-style
// `# line` directives the lexer treats as comments), so the chain assembles them
// to a real .tbn byte-matching the host over the preprocessed text.
var prepChainFixtures = []prepChainFixture{
	{
		// Same-file macro expanding to instructions.
		name:  "macro-samefile",
		src:   ".macro mov_zero reg\n\tmov \\reg, #0\n.endm\n\tmov_zero x0\n\tmov_zero x1\n\tret\n",
		path:  "unit.s",
		files: incFiles{},
	},
	{
		// .set + .if guard: the guarded instruction is kept; the .set line passes
		// through to the assembler; .if/.endif are consumed.
		name:  "set-if-true",
		src:   ".set ENABLE, 1\n.if ENABLE\n\tadd x0, x0, #1\n.endif\n\tret\n",
		path:  "unit.s",
		files: incFiles{},
	},
	{
		// .if false: the guarded instruction is dropped.
		name:  "set-if-false",
		src:   ".set ENABLE, 0\n.if ENABLE\n\tadd x0, x0, #99\n.endif\n\tret\n",
		path:  "unit.s",
		files: incFiles{},
	},
	{
		// Cross-file macro: defined in an included file, invoked in main. This
		// exercises the real memory-reader include path AND the b4a def-file
		// provenance (the expansion `# line` directive names lib.s, not main.s).
		name: "include-cross-file-macro",
		src:  ".include \"lib.s\"\n\tadd_two x0\n\tret\n",
		path: "main.s",
		files: incFiles{
			"lib.s": ".macro add_two r\n\tadd \\r, \\r, #2\n.endm\n",
		},
	},
}

// TestChainPagedPrepWiring is the i31b-b4b end-to-end gate: source text → prep →
// b8d chain → .tbn, entirely on-Z80 over the paged arrangement, with the expanded
// text and the .tbn both byte-matching the host authority.
func TestChainPagedPrepWiring(t *testing.T) {
	driverBin, parserImage, prepImage, enctabData, sysregData := loadPrepChainInputs(t)
	prepSyms := pagedPrepSyms(t, prepPagedImageMap)
	for _, in := range prepChainFixtures {
		t.Run(in.name, func(t *testing.T) {
			checkPrepChainFixture(t, in, driverBin, parserImage, prepImage, enctabData, sysregData, prepSyms)
		})
	}
}
