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
	for _, f := range aarch64enc.AllForms() {
		if word&f.Mask == f.Pattern {
			return decodeForm(ctx, f)
		}
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
