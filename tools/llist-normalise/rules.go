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
//	  C: expandTab6 — replace `{6}` escapes with N spaces where
//	     N = 16 - (col % 16) for col <= 31, else N = 16. Column
//	     resets at LF; tracking is per-line.
//	  D: stripAttributeCodes — strip `{16}{N}`, `{17}{N}`, `{18}{N}`,
//	     `{19}{N}`, `{20}{N}` escape pairs.
//
// Function implementations land in subsequent task commits.
package main

import (
	"bytes"
	"regexp"
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

// expandTab6 replaces every literal `{6}` escape with N spaces,
// where N is the number of columns to advance per the SAM ROM's
// PRCOMMA handler at rom-disasm:21577-21617:
//
//	col > 31 (WINDRHS): N = 16 (fixed)
//	col <= 31:          N = ((col/16) + 1) * 16 - col  (range 1..16)
//
// col is the 0-based current position on the printer line. It
// starts at 0, resets to 0 after every '\n', and otherwise advances
// by 1 per output byte (printable or otherwise). The full reasoning
// — including why TABVAR=0 (16-col tabstops) and WINDRHS=31 (MODE 1
// boot default) are the right constants for our llist-capture
// harness — is in /tmp/tab-rule-investigation.md.
//
// Applied to spike output as Rule C, after rule D so attribute-code
// escapes don't pollute the column counter. Only the exact 3-byte
// sequence `{`, `6`, `}` is touched; longer escapes containing 6
// (e.g. `{16}` if rule D missed them) pass through verbatim.
func expandTab6(in []byte) []byte {
	if len(in) == 0 {
		return in
	}
	out := make([]byte, 0, len(in))
	col := 0
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
				out = append(out, ' ')
			}
			col += n
			i += 3
			continue
		}
		// All other bytes pass through.
		b := in[i]
		out = append(out, b)
		if b == '\n' {
			col = 0
		} else {
			col++
		}
		i++
	}
	return out
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
