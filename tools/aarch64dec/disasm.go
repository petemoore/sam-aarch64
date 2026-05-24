// Package aarch64dec is the inverse of aarch64enc: take a 32-bit
// aarch64 machine-code word and produce text equivalent to
// `objdump -d -b binary -m aarch64`.
//
// The disassembler walks the same Form table the encoder uses
// (aarch64enc.AllForms()) and emits per-slot text via decodeSlot.
// The form table provides the (Pattern, Mask, Slots) tuple; the
// per-SlotKind decoders here read each slot's bits out of the
// word and render them in canonical objdump form.
package aarch64dec

import (
	"fmt"
	"strings"

	"github.com/petemoore/sam-aarch64/tools/aarch64enc"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Decode returns the canonical mnemonic + operand text for word.
// ok is false if no Form matches; callers can fall back to
// rendering the raw word as `.inst 0xNNNNNNNN`.
//
// Equivalent to DecodeAt(0, word) — matches objdump's default of
// numbering the disassembly from file offset 0 (`objdump -d -b
// binary -m aarch64`).  Callers that know the load address of word
// (round-trip tests, the editor) should use DecodeAt so that
// PC-relative kinds — branches, ADR, ADRP — render absolute target
// addresses correctly.
//
// The form table is scanned in AllForms() order (manualForms first,
// then generatedForms); the first matching Form wins.  Manual
// entries are deliberately listed first so the GNU-canonical
// encoding (e.g. `mov Xd, Xm` as ORR rather than ADD-imm-#0) shadows
// the MRA's preferred encoding — the same property the encoder
// relies on for byte-match.
func Decode(word uint32) (mnem string, operands string, ok bool) {
	return DecodeAt(0, word)
}

// DecodeAt is Decode with an explicit program-counter for word.
// pc is the byte address at which word would execute; it feeds the
// PC-relative decoders (BranchImm14/19/26, AdrpImm, AdrImm) so they
// emit absolute target addresses in objdump's style.
func DecodeAt(pc uint64, word uint32) (mnem string, operands string, ok bool) {
	ctx := decodeCtx{word: word, pc: pc}
	// The load/store (memory) family is hand-rolled by the encoder
	// (refenc/pass2.go) and is therefore absent from AllForms().  Decode
	// it via the special-case inverse in mem.go *first*: a handful of
	// form-table masks (the add/sub-immediate forms in particular) are
	// loose enough to spuriously match memory words, so the memory
	// decoder must take precedence on its own encoding space.  decodeMem
	// returns ok=false for everything outside that space, so the form
	// walk below still handles all other instructions.
	if mnem, operands, ok := decodeMem(pc, word); ok {
		return mnem, operands, true
	}
	// System instruction group (mrs/msr/dc/tlbi/at/ic + dsb/dmb/isb) and
	// test-and-branch (tbz/tbnz) are hand-rolled by the encoder and
	// absent from AllForms() — and occupy encoding spaces that no form
	// touches — so decode them via their special-case inverses ahead of
	// the form walk.  decodeSys declines the hint sub-space (nop/wfi),
	// which IS in AllForms, returning ok=false so the form walk handles
	// those.
	if mnem, operands, ok := decodeSys(word); ok {
		return mnem, operands, true
	}
	if mnem, operands, ok := decodeTestBranch(pc, word); ok {
		return mnem, operands, true
	}
	// UDF — the permanently-undefined encoding space: bits[31:16] == 0,
	// imm16 = bits[15:0] (ARM ARM C6.2.... "UDF").  objdump renders it
	// `udf #<imm16>` in decimal.  No real instruction occupies this space,
	// so a bare top-16-bits-zero test is unambiguous.  These appear in
	// release.img's data regions (small integer table entries that
	// linear-sweep as udf); matching objdump means rendering them likewise.
	if word>>16 == 0 {
		return "udf", fmt.Sprintf("#%d", word&0xffff), true
	}
	for _, f := range aarch64enc.AllForms() {
		if word&f.Mask == f.Pattern {
			return decodeForm(ctx, f)
		}
	}
	// Data-processing (shifted / extended register) — add/sub/logical —
	// is hand-rolled by the encoder (encodeShiftedRegInst /
	// encodeExtendedRegInst) and so is absent from AllForms().  Decode it
	// LAST, as a fallback: AllForms carries the GNU-preferred *alias*
	// forms over this same encoding space (mov = ORR Rn=xzr, cmp = SUBS
	// Rd=xzr, tst = ANDS Rd=xzr, etc.) with tighter masks, and those must
	// win.  Running decodeDPReg only after the form walk lets the alias
	// forms shadow it exactly as objdump prefers; decodeDPReg then names
	// the base mnemonic for every shifted/extended-reg word the aliases
	// don't claim.
	if mnem, operands, ok := decodeDPReg(word); ok {
		return mnem, operands, true
	}
	return "", "", false
}

// decodeForm renders a single matched Form to (mnemonic, operands).
// operands is empty when the form has no slots (e.g. `nop`, `wfi`).
func decodeForm(ctx decodeCtx, f aarch64enc.Form) (mnem string, operands string, ok bool) {
	mnem = format.MnemonicName(f.MnemonicID)
	if mnem == "" {
		return "", "", false
	}
	if len(f.Slots) == 0 {
		return mnem, "", true
	}
	parts := make([]string, 0, len(f.Slots))
	for _, slot := range f.Slots {
		s, sok := decodeSlot(ctx, slot)
		if !sok {
			return "", "", false
		}
		parts = append(parts, s)
	}
	return mnem, strings.Join(parts, ", "), true
}

// Format joins mnemonic + operands with a tab when operands is
// non-empty, matching `objdump -d -b binary -m aarch64`'s layout
// (`<mnem>\t<operands>`).
func Format(mnem, operands string) string {
	if operands == "" {
		return mnem
	}
	return mnem + "\t" + operands
}
