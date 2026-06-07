package format

import (
	"encoding/binary"
	"io"
)

// RecordWriter accumulates records into a byte slice in stream order.
type RecordWriter struct{ buf []byte }

func (w *RecordWriter) Bytes() []byte { return w.buf }

func (w *RecordWriter) writeHeader(kind RecordKind, payloadLen int) {
	w.buf = append(w.buf, byte(kind))
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], uint16(payloadLen))
	w.buf = append(w.buf, tmp[:]...)
}

// WriteComment writes a comment record. placement: 0=standalone, 1=trailing.
func (w *RecordWriter) WriteComment(placement byte, body []byte) {
	w.writeHeader(KindComment, 1+len(body))
	w.buf = append(w.buf, placement)
	w.buf = append(w.buf, body...)
}

// WriteLitData writes a LIT_DATA record: a run of constant data from a
// single numeric data directive, stored as its raw assembled bytes. The
// leading directiveID byte records which directive produced the bytes
// (.byte/.short/.hword/.word/.quad) so the disassembler round-trips the
// source spelling.
func (w *RecordWriter) WriteLitData(directiveID byte, raw []byte) {
	w.writeHeader(KindLitData, 1+len(raw))
	w.buf = append(w.buf, directiveID)
	w.buf = append(w.buf, raw...)
}

// WriteInsnRun writes a KindInsnRun record (compact `.tbn` v2 instruction
// overlay). mode 0 packs each element's bare 4-byte word; mode 1 writes
// each element as [base_word][patch_count][slot,expr_len,expr]…. Panics on
// programming errors (a mode-0 element carrying patches, a patch count or
// expr length exceeding the u8 wire field) — those are compaction bugs.
func (w *RecordWriter) WriteInsnRun(mode byte, elements []InsnElement) {
	payload := []byte{mode}
	switch mode {
	case 0:
		for _, el := range elements {
			if len(el.Patches) != 0 {
				panic("WriteInsnRun: mode 0 element carries patches")
			}
			payload = appendU32(payload, el.BaseWord)
		}
	case 1:
		for _, el := range elements {
			payload = appendU32(payload, el.BaseWord)
			if len(el.Patches) > 255 {
				panic("WriteInsnRun: patch_count exceeds 255")
			}
			payload = append(payload, byte(len(el.Patches)))
			for _, p := range el.Patches {
				if len(p.Expr) > 255 {
					panic("WriteInsnRun: patch expr_len exceeds 255")
				}
				payload = append(payload, p.Slot, byte(len(p.Expr)))
				payload = append(payload, p.Expr...)
			}
		}
	default:
		panic("WriteInsnRun: unknown mode")
	}
	w.writeHeader(KindInsnRun, len(payload))
	w.buf = append(w.buf, payload...)
}

func appendU32(buf []byte, v uint32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	return append(buf, tmp[:]...)
}

// WriteDirective writes a DIRECTIVE record. operands is the
// already-encoded operand stream.
func (w *RecordWriter) WriteDirective(directiveID, operandCount byte, operands []byte) {
	payloadLen := 1 + 1 + len(operands)
	w.writeHeader(KindDirective, payloadLen)
	w.buf = append(w.buf, directiveID, operandCount)
	w.buf = append(w.buf, operands...)
}

// WriteFile serialises a complete .tbn file to w: magic, version, flags,
// the symbol table's name list, the header label/local tables, and the
// record stream. The two header tables sit AFTER the name table and BEFORE
// the record stream (a label row references a name_id into the name table).
//
// labels/locals carry resolved position offsets (= symbolVMA - OriginVMA);
// the symbolic `.tbn` from text2bin passes both nil (no resolved PCs yet).
// WriteFile sorts copies of the rows by offset ascending (ties by
// NameID/Digit ascending) so the increasing-offset delta-varint invariant
// always holds regardless of the caller's row order.
func WriteFile(w io.Writer, st *SymbolTable, labels []LabelRow, locals []LocalRow, records []byte) error {
	if _, err := w.Write(Magic[:]); err != nil {
		return err
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint16(hdr[0:2], Version)
	binary.LittleEndian.PutUint16(hdr[2:4], Flags)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}

	names := st.Names()
	var cnt [2]byte
	binary.LittleEndian.PutUint16(cnt[:], uint16(len(names)))
	if _, err := w.Write(cnt[:]); err != nil {
		return err
	}
	// Front-code each entry against the PREVIOUS name (encounter order, no
	// sort — ids are wired through every PUSH_SYM patch and the header
	// tables, so the order must stay stable). An entry is
	// [shared_prefix_len uvarint][suffix_len uvarint][suffix bytes]; decode
	// copies `shared` bytes from the prior name and appends the suffix.
	var prev string
	var tmp [binary.MaxVarintLen64]byte
	for _, n := range names {
		shared := commonPrefixLen(prev, n)
		suffix := n[shared:]
		k := binary.PutUvarint(tmp[:], uint64(shared))
		if _, err := w.Write(tmp[:k]); err != nil {
			return err
		}
		k = binary.PutUvarint(tmp[:], uint64(len(suffix)))
		if _, err := w.Write(tmp[:k]); err != nil {
			return err
		}
		if _, err := w.Write([]byte(suffix)); err != nil {
			return err
		}
		prev = n
	}

	tables := writeLabelTable(nil, labels)
	tables = writeLocalTable(tables, locals)
	if _, err := w.Write(tables); err != nil {
		return err
	}

	if _, err := w.Write(records); err != nil {
		return err
	}
	return nil
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
