package translate

import (
	"fmt"
	"unicode"
)

type TokKind byte

const (
	TokEOF TokKind = iota
	TokEOL
	TokIdent
	TokInt
	TokString
	TokComma
	TokHash
	TokColon
	TokBang
	TokDot
	TokLBracket
	TokRBracket
	TokLParen
	TokRParen
	TokPlus
	TokMinus
	TokStar
	TokSlash
	TokAmp
	TokPipe
	TokCaret
	TokTilde
	TokShl
	TokShr
	TokLineComment
	TokBlockComment
	TokLocalRef
)

type Tok struct {
	Kind     TokKind
	Pos      Position
	Text     string
	Int      int64
	Bytes    []byte
	Digit    byte
	LocalDir byte
}

// Lex tokenises src; "path" is used for error positions.
func Lex(src []byte, path string) ([]Tok, error) {
	l := &lexer{src: src, path: path, line: 1, col: 1, atLineStart: true}
	var toks []Tok
	for {
		t, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.Kind == TokEOF {
			return toks, nil
		}
		l.atLineStart = t.Kind == TokEOL
	}
}

type lexer struct {
	src         []byte
	path        string
	pos         int
	line, col   int
	atLineStart bool // true at file start or after most recently emitted TokEOL
}

func (l *lexer) pos2() Position {
	return Position{File: l.path, Line: l.line, Col: l.col}
}

func (l *lexer) peek() byte {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

func (l *lexer) advance() byte {
	c := l.src[l.pos]
	l.pos++
	if c == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return c
}

func (l *lexer) next() (Tok, error) {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == ' ' || c == '\t' || c == '\r' {
			l.advance()
			continue
		}
		break
	}
	if l.pos >= len(l.src) {
		return Tok{Kind: TokEOF, Pos: l.pos2()}, nil
	}
	start := l.pos2()
	c := l.peek()
	switch {
	case c == '\n':
		l.advance()
		return Tok{Kind: TokEOL, Pos: start}, nil
	case c == ',':
		l.advance()
		return Tok{Kind: TokComma, Pos: start}, nil
	case c == '#':
		// GNU as treats # at the start of a logical line as a
		// line comment. Mid-line, # is the immediate-prefix used
		// in operands like `add x0, x0, #4`.
		//
		// The preprocessor emits cpp-style line directives of the form
		//     # <line> "<file>"
		// at file/macro boundaries. When we see one at the start of a
		// line, consume it and update the lexer position so subsequent
		// tokens are attributed to the original source location.
		if l.atLineStart {
			if l.tryConsumeLineDirective() {
				return l.next()
			}
			return l.readLineCommentChar(start, '#')
		}
		l.advance()
		return Tok{Kind: TokHash, Pos: start}, nil
	case c == ':':
		l.advance()
		return Tok{Kind: TokColon, Pos: start}, nil
	case c == '!':
		l.advance()
		return Tok{Kind: TokBang, Pos: start}, nil
	case c == '.':
		if l.pos+1 < len(l.src) && isIdentStart(l.src[l.pos+1]) {
			return l.readIdent()
		}
		l.advance()
		return Tok{Kind: TokDot, Pos: start}, nil
	case c == '[':
		l.advance()
		return Tok{Kind: TokLBracket, Pos: start}, nil
	case c == ']':
		l.advance()
		return Tok{Kind: TokRBracket, Pos: start}, nil
	case c == '(':
		l.advance()
		return Tok{Kind: TokLParen, Pos: start}, nil
	case c == ')':
		l.advance()
		return Tok{Kind: TokRParen, Pos: start}, nil
	case c == '+':
		l.advance()
		return Tok{Kind: TokPlus, Pos: start}, nil
	case c == '-':
		l.advance()
		return Tok{Kind: TokMinus, Pos: start}, nil
	case c == '*':
		l.advance()
		return Tok{Kind: TokStar, Pos: start}, nil
	case c == '/':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '/' {
			return l.readLineComment(start)
		}
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '*' {
			return l.readBlockComment(start)
		}
		l.advance()
		return Tok{Kind: TokSlash, Pos: start}, nil
	case c == '&':
		l.advance()
		return Tok{Kind: TokAmp, Pos: start}, nil
	case c == '|':
		l.advance()
		return Tok{Kind: TokPipe, Pos: start}, nil
	case c == '^':
		l.advance()
		return Tok{Kind: TokCaret, Pos: start}, nil
	case c == '~':
		l.advance()
		return Tok{Kind: TokTilde, Pos: start}, nil
	case c == '<':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '<' {
			l.advance()
			l.advance()
			return Tok{Kind: TokShl, Pos: start}, nil
		}
		return Tok{}, newErr(start, "unexpected '<' (did you mean '<<'?)")
	case c == '>':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '>' {
			l.advance()
			l.advance()
			return Tok{Kind: TokShr, Pos: start}, nil
		}
		return Tok{}, newErr(start, "unexpected '>' (did you mean '>>'?)")
	case c == '"':
		return l.readString(start)
	case c == '\'':
		return l.readCharLit(start)
	case unicode.IsDigit(rune(c)):
		return l.readNumberOrLocal(start)
	case isIdentStart(c):
		return l.readIdent()
	}
	return Tok{}, newErr(start, "unexpected character %q", c)
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '.'
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func (l *lexer) readIdent() (Tok, error) {
	start := l.pos2()
	startPos := l.pos
	for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) {
		l.advance()
	}
	return Tok{Kind: TokIdent, Pos: start, Text: string(l.src[startPos:l.pos])}, nil
}

func (l *lexer) readNumberOrLocal(start Position) (Tok, error) {
	// Try to match a local-label reference: one or two decimal digits followed
	// immediately by 'f' or 'b' (not followed by another ident character).
	// One-digit: Nf / Nb  (N in '0'..'9')
	// Two-digit: NNf / NNb (both chars in '0'..'9')
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	tryLocal := func(nDigits int) bool {
		if l.pos+nDigits >= len(l.src) {
			return false
		}
		for i := 0; i < nDigits; i++ {
			if !isDigit(l.src[l.pos+i]) {
				return false
			}
		}
		dir := l.src[l.pos+nDigits]
		if dir != 'f' && dir != 'b' {
			return false
		}
		// The char after 'f'/'b' must not be an ident continuation character.
		if l.pos+nDigits+1 < len(l.src) && isIdentCont(l.src[l.pos+nDigits+1]) {
			return false
		}
		return true
	}
	// Check two-digit first (more specific than one-digit).
	if tryLocal(2) {
		hi := l.advance() - '0'
		lo := l.advance() - '0'
		dir := l.advance()
		d := hi*10 + lo
		return Tok{Kind: TokLocalRef, Pos: start, Digit: d, LocalDir: dir}, nil
	}
	if tryLocal(1) {
		d := l.advance() - '0'
		dir := l.advance()
		return Tok{Kind: TokLocalRef, Pos: start, Digit: d, LocalDir: dir}, nil
	}
	return l.readNumber(start)
}

func (l *lexer) readNumber(start Position) (Tok, error) {
	startPos := l.pos
	base := 10
	c := l.peek()
	if c == '0' && l.pos+1 < len(l.src) {
		switch l.src[l.pos+1] {
		case 'x', 'X':
			base = 16
			l.advance()
			l.advance()
			startPos = l.pos
		case 'b', 'B':
			base = 2
			l.advance()
			l.advance()
			startPos = l.pos
		}
	}
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		ok := false
		switch base {
		case 10:
			ok = c >= '0' && c <= '9'
		case 16:
			ok = (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		case 2:
			ok = c == '0' || c == '1'
		}
		if !ok {
			break
		}
		l.advance()
	}
	text := string(l.src[startPos:l.pos])
	v, err := parseIntInBase(text, base)
	if err != nil {
		return Tok{}, newErr(start, "bad integer literal: %s", err)
	}
	return Tok{Kind: TokInt, Pos: start, Int: v, Text: text}, nil
}

func (l *lexer) readCharLit(start Position) (Tok, error) {
	l.advance()
	if l.pos >= len(l.src) {
		return Tok{}, newErr(start, "unterminated char literal")
	}
	c := l.advance()
	if c == '\\' {
		if l.pos >= len(l.src) {
			return Tok{}, newErr(start, "unterminated char escape")
		}
		esc := l.advance()
		switch esc {
		case 'n':
			c = '\n'
		case 't':
			c = '\t'
		case '\\':
			c = '\\'
		case '\'':
			c = '\''
		case '"':
			c = '"'
		case '0':
			c = 0
		default:
			return Tok{}, newErr(start, "unknown char escape '\\%c'", esc)
		}
	}
	if l.pos >= len(l.src) || l.advance() != '\'' {
		return Tok{}, newErr(start, "unterminated char literal")
	}
	return Tok{Kind: TokInt, Pos: start, Int: int64(c)}, nil
}

func (l *lexer) readString(start Position) (Tok, error) {
	l.advance()
	var body []byte
	for {
		if l.pos >= len(l.src) {
			return Tok{}, newErr(start, "unterminated string literal")
		}
		c := l.advance()
		if c == '"' {
			break
		}
		if c == '\\' {
			if l.pos >= len(l.src) {
				return Tok{}, newErr(start, "unterminated string escape")
			}
			esc := l.advance()
			switch esc {
			case 'n':
				body = append(body, '\n')
			case 't':
				body = append(body, '\t')
			case '\\':
				body = append(body, '\\')
			case '"':
				body = append(body, '"')
			case '\'':
				body = append(body, '\'')
			case '0':
				body = append(body, 0)
			case 'x':
				if l.pos+1 >= len(l.src) {
					return Tok{}, newErr(start, "truncated \\xNN escape")
				}
				hi := hexNibble(l.advance())
				lo := hexNibble(l.advance())
				if hi < 0 || lo < 0 {
					return Tok{}, newErr(start, "bad \\xNN escape")
				}
				body = append(body, byte(hi*16+lo))
			default:
				return Tok{}, newErr(start, "unknown string escape '\\%c'", esc)
			}
			continue
		}
		body = append(body, c)
	}
	return Tok{Kind: TokString, Pos: start, Bytes: body}, nil
}

func (l *lexer) readLineComment(start Position) (Tok, error) {
	l.advance()
	l.advance()
	startBody := l.pos
	for l.pos < len(l.src) && l.src[l.pos] != '\n' {
		l.advance()
	}
	return Tok{Kind: TokLineComment, Pos: start, Bytes: l.src[startBody:l.pos]}, nil
}

// readLineCommentChar consumes a line comment introduced by a single
// character (e.g. `#` at start of line). The opener character is
// included verbatim in the body, mirroring GNU as behaviour.
func (l *lexer) readLineCommentChar(start Position, opener byte) (Tok, error) {
	l.advance() // consume the opener
	startBody := l.pos
	for l.pos < len(l.src) && l.src[l.pos] != '\n' {
		l.advance()
	}
	return Tok{Kind: TokLineComment, Pos: start, Bytes: l.src[startBody:l.pos]}, nil
}

func (l *lexer) readBlockComment(start Position) (Tok, error) {
	l.advance()
	l.advance()
	startBody := l.pos
	for {
		if l.pos+1 >= len(l.src) {
			return Tok{}, newErr(start, "unterminated block comment")
		}
		if l.src[l.pos] == '*' && l.src[l.pos+1] == '/' {
			body := l.src[startBody:l.pos]
			l.advance()
			l.advance()
			return Tok{Kind: TokBlockComment, Pos: start, Bytes: body}, nil
		}
		l.advance()
	}
}

// tryConsumeLineDirective attempts to consume a cpp-style line directive of
// the form
//
//	# <line> "<file>"
//
// starting at the current position (which must be a '#' at start of line).
// On success it advances past the directive and the trailing newline,
// updates l.line / l.path, and returns true. On failure it returns false
// without advancing.
func (l *lexer) tryConsumeLineDirective() bool {
	// Must look like "# <digit>" — otherwise treat as a normal #-comment.
	p := l.pos + 1
	// Require exactly one space (cpp emits a single space; our preprocessor
	// also emits exactly one) — but be lenient and accept 1+ whitespace.
	if p >= len(l.src) || l.src[p] != ' ' {
		return false
	}
	for p < len(l.src) && (l.src[p] == ' ' || l.src[p] == '\t') {
		p++
	}
	if p >= len(l.src) || l.src[p] < '0' || l.src[p] > '9' {
		return false
	}
	lineStart := p
	for p < len(l.src) && l.src[p] >= '0' && l.src[p] <= '9' {
		p++
	}
	lineNum := 0
	for _, c := range []byte(string(l.src[lineStart:p])) {
		lineNum = lineNum*10 + int(c-'0')
	}
	// Require " "<file>"".
	if p >= len(l.src) || l.src[p] != ' ' {
		return false
	}
	for p < len(l.src) && (l.src[p] == ' ' || l.src[p] == '\t') {
		p++
	}
	if p >= len(l.src) || l.src[p] != '"' {
		return false
	}
	p++
	pathStart := p
	for p < len(l.src) && l.src[p] != '"' && l.src[p] != '\n' {
		p++
	}
	if p >= len(l.src) || l.src[p] != '"' {
		return false
	}
	pathStr := string(l.src[pathStart:p])
	p++
	// Skip trailing whitespace and require newline (or EOF).
	for p < len(l.src) && (l.src[p] == ' ' || l.src[p] == '\t' || l.src[p] == '\r') {
		p++
	}
	if p < len(l.src) && l.src[p] != '\n' {
		return false
	}
	if p < len(l.src) {
		p++ // consume newline
	}
	// Commit: jump position and update line/file. lineNum names the line
	// number that the *next* source line should have, since we have already
	// consumed the directive's terminating newline. The col is reset to 1.
	l.pos = p
	l.line = lineNum
	l.col = 1
	l.path = pathStr
	l.atLineStart = true
	return true
}

func hexNibble(c byte) int {
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

func parseIntInBase(s string, base int) (int64, error) {
	var v int64
	for _, c := range []byte(s) {
		d := -1
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		}
		if d < 0 || d >= base {
			return 0, fmt.Errorf("invalid digit %q", c)
		}
		v = v*int64(base) + int64(d)
	}
	return v, nil
}
