// asmlex_test.go — host-verification of src/asmlex.asm (i48c: the aarch64
// assembler-source tokenizer; Bricks B1–B1d).
//
// Drives lex_run under the flat-memory koron-go/z80 harness and compares every
// emitted token (kind + source-span + integer base + int64/char value) against
// a Go reference.
//
// The Go authority is tools/sam-aarch64/frontend/lexer.go (Lex). That package
// lives in a different Go module and pulls in the whole assembler front-end, so
// — as the editmodel harness test does for editmodel.go — refLex below is a
// faithful transcription of lexer.go's supported subset (kinds, spans, the
// 0x/0b base, the int64 value, char-literal values, local-label refs, and the
// decoded string-literal body), and the canonical hand cases assert kind
// sequences taken straight from frontend/lexer_test.go (authority-anchored).
// cpp line-directives are out of scope (a preprocessor artifact); on that input
// domain refLex is identical to frontend.Lex, since a cpp directive cannot arise
// (a line-start '#' lexes as a line comment in both).
package z80_test

import (
	"bytes"
	"encoding/binary"
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
		t.Fatalf("asmlex binary not built (%s); run `make asmlex-z80`", alBinPath)
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
	val  uint64 // for TOK_INT (numeric or character literal)
}

func refIsIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '.'
}
func refIsIdentCont(c byte) bool {
	return refIsIdentStart(c) || (c >= '0' && c <= '9')
}

// refDigitVal maps a digit char to its value (port of parseIntInBase's switch).
func refDigitVal(c byte) uint64 {
	switch {
	case c >= '0' && c <= '9':
		return uint64(c - '0')
	case c >= 'a' && c <= 'f':
		return uint64(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return uint64(c-'A') + 10
	}
	return 0
}

// refTryLocal tries to match a local-label ref at src[pos] (port of the
// tryLocal closures in readNumberOrLocal): one or two decimal digits + 'f'/'b',
// not followed by an ident-cont char. Returns the digit, the direction byte,
// the bytes consumed, and ok.
func refTryLocal(src []byte, pos int) (digit int, dir byte, adv int, ok bool) {
	n := len(src)
	isDig := func(c byte) bool { return c >= '0' && c <= '9' }
	try := func(nd int) (byte, bool) {
		if pos+nd >= n {
			return 0, false
		}
		for i := 0; i < nd; i++ {
			if !isDig(src[pos+i]) {
				return 0, false
			}
		}
		d := src[pos+nd]
		if d != 'f' && d != 'b' {
			return 0, false
		}
		if pos+nd+1 < n && refIsIdentCont(src[pos+nd+1]) {
			return 0, false
		}
		return d, true
	}
	if d, okk := try(2); okk {
		return int(src[pos]-'0')*10 + int(src[pos+1]-'0'), d, 3, true
	}
	if d, okk := try(1); okk {
		return int(src[pos] - '0'), d, 2, true
	}
	return 0, 0, 0, false
}

// refHexNibble maps a hex digit char to 0..15, or -1 if not a hex digit (port
// of lexer.go's hexNibble).
func refHexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// refEscape decodes a char-literal escape (port of readCharLit's switch). The
// same set, plus \xNN, applies inside string literals (see refLex's '"' case).
func refEscape(e byte) (byte, bool) {
	switch e {
	case 'n':
		return '\n', true
	case 'r':
		return '\r', true
	case 't':
		return '\t', true
	case '\\':
		return '\\', true
	case '\'':
		return '\'', true
	case '"':
		return '"', true
	case '0':
		return 0, true
	}
	return 0, false
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
// refTryLineDirective mirrors frontend/lexer.go tryConsumeLineDirective: a
// line-start `# <digit>+ "<file>"` (then newline/EOF) is a cpp line directive
// the lexer consumes for position, emitting no token. Returns the position past
// the directive (and its newline) and true on a match; (0,false) otherwise.
func refTryLineDirective(src []byte, pos int) (int, bool) {
	n := len(src)
	p := pos + 1 // past '#'
	if p >= n || src[p] != ' ' {
		return 0, false
	}
	for p < n && (src[p] == ' ' || src[p] == '\t') {
		p++
	}
	if p >= n || src[p] < '0' || src[p] > '9' {
		return 0, false
	}
	for p < n && src[p] >= '0' && src[p] <= '9' {
		p++
	}
	if p >= n || src[p] != ' ' {
		return 0, false
	}
	for p < n && (src[p] == ' ' || src[p] == '\t') {
		p++
	}
	if p >= n || src[p] != '"' {
		return 0, false
	}
	p++
	for p < n && src[p] != '"' && src[p] != '\n' {
		p++
	}
	if p >= n || src[p] != '"' {
		return 0, false
	}
	p++
	for p < n && (src[p] == ' ' || src[p] == '\t' || src[p] == '\r') {
		p++
	}
	if p < n && src[p] != '\n' {
		return 0, false
	}
	if p < n {
		p++ // consume the newline
	}
	return p, true
}

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
				if np, isDir := refTryLineDirective(src, pos); isDir {
					pos = np
					atLineStart = true // consumed the directive's newline
					continue
				}
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
			// Local-label ref first (port of readNumberOrLocal): 1-2 decimal
			// digits + 'f'/'b', not followed by an ident-cont char.
			if dig, dir, adv, ok := refTryLocal(src, pos); ok {
				pos += adv
				emit(refTok{kind: tLocalRef, val: uint64(dig), base: int(dir)})
				atLineStart = wasEOL
				continue
			}
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
			span := src[start:pos]
			var val uint64
			for _, dc := range span {
				val = val*uint64(base) + refDigitVal(dc)
			}
			emit(refTok{kind: tInt, span: span, base: base, val: val})
		case c == '"':
			// String literal (port of readString): decode the body into
			// `body`, resolving escapes (n/r/t/\/"/'/0 and \xNN). The token's
			// span is the decoded body, matching Go's Tok.Bytes.
			pos++ // opening "
			var body []byte
			for {
				if pos >= n {
					return toks, false // unterminated string literal
				}
				ch := src[pos]
				pos++
				if ch == '"' {
					break
				}
				if ch == '\\' {
					if pos >= n {
						return toks, false // unterminated string escape
					}
					esc := src[pos]
					pos++
					if esc == 'x' {
						if pos+1 >= n {
							return toks, false // truncated \xNN
						}
						hi := refHexNibble(src[pos])
						pos++
						lo := refHexNibble(src[pos])
						pos++
						if hi < 0 || lo < 0 {
							return toks, false // bad \xNN
						}
						body = append(body, byte(hi*16+lo))
						continue
					}
					dec, ok := refEscape(esc)
					if !ok {
						return toks, false // unknown escape
					}
					body = append(body, dec)
					continue
				}
				body = append(body, ch)
			}
			emit(refTok{kind: tString, span: body})
		case c == '\'':
			pos++ // opening '
			if pos >= n {
				return toks, false
			}
			ch := src[pos]
			pos++
			if ch == '\\' {
				if pos >= n {
					return toks, false
				}
				dec, ok := refEscape(src[pos])
				if !ok {
					return toks, false
				}
				ch = dec
				pos++
			}
			if pos >= n || src[pos] != '\'' {
				return toks, false
			}
			pos++ // closing '
			emit(refTok{kind: tInt, val: uint64(ch)})
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
		rec := mac.Read(symToks+uint16(i*14), 14)
		kind := int(rec[0])
		ptr := uint16(rec[1]) | uint16(rec[2])<<8
		ln := int(uint16(rec[3]) | uint16(rec[4])<<8)
		base := int(rec[5])
		val := binary.LittleEndian.Uint64(rec[6:14])
		var span []byte
		if ln > 0 {
			span = mac.Read(ptr, ln)
		}
		toks = append(toks, refTok{kind: kind, span: span, base: base, val: val})
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
		if want[i].kind == tInt {
			if got[i].base != want[i].base {
				t.Fatalf("%s: tok[%d] int base = %d, want %d", label, i, got[i].base, want[i].base)
			}
			if got[i].val != want[i].val {
				t.Fatalf("%s: tok[%d] int value = %d, want %d", label, i, got[i].val, want[i].val)
			}
		}
		if want[i].kind == tLocalRef {
			// val holds the label digit; base holds the 'f'/'b' direction char.
			if got[i].val != want[i].val {
				t.Fatalf("%s: tok[%d] localref digit = %d, want %d", label, i, got[i].val, want[i].val)
			}
			if got[i].base != want[i].base {
				t.Fatalf("%s: tok[%d] localref dir = %q, want %q", label, i, rune(got[i].base), rune(want[i].base))
			}
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
		// number bases — all three encode 42 (value checked via compareToks)
		{"42 0x2a 0b101010\n", []int{tInt, tInt, tInt, tEOL, tEOF}},
		// large hex value (exercises the 64-bit accumulator)
		{".quad 0x0123456789abcdef\n", []int{tIdent, tInt, tEOL, tEOF}},
		// character literals lex as TOK_INT with the char's value
		{"mov x0, #'A'\n", []int{tIdent, tIdent, tComma, tHash, tInt, tEOL, tEOF}},
		{"'A' '0' '\\n' '\\t' '\\\\' ' '\n", []int{tInt, tInt, tInt, tInt, tInt, tInt, tEOL, tEOF}},
		// string literals: span is the DECODED body (escapes resolved), checked
		// against refLex via compareToks. `.ascii "hi\n"` is the canonical case.
		{".ascii \"hi\\n\"\n", []int{tIdent, tString, tEOL, tEOF}},
		// \xNN hex escapes (\x41\x42 -> "AB"), \t, \0, escaped quote inside body
		{".asciz \"\\x41\\x42\"\n", []int{tIdent, tString, tEOL, tEOF}},
		{".ascii \"tab\\tend\\0\"\n", []int{tIdent, tString, tEOL, tEOF}},
		{".ascii \"a\\\"b\"\n", []int{tIdent, tString, tEOL, tEOF}},
		// empty string + single-char string, adjacent
		{"\"\" \"a\"\n", []int{tString, tString, tEOL, tEOF}},
		// memory operand shape + writeback
		{"ldr x0, [x1, #8]!\n", []int{tIdent, tIdent, tComma, tLBracket, tIdent, tComma, tHash, tInt, tRBracket, tBang, tEOL, tEOF}},
		// directive + shift operator
		{".word 1 << 4\n", []int{tIdent, tInt, tShl, tInt, tEOL, tEOF}},
		// label + assorted operators
		{"loop: x0 + x1 - 1 & 2 | 3 ^ ~4\n", []int{tIdent, tColon, tIdent, tPlus, tIdent, tMinus, tInt, tAmp, tInt, tPipe, tInt, tCaret, tTilde, tInt, tEOL, tEOF}},
		// '#' at start of line: a normal note is a line comment...
		{"# a note\nmov x0\n", []int{tLineComment, tEOL, tIdent, tIdent, tEOL, tEOF}},
		// ...but a cpp line directive (# <n> "<file>") emitted by the preprocessor
		// is consumed for position, emitting no token (like the host lexer).
		{"# 5 \"foo.s\"\nmov x0\n", []int{tIdent, tIdent, tEOL, tEOF}},
		// consecutive directives (a file + macro boundary) are both consumed.
		{"# 1 \"a.s\"\n# 2 \"a.s\"\nret\n", []int{tIdent, tEOL, tEOF}},
		// a '#' line that is not a well-formed directive stays a line comment:
		{"# 5 notaquote\nx0\n", []int{tLineComment, tEOL, tIdent, tEOL, tEOF}},
		{"#5 \"x.s\"\nx0\n", []int{tLineComment, tEOL, tIdent, tEOL, tEOF}},
		// local-label refs (frontend/lexer_test.go TestLexLocalLabelRef + 2-digit)
		{"b 1f\n", []int{tIdent, tLocalRef, tEOL, tEOF}},
		{"bne 10b cbz x0, 2f\n", []int{tIdent, tLocalRef, tIdent, tIdent, tComma, tLocalRef, tEOL, tEOF}},
		// 0b followed by a digit is a binary literal, not a 0/'b' local ref
		{"0b101 5f\n", []int{tInt, tLocalRef, tEOL, tEOF}},
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
	"0x0123456789abcdef", "65535", "0xcafef00d",
	"1f", "1b", "2f", "9b", "10f", "42b", "99f",
	"'A'", "'z'", "'0'", "' '", "'\\n'", "'\\t'", "'\\\\'", "'\\''",
	"\"\"", "\"hi\"", "\"a b\"", "\"\\n\\t\\r\\0\"", "\"\\\\\"", "\"esc\\\"q\"",
	"\"\\x41\\x7f\\xFF\"", "\".text\"", "\"%type\"",
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

// TestAsmLexError checks that the lexer's error cases set LEX_ERR (and that
// refLex agrees they are errors). Each case is a real Z80 path with teeth: an
// unterminated block comment, an unterminated string, a truncated/bad \xNN
// escape, and an unknown string escape.
func TestAsmLexError(t *testing.T) {
	mac := loadAsmlex(t)
	cases := []struct {
		name string
		src  string
	}{
		{"unterminated block comment", "add /* unterminated"},
		{"unterminated string", ".ascii \"no close"},
		{"unterminated string escape", ".ascii \"ends with backslash\\"},
		{"bad \\xNN (quote eaten as low nibble)", ".ascii \"\\x4\""},
		{"truncated \\xNN (no digits before close)", ".ascii \"\\x\""},
		{"bad \\xNN (non-hex digit)", ".ascii \"\\xZZ\""},
		{"unknown string escape", ".ascii \"\\q\""},
	}
	for _, c := range cases {
		_, errFlag := lexZ80(t, mac, []byte(c.src))
		if !errFlag {
			t.Errorf("%s (%q): LEX_ERR not set", c.name, c.src)
		}
		if _, ok := refLex([]byte(c.src)); ok {
			t.Errorf("%s (%q): refLex should report an error", c.name, c.src)
		}
	}
}
