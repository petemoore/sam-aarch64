// llist-normalise applies a fixed set of normalisation rules to
// spike-vs-llist BASIC listing capture pairs so they can be
// byte-compared. Reads a corpus directory (default
// ~/detok-captures/), produces a parallel directory tree under the
// --out path with each *.spike.txt and *.llist.txt transformed by
// the appropriate per-side rules. Writes a README into the output
// dir documenting every rule.
//
// See rules.go for the rule implementations and rules_test.go for
// the per-rule unit tests.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const readmeContent = `# detok-llist-normalised

Normalised 4-way detokeniser captures. The contents of this
directory are produced by ` + "`tools/llist-normalise`" + ` from the raw
captures at ` + "`~/detok-captures/`" + `. The original captures are not
modified.

## Per-side normalisation rules

### llist side (applied in this order)

1. **E — crlfToLf** — replace ` + "`\\r\\n`" + ` with ` + "`\\n`" + `. The SAM ROM's
   printer driver terminates lines with CR+LF (standard printer
   protocol); spike has LF-only.

2. **B — unwrap80ColContinuations** — any line beginning with
   exactly six spaces is a wrap continuation; concatenate its
   content onto the previous output line, dropping the 6-space
   prefix. LLIST emits line numbers right-justified in a 5-char
   field plus a separator space (6 chars total); wrap continuations
   reuse those 6 columns as a pure space pad.

3. **A — stripInjectedControlLine** — drop any line containing both
   the substrings ` + "`23203`" + ` and ` + "`23204`" + ` (the XPTR sysvar address
   pair that the llist-capture harness POKEs to zero before
   invoking LLIST). Two-substring match prevents false positives on
   user programs that POKE one address alone.

4. **F — stripCursorMarker** — replace the LLIST cursor marker
   ` + "`>`" + ` (inserted immediately after the line number of the line
   BASIC's PC pointed at when LLIST ran) with a space, restoring
   the spike-format ` + "`<padding><digits> <body>`" + ` layout.

5. **H — stripTrailingWhitespace** — strip trailing spaces and
   tabs from each line. Soaks up trailing-pad mismatches left by
   the symmetric chr(6) TAB expansion (rule C) and printer-side
   column padding.

### spike side (applied in this order)

1. **D — stripAttributeCodes** — strip every two-escape
   attribute-control sequence ` + "`{N}{M}`" + ` where N ∈ {16,17,18,19,20}
   and M is a decimal integer. These are SAM ink/paper/flash/
   bright/inverse codes the printer driver consumes silently. D
   runs before C so the column tracking in C isn't polluted.

2. **C — expandTab6** — replace every literal ` + "`{6}`" + ` escape with N
   spaces, where N = 16-(col%16) for col≤31 (with col%16==0 giving
   N=16), and N=16 for col>31. Rule comes straight from the ROM's
   PRCOMMA handler at rom-disasm:21577-21617; constants WINDRHS=31
   (MODE 1 boot default) and TABVAR=0 (16-col tabstops) are locked
   in because the llist-capture harness runs in that state. See
   ` + "`/tmp/tab-rule-investigation.md`" + ` for the empirical confirmation.

3. **H — stripTrailingWhitespace** — strip trailing spaces and
   tabs from each line. Applied symmetrically to both sides as
   the final normalisation step.

## Why per-side rules go in different directions

Where llist's printer pipeline is destructive (TAB expansion,
attribute-code stripping), we apply the same destruction to spike
to drive both sides toward llist's lossy form — you can't undo
destruction, but you can mirror it. Where llist adds artefacts that
spike doesn't have (CRLF, wraps, harness line), we strip them from
llist.

## Source

Rule implementations: ` + "`tools/llist-normalise/rules.go`" + `
CLI: ` + "`tools/llist-normalise/main.go`" + `
Tests: ` + "`tools/llist-normalise/rules_test.go`" + `
`

func main() {
	home, _ := os.UserHomeDir()
	inDir := flag.String("in", filepath.Join(home, "detok-captures"), "input corpus root")
	outDir := flag.String("out", filepath.Join(home, "detok-llist-normalised"), "output corpus root")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", *outDir, err)
	}

	// Write the README first so it lands even if the sweep errors out.
	readmePath := filepath.Join(*outDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(readmeContent), 0o644); err != nil {
		log.Fatalf("write %s: %v", readmePath, err)
	}

	var nSpike, nLlist, nSkipped, nErr int

	err := filepath.WalkDir(*inDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		var transform func([]byte) []byte
		switch {
		case strings.HasSuffix(name, ".spike.txt"):
			transform = spikeSidePipeline
			nSpike++
		case strings.HasSuffix(name, ".llist.txt"):
			transform = llistSidePipeline
			nLlist++
		default:
			nSkipped++
			return nil
		}
		rel, err := filepath.Rel(*inDir, path)
		if err != nil {
			nErr++
			log.Printf("rel %s: %v", path, err)
			return nil
		}
		dst := filepath.Join(*outDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			nErr++
			log.Printf("mkdir %s: %v", filepath.Dir(dst), err)
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			nErr++
			log.Printf("read %s: %v", path, err)
			return nil
		}
		if err := os.WriteFile(dst, transform(body), 0o644); err != nil {
			nErr++
			log.Printf("write %s: %v", dst, err)
			return nil
		}
		return nil
	})
	if err != nil {
		log.Fatalf("walk %s: %v", *inDir, err)
	}

	fmt.Printf("llist-normalise: spike=%d llist=%d skipped=%d errors=%d -> %s\n",
		nSpike, nLlist, nSkipped, nErr, *outDir)
}

// spikeSidePipeline applies the spike-side rules in order:
// D (strip attribute codes), C (TAB expansion), H (trailing ws).
func spikeSidePipeline(in []byte) []byte {
	out := stripAttributeCodes(in)
	out = expandTab6(out)
	out = stripTrailingWhitespace(out)
	return out
}

// llistSidePipeline applies the llist-side rules in order:
// E (CRLF→LF), B (unwrap continuations), A (strip harness),
// F (strip cursor marker), H (trailing ws).
func llistSidePipeline(in []byte) []byte {
	out := crlfToLf(in)
	out = unwrap80ColContinuations(out)
	out = stripInjectedControlLine(out)
	out = stripCursorMarker(out)
	out = stripTrailingWhitespace(out)
	return out
}
