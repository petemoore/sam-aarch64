// fold_guard_test.go — proves the Z80 deferred-fold range guards fire.
//
// fold_mem_imm9 (signed imm9 in [-256,255]) and fold_pair_imm7 (scaled
// ldp/stp offset, multiple-of-scale and in [-64,63]) must FAIL with a tag
// on an out-of-range value rather than silently masking to the field
// width — mirroring overlay.go FoldMemImm9 (88-93) / FoldPairImm7
// (127-141).  We hand-build a malformed .tbn the Go front-end would never
// emit (it range-checks .set values up front) so the bad value reaches the
// Z80 fold, then assert the assembler reports the expected fail tag.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Build a malformed .tbn that carries an out-of-range deferred fold the
// Go front-end would never emit (it range-checks .set values), to prove
// the Z80 fold guards fire instead of silently masking.
//
//	.set OFF, <value>
//	<insn with a deferred FoldSlot patch referencing OFF>
func buildMalformedTbn(t *testing.T, value int64, baseWord uint32, foldSlot byte) []byte {
	t.Helper()
	st := format.NewSymbolTable()
	offID := st.Intern("OFF")

	var rw format.RecordWriter

	// .set OFF, value
	setID, ok := format.DirectiveID(".set")
	if !ok {
		t.Fatal("no .set directive id")
	}
	var symExpr format.ExprWriter
	symExpr.WriteSym(offID) // operand 1: the symbol name
	var valExpr format.ExprWriter
	valExpr.WriteImm(value) // operand 2: the value (widened as needed)
	var ow format.OperandWriter
	ow.WriteImmExpr(symExpr.Bytes())
	ow.WriteImmExpr(valExpr.Bytes())
	rw.WriteDirective(setID, 2, ow.Bytes())

	// deferred insn: base word + one patch (foldSlot, expr = push OFF)
	var patchExpr format.ExprWriter
	patchExpr.WriteSym(offID)
	rw.WriteInsnRun(1, []format.InsnElement{
		{BaseWord: baseWord, Patches: []format.InsnPatch{
			{Slot: foldSlot, Expr: patchExpr.Bytes()},
		}},
	})

	var buf bytes.Buffer
	if err := format.WriteFile(&buf, st, nil, nil, rw.Bytes(), nil, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return buf.Bytes()
}

func TestFoldGuardsFire(t *testing.T) {
	root := repoRoot(t)
	build := filepath.Join(root, "build")
	asm, _ := os.ReadFile(filepath.Join(build, "assembler-prod.bin"))
	enc, _ := os.ReadFile(filepath.Join(build, "enctab.enc"))

	const (
		sturX0X1 = 0xf8000020 // stur x0, [x1, #imm9] (imm9 zeroed)
		stpX0X1  = 0xa9000020 // stp x0, x1, [x1, #imm7] (imm7 zeroed; X-pair scale 8)
	)
	const (
		slotMemImm9  = 8
		slotPairImm7 = 11
	)

	cases := []struct {
		name     string
		value    int64
		baseWord uint32
		slot     byte
		wantTag  string // "" => should pass
	}{
		{"imm9 in-range 8", 8, sturX0X1, slotMemImm9, ""},
		{"imm9 in-range 255", 255, sturX0X1, slotMemImm9, ""},
		{"imm9 in-range -256", -256, sturX0X1, slotMemImm9, ""},
		{"imm9 in-range -1", -1, sturX0X1, slotMemImm9, ""},
		{"imm9 over 256", 256, sturX0X1, slotMemImm9, "FAILb1"},
		{"imm9 under -264", -264, sturX0X1, slotMemImm9, "FAILb1"},
		{"imm9 huge 70000", 70000, sturX0X1, slotMemImm9, "FAILb1"}, // fits i32 (M1 ok), imm9 OOR
		{"imm7 in-range 16", 16, stpX0X1, slotPairImm7, ""},
		{"imm7 in-range -512", -512, stpX0X1, slotPairImm7, ""}, // -512/8 = -64
		{"imm7 not-multiple 4", 4, stpX0X1, slotPairImm7, "FAILb2"},
		{"imm7 scaled-over 520", 520, stpX0X1, slotPairImm7, "FAILb2"}, // 520/8=65 > 63
		{"imm7 scaled-under -528", -528, stpX0X1, slotPairImm7, "FAILb2"},
	}

	for _, c := range cases {
		tbn := buildMalformedTbn(t, c.value, c.baseWord, c.slot)
		res := runProdComplete(t, asm, enc, tbn, 10*time.Second)
		if c.wantTag == "" {
			if !res.Passed {
				t.Errorf("%s: expected PASS, got printer=%q exit=%s", c.name, res.PrinterCapture, res.ExitReason)
			} else {
				t.Logf("%s: PASS as expected", c.name)
			}
			continue
		}
		if res.Passed {
			t.Errorf("%s: expected fail %s, but assembly PASSED (silent mask!)", c.name, c.wantTag)
			continue
		}
		if !strings.Contains(res.PrinterCapture, c.wantTag) {
			t.Errorf("%s: expected printer to contain %q, got %q", c.name, c.wantTag, res.PrinterCapture)
		} else {
			t.Logf("%s: %s as expected (printer=%q)", c.name, c.wantTag, res.PrinterCapture)
		}
	}
}
