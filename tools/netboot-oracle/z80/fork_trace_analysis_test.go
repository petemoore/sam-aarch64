// fork_trace_analysis_test.go — ANALYSIS HARNESS (not a product gate). Boots the
// reconstructed STOCK SAM v3.0 ROM and Colin's FORKED rom.bin from address 0 in
// the shared emulation core and diffs the execution traces to find every point
// where the two boots fork. Grounds the "what does the fork change" story in a
// running model rather than inference. Skips when the private captures are absent.
//
// Stock image: ~/sam-archive/samboot-capture/rom_stock_reconstructed.bin
//
//	= fork rom.bin with the 97 differing bytes reverted to stock values, so it is
//	byte-exact stock except for Colin's edits (built by the analysis scripts).
package z80_test

import (
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

func loadForkAnalysisROMs(t *testing.T) (stock, fork, eeprom []byte) {
	t.Helper()
	stockPath := realCapturePath("rom_stock_reconstructed.bin")
	forkPath := realCapturePath("rom.bin")
	eepPath := realCapturePath("eeprom.bin")
	if stockPath == "" || forkPath == "" || eepPath == "" {
		t.Skip("fork-analysis captures absent (need rom_stock_reconstructed.bin + rom.bin + eeprom.bin under ~/sam-archive/samboot-capture)")
	}
	var err error
	if stock, err = os.ReadFile(stockPath); err != nil {
		t.Fatalf("read stock: %v", err)
	}
	if fork, err = os.ReadFile(forkPath); err != nil {
		t.Fatalf("read fork: %v", err)
	}
	if eeprom, err = os.ReadFile(eepPath); err != nil {
		t.Fatalf("read eeprom: %v", err)
	}
	return
}

func bootTrace(t *testing.T, rom, eeprom []byte, cap uint64, limit int) []uint16 {
	t.Helper()
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom))
	mac.AttachIO(enc)

	trace := make([]uint16, 0, limit)
	mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: cap,
		Trace: func(pc uint16) {
			if len(trace) < limit {
				trace = append(trace, pc)
			}
		},
	})
	return trace
}

// TestForkBootDivergence finds the first instruction index where the stock and
// forked boots execute a different PC, and prints the surrounding context on both
// sides — the exact fork point(s) in the cold boot.
func TestForkBootDivergence(t *testing.T) {
	stock, fork, eeprom := loadForkAnalysisROMs(t)

	const cap = 3_000_000
	const limit = 1_500_000
	st := bootTrace(t, stock, eeprom, cap, limit)
	fk := bootTrace(t, fork, eeprom, cap, limit)
	t.Logf("stock trace length=%d, fork trace length=%d", len(st), len(fk))

	n := len(st)
	if len(fk) < n {
		n = len(fk)
	}
	div := -1
	for i := 0; i < n; i++ {
		if st[i] != fk[i] {
			div = i
			break
		}
	}
	if div < 0 {
		t.Logf("no index divergence within %d shared instructions", n)
	} else {
		t.Logf("FIRST INDEX DIVERGENCE at instruction %d (where the traces first step a different PC):", div)
		lo := div - 6
		if lo < 0 {
			lo = 0
		}
		for i := lo; i < div+3 && i < n; i++ {
			mark := "  "
			if i == div {
				mark = ">>"
			}
			t.Logf("  %s [%d] stock=&%04X  fork=&%04X", mark, i, st[i], fk[i])
		}
	}

	// Set-based comparison (the index-robust view): which PCs does each boot
	// EXECUTE that the other never does. Index alignment breaks after the first
	// fork (the probe's 256-iter delay vs the rainbow's 16-iter loop shifts every
	// later index), so the genuine "added vs removed code" is the PC-set diff.
	stSet := map[uint16]bool{}
	for _, p := range st {
		stSet[p] = true
	}
	fkSet := map[uint16]bool{}
	for _, p := range fk {
		fkSet[p] = true
	}
	stOnly := rangesOf(onlyIn(stSet, fkSet))
	fkOnly := rangesOf(onlyIn(fkSet, stSet))
	t.Logf("STOCK-ONLY executed PC ranges (code stock runs that the fork never reaches) — %d ranges:", len(stOnly))
	for _, r := range stOnly {
		t.Logf("   &%04X-&%04X (%d distinct PCs)", r.lo, r.hi, r.count)
	}
	t.Logf("FORK-ONLY executed PC ranges (code the fork runs that stock never reaches) — %d ranges:", len(fkOnly))
	for _, r := range fkOnly {
		t.Logf("   &%04X-&%04X (%d distinct PCs)", r.lo, r.hi, r.count)
	}
}

type pcRange struct {
	lo, hi uint16
	count  int
}

func onlyIn(a, b map[uint16]bool) []uint16 {
	var out []uint16
	for p := range a {
		if !b[p] {
			out = append(out, p)
		}
	}
	return out
}

// rangesOf groups a set of PCs into contiguous-ish ranges (merging gaps <= 8
// bytes, so a routine with short data gaps reads as one range) for a compact view.
func rangesOf(pcs []uint16) []pcRange {
	if len(pcs) == 0 {
		return nil
	}
	sortU16(pcs)
	var out []pcRange
	cur := pcRange{lo: pcs[0], hi: pcs[0], count: 1}
	for _, p := range pcs[1:] {
		if p-cur.hi <= 8 {
			cur.hi = p
			cur.count++
		} else {
			out = append(out, cur)
			cur = pcRange{lo: p, hi: p, count: 1}
		}
	}
	out = append(out, cur)
	return out
}

func sortU16(s []uint16) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
