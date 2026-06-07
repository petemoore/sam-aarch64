package render

// Overlay rendering: turn a compact v2 `.tbn` (M8 / i39a) back into text.
//
// A compact `.tbn` stores instructions as KindInsnRun records — each element
// is either a fully-literal assembled word or a base word (relocated field
// zeroed) plus a {FoldSlot, expression} overlay patch (see
// aarch64enc/overlay.go). To render an element back to editable source we
// decode the base word with aarch64dec and, for a patched element, splice the
// patch's symbolic operand back into the slot the fold zeroed.
//
// This is the inverse of refenc's encodeInstOverlay and the slot-enum fidelity
// guard for it: rendering then re-assembling a compact `.tbn` must reproduce
// the original bytes (tools/run-disasm-roundtrip.sh).
//
// The base word is decoded at pc=0. A literal element is PC-invariant by
// construction (refenc's two-probe guard in literalWord), and a patched
// element's PC-relative target is discarded — the symbolic operand replaces
// it — so the decode PC never affects the rendered text.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

	dec "github.com/petemoore/sam-aarch64/tools/aarch64dec"
	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// emitInsnRun renders a KindInsnRun record as one statement per element. Each
// element occupies 4 bytes; flush emits any header-table label/local whose PC
// falls on an element boundary (a label embedded mid-run — the case the header
// table exists to support).
func emitInsnRun(out *bytes.Buffer, f *format.File, rec format.Record, prevWasStatement *bool, pc *int64, flush func()) error {
	for _, el := range rec.Elements {
		flush()
		if *prevWasStatement {
			out.WriteByte('\n')
		}
		out.WriteString("  ")
		if err := emitInsnElement(out, f, el); err != nil {
			return err
		}
		*prevWasStatement = true
		*pc += 4
	}
	return nil
}

// emitInsnElement renders one INSN_RUN element (without the leading indent).
func emitInsnElement(out *bytes.Buffer, f *format.File, el format.InsnElement) error {
	if len(el.Patches) == 0 {
		out.WriteString(decodeLiteral(el.BaseWord))
		return nil
	}
	if len(el.Patches) != 1 {
		return fmt.Errorf("overlay element with %d patches unsupported", len(el.Patches))
	}
	line, err := renderOverlay(f, el.BaseWord, el.Patches[0])
	if err != nil {
		return err
	}
	out.WriteString(line)
	return nil
}

// decodeLiteral renders a fully-assembled word, falling back to `.inst` for
// words outside the decoder's coverage (preserving the exact bytes).
func decodeLiteral(word uint32) string {
	if mnem, ops, ok := dec.DecodeAt(0, word); ok {
		return dec.Format(mnem, ops)
	}
	return fmt.Sprintf(".inst\t%#08x", word)
}

// renderOverlay decodes a base word and splices the patch's symbolic operand
// into the slot the fold zeroed, producing the original source statement.
func renderOverlay(f *format.File, base uint32, patch format.InsnPatch) (string, error) {
	slot := enc.FoldSlot(patch.Slot)
	mnem, ops, ok := dec.DecodeAt(0, base)
	if !ok {
		// A few slots zero a field to an undecodeable pattern (an all-zero
		// logical immediate is reserved): fold a valid sentinel so the base
		// decodes to the right mnemonic + operand shape, then splice as usual.
		mnem, ops, ok = decodeWithSentinel(base, slot)
		if !ok {
			return "", fmt.Errorf("overlay: base word %#08x (slot %d) does not decode", base, patch.Slot)
		}
	}
	sym, err := renderPatchOperand(f, slot, patch.Expr)
	if err != nil {
		return "", err
	}
	return spliceOverlay(slot, base, mnem, ops, sym)
}

// spliceOverlay rebuilds the statement text for a slot, replacing or inserting
// the symbolic operand sym at the position the slot's field occupies. The
// register operands and mnemonic come from the decoded base word; only the
// relocated field differs from a plain disassembly.
func spliceOverlay(slot enc.FoldSlot, base uint32, mnem, ops, sym string) (string, error) {
	switch slot {
	case enc.FoldLitpool19:
		// ldr Rt, =expr — the decoded ldr-literal's first operand is Rt.
		parts := splitOps(ops)
		if len(parts) == 0 {
			return "", fmt.Errorf("overlay: litpool ldr has no register operand (%q)", ops)
		}
		return fmt.Sprintf("%s\t%s, =%s", mnem, parts[0], sym), nil

	case enc.FoldBranch26, enc.FoldBranch19, enc.FoldBranch14,
		enc.FoldAdr, enc.FoldAdrp, enc.FoldLogical:
		// The relocated field is the last operand — a branch/adr/adrp target,
		// or a logical (orr/and/eor) bitmask immediate. (renderPatchOperand
		// has already chosen target-style vs immediate-style for sym.)
		// Logical is here, not with the #0x0-matching immediates below,
		// because an all-zero bitmask is an invalid encoding: the base word
		// is decoded via a sentinel (decodeWithSentinel), so its immediate
		// does not render as `#0x0` — but it is always the last operand.
		return replaceOperand(mnem, ops, lastIndex(ops), sym)

	case enc.FoldMovzAuto:
		// `mov Rd, #value` auto-collapsed to movz: the assembler picks a hw
		// shift to encode the value, so the base disassembles as e.g.
		// `movz Rd, #0, lsl #16`. Render the original 2-operand `mov` form
		// with the full-value expression — re-assembly re-derives the shift.
		parts := splitOps(ops)
		if len(parts) == 0 {
			return "", fmt.Errorf("overlay: movz has no register operand (%q)", ops)
		}
		return fmt.Sprintf("mov\t%s, %s", parts[0], sym), nil

	case enc.FoldMovkImm16:
		// Explicit movz/movk: aarch64dec aliases a zeroed-imm movz (and a
		// movk whose shift folds to zero) to `mov`, which would lose the
		// shift-in-immediate convention on re-assembly. Recover the real
		// mnemonic from the move-wide opc field (bits 30:29: 0b10 movz,
		// 0b11 movk) and replace the zeroed immediate, keeping any explicit
		// `lsl #N` operand.
		m := "movz"
		if (base>>29)&3 == 3 {
			m = "movk"
		}
		return replaceImmOperand(m, ops, sym)

	case enc.FoldAddSubImm12:
		// Replace the zeroed immediate (the only operand disassembling as
		// `#0x0`); a sibling `lsl #12` shift (add/sub high-12) is a distinct
		// non-zero operand and is preserved.
		return replaceImmOperand(mnem, ops, sym)

	case enc.FoldMemImm12, enc.FoldMemImm9, enc.FoldPairImm7:
		// Memory offset: insert ", <sym>" inside the bracket group, since a
		// zeroed offset disassembles as `[base]` (offset omitted).
		return insertMemOffset(mnem, ops, sym)
	}
	return "", fmt.Errorf("overlay: no splice rule for slot %d", slot)
}

// splitOps splits a decoded operand string on the canonical ", " separator.
func splitOps(ops string) []string {
	if ops == "" {
		return nil
	}
	return strings.Split(ops, ", ")
}

func joinLine(mnem string, parts []string) string {
	if len(parts) == 0 {
		return mnem
	}
	return mnem + "\t" + strings.Join(parts, ", ")
}

func lastIndex(ops string) int { return len(splitOps(ops)) - 1 }

// replaceOperand replaces operand idx with sym and rejoins the statement.
func replaceOperand(mnem, ops string, idx int, sym string) (string, error) {
	parts := splitOps(ops)
	if idx < 0 || idx >= len(parts) {
		return "", fmt.Errorf("overlay: operand %d out of range in %q", idx, ops)
	}
	parts[idx] = sym
	return joinLine(mnem, parts), nil
}

// replaceImmOperand replaces the operand that disassembles as a zeroed
// immediate (`#0x0`) with sym. Exactly one field is zeroed in an overlay base
// word, so the zeroed immediate is unambiguous — any `lsl #N` shift operand is
// non-zero and left intact.
func replaceImmOperand(mnem, ops, sym string) (string, error) {
	parts := splitOps(ops)
	for i, p := range parts {
		if p == "#0x0" || p == "#0" {
			parts[i] = sym
			return joinLine(mnem, parts), nil
		}
	}
	return "", fmt.Errorf("overlay: no zeroed immediate operand in %q", ops)
}

// insertMemOffset inserts ", <sym>" before the closing bracket of a memory
// operand: `ldr w0, [x1]` → `ldr w0, [x1, <sym>]`. A pre-index `[x1]!` keeps
// its `!` (the insert lands before the bracket, not the bang).
func insertMemOffset(mnem, ops, sym string) (string, error) {
	i := strings.LastIndex(ops, "]")
	if i < 0 {
		return "", fmt.Errorf("overlay: no ']' in memory operand %q", ops)
	}
	return mnem + "\t" + ops[:i] + ", " + sym + ops[i:], nil
}

// decodeWithSentinel folds a non-zero sentinel into the slot's field so a base
// word whose zeroed field is itself undecodeable still decodes to the right
// mnemonic + operand shape. The decoded literal value is discarded (the caller
// splices the symbolic operand over it).
func decodeWithSentinel(base uint32, slot enc.FoldSlot) (string, string, bool) {
	bits, err := enc.Fold(slot, 1, 0, base)
	if err != nil {
		return "", "", false
	}
	return dec.DecodeAt(0, base|bits)
}

// renderPatchOperand renders a patch's expression in the textual style its
// slot's source operand uses: branch/adr/adrp targets carry no leading '#',
// litpool loads render `=expr`, and immediates use the standard immediate
// spelling (# for bare constants, none for symbol references).
func renderPatchOperand(f *format.File, slot enc.FoldSlot, expr []byte) (string, error) {
	switch slot {
	case enc.FoldBranch26, enc.FoldBranch19, enc.FoldBranch14,
		enc.FoldAdr, enc.FoldAdrp:
		return renderTargetText(f, expr)
	case enc.FoldLitpool19:
		// `=expr`: the value follows the `=` with no leading '#'.
		return renderImmText(f, expr, true)
	default:
		return renderImmText(f, expr, false)
	}
}

// renderImmText renders an expression as an instruction/directive immediate,
// reusing the same formatting the symbolic (KindInst) path uses.
func renderImmText(f *format.File, expr []byte, isDirective bool) (string, error) {
	var b bytes.Buffer
	if err := emitExprAsImmediateWithContext(&b, f, expr, isDirective); err != nil {
		return "", err
	}
	return b.String(), nil
}

// renderTargetText renders an expression as a branch/adr/adrp target: a bare
// address (no '#') for a constant, the symbol/local name for a symbol
// reference, or a parenthesised infix expression otherwise.
func renderTargetText(f *format.File, expr []byte) (string, error) {
	if v, ok := format.EvalConst(expr); ok {
		if v < 0 {
			return fmt.Sprintf("%d", v), nil
		}
		return fmt.Sprintf("0x%x", v), nil
	}
	if _, text, ok := simpleSymRef(expr, f); ok {
		return text, nil
	}
	var b bytes.Buffer
	b.WriteByte('(')
	if err := printExpr(&b, f, expr); err != nil {
		return "", err
	}
	b.WriteByte(')')
	return b.String(), nil
}

// emitLitInsts renders a KindLitInsts record (a run of literal words). The
// compactor no longer emits this kind — INSN_RUN mode 0 subsumes it — but the
// renderer handles it for completeness while the kind is being retired.
func emitLitInsts(out *bytes.Buffer, rec format.Record, prevWasStatement *bool, pc *int64, flush func()) {
	for i := 0; i < int(rec.LitCount); i++ {
		flush()
		word := binary.LittleEndian.Uint32(rec.LitWords[4*i:])
		if *prevWasStatement {
			out.WriteByte('\n')
		}
		out.WriteString("  ")
		out.WriteString(decodeLiteral(word))
		*prevWasStatement = true
		*pc += 4
	}
}

// litDataPerLine caps the values rendered on one data-directive line. A
// directive record stores its operand count in a single byte, so a line may
// carry at most 255 operands; 16 stays well under that and keeps the rendered
// data readable. (One LIT_DATA record packs up to ~1016 bytes, i.e. far more
// than 255 elements, so it must span several directive lines.)
const litDataPerLine = 16

// emitLitData renders a KindLitData record (a run of constant data bytes) as
// source data directives with comma-separated little-endian values, wrapping
// at litDataPerLine values per line. The rendered directives re-assemble to
// the identical bytes.
func emitLitData(out *bytes.Buffer, rec format.Record, prevWasStatement *bool) error {
	name := format.DirectiveName(rec.LitDataDirID)
	if name == "" {
		return fmt.Errorf("LIT_DATA: unknown directive id %d", rec.LitDataDirID)
	}
	width := dataWidth(name)
	if width == 0 {
		return fmt.Errorf("LIT_DATA: directive %q is not a numeric data directive", name)
	}
	if len(rec.LitData)%width != 0 {
		return fmt.Errorf("LIT_DATA: %q data length %d not a multiple of %d", name, len(rec.LitData), width)
	}
	nElem := len(rec.LitData) / width
	for i := 0; i < nElem; i++ {
		if i%litDataPerLine == 0 {
			if *prevWasStatement {
				out.WriteByte('\n')
			}
			out.WriteString("  ")
			out.WriteString(name)
			out.WriteByte(' ')
			*prevWasStatement = true
		} else {
			out.WriteString(", ")
		}
		var v uint64
		for b := 0; b < width; b++ {
			v |= uint64(rec.LitData[i*width+b]) << (8 * b)
		}
		fmt.Fprintf(out, "%#x", v)
	}
	return nil
}

func dataWidth(name string) int {
	switch name {
	case ".byte":
		return 1
	case ".short", ".hword":
		return 2
	case ".word":
		return 4
	case ".quad":
		return 8
	}
	return 0
}
