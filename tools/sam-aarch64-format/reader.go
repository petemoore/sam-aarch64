package format

import (
	"encoding/binary"
	"fmt"
)

// Record is a decoded statement-stream record. Only the fields
// appropriate to Kind are populated.
type Record struct {
	Kind RecordKind

	SymbolID  uint16
	Digit     byte
	Placement byte
	Body      []byte
	// RunLen is the number of consecutive blank source lines, populated for
	// KindBlankRun records (i78). In-memory IR only.
	RunLen       uint32
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
					if pos+1 > len(body) {
						return rec, fmt.Errorf("INSN_RUN mode 1: truncated patch header at %d", pos)
					}
					// Packed header [slot:4|expr_len:4]; len nibble 15
					// escapes to a real-length u8 (i39c).
					hdr := body[pos]
					pos++
					slot := hdr >> 4
					exprLen := int(hdr & 0x0F)
					if exprLen == exprLenEscape {
						if pos+1 > len(body) {
							return rec, fmt.Errorf("INSN_RUN mode 1: truncated patch length at %d", pos)
						}
						exprLen = int(body[pos])
						pos++
					}
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
	// GlobalNameIDs, Sidecar, and Comments are the editor region (compact
	// `.tbn` v2, M8 / i39b-2 + i78): name_ids that carried `.global`, and the
	// editor-region sidecar. Sidecar is the ordered tagged row stream (comments
	// + blank-line runs, in source order) — the renderer's authority. Comments
	// is the comment-only projection of Sidecar, retained for callers that only
	// want comments (comment-bench, tests). They are read from the tail of the
	// file and used only by the renderer / editor — the assembler never reads
	// them. Empty for an in-memory symbolic IR built by the front-end (where
	// comments and blank runs are still inline KindComment / KindBlankRun
	// records).
	GlobalNameIDs []uint16
	Sidecar       []SidecarRow
	Comments      []CommentRow
	// Records is the decoded statement stream. For a File built by the
	// front-end it is the in-memory symbolic IR (KindInst/KindLabelDef/
	// KindLocalDef/KindDirective/KindComment); for one read from disk via
	// ReadFile it is the overlay stream (KindInsnRun/KindLitData/KindDirective)
	// — comments and `.global` are in the editor region, not the record stream.
	Records []Record
}

func ReadFile(buf []byte) (*File, error) {
	const headerLen = 4 + 2 + 2 + 4 // magic + version + flags + editor_region_offset
	if len(buf) < headerLen {
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
	editorOffset := int(binary.LittleEndian.Uint32(buf[8:12]))
	if editorOffset < headerLen || editorOffset > len(buf) {
		return nil, fmt.Errorf("file: editor_region_offset %d out of range (file %d bytes)", editorOffset, len(buf))
	}
	pos := headerLen

	// Assembler-facing region: header position tables then the record stream,
	// which ends at editor_region_offset (the editor region follows).
	labels, pos, err := readLabelTable(buf, pos)
	if err != nil {
		return nil, err
	}
	locals, pos, err := readLocalTable(buf, pos)
	if err != nil {
		return nil, err
	}
	if pos > editorOffset {
		return nil, fmt.Errorf("file: header tables overran editor region (%d > %d)", pos, editorOffset)
	}

	// Decode the record stream (assembler-facing) up to the editor region.
	// An on-disk `.tbn` only carries overlay records (INSN_RUN/LIT_DATA/
	// DIRECTIVE); comments and `.global` live in the editor region.
	var records []Record
	rr := NewRecordReader(buf[pos:editorOffset])
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}

	// Editor region: name table, global flags, sidecar. The tagged-sidecar bit
	// in the header flags selects the row shape (a file written by this version
	// always sets it; a legacy untagged file parses as all-comments).
	tagged := flags&FlagTaggedSidecar != 0
	names, globals, sidecar, _, err := readEditorRegion(buf, editorOffset, tagged)
	if err != nil {
		return nil, err
	}

	var comments []CommentRow
	for _, r := range sidecar {
		if r.Kind == SidecarComment {
			comments = append(comments, r.Comment)
		}
	}

	return &File{
		Version:       version,
		Flags:         flags,
		Names:         names,
		Labels:        labels,
		Locals:        locals,
		GlobalNameIDs: globals,
		Sidecar:       sidecar,
		Comments:      comments,
		Records:       records,
	}, nil
}
