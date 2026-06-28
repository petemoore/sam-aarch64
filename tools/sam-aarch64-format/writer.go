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

// WriteInsnRun writes a KindInsnRun record (compact `.tbn` instruction
// overlay). mode 0 packs each element's bare 4-byte word; mode 1 writes
// each element as [base_word][patch_count][packed patches…], where a patch
// is one packed header byte [slot:4|expr_len:4] followed by the expr bytes
// — expr_len nibble 15 escapes to a real-length u8 (i39c slot-byte
// packing). Panics on programming errors (a mode-0 element carrying
// patches, a patch count or expr length exceeding the u8 wire field, a
// slot outside the 4-bit header field) — those are compaction bugs.
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
				if p.Slot == 0 || p.Slot > 15 {
					panic("WriteInsnRun: patch slot outside the 4-bit header field")
				}
				if len(p.Expr) > 255 {
					panic("WriteInsnRun: patch expr_len exceeds 255")
				}
				if len(p.Expr) < exprLenEscape {
					payload = append(payload, p.Slot<<4|byte(len(p.Expr)))
				} else {
					payload = append(payload, p.Slot<<4|exprLenEscape, byte(len(p.Expr)))
				}
				payload = append(payload, p.Expr...)
			}
		}
	default:
		panic("WriteInsnRun: unknown mode")
	}
	w.writeHeader(KindInsnRun, len(payload))
	w.buf = append(w.buf, payload...)
}

// exprLenEscape is the mode-1 patch-header expr_len nibble value marking
// "real u8 length follows" (§7.2). Inline lengths are 0–14.
const exprLenEscape = 0x0F

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

// WriteFile serialises a complete compact `.tbn` v2 file to w. The file
// splits into an assembler-facing region and a trailing editor region
// (M8 / i39b-2):
//
//	magic "SA64" · version u16 · flags u16
//	editor_region_offset u32 LE       — section index: where the editor region starts
//	── assembler-facing region ──
//	label table · local table         — header position tables (§2.4)
//	record stream                     — INSN_RUN / LIT_DATA / DIRECTIVE
//	── editor region (at editor_region_offset) ──
//	name table · global flags · sidecar   (editor_region.go)
//
// The name strings, `.global` flags, comments, and blank-line runs are data the
// assembler never reads (it resolves symbols by name_id via the header tables),
// so they move to the tail; the assembler stops its record walk at
// editor_region_offset and never maps the editor region. The label/local rows
// only carry name_ids, so the name strings can follow the records.
//
// labels/locals carry resolved position offsets (= symbolVMA - OriginVMA);
// globals is the list of name_ids that were `.global`; sidecar is the relocated
// editor-region sidecar (comments + blank-line runs, in source order). WriteFile
// stable-sorts the sidecar rows by anchor into their on-disk order, so the
// caller's slice order only matters for ties at a shared anchor (which it
// preserves — that is the source order).
func WriteFile(w io.Writer, st *SymbolTable, labels []LabelRow, locals []LocalRow, records []byte, globals []uint16, sidecar []SidecarRow) error {
	tables := writeLabelTable(nil, labels)
	tables = writeLocalTable(tables, locals)

	// editor_region_offset = bytes before the editor region:
	// magic(4)+version(2)+flags(2)+section_index(4) + tables + records.
	const headerLen = 4 + 2 + 2 + 4
	editorOffset := headerLen + len(tables) + len(records)

	hdr := make([]byte, 0, headerLen)
	hdr = append(hdr, Magic[:]...)
	var u16 [2]byte
	binary.LittleEndian.PutUint16(u16[:], Version)
	hdr = append(hdr, u16[:]...)
	binary.LittleEndian.PutUint16(u16[:], Flags)
	hdr = append(hdr, u16[:]...)
	var u32 [4]byte
	binary.LittleEndian.PutUint32(u32[:], uint32(editorOffset))
	hdr = append(hdr, u32[:]...)

	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if _, err := w.Write(tables); err != nil {
		return err
	}
	if _, err := w.Write(records); err != nil {
		return err
	}
	editor := appendEditorRegion(nil, st.Names(), globals, sidecar)
	if _, err := w.Write(editor); err != nil {
		return err
	}
	return nil
}
