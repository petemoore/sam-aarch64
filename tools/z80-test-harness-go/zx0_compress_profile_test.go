// zx0_compress_profile_test.go — flat T-state profile of the Z80 ZX0
// compressor by code region (i67).
//
// Runs build/zx0_compress.bin over one 4 KB corpus block and attributes each
// instruction's T-states to the symbol region containing its PC (using the
// pyz80 --mapfile output build/zx0_compress.map). The result is a flat
// profile by routine/label — the aiming tool for T-state optimization work.
//
// # Running
//
//	make zx0-corpus zx0-compress-payload
//	cd tools/z80-test-harness-go
//	go test -run TestZX0CompressProfile -v -count=1 .
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/koron-go/z80"
)

// mapSymbol is one entry of a pyz80 --mapfile (ADDR=name per line).
type mapSymbol struct {
	addr uint16
	name string
}

// readPyz80Map parses a pyz80 --mapfile into address-sorted symbols.
func readPyz80Map(path string) ([]mapSymbol, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var syms []mapSymbol
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		addr, err := strconv.ParseUint(line[:eq], 16, 16)
		if err != nil {
			continue
		}
		syms = append(syms, mapSymbol{addr: uint16(addr), name: line[eq+1:]})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.Slice(syms, func(i, j int) bool { return syms[i].addr < syms[j].addr })
	return syms, nil
}

// symbolFor returns the name of the region containing addr (the closest
// symbol at or below addr).
func symbolFor(syms []mapSymbol, addr uint16) string {
	idx := sort.Search(len(syms), func(i int) bool { return syms[i].addr > addr }) - 1
	if idx < 0 {
		return fmt.Sprintf("?%04X", addr)
	}
	return syms[idx].name
}

// TestZX0CompressProfile prints a flat per-region T-state profile of the
// compressor over the first 4 KB corpus block.
func TestZX0CompressProfile(t *testing.T) {
	root := repoRoot(t)

	corpus, err := os.ReadFile(filepath.Join(root, "build", "zx0-corpus.raw"))
	if err != nil {
		t.Skipf("build/zx0-corpus.raw not found — run 'make zx0-corpus' first: %v", err)
	}
	compressorBin, err := os.ReadFile(filepath.Join(root, "build", "zx0_compress.bin"))
	if err != nil {
		t.Skipf("build/zx0_compress.bin not found — run 'make zx0-compress-payload' first: %v", err)
	}
	syms, err := readPyz80Map(filepath.Join(root, "build", "zx0_compress.map"))
	if err != nil {
		t.Skipf("build/zx0_compress.map not found — run 'make zx0-compress-payload' first: %v", err)
	}

	src := corpus[:4096]

	// Inline variant of runZX0Compressor with per-region attribution.
	mem := &flatRAM{}
	copy(mem.mem[zxcCodeBase:], compressorBin)
	tAddr := zxcTrampolineBase
	tramp := []byte{
		0xDD, 0x21, byte(zxcWsBase & 0xFF), byte(zxcWsBase >> 8), // LD IX,ws
		0x01, byte(zxcDstBase & 0xFF), byte(zxcDstBase >> 8), // LD BC,dst
		0x11, byte(len(src) & 0xFF), byte(len(src) >> 8), // LD DE,srcLen
		0x21, byte(zxcSrcBase & 0xFF), byte(zxcSrcBase >> 8), // LD HL,src
		0xCD, 0x00, 0x00, // CALL 0
		0x76, // HALT
	}
	copy(mem.mem[tAddr:], tramp)
	copy(mem.mem[zxcSrcBase:], src)

	cpu := &z80.CPU{Memory: mem, IO: zx0NullIO{}}
	cpu.PC = zxcTrampolineBase
	cpu.SP = zxcInitialSP

	regionTS := make(map[string]uint64)
	var total uint64
	const maxSteps = 100_000_000
	for steps := 0; steps < maxSteps && !cpu.HALT; steps++ {
		ts := tstatesForInstruction(cpu, mem)
		if cpu.PC < zxcTrampolineBase {
			regionTS[symbolFor(syms, cpu.PC)] += ts
		}
		total += ts
		cpu.Step()
	}
	if !cpu.HALT {
		t.Fatalf("compressor did not HALT within step limit")
	}

	type entry struct {
		name string
		ts   uint64
	}
	var entries []entry
	for name, ts := range regionTS {
		entries = append(entries, entry{name, ts})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ts > entries[j].ts })

	t.Logf("flat T-state profile, first 4 KB corpus block (total %d T, %.1f T/byte):",
		total, float64(total)/float64(len(src)))
	t.Logf("%-22s  %12s  %6s", "region", "T-states", "share")
	for _, e := range entries {
		t.Logf("%-22s  %12d  %5.1f%%", e.name, e.ts, float64(e.ts)/float64(total)*100)
	}
}
