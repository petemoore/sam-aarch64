package format

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// The editor region (compact `.tbn` v2, M8 / i39b-2) holds the data the SAM
// assembler provably never reads — name strings, `.global` flags, and comment
// text — relocated out of the assembler-facing region (header tables + record
// stream) so a future build can leave it on disk / reuse its RAM (item i40).
// It sits at the tail of the file, starting at the header's
// editor_region_offset; the assembler stops its record walk at that boundary
// and never maps these bytes. Only the renderer / editor reads it, to put the
// label names, `.global` directives, and comments back when round-tripping to
// text.
//
// Layout (in order, at editor_region_offset):
//
//	name table     [count u16][front-coded entry]…   (moved here from the front)
//	global flags   [count u16][name_id u16]…          (which symbols were .global)
//	comment sidecar[count u16][anchor_delta uvarint][placement u8][len u16][text]…
//
// A name entry is front-coded against the PREVIOUS name (encounter order, no
// sort — ids stay stable): [shared_prefix_len uvarint][suffix_len uvarint]
// [suffix bytes]; decode copies `shared` bytes from the prior name and appends
// the suffix.

// CommentRow is one entry of the comment sidecar: a source comment relocated
// out of the record stream. Anchor is the output PC (byte offset from the
// origin VMA, ≥ 0) the comment attaches to — the renderer emits it when its PC
// walk reaches that offset. Placement is 0 = standalone (own line) / 1 =
// trailing (same line as the preceding statement), matching the COMMENT record.
type CommentRow struct {
	Anchor    int64
	Placement byte
	Body      []byte
}

// BlankRun is one entry of the sidecar (i78 source-structure preservation): a
// run of consecutive blank source lines. Anchor is the output PC of the next
// statement (the same anchor a comment preceding that statement uses); RunLen
// is the number of blank lines (≥ 1). A blank run carries no text and no
// placement — a blank line is always standalone. It is distinct from a textless
// `//` comment (a CommentRow with an empty Body): they render differently and
// the kind discriminator keeps them apart on disk.
type BlankRun struct {
	Anchor int64
	RunLen uint32
}

// SidecarKind discriminates the kinds of sidecar row on disk (the leading
// kind u8, present when the header carries FlagTaggedSidecar).
type SidecarKind byte

const (
	SidecarComment SidecarKind = 0 // a CommentRow
	SidecarBlank   SidecarKind = 1 // a BlankRun
	// 2+ reserved (e.g. i78 part-c indentation rows).
)

// SidecarRow is one tagged entry of the editor-region sidecar in source order.
// Exactly one of Comment / Blank is meaningful, selected by Kind. Carrying both
// kinds in one ordered slice preserves the source order of interleaved blank
// runs and comments at a shared anchor (design §2/§3.2: stored order is source
// order, and the renderer emits in stored order).
type SidecarRow struct {
	Kind    SidecarKind
	Comment CommentRow
	Blank   BlankRun
}

// Anchor returns the row's output-PC anchor regardless of kind.
func (r SidecarRow) Anchor() int64 {
	if r.Kind == SidecarBlank {
		return r.Blank.Anchor
	}
	return r.Comment.Anchor
}

// commonPrefixLen returns the number of leading bytes a and b share.
func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// appendNameTable writes [count u16] then front-coded name entries.
func appendNameTable(buf []byte, names []string) []byte {
	var cnt [2]byte
	binary.LittleEndian.PutUint16(cnt[:], uint16(len(names)))
	buf = append(buf, cnt[:]...)
	var prev string
	var tmp [binary.MaxVarintLen64]byte
	for _, n := range names {
		shared := commonPrefixLen(prev, n)
		suffix := n[shared:]
		k := binary.PutUvarint(tmp[:], uint64(shared))
		buf = append(buf, tmp[:k]...)
		k = binary.PutUvarint(tmp[:], uint64(len(suffix)))
		buf = append(buf, tmp[:k]...)
		buf = append(buf, suffix...)
		prev = n
	}
	return buf
}

// readNameTable parses a front-coded name table at buf[pos], returning the
// reconstructed full names and the position just past the table.
func readNameTable(buf []byte, pos int) ([]string, int, error) {
	if pos+2 > len(buf) {
		return nil, pos, fmt.Errorf("file: truncated name table count")
	}
	count := int(binary.LittleEndian.Uint16(buf[pos:]))
	pos += 2
	names := make([]string, count)
	var prev string
	for i := 0; i < count; i++ {
		shared, ns := binary.Uvarint(buf[pos:])
		if ns <= 0 {
			return nil, pos, fmt.Errorf("file: truncated name shared-prefix-len at %d", i)
		}
		pos += ns
		slen, nl := binary.Uvarint(buf[pos:])
		if nl <= 0 {
			return nil, pos, fmt.Errorf("file: truncated name suffix-len at %d", i)
		}
		pos += nl
		if int(shared) > len(prev) {
			return nil, pos, fmt.Errorf("file: name %d shared-prefix-len %d exceeds previous name length %d", i, shared, len(prev))
		}
		if pos+int(slen) > len(buf) {
			return nil, pos, fmt.Errorf("file: truncated name body at %d", i)
		}
		name := prev[:shared] + string(buf[pos:pos+int(slen)])
		pos += int(slen)
		names[i] = name
		prev = name
	}
	return names, pos, nil
}

// appendGlobalFlags writes [count u16] then count name_id u16 LE entries. The
// ids are sorted ascending for a deterministic file.
func appendGlobalFlags(buf []byte, ids []uint16) []byte {
	rows := append([]uint16(nil), ids...)
	sort.Slice(rows, func(i, j int) bool { return rows[i] < rows[j] })
	var cnt [2]byte
	binary.LittleEndian.PutUint16(cnt[:], uint16(len(rows)))
	buf = append(buf, cnt[:]...)
	var tmp [2]byte
	for _, id := range rows {
		binary.LittleEndian.PutUint16(tmp[:], id)
		buf = append(buf, tmp[:]...)
	}
	return buf
}

// readGlobalFlags parses the global-flags table at buf[pos].
func readGlobalFlags(buf []byte, pos int) ([]uint16, int, error) {
	if pos+2 > len(buf) {
		return nil, pos, fmt.Errorf("file: truncated global-flags count")
	}
	count := int(binary.LittleEndian.Uint16(buf[pos:]))
	pos += 2
	if count == 0 {
		return nil, pos, nil
	}
	ids := make([]uint16, count)
	for i := 0; i < count; i++ {
		if pos+2 > len(buf) {
			return nil, pos, fmt.Errorf("file: truncated global-flag id at %d", i)
		}
		ids[i] = binary.LittleEndian.Uint16(buf[pos:])
		pos += 2
	}
	return ids, pos, nil
}

// appendSidecar writes [count u16] then, per row (sorted by anchor ascending,
// ties stable in source order so interleaved blank runs and comments keep their
// source order), a tagged row. Anchors are delta-coded from the previous row's
// anchor (first row's previous is 0); since anchors are ≥ 0 and sorted ascending
// the deltas are unsigned.
//
// A tagged row is [kind u8][anchor_delta uvarint] then the kind-specific tail:
//
//	kind 0 (comment):  [placement u8][len u16][text]
//	kind 1 (blank-run): [run_len uvarint]
//
// The kind byte is always written here (the writer always sets
// FlagTaggedSidecar). A legacy untagged file omits it; readSidecar parses both
// shapes per the tagged flag.
func appendSidecar(buf []byte, rows []SidecarRow) []byte {
	sorted := append([]SidecarRow(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Anchor() < sorted[j].Anchor() })
	var cnt [2]byte
	binary.LittleEndian.PutUint16(cnt[:], uint16(len(sorted)))
	buf = append(buf, cnt[:]...)
	var prev int64
	var tmp [binary.MaxVarintLen64]byte
	for _, r := range sorted {
		buf = append(buf, byte(r.Kind))
		delta := r.Anchor() - prev
		if delta < 0 {
			delta = 0 // defensive: anchors are sorted, so this never fires
		}
		n := binary.PutUvarint(tmp[:], uint64(delta))
		buf = append(buf, tmp[:n]...)
		switch r.Kind {
		case SidecarBlank:
			n = binary.PutUvarint(tmp[:], uint64(r.Blank.RunLen))
			buf = append(buf, tmp[:n]...)
		default: // SidecarComment
			buf = append(buf, r.Comment.Placement)
			var l [2]byte
			binary.LittleEndian.PutUint16(l[:], uint16(len(r.Comment.Body)))
			buf = append(buf, l[:]...)
			buf = append(buf, r.Comment.Body...)
		}
		prev = r.Anchor()
	}
	return buf
}

// readSidecar parses the sidecar at buf[pos], accumulating anchor deltas back
// into absolute offsets. tagged selects the row shape: when true each row leads
// with a kind u8 (comment / blank-run); when false (a legacy file without
// FlagTaggedSidecar) every row is an untagged comment.
func readSidecar(buf []byte, pos int, tagged bool) ([]SidecarRow, int, error) {
	if pos+2 > len(buf) {
		return nil, pos, fmt.Errorf("file: truncated sidecar count")
	}
	count := int(binary.LittleEndian.Uint16(buf[pos:]))
	pos += 2
	if count == 0 {
		return nil, pos, nil
	}
	rows := make([]SidecarRow, count)
	var prev int64
	for i := 0; i < count; i++ {
		kind := SidecarComment
		if tagged {
			if pos+1 > len(buf) {
				return nil, pos, fmt.Errorf("file: truncated sidecar kind at %d", i)
			}
			kind = SidecarKind(buf[pos])
			pos++
		}
		delta, n := binary.Uvarint(buf[pos:])
		if n <= 0 {
			return nil, pos, fmt.Errorf("file: truncated sidecar anchor delta at %d", i)
		}
		pos += n
		prev += int64(delta)
		switch kind {
		case SidecarBlank:
			runLen, m := binary.Uvarint(buf[pos:])
			if m <= 0 {
				return nil, pos, fmt.Errorf("file: truncated blank-run length at %d", i)
			}
			pos += m
			rows[i] = SidecarRow{Kind: SidecarBlank, Blank: BlankRun{Anchor: prev, RunLen: uint32(runLen)}}
		case SidecarComment:
			if pos+1 > len(buf) {
				return nil, pos, fmt.Errorf("file: truncated comment placement at %d", i)
			}
			placement := buf[pos]
			pos++
			if pos+2 > len(buf) {
				return nil, pos, fmt.Errorf("file: truncated comment len at %d", i)
			}
			blen := int(binary.LittleEndian.Uint16(buf[pos:]))
			pos += 2
			if pos+blen > len(buf) {
				return nil, pos, fmt.Errorf("file: truncated comment body at %d", i)
			}
			body := append([]byte(nil), buf[pos:pos+blen]...)
			pos += blen
			rows[i] = SidecarRow{Kind: SidecarComment, Comment: CommentRow{Anchor: prev, Placement: placement, Body: body}}
		default:
			return nil, pos, fmt.Errorf("file: unknown sidecar kind %d at row %d", kind, i)
		}
	}
	return rows, pos, nil
}

// appendEditorRegion writes the whole editor region (name table, global flags,
// sidecar) to buf.
func appendEditorRegion(buf []byte, names []string, globals []uint16, sidecar []SidecarRow) []byte {
	buf = appendNameTable(buf, names)
	buf = appendGlobalFlags(buf, globals)
	buf = appendSidecar(buf, sidecar)
	return buf
}

// readEditorRegion parses the editor region (name table, global flags, sidecar)
// starting at buf[pos]. tagged comes from the header FlagTaggedSidecar bit.
func readEditorRegion(buf []byte, pos int, tagged bool) (names []string, globals []uint16, sidecar []SidecarRow, _ int, err error) {
	names, pos, err = readNameTable(buf, pos)
	if err != nil {
		return nil, nil, nil, pos, err
	}
	globals, pos, err = readGlobalFlags(buf, pos)
	if err != nil {
		return nil, nil, nil, pos, err
	}
	sidecar, pos, err = readSidecar(buf, pos, tagged)
	if err != nil {
		return nil, nil, nil, pos, err
	}
	return names, globals, sidecar, pos, nil
}
