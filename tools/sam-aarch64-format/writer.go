package format

import "encoding/binary"

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

// WriteDirective writes a DIRECTIVE record. operands is the
// already-encoded operand stream.
func (w *RecordWriter) WriteDirective(directiveID, operandCount byte, operands []byte) {
	payloadLen := 1 + 1 + len(operands)
	w.writeHeader(KindDirective, payloadLen)
	w.buf = append(w.buf, directiveID, operandCount)
	w.buf = append(w.buf, operands...)
}
