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

// litDataMaxBytes caps the raw bytes in one LIT_DATA record. The Z80
// reader stages each record's payload into a 1024-byte STAGING_BUF and
// fails (src/reader.asm tag 01) if the payload reaches 1024. A LIT_DATA
// payload is [dir_id]+bytes, so the bytes must stay ≤ 1022; 1016 leaves
// headroom and is divisible by every element width (1/2/4/8) so a record
// always holds whole elements. (LIT_INSTS is separately bounded by its
// 255-word/1021-byte cap.)
const litDataMaxBytes = 1016

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

	// Two run accumulators: fully-literal instruction words (LIT_INSTS)
	// and same-directive constant data bytes (LIT_DATA). Only one is
	// ever open at a time — a record of the other kind flushes it first.
	var instRun []uint32
	var dataRun []byte
	var dataDirID byte
	var dataWidth int // 0 = no open data run

	flushInst := func() {
		for len(instRun) > 0 {
			n := len(instRun)
			if n > 255 { // a LIT_INSTS run holds at most 255 words
				n = 255
			}
			w.WriteLitInsts(instRun[:n])
			instRun = instRun[n:]
		}
		instRun = nil
	}
	flushData := func() {
		if dataWidth == 0 {
			return
		}
		// Split so each record fits the Z80 STAGING_BUF (litDataMaxBytes),
		// on whole-element boundaries so a record always holds complete
		// .word/.quad/… elements (the disassembler depends on it).
		max := litDataMaxBytes / dataWidth * dataWidth
		for len(dataRun) > 0 {
			n := len(dataRun)
			if n > max {
				n = max
			}
			w.WriteLitData(dataDirID, dataRun[:n])
			dataRun = dataRun[n:]
		}
		dataRun = nil
		dataWidth = 0
	}
	flushAll := func() { flushInst(); flushData() }

	rr := format.NewRecordReader(f.Records)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			return nil, err
		}
		if word, ok := literalWord(rec, p1, f); ok {
			flushData()
			instRun = append(instRun, word)
			continue
		}
		if width, ok := format.ConstDataWidth(rec); ok {
			if raw, derr := encodeDirective(rec, 0, p1, f); derr == nil && raw != nil {
				flushInst()
				if dataWidth != 0 && dataDirID != rec.DirectiveID {
					flushData() // different directive — start a new run
				}
				dataDirID = rec.DirectiveID
				dataWidth = width
				dataRun = append(dataRun, raw...)
				continue
			}
			// encode error: fall through and keep the record symbolic.
		}
		flushAll()
		w.WriteRaw(rec.Kind, rec.Raw)
	}
	flushAll()
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
