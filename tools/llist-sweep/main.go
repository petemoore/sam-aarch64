// llist-sweep walks every BASIC file in the SAM corpus and compares
// the SAM ROM's LLIST output (captured via SimCoupé's
// parallel-port-to-file mechanism) against `samfile basic-to-text`
// for the same body bytes. Any divergence is a basic-to-text bug
// (or a known formatting difference — see
// docs/sambasic-roundtrip-caveats.md "Known differences" section).
//
// Per-file outcome is appended to a TSV (default /tmp/llist-vs-b2t-sweep.tsv).
// Running totals print to stderr every -progress files.
//
// Set parallel1=1 in SimCoupe.cfg ONCE at start and restore on exit
// to avoid thrashing user prefs across thousands of runs.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/petemoore/samfile/v3"
)

// excludedDisks matches sambasic/corpus_test.go's excludedDisks map.
// Kept in sync manually.
var excludedDisks = map[string]bool{
	"18 Rated Poker for 512k (19xx) (Supplement Software).mgt": true,
	"AMRAD Amateur Radio Logbook (1994) (Spencer).mgt":         true,
}

type job struct {
	diskPath string
	fileName string
}

type result struct {
	status string // MATCH, DIFFER, DETOK-ERROR, LLIST-ERROR, FILE-ERROR
	detail string
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("llist-sweep: ")

	home, _ := os.UserHomeDir()
	var (
		corpusDir      = flag.String("corpus", filepath.Join(home, "sam-corpus/disks"), "corpus disks directory")
		resultsPath    = flag.String("results", "/tmp/llist-vs-b2t-sweep.tsv", "per-file results TSV")
		progressEvery  = flag.Int("progress", 25, "report progress every N files")
		repoRoot       = flag.String("repo", filepath.Join(home, "git/sam-aarch64"), "sam-aarch64 repo root")
		samfileBin     = flag.String("samfile", "/tmp/samfile", "samfile binary (must already exist)")
		simCfg         = flag.String("simcfg", filepath.Join(home, "Library/Preferences/SimCoupe/SimCoupe.cfg"), "")
		samcoupeData   = flag.String("samdata", filepath.Join(home, "Documents/SimCoupe"), "SimCoupé Documents output dir")
		startOffset    = flag.Int("skip", 0, "skip the first N jobs (resume support)")
		limit          = flag.Int("limit", 0, "stop after N jobs (0 = all)")
		deleteCaptures = flag.Bool("delete-captures", true, "delete simc####.txt after each run to avoid filling Documents/SimCoupe")
	)
	flag.Parse()

	// Sanity-check tools.
	if _, err := os.Stat(*samfileBin); err != nil {
		log.Fatalf("samfile binary not found at %s: %v", *samfileBin, err)
	}
	captureBin := filepath.Join(*repoRoot, "tools/llist-capture/llist-capture")
	if _, err := os.Stat(captureBin); err != nil {
		log.Fatalf("llist-capture binary not built; build it first: %v", err)
	}
	runSim := filepath.Join(*repoRoot, "tools/run-simcoupe.sh")
	if _, err := os.Stat(runSim); err != nil {
		log.Fatalf("run-simcoupe.sh not found at %s", runSim)
	}
	samdos2 := filepath.Join(*repoRoot, "reference/samdos/samdos2.bin")
	if _, err := os.Stat(samdos2); err != nil {
		log.Fatalf("samdos2.bin not found at %s", samdos2)
	}

	// Flip SimCoupe.cfg parallel1=1 (and printerdev= empty so it
	// auto-generates simc####.txt). Restore on exit.
	cfgBefore, err := os.ReadFile(*simCfg)
	if err != nil {
		log.Fatalf("read SimCoupe.cfg: %v", err)
	}
	cfgPatched := patchCfg(cfgBefore)
	if err := os.WriteFile(*simCfg, cfgPatched, 0o644); err != nil {
		log.Fatalf("write patched SimCoupe.cfg: %v", err)
	}
	defer func() {
		os.WriteFile(*simCfg, cfgBefore, 0o644)
	}()

	// Enumerate (disk, file) pairs.
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
			continue
		}
		for _, fe := range di.DiskJournal() {
			if fe == nil || !fe.Used() {
				continue
			}
			if fe.Type != samfile.FT_SAM_BASIC {
				continue
			}
			jobs = append(jobs, job{dp, fe.Name.String()})
		}
	}
	fmt.Fprintf(os.Stderr, "Enumerated %d (disk, file) pairs across %d disks.\n", len(jobs), len(disks))

	// Open results TSV.
	f, err := os.Create(*resultsPath)
	if err != nil {
		log.Fatalf("create %s: %v", *resultsPath, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	fmt.Fprintln(w, "status\tdisk\tfile\tdetail")

	counts := map[string]int{}
	start := time.Now()
	progressTotal := len(jobs)
	if *limit > 0 && *limit < progressTotal-*startOffset {
		progressTotal = *startOffset + *limit
	}

	for i, j := range jobs {
		if i < *startOffset {
			continue
		}
		if *limit > 0 && i >= *startOffset+*limit {
			break
		}
		r := runOne(captureBin, runSim, samdos2, *samfileBin, *samcoupeData, j, *deleteCaptures)
		counts[r.status]++
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.status, filepath.Base(j.diskPath), j.fileName, r.detail)
		w.Flush()
		if (i+1-*startOffset)%*progressEvery == 0 {
			progressLine(os.Stderr, i+1, progressTotal, start, counts)
		}
	}
	progressLine(os.Stderr, progressTotal, progressTotal, start, counts)
	fmt.Fprintf(os.Stderr, "Done. Results: %s\n", *resultsPath)
}

func patchCfg(cfg []byte) []byte {
	var out bytes.Buffer
	seenP1, seenPdev, seenNF := false, false, false
	for _, line := range strings.Split(string(cfg), "\n") {
		switch {
		case strings.HasPrefix(line, "parallel1="):
			out.WriteString("parallel1=1\n")
			seenP1 = true
		case strings.HasPrefix(line, "printerdev="):
			out.WriteString("printerdev=\n")
			seenPdev = true
		case strings.HasPrefix(line, "nextfile="):
			out.WriteString(line + "\n")
			seenNF = true
		default:
			if line == "" {
				continue
			}
			out.WriteString(line + "\n")
		}
	}
	if !seenP1 {
		out.WriteString("parallel1=1\n")
	}
	if !seenPdev {
		out.WriteString("printerdev=\n")
	}
	_ = seenNF
	return out.Bytes()
}

func progressLine(w io.Writer, i, total int, start time.Time, counts map[string]int) {
	elapsed := time.Since(start)
	rate := float64(i) / elapsed.Seconds()
	remaining := total - i
	var eta string
	if rate > 0 {
		etaSec := float64(remaining) / rate
		eta = time.Duration(etaSec * float64(time.Second)).Round(time.Second).String()
	} else {
		eta = "?"
	}
	fmt.Fprintf(w, "[%5d/%-5d | %s elapsed | %.2f/s | ETA %s] ",
		i, total, elapsed.Round(time.Second), rate, eta)
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s=%d ", k, counts[k])
	}
	fmt.Fprintln(w)
}

func runOne(captureBin, runSim, samdos2, samfileBin, samcoupeData string, j job, deleteCaptures bool) (r result) {
	defer func() {
		if p := recover(); p != nil {
			r.status = "PANIC"
			r.detail = fmt.Sprintf("%v\n%s", p, debug.Stack())
		}
	}()

	// 1. Extract the target's body bytes via samfile API.
	di, err := samfile.Load(j.diskPath)
	if err != nil {
		return result{"FILE-ERROR", fmt.Sprintf("load disk: %v", err)}
	}
	f, err := di.File(j.fileName)
	if err != nil {
		return result{"FILE-ERROR", fmt.Sprintf("read file: %v", err)}
	}
	body := f.Body

	// 2. Build test disk via llist-capture binary.
	tmpDir, err := os.MkdirTemp("", "llist-sweep-")
	if err != nil {
		return result{"INTERNAL-ERROR", fmt.Sprintf("mkdtemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	testDisk := filepath.Join(tmpDir, "test.mgt")
	cmd := exec.Command(captureBin, "-source", j.diskPath, "-file", j.fileName, "-output", testDisk, "-samdos", samdos2)
	if out, err := cmd.CombinedOutput(); err != nil {
		return result{"BUILD-ERROR", fmt.Sprintf("build disk: %v: %s", err, string(out))}
	}

	// 3. Snapshot newest pre-run simc*.txt so we can identify the new one.
	prev := newestCapture(samcoupeData)

	// 4. Run SimCoupé via run-simcoupe.sh.
	simCmd := exec.Command(runSim, testDisk)
	if out, err := simCmd.CombinedOutput(); err != nil {
		// Timeout (exit 124) commonly indicates a slow load or
		// missing -exitonhalt patch — treat as LLIST-error.
		return result{"LLIST-ERROR", fmt.Sprintf("simcoupe: %v: %s", err, string(out))}
	}

	// 5. Find the new simc*.txt.
	llistPath := newestCapture(samcoupeData)
	if llistPath == "" || llistPath == prev {
		return result{"LLIST-ERROR", "no new simc*.txt produced"}
	}
	llistData, err := os.ReadFile(llistPath)
	if err != nil {
		return result{"LLIST-ERROR", fmt.Sprintf("read llist output: %v", err)}
	}
	if deleteCaptures {
		os.Remove(llistPath)
	}
	// 6. Render via samfile basic-to-text --lossy (LLIST-equivalent).
	b2tCmd := exec.Command(samfileBin, "basic-to-text", "--lossy")
	b2tCmd.Stdin = bytes.NewReader(body)
	var b2tOut bytes.Buffer
	b2tCmd.Stdout = &b2tOut
	if err := b2tCmd.Run(); err != nil {
		return result{"DETOK-ERROR", fmt.Sprintf("basic-to-text: %v", err)}
	}

	// 7. Direct byte compare — no normalisation needed because both
	// sides should be byte-identical when --lossy is faithful to
	// LLIST. (basic-to-text --lossy emits CR LF terminators to match
	// LLIST, so we compare the raw LLIST capture, NOT the CR-stripped
	// version.)
	if bytes.Equal(b2tOut.Bytes(), llistData) {
		return result{"MATCH", ""}
	}
	// Find first differing byte offset for a brief detail.
	a := b2tOut.Bytes()
	b := llistData
	offset := -1
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			offset = i
			break
		}
	}
	if offset < 0 {
		offset = min(len(a), len(b))
	}
	// Capture ~30 bytes of context around the divergence for the TSV
	// detail column so triage is one grep away.
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
	detail := fmt.Sprintf("b2t=%d llist=%d @%d b2t=%q llist=%q",
		len(a), len(b), offset, a[ctxLo:ctxHiA], b[ctxLo:ctxHiB])
	return result{"DIFFER", detail}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// normalizeForCompare folds LLIST and basic-to-text outputs into a
// canonical form for the sweep comparator, removing the known
// formatting differences that aren't basic-to-text bugs:
//
//   - LLIST's `>` current-line cursor between the line-number
//     column and the first body byte
//   - LLIST's 80-column line wrapping (continuation lines start
//     with 6 spaces of indentation matching the line-number column)
//   - Leading-space-before-keyword: basic-to-text inserts one for
//     readability and round-trippability; LLIST emits bytes verbatim
//
// To handle the leading-space-before-keyword difference, we STRIP
// every space OUTSIDE string literals. Spaces inside `"..."`
// literals (and the doubled-quote escape `""`) are preserved
// verbatim — string content is what needs faithful rendering and
// is where the most interesting bugs hide.
//
// Trailing whitespace on each line is also stripped.
func normalizeForCompare(s string) string {
	// 1. Un-wrap continuation lines. A continuation is a line whose
	//    first 6 chars are spaces; join to the previous line stripping
	//    those 6 chars.
	lines := strings.Split(s, "\n")
	var unwrapped []string
	for _, line := range lines {
		if len(unwrapped) > 0 && strings.HasPrefix(line, "      ") {
			unwrapped[len(unwrapped)-1] += line[6:]
			continue
		}
		unwrapped = append(unwrapped, line)
	}

	// 2 + 3: per-line normalisation.
	cursorRE := regexp.MustCompile(`^(\s*\d+)>(.*)$`)
	for i, line := range unwrapped {
		// Strip `>` cursor if present.
		if m := cursorRE.FindStringSubmatch(line); m != nil {
			line = m[1] + " " + m[2]
		}
		line = stripSpacesOutsideStrings(line)
		line = strings.TrimRight(line, " \t")
		unwrapped[i] = line
	}
	return strings.Join(unwrapped, "\n")
}

// stripSpacesOutsideStrings walks a single line and removes EVERY
// space outside `"..."` string literals. Spaces inside strings
// (and the doubled-quote escape `""`) are preserved verbatim — string
// content is faithful rendering territory and the main bug surface.
//
// Leading whitespace of the line (the 5-space line-number column
// padding before the digits) is preserved up to the first non-space
// run that contains a digit, then collapsed thereafter. (Simplest
// implementation: preserve the leading `<spaces><digits><single
// space>` prefix verbatim, strip-spaces-outside-strings on the rest.)
func stripSpacesOutsideStrings(line string) string {
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
				// Doubled `"` = literal `"` inside the string (still in).
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
			continue // strip
		}
		b.WriteByte(c)
	}
	return b.String()
}

var simcFile = regexp.MustCompile(`simc\d+\.txt$`)

func newestCapture(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMtime time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !simcFile.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestMtime) {
			newestMtime = info.ModTime()
			newest = filepath.Join(dir, e.Name())
		}
	}
	return newest
}
