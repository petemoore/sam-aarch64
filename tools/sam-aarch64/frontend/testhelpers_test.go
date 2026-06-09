package frontend

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// recCursor walks an in-memory []format.Record slice with a Next() interface
// mirroring format.RecordReader, so parser tests that asserted on the decoded
// record stream port over with minimal changes now that Parse/Translate return
// the records in-memory (never serialized).
type recCursor struct {
	recs []format.Record
	pos  int
}

func newRecCursor(recs []format.Record) *recCursor { return &recCursor{recs: recs} }

func (c *recCursor) AtEnd() bool { return c.pos >= len(c.recs) }

// Next returns the next record. The error return mirrors RecordReader.Next so
// existing `rec, _ := r.Next()` / `rec, err := r.Next()` call sites compile
// unchanged; it is always nil (the records are already decoded).
func (c *recCursor) Next() (format.Record, error) {
	if c.AtEnd() {
		return format.Record{}, nil
	}
	rec := c.recs[c.pos]
	c.pos++
	return rec, nil
}

// translateFile is parseHelper's companion: it returns the in-memory File the
// front-end produces (Translate now returns *format.File directly).
func translateFile(t *testing.T, src string) *format.File {
	t.Helper()
	f, err := Translate([]byte(src), "test.s")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// fileFromRecords builds an in-memory File from a name list and records.
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

func dirRec(directiveID, operandCount byte, operands []byte) format.Record {
	return format.Record{
		Kind:         format.KindDirective,
		DirectiveID:  directiveID,
		OperandCount: operandCount,
		Operands:     operands,
	}
}

func recordKinds(recs []format.Record) []format.RecordKind {
	kinds := make([]format.RecordKind, len(recs))
	for i, r := range recs {
		kinds[i] = r.Kind
	}
	return kinds
}
