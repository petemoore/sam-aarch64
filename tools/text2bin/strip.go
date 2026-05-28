package main

import (
	"bytes"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// stripCommentRecords returns the input `.tbn` with every KindComment
// record removed.  The file header (magic, version, flags, name table)
// is preserved verbatim; only the record stream is filtered.
//
// Use case: the SAM-side assembler's IN buffer caps at 96 KB; the full
// flattened spectrum4 release.tbn is ~408 KB.  Comments are by far the
// bulk of that volume and are not used by the encoder.  Stripping them
// produces an ~88 KB .tbn that fits the IN buffer ceiling with room
// to spare.  See the FAIL00 investigation note (docs/notes/2026-05-28-
// test-variant-ci-regression.md and the recovery PR) for context.
func stripCommentRecords(in []byte) ([]byte, error) {
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
