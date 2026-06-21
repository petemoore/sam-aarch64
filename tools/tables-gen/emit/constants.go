package emit

import (
	"fmt"
	"io"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// RenderConstantsInc writes the generated src/tbn_constants.inc: the
// OP_KIND_* / REC_KIND_* / DIR_* / MEM_SHAPE_* / EXT_* / SHIFT_* assembly-time
// equates the Z80 assembler uses to interpret a .tbn record stream, projected
// from the Go authority tools/sam-aarch64-format/{operands,kinds,directives}.go.
//
// These are assembly-time equates only — they emit zero runtime bytes — so
// the one-time switchover gate is exact: assembler{,-prod}.bin must stay
// byte-identical to the pre-switchover build (a rename-only refactor).
//
// Three things make the emission NOT a blind projection of the Go Name()
// helpers, and the explicit tables below encode each deliberately:
//
//   - OP_KIND_*: the Z80 spells the SP-register kinds OP_KIND_REG_XSP /
//     OP_KIND_REG_WSP, where the Go OperandKind.Name() returns REG_X_SP /
//     REG_W_SP. The opKinds list carries the exact Z80 suffix per value so
//     the generated symbol names match every `cp OP_KIND_*` site verbatim.
//   - REC_KIND_*: the Z80 carries a 6-of-9 SUBSET of the Go RecordKind set
//     (it omits LABEL_DEF, LOCAL_DEF, BLANK_RUN — record kinds it never
//     dispatches on). recKinds lists exactly the six it uses, in value
//     order, so the generated block reproduces the existing subset.
//   - DIR_*: the full DirectiveTable (append-only, IDs 0..N-1) maps
//     mechanically — `.text` → DIR_TEXT — with the directive name kept as
//     an inline comment, matching the hand-written block.
//   - MEM_SHAPE_*: the seven MemShape sub-codes for MEM operands, projected
//     from operands.go (MemShape). Each entry is guarded against Go-side
//     renumbering exactly as OP_KIND_* is.
//   - EXT_*: the eight ExtendKind values for memory index-register extensions,
//     projected from operands.go (ExtendKind). Suffix is the uppercase of
//     ExtendKind.Name() (e.g. EXT_UXTB). Same guard as MEM_SHAPE_*.
//   - SHIFT_*: the four ShiftKind values for register shift operands,
//     projected from operands.go (ShiftKind). Suffix is the uppercase of
//     ShiftKind.Name() (e.g. SHIFT_LSL). Same guard as EXT_*.
func RenderConstantsInc(w io.Writer) error {
	writeConstantsHeader(w)

	// OP_KIND_* — every OperandKind, value order. The suffix is the exact
	// Z80 spelling (see opKinds); the value is taken from the Go authority
	// and MUST agree, or this errors (catching a Go-side renumber).
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	fmt.Fprintln(w, "; OperandKind constants (tools/sam-aarch64-format/operands.go).")
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	for _, ok := range opKinds {
		if got := byte(ok.kind); got != ok.value {
			return fmt.Errorf("OP_KIND_%s: Go OperandKind value 0x%02x disagrees with expected 0x%02x", ok.suffix, got, ok.value)
		}
		emitEqu(w, "OP_KIND_"+ok.suffix, fmt.Sprintf("&%02X", ok.value), "")
	}

	// REC_KIND_* — the 6-of-9 subset the Z80 dispatches on, value order.
	fmt.Fprintln(w)
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	fmt.Fprintln(w, "; RecordKind constants (tools/sam-aarch64-format/kinds.go).")
	fmt.Fprintln(w, "; The Z80 carries a SUBSET of the Go RecordKind set — only the kinds it")
	fmt.Fprintln(w, "; dispatches on (it omits LABEL_DEF, LOCAL_DEF).")
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	for _, rk := range recKinds {
		if got := byte(rk.kind); got != rk.value {
			return fmt.Errorf("REC_KIND_%s: Go RecordKind value 0x%02x disagrees with expected 0x%02x", rk.suffix, got, rk.value)
		}
		emitEqu(w, "REC_KIND_"+rk.suffix, fmt.Sprintf("&%02X", rk.value), "")
	}

	// DIR_* — the full append-only DirectiveTable, ID order.
	fmt.Fprintln(w)
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	fmt.Fprintln(w, "; Directive ID constants (tools/sam-aarch64-format/directives.go).")
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	for id, name := range format.DirectiveTable {
		suffix := dirSuffix(name)
		emitEqu(w, "DIR_"+suffix, fmt.Sprintf("%d", id), dirComment(name))
	}

	// MEM_SHAPE_* — the seven MemShape sub-codes for MEM operands, value order.
	fmt.Fprintln(w)
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	fmt.Fprintln(w, "; MemShape sub-codes for MEM operands (tools/sam-aarch64-format/operands.go).")
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	for _, ms := range memShapes {
		if got := byte(ms.shape); got != ms.value {
			return fmt.Errorf("MEM_SHAPE_%s: Go MemShape value %d disagrees with expected %d", ms.suffix, got, ms.value)
		}
		emitEqu(w, "MEM_SHAPE_"+ms.suffix, fmt.Sprintf("%d", ms.value), "")
	}

	// EXT_* — the eight ExtendKind values for index-register extend ops, value order.
	fmt.Fprintln(w)
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	fmt.Fprintln(w, "; ExtendKind constants for memory index-register extensions")
	fmt.Fprintln(w, "; (tools/sam-aarch64-format/operands.go).")
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	for i := 0; i < 8; i++ {
		ext := format.ExtendKind(i)
		suffix := extSuffix(ext.Name())
		if got := byte(ext); got != byte(i) {
			return fmt.Errorf("EXT_%s: Go ExtendKind value %d disagrees with expected %d", suffix, got, i)
		}
		emitEqu(w, "EXT_"+suffix, fmt.Sprintf("%d", i), "")
	}

	// SHIFT_* — the four ShiftKind values for shifted-register operands, value order.
	fmt.Fprintln(w)
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	fmt.Fprintln(w, "; ShiftKind constants for shifted-register operands")
	fmt.Fprintln(w, "; (tools/sam-aarch64-format/operands.go).")
	fmt.Fprintln(w, "; -----------------------------------------------------------------------")
	for i := 0; i < 4; i++ {
		sk := format.ShiftKind(i)
		suffix := shiftSuffix(sk.Name())
		if got := byte(sk); got != byte(i) {
			return fmt.Errorf("SHIFT_%s: Go ShiftKind value %d disagrees with expected %d", suffix, got, i)
		}
		emitEqu(w, "SHIFT_"+suffix, fmt.Sprintf("%d", i), "")
	}

	return nil
}

// opKindEntry binds a Go OperandKind to the exact Z80 equate suffix and the
// expected wire value. The suffix is NOT derived from OperandKind.Name():
// the SP kinds are spelled REG_XSP / REG_WSP on the Z80 side.
type opKindEntry struct {
	kind   format.OperandKind
	value  byte
	suffix string
}

var opKinds = []opKindEntry{
	{format.OpRegX, 0x01, "REG_X"},
	{format.OpRegW, 0x02, "REG_W"},
	{format.OpRegXSP, 0x03, "REG_XSP"},
	{format.OpRegWSP, 0x04, "REG_WSP"},
	{format.OpImmExpr, 0x05, "IMM_EXPR"},
	{format.OpShiftedReg, 0x06, "SHIFTED_REG"},
	{format.OpExtendedReg, 0x07, "EXTENDED_REG"},
	{format.OpMem, 0x08, "MEM"},
	{format.OpString, 0x09, "STRING"},
	{format.OpCond, 0x0A, "COND"},
	{format.OpSysName, 0x0B, "SYS_NAME"},
	{format.OpLitPool, 0x0C, "LIT_POOL"},
}

// recKindEntry binds a Go RecordKind to its Z80 equate suffix and value. The
// Z80 carries only the kinds it dispatches on — see RenderConstantsInc.
type recKindEntry struct {
	kind   format.RecordKind
	value  byte
	suffix string
}

var recKinds = []recKindEntry{
	{format.KindInst, 0x01, "INST"},
	{format.KindDirective, 0x04, "DIRECTIVE"},
	{format.KindComment, 0x05, "COMMENT"},
	{format.KindBlankRun, 0x06, "BLANK_RUN"},
	{format.KindLitInsts, 0x07, "LIT_INSTS"},
	{format.KindLitData, 0x08, "LIT_DATA"},
	{format.KindInsnRun, 0x09, "INSN_RUN"},
}

// memShapeEntry binds a Go MemShape to its Z80 equate suffix and expected value.
// The suffix is the Z80 spelling (e.g. BASE_OFF_PRE); the guard catches a
// Go-side renumber before a stale generated file can cause silent mismatch.
type memShapeEntry struct {
	shape  format.MemShape
	value  byte
	suffix string
}

var memShapes = []memShapeEntry{
	{format.MemBase, 0, "BASE"},
	{format.MemBaseOff, 1, "BASE_OFF"},
	{format.MemBaseOffPre, 2, "BASE_OFF_PRE"},
	{format.MemBaseOffPost, 3, "BASE_OFF_POST"},
	{format.MemBaseIdx, 4, "BASE_IDX"},
	{format.MemBaseIdxShifted, 5, "BASE_IDX_SHIFTED"},
	{format.MemBaseIdxExtended, 6, "BASE_IDX_EXTENDED"},
}

// extSuffix upper-cases a lowercase ExtendKind name ("uxtb") to "UXTB".
func extSuffix(name string) string {
	out := make([]byte, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// shiftSuffix upper-cases a lowercase ShiftKind name ("lsl") to "LSL".
func shiftSuffix(name string) string {
	out := make([]byte, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// dirSuffix maps a directive spelling (".balign") to its equate suffix
// (BALIGN). The leading dot is dropped and the rest upper-cased; directive
// names are ASCII [a-z] only, so a simple per-byte upcase suffices.
func dirSuffix(name string) string {
	body := name[1:] // drop the leading '.'
	out := make([]byte, len(body))
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// dirComment returns the inline comment shown after a DIR_* equate, matching
// the disambiguating notes the hand-written block carried for the synonyms.
func dirComment(name string) string {
	switch name {
	case ".align":
		return name + " (2^N)"
	case ".hword":
		return name + " (synonym of .short)"
	default:
		return name
	}
}

// emitEqu writes one `NAME: equ VALUE   ; comment` line in the column style
// of the hand-written src/main_loop.asm block. Names longer than 23 characters
// get a single trailing space before `equ` so the assembler can parse the line.
func emitEqu(w io.Writer, name, value, comment string) {
	label := name + ":"
	// Ensure at least one space between the label and `equ`.
	col := "%-24s"
	if len(label) >= 24 {
		col = "%s "
	}
	if comment == "" {
		fmt.Fprintf(w, col+"equ     %s\n", label, value)
		return
	}
	fmt.Fprintf(w, col+"equ     %-4s; %s\n", label, value, comment)
}

func writeConstantsHeader(w io.Writer) {
	fmt.Fprintln(w, "; GENERATED by tools/tables-gen — do not edit; regen with make tables")
	fmt.Fprintln(w, ";")
	fmt.Fprintln(w, "; tbn_constants.inc — the OP_KIND_* / REC_KIND_* / DIR_* / MEM_SHAPE_* /")
	fmt.Fprintln(w, "; EXT_* / SHIFT_* assembly-time equates the Z80 assembler uses to interpret")
	fmt.Fprintln(w, "; a .tbn record stream.")
	fmt.Fprintln(w, ";")
	fmt.Fprintln(w, "; Projected from the Go authority:")
	fmt.Fprintln(w, ";   OP_KIND_*   — tools/sam-aarch64-format/operands.go (OperandKind)")
	fmt.Fprintln(w, ";   REC_KIND_*  — tools/sam-aarch64-format/kinds.go (RecordKind; a 7-of-9")
	fmt.Fprintln(w, ";                 subset — the Z80 dispatches only on these)")
	fmt.Fprintln(w, ";   DIR_*       — tools/sam-aarch64-format/directives.go (DirectiveTable)")
	fmt.Fprintln(w, ";   MEM_SHAPE_* — tools/sam-aarch64-format/operands.go (MemShape)")
	fmt.Fprintln(w, ";   EXT_*       — tools/sam-aarch64-format/operands.go (ExtendKind)")
	fmt.Fprintln(w, ";   SHIFT_*     — tools/sam-aarch64-format/operands.go (ShiftKind)")
	fmt.Fprintln(w, ";")
	fmt.Fprintln(w, "; These are equates only — zero runtime bytes — so the assembler binary")
	fmt.Fprintln(w, "; is byte-identical across the rename-only switchover.  The freshness guard")
	fmt.Fprintln(w, "; `make tables-sync-check` fails if this file drifts from `make tables`.")
	fmt.Fprintln(w)
}
