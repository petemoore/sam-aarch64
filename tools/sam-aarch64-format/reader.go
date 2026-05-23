package format

import (
	"encoding/binary"
	"fmt"
)

// Record is a decoded statement-stream record. Only the fields
// appropriate to Kind are populated.
type Record struct {
	Kind RecordKind

	SymbolID     uint16
	Digit        byte
	Placement    byte
	Body         []byte
	MnemonicID   uint16
	DirectiveID  byte
	OperandCount byte
	Operands     []byte

	Raw []byte
}

type RecordReader struct {
	buf []byte
	pos int
}

func NewRecordReader(buf []byte) *RecordReader {
	return &RecordReader{buf: buf}
}

func (r *RecordReader) AtEnd() bool { return r.pos >= len(r.buf) }

func (r *RecordReader) Next() (Record, error) {
	if r.AtEnd() {
		return Record{}, fmt.Errorf("record: read past end")
	}
	if r.pos+3 > len(r.buf) {
		return Record{}, fmt.Errorf("record: truncated header at offset %d", r.pos)
	}
	kind := RecordKind(r.buf[r.pos])
	length := int(binary.LittleEndian.Uint16(r.buf[r.pos+1:]))
	r.pos += 3
	if r.pos+length > len(r.buf) {
		return Record{}, fmt.Errorf("record: truncated payload at offset %d (need %d, have %d)",
			r.pos, length, len(r.buf)-r.pos)
	}
	payload := r.buf[r.pos : r.pos+length]
	r.pos += length

	rec := Record{Kind: kind, Raw: payload}
	switch kind {
	case KindLabelDef:
		if len(payload) != 2 {
			return rec, fmt.Errorf("LABEL_DEF: payload len = %d, want 2", len(payload))
		}
		rec.SymbolID = binary.LittleEndian.Uint16(payload)
	case KindLocalDef:
		if len(payload) != 1 {
			return rec, fmt.Errorf("LOCAL_DEF: payload len = %d, want 1", len(payload))
		}
		rec.Digit = payload[0]
	case KindComment:
		if len(payload) < 1 {
			return rec, fmt.Errorf("COMMENT: payload too short")
		}
		rec.Placement = payload[0]
		rec.Body = payload[1:]
	case KindInst:
		if len(payload) < 3 {
			return rec, fmt.Errorf("INST: payload too short")
		}
		rec.MnemonicID = binary.LittleEndian.Uint16(payload)
		rec.OperandCount = payload[2]
		rec.Operands = payload[3:]
	case KindDirective:
		if len(payload) < 2 {
			return rec, fmt.Errorf("DIRECTIVE: payload too short")
		}
		rec.DirectiveID = payload[0]
		rec.OperandCount = payload[1]
		rec.Operands = payload[2:]
	}
	return rec, nil
}
