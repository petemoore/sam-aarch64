package render

import (
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// fileFromRecords builds an in-memory File from a name list and records — the
// shape the front-end hands the assembler / renderer. Render tests use it to
// exercise EmitFile directly on the symbolic IR, since the symbolic record
// kinds are no longer serialized.
func fileFromRecords(names []string, recs []format.Record) *format.File {
	return &format.File{
		Version: format.Version,
		Flags:   format.Flags,
		Names:   names,
		Records: recs,
	}
}

func instRec(mnemonicID uint16, operandCount byte, operands []byte) format.Record {
	return format.Record{
		Kind:         format.KindInst,
		MnemonicID:   mnemonicID,
		OperandCount: operandCount,
		Operands:     operands,
	}
}

func labelRec(symID uint16) format.Record {
	return format.Record{Kind: format.KindLabelDef, SymbolID: symID}
}

func localRec(digit byte) format.Record {
	return format.Record{Kind: format.KindLocalDef, Digit: digit}
}

func commentRec(placement byte, body []byte) format.Record {
	return format.Record{Kind: format.KindComment, Placement: placement, Body: body}
}

func blankRunRec(runLen uint32) format.Record {
	return format.Record{Kind: format.KindBlankRun, RunLen: runLen}
}

func dirRec(directiveID, operandCount byte, operands []byte) format.Record {
	return format.Record{
		Kind:         format.KindDirective,
		DirectiveID:  directiveID,
		OperandCount: operandCount,
		Operands:     operands,
	}
}
