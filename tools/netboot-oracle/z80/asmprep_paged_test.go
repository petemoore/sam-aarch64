// asmprep_paged_test.go — i370: proves the on-SAM preprocessor runs in a
// two-page LMPR window end-to-end under the koron-go/z80 harness (the shared
// paged prep foundation i371's real-reader exercise and i31b-b4's chain wiring
// build on).
//
// The paged prep image (build/asmprep_paged.bin, org &4000, ASMPREP_PAGED_BUFS=1)
// lives in physical pages 8 and 9.  LMPR=&28 maps those to sections A and B:
//
//	Section A (&0000-&3FFF) = physical page 8: PREP_OUT(&0000), PREP_SRC(&2000),
//	                                           PREP_FILES(&3000)
//	Section B (&4000-&7FFF) = physical page 9: prep code + small buffers
//	                                           (SET_TAB.. + PREP_PATH/PREP_INCDIRS)
//
// The driver (build/prep_paged_driver.bin, org &8000) is the test entry point:
// prep_paged_run saves boot LMPR+SP, moves SP to section D, switches LMPR to &28,
// calls prep_run (BC=src len, default memory reader), snapshots PREP_ERR + the
// output byte count into section-C cells, and restores LMPR+SP before RET.
//
// For each fixture the paged prep_run output (PREP_OUT, physical page 8) is
// byte-compared against host frontend.Preprocess — the same oracle the flat
// asmprep suite uses.  Section C/D stay free throughout, which is the room
// i371's real reader needs for its trampoline-HLOAD.
package z80_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// Section-A window offsets within physical page 8 — the ASMPREP_PAGED_BUFS
// equates in src/asmprep.asm.  They are equ constants (not map labels), so they
// are hardcoded here under the same drift-equals-silent-corruption contract as
// pagedLEXSRCOffset in asmparse_paged_test.go: a change to the asmprep.asm
// window layout must update these three lines.
const (
	pagedPREPOUTOffset   = 0x0000 // PREP_OUT:   equ &0000 (page-8 window)
	pagedPREPSRCOffset   = 0x2000 // PREP_SRC:   equ &2000
	pagedPREPFILESOffset = 0x3000 // PREP_FILES: equ &3000
)

// pagedPrepSyms parses build/asmprep_paged.map (lines "HEXADDR=SYMBOL") into a
// symbol table.  The section-B buffers (PREP_PATH/PREP_NFILES/PREP_INCDIRS/…)
// are real defs labels that shift as asmprep grows, so reading them from the map
// keeps the test robust rather than hardcoding fragile page-9 offsets.
func pagedPrepSyms(t *testing.T, mapPath string) map[string]uint16 {
	t.Helper()
	data, err := os.ReadFile(mapPath)
	if err != nil {
		t.Fatalf("read paged prep map %s: %v (run `make asmprep-paged-z80`)", mapPath, err)
	}
	syms := map[string]uint16{}
	for _, line := range strings.Split(string(data), "\n") {
		kv := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(kv) != 2 {
			continue
		}
		v, perr := strconv.ParseUint(kv[0], 16, 32)
		if perr != nil {
			continue
		}
		syms[kv[1]] = uint16(v)
	}
	return syms
}

// runPagedPrep drives the paged prep image over src (+ memory-reader files) on a
// fresh machine and returns the expanded output.  Fresh per call so no prep
// state leaks between fixtures.
func runPagedPrep(t *testing.T, driverBin, driverMap, imageBin, imageMap string, src []byte, path string, files incFiles) (out []byte, errFlag bool) {
	t.Helper()

	mac, err := z80h.Load(driverBin, driverMap)
	if err != nil {
		t.Fatalf("load prep-paged-driver: %v", err)
	}
	image, err := os.ReadFile(imageBin)
	if err != nil {
		t.Fatalf("read asmprep-paged image: %v", err)
	}
	pager := mac.Pager()
	copy(pager.RAM[9][:], image) // page 9 ← prep code + small buffers (org &4000)

	syms := pagedPrepSyms(t, imageMap)
	// page9off returns the physical-page-9 offset of a section-B symbol.
	page9off := func(name string) int {
		a, ok := syms[name]
		if !ok {
			t.Fatalf("symbol %q not in paged prep map", name)
		}
		if a < 0x4000 {
			t.Fatalf("symbol %q at &%04X is not in section B (&4000-&7FFF)", name, a)
		}
		return int(a) - 0x4000
	}

	// --- plant source (section A, page 8 @ PREP_SRC) ---
	copy(pager.RAM[8][pagedPREPSRCOffset:], src)
	// --- plant PREP_PATH (section B, page 9), NUL-terminated ---
	copy(pager.RAM[9][page9off("PREP_PATH"):], append([]byte(path), 0))

	// --- plant the memory-reader include table (section A @ PREP_FILES) + count ---
	var ft []byte
	n := 0
	for name, content := range files {
		if len(name) > 255 {
			t.Fatalf("include name too long: %q", name)
		}
		ft = append(ft, byte(len(name)))
		ft = append(ft, name...)
		ft = append(ft, byte(len(content)&0xff), byte(len(content)>>8))
		ft = append(ft, content...)
		n++
	}
	if pagedPREPFILESOffset+len(ft) > 0x4000 {
		t.Fatalf("PREP_FILES table (%d B) overflows the page-8 window (16 KB)", len(ft))
	}
	copy(pager.RAM[8][pagedPREPFILESOffset:], ft)
	pager.RAM[9][page9off("PREP_NFILES")] = byte(n)
	pager.RAM[9][page9off("PREP_NINCDIRS")] = 0 // no -I dirs on the paged SAM v1

	// --- drive it ---
	if _, callErr := mac.CallEntry("prep_paged_run", z80h.Entry{BC: uint16(len(src))}); callErr != nil {
		t.Fatalf("prep_paged_run: %v", callErr)
	}
	if pager.LMPR == 0x28 {
		t.Fatalf("LMPR still &28 after prep_paged_run: driver did not restore LMPR")
	}

	errAddr, e := mac.Sym("PPD_ERR")
	if e != nil {
		t.Fatal(e)
	}
	errFlag = mac.Read(errAddr, 1)[0] != 0
	if errFlag {
		return nil, true
	}
	lenAddr, e := mac.Sym("PPD_OUTLEN")
	if e != nil {
		t.Fatal(e)
	}
	raw := mac.Read(lenAddr, 2)
	outLen := int(raw[0]) | int(raw[1])<<8
	out = make([]byte, outLen)
	copy(out, pager.RAM[8][pagedPREPOUTOffset:pagedPREPOUTOffset+outLen])
	return out, false
}

// TestAsmprepPaged proves prep runs paged with the memory reader, byte-matching
// host frontend.Preprocess across representative fixtures spanning bricks 1-3
// (.set/.if, macros, .include).
func TestAsmprepPaged(t *testing.T) {
	// Resolve binaries to absolute paths BEFORE any prepGoInc t.Chdir (which
	// would otherwise break these relative paths — the asmprep_test.go hazard).
	abs := func(p string) string {
		a, err := filepath.Abs(p)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	driverBin := abs("../../../build/prep_paged_driver.bin")
	driverMap := abs("../../../build/prep_paged_driver.map")
	imageBin := abs("../../../build/asmprep_paged.bin")
	imageMap := abs("../../../build/asmprep_paged.map")
	for _, p := range []string{driverBin, imageBin, imageMap} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s not built; run `make asmprep-paged-z80 prep-paged-driver-z80`", p)
		}
	}

	cases := []struct {
		name  string
		src   string
		path  string
		files incFiles
	}{
		{
			name: "set_if",
			path: "top.s",
			src:  ".set FOO 1\n.if FOO\nmov x0, x1\n.else\nnop\n.endif\nret\n",
		},
		{
			name: "macro",
			path: "top.s",
			src:  ".macro two a, b\n\\a\n\\b\n.endm\ntwo mov x0 x1, add x2 x3 x4\nret\n",
		},
		{
			name:  "include",
			path:  "top.s",
			src:   ".include \"inc.s\"\nret\n",
			files: incFiles{"inc.s": ".set BAR 2\nmov x0, x1\n"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Z80 side first (needs cwd for the abs paths already resolved).
			gotZ80, z80Err := runPagedPrep(t, driverBin, driverMap, imageBin, imageMap, []byte(tc.src), tc.path, tc.files)
			// Host oracle (prepGoInc chdir's into a temp dir with the include files).
			wantHost, hostErr := prepGoInc(t, []byte(tc.src), tc.path, tc.files, nil)
			if (hostErr != nil) != z80Err {
				t.Fatalf("error disagreement: host err=%v, Z80 errFlag=%v", hostErr, z80Err)
			}
			if hostErr != nil {
				return // both errored — agreement is enough
			}
			if !bytes.Equal(gotZ80, wantHost) {
				i := 0
				for i < len(gotZ80) && i < len(wantHost) && gotZ80[i] == wantHost[i] {
					i++
				}
				t.Errorf("paged prep_run output differs from host Preprocess: %d B vs %d B, first divergence at %d\n got:  %q\n want: %q",
					len(gotZ80), len(wantHost), i, gotZ80, wantHost)
			}
		})
	}
}
