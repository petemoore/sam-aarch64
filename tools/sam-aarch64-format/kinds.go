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
	// https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md.
	KindLitInsts RecordKind = 0x07
	// KindLitData is a run of constant data from a single numeric data
	// directive (.byte/.short/.hword/.word/.quad), stored as its raw
	// assembled little-endian bytes (compact `.tbn`, Level 2). Payload:
	// [directive_id u8][raw bytes]. The directive_id preserves which
	// directive the author wrote so the disassembler round-trips the
	// source spelling; the assembler ignores it and memcpys the bytes.
	KindLitData RecordKind = 0x08
	// KindInsnRun is a run of consecutive instructions in the compact
	// `.tbn` v2 instruction overlay (M8 / i39a). It unifies fully-literal
	// and symbol/PC-bearing instructions into one record. Payload:
	// [mode u8][elements]. mode 0 packs bare 4-byte assembled words (the
	// LIT_INSTS floor). mode 1 stores each instruction as a base word
	// (with relocated bitfields zeroed) followed by [patch_count u8] and
	// patch_count × [slot u8][expr_len u8][expr bytes]; pass 2 evaluates
	// each patch expression and ORs the folded bits into the zeroed field.
	// See docs/specs/compact-tbn-nextgen-design.md.
	KindInsnRun RecordKind = 0x09
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
	case KindLitData:
		return "LIT_DATA"
	case KindInsnRun:
		return "INSN_RUN"
	}
	return "UNKNOWN"
}

// IsKnown reports whether the record kind is defined in format v1.
func (k RecordKind) IsKnown() bool {
	return k.Name() != "UNKNOWN"
}
