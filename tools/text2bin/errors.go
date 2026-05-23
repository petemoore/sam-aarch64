package main

import "fmt"

// Position is a source location used in error messages.
type Position struct {
	File string
	Line int
	Col  int
}

func (p Position) String() string {
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
}

// LexError is fail-fast lex/parse errors with location.
type LexError struct {
	Pos Position
	Msg string
}

func (e *LexError) Error() string {
	return fmt.Sprintf("%s: %s", e.Pos.String(), e.Msg)
}

func newErr(p Position, format string, args ...any) *LexError {
	return &LexError{Pos: p, Msg: fmt.Sprintf(format, args...)}
}
