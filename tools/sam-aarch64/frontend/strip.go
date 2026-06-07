package frontend

import (
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// dataDirectiveSet is the set of directive names that emit data bytes into
// the output binary.  These are stripped by StripDataRecords.
var dataDirectiveSet = map[string]bool{
	".byte": true, ".short": true, ".hword": true,
	".word": true, ".quad": true,
	".ascii": true, ".asciz": true,
	".skip": true, ".space": true,
	".ltorg": true,
}

// StripCommentRecords returns the in-memory record IR with every KindComment
// record removed.
//
// Use case: the SAM-side assembler's IN buffer caps at 96 KB; the full
// flattened spectrum4 release.tbn is ~408 KB.  Comments are by far the
// bulk of that volume and are not used by the encoder.  Stripping them
// produces an ~88 KB .tbn that fits the IN buffer ceiling with room
// to spare.  See the FAIL00 investigation note (docs/notes/2026-05-28-
// test-variant-ci-regression.md and the recovery PR) for context.
func StripCommentRecords(records []format.Record) []format.Record {
	out := make([]format.Record, 0, len(records))
	for _, rec := range records {
		if rec.Kind == format.KindComment {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// ThinComments returns the record IR keeping only one in every n COMMENT
// records (in source order), stripping the rest; n <= 1 keeps them all. Use
// case: the SAM m6/release gate can't fit the full ~335 KB of release.s
// comments in the 96 KB IN buffer, but stripping them all leaves the SAM-side
// editor region empty (no on-hardware coverage of a populated editor region).
// Thinning keeps a bounded, spread-out subset (e.g. 1-in-20 ≈ a few KB) so the
// full Z80 round-trip exercises a non-empty comment sidecar while the `.tbn`
// stays well under the ceiling. Byte-neutral: comments are assembly no-ops.
func ThinComments(records []format.Record, n int) []format.Record {
	if n <= 1 {
		return records
	}
	out := make([]format.Record, 0, len(records))
	ci := 0
	for _, rec := range records {
		if rec.Kind == format.KindComment {
			keep := ci%n == 0
			ci++
			if !keep {
				continue
			}
		}
		out = append(out, rec)
	}
	return out
}

// StripDataRecords returns the in-memory record IR with all data-emitting
// records removed: KindDirective records for data directives (.word, .quad,
// .byte, .hword, .short, .ascii, .asciz, .skip, .space, .ltorg) and KindInst
// records that carry an OpLitPool operand (ldr Xn, =expr).  Label,
// local-label, comment, and non-data directive records are preserved.
//
// Use case: produce a code-only .tbn for the disassembler round-trip gate
// on spectrum4 release.s, which embeds data (literal pools, .word/.quad
// tables).  Stripping data ensures the assembled binary contains only
// 4-byte instruction words, so aarch64dec never encounters .inst entries
// from embedded data words.
func StripDataRecords(records []format.Record) []format.Record {
	out := make([]format.Record, 0, len(records))
	for _, rec := range records {
		switch rec.Kind {
		case format.KindDirective:
			if dataDirectiveSet[format.DirectiveName(rec.DirectiveID)] {
				continue
			}
		case format.KindInst:
			if instHasLitPoolOperand(rec) {
				continue
			}
		}
		out = append(out, rec)
	}
	return out
}

// instHasLitPoolOperand reports whether rec (a KindInst record) contains an
// OpLitPool operand, i.e. whether it is an `ldr Xn, =expr` instruction.
func instHasLitPoolOperand(rec format.Record) bool {
	or := format.NewOperandReader(rec.Operands)
	for !or.AtEnd() {
		o, err := or.Next()
		if err != nil {
			break
		}
		if o.Kind == format.OpLitPool {
			return true
		}
	}
	return false
}
