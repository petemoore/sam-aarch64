// coverage.go — read-coverage recorder + dead-code map for item i111.
//
// # What this is
//
// A byte-level "was this read?" tracker over the test-variant assembler binary
// (build/assembler.bin), driven by the same boot the harness already runs.
// koron-go/z80 routes BOTH opcode fetches and data loads through Memory.Get, so
// hooking Get (see Hardware.Get) captures every byte the CPU touches.  After a
// run, mapping the touched bytes back through the pyz80 symbol table tells us
// which named code/data regions of the binary were NEVER read — each is a
// dead-code candidate (delete to reclaim the &C000 section-C budget, item i205)
// or a coverage gap (add a fixture).
//
// # Keying: physical (page, offset), not logical address
//
// The assembler binary is org'd at &8000 and deposited into physical page 2
// (section C = &8000..&BFFF) and page 3 (section D overflow = &C000..) by
// depositPagesFrom(2, ...).  We record coverage by physical (page, offset) so a
// read is attributed to the binary regardless of what logical window currently
// maps page 2/3.  A symbol at logical L (>= &8000) lives at:
//
//	page   = binBasePage + (L-0x8000)/pageSize        (2, then 3, ...)
//	offset = (L-0x8000) % pageSize
//
// # Paging-fidelity caveat (Phase 1)
//
// This is the i111 "Phase 1" model: the resident section-C assembler code is
// NOT paged, so its coverage is faithful.  Off-axis / ENCTAB payloads that live
// on other physical pages (4, 12-15) are paged in via LMPR/HMPR; reads of those
// pages ARE recorded too (they're just different physical pages), but they are
// not part of build/assembler.bin's address space, so the report — which only
// maps assembler.sym symbols sitting in pages 2/3 — does not attribute them.
// That is correct for the budget question (the &C000 cliff is section-C resident
// code).  A symbol that is only reachable on a paged copy of its own page would
// be a fidelity gap, but the resident code we care about is read in place.
package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// binBasePage is the physical page the assembler binary's &8000 origin lands
// in (matches depositPagesFrom(2, ...) and hmprDefault's section-C page).
const binBasePage = 2

// coverage is a touched-bitmap over physical RAM pages.  pages[p][off] is true
// once any read resolves to physical page p, offset off.  Allocated lazily per
// page so an untouched page costs nothing.
type coverage struct {
	pages map[int]*[pageSize]bool
}

func newCoverage() *coverage {
	return &coverage{pages: make(map[int]*[pageSize]bool)}
}

// mark records a read of physical (page, offset).
func (c *coverage) mark(page, off int) {
	if page < 0 || page >= numPages || off < 0 || off >= pageSize {
		return
	}
	b := c.pages[page]
	if b == nil {
		b = new([pageSize]bool)
		c.pages[page] = b
	}
	b[off] = true
}

// touchedBin reports, for byte index i of the assembler binary (0-based from
// &8000), whether it was read.  Maps i to its physical (page, offset).
func (c *coverage) touchedBin(i int) bool {
	page := binBasePage + i/pageSize
	off := i % pageSize
	b := c.pages[page]
	return b != nil && b[off]
}

// symbol is one (address, name) entry of the assembler symbol table, with
// address an absolute logical Z80 address (org &8000 for code/data symbols).
type symbol struct {
	addr uint32
	name string
}

// symRange is a [start, end) logical-address span owned by one symbol, plus its
// read-coverage tally over the assembler binary image.
type symRange struct {
	name       string
	start, end uint32 // logical addresses; [start, end)
	size       int    // end - start, bytes
	touched    int    // bytes in [start,end) that were read
	excluded   bool   // matched an allowlist exclusion (not counted reclaimable)
	exclReason string
}

// loadSymbolsSorted parses the pyz80 --exportfile pickle (via the existing
// loadSAMSymbols) and returns only the code/data symbols (addr >= &8000),
// sorted ascending by address.  Symbols below &8000 are build flags / data
// constants (e.g. STAGING_BUF in section A/B scratch), never part of the
// resident binary image, so they are excluded.
func loadSymbolsSorted(symPath string) ([]symbol, error) {
	m, err := loadSAMSymbols(symPath)
	if err != nil {
		return nil, err
	}
	var syms []symbol
	for name, addr := range m {
		if addr >= codeOrigin {
			syms = append(syms, symbol{addr: addr, name: name})
		}
	}
	sort.Slice(syms, func(i, j int) bool {
		if syms[i].addr != syms[j].addr {
			return syms[i].addr < syms[j].addr
		}
		return syms[i].name < syms[j].name
	})
	return syms, nil
}

// buildSymRanges turns the sorted symbol list into contiguous [label, next
// label) ranges over the binary image [&8000, &8000+binLen).  Adjacent symbols
// at the same address collapse (zero-length leading ranges are dropped).  The
// last symbol's range runs to the end of the binary image.
func buildSymRanges(syms []symbol, binLen int) []symRange {
	imgEnd := uint32(codeOrigin) + uint32(binLen)
	var out []symRange
	for i, s := range syms {
		start := s.addr
		if start >= imgEnd {
			break // symbol past the end of the binary image
		}
		var end uint32
		if i+1 < len(syms) {
			end = syms[i+1].addr
		} else {
			end = imgEnd
		}
		if end > imgEnd {
			end = imgEnd
		}
		if end <= start {
			continue // zero-length (duplicate address) — folded into neighbour
		}
		out = append(out, symRange{name: s.name, start: start, end: end, size: int(end - start)})
	}
	return out
}

// tallyCoverage fills each symRange's touched count from the coverage map and
// applies the exclusion allowlist.
func tallyCoverage(ranges []symRange, cov *coverage, excl *exclusionList) {
	for i := range ranges {
		r := &ranges[i]
		for a := r.start; a < r.end; a++ {
			binIdx := int(a - uint32(codeOrigin))
			if cov.touchedBin(binIdx) {
				r.touched++
			}
		}
		if reason, ok := excl.match(r.name); ok {
			r.excluded = true
			r.exclReason = reason
		}
	}
}

// exclusionList is the drift-proofing allowlist (item i111 "MARKER SYMBOLS"):
// symbol-name prefixes whose unread-ness is KNOWN-ACCEPTABLE (data tables,
// error-only halt paths) and so must not be counted as reclaimable dead code.
// A small, documented prefix convention rather than a separate marker-symbol
// build step — easy to read and to extend in coverage_exclusions.go.
type exclusionList struct {
	prefixes []exclEntry
}

type exclEntry struct {
	prefix, reason string
}

func (e *exclusionList) match(name string) (string, bool) {
	for _, p := range e.prefixes {
		if strings.HasPrefix(name, p.prefix) {
			return p.reason, true
		}
	}
	return "", false
}

// coverageReport is the full result of a coverage run.
type coverageReport struct {
	binLen       int
	totalBytes   int // bytes covered by named ranges in the image
	touchedBytes int
	untouched    []symRange // ranges with touched < size, sorted size desc
	reclaimable  int        // sum of fully-untouched, non-excluded range sizes
}

// computeReport assembles the ranked dead-code report from tallied ranges.
func computeReport(ranges []symRange, binLen int) coverageReport {
	rep := coverageReport{binLen: binLen}
	for _, r := range ranges {
		rep.totalBytes += r.size
		rep.touchedBytes += r.touched
		if r.touched < r.size {
			rep.untouched = append(rep.untouched, r)
		}
		if r.touched == 0 && !r.excluded {
			rep.reclaimable += r.size
		}
	}
	sort.Slice(rep.untouched, func(i, j int) bool {
		ui := rep.untouched[i].size - rep.untouched[i].touched
		uj := rep.untouched[j].size - rep.untouched[j].touched
		if ui != uj {
			return ui > uj // most-unread first
		}
		return rep.untouched[i].start < rep.untouched[j].start
	})
	return rep
}

// render writes a human-readable report to w.
func (rep coverageReport) render(w *strings.Builder) {
	fmt.Fprintf(w, "i111 read-coverage / dead-code report\n")
	fmt.Fprintf(w, "=====================================\n")
	fmt.Fprintf(w, "binary image: %d bytes (&8000..&%04X)\n", rep.binLen, codeOrigin+rep.binLen)
	fmt.Fprintf(w, "named-symbol coverage: %d / %d bytes read (%.1f%%)\n",
		rep.touchedBytes, rep.totalBytes, pct(rep.touchedBytes, rep.totalBytes))
	fmt.Fprintf(w, "fully-untouched, non-excluded (reclaimable estimate): %d bytes\n\n", rep.reclaimable)

	fmt.Fprintf(w, "%-6s %-7s %-7s %-6s  %s\n", "addr", "size", "unread", "state", "symbol")
	fmt.Fprintf(w, "%-6s %-7s %-7s %-6s  %s\n", "----", "----", "------", "-----", "------")
	for _, r := range rep.untouched {
		unread := r.size - r.touched
		state := "DEAD"
		if r.touched > 0 {
			state = "part"
		}
		if r.excluded {
			state = "excl"
		}
		note := ""
		if r.excluded {
			note = "  (" + r.exclReason + ")"
		}
		fmt.Fprintf(w, "&%04X  %-7d %-7d %-6s  %s%s\n", r.start, r.size, unread, state, r.name, note)
	}
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}

// writeReportFile writes the rendered report to path (best-effort artefact for
// the i205 optimization pass).
func writeReportFile(path, body string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	if _, err := bw.WriteString(body); err != nil {
		return err
	}
	return bw.Flush()
}
