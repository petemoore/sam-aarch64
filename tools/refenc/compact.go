package main

import (
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// pcProbeA and pcProbeB are two distinct 4-byte-aligned PCs used to
// confirm a structurally-literal instruction encodes identically
// regardless of PC before its bytes are frozen into a KindLitInsts run.
// A constant-target PC-relative form (e.g. `b 0x1000`) is structurally
// literal yet PC-dependent, so it must stay symbolic — this guard
// catches that.
const (
	pcProbeA = int64(0)
	pcProbeB = int64(0x40000)
)

// Compact rewrites a symbolic .tbn record stream into a compact one:
// runs of consecutive fully-literal, PC-invariant instructions collapse
// to KindLitInsts records storing their assembled words; every other
// record passes through verbatim. PC accounting is preserved exactly (a
// run of N words occupies 4*N bytes), so label positions, the literal
// pool, and the 2-pass values for symbolic instructions are unchanged —
// the m6-release gate verifies this by byte-matching the assembled
// output of the compact .tbn against the symbolic one.
func Compact(f *format.File, p1 *Pass1Result) ([]byte, error) {
	var w format.RecordWriter
	var run []uint32

	flush := func() {
		// A KindLitInsts run holds at most 255 words; split longer runs
		// across successive records.
		for len(run) > 0 {
			n := len(run)
			if n > 255 {
				n = 255
			}
			w.WriteLitInsts(run[:n])
			run = run[n:]
		}
		run = nil
	}

	rr := format.NewRecordReader(f.Records)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			return nil, err
		}
		if word, ok := literalWord(rec, p1, f); ok {
			run = append(run, word)
			continue
		}
		flush()
		w.WriteRaw(rec.Kind, rec.Raw)
	}
	flush()
	return w.Bytes(), nil
}

// literalWord returns the assembled word for rec when it is a fully-
// literal, PC-invariant instruction that can be frozen into a
// KindLitInsts run. ok=false means rec must be kept symbolic. An
// encoder error never aborts compaction — the instruction is simply
// kept symbolic, so pass2 reaches it at its true PC and reports any real
// error there rather than at a probe PC.
func literalWord(rec format.Record, p1 *Pass1Result, f *format.File) (uint32, bool) {
	if !format.IsFullyLiteral(rec) {
		return 0, false
	}
	wA, err := encodeInst(rec, pcProbeA, p1, f)
	if err != nil {
		return 0, false
	}
	wB, err := encodeInst(rec, pcProbeB, p1, f)
	if err != nil {
		return 0, false
	}
	if wA != wB {
		return 0, false
	}
	return wA, true
}
