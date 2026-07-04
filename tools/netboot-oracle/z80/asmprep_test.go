// asmprep_test.go — host-verification of src/asmprep.asm (i31b: the on-SAM
// assembler-source preprocessor; Bricks 1 + 2a).
//
// Drives prep_run under the flat-memory koron-go/z80 harness and byte-compares
// its expanded output against the Go authority, frontend.Preprocess
// (tools/sam-aarch64/frontend/preprocess.go). The authority is imported
// directly (as the asmparse corpus test does), so there is no transcription to
// drift.
//
// Coverage: TestAsmprepBrick1 + TestAsmprepBrick1Random exercise the
// conditional-assembly core (leading `# <line> "<file>"` directive, `.set`
// capture + pass-through, `.if`/`.else`/`.endif` nesting). TestAsmprepBrick2aDefine
// + TestAsmprepBrick2aStorage exercise macro DEFINITION (.macro/.endm parse +
// collect + store) — a definition is consumed (emits nothing), so the byte-
// compare still holds, and the stored records are validated via exposed symbols.
// Macro invocation/expansion is Brick 2b; .include is Brick 3; so the fixtures
// here contain no macro-invocation or .include, keeping prep_run byte-identical
// to frontend.Preprocess on this input domain.
package z80_test

import (
	"bytes"
	"math/rand"
	"os"
	"strings"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
)

const (
	prepBinPath = "../../../build/asmprep.bin"
	prepMapPath = "../../../build/asmprep.map"
)

func loadAsmprep(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(prepBinPath); err != nil {
		t.Fatalf("asmprep binary not built (%s); run `make asmprep-z80`", prepBinPath)
	}
	mac, err := z80h.Load(prepBinPath, prepMapPath)
	if err != nil {
		t.Fatalf("load asmprep: %v", err)
	}
	return mac
}

// prepZ80 runs prep_run on src with the given path and returns the expanded
// output bytes plus the error flag.
func prepZ80(t *testing.T, mac *z80h.Machine, src []byte, path string) (out []byte, errFlag bool) {
	t.Helper()
	symSrc, err := mac.Sym("PREP_SRC")
	if err != nil {
		t.Fatal(err)
	}
	symOut, err := mac.Sym("PREP_OUT")
	if err != nil {
		t.Fatal(err)
	}
	symPath, err := mac.Sym("PREP_PATH")
	if err != nil {
		t.Fatal(err)
	}
	symErr, err := mac.Sym("PREP_ERR")
	if err != nil {
		t.Fatal(err)
	}

	mac.Write(symSrc, src)
	mac.Write(symPath, append([]byte(path), 0)) // NUL-terminated
	res, err := mac.CallEntry("prep_run", z80h.Entry{BC: uint16(len(src))})
	if err != nil {
		t.Fatalf("prep_run: %v", err)
	}
	errFlag = mac.Read(symErr, 1)[0] != 0
	if !errFlag {
		out = mac.Read(symOut, int(res.BC))
	}
	return out, errFlag
}

// prepCases are the brick-1 fixtures: name, source, and path. Each is run
// through both frontend.Preprocess and the Z80 prep_run; the two must agree
// byte-for-byte (or both report an error).
var prepCases = []struct {
	name string
	src  string
	path string
}{
	{"passthrough", ".text\nmain:\n  mov x0, #1\n", "x.s"},
	{"empty", "", "x.s"},
	{"no-trailing-newline", ".text\nmain:", "x.s"},
	{"blank-lines", "a\n\nb\n\n", "x.s"},
	{"leading-ws-preserved", "\t.text\n    mov x0, #1\n", "x.s"},
	{"crlf", ".text\r\nmain:\r\n", "x.s"},

	// .set capture + pass-through (the .set line itself is emitted).
	{"set-decimal", ".set FOO, 1\n.if FOO\nyes\n.endif\n", "x.s"},
	{"set-hex", ".set FOO, 0x10\n.if FOO\nyes\n.endif\n", "x.s"},
	{"set-hex-zero", ".set FOO, 0x00\n.if FOO\nyes\n.else\nno\n.endif\n", "x.s"},
	{"set-bin", ".set FOO, 0b101\n.if FOO\nyes\n.endif\n", "x.s"},
	{"set-neg", ".set FOO, -1\n.if FOO\nyes\n.endif\n", "x.s"},
	{"set-plus", ".set FOO, +0\n.if FOO\nyes\n.else\nno\n.endif\n", "x.s"},
	{"set-nonliteral-passthrough", ".set FOO, BAR+1\n.if FOO\nyes\n.else\nno\n.endif\n", "x.s"},
	{"set-redefine-lastwins", ".set FOO, 1\n.set FOO, 0\n.if FOO\nyes\n.else\nno\n.endif\n", "x.s"},
	{"set-trailing-comment", ".set FOO, 1 // enable\n.if FOO\nyes\n.endif\n", "x.s"},
	{"set-trailing-block-comment", ".set FOO, 1 /* on */\n.if FOO\nyes\n.endif\n", "x.s"},
	{"set-hex-upper", ".set FOO, 0xFF\n.if FOO\nyes\n.endif\n", "x.s"},
	{"set-invalid-bin-passthrough", ".set FOO, 0b12\n.if FOO\nyes\n.else\nno\n.endif\n", "x.s"},
	{"set-block-comment-midvalue", ".set FOO, 0x1/*c*/0\n.if FOO\nyes\n.endif\n", "x.s"},
	{"set-slashslash-nospace", ".set FOO, 1//x\n.if FOO\nyes\n.endif\n", "x.s"},
	{"if-symbol-from-nonliteral-false", ".set FOO, BAR\n.if FOO\nyes\n.else\nno\n.endif\n", "x.s"},
	// Exercises stripTrailingComment's escaped-quote check across a spliced-out
	// /* */ block (the escape-adjacency case): both sides agree the .set is not
	// captured. In Brick 1 the strip result only feeds isBareIdent/parseIntLiteral,
	// so the outcome is identical regardless; it becomes observable once Bricks
	// b2/b3 emit strip results (macro args / include paths).
	{"set-escape-block-quote", ".set FOO, \\/*c*/\"//x\n.if FOO\nyes\n.else\nno\n.endif\n", "x.s"},

	// .if / .else / .endif truthiness and nesting.
	{"if-truthy", ".set U, 1\n.if U\n  emitted\n.else\n  not_emitted\n.endif\n", "x.s"},
	{"if-falsy", ".set U, 0\n.if U\n  not_emitted\n.else\n  emitted\n.endif\n", "x.s"},
	{"if-unknown-symbol-false", ".if NOPE\nx\n.else\ny\n.endif\n", "x.s"},
	{"if-comment-on-directive", ".set U, 1\n.if U // note\nyes\n.endif\n", "x.s"},
	{"if-nested-both-true", ".set A, 1\n.set B, 1\n.if A\n.if B\ndeep\n.endif\nmid\n.endif\n", "x.s"},
	{"if-nested-outer-false", ".set OUTER, 0\n.if OUTER\n.if INNER_UNDEF\ndeep\n.endif\nouter_only\n.endif\nend\n", "x.s"},
	{"if-nested-inner-false", ".set A, 1\n.set B, 0\n.if A\n.if B\ndeep\n.else\ninner_else\n.endif\nmid\n.endif\n", "x.s"},
	{"else-taken-once", ".set U, 1\n.if U\na\n.else\nb\n.endif\nafter\n", "x.s"},
	{"if-no-else", ".set U, 0\n.if U\nhidden\n.endif\nvisible\n", "x.s"},
	{"if-tab-separated", ".set U, 1\n.if\tU\nyes\n.endif\n", "x.s"},

	// Error cases — Go returns an error, Z80 sets PREP_ERR.
	{"err-unterminated-if", ".set A, 1\n.if A\nhello\n", "x.s"},
	{"err-else-outside-if", "hello\n.else\nx\n", "x.s"},
	{"err-endif-outside-if", "hello\n.endif\n", "x.s"},
	{"err-duplicate-else", ".if X\na\n.else\nb\n.else\nc\n.endif\n", "x.s"},
	{"err-if-missing-symbol", ".if\n.endif\n", "x.s"},
	{"err-if-expression", ".set A,1\n.if A+1\nx\n.endif\n", "x.s"},
}

func TestAsmprepBrick1(t *testing.T) {
	for _, tc := range prepCases {
		t.Run(tc.name, func(t *testing.T) {
			want, wantErr := frontend.Preprocess([]byte(tc.src), tc.path, frontend.PreprocessOptions{})

			// Fresh machine per case: prep_run re-initialises all state, but
			// reloading keeps buffers pristine and cases fully independent.
			m := loadAsmprep(t)
			got, gotErr := prepZ80(t, m, []byte(tc.src), tc.path)

			if wantErr != nil {
				if !gotErr {
					t.Fatalf("expected error (Go: %v), but prep_run reported success; got=%q", wantErr, got)
				}
				return
			}
			if gotErr {
				t.Fatalf("prep_run reported error, but Go succeeded; want=%q", want)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("output mismatch:\n got=%q\nwant=%q", got, want)
			}
		})
	}
}

// genProg emits a random balanced .set/.if program into b. Statements are
// drawn from the brick-1 construct set only (plain lines, .set NAME, INT, and
// nested .if/.else/.endif blocks); it never emits .macro/.include/invocations,
// so frontend.Preprocess and prep_run must agree byte-for-byte.
func genProg(r *rand.Rand, b *strings.Builder, depth int) {
	syms := []string{"A", "B", "C", "FOO", "BAR", "UART"}
	vals := []string{"0", "1", "2", "0x0", "0x10", "0b0", "0b11", "-1", "+0", "BAR+1", "sysvar"}
	plain := []string{"nop", "  mov x0, #1", "label:", "\t.text", "x // c", "", "  ret /* t */"}
	n := r.Intn(4)
	for i := 0; i < n; i++ {
		switch r.Intn(4) {
		case 0:
			b.WriteString(plain[r.Intn(len(plain))])
			b.WriteByte('\n')
		case 1:
			b.WriteString(".set ")
			b.WriteString(syms[r.Intn(len(syms))])
			b.WriteString(", ")
			b.WriteString(vals[r.Intn(len(vals))])
			b.WriteByte('\n')
		default:
			if depth >= 3 {
				b.WriteString(plain[r.Intn(len(plain))])
				b.WriteByte('\n')
				continue
			}
			b.WriteString(".if ")
			b.WriteString(syms[r.Intn(len(syms))])
			b.WriteByte('\n')
			genProg(r, b, depth+1)
			if r.Intn(2) == 0 {
				b.WriteString(".else\n")
				genProg(r, b, depth+1)
			}
			b.WriteString(".endif\n")
		}
	}
}

// prepCasesB2a are the brick-2a fixtures: macro DEFINITIONS that are never
// invoked. A definition is consumed (emits nothing), so frontend.Preprocess and
// prep_run must still agree byte-for-byte; invocation is Brick 2b.
var prepCasesB2a = []struct {
	name string
	src  string
	path string
}{
	{"define-consumed", ".macro foo\n  body_line\n.endm\nafter\n", "x.s"},
	{"define-empty-body", ".macro foo\n.endm\nafter\n", "x.s"},
	{"define-with-params", ".macro strb val, addr\n  x \\val\n  y \\addr\n.endm\nafter\n", "x.s"},
	{"define-leading-comma", ".macro foo, a, b\n  body\n.endm\nafter\n", "x.s"},
	{"define-comment-on-header", ".macro foo a // an arg\n  body\n.endm\nafter\n", "x.s"},
	{"define-tab-separated", ".macro\tfoo a\n  body\n.endm\nafter\n", "x.s"},
	{"define-preceded-by-set", ".set U, 1\n.macro foo\n  body\n.endm\nafter\n", "x.s"},
	{"define-inside-active-if", ".set U, 1\n.if U\n.macro foo\n  x\n.endm\n.endif\nafter\n", "x.s"},
	{"define-inside-inactive-if", ".set U, 0\n.if U\n.macro foo\n  x\n.endm\n.endif\nafter\n", "x.s"},
	{"multiple-defines", ".macro a\n  aa\n.endm\n.macro b p\n  bb \\p\n.endm\nafter\n", "x.s"},
	{"define-body-has-directive-text", ".macro foo\n  .set INNER, 1\n  .if INNER\n  q\n  .endif\n.endm\nafter\n", "x.s"},

	// Error cases — Go returns an error, Z80 sets PREP_ERR.
	{"err-unterminated-macro", ".macro foo\n  body\n", "x.s"},
	{"err-nested-macro", ".macro foo\n.macro bar\n.endm\n.endm\n", "x.s"},
	{"err-endm-outside-macro", "code\n.endm\n", "x.s"},
	{"err-macro-missing-name", ".macro \n.endm\n", "x.s"},
}

func TestAsmprepBrick2aDefine(t *testing.T) {
	for _, tc := range prepCasesB2a {
		t.Run(tc.name, func(t *testing.T) {
			want, wantErr := frontend.Preprocess([]byte(tc.src), tc.path, frontend.PreprocessOptions{})
			m := loadAsmprep(t)
			got, gotErr := prepZ80(t, m, []byte(tc.src), tc.path)

			if wantErr != nil {
				if !gotErr {
					t.Fatalf("expected error (Go: %v), prep_run succeeded; got=%q", wantErr, got)
				}
				return
			}
			if gotErr {
				t.Fatalf("prep_run reported error, Go succeeded; want=%q", want)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("output mismatch:\n got=%q\nwant=%q", got, want)
			}
		})
	}
}

// storedMacro reads MACRO_TAB record k back from the harness, decoding the
// 12-byte layout {name_ptr:2, name_len:1, nparams:1, params_ptr:2, nbody:2,
// body_ptr:2, defline:2}. This validates the brick-2a storage format directly
// (its body/param contents are otherwise not observable until brick 2b invokes
// a macro), de-risking the reader that 2b will build on.
type storedMacro struct {
	name    string
	params  []string
	nbody   int
	defline int
}

func readStoredMacro(mac *z80h.Machine, k int) storedMacro {
	tab, _ := mac.Sym("MACRO_TAB")
	rec := mac.Read(tab+uint16(k*12), 12)
	namePtr := uint16(rec[0]) | uint16(rec[1])<<8
	nameLen := int(rec[2])
	nparams := int(rec[3])
	paramsPtr := uint16(rec[4]) | uint16(rec[5])<<8
	nbody := int(uint16(rec[6]) | uint16(rec[7])<<8)
	defline := int(uint16(rec[10]) | uint16(rec[11])<<8)
	sm := storedMacro{name: string(mac.Read(namePtr, nameLen)), nbody: nbody, defline: defline}
	for i := 0; i < nparams; i++ {
		pe := mac.Read(paramsPtr+uint16(i*4), 4)
		pptr := uint16(pe[0]) | uint16(pe[1])<<8
		plen := int(uint16(pe[2]) | uint16(pe[3])<<8)
		sm.params = append(sm.params, string(mac.Read(pptr, plen)))
	}
	return sm
}

func TestAsmprepBrick2aStorage(t *testing.T) {
	type want struct {
		count  int
		macros []storedMacro
	}
	cases := []struct {
		name string
		src  string
		want want
	}{
		{
			"single-no-params",
			".macro foo\n  a\n  b\n.endm\n",
			want{count: 1, macros: []storedMacro{{name: "foo", params: nil, nbody: 2, defline: 1}}},
		},
		{
			"params-and-defline",
			"pad\n.macro strb val, addr\n  x\n.endm\n",
			want{count: 1, macros: []storedMacro{{name: "strb", params: []string{"val", "addr"}, nbody: 1, defline: 2}}},
		},
		{
			"leading-comma-params",
			".macro foo, a, b\n.endm\n",
			want{count: 1, macros: []storedMacro{{name: "foo", params: []string{"a", "b"}, nbody: 0, defline: 1}}},
		},
		{
			"inactive-if-not-stored",
			".set U, 0\n.if U\n.macro foo\n x\n.endm\n.endif\n",
			want{count: 0},
		},
		{
			"two-macros",
			".macro a\n aa\n.endm\n.macro b p, q\n bb\n cc\n.endm\n",
			want{count: 2, macros: []storedMacro{
				{name: "a", params: nil, nbody: 1, defline: 1},
				{name: "b", params: []string{"p", "q"}, nbody: 2, defline: 4},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := loadAsmprep(t)
			_, errFlag := prepZ80(t, m, []byte(tc.src), "x.s")
			if errFlag {
				t.Fatalf("prep_run reported error")
			}
			cnt := int(m.Read(mustSym(t, m, "MACRO_COUNT"), 1)[0])
			if cnt != tc.want.count {
				t.Fatalf("MACRO_COUNT=%d want %d", cnt, tc.want.count)
			}
			for k, wm := range tc.want.macros {
				gm := readStoredMacro(m, k)
				if gm.name != wm.name {
					t.Errorf("macro[%d].name=%q want %q", k, gm.name, wm.name)
				}
				if strings.Join(gm.params, ",") != strings.Join(wm.params, ",") {
					t.Errorf("macro[%d].params=%v want %v", k, gm.params, wm.params)
				}
				if gm.nbody != wm.nbody {
					t.Errorf("macro[%d].nbody=%d want %d", k, gm.nbody, wm.nbody)
				}
				if gm.defline != wm.defline {
					t.Errorf("macro[%d].defline=%d want %d", k, gm.defline, wm.defline)
				}
			}
		})
	}
}

// prepCasesB2b are the brick-2b fixtures: macro INVOCATIONS. Each expands via
// \param substitution + recursive re-preprocessing, so the expanded text AND
// the mid-stream `# <line> "<file>"` directives must match frontend.Preprocess
// byte-for-byte. These mirror the macro cases in
// tools/sam-aarch64/frontend/preprocess_test.go.
var prepCasesB2b = []struct {
	name string
	src  string
	path string
}{
	// Simple positional substitution (preprocess_test SimpleMacro).
	{"simple", ".macro _strb val, addr\n  mov     w0, \\val & 0xff\n  adrp    x1, \\addr\n  add     x1, x1, :lo12:\\addr\n  strb    w0, [x1]\n.endm\nmain:\n  _strb 0x34, BORDCR\n", "x.s"},
	// Longest-param-first: \address must not be eaten as \a + "ddress".
	{"longest-arg-first", ".macro foo a, address\n  mov \\a, \\address\n.endm\nfoo x1, MY_LABEL\n", "x.s"},
	// The reverse declaration order still resolves \a correctly.
	{"shorter-arg-first", ".macro foo address, a\n  mov \\a, \\address\n.endm\nfoo MY_LABEL, x1\n", "x.s"},
	// Token-paste inside an identifier.
	{"token-paste", ".macro logarm reg\n  adrp    x0, msg_\\reg\n  add     x0, x0, :lo12:msg_\\reg\n.endm\nlogarm NZCV\n", "x.s"},
	// Substitution inside a string literal + a label.
	{"subst-in-string", ".macro msgreg regname\nmsg_\\regname:\n.asciz \"\\regname: \"\n.endm\nmsgreg NZCV\n", "x.s"},
	// A no-arg macro.
	{"no-args", ".macro nop_macro\n  ret\n.endm\nfoo:\n  nop_macro\n  after\n", "x.s"},
	// An empty-body macro (two directives, no body).
	{"empty-body-invoke", ".macro empt\n.endm\nbefore\nempt\nafter\n", "x.s"},
	// splitMacroArgs: a comma inside parens is not a separator.
	{"paren-args", ".macro _setmsk mask, address\n  orr w0, w0, \\mask\n  x \\address\n.endm\n_setmsk (1<<3), TV_FLAG\n", "x.s"},
	// splitMacroArgs: a comma inside a string is not a separator.
	{"string-comma-arg", ".macro emit s\n.asciz \"\\s\"\n.endm\nemit \"a, b, c\"\n", "x.s"},
	// Two-level recursive expansion (preprocess_test RecursiveMacro).
	{"recursive", ".macro _setmsk mask, address\n  ldrb    w0, [x28, \\address-sysvars]\n  orr     w0, w0, \\mask\n  strb    w0, [x28, \\address-sysvars]\n.endm\n.macro _setbit bit, address\n  _setmsk (1<<\\bit), \\address\n.endm\n_setbit 0, TV_FLAG\n", "x.s"},
	// .if inside a macro body, evaluated after expansion against the caller .set.
	{"if-in-macro-truthy", ".set UART_DEBUG, 1\n.macro log char\n.if UART_DEBUG\n  mov x0, #\\char\n.endif\n.endm\nlog 'x'\n", "x.s"},
	{"if-in-macro-falsy", ".set UART_DEBUG, 0\n.macro log char\n.if UART_DEBUG\n  mov x0, #\\char\n.endif\n.endm\nlog 'x'\nafter\n", "x.s"},
	// A trailing comment on the invocation line is stripped before arg-split.
	{"invoke-trailing-comment", ".macro mv a, b\n  mov \\a, \\b\n.endm\n  mv x0, x1 // do it\n", "x.s"},
	// \\ is a literal backslash pair (not the start of a substitution).
	{"backslash-literal", ".macro bs a\n  db \\a, \\\\n\n.endm\nbs 5\n", "x.s"},
	// An unrecognised \x keeps the backslash and rescans from x.
	{"unknown-escape", ".macro ue a\n  \\z \\a\n.endm\nue 7\n", "x.s"},
	// Macro invoked inside an active .if.
	{"invoke-inside-if", ".set ON, 1\n.macro m a\n  use \\a\n.endm\n.if ON\n  m 42\n.endif\n", "x.s"},
	// Redefinition: the newest definition wins (Go map-overwrite semantics).
	{"redefine-last-wins", ".macro dup a\n  first \\a\n.endm\n.macro dup a\n  second \\a\n.endm\ndup 9\n", "x.s"},
	// stripTrailingComment's escaped-quote-after-block check, now OBSERVABLE via a
	// macro arg (the strip result flows into output). Escape-adjacency form.
	{"arg-escape-block-quote", ".macro m a\n[\\a]\n.endm\nm \\/*c*/\\\"//x\n", "x.s"},
	// The divergence form: a spliced-out /* */ block leaves the byte before the
	// quote as '/', but the LAST EMITTED byte is '\\' — so the quote is escaped
	// (no toggle) only if the strip uses the emitted predecessor. A raw-in[i-1]
	// implementation would diverge here.
	{"arg-block-escape-quote-divergent", ".macro m a\n[\\a]\n.endm\nm \\/* */\\\"//x\n", "x.s"},

	// Error cases — Go returns an error, Z80 sets PREP_ERR.
	{"err-too-few-args", ".macro two a, b\n  x \\a \\b\n.endm\ntwo 1\n", "x.s"},
	{"err-too-many-args", ".macro one a\n  x \\a\n.endm\none 1, 2\n", "x.s"},
	{"err-direct-cycle", ".macro loop a\n  loop \\a\n.endm\nloop 1\n", "x.s"},
	{"err-indirect-cycle", ".macro a x\n  b \\x\n.endm\n.macro b x\n  a \\x\n.endm\na 1\n", "x.s"},
}

func TestAsmprepBrick2bInvoke(t *testing.T) {
	for _, tc := range prepCasesB2b {
		t.Run(tc.name, func(t *testing.T) {
			want, wantErr := frontend.Preprocess([]byte(tc.src), tc.path, frontend.PreprocessOptions{})
			m := loadAsmprep(t)
			got, gotErr := prepZ80(t, m, []byte(tc.src), tc.path)

			if wantErr != nil {
				if !gotErr {
					t.Fatalf("expected error (Go: %v), prep_run succeeded; got=%q", wantErr, got)
				}
				return
			}
			if gotErr {
				t.Fatalf("prep_run reported error, Go succeeded; want=%q", want)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("output mismatch:\n got=%q\nwant=%q", got, want)
			}
		})
	}
}

// TestAsmprepBrick1Random fuzzes prep_run against the Go authority over many
// randomly-generated balanced .set/.if programs. Deterministic seed so a
// failure reproduces.
func TestAsmprepBrick1Random(t *testing.T) {
	r := rand.New(rand.NewSource(0x31b1))
	for i := 0; i < 800; i++ {
		var b strings.Builder
		// A handful of top-level .set to populate the table, then a block.
		genProg(r, &b, 0)
		src := b.String()
		want, wantErr := frontend.Preprocess([]byte(src), "r.s", frontend.PreprocessOptions{})

		m := loadAsmprep(t)
		got, gotErr := prepZ80(t, m, []byte(src), "r.s")

		if wantErr != nil {
			if !gotErr {
				t.Fatalf("case %d: expected error (Go: %v), prep_run succeeded\nsrc=%q\ngot=%q", i, wantErr, src, got)
			}
			continue
		}
		if gotErr {
			t.Fatalf("case %d: prep_run errored, Go succeeded\nsrc=%q\nwant=%q", i, src, want)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("case %d mismatch:\nsrc=%q\n got=%q\nwant=%q", i, src, got, want)
		}
	}
}

// genMacroProg emits a random program that DEFINES a few macros with known
// arities and then INVOKES them, interleaved with .set/.if blocks and plain
// lines. Bodies substitute their params in several ways (token-paste, in-string,
// bare); one macro's body invokes a lower-indexed macro to exercise recursive
// re-preprocessing. Args are drawn from tokens with no top-level commas and no
// leading '.'/backslash/macro-name, so a substituted body never turns into a
// surprise directive or cycle — the arity always matches, so prep_run and
// frontend.Preprocess agree byte-for-byte (directives included).
func genMacroProg(r *rand.Rand, b *strings.Builder) {
	// m0: 1 param; m1: 2 params; m2: 0 params. m1's body invokes m0 (safe:
	// m0 does not call back, so no cycle).
	b.WriteString(".macro m0 a\n  adrp x0, msg_\\a\n  mov x1, \\a\n.endm\n")
	b.WriteString(".macro m1 a, b\n  m0 \\a\n  op \\b, \\a\n.endm\n")
	b.WriteString(".macro m2\n  barrier\n.endm\n")
	args := []string{"x0", "x1", "1", "0x10", "(1<<2)", "LBL", "\"hi\"", "42"}
	arg := func() string { return args[r.Intn(len(args))] }
	n := 3 + r.Intn(6)
	for i := 0; i < n; i++ {
		switch r.Intn(6) {
		case 0:
			b.WriteString("  m0 ")
			b.WriteString(arg())
			b.WriteByte('\n')
		case 1:
			b.WriteString("  m1 ")
			b.WriteString(arg())
			b.WriteString(", ")
			b.WriteString(arg())
			b.WriteByte('\n')
		case 2:
			b.WriteString("  m2\n")
		case 3:
			b.WriteString(".set K")
			b.WriteByte(byte('0' + r.Intn(3)))
			b.WriteString(", ")
			b.WriteString([]string{"0", "1", "0x10"}[r.Intn(3)])
			b.WriteByte('\n')
		case 4:
			// A .if guarding an invocation, using a K symbol.
			b.WriteString(".if K")
			b.WriteByte(byte('0' + r.Intn(3)))
			b.WriteByte('\n')
			b.WriteString("  m0 ")
			b.WriteString(arg())
			b.WriteByte('\n')
			b.WriteString(".endif\n")
		default:
			b.WriteString([]string{"plain", "  label:", "\t.text", "x // c"}[r.Intn(4)])
			b.WriteByte('\n')
		}
	}
}

// TestAsmprepBrick2bRandom fuzzes macro invocation/expansion against the Go
// authority. Deterministic seed so a failure reproduces.
func TestAsmprepBrick2bRandom(t *testing.T) {
	r := rand.New(rand.NewSource(0x2b2b))
	for i := 0; i < 600; i++ {
		var b strings.Builder
		genMacroProg(r, &b)
		src := b.String()
		want, wantErr := frontend.Preprocess([]byte(src), "r.s", frontend.PreprocessOptions{})

		m := loadAsmprep(t)
		got, gotErr := prepZ80(t, m, []byte(src), "r.s")

		if wantErr != nil {
			if !gotErr {
				t.Fatalf("case %d: expected error (Go: %v), prep_run succeeded\nsrc=%q\ngot=%q", i, wantErr, src, got)
			}
			continue
		}
		if gotErr {
			t.Fatalf("case %d: prep_run errored, Go succeeded\nsrc=%q\nwant=%q", i, src, want)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("case %d mismatch:\nsrc=%q\n got=%q\nwant=%q", i, src, got, want)
		}
	}
}
