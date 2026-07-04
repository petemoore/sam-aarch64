// tbn_render_test.go — oracle for the Z80 `.tbn` → source-text renderer
// (i365a slice 1).  It runs build/tbn_render_driver.bin in the koron-go/z80
// emulator over a single-`nop` on-disk `.tbn` and asserts the rendered text
// byte-matches the Go authority render.Emit.
//
// The renderer reuses build/disasm.bin (the src/disasm.asm disassembler) for
// instruction decode via paged_call, so both binaries are loaded: the driver
// into a section-C page (+ its section-D neighbour), disasm.bin into physical
// page 15 (DISASM_PAGE).  The fixture `.tbn` is staged in the IN page and
// render_run walks it, writing text to render_out with its length in
// render_out_len (both section-C, read after the routine returns).
package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
	render "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
	"github.com/petemoore/sam-aarch64/tools/sampage"
)

const (
	tbnRenderDriverBin = "../../../build/tbn_render_driver.bin"
	tbnRenderDriverMap = "../../../build/tbn_render_driver.map"
	tbnRenderDisasmBin = "../../../build/disasm.bin"

	// tbnRenderDriverPage is the physical page the driver's section C maps to
	// (HMPR low 5 bits); section D is the next page.
	tbnRenderDriverPage = 3
	// tbnRenderInPage is the physical page the fixture `.tbn` is staged in
	// (LMPR_RENDER=&28 → section A = page 8, section B = page 9 = paged_call).
	tbnRenderInPage = 8
	// tbnRenderDisasmPage is where build/disasm.bin is resident (DISASM_PAGE).
	tbnRenderDisasmPage = 15
)

// TestTbnRenderNop is slice 1's parity proof: a single-`nop` on-disk `.tbn`
// renders to exactly "  nop\n" on the Z80, byte-identical to render.Emit.
func TestTbnRenderNop(t *testing.T) {
	for _, path := range []string{tbnRenderDriverBin, tbnRenderDriverMap, tbnRenderDisasmBin} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("required binary not built (%s); run `make tbn-render-driver-z80`", path)
		}
	}

	// Build the single-nop `.tbn` in-process (the on-disk overlay form).
	var rw format.RecordWriter
	rw.WriteInsnRun(0, []format.InsnElement{{BaseWord: 0xD503201F}}) // nop
	var buf bytes.Buffer
	if err := format.WriteFile(&buf, format.NewSymbolTable(), nil, nil, rw.Bytes(), nil, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tbn := buf.Bytes()

	// The Go authority's rendering is the expected output.
	want, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}

	driverBin, err := os.ReadFile(tbnRenderDriverBin)
	if err != nil {
		t.Fatalf("read driver: %v", err)
	}
	disasmBin, err := os.ReadFile(tbnRenderDisasmBin)
	if err != nil {
		t.Fatalf("read disasm: %v", err)
	}

	// Fresh machine: section C = driver page, section D = its neighbour.
	mac := z80h.New()
	pager := mac.Pager()
	pager.HMPR = tbnRenderDriverPage

	firstPage := driverBin
	var secondPage []byte
	if len(driverBin) > sampage.PageSize {
		firstPage = driverBin[:sampage.PageSize]
		secondPage = driverBin[sampage.PageSize:]
	}
	copy(pager.RAM[tbnRenderDriverPage][:], firstPage)
	if len(secondPage) > 0 {
		copy(pager.RAM[tbnRenderDriverPage+1][:], secondPage)
	}
	copy(pager.RAM[tbnRenderDisasmPage][:], disasmBin) // disasm.bin in page 15
	copy(pager.RAM[tbnRenderInPage][:], tbn)           // `.tbn` at offset 0 of the IN page

	if err := mac.LoadSymbols(tbnRenderDriverMap); err != nil {
		t.Fatalf("LoadSymbols: %v", err)
	}

	res, callErr := mac.CallEntry("render_run", z80h.Entry{})
	if callErr != nil {
		t.Fatalf("render_run: %v", callErr)
	}
	if !res.Halted {
		t.Fatalf("render_run did not return cleanly (PC=&%04X)", res.PC)
	}

	outLenAddr, err := mac.Sym("render_out_len")
	if err != nil {
		t.Fatalf("render_out_len symbol: %v", err)
	}
	gotLen := int(binary.LittleEndian.Uint16(mac.Read(outLenAddr, 2)))

	outAddr, err := mac.Sym("render_out")
	if err != nil {
		t.Fatalf("render_out symbol: %v", err)
	}
	got := mac.Read(outAddr, gotLen)

	if !bytes.Equal(got, want) {
		t.Errorf("render mismatch: Z80 %d bytes %X, host %d bytes %X", gotLen, got, len(want), want)
		// Emit context around the first byte divergence to aid debugging.
		maxShow := gotLen
		if len(want) < maxShow {
			maxShow = len(want)
		}
		for i := 0; i < maxShow; i++ {
			if got[i] != want[i] {
				lo := i - 8
				if lo < 0 {
					lo = 0
				}
				hi := i + 16
				if hi > maxShow {
					hi = maxShow
				}
				t.Errorf("first diff at byte %d: got[%d:%d]=%X want[%d:%d]=%X",
					i, lo, hi, got[lo:hi], lo, hi, want[lo:hi])
				break
			}
		}
	}
}
