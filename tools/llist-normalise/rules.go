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
