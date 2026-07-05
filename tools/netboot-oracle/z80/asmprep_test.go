// asmprep_test.go — host-verification of src/asmprep.asm (i31b: the on-SAM
// assembler-source preprocessor; Brick 1).
//
// Drives prep_run under the flat-memory koron-go/z80 harness and byte-compares
// its expanded output against the Go authority, frontend.Preprocess
// (tools/sam-aarch64/frontend/preprocess.go), on .set/.if-only fixtures. The
// authority is imported directly (as the asmparse corpus test does), so there
// is no transcription to drift.
//
// Brick 1's scope is the conditional-assembly core: the leading
// `# <line> "<file>"` directive, `.set NAME, INT` capture + pass-through, and
// `.if SYMBOL`/`.else`/`.endif` with a nesting frame stack. Macros (Brick 2),
// .include + mid-stream # line emission (Brick 3) and the chain wiring
// (Brick 4) are separate items; the fixtures here deliberately contain no
// .macro/.endm/.include/macro-invocation, so brick-1 prep_run is byte-identical
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
