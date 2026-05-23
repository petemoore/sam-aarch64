package translate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// preprocOnly runs the preprocessor in isolation and returns the expanded
// source, after stripping the synthetic # line "file" directives so tests can
// focus on the actual content.
func preprocOnly(t *testing.T, src, path string, opts PreprocessOptions) string {
	t.Helper()
	out, err := Preprocess([]byte(src), path, opts)
	if err != nil {
		t.Fatalf("Preprocess: %v\nsrc:\n%s", err, src)
	}
	var b strings.Builder
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "# ") && strings.Contains(line, "\"") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	// Drop final synthetic trailing newline from the join above.
	s := b.String()
	s = strings.TrimRight(s, "\n") + "\n"
	return s
}

func TestPreprocessPassthrough(t *testing.T) {
	src := ".text\nmain:\n  mov x0, #1\n"
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	if got != src {
		t.Errorf("passthrough mismatch:\n got=%q\nwant=%q", got, src)
	}
}

func TestPreprocessSimpleMacro(t *testing.T) {
	src := strings.Join([]string{
		".macro _strb val, addr",
		"  mov     w0, \\val & 0xff",
		"  adrp    x1, \\addr",
		"  add     x1, x1, :lo12:\\addr",
		"  strb    w0, [x1]",
		".endm",
		"main:",
		"  _strb 0x34, BORDCR",
		"",
	}, "\n")
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	for _, want := range []string{
		"mov     w0, 0x34 & 0xff",
		"adrp    x1, BORDCR",
		"add     x1, x1, :lo12:BORDCR",
		"strb    w0, [x1]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected expansion to contain %q\ngot:\n%s", want, got)
		}
	}
}

func TestPreprocessLongestArgFirst(t *testing.T) {
	// \address must NOT be mis-substituted as \a + "ddress".
	src := strings.Join([]string{
		".macro foo a, address",
		"  mov \\a, \\address",
		".endm",
		"foo x1, MY_LABEL",
		"",
	}, "\n")
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	if !strings.Contains(got, "mov x1, MY_LABEL") {
		t.Errorf("expected `mov x1, MY_LABEL`, got:\n%s", got)
	}
}

func TestPreprocessShorterArgFirst(t *testing.T) {
	// The reverse — \a must still match when declared first.
	src := strings.Join([]string{
		".macro foo address, a",
		"  mov \\a, \\address",
		".endm",
		"foo MY_LABEL, x1",
		"",
	}, "\n")
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	if !strings.Contains(got, "mov x1, MY_LABEL") {
		t.Errorf("expected `mov x1, MY_LABEL`, got:\n%s", got)
	}
}

func TestPreprocessTokenPasteIdent(t *testing.T) {
	src := strings.Join([]string{
		".macro logarm reg",
		"  adrp    x0, msg_\\reg",
		"  add     x0, x0, :lo12:msg_\\reg",
		".endm",
		"logarm NZCV",
		"",
	}, "\n")
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	if !strings.Contains(got, "adrp    x0, msg_NZCV") {
		t.Errorf("expected token-paste msg_NZCV, got:\n%s", got)
	}
	if !strings.Contains(got, ":lo12:msg_NZCV") {
		t.Errorf("expected :lo12:msg_NZCV, got:\n%s", got)
	}
}

func TestPreprocessSubstInsideString(t *testing.T) {
	src := strings.Join([]string{
		".macro msgreg regname",
		"msg_\\regname:",
		".asciz \"\\regname: \"",
		".endm",
		"msgreg NZCV",
		"",
	}, "\n")
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	if !strings.Contains(got, "msg_NZCV:") {
		t.Errorf("expected label msg_NZCV:, got:\n%s", got)
	}
	if !strings.Contains(got, ".asciz \"NZCV: \"") {
		t.Errorf("expected .asciz \"NZCV: \", got:\n%s", got)
	}
}

func TestPreprocessRecursiveMacro(t *testing.T) {
	src := strings.Join([]string{
		".macro _setmsk mask, address",
		"  ldrb    w0, [x28, \\address-sysvars]",
		"  orr     w0, w0, \\mask",
		"  strb    w0, [x28, \\address-sysvars]",
		".endm",
		".macro _setbit bit, address",
		"  _setmsk (1<<\\bit), \\address",
		".endm",
		"_setbit 0, TV_FLAG",
		"",
	}, "\n")
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	// After two levels of expansion, expect concrete instructions.
	for _, want := range []string{
		"ldrb    w0, [x28, TV_FLAG-sysvars]",
		"orr     w0, w0, (1<<0)",
		"strb    w0, [x28, TV_FLAG-sysvars]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected expansion to contain %q\ngot:\n%s", want, got)
		}
	}
}

func TestPreprocessIfTruthy(t *testing.T) {
	src := strings.Join([]string{
		".set UART_DEBUG, 1",
		".if UART_DEBUG",
		"  emitted",
		".else",
		"  not_emitted",
		".endif",
		"",
	}, "\n")
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	if !strings.Contains(got, "emitted") || strings.Contains(got, "not_emitted") {
		t.Errorf(".if UART_DEBUG=1 mis-handled:\n%s", got)
	}
}

func TestPreprocessIfFalsy(t *testing.T) {
	src := strings.Join([]string{
		".set UART_DEBUG, 0",
		".if UART_DEBUG",
		"  not_emitted",
		".else",
		"  emitted",
		".endif",
		"",
	}, "\n")
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	if !strings.Contains(got, "emitted") || strings.Contains(got, "not_emitted") {
		t.Errorf(".if UART_DEBUG=0 mis-handled:\n%s", got)
	}
}

func TestPreprocessIfNestedSkipped(t *testing.T) {
	// Inner .if inside an inactive outer .if must NOT evaluate (it could
	// reference symbols that aren't .set).
	src := strings.Join([]string{
		".set OUTER, 0",
		".if OUTER",
		"  .if INNER_UNDEFINED",
		"    deeply_nested",
		"  .endif",
		"  outer_only",
		".endif",
		"end",
		"",
	}, "\n")
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	if strings.Contains(got, "deeply_nested") || strings.Contains(got, "outer_only") {
		t.Errorf("inactive outer .if emitted content:\n%s", got)
	}
	if !strings.Contains(got, "end") {
		t.Errorf("post-.if content missing:\n%s", got)
	}
}

func TestPreprocessIfInsideMacro(t *testing.T) {
	// .if inside a macro body must be evaluated *after* expansion, using
	// the .set table populated by the caller.
	src := strings.Join([]string{
		".set UART_DEBUG, 1",
		".macro log char",
		".if UART_DEBUG",
		"  mov x0, #\\char",
		".endif",
		".endm",
		"log 'x'",
		"",
	}, "\n")
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	if !strings.Contains(got, "mov x0, #'x'") {
		t.Errorf("expected `mov x0, #'x'`, got:\n%s", got)
	}
}

func TestPreprocessIfInsideMacroFalsy(t *testing.T) {
	src := strings.Join([]string{
		".set UART_DEBUG, 0",
		".macro log char",
		".if UART_DEBUG",
		"  mov x0, #\\char",
		".endif",
		".endm",
		"log 'x'",
		"after",
		"",
	}, "\n")
	got := preprocOnly(t, src, "x.s", PreprocessOptions{})
	if strings.Contains(got, "mov x0") {
		t.Errorf("expected NO `mov x0`, got:\n%s", got)
	}
	if !strings.Contains(got, "after") {
		t.Errorf("expected `after`, got:\n%s", got)
	}
}

func TestPreprocessUnterminatedIf(t *testing.T) {
	src := ".set A, 1\n.if A\nhello\n"
	if _, err := Preprocess([]byte(src), "x.s", PreprocessOptions{}); err == nil {
		t.Errorf("expected error for unterminated .if")
	}
}

func TestPreprocessUnterminatedMacro(t *testing.T) {
	src := ".macro foo a\n  mov x0, \\a\n"
	if _, err := Preprocess([]byte(src), "x.s", PreprocessOptions{}); err == nil {
		t.Errorf("expected error for unterminated .macro")
	}
}

func TestPreprocessIncludeRelative(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.s"), []byte(".include \"inc.s\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inc.s"), []byte("included_line\n"), 0644); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(filepath.Join(dir, "main.s"))
	out, err := Preprocess(src, filepath.Join(dir, "main.s"), PreprocessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "included_line") {
		t.Errorf("included content missing:\n%s", out)
	}
}

func TestPreprocessIncludeSearchPath(t *testing.T) {
	rootDir := t.TempDir()
	incDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "main.s"), []byte(".include \"helper.s\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "helper.s"), []byte("from_helper\n"), 0644); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(filepath.Join(rootDir, "main.s"))
	out, err := Preprocess(src, filepath.Join(rootDir, "main.s"), PreprocessOptions{IncludeDirs: []string{incDir}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "from_helper") {
		t.Errorf("search-path lookup missed:\n%s", out)
	}
}

func TestPreprocessIncludeMissing(t *testing.T) {
	src := ".include \"nope.s\"\n"
	if _, err := Preprocess([]byte(src), "x.s", PreprocessOptions{}); err == nil {
		t.Errorf("expected error for missing include")
	}
}

func TestPreprocessIncludeSkippedInsideFalseIf(t *testing.T) {
	// An .include inside a false .if must not even be opened.
	src := strings.Join([]string{
		".set GUARD, 0",
		".if GUARD",
		"  .include \"definitely_does_not_exist.s\"",
		".endif",
		"trailing",
		"",
	}, "\n")
	got, err := Preprocess([]byte(src), "x.s", PreprocessOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), "trailing") {
		t.Errorf("expected `trailing`:\n%s", got)
	}
}

// End-to-end via Translate with -I search path and an included file.
func TestTranslateIncludeViaSearchPath(t *testing.T) {
	rootDir := t.TempDir()
	incDir := t.TempDir()
	main := strings.Join([]string{
		".set UART_DEBUG, 1",
		".include \"helper.s\"",
		"foo:",
		"  log_msg",
		"  ret",
		"",
	}, "\n")
	helper := strings.Join([]string{
		".macro log_msg",
		".if UART_DEBUG",
		"  mov x0, #1",
		".endif",
		".endm",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(rootDir, "main.s"), []byte(main), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "helper.s"), []byte(helper), 0644); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(filepath.Join(rootDir, "main.s"))
	out, err := TranslateWithOptions(src, filepath.Join(rootDir, "main.s"), PreprocessOptions{IncludeDirs: []string{incDir}})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	f, err := format.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	found := false
	for _, n := range f.Names {
		if n == "foo" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected name `foo`, got %v", f.Names)
	}
}

// Position tracking: an error inside a macro expansion must be attributed to
// the *macro definition* line, not to the post-expansion line in the
// preprocessed blob.
func TestPreprocessPositionInsideMacroExpansion(t *testing.T) {
	src := strings.Join([]string{
		"// pad",
		"// pad",
		"// pad",
		".macro bad x",
		"  ldr y0, [\\x]", // y0 is not a register; parseInst will error
		".endm",
		"foo:",
		"  bad addr",
		"",
	}, "\n")
	_, err := Translate([]byte(src), "src.s")
	if err == nil {
		t.Fatalf("expected parse error")
	}
	msg := err.Error()
	// The error should mention the macro body line (line 5) of src.s.
	if !strings.Contains(msg, "src.s:5:") {
		t.Errorf("expected position src.s:5:* in error, got %q", msg)
	}
}

// Position tracking: an error after a macro expansion is attributed to the
// post-expansion line (the line *after* the call site, not the call site
// itself, because we emit the line directive pointing one past the call).
func TestPreprocessPositionAfterMacroExpansion(t *testing.T) {
	src := strings.Join([]string{
		".macro nop_macro",
		"  ret",
		".endm",
		"foo:",
		"  nop_macro",
		"  not_a_mnemonic", // line 6
		"",
	}, "\n")
	_, err := Translate([]byte(src), "src.s")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "src.s:6:") {
		t.Errorf("expected src.s:6:* in error, got %q", err.Error())
	}
}

// End-to-end via Translate: ensure a macro+.if file lexes/parses cleanly.
func TestTranslateMacroAndIf(t *testing.T) {
	src := strings.Join([]string{
		".set UART_DEBUG, 1",
		".macro log char",
		".if UART_DEBUG",
		"  mov x0, #\\char",
		".endif",
		".endm",
		"start:",
		"  log 0x41",
		"  ret",
		"",
	}, "\n")
	out, err := Translate([]byte(src), "x.s")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	f, err := format.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	gotNames := strings.Join(f.Names, ",")
	if !strings.Contains(gotNames, "start") {
		t.Errorf("expected `start` in names, got %q", gotNames)
	}
}
