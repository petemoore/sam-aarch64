// zx0_greedy_oracle_test.go — oracle-validates the greedy ZX0 compressor
// against the real upstream Z80 decoders.
//
// # What this test does
//
// For each raw corpus block in build/zx0-blocks/ (produced by 'make zx0-blocks'):
//  1. Compress the raw block with the greedy compressor at every (H,D) point.
//  2. Decompress each compressed stream with the REAL upstream dzx0_standard
//     decoder running in the koron-go/z80 emulator.
//  3. Byte-compare the decompressed output against the original raw block.
//
// Any mismatch means the greedy compressor produced a bitstream the decoder
// cannot correctly reconstruct — that is a compressor bug, not a decoder bug.
// The upstream decoder is the ground truth.
//
// # Metrics collected
//
// For each (blockSizeKB, H, D) combination the test logs:
//   - greedy compressed size and ratio vs raw
//   - optimal ZX0 size (from .zx0 file on disk) and ratio
//   - greedy/optimal ratio gap (%)
//
// A summary table is printed at the end covering all tested (H,D) points,
// enabling the ratio vs scratch-RAM vs chain-step trade-off analysis in
// docs/notes/comment-compression-research.md.
//
// # Running
//
//	cd tools/z80-test-harness-go
//	go test -run TestZX0GreedyOracle -v -count=1 .
//
// Requires: build/zx0-blocks/ (run 'make zx0-blocks' first).
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	zx0greedy "github.com/petemoore/sam-aarch64/tools/zx0-greedy"
)

// TestZX0GreedyOracle compresses real corpus blocks with the greedy
// compressor, decompresses with the upstream Z80 decoder, and byte-compares.
func TestZX0GreedyOracle(t *testing.T) {
	root := repoRoot(t)
	tdDir := filepath.Join(root, "tools", "z80-test-harness-go", "testdata")

	standardBin := assembleZX0Decoder(t, tdDir, "dzx0_standard")

	blocksDir := filepath.Join(root, "build", "zx0-blocks")
	if _, err := os.Stat(blocksDir); os.IsNotExist(err) {
		t.Skipf("no ZX0 test blocks at %s — run 'make zx0-blocks' first", blocksDir)
	}

	// (H,D) parameter grid — covers all Z80-feasible points.
	type hdPoint struct {
		h, d int
	}
	hdPoints := []hdPoint{
		{256, 4}, {256, 8}, {256, 16}, {256, 32},
		{512, 4}, {512, 8}, {512, 16}, {512, 32},
		{1024, 4}, {1024, 8}, {1024, 16}, {1024, 32},
		{2048, 4}, {2048, 8}, {2048, 16}, {2048, 32},
	}

	type blockKey struct {
		blockSizeKB, h, d int
	}
	type blockStats struct {
		n          int // number of blocks
		sumRaw     int
		sumGreedy  int
		sumOptimal int
	}
	statMap := make(map[blockKey]*blockStats)

	totalBlocks := 0
	totalPassed := 0

	for _, bsz := range []int{1, 2, 4, 8} {
		pattern := filepath.Join(blocksDir, fmt.Sprintf("block_%04dkb_*.raw", bsz))
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			t.Logf("no blocks for %d KB: skipping", bsz)
			continue
		}
		sort.Strings(matches)

		for _, rawPath := range matches {
			rawData, err := os.ReadFile(rawPath)
			if err != nil {
				t.Errorf("read %s: %v", rawPath, err)
				continue
			}

			// Read optimal ZX0 for ratio comparison.
			zx0Path := rawPath[:len(rawPath)-4] + ".zx0"
			optData, optErr := os.ReadFile(zx0Path)

			for _, hd := range hdPoints {
				p := zx0greedy.Params{HashSize: hd.h, ChainDepth: hd.d}
				compressed := zx0greedy.Compress(rawData, p)

				const maxSteps = 10_000_000
				_, decompressed, err := runZX0Decoder(standardBin, compressed, len(rawData), maxSteps)
				totalBlocks++
				if err != nil {
					t.Errorf("[H=%d D=%d] %s: decoder error: %v",
						hd.h, hd.d, filepath.Base(rawPath), err)
					continue
				}
				if string(decompressed) != string(rawData) {
					t.Errorf("[H=%d D=%d] %s: MISMATCH got %d B want %d B",
						hd.h, hd.d, filepath.Base(rawPath),
						len(decompressed), len(rawData))
					continue
				}
				totalPassed++

				key := blockKey{bsz, hd.h, hd.d}
				s := statMap[key]
				if s == nil {
					s = &blockStats{}
					statMap[key] = s
				}
				s.n++
				s.sumRaw += len(rawData)
				s.sumGreedy += len(compressed)
				if optErr == nil {
					s.sumOptimal += len(optData)
				}
			}
		}
	}

	t.Logf("")
	t.Logf("oracle result: %d/%d blocks passed (all (H,D) × block sizes)", totalPassed, totalBlocks)
	if totalPassed != totalBlocks {
		t.Fatalf("greedy compressor produced invalid bitstreams — see errors above")
	}

	// ── Ratio summary table ──────────────────────────────────────────────────
	t.Logf("")
	t.Logf("── Greedy vs optimal ZX0 ratio table (all corpus blocks) ──────────────────")
	t.Logf("%-6s  %-6s  %-6s  %-8s  %-8s  %-8s  %-8s",
		"H", "D", "blkKB", "greedy", "optimal", "gap%", "scratch")

	for _, bsz := range []int{1, 2, 4, 8} {
		for _, hd := range hdPoints {
			key := blockKey{bsz, hd.h, hd.d}
			s := statMap[key]
			if s == nil || s.n == 0 {
				continue
			}
			greedyRatio := float64(s.sumGreedy) / float64(s.sumRaw)
			scratch := zx0greedy.ScratchBytes(zx0greedy.Params{HashSize: hd.h, ChainDepth: hd.d}, bsz*1024)
			if s.sumOptimal > 0 {
				optRatio := float64(s.sumOptimal) / float64(s.sumRaw)
				gap := (greedyRatio - optRatio) / optRatio * 100
				t.Logf("%-6d  %-6d  %-6d  %-8.4f  %-8.4f  %+7.2f%%  %d B",
					hd.h, hd.d, bsz, greedyRatio, optRatio, gap, scratch)
			} else {
				t.Logf("%-6d  %-6d  %-6d  %-8.4f  (no .zx0)   ---       %d B",
					hd.h, hd.d, bsz, greedyRatio, scratch)
			}
		}
	}

	// ── Recommended operating point and time model ───────────────────────────
	t.Logf("")
	t.Logf("── T-state model: probe stats per input byte at recommended H=512, D=16 ──")

	// Count actual chain steps and hash probes for H=512 D=16 at 4 KB.
	recommended := zx0greedy.Params{HashSize: 512, ChainDepth: 16}
	var probeStats zx0greedy.ProbeStats
	pattern4 := filepath.Join(blocksDir, "block_0004kb_0000.raw")
	if raw4, err := os.ReadFile(pattern4); err == nil {
		probeStats = zx0greedy.CollectProbeStats(raw4, recommended)
		t.Logf("  positions probed: %d", probeStats.Positions)
		t.Logf("  total chain steps: %d (%.2f steps/byte)", probeStats.ChainSteps, float64(probeStats.ChainSteps)/float64(len(raw4)))
		t.Logf("  total byte comparisons: %d (%.2f cmps/byte)", probeStats.ByteComps, float64(probeStats.ByteComps)/float64(len(raw4)))

		// T-state estimate: ~20 T per hash+insert, ~40 T per chain step.
		tPerByte4 := 20.0 + float64(probeStats.ChainSteps)/float64(len(raw4))*40.0
		msPerBlock4 := float64(len(raw4)) * tPerByte4 / 6_000_000.0 * 1000.0
		t.Logf("  estimated T-states/byte (H=512 D=16, 4 KB): %.0f", tPerByte4)
		t.Logf("  estimated compress time at 6 MHz, 4 KB block: %.0f ms [model-based estimate]", msPerBlock4)
	}

	pattern8 := filepath.Join(blocksDir, "block_0008kb_0000.raw")
	if raw8, err := os.ReadFile(pattern8); err == nil {
		probeStats8 := zx0greedy.CollectProbeStats(raw8, recommended)
		tPerByte8 := 20.0 + float64(probeStats8.ChainSteps)/float64(len(raw8))*40.0
		msPerBlock8 := float64(len(raw8)) * tPerByte8 / 6_000_000.0 * 1000.0
		t.Logf("  estimated T-states/byte (H=512 D=16, 8 KB): %.0f", tPerByte8)
		t.Logf("  estimated compress time at 6 MHz, 8 KB block: %.0f ms [model-based estimate]", msPerBlock8)
	}

	// Print a per-(H,D) scratch vs ratio summary at 4 KB for the research doc.
	t.Logf("")
	t.Logf("── Scratch-RAM model at 4 KB block size ────────────────────────────────────")
	t.Logf("%-6s  %-6s  %-8s  %-8s  %-8s", "H", "D", "scratch", "greedyR", "gap%")
	for _, hd := range hdPoints {
		key := blockKey{4, hd.h, hd.d}
		s := statMap[key]
		if s == nil || s.n == 0 {
			continue
		}
		scratch := zx0greedy.ScratchBytes(zx0greedy.Params{HashSize: hd.h, ChainDepth: hd.d}, 4096)
		greedyRatio := float64(s.sumGreedy) / float64(s.sumRaw)
		gap := math.NaN()
		if s.sumOptimal > 0 {
			optRatio := float64(s.sumOptimal) / float64(s.sumRaw)
			gap = (greedyRatio - optRatio) / optRatio * 100
		}
		t.Logf("%-6d  %-6d  %-8d  %-8.4f  %+7.2f%%", hd.h, hd.d, scratch, greedyRatio, gap)
	}
}
