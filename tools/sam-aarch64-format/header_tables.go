package format

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// LabelRow is one entry of the compact `.tbn` v2 header label table: a
// named position-label resolved to a byte offset from the origin VMA
// (offset = symbolVMA - OriginVMA, always ≥ 0). NameID indexes the name
// table. See docs/plans/2026-06-08-i39-phase1-instruction-overlay-plan.md
// §2.4.
type LabelRow struct {
	NameID uint16
	Offset int64
}

// LocalRow is one entry of the compact `.tbn` v2 header local-label table:
// one row per numeric-local definition site (1:/2:/…), resolved to a byte
// offset from the origin VMA. The same digit may repeat (multiple def
// sites), so pass 2's forward/backward (Nf/Nb) resolution keeps the full
// ordered list per digit.
type LocalRow struct {
	Digit  byte
	Offset int64
}

// writeLabelTable serialises the header label table: [count u16 LE] then
// count rows of [name_id u16 LE][offset_delta uvarint]. Rows are sorted by
// offset ascending (ties by NameID ascending) and offsets are stored as
// deltas (offset = prevOffset + delta, first row's prev = 0). The input
// slice is copied before sorting, so the caller's slice is untouched and
// the on-disk invariant holds regardless of caller order.
func writeLabelTable(buf []byte, labels []LabelRow) []byte {
	rows := append([]LabelRow(nil), labels...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Offset != rows[j].Offset {
			return rows[i].Offset < rows[j].Offset
		}
		return rows[i].NameID < rows[j].NameID
	})
	var cnt [2]byte
	binary.LittleEndian.PutUint16(cnt[:], uint16(len(rows)))
	buf = append(buf, cnt[:]...)
	var prev int64
	var tmp [binary.MaxVarintLen64]byte
	for _, r := range rows {
		var id [2]byte
		binary.LittleEndian.PutUint16(id[:], r.NameID)
		buf = append(buf, id[:]...)
		delta := r.Offset - prev
		n := binary.PutUvarint(tmp[:], uint64(delta))
		buf = append(buf, tmp[:n]...)
		prev = r.Offset
	}
	return buf
}

// writeLocalTable serialises the header local-label table: [count u16 LE]
// then count rows of [digit u8][offset_delta uvarint]. Rows are sorted by
// offset ascending (ties by digit ascending); deltas accumulate as for the
// label table, with an independent accumulator.
func writeLocalTable(buf []byte, locals []LocalRow) []byte {
	rows := append([]LocalRow(nil), locals...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Offset != rows[j].Offset {
			return rows[i].Offset < rows[j].Offset
		}
		return rows[i].Digit < rows[j].Digit
	})
	var cnt [2]byte
	binary.LittleEndian.PutUint16(cnt[:], uint16(len(rows)))
	buf = append(buf, cnt[:]...)
	var prev int64
	var tmp [binary.MaxVarintLen64]byte
	for _, r := range rows {
		buf = append(buf, r.Digit)
		delta := r.Offset - prev
		n := binary.PutUvarint(tmp[:], uint64(delta))
		buf = append(buf, tmp[:n]...)
		prev = r.Offset
	}
	return buf
}

// readLabelTable parses a header label table starting at buf[pos], returning
// the rows and the position just past the table. Deltas are accumulated back
// into absolute offsets.
func readLabelTable(buf []byte, pos int) ([]LabelRow, int, error) {
	if pos+2 > len(buf) {
		return nil, pos, fmt.Errorf("file: truncated label table count")
	}
	count := int(binary.LittleEndian.Uint16(buf[pos:]))
	pos += 2
	rows := make([]LabelRow, count)
	var prev int64
	for i := 0; i < count; i++ {
		if pos+2 > len(buf) {
			return nil, pos, fmt.Errorf("file: truncated label name_id at row %d", i)
		}
		nameID := binary.LittleEndian.Uint16(buf[pos:])
		pos += 2
		delta, n := binary.Uvarint(buf[pos:])
		if n <= 0 {
			return nil, pos, fmt.Errorf("file: truncated label offset delta at row %d", i)
		}
		pos += n
		prev += int64(delta)
		rows[i] = LabelRow{NameID: nameID, Offset: prev}
	}
	return rows, pos, nil
}

// readLocalTable parses a header local-label table starting at buf[pos],
// returning the rows and the position just past the table.
func readLocalTable(buf []byte, pos int) ([]LocalRow, int, error) {
	if pos+2 > len(buf) {
		return nil, pos, fmt.Errorf("file: truncated local table count")
	}
	count := int(binary.LittleEndian.Uint16(buf[pos:]))
	pos += 2
	rows := make([]LocalRow, count)
	var prev int64
	for i := 0; i < count; i++ {
		if pos+1 > len(buf) {
			return nil, pos, fmt.Errorf("file: truncated local digit at row %d", i)
		}
		digit := buf[pos]
		pos++
		delta, n := binary.Uvarint(buf[pos:])
		if n <= 0 {
			return nil, pos, fmt.Errorf("file: truncated local offset delta at row %d", i)
		}
		pos += n
		prev += int64(delta)
		rows[i] = LocalRow{Digit: digit, Offset: prev}
	}
	return rows, pos, nil
}
