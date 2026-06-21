package assemble

import (
	"fmt"
	"testing"

	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// overlay_classify_fixtures_test.go — the authority for the Z80
// overlay_classify / literal_word / compact_inst boot self-test (item
// i204 / i48c-b8e brick 4).
//
// Each fixture mirrors exactly one vector baked into
// src/test_overlay_classify.asm. The Z80 side runs the same routine on
// the same {mnemonic_id, operand bytes, pc} and asserts the same
// classification (slot / isLitpool / litWidth / rt) and, where the
// instruction is compact_inst-encodable without a symbol table, the same
// base word + is-literal flag. This test is the drift guard: if the Go
// authority (overlay.go / compact.go, CLAUDE.md §6) changes a result,
// this test logs the new value and the asm vector must be re-baked.
//
// Symbol-bearing fixtures use a PUSH_SYM bytecode whose value is never
// resolved: overlayClassify's slot selection is structural (exprContextDep
// inspects opcodes, not values) except for classifyMovImm, which evals a
// constant. So the slot-only fixtures need no resolved symbol table, and
// the compact_inst-encode fixtures all use constant PC-relative targets
// the encoder handles without symbols.

// symExpr builds an IMM_EXPR bytecode that is one PUSH_SYM (context
// dependent, never resolved — only its structure matters to
// overlayClassify's exprContextDep).
func symExpr(id uint16) []byte {
	var ew format.ExprWriter
	ew.WriteSym(id)
	return ew.Bytes()
}

// lo12Expr builds a :lo12: relocation expression over a symbol (context
// dependent), the shape `add Rd, Rn, :lo12:sym` uses.
func lo12Expr(id uint16) []byte {
	var ew format.ExprWriter
	ew.WriteSym(id)
	ew.WriteOp(format.OpRelLo12)
	return ew.Bytes()
}

type ovlFixture struct {
	name     string
	mnemonic string
	pc       int64
	opCount  byte
	build    func(w *format.OperandWriter)
	// encodable: true if compact_inst can encode the base word without a
	// resolved symbol table (constant / PC-relative-constant / litpool /
	// literal). When false, only overlayClassify's slot selection is
	// asserted on the Z80 side.
	encodable bool
}

func TestOverlayClassifyFixtures(t *testing.T) {
	st := format.NewSymbolTable()
	symID := st.Intern("sym")
	names := st.Names()

	mid := func(s string) uint16 {
		id, ok := format.MnemonicID(s)
		if !ok {
			t.Fatalf("unknown mnemonic %q", s)
		}
		return id
	}

	fixtures := []ovlFixture{
		// --- literal_word: fully-literal + PC-invariant -> bare word ---
		{"lit_nop", "nop", 0, 0, func(w *format.OperandWriter) {}, true},
		{"lit_add_x0_x1_5", "add", 0, 3, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegXSP, 0)
			w.WriteReg(format.OpRegXSP, 1)
			w.WriteImmExpr(immExpr(5))
		}, true},
		// b 0x1000 @pc=0x1000 — structurally literal but PC-variant ->
		// keep symbolic (overlay element).
		{"sym_b_const", "b", 0x1000, 1, func(w *format.OperandWriter) {
			w.WriteImmExpr(immExpr(0x1010))
		}, true},

		// --- generic form-table PC-dependent slots (constant target) ---
		{"b_branch26", "b", 0x1000, 1, func(w *format.OperandWriter) {
			w.WriteImmExpr(immExpr(0x1010))
		}, true},
		{"cbz_branch19", "cbz", 0x1000, 2, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(0x1008))
		}, true},
		{"adrp_slot", "adrp", 0x1000, 2, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(0x3000))
		}, true},
		{"adr_slot", "adr", 0x1000, 2, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(0x1004))
		}, true},

		// --- generic context-dependent slot (symbol expr, slot only) ---
		// add x0,x1,:lo12:sym -> FoldAddSubImm12 (context dep, not PC dep).
		{"add_lo12", "add", 0, 3, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegXSP, 0)
			w.WriteReg(format.OpRegXSP, 1)
			w.WriteImmExpr(lo12Expr(symID))
		}, false},

		// --- mem forms (classifyMem) ---
		// ldr x0,[x1,#sym] -> FoldMemImm12 (slot only; sym expr).
		{"ldr_memimm12", "ldr", 0, 2, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteMemBaseOff(format.MemBaseOff, 1, symExpr(symID))
		}, false},
		// stur x0,[x1,#sym] -> FoldMemImm9 (unscaled mnemonic 74).
		{"stur_memimm9", "stur", 0, 2, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteMemBaseOff(format.MemBaseOff, 1, symExpr(symID))
		}, false},
		// ldp x0,x1,[x2,#sym] -> FoldPairImm7.
		{"ldp_pairimm7", "ldp", 0, 3, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteMemBaseOff(format.MemBaseOff, 2, symExpr(symID))
		}, false},

		// --- mov-imm (classifyMovImm) ---
		// mov x0,#0x12340000 -> FoldMovzAuto (single 16-bit chunk).
		{"mov_movz", "mov", 0, 2, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(0x12340000))
		}, false},
		// mov x2,#0x5555... -> FoldLogical (bitmask immediate).
		{"mov_logical", "mov", 0, 2, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 2)
			w.WriteImmExpr(immExpr(0x5555555555555555))
		}, false},

		// --- ldr-literal (overlay.go:118) ---
		// ldr x0,sym -> FoldBranch19, encodable as PC-relative constant.
		{"ldr_lit_branch19", "ldr", 0x1000, 2, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(0x1008))
		}, true},

		// --- litpool (overlay.go:102) ---
		// ldr x0,=sym -> FoldLitpool19, isLitpool, width 8, rt 0.
		{"litpool", "ldr", 0, 2, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteLitPool(8, symExpr(symID))
		}, false},

		// --- loud gaps ---
		// lsl x0,x1,#sym -> special-form loud gap (no overlay slot).
		{"loud_lsl", "lsl", 0, 3, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(symExpr(symID))
		}, false},
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			var ow format.OperandWriter
			fx.build(&ow)
			ops := ow.Bytes()
			rec := instRec(mid(fx.mnemonic), fx.opCount, ops)
			f := fileFromRecords(names, []format.Record{rec})
			p1, err := Pass1(f)
			if err != nil {
				t.Fatalf("Pass1: %v", err)
			}

			hexops := ""
			for _, b := range ops {
				hexops += fmt.Sprintf("%02x ", b)
			}

			// compact_inst is the top-level entry: literalWord first, then
			// overlayClassify. Run it for every encodable fixture so the
			// literal-first cases (nop / add #5) report their bare word.
			litFlag := 0
			baseStr := "----"
			if fx.encodable {
				el, ierr := compactInst(rec, fx.pc, p1, f)
				if ierr != nil {
					t.Fatalf("compactInst: %v", ierr)
				}
				if len(el.Patches) == 0 {
					litFlag = 1
				}
				baseStr = fmt.Sprintf("%02x %02x %02x %02x",
					byte(el.BaseWord), byte(el.BaseWord>>8),
					byte(el.BaseWord>>16), byte(el.BaseWord>>24))
			}

			// overlayClassify slot selection (skipped for the literal-first
			// fixtures, which never reach overlayClassify).
			slot, _, isLit, litW, rt, cerr := overlayClassify(rec, fx.pc, p1, f)
			if cerr != nil {
				t.Logf("OVLFIX %-18s mnem=%-3d pc=0x%05x OVLGAP err=%q  compact_isLit=%d base=[%s]  ops=[ %s]",
					fx.name, mid(fx.mnemonic), fx.pc, cerr.Error(), litFlag, baseStr, hexops)
				return
			}

			t.Logf("OVLFIX %-18s mnem=%-3d pc=0x%05x slot=%-2d isLit=%d litW=%d rt=%d  compact_isLit=%d base=[%s]  ops=[ %s]",
				fx.name, mid(fx.mnemonic), fx.pc, slot,
				boolToInt(isLit), litW, rt, litFlag, baseStr, hexops)
			_ = enc.FoldBranch26
		})
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
