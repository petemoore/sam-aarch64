package frontend

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
	// The symbolic `.tbn` has no resolved PCs, so both header tables are
	// empty — labels/locals are still carried as LABEL_DEF/LOCAL_DEF records.
	if err := format.WriteFile(&out, st, nil, nil, records); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// TranslateAndFlatten preprocesses, parses, and then applies the
// linker-equivalent Flatten pass before emitting the .tbn file. See
// flatten.go for the spectrum4 layout encoded in the flatten step.
func TranslateAndFlatten(src []byte, path string, preOpts PreprocessOptions, flatOpts FlattenOptions) ([]byte, error) {
	pre, err := Preprocess(src, path, preOpts)
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
	flat, err := Flatten(records, st.Names(), flatOpts)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	// Symbolic `.tbn` (flattened): header tables empty, labels/locals stay
	// as records.
	if err := format.WriteFile(&out, st, nil, nil, flat); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
