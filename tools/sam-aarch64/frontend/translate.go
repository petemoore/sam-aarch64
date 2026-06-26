// Package frontend is the front end of the host assembler: it turns GNU-as
// source text into the in-memory symbolic record IR (*format.File) that the
// assemble package consumes. The pipeline is preprocess (includes / macros /
// conditional assembly, preprocess.go) → lex (lexer.go) → parse (parser.go),
// with Translate as the entry point. It also provides the post-parse record
// transforms the host driver applies: the linker-equivalent flatten pass
// (flatten.go / layout.go) and the comment/data stripping passes (strip.go).
// The IR is never serialized — it is handed straight to the assembler (i48
// decision A).
package frontend

import (
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Translate accepts source bytes and a path (for error messages) and returns
// the in-memory symbolic IR as a *format.File. It runs the macro / include /
// conditional preprocessor over src before lex+parse. The records are never
// serialized — the assembler consumes the slice directly (i48 decision A).
func Translate(src []byte, path string) (*format.File, error) {
	return TranslateWithOptions(src, path, PreprocessOptions{})
}

// TranslateWithOptions is the same as Translate but allows callers to
// configure the preprocessor (e.g. .include search path).
func TranslateWithOptions(src []byte, path string, opts PreprocessOptions) (*format.File, error) {
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
	// The in-memory symbolic IR has no resolved PCs, so both header tables are
	// empty — labels/locals are carried as LABEL_DEF/LOCAL_DEF records.
	return &format.File{
		Version: format.Version,
		Flags:   format.Flags,
		Names:   st.Names(),
		Records: records,
	}, nil
}

// TranslateAndFlatten preprocesses, parses, and then applies the
// linker-equivalent Flatten pass before returning the in-memory IR. See
// flatten.go for the spectrum4 layout encoded in the flatten step.
func TranslateAndFlatten(src []byte, path string, preOpts PreprocessOptions, flatOpts FlattenOptions) (*format.File, error) {
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
	return &format.File{
		Version: format.Version,
		Flags:   format.Flags,
		Names:   st.Names(),
		Records: flat,
	}, nil
}
