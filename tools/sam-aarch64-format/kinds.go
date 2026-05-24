package format

// RecordKind identifies a record's payload shape (§3).
type RecordKind byte

const (
	KindInst      RecordKind = 0x01
	KindLabelDef  RecordKind = 0x02
	KindLocalDef  RecordKind = 0x03
	KindDirective RecordKind = 0x04
	KindComment   RecordKind = 0x05
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
	}
	return "UNKNOWN"
}

// IsKnown reports whether the record kind is defined in format v1.
func (k RecordKind) IsKnown() bool {
	return k.Name() != "UNKNOWN"
}
