package assemble

import (
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// fileFromRecords builds an in-memory symbolic *format.File from a name list
// and a record slice — the same shape the front-end hands the assembler. Tests
// use it instead of the former serialize-via-WriteFile / parse-via-ReadFile
// round-trip now that the symbolic record kinds are never serialized.
func fileFromRecords(names []string, recs []format.Record) *format.File {
	return &format.File{
		Version: format.Version,
		Flags:   format.Flags,
		Names:   names,
		Records: recs,
	}
}

// instRec builds an INST record from its fields.
func instRec(mnemonicID uint16, operandCount byte, operands []byte) format.Record {
	return format.Record{
		Kind:         format.KindInst,
		MnemonicID:   mnemonicID,
		OperandCount: operandCount,
		Operands:     operands,
	}
}

// labelRec builds a LABEL_DEF record.
func labelRec(symID uint16) format.Record {
	return format.Record{Kind: format.KindLabelDef, SymbolID: symID}
}

// localRec builds a LOCAL_DEF record.
func localRec(digit byte) format.Record {
	return format.Record{Kind: format.KindLocalDef, Digit: digit}
}

// dirRec builds a DIRECTIVE record.
func dirRec(directiveID, operandCount byte, operands []byte) format.Record {
	return format.Record{
		Kind:         format.KindDirective,
		DirectiveID:  directiveID,
		OperandCount: operandCount,
		Operands:     operands,
	}
}
