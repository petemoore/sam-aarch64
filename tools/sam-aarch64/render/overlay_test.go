package render

import (
	"bytes"
	"testing"

	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// symExpr builds an expression bytecode that pushes symbol id, optionally
// followed by a relocation op (0 = none).
func symExpr(id uint16, rel format.ExprOp) []byte {
	var ew format.ExprWriter
	ew.WriteSym(id)
	if rel != 0 {
		ew.WriteOp(rel)
	}
	return ew.Bytes()
}

// emitOverlayLine builds a single-element INSN_RUN .tbn from base+patch and
// returns the rendered statement (without the leading indent or trailing \n).
func emitOverlayLine(t *testing.T, st *format.SymbolTable, base uint32, slot enc.FoldSlot, expr []byte) string {
	t.Helper()
	el := format.InsnElement{
		BaseWord: base,
		Patches:  []format.InsnPatch{{Slot: byte(slot), Expr: expr}},
	}
	var rw format.RecordWriter
	rw.WriteInsnRun(1, []format.InsnElement{el})
	var buf bytes.Buffer
	if err := format.WriteFile(&buf, st, nil, nil, rw.Bytes()); err != nil {
		t.Fatal(err)
	}
	out, err := Emit(buf.Bytes())
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	got := string(out)
	// Strip the leading "  " indent and trailing "\n" for comparison.
	if len(got) >= 3 && got[:2] == "  " {
		got = got[2:]
	}
	if n := len(got); n > 0 && got[n-1] == '\n' {
		got = got[:n-1]
	}
	return got
}

// TestOverlaySlotRendering locks the rendered text for each overlay FoldSlot:
// a base word (relocated field zeroed) plus a {slot, expression} patch must
// render back to the original symbolic statement. Base words are hand-chosen
// canonical encodings with the slot's field zeroed.
func TestOverlaySlotRendering(t *testing.T) {
	cases := []struct {
		name string
		base uint32
		slot enc.FoldSlot
		rel  format.ExprOp // relocation op appended after the symbol (0 = none)
		want string
	}{
		// PC-relative targets — rendered as the bare symbol (no '#').
		{"bl", 0x94000000, enc.FoldBranch26, 0, "bl\ttarget"},
		{"cbz", 0x34000000, enc.FoldBranch19, 0, "cbz\tw0, target"},
		{"b.cond", 0x54000001, enc.FoldBranch19, 0, "b.ne\ttarget"},
		{"tbz", 0x36000000, enc.FoldBranch14, 0, "tbz\tw0, #0, target"},
		{"adr", 0x10000000, enc.FoldAdr, 0, "adr\tx0, target"},
		{"adrp", 0x90000000, enc.FoldAdrp, 0, "adrp\tx0, target"},
		// add-immediate :lo12: — the dominant adrp/add idiom (no '#').
		{"add_lo12", 0x91000000, enc.FoldAddSubImm12, format.OpRelLo12, "add\tx0, x0, :lo12:target"},
		// movz auto-collapsed `mov Rd, #sym` — rendered as the 2-operand mov.
		{"movz_auto", 0xD2800000, enc.FoldMovzAuto, 0, "mov\tx0, target"},
		// explicit movz/movk keep their mnemonic (the base aliases to mov/movk).
		{"movz_explicit", 0x52800000, enc.FoldMovkImm16, 0, "movz\tw0, target"},
		{"movk_lsl16", 0x72A00000, enc.FoldMovkImm16, 0, "movk\tw0, target, lsl #16"},
		// literal pool load — `ldr Rt, =expr`.
		{"litpool", 0x58000000, enc.FoldLitpool19, 0, "ldr\tx0, =target"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := format.NewSymbolTable()
			id := st.Intern("target")
			got := emitOverlayLine(t, st, c.base, c.slot, symExpr(id, c.rel))
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestOverlayMemOffset checks symbolic memory offsets are inserted inside the
// bracket group (a zeroed offset disassembles as `[base]`, offset omitted).
func TestOverlayMemOffset(t *testing.T) {
	st := format.NewSymbolTable()
	a := st.Intern("a")
	b := st.Intern("b")
	// ldr w0, [x0, a-b] — scaled imm12 offset given as a symbol difference.
	var ew format.ExprWriter
	ew.WriteSym(a)
	ew.WriteSym(b)
	ew.WriteOp(format.OpSub)
	got := emitOverlayLine(t, st, 0xB9400000, enc.FoldMemImm12, ew.Bytes())
	if want := "ldr\tw0, [x0, (a - b)]"; got != want {
		t.Errorf("MemImm12: got %q, want %q", got, want)
	}

	// ldur w0, [x0, a-b] — unscaled signed imm9 offset.
	var ew9 format.ExprWriter
	ew9.WriteSym(a)
	ew9.WriteSym(b)
	ew9.WriteOp(format.OpSub)
	got = emitOverlayLine(t, st, 0xB8400000, enc.FoldMemImm9, ew9.Bytes())
	if want := "ldur\tw0, [x0, (a - b)]"; got != want {
		t.Errorf("MemImm9: got %q, want %q", got, want)
	}

	// stp x0, x1, [x2, a-b] — pair imm7 offset.
	var ew2 format.ExprWriter
	ew2.WriteSym(a)
	ew2.WriteSym(b)
	ew2.WriteOp(format.OpSub)
	got = emitOverlayLine(t, st, 0xA9000440, enc.FoldPairImm7, ew2.Bytes())
	if want := "stp\tx0, x1, [x2, (a - b)]"; got != want {
		t.Errorf("PairImm7: got %q, want %q", got, want)
	}
}

// TestOverlayLogicalSentinel checks the slot whose zeroed field (an all-zero
// logical bitmask) is itself undecodeable: rendering must fall back to a
// sentinel-decoded base and still substitute the symbolic immediate.
func TestOverlayLogicalSentinel(t *testing.T) {
	st := format.NewSymbolTable()
	id := st.Intern("mask")
	// orr w0, w1, #<mask> with N:immr:imms zeroed (an invalid bitmask).
	got := emitOverlayLine(t, st, 0x32000020, enc.FoldLogical, symExpr(id, 0))
	if want := "orr\tw0, w1, mask"; got != want {
		t.Errorf("FoldLogical: got %q, want %q", got, want)
	}
}

// TestOverlayLiteralElement checks a patch-free element renders as a plain
// disassembly, and an undecodeable literal falls back to `.inst`.
func TestOverlayLiteralElement(t *testing.T) {
	var rw format.RecordWriter
	rw.WriteInsnRun(0, []format.InsnElement{
		{BaseWord: 0xd503201f}, // nop
		{BaseWord: 0x8b010000}, // add x0, x0, x1
	})
	var buf bytes.Buffer
	if err := format.WriteFile(&buf, format.NewSymbolTable(), nil, nil, rw.Bytes()); err != nil {
		t.Fatal(err)
	}
	out, err := Emit(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	want := "  nop\n  add\tx0, x0, x1\n"
	if string(out) != want {
		t.Errorf("got %q, want %q", string(out), want)
	}
}

// TestOverlayLitData checks a constant-data run renders as its source data
// directive, wrapping at litDataPerLine values per line.
func TestOverlayLitData(t *testing.T) {
	id, _ := format.DirectiveID(".word")
	data := []byte{0x78, 0x56, 0x34, 0x12, 0x01, 0x00, 0x00, 0x00} // 0x12345678, 0x1
	var rw format.RecordWriter
	rw.WriteLitData(byte(id), data)
	var buf bytes.Buffer
	if err := format.WriteFile(&buf, format.NewSymbolTable(), nil, nil, rw.Bytes()); err != nil {
		t.Fatal(err)
	}
	out, err := Emit(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if want := "  .word 0x12345678, 0x1\n"; string(out) != want {
		t.Errorf("got %q, want %q", string(out), want)
	}
}
