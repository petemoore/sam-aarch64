package assemble

import (
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// pcProbeA and pcProbeB are two distinct 4-byte-aligned PCs used to
// confirm a structurally-literal instruction encodes identically
// regardless of PC before its bytes are frozen into a literal INSN_RUN
// element. A constant-target PC-relative form (e.g. `b 0x1000`) is
// structurally literal yet PC-dependent, so it must stay an overlay
// element — this guard catches that.
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

// Compact rewrites a symbolic .tbn record stream into a compact v2 one:
// every instruction becomes an element of a KindInsnRun record — a
// fully-literal, PC-invariant instruction as a bare 4-byte word, a
// symbol/PC-bearing one as a base word (relocated field zeroed) plus a
// {slot, expression} overlay patch. Same-directive constant data collapses
// to KindLitData; every other record passes through verbatim. PC accounting
// is preserved exactly (each instruction occupies 4 bytes), so label
// positions, the literal pool, and the 2-pass values are unchanged — the
// m6-release gate verifies this by byte-matching the assembled output of the
// compact .tbn against the symbolic one.
func Compact(f *format.File, p1 *Pass1Result) ([]byte, error) {
	var w format.RecordWriter

	// Two run accumulators: instruction-run elements (INSN_RUN) and
	// same-directive constant data bytes (LIT_DATA). Only one is ever open
	// at a time — a record of the other kind flushes it first.
	var instRun []format.InsnElement
	var dataRun []byte
	var dataDirID byte
	var dataWidth int // 0 = no open data run

	flushInst := func() {
		emitInsnRunFrames(&w, instRun)
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

	// instIdx indexes p1.InstPC: the Nth KindInst this loop sees is the Nth
	// pass1 recorded, so InstPC[instIdx] is its true PC.
	instIdx := 0
	rr := format.NewRecordReader(f.Records)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			return nil, err
		}
		// Position-labels and numeric-local defs move out of the record
		// stream into the header label/local tables (§2.4). Drop them here
		// WITHOUT flushing the open instruction run, so a run spans the
		// labels embedded in it — the whole point of the header table. Their
		// offsets are recorded by pass1 (res.Symbols / res.LocalDefs) and
		// emitted as header rows by writeCompactTBN.
		//
		// A data run, however, IS flushed at a label/local: a LIT_DATA record
		// is one contiguous block keyed by its start PC, so merging data
		// across a label would bury the label's offset mid-block where the
		// disassembler cannot place it. Flushing keeps every label/local on a
		// clean record boundary. (The assembled bytes are unchanged — this
		// only splits one LIT_DATA into two; the m6 byte-match proves it.)
		if rec.Kind == format.KindLabelDef || rec.Kind == format.KindLocalDef {
			flushData()
			continue
		}
		if rec.Kind == format.KindInst {
			flushData()
			if instIdx >= len(p1.InstPC) {
				return nil, fmt.Errorf("compact: instruction index %d past pass1 InstPC (%d)", instIdx, len(p1.InstPC))
			}
			el, ierr := compactInst(rec, p1.InstPC[instIdx], p1, f)
			instIdx++
			if ierr != nil {
				return nil, ierr
			}
			instRun = append(instRun, el)
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

// headerRows builds the compact `.tbn` header label/local tables from pass1
// results. Label rows are one per position-label (p1.LabelDefs filters out
// `.equ`/`.set` value-symbols); local rows are one per numeric-local def
// site. Offsets are byte offsets from origin (= symbolVMA - OriginVMA, ≥ 0).
// name_id is the name's index in f.Names. The rows are returned unsorted;
// WriteFile sorts them by offset into the on-disk delta-varint order.
func headerRows(f *format.File, p1 *Pass1Result) ([]format.LabelRow, []format.LocalRow) {
	nameID := make(map[string]uint16, len(f.Names))
	for i, n := range f.Names {
		// First-occurrence index matches the IDs the records reference (the
		// name table is interned in ID order with no duplicates).
		if _, seen := nameID[n]; !seen {
			nameID[n] = uint16(i)
		}
	}
	var labels []format.LabelRow
	for name := range p1.LabelDefs {
		labels = append(labels, format.LabelRow{
			NameID: nameID[name],
			Offset: p1.Symbols[name] - p1.OriginVMA,
		})
	}
	var locals []format.LocalRow
	for digit, pcs := range p1.LocalDefs {
		for _, pc := range pcs {
			locals = append(locals, format.LocalRow{
				Digit:  digit,
				Offset: pc - p1.OriginVMA,
			})
		}
	}
	return labels, locals
}

// compactInst turns one KindInst record at its true PC into an INSN_RUN
// element: a bare base word when the instruction is fully-literal and
// PC-invariant, else a base word with a {slot, expression} overlay patch.
func compactInst(rec format.Record, pc int64, p1 *Pass1Result, f *format.File) (format.InsnElement, error) {
	if word, ok := literalWord(rec, p1, f); ok {
		return format.InsnElement{BaseWord: word}, nil
	}
	base, patches, err := encodeInstOverlay(rec, pc, p1, f)
	if err != nil {
		return format.InsnElement{}, err
	}
	return format.InsnElement{BaseWord: base, Patches: patches}, nil
}

// insnRunMaxPayload caps an INSN_RUN payload to the Z80 STAGING_BUF budget
// (the same 1016-byte headroom as litDataMaxBytes); runs split on whole-
// element boundaries so the Z80 decoder never sees a partial element.
const insnRunMaxPayload = 1016

// litBreak is the literal-run length at which the packer closes an overlay
// (mode-1) frame and emits the following literals as a mode-0 frame (4-byte
// floor) instead of absorbing them as 5-byte overlay literals. This choice
// only affects size, never the assembled bytes.
const litBreak = 4

// emitInsnRunFrames packs a maximal run of consecutive instructions into
// INSN_RUN frames: maximal patch-free stretches become mode-0 frames (4
// bytes/instruction), stretches around symbolic instructions become mode-1
// frames (absorbing short literal gaps). Every frame is split on whole-
// element boundaries to fit the Z80 staging budget.
func emitInsnRunFrames(w *format.RecordWriter, elems []format.InsnElement) {
	i := 0
	for i < len(elems) {
		if len(elems[i].Patches) == 0 {
			j := i
			for j < len(elems) && len(elems[j].Patches) == 0 {
				j++
			}
			emitMode0InsnRun(w, elems[i:j])
			i = j
			continue
		}
		j := i + 1
		for j < len(elems) {
			if len(elems[j].Patches) > 0 {
				j++
				continue
			}
			k := j
			for k < len(elems) && len(elems[k].Patches) == 0 {
				k++
			}
			if k-j >= litBreak || k == len(elems) {
				break // long gap or tail: close mode-1, let the literals be mode-0
			}
			j = k // absorb a short literal gap into the overlay frame
		}
		emitMode1InsnRun(w, elems[i:j])
		i = j
	}
}

func emitMode0InsnRun(w *format.RecordWriter, elems []format.InsnElement) {
	const maxWords = (insnRunMaxPayload - 1) / 4 // payload = 1 (mode) + 4*n
	for len(elems) > 0 {
		n := len(elems)
		if n > maxWords {
			n = maxWords
		}
		w.WriteInsnRun(0, elems[:n])
		elems = elems[n:]
	}
}

func emitMode1InsnRun(w *format.RecordWriter, elems []format.InsnElement) {
	start := 0
	payload := 1 // mode byte
	for idx := range elems {
		sz := insnElementSize(elems[idx])
		if idx > start && payload+sz > insnRunMaxPayload {
			w.WriteInsnRun(1, elems[start:idx])
			start = idx
			payload = 1
		}
		payload += sz
	}
	if start < len(elems) {
		w.WriteInsnRun(1, elems[start:])
	}
}

func insnElementSize(el format.InsnElement) int {
	sz := 4 + 1 // base word + patch_count
	for _, p := range el.Patches {
		sz += 2 + len(p.Expr) // slot + expr_len + expr bytes
	}
	return sz
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
