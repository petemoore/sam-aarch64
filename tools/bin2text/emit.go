package main

import (
	"bytes"
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Emit reads .tbn bytes and returns canonically-formatted text.
func Emit(in []byte) ([]byte, error) {
	f, err := format.ReadFile(in)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	rr := format.NewRecordReader(f.Records)
	var prevWasStatement bool
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			return nil, err
		}
		switch rec.Kind {
		case format.KindLabelDef:
			if prevWasStatement {
				out.WriteByte('\n')
			}
			fmt.Fprintf(&out, "%s:", f.Names[rec.SymbolID])
			prevWasStatement = true
		case format.KindLocalDef:
			if prevWasStatement {
				out.WriteByte('\n')
			}
			fmt.Fprintf(&out, "%d:", rec.Digit)
			prevWasStatement = true
		case format.KindComment:
			if rec.Placement == 1 && prevWasStatement {
				out.WriteByte(' ')
				fmt.Fprintf(&out, "//%s", string(rec.Body))
				out.WriteByte('\n')
				prevWasStatement = false
				continue
			}
			if prevWasStatement {
				out.WriteByte('\n')
			}
			fmt.Fprintf(&out, "//%s\n", string(rec.Body))
			prevWasStatement = false
		case format.KindInst, format.KindDirective:
			if prevWasStatement {
				out.WriteByte('\n')
			}
			if err := emitStatement(&out, f, rec); err != nil {
				return nil, err
			}
			prevWasStatement = true
		default:
			fmt.Fprintf(&out, "// [skipped unknown record kind 0x%02x, %d bytes]\n",
				byte(rec.Kind), len(rec.Raw))
			prevWasStatement = false
		}
	}
	if prevWasStatement {
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// emitStatement is filled in by Task 24.
func emitStatement(out *bytes.Buffer, f *format.File, rec format.Record) error {
	return fmt.Errorf("emitStatement: not yet implemented for kind %v", rec.Kind)
}
