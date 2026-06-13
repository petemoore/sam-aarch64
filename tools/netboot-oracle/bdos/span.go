package bdos

import "fmt"

// The firmware-spanning storage convention (i99 / q16). A fetched object larger
// than one bounded storage record is persisted as a sequence of records, each
// written with a plain HSAVE — the only *verified* save hook (the byte-stream
// append hooks HOFLE/HSBYT are broken for external RST 8 callers, sam-stub-
// audit.md) — and reassembled in order at serve time. This file is the Go
// authority for that split + the record naming; the Z80 src/netboot/fw_span.asm
// will mirror the per-record length + naming, and the real RST 8 HSAVE/HLOAD per
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
// Naming. A non-spanned object (size ≤ cap, a single record) keeps its plain
// logical name, so the kernel and small files are stored and served by their
// natural TFTP name through the existing flat store (bdos.NameToUIFA), unchanged.
// A spanned object's records are named <prefix><NNN>: the logical name truncated
// to (NameLen-SpanIndexDigits)=7 chars plus a 3-digit zero-padded decimal index
// (000, 001, …), a ≤10-char B-DOS name. The server derives the record count from
// the object's known size — N = ceil(size/cap), the manifest carries the size —
// so no on-disk index/metadata record is needed: it reads <prefix>000 …
// <prefix>(N-1) in order and streams them as one TFTP object. (A spanned object
// NOT in the manifest would need count discovery by probing names — a noted
// future extension; the firmware blobs that actually span are all manifest
// entries.)
//
// Constraint: spanned objects must have unique 7-char name prefixes — the six RPi
// firmware files do ("start.e" vs "start4." etc.). Three index digits bound a
// spanned object to 1000 records (1000·cap bytes — ample for any firmware blob).

// SpanIndexDigits is the width of the zero-padded record-index suffix on a
// spanned object's record names.
const SpanIndexDigits = 3

// SpanRecord is one stored record of a (possibly spanned) object: the B-DOS name
// it is HSAVE'd / HLOAD'd under, and the [Offset, Offset+Length) byte range of the
// logical object it holds.
type SpanRecord struct {
	Name   string
	Offset int
	Length int
}

// SpanRecordName returns the storage record name for record index of a spanned
// logical object: its name truncated to NameLen-SpanIndexDigits chars plus the
// zero-padded index. (A single-record object keeps its plain name — see SpanPlan;
// this suffixed form is used only when an object spans more than one record.)
func SpanRecordName(name string, index int) string {
	prefix := name
	if maxPrefix := NameLen - SpanIndexDigits; len(prefix) > maxPrefix {
		prefix = prefix[:maxPrefix]
	}
	return fmt.Sprintf("%s%0*d", prefix, SpanIndexDigits, index)
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

// SpanPlan returns the ordered records a logical object (name, size) is stored as
// at the given per-record cap. A non-spanned object (size ≤ cap) is a single
// record under the plain name; a spanned object is N = ceil(size/cap) records
// named <prefix>NNN, each holding up to cap bytes of the object in order. This is
// both the persist write plan (HSAVE each record) and the serve read order (HLOAD
// each in order, concatenated into one TFTP stream); the Z80 fw_span.asm mirrors
// the per-record length + naming. recordCap ≤ 0 returns nil.
//
// Invariants (asserted by the tests): the records' lengths sum to size, their
// offsets run contiguously from 0, every length is ≤ cap, and len(records) ==
// SpanCount(size, cap).
func SpanPlan(name string, size, recordCap int) []SpanRecord {
	if recordCap <= 0 {
		return nil
	}
	if size <= recordCap {
		return []SpanRecord{{Name: name, Offset: 0, Length: size}}
	}
	n := SpanCount(size, recordCap)
	recs := make([]SpanRecord, n)
	for i := 0; i < n; i++ {
		off := i * recordCap
		length := recordCap
		if rem := size - off; rem < recordCap {
			length = rem
		}
		recs[i] = SpanRecord{Name: SpanRecordName(name, i), Offset: off, Length: length}
	}
	return recs
}
