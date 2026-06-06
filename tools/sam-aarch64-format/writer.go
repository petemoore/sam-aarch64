package format

import (
	"encoding/binary"
	"io"
)

// RecordWriter accumulates records into a byte slice in stream order.
type RecordWriter struct{ buf []byte }

func (w *RecordWriter) Bytes() []byte { return w.buf }
func (w *RecordWriter) Reset()        { w.buf = w.buf[:0] }

func (w *RecordWriter) writeHeader(kind RecordKind, payloadLen int) {
	w.buf = append(w.buf, byte(kind))
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], uint16(payloadLen))
	w.buf = append(w.buf, tmp[:]...)
}

func (w *RecordWriter) WriteLabelDef(symID uint16) {
	w.writeHeader(KindLabelDef, 2)
	w.buf = append(w.buf, byte(symID), byte(symID>>8))
}

func (w *RecordWriter) WriteLocalDef(digit byte) {
	w.writeHeader(KindLocalDef, 1)
	w.buf = append(w.buf, digit)
}

// WriteComment writes a comment record. placement: 0=standalone, 1=trailing.
func (w *RecordWriter) WriteComment(placement byte, body []byte) {
	w.writeHeader(KindComment, 1+len(body))
	w.buf = append(w.buf, placement)
	w.buf = append(w.buf, body...)
}

// WriteInst writes an INST record. operands is the already-encoded
// operand stream produced by OperandWriter.
func (w *RecordWriter) WriteInst(mnemonicID uint16, operandCount byte, operands []byte) {
	payloadLen := 2 + 1 + len(operands)
	w.writeHeader(KindInst, payloadLen)
	w.buf = append(w.buf, byte(mnemonicID), byte(mnemonicID>>8), operandCount)
	w.buf = append(w.buf, operands...)
}

// WriteLitInsts writes a LIT_INSTS record: a run of fully-literal
// instructions stored as their assembled little-endian words. The run
// length must be 1..255 (the caller splits longer runs into successive
// records); WriteLitInsts panics on a zero or over-long run, which is a
// programming error in the compaction pass.
func (w *RecordWriter) WriteLitInsts(words []uint32) {
	if len(words) == 0 || len(words) > 255 {
		panic("WriteLitInsts: run length must be 1..255")
	}
	payloadLen := 1 + 4*len(words)
	w.writeHeader(KindLitInsts, payloadLen)
	w.buf = append(w.buf, byte(len(words)))
	var tmp [4]byte
	for _, word := range words {
		binary.LittleEndian.PutUint32(tmp[:], word)
		w.buf = append(w.buf, tmp[:]...)
	}
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

// WriteRaw writes a record with an already-encoded payload verbatim.
// Used by the compaction pass to pass non-literal records through
// unchanged (the payload is the Record.Raw slice from the reader).
func (w *RecordWriter) WriteRaw(kind RecordKind, payload []byte) {
	w.writeHeader(kind, len(payload))
	w.buf = append(w.buf, payload...)
}

// WriteDirective writes a DIRECTIVE record. operands is the
// already-encoded operand stream.
func (w *RecordWriter) WriteDirective(directiveID, operandCount byte, operands []byte) {
	payloadLen := 1 + 1 + len(operands)
	w.writeHeader(KindDirective, payloadLen)
	w.buf = append(w.buf, directiveID, operandCount)
	w.buf = append(w.buf, operands...)
}

// WriteFile serialises a complete .tbn file to w: magic, version,
// flags, the symbol table's name list, and the record stream.
func WriteFile(w io.Writer, st *SymbolTable, records []byte) error {
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
	for _, n := range names {
		var ln [2]byte
		binary.LittleEndian.PutUint16(ln[:], uint16(len(n)))
		if _, err := w.Write(ln[:]); err != nil {
			return err
		}
		if _, err := w.Write([]byte(n)); err != nil {
			return err
		}
	}

	if _, err := w.Write(records); err != nil {
		return err
	}
	return nil
}
