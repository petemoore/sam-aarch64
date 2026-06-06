package aarch64dec

import (
	"fmt"

	"github.com/petemoore/sam-aarch64/tools/aarch64enc"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// decodeCtx carries the per-instruction context (the full 32-bit
// word and the PC the instruction is being decoded at) so that
// kinds whose textual output depends on PC (branch / ADR / ADRP)
// or on bits outside slot.BitPosition..BitPosition+BitWidth
// (Imm12Shifted / Imm16Shifted / ExtendOp / AdrpImm / AdrImm)
// can reach them.
//
// Decode() defaults pc=0 (matches objdump's `objdump -d -b binary`
// behaviour: addresses start at file offset 0); DecodeAt() lets the
// caller pin a base address — needed for round-trip tests where the
// disassembler input was assembled at a known PC.
type decodeCtx struct {
	word uint32
	pc   uint64
}

// decodeSlot reads bits[BitPosition:BitPosition+BitWidth] (and any
// extra fields the kind owns beyond BitWidth — see Imm12Shifted /
// Imm16Shifted / ExtendOp / AdrpImm / AdrImm) and returns the
// canonical objdump text.
//
// ok=false when the slot kind is not yet implemented; callers must
// treat that as a non-match (the disassembler is grown SlotKind by
// SlotKind — see the M6 strand B plan).
func decodeSlot(ctx decodeCtx, slot aarch64enc.OperandSlot) (string, bool) {
	bits := extractBits(ctx.word, slot.BitPosition, slot.BitWidth)
	switch slot.SlotKind {
	case aarch64enc.Xreg:
		return decodeReg(bits, "x", "xzr"), true
	case aarch64enc.Wreg:
		return decodeReg(bits, "w", "wzr"), true
	case aarch64enc.XregOrSp:
		return decodeReg(bits, "x", "sp"), true
	case aarch64enc.WregOrSp:
		return decodeReg(bits, "w", "wsp"), true
	case aarch64enc.CondCode:
		return decodeCond(bits), true
	case aarch64enc.Imm5, aarch64enc.Imm6:
		// Imm5 / Imm6 are plain unsigned N-bit immediates; the bit
		// width (value range) is carried in slot.BitWidth — see
		// aarch64enc.encodeImmN.  The only forms using them are
		// ccmp / ccmn (imm5 and nzcv fields), which objdump renders
		// in hex (`#0x5`, `#0x3`); match that.
		return fmt.Sprintf("#%#x", bits), true
	case aarch64enc.ShiftAmount:
		// ShiftAmount is an unsigned N-bit immediate that lives
		// inside a shifted-register operand (`Xn, lsl #N`).  The
		// surrounding render — the comma, the shift-kind keyword —
		// belongs to whatever dispatcher composes the operand; the
		// slot itself contributes just `#N`.
		return fmt.Sprintf("#%d", bits), true
	case aarch64enc.Imm12Shifted:
		return decodeImm12Shifted(ctx.word, slot), true
	case aarch64enc.Imm16Shifted:
		return decodeImm16Shifted(ctx.word, slot), true
	case aarch64enc.ExtendOp:
		return decodeExtendOp(ctx.word, slot), true
	case aarch64enc.BranchImm26, aarch64enc.BranchImm19, aarch64enc.BranchImm14:
		return decodeBranchImm(ctx, slot), true
	case aarch64enc.AdrpImm:
		return decodeAdrpImm(ctx), true
	case aarch64enc.AdrImm:
		return decodeAdrImm(ctx), true
	case aarch64enc.LogicalImm:
		// Returns ok=false when N:immr:imms is not a legal bitmask
		// encoding; that fails the Form so DecodeAt falls back to .inst,
		// matching objdump's `undefined` on those words.
		return decodeLogicalImm(ctx.word, slot)
	case aarch64enc.BitfieldImm:
		// BitfieldImm appears twice in any form that uses it
		// (immr and imms slots); each slot independently renders
		// as a 6-bit unsigned `#N`.  Alias-driven renaming to
		// ubfx / bfxil / lsl / lsr — which converts (immr, imms)
		// to (lsb, width) — is Task 3's job.
		return fmt.Sprintf("#%d", bits), true
	}
	return "", false
}

// extractBits returns word[bitPosition : bitPosition+bitWidth] as a
// right-justified uint32.  Mirrors the encoder's shift-and-mask.
func extractBits(word uint32, bitPosition, bitWidth uint8) uint32 {
	mask := (uint32(1) << bitWidth) - 1
	return (word >> bitPosition) & mask
}

// decodeReg renders a 5-bit register index, using zeroName when the
// index is 31 (xzr/wzr for the zero-register kinds; sp/wsp for the
// SP-able kinds).
func decodeReg(bits uint32, prefix, zeroName string) string {
	if bits == 31 {
		return zeroName
	}
	return fmt.Sprintf("%s%d", prefix, bits)
}

// decodeCond maps a 4-bit condition field to its objdump-canonical
// name (`eq`, `ne`, `cs`, …, `nv`).  See format.CondCode.
func decodeCond(bits uint32) string {
	return format.CondCode(bits & 0xF).Name()
}

// decodeImm12Shifted reads the (sh:1, imm12:12) pair used by
// add/sub immediate.  imm12 lives at slot.BitPosition (BitWidth=12);
// sh lives at slot.BitPosition+12 (BitWidth=1) — same layout the
// encoder uses in aarch64enc.encodeImm12Shifted.
//
// objdump renders this as a single operand `#0xN` when sh=0, and as
// two comma-separated operands `#0xN, lsl #12` when sh=1.  Returning
// a comma-embedded string lets the outer Join collapse cleanly into
// the right text without the decode loop having to know about
// multi-operand slots.
func decodeImm12Shifted(word uint32, slot aarch64enc.OperandSlot) string {
	imm12 := extractBits(word, slot.BitPosition, 12)
	sh := extractBits(word, slot.BitPosition+12, 1)
	if sh == 1 {
		return fmt.Sprintf("#%#x, lsl #12", imm12)
	}
	return fmt.Sprintf("#%#x", imm12)
}

// decodeImm16Shifted reads the (hw:2, imm16:16) pair used by movz /
// movk / movn.  imm16 lives at slot.BitPosition (BitWidth=16); hw
// lives at slot.BitPosition+16 (BitWidth=2) — mirror of
// aarch64enc.encodeImm16Shifted.
//
// hw decodes to lsl-amount via 0→0, 1→16, 2→32, 3→48.  Objdump
// suppresses `, lsl #0` (i.e. renders just `#0xN`) and emits the
// explicit shift otherwise.  Verified with
//
//	echo -ne '\x69\x24\x80\xf2' > /tmp/t.bin            # movk x9, #0x123
//	aarch64-elf-objdump -D -b binary -m aarch64 /tmp/t.bin
//	  → movk x9, #0x123
//	echo -ne '\x69\x24\xa0\xf2' > /tmp/t.bin            # ", lsl #16
//	  → movk x9, #0x123, lsl #16
func decodeImm16Shifted(word uint32, slot aarch64enc.OperandSlot) string {
	imm16 := extractBits(word, slot.BitPosition, 16)
	hw := extractBits(word, slot.BitPosition+16, 2)
	if hw == 0 {
		return fmt.Sprintf("#%#x", imm16)
	}
	return fmt.Sprintf("#%#x, lsl #%d", imm16, hw*16)
}

// decodeExtendOp reads the (option:3, imm3:3) pair used by
// extended-register operand forms (`Rn, sxtw #N`, `Rn, uxtb`, …).
// option (extend kind) sits at slot.BitPosition+3; imm3 (shift
// amount) sits at slot.BitPosition — mirror of
// aarch64enc.encodeExtendOp.  Objdump renders
//
//	0  →  `<ext>`           (when imm3 = 0)
//	N>0 → `<ext> #N`
//
// where `<ext>` is one of uxtb/uxth/uxtw/uxtx/sxtb/sxth/sxtw/sxtx.
// The bracket-wrapping / surrounding register text belongs to the
// composing dispatcher; the slot contributes just the extend phrase.
func decodeExtendOp(word uint32, slot aarch64enc.OperandSlot) string {
	option := extractBits(word, slot.BitPosition+3, 3)
	imm3 := extractBits(word, slot.BitPosition, 3)
	ext := format.ExtendKind(option).Name()
	if imm3 == 0 {
		return ext
	}
	return fmt.Sprintf("%s #%d", ext, imm3)
}
