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
		startOffset   = flag.Int("skip", 0, "skip the first N jobs (resume support)")
		limit         = flag.Int("limit", 0, "stop after N jobs (0 = all)")
		failFast      = flag.Bool("fail-fast", false, "exit on first DIFFER (with skip position for resume)")
	)
	flag.Parse()

	if _, err := os.Stat(*spikeBin); err != nil {
		log.Fatalf("spike binary not found at %s: %v", *spikeBin, err)
	}
	if _, err := os.Stat(*samfileBin); err != nil {
		log.Fatalf("samfile binary not found at %s: %v", *samfileBin, err)
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
		r := compareOne(j, *spikeBin, *samfileBin)
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

func compareOne(j job, spikeBin, samfileBin string) result {
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

	// Two normalisation steps before byte-compare:
	//   1. Render control bytes (<0x20 except newlines) as samfile's
	//      `{N}` form so spike's raw bytes match samfile-faithful's
	//      readable rendering.
	//   2. Strip whitespace outside string literals (same as llist-sweep)
	//      so we ignore samfile's "leading space before keyword"
	//      formatting which SAM's EDIT/R-channel omits (EDKY sets
	//      LISTFLG=0, suppressing SPACES at rom-disasm:F36E).
	a := []byte(stripSpacesOutsideStrings(renderControls(string(spikeBytes))))
	b := []byte(stripSpacesOutsideStrings(renderControls(string(b2tOut.Bytes()))))
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

// renderControls converts low-ASCII control bytes (0x00..0x1F except
// 0x0A=LF and 0x0D=CR) into samfile-faithful's `{N}` text form so
// spike's raw byte output can be compared against samfile output that
// has already rendered them.
func renderControls(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 && c != 0x0A && c != 0x0D {
			fmt.Fprintf(&b, "{%d}", c)
		} else {
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
