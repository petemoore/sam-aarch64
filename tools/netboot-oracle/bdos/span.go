package bdos

import "fmt"

// The firmware-spanning storage convention (i99 / q16). A fetched object larger
// than one bounded storage record is persisted as a sequence of records, each
// written with a plain HSAVE — the only *verified* save hook (the byte-stream
// append hooks HOFLE/HSBYT are broken for external RST 8 callers, sam-stub-
// audit.md) — and reassembled in order at serve time. This file is the Go
// authority for that split + the record naming; the Z80 src/netboot/fw_span.asm
// mirrors the per-record length + naming, and the real RST 8 HSAVE/HLOAD per
// record stays the hardware gate (CLAUDE.md §5). Emulation-verified ≠ hardware-
// verified.
//
// Why span at all: the HSAVE source must be contiguous in (paged) RAM, and a
// multi-MB firmware blob (start.elf ≈ 2.9 MB) exceeds the SAM's free RAM, so the
// streamed body is flushed to storage in bounded records as it arrives (the i99
// streaming sink) rather than held whole. The per-record cap is bounded by the
// RAM available for one HSAVE source; its exact value is a hardware detail pinned
// when the real persist is built, so it is a PARAMETER here (recordCap) — the
// split arithmetic is correct for any cap, with no speculative constant baked in.
//
// Naming (i114d). Record names are content-addressed: the first 3 bytes of the
// blob's SHA-256 digest encoded as 6 lowercase hex chars, plus a 3-digit
// zero-padded decimal record index (000, 001, …). A single-record blob uses
// <hash6>000 — there is no "plain name" special case for record-stored blobs.
// The 6-hex prefix is a fast index, not the identity source: the manifest (i114a)
// carries the full 32-byte hash, so a name-match is only a candidate; full-hash
// verification before any content-match conclusion is the caller's responsibility
// (i122). Local boot-disk files that never enter the span store keep their
// natural B-DOS name through the existing flat-store NameToUIFA path, unchanged.
// Three index digits bound a spanned object to 1000 records — ample for any
// firmware blob. The naming is internal and never user-visible.

// SpanIndexDigits is the width of the zero-padded record-index suffix on a
// content-addressed record name.
const SpanIndexDigits = 3

// SpanRecord is one stored record of a (possibly spanned) object: the B-DOS name
// it is HSAVE'd / HLOAD'd under, and the [Offset, Offset+Length) byte range of the
// logical object it holds.
type SpanRecord struct {
	Name   string
	Offset int
	Length int
}

// SpanRecordName returns the content-addressed B-DOS record name for record index
// of a blob identified by blobHash: the first 3 bytes of the hash as 6 lowercase
// hex chars, followed by the 3-digit zero-padded decimal index. The result is
// always 9 chars, within the 10-char B-DOS NameLen field.
func SpanRecordName(blobHash [32]byte, index int) string {
	return fmt.Sprintf("%02x%02x%02x%0*d", blobHash[0], blobHash[1], blobHash[2], SpanIndexDigits, index)
}

// SpanCount returns the number of records a size-byte object occupies at the
// given per-record cap: ceil(size/cap), and at least 1 (a zero-length object is
// a single empty record). recordCap ≤ 0 is invalid and returns 0.
func SpanCount(size, recordCap int) int {
	if recordCap <= 0 {
		return 0
	}
	if size <= 0 {
		return 1
	}
	return (size + recordCap - 1) / recordCap
}

// SpanPlan returns the ordered records a blob (identified by blobHash, of size
// bytes) is stored as at the given per-record cap. Every record carries a
// content-addressed name (SpanRecordName(blobHash, i)), including the
// single-record case (size ≤ cap), which is <hash6>000. The split arithmetic
// (offsets/lengths) is unchanged: N = ceil(size/cap) records, each holding up
// to cap bytes in order. This is both the persist write plan (HSAVE each record)
// and the serve read order (HLOAD each in order, concatenated into one TFTP
// stream); the Z80 fw_span.asm mirrors the per-record length + naming.
// recordCap ≤ 0 returns nil.
//
// Invariants (asserted by the tests): the records' lengths sum to size, their
// offsets run contiguously from 0, every length is ≤ cap, and len(records) ==
// SpanCount(size, cap).
func SpanPlan(blobHash [32]byte, size, recordCap int) []SpanRecord {
	if recordCap <= 0 {
		return nil
	}
	n := SpanCount(size, recordCap)
	recs := make([]SpanRecord, n)
	for i := 0; i < n; i++ {
		off := i * recordCap
		length := recordCap
		if rem := size - off; rem < recordCap {
			length = rem
		}
		recs[i] = SpanRecord{Name: SpanRecordName(blobHash, i), Offset: off, Length: length}
	}
	return recs
}

// StoredRecordNames returns the ordered B-DOS record names a blob occupies given
// its content hash and the number of records it spans. This is the lookup
// sequence the i122 dedup-before-fetch uses to check whether the blob is already
// present on-card (check <hash6>000, <hash6>001, … <hash6>(n-1) in order).
// spanCount must equal SpanCount(blobSize, recordCap) for the blob in question.
func StoredRecordNames(blobHash [32]byte, spanCount int) []string {
	names := make([]string, spanCount)
	for i := 0; i < spanCount; i++ {
		names[i] = SpanRecordName(blobHash, i)
	}
	return names
}
