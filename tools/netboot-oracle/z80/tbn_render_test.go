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
	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
	render "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
	"github.com/petemoore/sam-aarch64/tools/sampage"
)

// renderTBNOnZ80 boots build/tbn_render_driver.bin over the staged `.tbn` and
// returns the bytes render_run wrote to render_out.  Shared by the slice tests.
func renderTBNOnZ80(t *testing.T, tbn []byte) []byte {
	t.Helper()
	for _, path := range []string{tbnRenderDriverBin, tbnRenderDriverMap, tbnRenderDisasmBin} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("required binary not built (%s); run `make tbn-render-driver-z80`", path)
		}
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
	return mac.Read(outAddr, gotLen)
}

// assertRenderMatch compares the Z80 render output against the Go authority,
// printing the first byte divergence for debugging.
func assertRenderMatch(t *testing.T, got, want []byte) {
	t.Helper()
	if bytes.Equal(got, want) {
		return
	}
	t.Errorf("render mismatch: Z80 %d bytes %X, host %d bytes %X", len(got), got, len(want), want)
	maxShow := len(got)
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

// TestTbnRenderGlobalLabelRet is slice 2's parity proof.  A compact `.tbn`
// built from `.global _start` / `_start:` / `ret` exercises the editor-region
// name table + `.global` flags and the header-table label flush placed by PC:
// it must render to exactly "  .global _start\n_start:\n  ret\n", byte-identical
// to render.Emit.  Building via the frontend gives a realistic editor region
// and header tables (mirrors compact_ir_b8d_test.go's fixture pipeline).
func TestTbnRenderGlobalLabelRet(t *testing.T) {
	src := []byte(".global _start\n_start:\n  ret\n")
	f, err := frontend.Translate(src, "s2.s")
	if err != nil {
		t.Fatalf("frontend.Translate: %v", err)
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		t.Fatalf("assemble.Pass1: %v", err)
	}
	tbn, err := assemble.CompactTBNBytes(f, p1)
	if err != nil {
		t.Fatalf("assemble.CompactTBNBytes: %v", err)
	}

	// The Go authority's rendering is the expected output.
	want, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}

	got := renderTBNOnZ80(t, tbn)
	assertRenderMatch(t, got, want)
}

// buildCompactTBN builds a compact `.tbn` from source via the frontend +
// assembler (the same pipeline compact_ir_b8d_test.go uses), so the fixture
// carries a realistic editor region + header tables.
func buildCompactTBN(t *testing.T, src []byte, name string) []byte {
	t.Helper()
	f, err := frontend.Translate(src, name)
	if err != nil {
		t.Fatalf("frontend.Translate: %v", err)
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		t.Fatalf("assemble.Pass1: %v", err)
	}
	tbn, err := assemble.CompactTBNBytes(f, p1)
	if err != nil {
		t.Fatalf("assemble.CompactTBNBytes: %v", err)
	}
	return tbn
}

// TestTbnRenderMultiInsnLabel is slice 3's parity proof: a multi-instruction
// mode-0 INSN_RUN carrying operands (mov/add/sub/ret) plus an interior label
// (`loop:` at PC 8, a header-table label flushed mid-run). It must render
// byte-identically to render.Emit, exercising operand capture (TAB + operands
// via the disasm comm buffer), PC advance, and the mid-run label flush.
func TestTbnRenderMultiInsnLabel(t *testing.T) {
	src := []byte(".global _start\n_start:\n  mov x0, #1\n  add x0, x0, #2\nloop:\n  sub x0, x0, #1\n  ret\n")
	tbn := buildCompactTBN(t, src, "s3.s")

	want, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}

	got := renderTBNOnZ80(t, tbn)
	assertRenderMatch(t, got, want)
}

// TestTbnRenderLitData is slice 4's parity proof: a source of constant data
// directives (.byte/.hword/.word/.quad) produces LIT_DATA (0x08) records,
// which render as the directive name followed by comma-separated
// leading-zero-suppressed little-endian hex values. It must render
// byte-identically to render.Emit.
func TestTbnRenderLitData(t *testing.T) {
	// Values chosen to exercise every hex-formatter path: 0 (all-zero →
	// "0x0"), 0xf (single significant nibble), 0xff (both nibbles), the
	// wider widths, and high-bit-set values (0xdeadbeef, 0x1122334455667788).
	src := []byte("  .byte 0, 1, 0xf, 0xff\n  .hword 0x1234, 0x5678\n  .word 0xdeadbeef\n  .quad 0x1122334455667788\n")
	tbn := buildCompactTBN(t, src, "s4.s")

	want, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}

	got := renderTBNOnZ80(t, tbn)
	assertRenderMatch(t, got, want)
}
