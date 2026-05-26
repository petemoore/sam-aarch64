package translate

import (
	"bytes"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Translate accepts source bytes and a path (for error messages) and
// returns the encoded .tbn bytes. It runs the macro / include / conditional
// preprocessor over src before lex+parse.
func Translate(src []byte, path string) ([]byte, error) {
	return TranslateWithOptions(src, path, PreprocessOptions{})
}

// TranslateWithOptions is the same as Translate but allows callers to
// configure the preprocessor (e.g. .include search path).
func TranslateWithOptions(src []byte, path string, opts PreprocessOptions) ([]byte, error) {
	pre, err := Preprocess(src, path, opts)
	if err != nil {
		return nil, err
	}
	toks, err := Lex(pre, path)
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
