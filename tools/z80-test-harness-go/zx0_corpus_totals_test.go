// zx0_corpus_totals_test.go — whole-corpus T-state totals for the ZX0 path
// (i67).
//
// # What this test does
//
// Reads the full flat comment corpus (build/zx0-corpus.raw, produced by
// 'make zx0-corpus'), splits it into blocks at 4 KB and 8 KB blockings, and
// for EVERY block:
//
//  1. Compresses with the Go greedy compressor (H=512, D=16) — the authority.
//  2. Compresses with the Z80 port (build/zx0_compress.bin) and asserts
//     byte-identity against the Go output. This extends the 24-block
//     TestZX0CompressPort oracle to the whole corpus.
//  3. Round-trips the Z80-compressed stream through the dzx0_standard decoder
//     and asserts byte-identity against the raw block.
//  4. Decompresses the same stream with dzx0_turbo and asserts byte-identity.
//
// T-states are summed across all blocks of each blocking for: (a) Z80
// compression, (b) dzx0_standard decode, (c) dzx0_turbo decode. The decode
// input is the greedy-compressed stream — the bytes the editor pipeline will
// actually decode on the SAM — not the optimal-parse .zx0 files used by
// TestZX0DecodeBench.
//
// The totals are the X→Y ledger for the i67 optimization work: run before and
// after a change to src/zx0_compress.asm to see the whole-corpus delta.
//
// Counting method: instruction-level T-state table (koron-go/z80 v0.10.2),
// same as TestZX0DecodeBench. Figures are for uncontended RAM.
//
// # Running
//
//	make zx0-corpus zx0-compress-payload
//	cd tools/z80-test-harness-go
//	go test -run TestZX0CorpusTotals -v -count=1 .
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	zx0greedy "github.com/petemoore/sam-aarch64/tools/zx0-greedy"
)

// TestZX0CorpusTotals measures whole-corpus compress + decode T-state totals
// at 4 KB and 8 KB blockings, with full byte-identity oracle coverage.
func TestZX0CorpusTotals(t *testing.T) {
	root := repoRoot(t)

	corpus, err := os.ReadFile(filepath.Join(root, "build", "zx0-corpus.raw"))
	if err != nil {
		t.Skipf("build/zx0-corpus.raw not found — run 'make zx0-corpus' first: %v", err)
	}
	compressorBin, err := os.ReadFile(filepath.Join(root, "build", "zx0_compress.bin"))
	if err != nil {
		t.Skipf("build/zx0_compress.bin not found — run 'make zx0-compress-payload' first: %v", err)
	}
	t.Logf("corpus: %d bytes, zx0_compress.bin: %d bytes", len(corpus), len(compressorBin))

	tdDir := filepath.Join(root, "tools", "z80-test-harness-go", "testdata")
	standardBin := assembleZX0Decoder(t, tdDir, "dzx0_standard")
	turboBin := assembleZX0Decoder(t, tdDir, "dzx0_turbo")

	goParams := zx0greedy.Params{HashSize: 512, ChainDepth: 16}

	type totals struct {
		blocks     int
		rawBytes   int
		compBytes  int
		compressTS uint64
		stdTS      uint64
		turboTS    uint64
	}

	for _, blockSize := range []int{4096, 8192} {
		var tot totals

		for start := 0; start < len(corpus); start += blockSize {
			end := start + blockSize
			if end > len(corpus) {
				end = len(corpus)
			}
			raw := corpus[start:end]
			name := fmt.Sprintf("%dKB block @%d", blockSize/1024, start)

			// (1) Go authority compression.
			goCompressed := zx0greedy.Compress(raw, goParams)

			// (2) Z80 compression + byte-identity oracle.
			const maxSteps = 100_000_000
			cts, z80Compressed, err := runZX0Compressor(compressorBin, raw, maxSteps)
			if err != nil {
				t.Fatalf("%s: Z80 compressor error: %v", name, err)
			}
			if string(z80Compressed) != string(goCompressed) {
				t.Fatalf("%s: byte-identity FAIL — Z80 len=%d Go len=%d",
					name, len(z80Compressed), len(goCompressed))
			}

			// (3) dzx0_standard round-trip + decode T-states.
			const rtMaxSteps = 10_000_000
			sts, stdOut, err := runZX0Decoder(standardBin, z80Compressed, len(raw), rtMaxSteps)
			if err != nil {
				t.Fatalf("%s: dzx0_standard error: %v", name, err)
			}
			if string(stdOut) != string(raw) {
				t.Fatalf("%s: dzx0_standard round-trip MISMATCH", name)
			}

			// (4) dzx0_turbo round-trip + decode T-states.
			tts, turboOut, err := runZX0Decoder(turboBin, z80Compressed, len(raw), rtMaxSteps)
			if err != nil {
				t.Fatalf("%s: dzx0_turbo error: %v", name, err)
			}
			if string(turboOut) != string(raw) {
				t.Fatalf("%s: dzx0_turbo round-trip MISMATCH", name)
			}

			tot.blocks++
			tot.rawBytes += len(raw)
			tot.compBytes += len(z80Compressed)
			tot.compressTS += cts
			tot.stdTS += sts
			tot.turboTS += tts
		}

		secs := func(ts uint64) float64 { return float64(ts) / samClockHz }
		tpb := func(ts uint64) float64 { return float64(ts) / float64(tot.rawBytes) }

		t.Logf("")
		t.Logf("── Whole-corpus totals at %d KB blocking ──────────────────────────────", blockSize/1024)
		t.Logf("blocks: %d   raw: %d B   compressed: %d B   ratio: %.4f",
			tot.blocks, tot.rawBytes, tot.compBytes,
			float64(tot.compBytes)/float64(tot.rawBytes))
		t.Logf("%-22s  %14s  %10s  %8s", "phase", "T-states", "sec@6MHz", "T/byte")
		t.Logf("%-22s  %14d  %10.3f  %8.1f", "Z80 compress", tot.compressTS, secs(tot.compressTS), tpb(tot.compressTS))
		t.Logf("%-22s  %14d  %10.3f  %8.1f", "dzx0_standard decode", tot.stdTS, secs(tot.stdTS), tpb(tot.stdTS))
		t.Logf("%-22s  %14d  %10.3f  %8.1f", "dzx0_turbo decode", tot.turboTS, secs(tot.turboTS), tpb(tot.turboTS))
	}
}
