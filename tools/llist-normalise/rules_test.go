package main

import (
	"bytes"
	"testing"
)

func TestCrlfToLf_Empty(t *testing.T) {
	got := crlfToLf(nil)
	if !bytes.Equal(got, []byte{}) && got != nil {
		t.Errorf("crlfToLf(nil): got %q, want empty", got)
	}
}

func TestCrlfToLf_NoCRLF(t *testing.T) {
	in := []byte("hello\nworld\n")
	got := crlfToLf(in)
	want := []byte("hello\nworld\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCrlfToLf_BasicReplacement(t *testing.T) {
	in := []byte("line1\r\nline2\r\nline3\r\n")
	got := crlfToLf(in)
	want := []byte("line1\nline2\nline3\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCrlfToLf_MixedEndings(t *testing.T) {
	// CRLF, lone LF, lone CR — only CRLF gets normalised.
	in := []byte("a\r\nb\nc\rd\r\n")
	got := crlfToLf(in)
	want := []byte("a\nb\nc\rd\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCrlfToLf_OnlyCR(t *testing.T) {
	// Lone CR (no LF following) must be preserved verbatim.
	in := []byte("a\rb\rc")
	got := crlfToLf(in)
	want := []byte("a\rb\rc")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCrlfToLf_TrailingCR(t *testing.T) {
	// Lone CR at end of input is not CRLF; keep it.
	in := []byte("hello\r")
	got := crlfToLf(in)
	want := []byte("hello\r")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripInjectedControlLine_NoHarness(t *testing.T) {
	in := []byte("10 PRINT 1\n20 PRINT 2\n")
	got := stripInjectedControlLine(in)
	want := []byte("10 PRINT 1\n20 PRINT 2\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripInjectedControlLine_DropsHarness(t *testing.T) {
	in := []byte(
		"10 PRINT 1\n" +
			"20 LET x=5\n" +
			"65279 POKE 23203,0: POKE 23204,0: LLIST 1 TO 65278: CALL 16384\n",
	)
	got := stripInjectedControlLine(in)
	want := []byte("10 PRINT 1\n20 LET x=5\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripInjectedControlLine_HarnessInMiddle(t *testing.T) {
	// Defensive: even if the harness somehow appears mid-listing,
	// drop just that one line.
	in := []byte(
		"10 PRINT 1\n" +
			"15 POKE 23203,0: POKE 23204,0: LLIST 1 TO 65278: CALL 16384\n" +
			"20 PRINT 2\n",
	)
	got := stripInjectedControlLine(in)
	want := []byte("10 PRINT 1\n20 PRINT 2\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripInjectedControlLine_PreservesUserPokeOfOneAddress(t *testing.T) {
	// A real user line POKEing only 23203 (or only 23204) must
	// survive — only the line containing BOTH gets dropped.
	in := []byte(
		"10 PRINT 1\n" +
			"20 POKE 23203,0: REM clear xptr lo\n" +
			"30 POKE 23204,0: REM clear xptr hi\n" +
			"40 PRINT 4\n",
	)
	got := stripInjectedControlLine(in)
	want := []byte(
		"10 PRINT 1\n" +
			"20 POKE 23203,0: REM clear xptr lo\n" +
			"30 POKE 23204,0: REM clear xptr hi\n" +
			"40 PRINT 4\n",
	)
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripInjectedControlLine_NoTrailingNewline(t *testing.T) {
	// Input ends mid-line (no final \n). Preserve that — don't
	// synthesise a terminator.
	in := []byte("10 PRINT 1\n20 PRINT 2")
	got := stripInjectedControlLine(in)
	want := []byte("10 PRINT 1\n20 PRINT 2")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripInjectedControlLine_EmptyInput(t *testing.T) {
	got := stripInjectedControlLine(nil)
	if len(got) != 0 {
		t.Errorf("got %q, want empty", got)
	}
}
