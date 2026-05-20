package main

import (
	"bytes"
	"strings"
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

func TestUnwrap_NoWraps(t *testing.T) {
	in := []byte("10 PRINT 1\n20 PRINT 2\n")
	got := unwrap80ColContinuations(in)
	want := []byte("10 PRINT 1\n20 PRINT 2\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUnwrap_SingleContinuation(t *testing.T) {
	in := []byte("10 PRINT \"hello world from a long line\"\n      MORE\n20 PRINT 2\n")
	got := unwrap80ColContinuations(in)
	want := []byte("10 PRINT \"hello world from a long line\"MORE\n20 PRINT 2\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUnwrap_MultipleContinuations(t *testing.T) {
	// Three-chunk wrap: base + cont1 + cont2.
	in := []byte("10 base\n      cont1\n      cont2\n20 next\n")
	got := unwrap80ColContinuations(in)
	want := []byte("10 basecont1cont2\n20 next\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUnwrap_PreservesLeadingSpacesBeyondSix(t *testing.T) {
	// Continuation content that itself begins with a space:
	// strip exactly 6 leading spaces, keep the rest verbatim.
	in := []byte("10 base\n        extra-space-in-content\n20 next\n")
	got := unwrap80ColContinuations(in)
	want := []byte("10 base  extra-space-in-content\n20 next\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUnwrap_LineWithFiveLeadingSpacesIsNotContinuation(t *testing.T) {
	// 5 leading spaces + "1" + " " is a line-numbered line, NOT
	// a continuation. Pass through unchanged.
	in := []byte("    1 PRINT\n20 next\n")
	got := unwrap80ColContinuations(in)
	want := []byte("    1 PRINT\n20 next\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUnwrap_ContinuationWithNoPrior(t *testing.T) {
	// Degenerate case: continuation appears first. Preserve verbatim.
	in := []byte("      orphan\n10 PRINT\n")
	got := unwrap80ColContinuations(in)
	want := []byte("      orphan\n10 PRINT\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUnwrap_NoTrailingNewline(t *testing.T) {
	in := []byte("10 base\n      cont")
	got := unwrap80ColContinuations(in)
	want := []byte("10 basecont")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUnwrap_EmptyInput(t *testing.T) {
	got := unwrap80ColContinuations(nil)
	if len(got) != 0 {
		t.Errorf("got %q, want empty", got)
	}
}

func TestUnwrap_BareContinuationLine(t *testing.T) {
	// A line that's literally just 6 spaces (no further content):
	// merges nothing onto the previous line.
	in := []byte("10 base\n      \n20 next\n")
	got := unwrap80ColContinuations(in)
	want := []byte("10 base\n20 next\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAttr_NoCodes(t *testing.T) {
	in := []byte("10 PRINT {6}1{6}2\n")
	got := stripAttributeCodes(in)
	want := []byte("10 PRINT {6}1{6}2\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAttr_StripsSinglePair(t *testing.T) {
	in := []byte("PRINT {16}{0}HELLO")
	got := stripAttributeCodes(in)
	want := []byte("PRINT HELLO")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAttr_StripsAllFiveTypes(t *testing.T) {
	in := []byte("a{16}{1}b{17}{2}c{18}{3}d{19}{4}e{20}{5}f")
	got := stripAttributeCodes(in)
	want := []byte("abcdef")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAttr_StripsConsecutivePairs(t *testing.T) {
	in := []byte("X{16}{0}{17}{255}{20}{42}Y")
	got := stripAttributeCodes(in)
	want := []byte("XY")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAttr_PreservesOther(t *testing.T) {
	// Other {N} escapes must survive.
	in := []byte("{6}{8}{13}{15}{21}{100}{255}")
	got := stripAttributeCodes(in)
	want := []byte("{6}{8}{13}{15}{21}{100}{255}")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAttr_PreservesUnmatchedAttr(t *testing.T) {
	// {16} without a following {N}: leave it.
	in := []byte("X{16}Y")
	got := stripAttributeCodes(in)
	want := []byte("X{16}Y")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAttr_PreservesNonNumericSecond(t *testing.T) {
	// {16}{abc} isn't a valid attribute pair (M must be decimal).
	in := []byte("X{16}{abc}Y")
	got := stripAttributeCodes(in)
	want := []byte("X{16}{abc}Y")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAttr_SecondCanBeMultiDigit(t *testing.T) {
	// Attribute values can be multi-digit (e.g. {18}{128} = bright).
	in := []byte("X{18}{128}Y")
	got := stripAttributeCodes(in)
	want := []byte("XY")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAttr_EmptyInput(t *testing.T) {
	got := stripAttributeCodes(nil)
	if len(got) != 0 {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExpandTab6_NoTabs(t *testing.T) {
	in := []byte("10 PRINT 1\n20 PRINT 2\n")
	got := expandTab6(in)
	want := []byte("10 PRINT 1\n20 PRINT 2\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTab6_AtColumnZero(t *testing.T) {
	// col=0: N = ((0/16)+1)*16 - 0 = 16 spaces.
	in := []byte("{6}X")
	got := expandTab6(in)
	want := []byte(strings.Repeat(" ", 16) + "X")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTab6_BeforeColumn16(t *testing.T) {
	// "ABC" advances col to 3; {6} should produce 16-3=13 spaces.
	in := []byte("ABC{6}X")
	got := expandTab6(in)
	want := []byte("ABC" + strings.Repeat(" ", 13) + "X")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTab6_ExactlyColumn16(t *testing.T) {
	// 16 chars → col=16; {6} produces N = ((16/16)+1)*16 - 16 = 16 spaces.
	in := []byte(strings.Repeat("X", 16) + "{6}Y")
	got := expandTab6(in)
	want := []byte(strings.Repeat("X", 16) + strings.Repeat(" ", 16) + "Y")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTab6_AtColumn22_LikeAutoMork(t *testing.T) {
	// Investigation report's empirical case: col 22 → 10 spaces.
	// 22 chars of X advance col to 22; ((22/16)+1)*16 - 22 = 32-22 = 10.
	in := []byte(strings.Repeat("X", 22) + "{6}Y")
	got := expandTab6(in)
	want := []byte(strings.Repeat("X", 22) + strings.Repeat(" ", 10) + "Y")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTab6_PastColumn31_FixedSixteen(t *testing.T) {
	// col 32 (one past WINDRHS=31): N = 16 unconditionally.
	in := []byte(strings.Repeat("X", 32) + "{6}Y")
	got := expandTab6(in)
	want := []byte(strings.Repeat("X", 32) + strings.Repeat(" ", 16) + "Y")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTab6_AtColumn64_LikeEllipse(t *testing.T) {
	// Investigation report's empirical case: col 64 → 16 spaces (PC25 path).
	in := []byte(strings.Repeat("X", 64) + "{6}Y")
	got := expandTab6(in)
	want := []byte(strings.Repeat("X", 64) + strings.Repeat(" ", 16) + "Y")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTab6_TwoConsecutiveTabs(t *testing.T) {
	// "X" col=1; {6} pads to col=16; {6} pads to col=32 (16 more spaces).
	in := []byte("X{6}{6}Y")
	got := expandTab6(in)
	want := []byte("X" + strings.Repeat(" ", 15) + strings.Repeat(" ", 16) + "Y")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTab6_ResetsAtNewline(t *testing.T) {
	// {6} on a fresh line should produce 16 spaces (col reset by \n).
	in := []byte("ABC\n{6}X")
	got := expandTab6(in)
	want := []byte("ABC\n" + strings.Repeat(" ", 16) + "X")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTab6_PreservesOtherEscapes(t *testing.T) {
	// {8}, {13}, etc. are not {6} and must pass through verbatim,
	// even though they DO advance the column counter (by their
	// textual byte count — see rule doc).
	in := []byte("{8}{13}{6}X")
	// {8} is 3 chars → col=3; {13} is 4 chars → col=7; {6} at col=7 → 9 spaces.
	got := expandTab6(in)
	want := []byte("{8}{13}" + strings.Repeat(" ", 9) + "X")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTab6_EmptyInput(t *testing.T) {
	got := expandTab6(nil)
	if len(got) != 0 {
		t.Errorf("got %q, want empty", got)
	}
}

// ---- Rule F: stripCursorMarker ----

func TestCursorMarker_NoCursor(t *testing.T) {
	in := []byte("    1 REM hello\n   20 PRINT 1\n")
	got := stripCursorMarker(in)
	want := []byte("    1 REM hello\n   20 PRINT 1\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCursorMarker_StripsCursor(t *testing.T) {
	in := []byte("    1>REM hello\n   20 PRINT 1\n")
	got := stripCursorMarker(in)
	want := []byte("    1 REM hello\n   20 PRINT 1\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCursorMarker_OnlyImmediatelyAfterLineNumber(t *testing.T) {
	// A `>` not in the line-number-cursor position must survive.
	in := []byte("    1 IF x>0 THEN PRINT 1\n")
	got := stripCursorMarker(in)
	want := []byte("    1 IF x>0 THEN PRINT 1\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCursorMarker_LineNumberWithoutLeadingSpaces(t *testing.T) {
	// 65279>POKE — cursor right after a wide line number.
	in := []byte("65279>POKE\n")
	got := stripCursorMarker(in)
	want := []byte("65279 POKE\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCursorMarker_OnlyFirstOccurrencePerLine(t *testing.T) {
	// Defensive: only the leading line-number-cursor `>` gets
	// replaced; later `>` characters survive.
	in := []byte("    5>IF a>b THEN c=>d\n")
	got := stripCursorMarker(in)
	want := []byte("    5 IF a>b THEN c=>d\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCursorMarker_EmptyInput(t *testing.T) {
	got := stripCursorMarker(nil)
	if len(got) != 0 {
		t.Errorf("got %q, want empty", got)
	}
}

// ---- Rule H: stripTrailingWhitespace ----

func TestTrailingWS_NoTrailing(t *testing.T) {
	in := []byte("hello\nworld\n")
	got := stripTrailingWhitespace(in)
	want := []byte("hello\nworld\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTrailingWS_StripsSpaces(t *testing.T) {
	in := []byte("hello   \nworld  \n")
	got := stripTrailingWhitespace(in)
	want := []byte("hello\nworld\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTrailingWS_StripsTabs(t *testing.T) {
	in := []byte("hello\t\t\nworld\t \t\n")
	got := stripTrailingWhitespace(in)
	want := []byte("hello\nworld\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTrailingWS_PreservesInternalWhitespace(t *testing.T) {
	// Only TRAILING whitespace is stripped; internal stays.
	in := []byte("hello  world  \n")
	got := stripTrailingWhitespace(in)
	want := []byte("hello  world\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTrailingWS_AllWhitespaceLineBecomesEmpty(t *testing.T) {
	in := []byte("a\n   \nb\n")
	got := stripTrailingWhitespace(in)
	want := []byte("a\n\nb\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTrailingWS_LastLineNoTrailingNL(t *testing.T) {
	// Final line without LF still gets stripped.
	in := []byte("hello\nworld   ")
	got := stripTrailingWhitespace(in)
	want := []byte("hello\nworld")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTrailingWS_EmptyInput(t *testing.T) {
	got := stripTrailingWhitespace(nil)
	if len(got) != 0 {
		t.Errorf("got %q, want empty", got)
	}
}
