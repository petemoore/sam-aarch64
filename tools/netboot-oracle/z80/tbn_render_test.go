// tbn_render_test.go — oracle for the Z80 `.tbn` → source-text renderer
// (i365a).  It runs build/tbn_render_driver.bin in the koron-go/z80 emulator
// over an on-disk `.tbn` and asserts the streamed source text byte-matches the
// Go authority render.Emit.
//
// The renderer reuses build/disasm.bin (the src/disasm.asm disassembler) for
// instruction decode via paged_call, so both binaries are loaded: the driver
// into a section-C page (+ its section-D neighbour), disasm.bin into the top
// physical page (DISASM_PAGE).  The `.tbn` is staged across a contiguous run of
// IN pages (one page for a small fixture, ~23 for the full release), and
// render_run streams the rendered text out RENDER_SINK_PORT, which the harness
// captures (renderSink).  The S8 capstone (TestTbnRenderFullRelease) renders the
// full build/release-unstripped.tbn byte-for-byte.
package z80_test

import (
	"bytes"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
	render "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
	"github.com/petemoore/sam-aarch64/tools/sampage"
)

// renderSink captures the streamed source text: the driver emits each rendered
// byte with `out (RENDER_SINK_PORT), a`, and this IODevice accumulates every
// write to that port — the same shape the assembler harness uses to capture the
// printer channel.  The output (~417 KB for the release corpus) is never resident
// on the Z80 side, so this stream is the render's only observable output.
type renderSink struct {
	buf bytes.Buffer
}

// renderSinkPort mirrors RENDER_SINK_PORT in tbn_render.asm (the printer data
// port &E8).
const renderSinkPort = 0xE8

func (s *renderSink) In(port uint8) uint8 { return 0xFF }
func (s *renderSink) Out(port uint8, value uint8) {
	if port == renderSinkPort {
		s.buf.WriteByte(value)
	}
}

// stageRenderMachine loads the driver, disasm.bin, and the `.tbn` (staged across
// a contiguous run of IN pages) into a fresh machine, attaches the streaming
// sink, and returns both.  The multi-page IN staging is the S8 capstone: a small
// fixture occupies one IN page; the full release `.tbn` spans ~23 pages.
func stageRenderMachine(t *testing.T, tbn []byte) (*z80h.Machine, *renderSink) {
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
	copy(pager.RAM[tbnRenderDisasmPage][:], disasmBin) // disasm.bin in the top page

	// Stage the `.tbn` across the contiguous IN run (pages tbnRenderInPage..).
	// paged_call's page (tbnRenderInPage-1) is left empty — render_run installs
	// the trampoline body there at runtime.
	for off, page := 0, tbnRenderInPage; off < len(tbn); off, page = off+sampage.PageSize, page+1 {
		end := off + sampage.PageSize
		if end > len(tbn) {
			end = len(tbn)
		}
		if page >= sampage.NumPages {
			t.Fatalf("`.tbn` too large: %d bytes overflows the IN run at page %d", len(tbn), page)
		}
		copy(pager.RAM[page][:], tbn[off:end])
	}

	if err := mac.LoadSymbols(tbnRenderDriverMap); err != nil {
		t.Fatalf("LoadSymbols: %v", err)
	}

	sink := &renderSink{}
	mac.AttachIO(sink)
	return mac, sink
}

// renderTBNOnZ80 boots build/tbn_render_driver.bin over the staged `.tbn` and
// returns the streamed source text.  Shared by the slice tests.
func renderTBNOnZ80(t *testing.T, tbn []byte) []byte {
	t.Helper()
	mac, sink := stageRenderMachine(t, tbn)

	res, callErr := mac.CallEntry("render_run", z80h.Entry{StepCap: renderStepCap})
	if callErr != nil {
		t.Fatalf("render_run: %v", callErr)
	}
	if !res.Halted {
		t.Fatalf("render_run did not return cleanly (PC=&%04X)", res.PC)
	}
	return sink.buf.Bytes()
}

// renderStepCap bounds a render run.  The full-release render takes ~24.7M steps
// (decoding ~9000 instructions through disasm.bin and streaming ~417 KB), past
// the 5M default; this ceiling gives ~8x headroom yet still fails a genuine
// runaway fast.
const renderStepCap = 200_000_000

// assertRenderMatch compares the Z80 render output against the Go authority,
// printing the first byte divergence with a readable context window.  It never
// dumps the whole buffers (the release output is ~417 KB); on mismatch it reports
// the lengths, the first differing offset, and ~200 bytes of got vs want around
// it as Go-quoted strings (so newlines/tabs are visible).
func assertRenderMatch(t *testing.T, got, want []byte) {
	t.Helper()
	if bytes.Equal(got, want) {
		return
	}
	t.Errorf("render mismatch: Z80 produced %d bytes, host authority %d bytes", len(got), len(want))

	maxShow := len(got)
	if len(want) < maxShow {
		maxShow = len(want)
	}
	diff := -1
	for i := 0; i < maxShow; i++ {
		if got[i] != want[i] {
			diff = i
			break
		}
	}
	if diff < 0 {
		// One is a prefix of the other; the divergence is at the shorter length.
		diff = maxShow
	}
	const ctx = 100
	lo := diff - ctx
	if lo < 0 {
		lo = 0
	}
	clip := func(b []byte) []byte {
		hi := diff + ctx
		if hi > len(b) {
			hi = len(b)
		}
		if lo > len(b) {
			return nil
		}
		return b[lo:hi]
	}
	t.Errorf("first diff at byte %d", diff)
	t.Errorf("got [%d:]  = %q", lo, clip(got))
	t.Errorf("want[%d:]  = %q", lo, clip(want))
}

const (
	tbnRenderDriverBin = "../../../build/tbn_render_driver.bin"
	tbnRenderDriverMap = "../../../build/tbn_render_driver.map"
	tbnRenderDisasmBin = "../../../build/disasm.bin"

	// tbnRenderDriverPage is the physical page the driver's section C maps to
	// (HMPR low 5 bits); section D is the next page (driver data spills there for
	// the full corpus, with STAGING_BUF + stack relocated to the top of it).
	tbnRenderDriverPage = 3
	// tbnRenderInPage is the first physical page of the contiguous IN run; the
	// `.tbn` is staged from here upward (IN_FIRST_LMPR=&28 → page 8).  The page
	// below it (7) holds the runtime-installed paged_call body, out of the run.
	tbnRenderInPage = 8
	// tbnRenderDisasmPage is where build/disasm.bin is resident (DISASM_PAGE),
	// the top page above the IN run.
	tbnRenderDisasmPage = 31
)

// TestTbnRenderNop is slice 1's parity proof: a single-`nop` on-disk `.tbn`
// renders to exactly "  nop\n" on the Z80, byte-identical to render.Emit.
func TestTbnRenderNop(t *testing.T) {
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

	got := renderTBNOnZ80(t, tbn)
	assertRenderMatch(t, got, want)
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

// TestTbnRenderDirectives is slice 5's parity proof: a source exercising the
// common DIRECTIVE (0x04) record forms — `.arch`/`.cpu` (SYS_NAME operands),
// `.set` with constant and symbol-reference operands, `.org` (origin re-base),
// `.align` (padding that moves the PC so a post-align label flushes at the
// right offset), `.ascii`/`.asciz` (escaped strings + string PC sizing), and a
// compound-expression `.set` that drives the infix printExpr path. It must
// render byte-identically to render.Emit, proving the operand/expr printer and
// the directive PC accounting.
func TestTbnRenderDirectives(t *testing.T) {
	src := []byte("  .arch armv8-a\n" +
		"  .cpu cortex-a53\n" +
		"  .set WIDTH, 0x780\n" +
		"  .set HEIGHT, 1200\n" +
		"  .org 0x1000\n" +
		"_start:\n" +
		"  nop\n" +
		"  .align 4\n" +
		"aligned:\n" +
		"  ret\n" +
		"  .ascii \"hi\"\n" +
		"  .asciz \"world\"\n" +
		"  .set X, WIDTH+1\n")
	tbn := buildCompactTBN(t, src, "s5.s")

	want, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}

	got := renderTBNOnZ80(t, tbn)
	assertRenderMatch(t, got, want)
}

// TestTbnRenderDirectiveExprForms extends slice 5's coverage to the operand
// forms the primary fixture only lightly touches: writeEscapedString's escape
// branches (\t \n \" \\ \xNN, plus raw printables) and printExpr's
// asOperand parenthesisation across nested binary, unary NEG/NOT and shift
// operators. Each must render byte-identically to render.Emit.
func TestTbnRenderDirectiveExprForms(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		// \t \n \" \\ in a .ascii, control + high bytes as \xNN in a .asciz.
		{"escapes", "  .ascii \"a\\tb\\nc\\\"d\\\\e\"\n  .asciz \"\\x01\\x7f end\"\n"},
		// Nested binary needs the inner group parenthesised; NEG/NOT/shift each
		// wrap their non-atomic operand.
		{"nested", "  .set A, 5\n  .set Y, (A+1)*2\n  .set Z, -A\n  .set S, A<<2\n  .set N, ~A\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tbn := buildCompactTBN(t, []byte(c.src), c.name+".s")
			want, err := render.Emit(tbn)
			if err != nil {
				t.Fatalf("render.Emit: %v", err)
			}
			got := renderTBNOnZ80(t, tbn)
			assertRenderMatch(t, got, want)
		})
	}
}

// TestTbnRenderOverlayTargets is slice 6a's parity proof: a source whose
// branch/adr/adrp targets are symbolic produces INSN_RUN mode-1 (overlay)
// records, one patch per element across the target-text FoldSlot families —
// FoldBranch26 (b/bl), FoldBranch19 (b.cond/cbz), FoldBranch14 (tbz),
// FoldAdr and FoldAdrp. Each element decodes its base word via disasm.bin and
// splices the patch's symbol name back into the folded (last) operand. It must
// render byte-identically to render.Emit.
func TestTbnRenderOverlayTargets(t *testing.T) {
	src := []byte(".global _start\n" +
		"_start:\n" +
		"  b forward\n" +
		"  bl _start\n" +
		"back:\n" +
		"  b.ne back\n" +
		"  cbz x0, forward\n" +
		"  tbz x1, #3, back\n" +
		"forward:\n" +
		"  adr x2, _start\n" +
		"  adrp x3, forward\n" +
		"  ret\n")
	tbn := buildCompactTBN(t, src, "s6a.s")

	want, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}

	got := renderTBNOnZ80(t, tbn)
	assertRenderMatch(t, got, want)
}

// TestTbnRenderOverlayImmMem is slice 6b's parity proof: a source whose
// immediate/memory operands are symbolic produces INSN_RUN mode-1 (overlay)
// records across the immediate/memory FoldSlot families present in the release
// corpus — FoldMemImm12 (ldr/str `[base, #off]`), FoldAddSubImm12 (add #imm),
// FoldLitpool19 (ldr =expr), FoldMovkImm16 (explicit movz/movk, mnemonic
// recovered from the base word), FoldPairImm7 (ldp `[base, #off]`) and
// FoldMovzAuto (`mov Rd, #value` collapsed to movz). Each element decodes its
// base word via disasm.bin and splices the patch expression into the folded
// field (bracket-relative insert, zeroed-immediate replace, litpool `=`,
// mnemonic recovery, or first-operand `mov`). The movz/movk cases also drive
// the compound-infix printExpr path (the :abs_g*_nc: folds render as
// parenthesised expressions). It must render byte-identically to render.Emit.
func TestTbnRenderOverlayImmMem(t *testing.T) {
	src := []byte(".global _start\n" +
		"val: .quad 0\n" +
		"_start:\n" +
		"  ldr x0, [x1, #:lo12:val]\n" +
		"  str x2, [x3, #:lo12:val]\n" +
		"  add x4, x5, #:lo12:val\n" +
		"  ldr x6, =val\n" +
		"  movz x7, #:abs_g0_nc:val\n" +
		"  movk x7, #:abs_g1_nc:val, lsl #16\n" +
		"  ldp x8, x9, [x10, #:lo12:val]\n" +
		"  mov x11, val\n" +
		"  ret\n")
	tbn := buildCompactTBN(t, src, "s6b.s")

	want, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}

	got := renderTBNOnZ80(t, tbn)
	assertRenderMatch(t, got, want)
}

// TestTbnRenderCommentSidecar is slice 7's parity proof: a source with
// standalone `//` comments, a TRAILING comment on an instruction, a multi-line
// block comment, and blank-line runs produces a populated editor-region
// comment/blank-run sidecar. The renderer must interleave those rows by output
// PC — draining them before the header-table label flush at each byte-emitting
// boundary — reproducing render.Emit exactly. The tricky cases: a trailing
// comment (placement 1) appends to the still-open statement line as ` //…`, a
// blank run closes an open statement with its own `\n` before emitting the
// blanks, and a block comment's body splits on `\n` into one `//` line each.
func TestTbnRenderCommentSidecar(t *testing.T) {
	src := []byte("// licence header line 1\n" +
		"// licence header line 2\n" +
		"\n" +
		".global _start\n" +
		"\n" +
		"_start:\n" +
		"  nop                 // trailing on nop\n" +
		"  // standalone before ret\n" +
		"  ret\n" +
		"\n" +
		"/* a block\n" +
		"   comment */\n" +
		"\n" +
		"  .word 0x1234        // trailing on data\n")
	tbn := buildCompactTBN(t, src, "s7.s")

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

// TestTbnRenderFullRelease is the S8 capstone: render the ENTIRE
// build/release-unstripped.tbn (371 KB, ~23 IN pages) on the Z80 and assert the
// streamed source text is byte-for-byte identical to the Go authority
// render.Emit (~417 KB). It exercises every part of the streaming architecture at
// scale — the multi-page IN window (the reader walks all 23 pages), the streaming
// output sink (the 417 KB output is never resident), and the streamed comment/
// blank-run sidecar (7839 rows / 291 KB of bodies read in-place, one at a time) —
// plus the full-corpus resident tables (475 names, 282 labels, 172 locals). On
// mismatch assertRenderMatch dumps the first differing offset with context.
func TestTbnRenderFullRelease(t *testing.T) {
	const tbnPath = "../../../build/release-unstripped.tbn"
	tbn, err := os.ReadFile(tbnPath)
	if err != nil {
		t.Fatalf("read %s (run `make release-unstripped-tbn`): %v", tbnPath, err)
	}

	want, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}

	got := renderTBNOnZ80(t, tbn)
	assertRenderMatch(t, got, want)
}

// TestTbnRenderCapOverflowGuards proves the #858-review overflow guards fire.
// The header label/local tables are fixed-size resident buffers sized for the
// release corpus (RENDER_MAX_LABELS=320, RENDER_MAX_LOCALS=192 in tbn_render.asm);
// once an arbitrary user program supplies more rows than the cap, the store
// routine must fail loud (`jp fail` = di;halt) rather than silently overrun into
// the adjacent resident tables.  This drives each store routine directly at its
// cap boundary: one below the cap must still store and return cleanly, at the cap
// must trap.  These caps are duplicated here deliberately — if the asm cap moves
// without updating this test, the boundary case fails loud, flagging the drift.
func TestTbnRenderCapOverflowGuards(t *testing.T) {
	// A minimal `.tbn` is enough — we call the store routines directly, not
	// render_run, so the staged content is never walked.
	var rw format.RecordWriter
	rw.WriteInsnRun(0, []format.InsnElement{{BaseWord: 0xD503201F}}) // nop
	var buf bytes.Buffer
	if err := format.WriteFile(&buf, format.NewSymbolTable(), nil, nil, rw.Bytes(), nil, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tbn := buf.Bytes()

	cases := []struct {
		name     string
		routine  string
		countSym string
		cap      int // RENDER_MAX_* in tbn_render.asm
	}{
		{"labels", "render_store_label", "render_label_count", 320},
		{"locals", "render_store_local", "render_local_count", 192},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for _, sub := range []struct {
				label    string
				count    int
				wantTrap bool
			}{
				{"below-cap stores", tc.cap - 1, false},
				{"at-cap traps", tc.cap, true},
			} {
				sub := sub
				t.Run(sub.label, func(t *testing.T) {
					mac, _ := stageRenderMachine(t, tbn)
					if _, err := mac.CallEntry("render_reset_state", z80h.Entry{StepCap: 100_000}); err != nil {
						t.Fatalf("render_reset_state: %v", err)
					}
					countAddr, err := mac.Sym(tc.countSym)
					if err != nil {
						t.Fatalf("Sym(%s): %v", tc.countSym, err)
					}
					failAddr, err := mac.Sym("fail")
					if err != nil {
						t.Fatalf("Sym(fail): %v", err)
					}
					mac.WriteU16LE(countAddr, uint16(sub.count))

					res, err := mac.CallEntry(tc.routine, z80h.Entry{StepCap: 100_000})
					if err != nil {
						t.Fatalf("%s: %v", tc.routine, err)
					}
					// `fail` is `di; halt`; a trapped run stops with PC on the halt.
					trapped := res.PC == failAddr || res.PC == failAddr+1
					b := mac.Read(countAddr, 2)
					gotCount := int(b[0]) | int(b[1])<<8

					if sub.wantTrap {
						if !trapped {
							t.Errorf("count=%d (cap %d): did not trap (PC=&%04X, want fail=&%04X)",
								sub.count, tc.cap, res.PC, failAddr)
						}
						if gotCount != sub.count {
							t.Errorf("count=%d: guard must not store — count changed to %d", sub.count, gotCount)
						}
					} else {
						if trapped {
							t.Errorf("count=%d (cap %d): trapped at fail but should store", sub.count, tc.cap)
						}
						if gotCount != sub.count+1 {
							t.Errorf("count=%d: store did not increment count (got %d, want %d)",
								sub.count, gotCount, sub.count+1)
						}
					}
				})
			}
		})
	}
}
