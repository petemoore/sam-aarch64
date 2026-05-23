package translate

import (
	"bytes"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Translate accepts source bytes and a path (for error messages) and
// returns the encoded .tbn bytes.
func Translate(src []byte, path string) ([]byte, error) {
	toks, err := Lex(src, path)
	if err != nil {
		return nil, err
	}
	records, st, err := Parse(toks)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := format.WriteFile(&out, st, records); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
