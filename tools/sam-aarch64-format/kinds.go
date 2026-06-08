package format

// RecordKind identifies a record's payload shape (§3).
type RecordKind byte

const (
	KindInst      RecordKind = 0x01
	KindLabelDef  RecordKind = 0x02
	KindLocalDef  RecordKind = 0x03
	KindDirective RecordKind = 0x04
	KindComment   RecordKind = 0x05
	// KindLitInsts is a run of consecutive fully-literal instructions
	// stored as their assembled machine code (compact `.tbn`, Level 2).
	// Payload: [count:1][word0:4 LE]…[word{count-1}:4 LE]. The assembler
	// memcpys the words straight to OUT — zero encoding work. See
	// docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md.
	KindLitInsts RecordKind = 0x07
)

// Name returns the symbolic name of the record kind, or "UNKNOWN" for
// reserved or future kinds.
func (k RecordKind) Name() string {
	switch k {
	case KindInst:
		return "INST"
	case KindLabelDef:
		return "LABEL_DEF"
	case KindLocalDef:
		return "LOCAL_DEF"
	case KindDirective:
		return "DIRECTIVE"
	case KindComment:
		return "COMMENT"
	case KindLitInsts:
		return "LIT_INSTS"
	}
	return "UNKNOWN"
}

// IsKnown reports whether the record kind is defined in format v1.
func (k RecordKind) IsKnown() bool {
	return k.Name() != "UNKNOWN"
}
