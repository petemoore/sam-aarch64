// basic-detokeniser-sweep walks the SAM corpus and compares
// basic-detokeniser-spike's output (per-line, un-wrapped, via ROM
// emulation) against `samfile basic-to-text` (faithful mode, also
// un-wrapped, pure-Go) for every FT_SAM_BASIC file in every disk.
//
// Both sides produce per-line decoded text — same axis, no wrap step
// involved. Divergences are either spike bugs or samfile bugs;
// follow up in the TSV detail column.
//
// Mirrors the shape of llist-sweep: TSV output, --skip / --limit
// resume, progress reporting every N files, deterministic ordering.
// Differences from llist-sweep:
//   - No SimCoupé / parallel-port / Docker dependency.
//   - Two pure-Go binaries invoked per file: spike + samfile.
//   - No wrap step (both UUTs un-wrapped).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/petemoore/samfile/v3"
)

// excludedDisks matches sambasic/corpus_test.go and llist-sweep's set.
var excludedDisks = map[string]bool{
	"18 Rated Poker for 512k (19xx) (Supplement Software).mgt": true,
	"AMRAD Amateur Radio Logbook (1994) (Spencer).mgt":         true,
}

type job struct {
	diskPath string
	fileName string
}

type result struct {
	status string // MATCH, DIFFER, SPIKE-ERROR, B2T-ERROR, READ-ERROR
	detail string
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("detok-sweep: ")

	home, _ := os.UserHomeDir()
	var (
		corpusDir     = flag.String("corpus", filepath.Join(home, "sam-corpus/disks"), "corpus disks directory")
		resultsPath   = flag.String("results", "/tmp/detok-sweep.tsv", "per-file results TSV")
		progressEvery = flag.Int("progress", 25, "report progress every N files")
		spikeBin      = flag.String("spike", "/tmp/detok-spike", "basic-detokeniser-spike binary")
		samfileBin    = flag.String("samfile", "/tmp/samfile", "samfile binary")
		llistCapture  = flag.String("llist-capture", filepath.Join(home, "git/sam-aarch64/tools/llist-capture.sh"), "llist-capture.sh path (only used with --vs=llist)")
		vs            = flag.String("vs", "b2t", "oracle: b2t (samfile basic-to-text faithful, fast, pure-Go), llist-capture (real SAM ROM under SimCoupé — captures pairs to --captures-dir without comparing)")
		capturesDir   = flag.String("captures-dir", "/tmp/detok-captures", "directory to write spike + llist captures into when --vs=llist-capture")
		startOffset   = flag.Int("skip", 0, "skip the first N jobs (resume support)")
		limit         = flag.Int("limit", 0, "stop after N jobs (0 = all)")
		failFast      = flag.Bool("fail-fast", false, "exit on first DIFFER (with skip position for resume)")
	)
	flag.Parse()

	if _, err := os.Stat(*spikeBin); err != nil {
		log.Fatalf("spike binary not found at %s: %v", *spikeBin, err)
	}
	if *vs != "b2t" && *vs != "llist-capture" {
		log.Fatalf("--vs must be 'b2t' or 'llist-capture'")
	}
	if *vs == "b2t" {
		if _, err := os.Stat(*samfileBin); err != nil {
			log.Fatalf("samfile binary not found at %s: %v", *samfileBin, err)
		}
	} else {
		if _, err := os.Stat(*samfileBin); err != nil {
			log.Fatalf("samfile binary not found at %s (needed for b2t capture even in --vs=llist-capture mode): %v", *samfileBin, err)
		}
		if _, err := os.Stat(*llistCapture); err != nil {
			log.Fatalf("llist-capture.sh not found at %s: %v", *llistCapture, err)
		}
		if err := os.MkdirAll(*capturesDir, 0o755); err != nil {
			log.Fatalf("create captures dir %s: %v", *capturesDir, err)
		}
		log.Printf("llist-capture mode: writing four-way captures to %s (.spike.txt + .llist.txt + .b2t.txt + .b2t-lossy.txt per job)", *capturesDir)
	}

	// Enumerate (disk, file) pairs deterministically.
	disks, err := filepath.Glob(filepath.Join(*corpusDir, "*.mgt"))
	if err != nil {
		log.Fatalf("glob corpus: %v", err)
	}
	sort.Strings(disks)
	var jobs []job
	for _, dp := range disks {
		base := filepath.Base(dp)
		if excludedDisks[base] {
			continue
		}
		di, err := samfile.Load(dp)
		if err != nil {
			log.Printf("WARN: load %s: %v — skipping disk", base, err)
			continue
		}
		var files []string
		for _, fe := range di.DiskJournal() {
			if !fe.Used() {
				continue
			}
			if fe.Type != samfile.FT_SAM_BASIC {
				continue
			}
			files = append(files, fe.Name.String())
		}
		sort.Strings(files)
		for _, name := range files {
			jobs = append(jobs, job{diskPath: dp, fileName: name})
		}
	}
	log.Printf("found %d (disk, basic-file) jobs across %d disks", len(jobs), len(disks))

	if *limit > 0 && *startOffset+*limit < len(jobs) {
		jobs = jobs[*startOffset : *startOffset+*limit]
	} else if *startOffset > 0 {
		jobs = jobs[*startOffset:]
	}

	out, err := os.OpenFile(*resultsPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatalf("open results %s: %v", *resultsPath, err)
	}
	defer out.Close()

	// Counters.
	totals := map[string]int{}
	start := time.Now()

	for i, j := range jobs {
		var r result
		if *vs == "b2t" {
			r = compareOne(j, *spikeBin, *samfileBin)
		} else {
			r = captureOneTriple(j, *spikeBin, *samfileBin, *llistCapture, *capturesDir)
		}
		totals[r.status]++
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n",
			r.status, filepath.Base(j.diskPath), j.fileName, sanitize(r.detail))

		if (i+1)%*progressEvery == 0 || i+1 == len(jobs) {
			elapsed := time.Since(start)
			rate := float64(i+1) / elapsed.Seconds()
			log.Printf("[%d/%d] %.1f/s | %s",
				i+1, len(jobs), rate, summary(totals))
		}

		if *failFast && r.status == "DIFFER" {
			log.Printf("DIFFER hit at offset %d (resume with --skip=%d)", *startOffset+i, *startOffset+i)
			os.Exit(1)
		}
	}
	log.Printf("DONE in %s — %s", time.Since(start).Truncate(time.Second), summary(totals))
}

func summary(totals map[string]int) string {
	keys := make([]string, 0, len(totals))
	for k := range totals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, totals[k]))
	}
	return strings.Join(parts, " ")
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func compareOne(j job, spikeBin, samfileBin string) (r result) {
	// samfile occasionally panics on malformed corpus disks
	// (e.g. slice-bounds-out-of-range in DiskImage.File). Treat
	// any panic as a READ-ERROR so the sweep continues.
	defer func() {
		if rec := recover(); rec != nil {
			r = result{"READ-ERROR", fmt.Sprintf("samfile panic: %v", rec)}
		}
	}()
	// Read body via samfile.
	di, err := samfile.Load(j.diskPath)
	if err != nil {
		return result{"READ-ERROR", fmt.Sprintf("load disk: %v", err)}
	}
	f, err := di.File(j.fileName)
	if err != nil {
		return result{"READ-ERROR", fmt.Sprintf("find file: %v", err)}
	}
	if f.Header.Type != samfile.FT_SAM_BASIC {
		return result{"READ-ERROR", "not FT_SAM_BASIC"}
	}
	body := f.Body

	// samfile basic-to-text (faithful, no --lossy) on the body.
	b2tCmd := exec.Command(samfileBin, "basic-to-text")
	b2tCmd.Stdin = bytes.NewReader(body)
	var b2tOut bytes.Buffer
	b2tCmd.Stdout = &b2tOut
	var b2tErr bytes.Buffer
	b2tCmd.Stderr = &b2tErr
	if err := b2tCmd.Run(); err != nil {
		return result{"B2T-ERROR", fmt.Sprintf("samfile b2t: %v: %s", err, strings.TrimSpace(b2tErr.String()))}
	}

	// spike: invoke with --mgt + --filename, capture stdout.
	tmpOut, err := os.CreateTemp("", "spike-out-*.txt")
	if err != nil {
		return result{"SPIKE-ERROR", fmt.Sprintf("tmp file: %v", err)}
	}
	tmpOut.Close()
	defer os.Remove(tmpOut.Name())

	spikeCmd := exec.Command(spikeBin, "--mgt", j.diskPath, "--filename", j.fileName, "--out", tmpOut.Name())
	var spikeErr bytes.Buffer
	spikeCmd.Stderr = &spikeErr
	if err := spikeCmd.Run(); err != nil {
		return result{"SPIKE-ERROR", fmt.Sprintf("spike: %v: %s", err, strings.TrimSpace(spikeErr.String()))}
	}
	spikeBytes, err := os.ReadFile(tmpOut.Name())
	if err != nil {
		return result{"SPIKE-ERROR", fmt.Sprintf("read spike out: %v", err)}
	}

	// Normalise both sides into a canonical form before byte-compare:
	//   - drop line 0 entries (spike can't extract line 0 because EDKY
	//     explicitly RETs for it at rom-disasm:03A1; samfile renders
	//     line 0 if present in PROG)
	//   - render control bytes (<0x20) as samfile-faithful's `{N}`
	//     form, including inside string literals (where 0x0A is a
	//     literal byte, not a line terminator)
	//   - strip whitespace outside string literals to ignore samfile's
	//     "leading space before keyword" formatting that SAM's EDKY
	//     omits (LISTFLG=0 → SPACES suppressed at rom-disasm:F36E)
	a := []byte(normalise(string(spikeBytes)))
	b := []byte(normalise(string(b2tOut.Bytes())))
	if bytes.Equal(a, b) {
		return result{"MATCH", ""}
	}

	// First-divergence context for triage.
	offset := -1
	for k := 0; k < len(a) && k < len(b); k++ {
		if a[k] != b[k] {
			offset = k
			break
		}
	}
	if offset < 0 {
		offset = min(len(a), len(b))
	}
	ctxLo := offset - 15
	if ctxLo < 0 {
		ctxLo = 0
	}
	ctxHiA := offset + 30
	if ctxHiA > len(a) {
		ctxHiA = len(a)
	}
	ctxHiB := offset + 30
	if ctxHiB > len(b) {
		ctxHiB = len(b)
	}
	return result{"DIFFER", fmt.Sprintf("spike=%d b2t=%d @%d spike=%q b2t=%q",
		len(a), len(b), offset, a[ctxLo:ctxHiA], b[ctxLo:ctxHiB])}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// captureOneTriple runs spike + llist-capture + samfile basic-to-text
// (both faithful and --lossy modes) against (disk, file) and writes
// their raw outputs to four files in a per-disk subdirectory of
// capturesDir. No comparison happens here — the captured outputs are
// kept as untouched reference so post-processing experiments (un-wrap,
// normalise, diff in any combination) can be iterated offline without
// re-running the slow SimCoupé path.
//
// Status semantics: the TSV status column records WHICH tools
// completed, in the form "spike,llist,b2t,b2t-lossy" with failed tools
// omitted. So:
//
//	CAPTURED      — all four outputs successfully captured
//	PARTIAL       — at least one but not all tools succeeded; detail
//	                lists the failures with their stderr context
//	READ-ERROR    — disk/file read failed (samfile pre-validation);
//	                nothing else attempted
//
// Even when a tool fails, the other tools still run — partial output
// is still useful for the post-hoc investigation.
func captureOneTriple(j job, spikeBin, samfileBin, llistCaptureScript, capturesDir string) (r result) {
	defer func() {
		if rec := recover(); rec != nil {
			r = result{"READ-ERROR", fmt.Sprintf("samfile panic: %v", rec)}
		}
	}()

	// Sanity check the file exists in the disk so we don't waste a
	// SimCoupé spin on a malformed entry.
	di, err := samfile.Load(j.diskPath)
	if err != nil {
		return result{"READ-ERROR", fmt.Sprintf("load disk: %v", err)}
	}
	f, err := di.File(j.fileName)
	if err != nil {
		return result{"READ-ERROR", fmt.Sprintf("find file: %v", err)}
	}

	// One directory per disk so basic-file names can collide freely
	// across disks (e.g. every disk has its own "auto").
	diskDir := filepath.Join(capturesDir, safeName(strings.TrimSuffix(filepath.Base(j.diskPath), filepath.Ext(j.diskPath))))
	if err := os.MkdirAll(diskDir, 0o755); err != nil {
		return result{"READ-ERROR", fmt.Sprintf("mkdir %s: %v", diskDir, err)}
	}
	fileStem := safeName(j.fileName)
	spikeOut := filepath.Join(diskDir, fileStem+".spike.txt")
	llistOut := filepath.Join(diskDir, fileStem+".llist.txt")
	b2tOut := filepath.Join(diskDir, fileStem+".b2t.txt")
	b2tLossyOut := filepath.Join(diskDir, fileStem+".b2t-lossy.txt")

	body := f.Body
	var fails []string

	// Spike: fast pure-Go, deterministic.
	if err := runCmd(exec.Command(spikeBin, "--mgt", j.diskPath, "--filename", j.fileName, "--out", spikeOut)); err != nil {
		fails = append(fails, "spike:"+err.Error())
	}

	// samfile basic-to-text (faithful, no --lossy).
	if err := runSamfileB2T(samfileBin, body, b2tOut, false); err != nil {
		fails = append(fails, "b2t:"+err.Error())
	}

	// samfile basic-to-text --lossy (LLIST-format equivalent).
	if err := runSamfileB2T(samfileBin, body, b2tLossyOut, true); err != nil {
		fails = append(fails, "b2t-lossy:"+err.Error())
	}

	// llist-capture last — slowest, runs SimCoupé.
	if err := runCmd(exec.Command(llistCaptureScript, j.diskPath, j.fileName, llistOut)); err != nil {
		fails = append(fails, "llist:"+err.Error())
	}

	if len(fails) == 0 {
		return result{"CAPTURED", ""}
	}
	return result{"PARTIAL", strings.Join(fails, "; ")}
}

// runCmd runs a command, captures stderr for the error message, and
// returns nil on exit 0 / formatted error otherwise.
func runCmd(cmd *exec.Cmd) error {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// runSamfileB2T pipes the BASIC body bytes through `samfile basic-to-text`
// (optionally with --lossy) and writes the result to outPath.
func runSamfileB2T(samfileBin string, body []byte, outPath string, lossy bool) error {
	args := []string{"basic-to-text"}
	if lossy {
		args = append(args, "--lossy")
	}
	cmd := exec.Command(samfileBin, args...)
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return os.WriteFile(outPath, out, 0o644)
}

// safeName returns a filesystem-safe variant of the input string,
// using a bijective encoding so the original byte string can be
// recovered exactly. The alphabet is [A-Za-z0-9.+-] (literal) plus `_`
// reserved as an escape prefix:
//
//	A-Z a-z 0-9 . + -   →  itself
//	_                   →  __        (literal underscore, doubled)
//	any other byte      →  _XX       (uppercase hex of the byte)
//
// Decoding: scan left-to-right; on `_`, peek the next byte — if `_`
// emit `_`, otherwise consume two hex digits and emit that byte.
// Used for both disk-stem and basic-filename components of the
// captures path. Earlier versions of this function collapsed every
// non-safe byte to `_`, which lost information when a BASIC filename
// contained non-printable bytes (e.g. "CARS\x95\"\r\xff \r" in
// FRED 31 → "CARS______", indistinguishable from any other CARS-prefixed
// garbage).
func safeName(s string) string {
	const hex = "0123456789ABCDEF"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		case c == '-' || c == '.' || c == '+':
			out = append(out, c)
		case c == '_':
			out = append(out, '_', '_')
		default:
			out = append(out, '_', hex[c>>4], hex[c&0x0F])
		}
	}
	return string(out)
}

// normalise folds spike output and samfile-faithful output into a
// canonical form. Tracks string-literal state so we can distinguish
// 0x0A as line-terminator (outside strings) from 0x0A as literal
// content (inside strings — renders as `{10}` to match samfile).
//
// Also drops any "    0 ..." line — the spike can't extract line 0
// (EDKY RETs for line 0 at rom-disasm:03A1).
func normalise(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	// Drop line-0 entries from each input. A "line 0" looks like
	// optional spaces + "0" + space + content + \n.
	var trimmed []string
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimLeft(ln, " ")
		if strings.HasPrefix(t, "0 ") || t == "0" {
			continue
		}
		trimmed = append(trimmed, ln)
	}
	s = strings.Join(trimmed, "\n")

	var b strings.Builder
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			// Newline resets string state — see comment in the
			// outside-string \n branch below.
			if c == '\n' {
				b.WriteByte('\n')
				inString = false
				continue
			}
			if c < 0x20 {
				fmt.Fprintf(&b, "{%d}", c)
			} else {
				b.WriteByte(c)
			}
			if c == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte('"')
					i++
					continue
				}
				inString = false
			}
			continue
		}
		// Outside strings
		switch {
		case c == '"':
			inString = true
			b.WriteByte('"')
		case c == '\n':
			// Reset string state at line boundaries — an unmatched `"`
			// (e.g. inside a REM body) would otherwise leak inString
			// across the rest of the file.
			b.WriteByte('\n')
		case c < 0x20:
			fmt.Fprintf(&b, "{%d}", c)
		case c == ' ':
			// strip (line-number prefix gets re-handled by line splitting)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// stripSpacesOutsideStrings normalises a multi-line listing by
// removing every space outside `"..."` literals on every line.
// Lifted from llist-sweep where it handles the same family of
// formatting-but-not-semantic differences.
func stripSpacesOutsideStrings(s string) string {
	// Normalise CR LF → LF first so per-line splitting is consistent.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = stripSpacesOutsideStringsLine(line)
	}
	return strings.Join(lines, "\n")
}

func stripSpacesOutsideStringsLine(line string) string {
	// Split off the leading "<spaces><digits><space>" prefix so the
	// line number stays formatted predictably.
	prefixEnd := 0
	for prefixEnd < len(line) && line[prefixEnd] == ' ' {
		prefixEnd++
	}
	for prefixEnd < len(line) && line[prefixEnd] >= '0' && line[prefixEnd] <= '9' {
		prefixEnd++
	}
	if prefixEnd < len(line) && line[prefixEnd] == ' ' {
		prefixEnd++
	}
	prefix := line[:prefixEnd]
	rest := line[prefixEnd:]

	var b strings.Builder
	b.WriteString(prefix)
	inString := false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if inString {
			b.WriteByte(c)
			if c == '"' {
				if i+1 < len(rest) && rest[i+1] == '"' {
					b.WriteByte('"')
					i++
					continue
				}
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			b.WriteByte('"')
			continue
		}
		if c == ' ' {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
