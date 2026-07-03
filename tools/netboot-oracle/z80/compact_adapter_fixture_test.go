// compact_adapter_fixture_test.go — the authority-side fixture for the Z80
// compact encoder adapter boot self-test (item i48c-b8e).
//
// The Z80 side (src/compact_emit.asm + src/test_compact_adapter.asm, riding
// the enc-tests ovl12 payload) walks a serialized IR record stream and emits
// compact record-stream bytes: INST records become real INSN_RUN elements via
// compact_inst (bare literal words / base word + overlay patch), packed into
// mode-0/mode-1 frames exactly as assemble.Compact's emitInsnRunFrames packs
// them. The input IR bytes and the expected compact record bytes are BAKED
// into the page-11 enc_fix payload (src/test_encode_inst_payload.asm,
// cadapt_* block); this test re-derives both from the Go authority
// (serializeIR for the input wire bytes, assemble.Compact for the output) and
// asserts they equal the baked copies below — so any authority change (a
// packing-rule tweak, a wire-format change, an encoder fix) fails HERE and
// forces a re-bake of the asm payload in the same change.
//
// On mismatch the test dumps both byte arrays as pyz80 `defb` lines — the
// re-bake source for the asm payload block.
package z80_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
)

// adapterImmExpr builds an IMM_EXPR bytecode pushing one constant.
func adapterImmExpr(v int64) []byte {
	var ew format.ExprWriter
	ew.WriteImm(v)
	return ew.Bytes()
}

// buildCompactAdapterFixture constructs the mixed record stream the Z80
// adapter self-test walks. The mix exercises every arm of the adapter:
//
//   - a pass-through directive (.org 0x1000 — also sets the origin/PC);
//   - fully-literal instructions (nop / add) → bare mode-0 words;
//   - PC-dependent constant-target instructions (b / cbz) → overlay
//     elements (base word + Branch26/Branch19 patch);
//   - a constant-expression litpool load (ldr x1,=imm64) → the litpool
//     base + Litpool19 patch arm of compact_inst (no pool table needed —
//     classification and base are structural);
//   - comments both between and inside instruction/data runs (they must
//     span the open run, never flush it);
//   - same-directive constant data that merges (.word 1,2,3 + .word 4)
//     and a different-directive run that splits (.byte 0xaa,0xbb);
//   - a trailing instruction after the data (reopens an instruction run,
//     flushed by the end-of-input flushAll).
//
// The element sequence [lit, lit, ovl, lit, ovl, ovl, lit] makes
// emitInsnRunFrames produce mode0(2) + mode1(4, absorbing the 1-literal gap
// under litBreak) + mode0(1) — all three packing shapes.
func buildCompactAdapterFixture(t *testing.T) *format.File {
	t.Helper()

	mid := func(s string) uint16 {
		id, ok := format.MnemonicID(s)
		if !ok {
			t.Fatalf("unknown mnemonic %q", s)
		}
		return id
	}
	did := func(s string) byte {
		id, ok := format.DirectiveID(s)
		if !ok {
			t.Fatalf("unknown directive %q", s)
		}
		return id
	}

	inst := func(mnem string, n byte, build func(w *format.OperandWriter)) format.Record {
		var ow format.OperandWriter
		if build != nil {
			build(&ow)
		}
		return format.Record{Kind: format.KindInst, MnemonicID: mid(mnem), OperandCount: n, Operands: ow.Bytes()}
	}
	dir := func(name string, n byte, build func(w *format.OperandWriter)) format.Record {
		var ow format.OperandWriter
		build(&ow)
		return format.Record{Kind: format.KindDirective, DirectiveID: did(name), OperandCount: n, Operands: ow.Bytes()}
	}
	comment := func(placement byte, body string) format.Record {
		return format.Record{Kind: format.KindComment, Placement: placement, Body: []byte(body)}
	}

	return &format.File{Records: []format.Record{
		dir(".org", 1, func(w *format.OperandWriter) { w.WriteImmExpr(adapterImmExpr(0x1000)) }),
		comment(0, " head"),
		inst("nop", 0, nil),
		inst("add", 3, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegXSP, 0)
			w.WriteReg(format.OpRegXSP, 1)
			w.WriteImmExpr(adapterImmExpr(5))
		}),
		inst("b", 1, func(w *format.OperandWriter) { w.WriteImmExpr(adapterImmExpr(0x1020)) }),
		inst("nop", 0, nil),
		comment(1, " mid"), // inside the instruction run: must span, not flush
		inst("cbz", 2, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(adapterImmExpr(0x1018))
		}),
		inst("ldr", 2, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 1)
			w.WriteLitPool(8, adapterImmExpr(0x123456789abcdef0))
		}),
		inst("nop", 0, nil),
		dir(".word", 3, func(w *format.OperandWriter) {
			w.WriteImmExpr(adapterImmExpr(1))
			w.WriteImmExpr(adapterImmExpr(2))
			w.WriteImmExpr(adapterImmExpr(3))
		}),
		dir(".word", 1, func(w *format.OperandWriter) { w.WriteImmExpr(adapterImmExpr(4)) }),
		comment(0, " data"), // inside the data run: must span, not flush
		dir(".byte", 2, func(w *format.OperandWriter) {
			w.WriteImmExpr(adapterImmExpr(0xaa))
			w.WriteImmExpr(adapterImmExpr(0xbb))
		}),
		inst("nop", 0, nil),
	}}
}

// The baked copies of the Z80 payload's cadapt_ir / cadapt_expected blocks
// (src/test_encode_inst_payload.asm). Regenerate from the defb dump this test
// prints on mismatch.
var bakedCompactAdapterIR = []byte{
	0x04, 0x08, 0x00, 0x0c, 0x01, 0x05, 0x03, 0x00, 0x02, 0x00, 0x10, 0x05,
	0x06, 0x00, 0x00, 0x20, 0x68, 0x65, 0x61, 0x64, 0x01, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x01, 0x01, 0x00, 0x03, 0x09, 0x00, 0x03, 0x00, 0x03, 0x01,
	0x05, 0x02, 0x00, 0x01, 0x05, 0x01, 0x09, 0x00, 0x01, 0x06, 0x00, 0x05,
	0x03, 0x00, 0x02, 0x20, 0x10, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05,
	0x05, 0x00, 0x01, 0x20, 0x6d, 0x69, 0x64, 0x01, 0x14, 0x00, 0x02, 0x08,
	0x00, 0x01, 0x00, 0x05, 0x03, 0x00, 0x02, 0x18, 0x10, 0x01, 0x05, 0x00,
	0x02, 0x0f, 0x00, 0x01, 0x01, 0x0c, 0x08, 0x09, 0x00, 0x04, 0xf0, 0xde,
	0xbc, 0x9a, 0x78, 0x56, 0x34, 0x12, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x04, 0x11, 0x00, 0x04, 0x03, 0x05, 0x02, 0x00, 0x01, 0x01, 0x05, 0x02,
	0x00, 0x01, 0x02, 0x05, 0x02, 0x00, 0x01, 0x03, 0x04, 0x07, 0x00, 0x04,
	0x01, 0x05, 0x02, 0x00, 0x01, 0x04, 0x05, 0x06, 0x00, 0x00, 0x20, 0x64,
	0x61, 0x74, 0x61, 0x04, 0x0e, 0x00, 0x02, 0x02, 0x05, 0x03, 0x00, 0x02,
	0xaa, 0x00, 0x05, 0x03, 0x00, 0x02, 0xbb, 0x00, 0x01, 0x00, 0x00, 0x00,
	0x00, 0x00,
}

var bakedCompactAdapterExpected = []byte{
	0x04, 0x08, 0x00, 0x0c, 0x01, 0x05, 0x03, 0x00, 0x02, 0x00, 0x10, 0x09,
	0x09, 0x00, 0x00, 0x1f, 0x20, 0x03, 0xd5, 0x20, 0x14, 0x00, 0x91, 0x09,
	0x2a, 0x00, 0x01, 0x00, 0x00, 0x00, 0x14, 0x01, 0x01, 0x03, 0x02, 0x20,
	0x10, 0x1f, 0x20, 0x03, 0xd5, 0x00, 0x00, 0x00, 0x00, 0xb4, 0x01, 0x02,
	0x03, 0x02, 0x18, 0x10, 0x01, 0x00, 0x00, 0x58, 0x01, 0x0c, 0x09, 0x04,
	0xf0, 0xde, 0xbc, 0x9a, 0x78, 0x56, 0x34, 0x12, 0x09, 0x05, 0x00, 0x00,
	0x1f, 0x20, 0x03, 0xd5, 0x08, 0x11, 0x00, 0x04, 0x01, 0x00, 0x00, 0x00,
	0x02, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00,
	0x08, 0x03, 0x00, 0x02, 0xaa, 0xbb, 0x09, 0x05, 0x00, 0x00, 0x1f, 0x20,
	0x03, 0xd5,
}

func TestCompactAdapterFixture(t *testing.T) {
	f := buildCompactAdapterFixture(t)

	p1, err := assemble.Pass1(f)
	if err != nil {
		t.Fatalf("Pass1: %v", err)
	}
	recs, _, _, err := assemble.Compact(f, p1)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	ir := serializeIR(t, f)

	// Structural guard: the fixture must keep exercising all three INSN_RUN
	// packing shapes plus the LIT_DATA merge/split and the pass-through
	// directive. If a packing constant (litBreak, insnRunMaxPayload) or the
	// dispatch changes, this fails loudly with the new shape.
	type shape struct {
		kind  format.RecordKind
		mode  byte
		elems int
	}
	var got []shape
	rr := format.NewRecordReader(recs)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			t.Fatalf("record reader: %v", err)
		}
		s := shape{kind: rec.Kind}
		if rec.Kind == format.KindInsnRun {
			s.mode = rec.Mode
			s.elems = len(rec.Elements)
		}
		got = append(got, s)
	}
	want := []shape{
		{kind: format.KindDirective},                  // .org pass-through
		{kind: format.KindInsnRun, mode: 0, elems: 2}, // nop, add
		{kind: format.KindInsnRun, mode: 1, elems: 4}, // b*, nop, cbz*, ldr=*
		{kind: format.KindInsnRun, mode: 0, elems: 1}, // nop
		{kind: format.KindLitData},                    // .word ×4 merged
		{kind: format.KindLitData},                    // .byte ×2
		{kind: format.KindInsnRun, mode: 0, elems: 1}, // trailing nop
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("compact stream shape drifted:\n got %v\nwant %v", got, want)
	}

	irOK := bytes.Equal(ir, bakedCompactAdapterIR)
	expOK := bytes.Equal(recs, bakedCompactAdapterExpected)
	if !irOK {
		t.Errorf("serialized IR differs from the baked cadapt_ir block (%d B derived vs %d B baked) — re-bake BOTH this test's constant and src/test_encode_inst_payload.asm:\n%s",
			len(ir), len(bakedCompactAdapterIR), defbDump(ir))
	}
	if !expOK {
		t.Errorf("Compact records differ from the baked cadapt_expected block (%d B derived vs %d B baked) — re-bake BOTH this test's constant and src/test_encode_inst_payload.asm:\n%s",
			len(recs), len(bakedCompactAdapterExpected), defbDump(recs))
	}
}

// defbDump renders bytes as pyz80 defb lines (the payload bake format).
func defbDump(b []byte) string {
	var sb strings.Builder
	for i := 0; i < len(b); i += 12 {
		end := i + 12
		if end > len(b) {
			end = len(b)
		}
		sb.WriteString("                defb    ")
		for j, v := range b[i:end] {
			if j > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "&%02x", v)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
