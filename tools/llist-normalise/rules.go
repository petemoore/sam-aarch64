// Package main: per-side normalisation rules for spike-vs-llist
// listing comparison. Each rule is a pure function taking a
// byte slice (or string) and returning the transformed result.
// Tests live in rules_test.go.
//
// Per-side rule map (driven by the investigation at
// /tmp/spike-llist-diff-report.md and /tmp/tab-rule-investigation.md):
//
//	Applied to llist output:
//	  A: stripInjectedControlLine — drop the boot-harness line + its
//	     6-space-indented wrap continuations.
//	  B: unwrap80ColContinuations — concat any line starting with
//	     exactly 6 spaces onto the previous line, stripping the
//	     6-space prefix.
//	  E: crlfToLf — convert \r\n to \n.
//
//	Applied to spike output:
//	  D: stripAttributeCodes — strip `{16}{N}`, `{17}{N}`, `{18}{N}`,
//	     `{19}{N}`, `{20}{N}` escape pairs.
//	  G: stripRemainingControls — strip every `{N}` escape where
//	     N ∈ [0,31] except N=6 (reserved for rule C).
//	  C: expandTab6 — replace `{6}` escapes with N spaces where
//	     N = 16 - (col % 16) for col <= 31, else N = 16. Column
//	     resets at LF; tracking is per-line.
//
// Function implementations land in subsequent task commits.
package main

import (
	"bytes"
	"regexp"
	"strconv"
)

// crlfToLf replaces every "\r\n" with "\n", leaving lone CRs and lone
// LFs untouched. Applied to llist output as Rule E; matches the SAM
// ROM printer's CR+LF line termination so subsequent rules can assume
// LF-only line separators.
func crlfToLf(in []byte) []byte {
	return bytes.ReplaceAll(in, []byte("\r\n"), []byte("\n"))
}

// unwrap80ColContinuations merges any line beginning with exactly 6
// spaces onto the preceding line, dropping the 6-space prefix.
// LLIST emits line numbers right-justified in a 5-char field plus a
// separator space (6 chars total); a wrap continuation reuses those
// 6 columns as a pure space pad. Lines with shorter line numbers
// (e.g. "    1 PRINT") still have a non-space character at position
// 4 or earlier, so they're unambiguously distinguishable.
//
// If a continuation line appears with no preceding line to merge
// onto, it's preserved verbatim (defensive — shouldn't happen with
// real LLIST output).
//
// Operates on LF-separated input; trailing-LF presence is preserved.
func unwrap80ColContinuations(in []byte) []byte {
	if len(in) == 0 {
		return in
	}
	// Walk lines; for each, decide: continuation onto prev, or new line.
	const prefix = "      " // 6 spaces
	out := make([]byte, 0, len(in))
	// Track where the previous output line started so we can rewind
	// to it for a continuation merge.
	prevLineEnd := -1 // index in `out` of the LF that terminates the previous line; -1 means no previous line yet
	start := 0
	for i := 0; i <= len(in); i++ {
		if i == len(in) || in[i] == '\n' {
			line := in[start:i]
			hadLF := i < len(in)
			if prevLineEnd >= 0 && len(line) >= 6 && bytes.Equal(line[:6], []byte(prefix)) {
				// Continuation: remove the LF from the previous line, append the post-prefix content.
				out = out[:prevLineEnd]
				out = append(out, line[6:]...)
				if hadLF {
					out = append(out, '\n')
					prevLineEnd = len(out) - 1
				} else {
					prevLineEnd = -1 // no LF to rewind to next time
				}
			} else {
				out = append(out, line...)
				if hadLF {
					out = append(out, '\n')
					prevLineEnd = len(out) - 1
				} else {
					prevLineEnd = -1
				}
			}
			start = i + 1
		}
	}
	return out
}

// attrCodePair matches a SAM attribute-control escape pair:
//
//	{16}{N} | {17}{N} | {18}{N} | {19}{N} | {20}{N}
//
// where N is one or more decimal digits. The SAM ROM's printer
// driver consumes these pairs silently while updating ink/paper/
// flash/bright/inverse state; spike preserves them verbatim as
// {N}-escaped control bytes. Stripping them here on the spike side
// matches the lossy printer behaviour.
var attrCodePair = regexp.MustCompile(`\{(?:16|17|18|19|20)\}\{[0-9]+\}`)

// stripAttributeCodes removes every two-escape attribute-control
// sequence from the input. All other content (including other {N}
// escapes such as {6} TAB or {13} CR) is preserved. Applied to
// spike output as Rule D before rule C, so the column tracking in
// rule C isn't polluted by these zero-width-on-printer codes.
func stripAttributeCodes(in []byte) []byte {
	if len(in) == 0 {
		return in
	}
	return attrCodePair.ReplaceAll(in, nil)
}

// expandTab6 replaces every literal `{6}` escape with N spaces, where
// N comes from the SAM ROM's PRCOMMA at rom-disasm:21577-21617:
//
//	col > 31 (WINDRHS): N = 16 (fixed, PC25 path)
//	col <= 31:          N = ((col/16) + 1) * 16 - col  (range 1..16)
//
// col is the 0-based current position on the (notional) printer
// line. It starts at 0 at input start, resets to 0 at every '\n',
// and otherwise advances by 1 per emitted byte (printable or
// otherwise).
//
// Wrap modelling: LLIST's printer wraps at column 80, emitting CRLF
// + 6 indent spaces (rom-disasm:21758 LPRENT + rom-disasm:26219
// INDOPEN). Rule B strips that indent from the normalised LLIST
// stream, so in the normalised representation a wrap looks like
// "col goes from 80 back to 6+1=7" — no LF inserted, the 6 indent
// bytes never appear. Mirror that here: when col reaches 80, reset
// col to 6 just before the next emit.
//
// The wrap check applies BEFORE every emit, including each space
// inside a {6} expansion's inner loop. The TAB's N is computed once
// at the TAB's starting col (PRCOMMA reads col into E once at entry
// and returns N — subsequent wraps inside the space loop do NOT
// re-compute N). This matches the ROM behaviour exactly.
//
// Applied to spike output as Rule C, after rules D and G. Other {N}
// escapes (N != 6) pass through verbatim.
func expandTab6(in []byte) []byte {
	if len(in) == 0 {
		return in
	}
	out := make([]byte, 0, len(in))
	col := 0
	emit := func(b byte) {
		// LLIST wraps when col reaches 80; in the normalised stream
		// the 6-space indent is invisible (Rule B), so reset col=6.
		if col == 80 {
			col = 6
		}
		out = append(out, b)
		col++
	}
	i := 0
	for i < len(in) {
		// Detect literal `{6}` (3 bytes).
		if i+2 < len(in) && in[i] == '{' && in[i+1] == '6' && in[i+2] == '}' {
			var n int
			if col > 31 {
				n = 16
			} else {
				n = ((col/16)+1)*16 - col
			}
			for j := 0; j < n; j++ {
				emit(' ')
			}
			i += 3
			continue
		}
		b := in[i]
		if b == '\n' {
			out = append(out, b)
			col = 0
		} else {
			emit(b)
		}
		i++
	}
	return out
}

// cursorAfterLineNum matches a `>` cursor marker in the column
// immediately following a (possibly-space-padded) line number at
// the start of a line. LLIST inserts this on whichever line BASIC's
// PC pointed to when LLIST ran. Capture group 1 is the leading
// whitespace + digits we want to preserve.
var cursorAfterLineNum = regexp.MustCompile(`(?m)^([ ]*[0-9]+)>`)

// stripCursorMarker replaces every `>` immediately following a
// line-number prefix with a space, restoring the spike-format
// `<padding><digits> <body>` layout. Other `>` characters on the
// line survive (regex is anchored to the start-of-line position).
// Applied to llist output as Rule F, after rule A.
func stripCursorMarker(in []byte) []byte {
	if len(in) == 0 {
		return in
	}
	return cursorAfterLineNum.ReplaceAll(in, []byte("$1 "))
}

// stripTrailingWhitespace strips trailing spaces and tabs from each
// line (LF-terminated) and from the final line (if it has no LF).
// Applied to both sides as the final normalisation step, soaking up
// the symmetric trailing-pad mismatches left by chr(6) TAB
// expansion (rule C) and printer-side column padding.
func stripTrailingWhitespace(in []byte) []byte {
	if len(in) == 0 {
		return in
	}
	out := make([]byte, 0, len(in))
	start := 0
	for i := 0; i <= len(in); i++ {
		if i == len(in) || in[i] == '\n' {
			line := in[start:i]
			// Trim trailing spaces/tabs.
			end := len(line)
			for end > 0 && (line[end-1] == ' ' || line[end-1] == '\t') {
				end--
			}
			out = append(out, line[:end]...)
			if i < len(in) {
				out = append(out, '\n')
			}
			start = i + 1
		}
	}
	return out
}

// remainingCtrlRegexp matches a {N} escape with one or more decimal
// digits. Whether to strip depends on the value of N — see
// stripRemainingControls.
var remainingCtrlRegexp = regexp.MustCompile(`\{([0-9]+)\}`)

// stripRemainingControls removes every {N} escape where N ∈ [0,31]
// EXCEPT N=6. The exception preserves {6} for rule C (TAB expansion).
// Rationale: spike captures every byte < 0x20 as {N}; rule D handles
// the 2-byte attribute pairs {16..20}{M}; this rule handles all other
// single-byte control codes that LLIST's printer driver consumes
// silently (null, bell, backspace, form-feed, embedded CR, etc.).
//
// Numbers ≥ 32 and non-numeric brace expressions ({hello}, etc.) are
// preserved unchanged.
//
// Applied to spike output between rules D and C.
func stripRemainingControls(in []byte) []byte {
	if len(in) == 0 {
		return in
	}
	return remainingCtrlRegexp.ReplaceAllFunc(in, func(match []byte) []byte {
		// match is e.g. "{12}". Parse the digits between the braces.
		inner := match[1 : len(match)-1]
		n, err := strconv.Atoi(string(inner))
		if err != nil {
			return match // shouldn't happen given the regex, defensive
		}
		if n == 6 || n >= 32 {
			return match // preserve
		}
		return nil // strip
	})
}

// stripInjectedControlLine drops any line that contains both "23203"
// and "23204" (the address pair the llist-capture harness POKEs to
// zero — see tools/llist-capture/builder/builder.go for the harness
// BASIC line). Any other line passes through unchanged. Operates on
// LF-separated input (apply after crlfToLf). Trailing-LF presence
// in the input is preserved in the output.
func stripInjectedControlLine(in []byte) []byte {
	if len(in) == 0 {
		return in
	}
	out := make([]byte, 0, len(in))
	// Walk the input line-by-line. Keep track of whether the input
	// ended with a LF so we can faithfully preserve that.
	start := 0
	for i := 0; i <= len(in); i++ {
		if i == len(in) || in[i] == '\n' {
			line := in[start:i]
			if !(bytes.Contains(line, []byte("23203")) && bytes.Contains(line, []byte("23204"))) {
				out = append(out, line...)
				if i < len(in) {
					out = append(out, '\n')
				}
			}
			start = i + 1
		}
	}
	return out
}
