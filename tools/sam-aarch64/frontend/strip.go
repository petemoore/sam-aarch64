package frontend

import (
	"bytes"

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

// StripCommentRecords returns the input `.tbn` with every KindComment
// record removed.  The file header (magic, version, flags, name table)
// is preserved verbatim; only the record stream is filtered.
//
// Use case: the SAM-side assembler's IN buffer caps at 96 KB; the full
// flattened spectrum4 release.tbn is ~408 KB.  Comments are by far the
// bulk of that volume and are not used by the encoder.  Stripping them
// produces an ~88 KB .tbn that fits the IN buffer ceiling with room
// to spare.  See the FAIL00 investigation note (docs/notes/2026-05-28-
// test-variant-ci-regression.md and the recovery PR) for context.
func StripCommentRecords(in []byte) ([]byte, error) {
	f, err := format.ReadFile(in)
	if err != nil {
		return nil, err
	}

	// The pre-records section (magic + version + flags + name table) is
	// whatever's before f.Records in the input.  Copy it verbatim.
	headerLen := len(in) - len(f.Records)
	header := in[:headerLen]

	var newRecords bytes.Buffer
	r := format.NewRecordReader(f.Records)
	for !r.AtEnd() {
		rec, err := r.Next()
		if err != nil {
			return nil, err
		}
		if rec.Kind == format.KindComment {
			continue
		}
		// Re-emit the 3-byte record header (kind + u16 little-endian
		// length) and the raw payload verbatim.  Reader strips the
		// header into rec.Kind + len(rec.Raw); we reconstruct it.
		n := uint16(len(rec.Raw))
		newRecords.WriteByte(byte(rec.Kind))
		newRecords.WriteByte(byte(n))
		newRecords.WriteByte(byte(n >> 8))
		newRecords.Write(rec.Raw)
	}

	out := make([]byte, 0, len(header)+newRecords.Len())
	out = append(out, header...)
	out = append(out, newRecords.Bytes()...)
	return out, nil
}

// StripDataRecords returns the input `.tbn` with all data-emitting records
// removed: KindDirective records for data directives (.word, .quad, .byte,
// .hword, .short, .ascii, .asciz, .skip, .space, .ltorg) and KindInst
// records that carry an OpLitPool operand (ldr Xn, =expr).  Label,
// local-label, comment, and non-data directive records are preserved.
//
// Use case: produce a code-only .tbn for the disassembler round-trip gate
// on spectrum4 release.s, which embeds data (literal pools, .word/.quad
// tables).  Stripping data ensures the assembled binary contains only
// 4-byte instruction words, so aarch64dec never encounters .inst entries
// from embedded data words.
func StripDataRecords(in []byte) ([]byte, error) {
	f, err := format.ReadFile(in)
	if err != nil {
		return nil, err
	}

	headerLen := len(in) - len(f.Records)
	header := in[:headerLen]

	var newRecords bytes.Buffer
	r := format.NewRecordReader(f.Records)
	for !r.AtEnd() {
		rec, err := r.Next()
		if err != nil {
			return nil, err
		}
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
		n := uint16(len(rec.Raw))
		newRecords.WriteByte(byte(rec.Kind))
		newRecords.WriteByte(byte(n))
		newRecords.WriteByte(byte(n >> 8))
		newRecords.Write(rec.Raw)
	}

	out := make([]byte, 0, len(header)+newRecords.Len())
	out = append(out, header...)
	out = append(out, newRecords.Bytes()...)
	return out, nil
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
