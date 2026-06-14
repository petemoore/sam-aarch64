// Package editmodel implements the paged block-list document model for the
// on-SAM editor (items i41a, i41b). It is a host-side correctness prototype of
// the block-list + record-id location-table algorithm and the bounded ring-journal
// undo/redo; the exact SAM byte layout, Z80 paging mechanics, and the actual
// ring-buffer data structure are the Z80 port's concern (i41d).
//
// Design authority: docs/specs/editor-edit-model-design.md (§4.4, §7.2).
package editmodel

import (
	"errors"
	"fmt"
)

// RecordID is a stable, per-session-monotonic, never-reused line identifier.
// It is constrained to u24 (max 0xFFFFFF). RecordID 0 is reserved and never
// allocated; it serves as a sentinel meaning "none".
type RecordID uint32

// maxRecordID is the largest valid RecordID (u24).
const maxRecordID = 0xFFFFFF

// blockCapacityBytes is the soft capacity of a single block (½-page, 8 KB,
// per design decision 1). This is the empirically-tunable ½-page knob —
// changing it does not require structural changes to the algorithm.
const blockCapacityBytes = 8192

// blockMergeThresholdBytes is the fill level below which an underflowing
// block is a merge candidate when an adjacent block can absorb it.
// The ¼-capacity threshold gives the block-list room to absorb a burst
// of deletes before triggering a merge.
const blockMergeThresholdBytes = blockCapacityBytes / 4

// lineOverheadBytes is the approximate SAM per-line overhead used in byte
// accounting for split/merge decisions (3-byte id + 2-byte in-block offset
// entry). This approximates the SAM block layout; the precise constant is
// tuned at Z80-port time (i41d).
const lineOverheadBytes = 5

// line is one source line in the document.
//
// text is the source text WITHOUT a trailing newline; the line boundary is
// implied by the record structure. The payload here is raw text per design
// decision 3 / §6 of the spec; wiring the i48 parsed-IR payload behind the
// serialize seam is deferred to sub-item i41c.
type line struct {
	id   RecordID
	text []byte
}

// block is a group of consecutive lines sized to fit within approximately
// one ½-page (8 KB) of SAM RAM.
//
// On the SAM, a block is one ½-page physical page with an intra-block gap
// buffer and a per-line offset table. This host model represents a block as
// an ordered line slice and proves the block-list + location-table algorithm
// (correctness). The exact SAM byte layout, intra-block gap, and latency are
// the Z80 port's concern (i41d).
type block struct {
	lines []line
}

// bytes returns the byte count of the block's content under SAM accounting:
// each line contributes its text length plus lineOverheadBytes for the id
// and offset-table entry.
func (b *block) bytes() int {
	total := 0
	for i := range b.lines {
		total += len(b.lines[i].text) + lineOverheadBytes
	}
	return total
}

// maxUndoDepth is the tunable cap on the undo ring journal (design §7.2).
// It is the bounded depth beyond which the oldest entries are dropped.
// The Z80 port (i41d) implements the actual ring buffer; this host model
// captures the capped-depth + drop-oldest semantics faithfully.
const maxUndoDepth = 256

// opKind distinguishes whether an editEntry records an insert or a delete.
type opKind uint8

const (
	opInsert opKind = iota
	opDelete
)

// editEntry records a single public edit for the undo/redo journal.
// For an insert, at is the document index where the line was inserted and
// id/text are the inserted line's identity and content. For a delete, at is
// the index that was deleted and id/text are the removed line's identity and
// content. A copy of text is always stored so the entry is independent of
// any external slice.
type editEntry struct {
	op   opKind
	at   int
	id   RecordID
	text []byte
}

// Document is the in-RAM document the editor mutates. It holds the logical
// sequence of lines partitioned into blocks, together with a bounded ring-journal
// undo/redo history (design §4.4, §7.2).
//
//   - blocks is the ordered list of blocks; each block holds a contiguous run
//     of lines. Block boundaries are not part of the logical document — they
//     are a storage detail driven by the ½-page capacity budget.
//   - nextID is the next RecordID to allocate; it only ever increases.
//   - loc maps every live RecordID to a stable pointer to the block currently
//     holding it (design §3.4). A pointer survives d.blocks reordering, so
//     inserting or removing a block touches zero unrelated entries — only the
//     records that actually move between blocks get re-pointed. On a split,
//     only the O(½ block) lines that move to the new right-half block are
//     updated (design §3.4). IndexOf locates a block's position by a small
//     scan of the resident block list (design §2.5 accepts this ~50-entry
//     scan). Entries are added on insert and removed on delete.
//   - undo is the undo journal stack (top = most recent edit); capped at
//     maxUndoDepth entries — the oldest is dropped when the cap is reached.
//   - redo is the redo stack rebuilt by Undo; cleared by any fresh public edit.
type Document struct {
	blocks []*block
	nextID RecordID
	loc    map[RecordID]*block
	undo   []editEntry
	redo   []editEntry
}

// New returns an empty Document. nextID starts at 1 (RecordID 0 is reserved).
// The undo and redo stacks start empty (nil slices); no history is synthesised
// for the initial empty state.
func New() *Document {
	return &Document{
		nextID: 1,
		loc:    make(map[RecordID]*block),
	}
}

// LineCount returns the total number of lines across all blocks.
func (d *Document) LineCount() int {
	total := 0
	for _, b := range d.blocks {
		total += len(b.lines)
	}
	return total
}

// blockAndLocalIndex returns the block index and the local line index within
// that block for the given document-level line index.
//
// When at is strictly less than LineCount(), the returned (blockIdx, localIdx)
// satisfy 0 ≤ localIdx < len(blocks[blockIdx].lines). When at equals
// LineCount() (insert-at-end), localIdx equals len(blocks[last].lines),
// which is the correct append position for InsertLine. Callers that require
// a valid in-bounds local index (LineAt, DeleteLine) have already validated
// at < LineCount() before calling.
func (d *Document) blockAndLocalIndex(at int) (blockIdx, localIdx int) {
	acc := 0
	for i, b := range d.blocks {
		cnt := len(b.lines)
		if at < acc+cnt {
			// at falls within this block.
			return i, at - acc
		}
		acc += cnt
	}
	// at == total line count (insert-at-end): append to the last block.
	if len(d.blocks) > 0 {
		last := len(d.blocks) - 1
		return last, len(d.blocks[last].lines)
	}
	return 0, 0
}

// doInsert performs a pure structural insert of a line with the given explicit
// id and text into position at. It does NOT allocate a new id and does NOT
// touch d.nextID; callers supply the id. text is copied so the caller can
// reuse its buffer. This primitive is used by both InsertLine (which allocates
// a fresh id and journals) and Undo/Redo (which replay with the original id
// and must not journal).
func (d *Document) doInsert(at int, id RecordID, text []byte) {
	// Copy text so callers can reuse their buffer.
	t := make([]byte, len(text))
	copy(t, text)
	l := line{id: id, text: t}

	if len(d.blocks) == 0 {
		// First line: create the first block.
		b := &block{lines: []line{l}}
		d.blocks = append(d.blocks, b)
		d.loc[id] = b
		return
	}

	blockIdx, localIdx := d.blockAndLocalIndex(at)
	b := d.blocks[blockIdx]
	// Insert into the block's line slice at localIdx.
	b.lines = append(b.lines, line{})
	copy(b.lines[localIdx+1:], b.lines[localIdx:])
	b.lines[localIdx] = l
	d.loc[id] = b

	// Split if over capacity and there are at least 2 lines to split.
	if b.bytes() > blockCapacityBytes && len(b.lines) >= 2 {
		d.splitBlock(blockIdx)
	}
}

// doDelete performs a pure structural delete of the line at position at.
// It returns the removed line's id and a copy of its text. This primitive is
// used by both DeleteLine (which journals) and Undo/Redo (which must not
// journal).
func (d *Document) doDelete(at int) (RecordID, []byte) {
	blockIdx, localIdx := d.blockAndLocalIndex(at)
	b := d.blocks[blockIdx]
	l := b.lines[localIdx]
	id := l.id

	// Copy text before removing, so the caller owns an independent slice.
	textCopy := make([]byte, len(l.text))
	copy(textCopy, l.text)

	// Remove from loc before mutating the block.
	delete(d.loc, id)

	// Remove from block's line slice.
	b.lines = append(b.lines[:localIdx], b.lines[localIdx+1:]...)

	if len(b.lines) == 0 {
		// Remove the empty block. No loc updates needed: other blocks' pointers
		// are unchanged and the deleted line's entry was already removed above.
		d.blocks = append(d.blocks[:blockIdx], d.blocks[blockIdx+1:]...)
		return id, textCopy
	}

	// Try to merge an underflowing block with a neighbour.
	if b.bytes() < blockMergeThresholdBytes {
		d.tryMerge(blockIdx)
	}

	return id, textCopy
}

// pushUndo appends an entry to the undo journal, dropping the oldest entry
// when the cap is reached (drop-oldest ring semantics, design §7.2).
func (d *Document) pushUndo(e editEntry) {
	d.undo = append(d.undo, e)
	if len(d.undo) > maxUndoDepth {
		d.undo = d.undo[1:]
	}
}

// InsertLine inserts a new line before the line at document index at.
// at == LineCount() appends to the end. Returns the new line's RecordID.
// Records an undo entry and clears the redo stack.
// Panics if at ∉ [0, LineCount()].
func (d *Document) InsertLine(at int, text []byte) RecordID {
	lc := d.LineCount()
	if at < 0 || at > lc {
		panic(fmt.Sprintf("editmodel: InsertLine index %d out of range [0, %d]", at, lc))
	}
	if d.nextID > maxRecordID {
		panic(fmt.Sprintf("editmodel: RecordID exhausted (max 0x%X)", maxRecordID))
	}
	id := d.nextID
	d.nextID++

	d.doInsert(at, id, text)

	// Journal this edit and clear redo (a fresh edit invalidates any pending redo).
	textCopy := make([]byte, len(text))
	copy(textCopy, text)
	d.pushUndo(editEntry{op: opInsert, at: at, id: id, text: textCopy})
	d.redo = d.redo[:0]

	return id
}

// DeleteLine deletes the line at document index at and returns its (now-dead,
// never-to-be-reused) RecordID. Records an undo entry and clears the redo stack.
// Panics if at ∉ [0, LineCount()).
func (d *Document) DeleteLine(at int) RecordID {
	lc := d.LineCount()
	if at < 0 || at >= lc {
		panic(fmt.Sprintf("editmodel: DeleteLine index %d out of range [0, %d)", at, lc))
	}

	id, text := d.doDelete(at)

	// Journal this edit and clear redo (a fresh edit invalidates any pending redo).
	d.pushUndo(editEntry{op: opDelete, at: at, id: id, text: text})
	d.redo = d.redo[:0]

	return id
}

// Undo reverses the most recent public edit. Returns true if an edit was
// undone, false if the undo stack is empty.
//
// For undo-of-insert: the line with e.id currently exists; its current index
// is found via IndexOf(e.id) — robust to any position drift since the original
// edit — then doDelete removes it.
//
// For undo-of-delete: the line is re-inserted at the recorded e.at index using
// the original e.id (preserving id-stability — design §3; anchors keyed on
// e.id remain valid). e.at is the correct re-insertion position because undo is
// strictly LIFO: the operation being inverted is always the most-recent boundary,
// so the position has not drifted since it was recorded.
func (d *Document) Undo() bool {
	if len(d.undo) == 0 {
		return false
	}
	e := d.undo[len(d.undo)-1]
	d.undo = d.undo[:len(d.undo)-1]

	switch e.op {
	case opInsert:
		// Invert an insert: find the line by id (robust to drift) and delete it.
		idx, ok := d.IndexOf(e.id)
		if !ok {
			panic(fmt.Sprintf("editmodel: Undo: id %d not found (should be live)", e.id))
		}
		d.doDelete(idx)
	case opDelete:
		// Invert a delete: re-insert at the recorded position with the same id.
		d.doInsert(e.at, e.id, e.text)
	}

	d.redo = append(d.redo, e)
	return true
}

// Redo re-applies the most recently undone edit. Returns true if a redo was
// available, false if the redo stack is empty.
//
// The redo stack is cleared whenever a fresh public edit (InsertLine or
// DeleteLine) is made.
//
// For redo-of-insert: re-insert at e.at with the original e.id (same LIFO
// position reasoning as Undo's delete direction — the redo is the top of the
// redo stack, so the position is current).
//
// For redo-of-delete: find the line by e.id via IndexOf and delete it.
func (d *Document) Redo() bool {
	if len(d.redo) == 0 {
		return false
	}
	e := d.redo[len(d.redo)-1]
	d.redo = d.redo[:len(d.redo)-1]

	switch e.op {
	case opInsert:
		// Re-apply an insert: re-insert at the recorded position with the same id.
		d.doInsert(e.at, e.id, e.text)
	case opDelete:
		// Re-apply a delete: find the line by id (robust to drift) and delete it.
		idx, ok := d.IndexOf(e.id)
		if !ok {
			panic(fmt.Sprintf("editmodel: Redo: id %d not found (should be live)", e.id))
		}
		d.doDelete(idx)
	}

	// Push back onto undo, respecting the cap.
	d.pushUndo(e)
	return true
}

// CanUndo reports whether there is at least one edit available to undo.
func (d *Document) CanUndo() bool { return len(d.undo) > 0 }

// CanRedo reports whether there is at least one undone edit available to redo.
func (d *Document) CanRedo() bool { return len(d.redo) > 0 }

// tryMerge attempts to merge the block at blockIdx with an adjacent block
// when the combined bytes fit within blockCapacityBytes.
func (d *Document) tryMerge(blockIdx int) {
	b := d.blocks[blockIdx]

	// Try the next neighbour first.
	if blockIdx+1 < len(d.blocks) {
		next := d.blocks[blockIdx+1]
		if b.bytes()+next.bytes() <= blockCapacityBytes {
			// Absorb next into b. Re-point only the lines that move.
			b.lines = append(b.lines, next.lines...)
			for i := range next.lines {
				d.loc[next.lines[i].id] = b
			}
			d.blocks = append(d.blocks[:blockIdx+1], d.blocks[blockIdx+2:]...)
			return
		}
	}

	// Try the previous neighbour.
	if blockIdx > 0 {
		prev := d.blocks[blockIdx-1]
		if prev.bytes()+b.bytes() <= blockCapacityBytes {
			// Absorb b into prev. Re-point only the lines that move.
			prev.lines = append(prev.lines, b.lines...)
			for i := range b.lines {
				d.loc[b.lines[i].id] = prev
			}
			d.blocks = append(d.blocks[:blockIdx], d.blocks[blockIdx+1:]...)
			return
		}
	}
}

// LineAt returns the RecordID and a copy of the text of line at document
// index index. Panics if out of range.
func (d *Document) LineAt(index int) (RecordID, []byte) {
	lc := d.LineCount()
	if index < 0 || index >= lc {
		panic(fmt.Sprintf("editmodel: LineAt index %d out of range [0, %d)", index, lc))
	}
	blockIdx, localIdx := d.blockAndLocalIndex(index)
	l := d.blocks[blockIdx].lines[localIdx]
	t := make([]byte, len(l.text))
	copy(t, l.text)
	return l.id, t
}

// IndexOf returns the current document-level line index of id and true if id
// is live, or (0, false) if id is dead or unknown. This is the goto-by-id
// operation (design §3).
//
// The block's position is found by scanning d.blocks for the pointer — a
// ≤O(blocks) scan over the small resident block list (design §2.5 accepts
// this ~50-entry scan).
func (d *Document) IndexOf(id RecordID) (int, bool) {
	b, ok := d.loc[id]
	if !ok {
		return 0, false
	}
	// Locate the block's position by scanning d.blocks for the pointer.
	offset := 0
	for _, blk := range d.blocks {
		if blk == b {
			// Found the block; now find the local index within it.
			for j, l := range b.lines {
				if l.id == id {
					return offset + j, true
				}
			}
			// Pointer found in d.blocks but id not in the block — corrupted.
			return 0, false
		}
		offset += len(blk.lines)
	}
	// Pointer not found in d.blocks — corrupted.
	return 0, false
}

// RenderedLine is one line as returned by Render.
type RenderedLine struct {
	ID   RecordID
	Text []byte
}

// Render returns up to count lines (as copies) starting at document index
// from. Bounds are clamped: if from is past the end or count ≤ 0, an empty
// slice is returned without panicking. This is intentional — Render is a
// display call, and clamping is more useful than panicking.
func (d *Document) Render(from, count int) []RenderedLine {
	lc := d.LineCount()
	if count <= 0 || from >= lc || from < 0 {
		return nil
	}
	end := from + count
	if end > lc {
		end = lc
	}
	result := make([]RenderedLine, 0, end-from)
	for i := from; i < end; i++ {
		id, text := d.LineAt(i)
		result = append(result, RenderedLine{ID: id, Text: text})
	}
	return result
}

// splitBlock splits the block at blockIdx into two blocks at approximately
// the half-line count. The second half becomes a new block at blockIdx+1.
// Only the lines that move to the right-half block are re-pointed in loc —
// O(½ block) updates per split (design §3.4). A single-line block is never split.
func (d *Document) splitBlock(blockIdx int) {
	b := d.blocks[blockIdx]
	if len(b.lines) < 2 {
		return
	}
	mid := len(b.lines) / 2

	// Create the new right-half block.
	right := &block{
		lines: make([]line, len(b.lines)-mid),
	}
	copy(right.lines, b.lines[mid:])
	b.lines = b.lines[:mid]

	// Insert the new block at blockIdx+1.
	d.blocks = append(d.blocks, nil)
	copy(d.blocks[blockIdx+2:], d.blocks[blockIdx+1:])
	d.blocks[blockIdx+1] = right

	// Re-point only the lines that moved into right. Lines remaining in b
	// already have loc pointing to b (unchanged).
	for _, l := range right.lines {
		d.loc[l.id] = right
	}
}

// checkInvariants verifies internal consistency of the Document. It is used
// by tests to catch bugs in split/merge/loc maintenance. Returns a descriptive
// error on the first violation found.
func (d *Document) checkInvariants() error {
	totalLines := 0
	// Build a set of valid block pointers for O(1) membership checks below.
	blockPtrs := make(map[*block]int, len(d.blocks)) // ptr → index
	for bi, b := range d.blocks {
		blockPtrs[b] = bi
	}

	seenIDs := make(map[RecordID]int) // id → block index

	for bi, b := range d.blocks {
		if len(b.lines) == 0 {
			return fmt.Errorf("block %d is empty (only allowed when document is empty)", bi)
		}
		for _, l := range b.lines {
			if prev, dup := seenIDs[l.id]; dup {
				return fmt.Errorf("duplicate RecordID %d in block %d and block %d", l.id, prev, bi)
			}
			seenIDs[l.id] = bi

			// loc must point to this block (via pointer).
			locBlk, ok := d.loc[l.id]
			if !ok {
				return fmt.Errorf("RecordID %d in block %d has no loc entry", l.id, bi)
			}
			if locBlk != b {
				locBI, inBlocks := blockPtrs[locBlk]
				if !inBlocks {
					return fmt.Errorf("RecordID %d: loc points to a block not in d.blocks (actually in block %d)", l.id, bi)
				}
				return fmt.Errorf("RecordID %d: loc points to block %d but actually in block %d", l.id, locBI, bi)
			}
		}
		// A block with ≥2 lines must not exceed capacity. A single
		// over-long line may legitimately exceed it.
		if len(b.lines) >= 2 && b.bytes() > blockCapacityBytes {
			return fmt.Errorf("block %d has %d lines and %d bytes > capacity %d",
				bi, len(b.lines), b.bytes(), blockCapacityBytes)
		}
		totalLines += len(b.lines)
	}

	// loc must have exactly one entry per live line (no stale entries).
	if len(d.loc) != totalLines {
		return fmt.Errorf("loc has %d entries but document has %d lines", len(d.loc), totalLines)
	}

	// Every loc entry must point to a block pointer present in d.blocks, and
	// that block must contain the id.
	for id, blk := range d.loc {
		bi, inBlocks := blockPtrs[blk]
		if !inBlocks {
			return fmt.Errorf("loc[%d] points to a block not present in d.blocks", id)
		}
		found := false
		for _, l := range d.blocks[bi].lines {
			if l.id == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("loc[%d] points to block %d but that block does not contain the id", id, bi)
		}
	}

	// Line count cross-check.
	if totalLines != d.LineCount() {
		return errors.New("totalLines from block iteration disagrees with LineCount()")
	}

	return nil
}
