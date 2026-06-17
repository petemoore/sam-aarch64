// asmlex_test.go — host-verification of src/asmlex.asm (i48c Brick B1: the
// aarch64 assembler-source tokenizer).
//
// Drives lex_run under the flat-memory koron-go/z80 harness and compares every
// emitted token (kind + source-span + integer base) against a Go reference.
//
// The Go authority is tools/sam-aarch64/frontend/lexer.go (Lex). That package
// lives in a different Go module and pulls in the whole assembler front-end, so
// — as the editmodel harness test does for editmodel.go — refLex below is a
// faithful transcription of lexer.go's Brick-B1 subset (kinds, spans, and the
// 0x/0b base), and the canonical hand cases assert kind sequences taken straight
// from frontend/lexer_test.go (authority-anchored). On B1's input domain (no
// string/char literals, no local-label refs, no cpp line-directives — all
// deferred to B1b) refLex is identical to frontend.Lex, since none of the
// deferred constructs can arise without a '"', '\”, a digit-then-f/b, or a
// quoted line directive, none of which the corpus emits.
package z80_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	alBinPath = "../../../build/asmlex.bin"
	alMapPath = "../../../build/asmlex.map"
)

// Token kinds — MUST match TokKind in lexer.go and TOK_* in asmlex.asm.
const (
	tEOF = iota
	tEOL
	tIdent
	tInt
	tString
	tComma
	tHash
	tColon
	tBang
	tDot
	tLBracket
	tRBracket
	tLParen
	tRParen
	tPlus
	tMinus
	tStar
	tSlash
	tAmp
	tPipe
	tCaret
	tTilde
	tShl
	tShr
	tLineComment
	tBlockComment
	tLocalRef
	tEquals
	tPercent
)

func loadAsmlex(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(alBinPath); err != nil {
		t.Skipf("asmlex binary not built (%s); run `make asmlex-z80`", alBinPath)
	}
	mac, err := z80h.Load(alBinPath, alMapPath)
	if err != nil {
		t.Fatalf("load asmlex: %v", err)
	}
	return mac
}

// refTok is one reference token: kind, the source span (empty for tokens that
// carry no span), and the base for integer literals.
type refTok struct {
	kind int
	span []byte
	base int
}

func refIsIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '.'
}
func refIsIdentCont(c byte) bool {
	return refIsIdentStart(c) || (c >= '0' && c <= '9')
}
func refDigitForBase(c byte, base int) bool {
	switch base {
	case 2:
		return c == '0' || c == '1'
	case 16:
		return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
	default:
		return c >= '0' && c <= '9'
	}
}

// refLex is a faithful transcription of lexer.go's next() for Brick B1's token
// subset. Returns the tokens (including the trailing EOF) and ok=false if a
// lexical error was hit (unterminated block comment / lone '<' or '>' /
// unexpected character) — matching how asmlex sets LEX_ERR.
func refLex(src []byte) (toks []refTok, ok bool) {
	pos := 0
	n := len(src)
	atLineStart := true
	emit := func(t refTok) { toks = append(toks, t) }
	for {
		for pos < n && (src[pos] == ' ' || src[pos] == '\t' || src[pos] == '\r') {
			pos++
		}
		if pos >= n {
			emit(refTok{kind: tEOF})
			return toks, true
		}
		c := src[pos]
		wasEOL := false
		switch {
		case c == '\n':
			pos++
			emit(refTok{kind: tEOL})
			wasEOL = true
		case c == ',':
			pos++
			emit(refTok{kind: tComma})
		case c == '#':
			if atLineStart {
				pos++ // consume '#'
				start := pos
				for pos < n && src[pos] != '\n' {
					pos++
				}
				emit(refTok{kind: tLineComment, span: src[start:pos]})
			} else {
				pos++
				emit(refTok{kind: tHash})
			}
		case c == ':':
			pos++
			emit(refTok{kind: tColon})
		case c == '!':
			pos++
			emit(refTok{kind: tBang})
		case c == '.':
			if pos+1 < n && refIsIdentStart(src[pos+1]) {
				start := pos
				for pos < n && refIsIdentCont(src[pos]) {
					pos++
				}
				emit(refTok{kind: tIdent, span: src[start:pos]})
			} else {
				pos++
				emit(refTok{kind: tDot})
			}
		case c == '[':
			pos++
			emit(refTok{kind: tLBracket})
		case c == ']':
			pos++
			emit(refTok{kind: tRBracket})
		case c == '(':
			pos++
			emit(refTok{kind: tLParen})
		case c == ')':
			pos++
			emit(refTok{kind: tRParen})
		case c == '+':
			pos++
			emit(refTok{kind: tPlus})
		case c == '-':
			pos++
			emit(refTok{kind: tMinus})
		case c == '*':
			pos++
			emit(refTok{kind: tStar})
		case c == '/':
			if pos+1 < n && src[pos+1] == '/' {
				pos += 2
				start := pos
				for pos < n && src[pos] != '\n' {
					pos++
				}
				emit(refTok{kind: tLineComment, span: src[start:pos]})
			} else if pos+1 < n && src[pos+1] == '*' {
				pos += 2
				start := pos
				for {
					if pos+1 >= n {
						return toks, false // unterminated
					}
					if src[pos] == '*' && src[pos+1] == '/' {
						emit(refTok{kind: tBlockComment, span: src[start:pos]})
						pos += 2
						break
					}
					pos++
				}
			} else {
				pos++
				emit(refTok{kind: tSlash})
			}
		case c == '&':
			pos++
			emit(refTok{kind: tAmp})
		case c == '|':
			pos++
			emit(refTok{kind: tPipe})
		case c == '^':
			pos++
			emit(refTok{kind: tCaret})
		case c == '~':
			pos++
			emit(refTok{kind: tTilde})
		case c == '=':
			pos++
			emit(refTok{kind: tEquals})
		case c == '%':
			pos++
			emit(refTok{kind: tPercent})
		case c == '<':
			if pos+1 < n && src[pos+1] == '<' {
				pos += 2
				emit(refTok{kind: tShl})
			} else {
				return toks, false
			}
		case c == '>':
			if pos+1 < n && src[pos+1] == '>' {
				pos += 2
				emit(refTok{kind: tShr})
			} else {
				return toks, false
			}
		case c >= '0' && c <= '9':
			base := 10
			if c == '0' && pos+1 < n {
				switch src[pos+1] {
				case 'x', 'X':
					base = 16
					pos += 2
				case 'b', 'B':
					base = 2
					pos += 2
				}
			}
			start := pos
			for pos < n && refDigitForBase(src[pos], base) {
				pos++
			}
			emit(refTok{kind: tInt, span: src[start:pos], base: base})
		case refIsIdentStart(c):
			start := pos
			for pos < n && refIsIdentCont(src[pos]) {
				pos++
			}
			emit(refTok{kind: tIdent, span: src[start:pos]})
		default:
			return toks, false
		}
		atLineStart = wasEOL
	}
}

// lexZ80 runs the source through asmlex and returns the emitted tokens and the
// LEX_ERR flag.
func lexZ80(t *testing.T, mac *z80h.Machine, src []byte) (toks []refTok, errFlag bool) {
	t.Helper()
	symSrc, _ := mac.Sym("LEX_SRC")
	symToks, _ := mac.Sym("LEX_TOKS")
	symErr, _ := mac.Sym("LEX_ERR")

	mac.Write(symSrc, src)
	res, err := mac.CallEntry("lex_run", z80h.Entry{BC: uint16(len(src))})
	if err != nil {
		t.Fatalf("lex_run: %v", err)
	}
	count := int(res.BC)
	for i := 0; i < count; i++ {
		rec := mac.Read(symToks+uint16(i*6), 6)
		kind := int(rec[0])
		ptr := uint16(rec[1]) | uint16(rec[2])<<8
		ln := int(uint16(rec[3]) | uint16(rec[4])<<8)
		base := int(rec[5])
		var span []byte
		if ln > 0 {
			span = mac.Read(ptr, ln)
		}
		toks = append(toks, refTok{kind: kind, span: span, base: base})
	}
	errFlag = mac.Read(symErr, 1)[0] != 0
	return toks, errFlag
}

// kindName maps a kind to a label for diagnostics.
func kindName(k int) string {
	names := []string{"EOF", "EOL", "Ident", "Int", "String", "Comma", "Hash", "Colon",
		"Bang", "Dot", "LBracket", "RBracket", "LParen", "RParen", "Plus", "Minus",
		"Star", "Slash", "Amp", "Pipe", "Caret", "Tilde", "Shl", "Shr", "LineComment",
		"BlockComment", "LocalRef", "Equals", "Percent"}
	if k >= 0 && k < len(names) {
		return names[k]
	}
	return fmt.Sprintf("kind%d", k)
}

// compareToks asserts the Z80 token stream matches the reference token stream
// (kind, span, and base for ints).
func compareToks(t *testing.T, label string, got, want []refTok) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d tokens, want %d\n got:  %s\n want: %s",
			label, len(got), len(want), dumpToks(got), dumpToks(want))
	}
	for i := range want {
		if got[i].kind != want[i].kind {
			t.Fatalf("%s: tok[%d] kind = %s, want %s (full: got %s / want %s)",
				label, i, kindName(got[i].kind), kindName(want[i].kind), dumpToks(got), dumpToks(want))
		}
		if !bytes.Equal(got[i].span, want[i].span) {
			t.Fatalf("%s: tok[%d] (%s) span = %q, want %q",
				label, i, kindName(got[i].kind), got[i].span, want[i].span)
		}
		if want[i].kind == tInt && got[i].base != want[i].base {
			t.Fatalf("%s: tok[%d] int base = %d, want %d", label, i, got[i].base, want[i].base)
		}
	}
}

func dumpToks(toks []refTok) string {
	s := ""
	for _, tk := range toks {
		s += kindName(tk.kind)
		if len(tk.span) > 0 {
			s += fmt.Sprintf("(%q)", tk.span)
		}
		s += " "
	}
	return s
}

// TestAsmLexHandCases pins exact kind sequences taken from frontend/lexer_test.go
// (authority-anchored) plus a few B1-subset extensions.
func TestAsmLexHandCases(t *testing.T) {
	mac := loadAsmlex(t)
	cases := []struct {
		src   string
		kinds []int
	}{
		// frontend/lexer_test.go TestLexBasic
		{"add x0, x1, #4\n", []int{tIdent, tIdent, tComma, tIdent, tComma, tHash, tInt, tEOL, tEOF}},
		// frontend/lexer_test.go TestLexComments
		{"// hi\nadd /* mid */ x0\n", []int{tLineComment, tEOL, tIdent, tBlockComment, tIdent, tEOL, tEOF}},
		// number bases (values are B1b; kinds + bases here)
		{"42 0x2a 0b101010\n", []int{tInt, tInt, tInt, tEOL, tEOF}},
		// memory operand shape + writeback
		{"ldr x0, [x1, #8]!\n", []int{tIdent, tIdent, tComma, tLBracket, tIdent, tComma, tHash, tInt, tRBracket, tBang, tEOL, tEOF}},
		// directive + shift operator
		{".word 1 << 4\n", []int{tIdent, tInt, tShl, tInt, tEOL, tEOF}},
		// label + assorted operators
		{"loop: x0 + x1 - 1 & 2 | 3 ^ ~4\n", []int{tIdent, tColon, tIdent, tPlus, tIdent, tMinus, tInt, tAmp, tInt, tPipe, tInt, tCaret, tTilde, tInt, tEOL, tEOF}},
		// '#' at start of line is a comment (cpp directive handling is B1b)
		{"# a note\nmov x0\n", []int{tLineComment, tEOL, tIdent, tIdent, tEOL, tEOF}},
		// lone dot, parens, equals, percent, shr
		{". ( ) = %x 8 >> 2\n", []int{tDot, tLParen, tRParen, tEquals, tPercent, tIdent, tInt, tShr, tInt, tEOL, tEOF}},
		// empty input
		{"", []int{tEOF}},
	}
	for _, c := range cases {
		got, errFlag := lexZ80(t, mac, []byte(c.src))
		if errFlag {
			t.Errorf("%q: LEX_ERR set unexpectedly", c.src)
			continue
		}
		want, ok := refLex([]byte(c.src))
		if !ok {
			t.Fatalf("%q: refLex reported error on a valid case", c.src)
		}
		// Authority-anchored kind check.
		if len(got) != len(c.kinds) {
			t.Errorf("%q: got %d tokens, want %d: %s", c.src, len(got), len(c.kinds), dumpToks(got))
			continue
		}
		for i, k := range c.kinds {
			if got[i].kind != k {
				t.Errorf("%q: tok[%d] kind = %s, want %s", c.src, i, kindName(got[i].kind), kindName(k))
			}
		}
		// Full span/base check against the reference.
		compareToks(t, fmt.Sprintf("%q", c.src), got, want)
	}
}

// alPieces are B1-domain token fragments; joined with spaces they form an
// unambiguous (no accidental string/char/local-ref/comment) token stream.
var alPieces = []string{
	"add", "sub", "ldr", "mov", "b", "ret", "cmp", "orr", "x0", "x1", "x29", "w5",
	"sp", "xzr", "lr", "loop", "_start", "foo", ".text", ".word", ".quad", ".align",
	"0", "1", "4", "42", "255", "0x1f", "0xFF", "0xdead", "0b1010", "0b0", "1000",
	"#", ",", ":", "!", "[", "]", "(", ")", "+", "-", "*", "/", "&", "|", "^", "~",
	"=", "%", "<<", ">>", ".", "/* blk */",
}

// TestAsmLexFuzz compares asmlex against refLex over random B1-domain source.
func TestAsmLexFuzz(t *testing.T) {
	mac := loadAsmlex(t)
	for _, seed := range []int64{1, 42, 137, 999, 31337} {
		rng := rand.New(rand.NewSource(seed))
		var src []byte
		lines := 6 + rng.Intn(10)
		for li := 0; li < lines; li++ {
			pieces := 1 + rng.Intn(8)
			for pi := 0; pi < pieces; pi++ {
				src = append(src, alPieces[rng.Intn(len(alPieces))]...)
				src = append(src, ' ')
			}
			// ~30% of lines end in a // line comment.
			if rng.Intn(10) < 3 {
				src = append(src, []byte("// trailing comment text")...)
			}
			src = append(src, '\n')
		}
		want, ok := refLex(src)
		if !ok {
			t.Fatalf("seed %d: refLex reported error on generated B1 source:\n%s", seed, src)
		}
		got, errFlag := lexZ80(t, mac, src)
		if errFlag {
			t.Fatalf("seed %d: LEX_ERR set on valid B1 source:\n%s", seed, src)
		}
		compareToks(t, fmt.Sprintf("seed%d", seed), got, want)
		t.Logf("seed=%d: %d source bytes, %d tokens matched", seed, len(src), len(got))
	}
}

// TestAsmLexError checks that an unterminated block comment sets LEX_ERR.
func TestAsmLexError(t *testing.T) {
	mac := loadAsmlex(t)
	_, errFlag := lexZ80(t, mac, []byte("add /* unterminated"))
	if !errFlag {
		t.Errorf("unterminated block comment: LEX_ERR not set")
	}
	if _, ok := refLex([]byte("add /* unterminated")); ok {
		t.Errorf("refLex: unterminated block comment should report error")
	}
}
