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

	// LitCount and LitWords are populated for KindLitInsts records.
	// LitWords is the raw little-endian word stream (4*LitCount bytes),
	// ready to memcpy straight to OUT.
	LitCount byte
	LitWords []byte

	// LitDataDirID and LitData are populated for KindLitData records.
	// LitData is the raw assembled little-endian data bytes (ready to
	// memcpy to OUT); LitDataDirID is the source directive's ID.
	LitDataDirID byte
	LitData      []byte

	// Mode and Elements are populated for KindInsnRun records. Mode is the
	// run's element encoding (0 = packed literal words, 1 = base+patch
	// overlay); Elements is the decoded element list (mode 0 yields
	// patch-free elements).
	Mode     byte
	Elements []InsnElement

	Raw []byte
}

// InsnElement is one instruction in a KindInsnRun record: its assembled
// base word (with relocated bitfields zeroed in the overlay case) plus zero
// or more patches. A patch-free element is a fully-literal instruction.
type InsnElement struct {
	BaseWord uint32
	Patches  []InsnPatch
}

// InsnPatch is one relocated field of an overlay instruction: the slot id
// (see aarch64enc.FoldSlot) selecting the bit-range and fold-rule, and the
// expression bytecode whose evaluated value folds into that field.
type InsnPatch struct {
	Slot byte
	Expr []byte
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
	case KindComment:
		if len(payload) < 1 {
			return rec, fmt.Errorf("COMMENT: payload too short")
		}
		rec.Placement = payload[0]
		rec.Body = payload[1:]
	case KindDirective:
		if len(payload) < 2 {
			return rec, fmt.Errorf("DIRECTIVE: payload too short")
		}
		rec.DirectiveID = payload[0]
		rec.OperandCount = payload[1]
		rec.Operands = payload[2:]
	case KindLitData:
		if len(payload) < 1 {
			return rec, fmt.Errorf("LIT_DATA: payload too short")
		}
		rec.LitDataDirID = payload[0]
		rec.LitData = payload[1:]
	case KindInsnRun:
		if len(payload) < 1 {
			return rec, fmt.Errorf("INSN_RUN: payload too short")
		}
		rec.Mode = payload[0]
		body := payload[1:]
		switch rec.Mode {
		case 0:
			if len(body)%4 != 0 {
				return rec, fmt.Errorf("INSN_RUN mode 0: payload %d not a multiple of 4", len(body))
			}
			for i := 0; i < len(body); i += 4 {
				rec.Elements = append(rec.Elements, InsnElement{
					BaseWord: binary.LittleEndian.Uint32(body[i:]),
				})
			}
		case 1:
			pos := 0
			for pos < len(body) {
				if pos+5 > len(body) {
					return rec, fmt.Errorf("INSN_RUN mode 1: truncated element at %d", pos)
				}
				el := InsnElement{BaseWord: binary.LittleEndian.Uint32(body[pos:])}
				pos += 4
				patchCount := int(body[pos])
				pos++
				for p := 0; p < patchCount; p++ {
					if pos+2 > len(body) {
						return rec, fmt.Errorf("INSN_RUN mode 1: truncated patch header at %d", pos)
					}
					slot := body[pos]
					exprLen := int(body[pos+1])
					pos += 2
					if pos+exprLen > len(body) {
						return rec, fmt.Errorf("INSN_RUN mode 1: truncated patch expr at %d", pos)
					}
					el.Patches = append(el.Patches, InsnPatch{Slot: slot, Expr: body[pos : pos+exprLen]})
					pos += exprLen
				}
				rec.Elements = append(rec.Elements, el)
			}
		default:
			return rec, fmt.Errorf("INSN_RUN: unknown mode %d", rec.Mode)
		}
	}
	return rec, nil
}

// File is a decoded .tbn file.
type File struct {
	Version uint16
	Flags   uint16
	Names   []string
	// Labels and Locals are the header position tables (§2.4): named
	// position-labels and numeric-local def sites resolved to byte offsets
	// from the origin VMA. Empty for an in-memory symbolic IR built by the
	// front-end.
	Labels []LabelRow
	Locals []LocalRow
	// Records is the decoded statement stream. For a File built by the
	// front-end it is the in-memory symbolic IR (KindInst/KindLabelDef/
	// KindLocalDef/KindDirective/KindComment); for one read from disk via
	// ReadFile it is the overlay stream (KindInsnRun/KindLitData/KindDirective/
	// KindComment).
	Records []Record
}

func ReadFile(buf []byte) (*File, error) {
	if len(buf) < 8 {
		return nil, fmt.Errorf("file: too short for header")
	}
	if string(buf[0:4]) != "SA64" {
		return nil, fmt.Errorf("file: bad magic %q", string(buf[0:4]))
	}
	version := binary.LittleEndian.Uint16(buf[4:6])
	if version != Version {
		return nil, fmt.Errorf("file: unsupported version %d (want %d)", version, Version)
	}
	flags := binary.LittleEndian.Uint16(buf[6:8])
	pos := 8

	if pos+2 > len(buf) {
		return nil, fmt.Errorf("file: truncated name table count")
	}
	count := int(binary.LittleEndian.Uint16(buf[pos:]))
	pos += 2

	// Front-coded entries (§ name table): each is
	// [shared_prefix_len uvarint][suffix_len uvarint][suffix bytes];
	// reconstruct by copying `shared` bytes from the previous name.
	names := make([]string, count)
	var prev string
	for i := 0; i < count; i++ {
		shared, ns := binary.Uvarint(buf[pos:])
		if ns <= 0 {
			return nil, fmt.Errorf("file: truncated name shared-prefix-len at %d", i)
		}
		pos += ns
		slen, nl := binary.Uvarint(buf[pos:])
		if nl <= 0 {
			return nil, fmt.Errorf("file: truncated name suffix-len at %d", i)
		}
		pos += nl
		if int(shared) > len(prev) {
			return nil, fmt.Errorf("file: name %d shared-prefix-len %d exceeds previous name length %d", i, shared, len(prev))
		}
		if pos+int(slen) > len(buf) {
			return nil, fmt.Errorf("file: truncated name body at %d", i)
		}
		name := prev[:shared] + string(buf[pos:pos+int(slen)])
		pos += int(slen)
		names[i] = name
		prev = name
	}

	labels, pos, err := readLabelTable(buf, pos)
	if err != nil {
		return nil, err
	}
	locals, pos, err := readLocalTable(buf, pos)
	if err != nil {
		return nil, err
	}

	// Decode the remaining byte stream into the in-memory record slice.
	// An on-disk `.tbn` only carries overlay-format records (INSN_RUN/
	// LIT_DATA/DIRECTIVE/COMMENT).
	var records []Record
	rr := NewRecordReader(buf[pos:])
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}

	return &File{
		Version: version,
		Flags:   flags,
		Names:   names,
		Labels:  labels,
		Locals:  locals,
		Records: records,
	}, nil
}
