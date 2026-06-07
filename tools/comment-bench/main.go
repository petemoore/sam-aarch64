// Command comment-bench benchmarks Z80-feasible compression schemes against
// the unstripped spectrum4 release comment corpus extracted from a compact
// overlay .tbn file (tools/sam-aarch64-format editor region, §2.5).
//
// Usage:
//
//	comment-bench <release-unstripped.tbn>
//
// Schemes measured: static word dictionary (N=128/256/512/1024), digram/BPE
// (256-entry table), canonical Huffman over bytes, dictionary+Huffman hybrid,
// greedy LZ77 with ZX0-style encoding costs (labelled "greedy stand-in"),
// and flate level-9 as a not-Z80-feasible ceiling.
//
// Granularities: whole corpus, PC-order blocks of 1/2/4/8 KB, and
// per-comment (dictionary / Huffman families only). All compressed sizes
// include their decompression table/dictionary overhead.
//
// Outputs a text report to stdout. Run from the repo root via:
//
//	make comment-bench
package main

import (
	"bytes"
	"compress/flate"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"unicode"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: comment-bench <release-unstripped.tbn>\n")
		os.Exit(1)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "comment-bench: %v\n", err)
		os.Exit(1)
	}
	f, err := format.ReadFile(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "comment-bench: %v\n", err)
		os.Exit(1)
	}

	// Extract comment bodies in anchor (PC) order, which is the sidecar sort order.
	comments := f.Comments
	if len(comments) == 0 {
		fmt.Fprintln(os.Stderr, "comment-bench: no comments found in .tbn — was it built without -strip-comments?")
		os.Exit(1)
	}

	// Build the flat corpus: bodies concatenated in PC order, newline-separated.
	// "PC-order concatenation" is the random-access unit the editor would use.
	var corpus []byte
	totalChars := 0
	for _, c := range comments {
		totalChars += len(c.Body)
		corpus = append(corpus, c.Body...)
		corpus = append(corpus, '\n')
	}

	// ── Sanity report ──────────────────────────────────────────────────────
	fmt.Println("═══════════════════════════════════════════════════════════════════════")
	fmt.Println("comment-bench — Z80-feasible compression of the release comment corpus")
	fmt.Println("═══════════════════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("Corpus:\n")
	fmt.Printf("  Comments (rows in sidecar)    : %d\n", len(comments))
	fmt.Printf("  Total comment-body bytes      : %d\n", totalChars)
	fmt.Printf("  Corpus bytes (bodies+NL sep)  : %d\n", len(corpus))
	avgLen := float64(totalChars) / float64(len(comments))
	fmt.Printf("  Average body length           : %.1f B\n", avgLen)

	// Byte distribution quick summary
	var freqMap [256]int
	for _, b := range corpus {
		freqMap[b]++
	}
	distinct := 0
	for _, v := range freqMap {
		if v > 0 {
			distinct++
		}
	}
	fmt.Printf("  Distinct byte values          : %d\n", distinct)
	fmt.Println()

	// ── Compression benchmarks ─────────────────────────────────────────────
	fmt.Println("─────────────────────────────────────────────────────────────────────")
	fmt.Println("WHOLE-CORPUS COMPRESSION (compressed size includes table/dict overhead)")
	fmt.Println("─────────────────────────────────────────────────────────────────────")
	fmt.Printf("  %-42s %9s  %s\n", "Scheme", "Bytes", "Ratio")
	fmt.Printf("  %-42s %9s  %s\n", strings.Repeat("-", 42), "---------", "-----")

	raw := len(corpus)
	printRow := func(label string, compressed int) {
		ratio := float64(compressed) / float64(raw)
		fmt.Printf("  %-42s %9d  %.3f\n", label, compressed, ratio)
	}

	// flate level-9 (not Z80-feasible ceiling)
	flateSz := flateCompress(corpus)
	printRow("flate level-9 [NOT Z80-feasible, ceiling]", flateSz)

	// Greedy LZ77 / ZX0-style
	lzSz := greedyLZ77(corpus)
	printRow("greedy LZ77/ZX0-style [greedy stand-in]*", lzSz)

	// Huffman
	huffSz := huffmanCompress(freqMap, raw)
	printRow("canonical Huffman (incl. 256-entry table)", huffSz)

	// BPE / digram
	bpeSz, _ := bpeCompress(corpus, 256)
	printRow("digram/BPE 256-entry table", bpeSz)

	// Word dictionary at 4 dictionary sizes
	for _, n := range []int{128, 256, 512, 1024} {
		dictSz := wordDictCompress(corpus, n)
		printRow(fmt.Sprintf("word dict N=%d (incl. dict bytes)", n), dictSz)
	}

	// Best word-dict → Huffman hybrid
	bestDictN := 256
	{
		bestSz := math.MaxInt
		for _, n := range []int{128, 256, 512, 1024} {
			s := wordDictCompress(corpus, n)
			if s < bestSz {
				bestSz = s
				bestDictN = n
			}
		}
	}
	hybridSz := dictHuffmanCompress(corpus, bestDictN)
	printRow(fmt.Sprintf("dict(N=%d)+Huffman hybrid (incl. tables)", bestDictN), hybridSz)

	fmt.Println()
	fmt.Println("  * greedy stand-in — real ZX0 optimal parse typically 2-5% smaller")
	fmt.Println()

	// ── Block-granularity table ────────────────────────────────────────────
	fmt.Println("─────────────────────────────────────────────────────────────────────")
	fmt.Println("BLOCK-GRANULARITY (PC-order concatenation, sliding blocks; no dict overhead)")
	fmt.Println("─────────────────────────────────────────────────────────────────────")
	fmt.Printf("  %-20s  %9s  %9s  %9s  %9s\n", "Scheme", "1 KB blk", "2 KB blk", "4 KB blk", "8 KB blk")
	fmt.Printf("  %-20s  %9s  %9s  %9s  %9s\n", strings.Repeat("-", 20), "---------", "---------", "---------", "---------")

	type blockResult struct {
		label   string
		results [4]int
	}
	blockSizes := []int{1024, 2048, 4096, 8192}
	var bResults []blockResult

	// Build block schemes
	schemes := []struct {
		label string
		fn    func([]byte) int
	}{
		{"flate l9 [ceiling]", flateCompress},
		{"greedy LZ77*", greedyLZ77},
		{"Huffman", func(b []byte) int {
			var fm [256]int
			for _, c := range b {
				fm[c]++
			}
			return huffmanCompress(fm, len(b))
		}},
		{"BPE-256", func(b []byte) int { sz, _ := bpeCompress(b, 256); return sz }},
	}

	for _, s := range schemes {
		br := blockResult{label: s.label}
		for i, bsz := range blockSizes {
			br.results[i] = blockCompress(corpus, bsz, s.fn)
		}
		bResults = append(bResults, br)
	}

	for _, br := range bResults {
		fmt.Printf("  %-20s", br.label)
		for _, v := range br.results {
			ratio := float64(v) / float64(raw)
			fmt.Printf("  %5d(%.3f)", v, ratio)
		}
		fmt.Println()
	}
	fmt.Println()
	fmt.Println("  Block values = total compressed bytes across all blocks (no dict overhead per block).")
	fmt.Println("  * greedy stand-in — real ZX0 optimal parse typically 2-5% smaller")
	fmt.Println()

	// ── Per-comment statistics (dictionary/Huffman families) ───────────────
	fmt.Println("─────────────────────────────────────────────────────────────────────")
	fmt.Println("PER-COMMENT DISTRIBUTION (dictionary/Huffman families; body-only, no table)")
	fmt.Println("─────────────────────────────────────────────────────────────────────")

	// Build a shared global Huffman table from the corpus, apply per-comment
	globalTable := buildHuffmanTable(freqMap)
	// Build a global word dictionary N=256
	_, globalDict256 := buildWordDict(corpus, 256)

	type perCommentStats struct {
		scheme   string
		total    int
		min, max int
		median   float64
	}

	computeStats := func(scheme string, sizes []int) perCommentStats {
		sort.Ints(sizes)
		total := 0
		for _, v := range sizes {
			total += v
		}
		med := 0.0
		n := len(sizes)
		if n > 0 {
			if n%2 == 0 {
				med = float64(sizes[n/2-1]+sizes[n/2]) / 2
			} else {
				med = float64(sizes[n/2])
			}
		}
		mn, mx := 0, 0
		if n > 0 {
			mn, mx = sizes[0], sizes[n-1]
		}
		return perCommentStats{scheme, total, mn, mx, med}
	}

	var rawSizes, huffSizes, dictSizes []int
	for _, c := range comments {
		body := c.Body
		rawSizes = append(rawSizes, len(body))
		huffSizes = append(huffSizes, huffmanEncodeWithTable(globalTable, body))
		dictSizes = append(dictSizes, encodeWithWordDict(body, globalDict256))
	}

	stats := []perCommentStats{
		computeStats("raw body", rawSizes),
		computeStats("Huffman (global table)", huffSizes),
		computeStats("word dict N=256 (global)", dictSizes),
	}

	fmt.Printf("  %-30s  %9s  %5s  %5s  %7s\n", "Scheme", "Total B", "Min", "Max", "Median")
	fmt.Printf("  %-30s  %9s  %5s  %5s  %7s\n", strings.Repeat("-", 30), "---------", "-----", "-----", "-------")
	for _, s := range stats {
		fmt.Printf("  %-30s  %9d  %5d  %5d  %7.1f\n", s.scheme, s.total, s.min, s.max, s.median)
	}
	fmt.Println()

	// ── Decoder-cost table (static reference data) ─────────────────────────
	fmt.Println("─────────────────────────────────────────────────────────────────────")
	fmt.Println("Z80 DECODER COST TABLE (static reference — not measured)")
	fmt.Println("─────────────────────────────────────────────────────────────────────")
	fmt.Println()
	fmt.Printf("  %-30s  %10s  %20s  %s\n", "Scheme", "Decoder B", "Decode speed", "Source / notes")
	fmt.Printf("  %-30s  %10s  %20s  %s\n", strings.Repeat("-", 30), "----------", "--------------------", "--------------")
	decoderRows := []struct {
		scheme, bytes, speed, source string
	}{
		{"ZX0 standard",
			"68",
			"unverified estimate",
			"https://github.com/einar-saukas/ZX0 (README: '68 bytes only')"},
		{"ZX0 turbo",
			"126",
			"~21% faster than std",
			"https://github.com/einar-saukas/ZX0 (README)"},
		{"LZSA1 small",
			"67",
			"unverified estimate",
			"https://github.com/emmanuel-marty/lzsa asm/z80/unlzsa1_small.asm header"},
		{"LZSA2 small",
			"134",
			"unverified estimate",
			"https://github.com/emmanuel-marty/lzsa asm/z80/unlzsa2_small.asm header"},
		{"canonical Huffman",
			"~80-120 est.",
			"~40-60 T/byte est.",
			"unverified estimate; classic Z80 Huffman decoders"},
		{"word dictionary",
			"~50-80 est.",
			"~30-50 T/byte est.",
			"unverified estimate; table lookup + copy loop"},
		{"BPE 256-entry",
			"~80-120 est.",
			"~50-100 T/byte est.",
			"unverified estimate; recursive expansion; stack-intensive"},
		{"flate level-9",
			"N/A (not Z80)",
			"N/A",
			"not feasible on Z80"},
	}
	for _, r := range decoderRows {
		fmt.Printf("  %-30s  %10s  %20s  %s\n", r.scheme, r.bytes, r.speed, r.source)
	}
	fmt.Println()

	// ── Capacity table ─────────────────────────────────────────────────────
	printCapacityTable(raw, lzSz, huffSz, bpeSz, hybridSz, bestDictN)
}

// ── Capacity table ──────────────────────────────────────────────────────────

func printCapacityTable(rawBytes, lzSz, huffSz, bpeSz, hybridSz, dictN int) {
	fmt.Println("─────────────────────────────────────────────────────────────────────")
	fmt.Println("CAPACITY TABLE — pages needed on a SAM Coupé (16 KB pages)")
	fmt.Println("─────────────────────────────────────────────────────────────────────")
	fmt.Println()
	fmt.Println("  Page-accounting assumptions:")
	fmt.Println("   • Assembler-facing prefix: ~38,584 B (≈2.4 pages, measured from release-stripped.tbn)")
	fmt.Println("     editor_region_offset = 38584; 3 IN pages used (pages 7-9 of the 6-page window).")
	fmt.Println("   • Assembler pages: 4 code pages (pages 4-7: ENCTAB + assembler + sysreg + disasm).")
	fmt.Println("   • OUT buffer: 2 pages (pages 5-6, 32 KB).")
	fmt.Println("   • Scratch/stack (section D): 1 page already within assembler mapping.")
	fmt.Println("   • Editor region (comments) stored compressed in additional pages.")
	fmt.Println("   • 256 KB SAM = 16 pages total; 512 KB SAM = 32 pages total.")
	fmt.Println("   • Minimum resident: ROM page 0 (1 page, not reclaimable in normal use).")
	fmt.Println()

	// Assembler minimum footprint (from memory-layout.md):
	// Page 4: ENCTAB; Page 5-6: OUT; Pages 7+: IN (.tbn); Page 13: sysreg; Page 15: disasm
	// Assembler code itself: section C (1 page), section D (1 page = stack+scratch), BASIC (page 1).
	// "Assembler-facing prefix" = 38,584 B = ceil(38584/16384) = 3 pages for the IN buffer.
	// Minimum non-comment pages: ENCTAB(1) + OUT(2) + IN-prefix(3) + assembler-code(1) + sysreg(1) + disasm(1) = 9 pages
	// Plus ROM(1) + BASIC sys(1) = 11 pages non-comment resident minimum.
	// This leaves 256KB(16 pages) - 11 = 5 pages for comments, 512KB(32 pages) - 11 = 21 pages.

	const pageSize = 16384
	const assemblerNonCommentPages = 11 // ROM+BASIC+code+ENCTAB+OUT(2)+IN-prefix(3)+sysreg+disasm
	const totalPages256 = 16
	const totalPages512 = 32

	pagesFor256 := totalPages256 - assemblerNonCommentPages // 5 pages free
	pagesFor512 := totalPages512 - assemblerNonCommentPages // 21 pages free

	pagesNeeded := func(sz int) int {
		return (sz + pageSize - 1) / pageSize
	}

	type capacityRow struct {
		label  string
		sz     int
		note   string
	}

	rows := []capacityRow{
		{"raw (uncompressed)", rawBytes, ""},
		{"greedy LZ77/ZX0-style*", lzSz, ""},
		{"canonical Huffman", huffSz, ""},
		{"BPE 256-entry", bpeSz, ""},
		{fmt.Sprintf("dict(N=%d)+Huffman", dictN), hybridSz, ""},
	}

	fmt.Printf("  %-28s  %9s  %5s  %9s  %9s  %s\n",
		"Scheme", "Comp.sz", "Pages", "Free/256KB", "Free/512KB", "Fits?")
	fmt.Printf("  %-28s  %9s  %5s  %9s  %9s  %s\n",
		strings.Repeat("-", 28), "---------", "-----", "----------", "----------", "-----")
	for _, r := range rows {
		pages := pagesNeeded(r.sz)
		fits256 := pagesFor256 - pages
		fits512 := pagesFor512 - pages
		verdict := "NO"
		if fits256 >= 0 {
			verdict = "YES(256+512KB)"
		} else if fits512 >= 0 {
			verdict = "512KB only"
		}
		fmt.Printf("  %-28s  %9d  %5d  %9d  %9d  %s\n",
			r.label, r.sz, pages, fits256, fits512, verdict)
	}
	fmt.Println()
	fmt.Printf("  Free pages on 256 KB SAM after assembler (no comments): %d pages = %d KB\n",
		pagesFor256, pagesFor256*16)
	fmt.Printf("  Free pages on 512 KB SAM after assembler (no comments): %d pages = %d KB\n",
		pagesFor512, pagesFor512*16)
	fmt.Println()
	fmt.Println("  * greedy stand-in — real ZX0 optimal parse typically 2-5% smaller")
	fmt.Println()
}

// ── Compression implementations ─────────────────────────────────────────────

// flateCompress compresses data with deflate level 9.
func flateCompress(data []byte) int {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Len()
}

// blockCompress splits corpus into blocks of blockSize bytes (PC order,
// last block may be shorter) and returns the total compressed size across
// all blocks.
func blockCompress(corpus []byte, blockSize int, fn func([]byte) int) int {
	total := 0
	for i := 0; i < len(corpus); i += blockSize {
		end := i + blockSize
		if end > len(corpus) {
			end = len(corpus)
		}
		total += fn(corpus[i:end])
	}
	return total
}

// ── Huffman coding ──────────────────────────────────────────────────────────

type huffNode struct {
	freq        int
	sym         int // -1 = internal
	left, right *huffNode
}

// buildHuffmanTable builds a canonical Huffman code length table from a
// byte-frequency map. Returns code lengths (0 = unused symbol).
func buildHuffmanTable(freqMap [256]int) [256]int {
	// Build heap
	var nodes []*huffNode
	for i, f := range freqMap {
		if f > 0 {
			nodes = append(nodes, &huffNode{freq: f, sym: i})
		}
	}
	if len(nodes) == 0 {
		return [256]int{}
	}
	if len(nodes) == 1 {
		var t [256]int
		t[nodes[0].sym] = 1
		return t
	}
	for len(nodes) > 1 {
		// Sort ascending by freq
		sort.Slice(nodes, func(i, j int) bool {
			return nodes[i].freq < nodes[j].freq
		})
		a, b := nodes[0], nodes[1]
		nodes = nodes[2:]
		parent := &huffNode{freq: a.freq + b.freq, sym: -1, left: a, right: b}
		nodes = append(nodes, parent)
	}
	root := nodes[0]
	var lengths [256]int
	var walk func(n *huffNode, depth int)
	walk = func(n *huffNode, depth int) {
		if n.sym >= 0 {
			lengths[n.sym] = depth
			return
		}
		walk(n.left, depth+1)
		walk(n.right, depth+1)
	}
	walk(root, 0)
	return lengths
}

// huffmanTableBytes returns the byte overhead for a canonical Huffman table:
// 256 1-byte code-length entries (standard DEFLATE-style representation).
// In a real Z80 implementation a canonical table is typically 256 bytes for
// lengths + 256 bytes for canonical codes = ~512 bytes, but a compact
// representation can achieve ~128-256 bytes. We use 256 bytes as a lower
// bound for the lengths-only representation.
const huffmanTableOverhead = 256

// huffmanCompress returns total compressed size = table overhead + bit-packed
// body (using natural Huffman code lengths, rounding up to byte).
func huffmanCompress(freqMap [256]int, rawLen int) int {
	lengths := buildHuffmanTable(freqMap)
	// Count bits: sum over each byte in corpus of its code length
	totalBits := 0
	for sym, cnt := range freqMap {
		if cnt > 0 {
			l := lengths[sym]
			if l == 0 {
				l = 1 // degenerate single-symbol
			}
			totalBits += cnt * l
		}
	}
	return huffmanTableOverhead + (totalBits+7)/8
}

// huffmanEncodeWithTable encodes a single body using a pre-built table,
// returning encoded byte count (body only, no table overhead).
func huffmanEncodeWithTable(lengths [256]int, body []byte) int {
	bits := 0
	for _, b := range body {
		l := lengths[b]
		if l == 0 {
			l = 8 // fall back to literal byte for zero-freq symbols
		}
		bits += l
	}
	return (bits + 7) / 8
}

// ── Word dictionary coding ───────────────────────────────────────────────────

// tokenize splits text into word-tokens and separator-tokens.
// Words are contiguous ASCII letters/digits; separators are everything else.
func tokenize(text []byte) []string {
	var tokens []string
	start := 0
	inWord := false
	for i, b := range text {
		isWordChar := b < 128 && (unicode.IsLetter(rune(b)) || unicode.IsDigit(rune(b)) || b == '_')
		if isWordChar {
			if !inWord {
				if i > start {
					tokens = append(tokens, string(text[start:i]))
				}
				start = i
				inWord = true
			}
		} else {
			if inWord {
				tokens = append(tokens, string(text[start:i]))
				start = i
				inWord = false
			}
		}
	}
	if start < len(text) {
		tokens = append(tokens, string(text[start:]))
	}
	return tokens
}

type dictEntry struct {
	token string
	freq  int
}

// buildWordDict builds a top-N token dictionary by frequency.
// Returns the dictionary slice and a map token→index.
func buildWordDict(corpus []byte, n int) ([]dictEntry, map[string]int) {
	freqMap := make(map[string]int)
	tokens := tokenize(corpus)
	for _, t := range tokens {
		freqMap[t]++
	}
	entries := make([]dictEntry, 0, len(freqMap))
	for t, f := range freqMap {
		entries = append(entries, dictEntry{t, f})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].freq != entries[j].freq {
			return entries[i].freq > entries[j].freq
		}
		return entries[i].token < entries[j].token
	})
	if len(entries) > n {
		entries = entries[:n]
	}
	idx := make(map[string]int, len(entries))
	for i, e := range entries {
		idx[e.token] = i
	}
	return entries, idx
}

// dictTableBytes computes the on-SAM byte cost of a word dictionary:
// for each entry we store a 1-byte length + the string bytes.
func dictTableBytes(dict []dictEntry) int {
	sz := 2 // count u16
	for _, e := range dict {
		sz += 1 + len(e.token) // length byte + string
	}
	return sz
}

// wordDictCompress estimates compressed size for a word dictionary scheme with
// N entries, using 1 byte per in-dict token (index) and raw bytes for
// out-of-dict content, plus an escape byte. Includes dict table overhead.
func wordDictCompress(corpus []byte, n int) int {
	dict, idx := buildWordDict(corpus, n)
	tableBytes := dictTableBytes(dict)
	tokens := tokenize(corpus)
	encoded := 0
	for _, t := range tokens {
		if _, ok := idx[t]; ok {
			encoded++ // 1 byte index (≤256 entries → 1 byte)
		} else {
			// escape + raw bytes
			encoded += 1 + len(t) // escape + raw length + bytes
		}
	}
	return tableBytes + encoded
}

// encodeWithWordDict encodes a single body using a pre-built global word dictionary.
func encodeWithWordDict(body []byte, idx map[string]int) int {
	tokens := tokenize(body)
	encoded := 0
	for _, t := range tokens {
		if _, ok := idx[t]; ok {
			encoded++
		} else {
			encoded += 1 + len(t)
		}
	}
	return encoded
}

// dictHuffmanCompress applies word dictionary coding then Huffman over the
// resulting token stream, including both tables.
func dictHuffmanCompress(corpus []byte, n int) int {
	dict, idx := buildWordDict(corpus, n)
	tableBytes := dictTableBytes(dict)
	tokens := tokenize(corpus)

	// Build the token stream (symbols: 0..n-1 = dict indices, n = escape)
	// Then Huffman over symbol frequencies
	const escSym = 256
	symFreq := make(map[int]int)
	var rawData [][]byte
	for _, t := range tokens {
		if i, ok := idx[t]; ok {
			symFreq[i]++
		} else {
			symFreq[escSym]++
			rawData = append(rawData, []byte(t))
		}
	}

	// Build Huffman over symbols (0..n+1 where n+1 = escape)
	nSym := n + 1
	nodes := make([]*huffNode, 0, nSym)
	for sym, freq := range symFreq {
		if freq > 0 {
			nodes = append(nodes, &huffNode{freq: freq, sym: sym})
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].freq < nodes[j].freq })
	for len(nodes) > 1 {
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].freq < nodes[j].freq })
		a, b := nodes[0], nodes[1]
		nodes = nodes[2:]
		nodes = append(nodes, &huffNode{freq: a.freq + b.freq, sym: -1, left: a, right: b})
	}

	lengths := make(map[int]int)
	if len(nodes) > 0 {
		var walkHuff func(nd *huffNode, d int)
		walkHuff = func(nd *huffNode, d int) {
			if nd.sym >= 0 {
				lengths[nd.sym] = d
				return
			}
			if nd.left != nil {
				walkHuff(nd.left, d+1)
			}
			if nd.right != nil {
				walkHuff(nd.right, d+1)
			}
		}
		walkHuff(nodes[0], 0)
	}

	// Encoded bits
	totalBits := 0
	for sym, freq := range symFreq {
		l := lengths[sym]
		if l == 0 {
			l = 1
		}
		totalBits += freq * l
	}
	// Raw literal bytes (after escape tokens in the bitstream) are stored as-is
	rawLiteralBytes := 0
	for _, r := range rawData {
		rawLiteralBytes += len(r)
	}

	// Huffman table overhead for the hybrid (nSym entries × ~1 byte each)
	huffOverhead := nSym/4 + 32 // compact representation estimate

	return tableBytes + huffOverhead + (totalBits+7)/8 + rawLiteralBytes
}

// ── BPE / digram coding ──────────────────────────────────────────────────────

// bpeCompress performs byte-pair encoding with a fixed table of maxPairs
// replacement entries, applied greedily from most-frequent pair to least.
// Returns compressed size (including pair table) and the replacement table.
func bpeCompress(data []byte, maxPairs int) (int, [][2]byte) {
	// Work with a mutable copy
	buf := make([]byte, len(data))
	copy(buf, data)

	// Track which bytes are in use (to find free bytes for new symbols)
	inUse := make([]bool, 256)
	for _, b := range buf {
		inUse[b] = true
	}

	var pairs [][2]byte

	for len(pairs) < maxPairs {
		// Count pair frequencies
		pairFreq := make(map[[2]byte]int)
		for i := 0; i+1 < len(buf); i++ {
			p := [2]byte{buf[i], buf[i+1]}
			pairFreq[p]++
		}
		if len(pairFreq) == 0 {
			break
		}
		// Find most frequent pair
		bestPair := [2]byte{}
		bestCount := 0
		for p, c := range pairFreq {
			if c > bestCount || (c == bestCount && p[0] < bestPair[0]) {
				bestCount = c
				bestPair = p
			}
		}
		if bestCount < 2 {
			break // no useful compression left
		}
		// Find a free byte for the replacement
		freeByte := -1
		for i := 255; i >= 0; i-- {
			if !inUse[i] {
				freeByte = i
				break
			}
		}
		if freeByte < 0 {
			break // no free bytes available
		}

		newByte := byte(freeByte)
		inUse[freeByte] = true
		pairs = append(pairs, bestPair)

		// Replace all occurrences (non-overlapping)
		out := buf[:0]
		i := 0
		for i < len(buf) {
			if i+1 < len(buf) && buf[i] == bestPair[0] && buf[i+1] == bestPair[1] {
				out = append(out, newByte)
				i += 2
			} else {
				out = append(out, buf[i])
				i++
			}
		}
		buf = out
	}

	// Table: count u8 + pairs × 2 bytes each
	tableSize := 1 + len(pairs)*2
	return tableSize + len(buf), pairs
}

// ── Greedy LZ77 / ZX0-style ─────────────────────────────────────────────────

// greedyLZ77 compresses data using a greedy LZ77 scheme with ZX0-style
// Elias-gamma encoded lengths and 16-bit backward offsets.
// This is a greedy stand-in; real ZX0 uses optimal parse (2-5% better).
//
// Cost model (ZX0 bitstream format, simplified):
//   - Literal run: Elias-gamma(count) bits + count*8 literal bits
//   - Backreference: 1-bit flag + Elias-gamma(length-1+1) bits + 16 offset bits
func greedyLZ77(data []byte) int {
	n := len(data)
	if n == 0 {
		return 0
	}

	// Elias-gamma bit cost for value v (≥1): 2*floor(log2(v))+1 bits
	eliasGammaBits := func(v int) int {
		if v <= 0 {
			v = 1
		}
		bits := 1
		for v > 1 {
			v >>= 1
			bits += 2
		}
		return bits
	}

	// Hash chain match finder
	const hashBits = 16
	const hashMask = (1 << hashBits) - 1
	hashHead := make([]int, 1<<hashBits)
	for i := range hashHead {
		hashHead[i] = -1
	}
	chain := make([]int, n)
	for i := range chain {
		chain[i] = -1
	}

	hash2 := func(i int) uint32 {
		if i+1 >= n {
			return uint32(data[i])
		}
		return (uint32(data[i]) * 31) ^ uint32(data[i+1])&hashMask
	}

	totalBits := 0
	litStart := 0

	flushLiterals := func(end int) {
		count := end - litStart
		if count <= 0 {
			return
		}
		totalBits += eliasGammaBits(count) + count*8
	}

	for i := 0; i < n; i++ {
		h := hash2(i) & hashMask

		// Search for the best match
		bestLen := 0
		candidate := hashHead[h]
		checked := 0
		for candidate >= 0 && checked < 64 && i-candidate <= 65535 {
			offset := i - candidate
			if offset <= 0 {
				candidate = chain[candidate]
				checked++
				continue
			}
			ml := 0
			maxML := n - i
			if maxML > 255 {
				maxML = 255
			}
			for ml < maxML && data[candidate+ml] == data[i+ml] {
				ml++
			}
			if ml > bestLen {
				bestLen = ml
			}
			candidate = chain[candidate]
			checked++
		}

		// Insert current position into hash chain
		chain[i] = hashHead[h]
		hashHead[h] = i

		const minMatch = 3 // minimum match length to save bytes
		if bestLen >= minMatch {
			flushLiterals(i)
			litStart = i + bestLen
			// ZX0-style ref cost: flag(1) + elias_gamma(length) + 16-bit offset
			totalBits += 1 + eliasGammaBits(bestLen) + 16
			// Skip matched positions (add to hash chain)
			for k := 1; k < bestLen && i+k < n; k++ {
				hk := hash2(i+k) & hashMask
				chain[i+k] = hashHead[hk]
				hashHead[hk] = i + k
			}
			i += bestLen - 1 // -1 because the for loop does i++
		}
	}
	flushLiterals(n)

	// End marker: 2 bytes (ZX0 standard convention)
	return (totalBits+7)/8 + 2
}

