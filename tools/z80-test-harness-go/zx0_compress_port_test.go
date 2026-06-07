// zx0_compress_port_test.go — validates the Z80 greedy ZX0 compressor port.
//
// # What this test does
//
// Loads the assembled zx0_compress.bin into the koron-go/z80 emulator and
// runs it over each corpus block from build/zx0-blocks/:
//
//  1. Byte-identity oracle: byte-compares the Z80 compressor output against
//     the Go greedy compressor at the same (H=512, D=16) parameters.
//
//  2. Round-trip: decompresses the Z80-produced compressed stream with the
//     upstream dzx0_standard decoder and byte-compares against the original.
//
//  3. T-state measurement: counts T-states spent in the compressor using the
//     same instruction-level table as TestZX0DecodeBench, derives T/input-byte
//     and ms per block at 6 MHz.
//
// # Memory layout (flat 64 KB)
//
//	0x0000–0x05FF  zx0_compress.bin code (1163 B fits)
//	0x0600         trampoline: LD IX/BC/DE/HL ; CALL 0x0000 ; HALT
//	0x0700–0x26FF  src block (max 8 KB input)
//	0x2700–0x46FF  dst buffer (max 8 KB + overhead)
//	0x4700–...     workspace (HASH_SIZE*2 + blockLen*2 + 30 bytes)
//
// Stack: SP = 0x06FE (descends; above trampoline, below src).
//
// # Running
//
//	cd tools/z80-test-harness-go
//	go test -run TestZX0CompressPort -v -count=1 .
//
// Requires: build/zx0-blocks/ and build/zx0_compress.bin.
// Run 'make zx0-blocks zx0-compress-payload' first.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/koron-go/z80"
	zx0greedy "github.com/petemoore/sam-aarch64/tools/zx0-greedy"
)

const (
	// Memory addresses for the compressor harness.
	// zx0_compress.bin is 1163 bytes; trampoline must start after it.
	zxcCodeBase       = 0x0000 // zx0_compress.bin loaded here (org 0)
	zxcTrampolineBase = 0x0600 // LD IX/BC/DE/HL setup + CALL 0x0000 + HALT
	zxcSrcBase        = 0x0700 // HL on entry (src block)
	zxcDstBase        = 0x2700 // BC on entry (dst buffer)
	zxcWsBase         = 0x4700 // IX on entry (scratch workspace)
	zxcInitialSP      = 0x06FE // stack descends from here
)

// runZX0Compressor runs the Z80 zx0_compress.bin over src and returns
// (tstates, compressed, err).
//
// compressorBin: assembled zx0_compress.bin bytes (loaded at 0x0000)
// src:           raw input block
// maxSteps:      step limit (to detect infinite loops)
func runZX0Compressor(compressorBin, src []byte, maxSteps uint64) (tstates uint64, compressed []byte, err error) {
	if len(compressorBin) > zxcTrampolineBase {
		return 0, nil, fmt.Errorf("compressor too large (%d B) to fit below 0x%04X", len(compressorBin), zxcTrampolineBase)
	}
	if len(src) > zxcDstBase-zxcSrcBase {
		return 0, nil, fmt.Errorf("src block (%d B) too large for harness layout", len(src))
	}

	mem := &flatRAM{}

	// Load compressor at 0x0000.
	copy(mem.mem[zxcCodeBase:], compressorBin)

	// Trampoline at 0x0200:
	//   LD IX, wsBase      (DD 21 lo hi)   ; 4 bytes
	//   LD BC, dstBase     (01 lo hi)       ; 3 bytes
	//   LD DE, srcLen      (11 lo hi)       ; 3 bytes
	//   LD HL, srcBase     (21 lo hi)       ; 3 bytes
	//   CALL 0x0000        (CD 00 00)       ; 3 bytes
	//   HALT               (76)             ; 1 byte
	ws := uint16(zxcWsBase)
	dst := uint16(zxcDstBase)
	srcLen := uint16(len(src))
	src16 := uint16(zxcSrcBase)

	t := zxcTrampolineBase
	// LD IX, ws
	mem.mem[t+0] = 0xDD
	mem.mem[t+1] = 0x21
	mem.mem[t+2] = byte(ws)
	mem.mem[t+3] = byte(ws >> 8)
	t += 4
	// LD BC, dst
	mem.mem[t+0] = 0x01
	mem.mem[t+1] = byte(dst)
	mem.mem[t+2] = byte(dst >> 8)
	t += 3
	// LD DE, srcLen
	mem.mem[t+0] = 0x11
	mem.mem[t+1] = byte(srcLen)
	mem.mem[t+2] = byte(srcLen >> 8)
	t += 3
	// LD HL, srcBase
	mem.mem[t+0] = 0x21
	mem.mem[t+1] = byte(src16)
	mem.mem[t+2] = byte(src16 >> 8)
	t += 3
	// CALL 0x0000
	mem.mem[t+0] = 0xCD
	mem.mem[t+1] = 0x00
	mem.mem[t+2] = 0x00
	t += 3
	// HALT
	mem.mem[t] = 0x76

	// Load src at zxcSrcBase.
	copy(mem.mem[zxcSrcBase:], src)

	cpu := &z80.CPU{Memory: mem, IO: zx0NullIO{}}
	cpu.PC = zxcTrampolineBase
	cpu.SP = zxcInitialSP

	var ts uint64
	var steps uint64
	for steps < maxSteps {
		if cpu.HALT {
			break
		}
		ts += tstatesForInstruction(cpu, mem)
		cpu.Step()
		steps++
	}
	if !cpu.HALT {
		return 0, nil, fmt.Errorf("compressor exceeded step limit (%d) without HALT (PC=%04X)", maxSteps, cpu.PC)
	}

	// HL holds the compressed length on return (from RET at end of CALL).
	// After the CALL returns to the trampoline HALT, HL is still the return value.
	compLen := cpu.HL.U16()
	if int(zxcDstBase)+int(compLen) > 65536 {
		return 0, nil, fmt.Errorf("compressed length %d exceeds address space", compLen)
	}
	out := make([]byte, compLen)
	copy(out, mem.mem[zxcDstBase:int(zxcDstBase)+int(compLen)])
	return ts, out, nil
}

// TestZX0CompressPort validates the Z80 ZX0 compressor via three checks:
// byte-identity vs Go, round-trip via decoder, and T-state measurement.
func TestZX0CompressPort(t *testing.T) {
	root := repoRoot(t)

	// Load zx0_compress.bin from build/.
	compressorBin, err := os.ReadFile(filepath.Join(root, "build", "zx0_compress.bin"))
	if err != nil {
		t.Skipf("build/zx0_compress.bin not found — run 'make zx0-compress-payload' first: %v", err)
	}
	t.Logf("zx0_compress.bin: %d bytes", len(compressorBin))

	// Assemble the ZX0 standard decoder for round-trip check.
	tdDir := filepath.Join(root, "tools", "z80-test-harness-go", "testdata")
	standardBin := assembleZX0Decoder(t, tdDir, "dzx0_standard")

	blocksDir := filepath.Join(root, "build", "zx0-blocks")
	if _, err := os.Stat(blocksDir); os.IsNotExist(err) {
		t.Skipf("no ZX0 test blocks at %s — run 'make zx0-blocks' first", blocksDir)
	}

	goParams := zx0greedy.Params{HashSize: 512, ChainDepth: 16}

	type result struct {
		name      string
		rawLen    int
		goLen     int
		z80Len    int
		tstates   uint64
	}

	var results []result
	totalPassed := 0
	totalBlocks := 0

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

			name := filepath.Base(rawPath)
			totalBlocks++

			// (a) Go reference compression at H=512, D=16.
			goCompressed := zx0greedy.Compress(rawData, goParams)

			// (b) Z80 compression.
			const maxSteps = 100_000_000
			ts, z80Compressed, err := runZX0Compressor(compressorBin, rawData, maxSteps)
			if err != nil {
				t.Errorf("%s: Z80 compressor error: %v", name, err)
				continue
			}

			// (a) Byte-identity oracle: Z80 output must equal Go output.
			byteMatch := string(z80Compressed) == string(goCompressed)
			if !byteMatch {
				t.Errorf("%s: byte-identity FAIL — Z80 len=%d Go len=%d",
					name, len(z80Compressed), len(goCompressed))
				// Print first differing byte.
				for i := 0; i < len(z80Compressed) && i < len(goCompressed); i++ {
					if z80Compressed[i] != goCompressed[i] {
						t.Errorf("  first diff at byte %d: Z80=%02X Go=%02X", i, z80Compressed[i], goCompressed[i])
						break
					}
				}
			}

			// (c) Round-trip: Z80-compressed → dzx0_standard decoder → original.
			const rtMaxSteps = 10_000_000
			_, decompressed, err := runZX0Decoder(standardBin, z80Compressed, len(rawData), rtMaxSteps)
			roundTripOK := err == nil && string(decompressed) == string(rawData)
			if err != nil {
				t.Errorf("%s: round-trip decoder error: %v", name, err)
			} else if !roundTripOK {
				t.Errorf("%s: round-trip MISMATCH — decompressed %d B want %d B",
					name, len(decompressed), len(rawData))
			}

			if byteMatch && roundTripOK {
				totalPassed++
			}

			results = append(results, result{
				name:    name,
				rawLen:  len(rawData),
				goLen:   len(goCompressed),
				z80Len:  len(z80Compressed),
				tstates: ts,
			})
		}
	}

	t.Logf("")
	t.Logf("oracle result: %d/%d blocks byte-identical + round-trip pass", totalPassed, totalBlocks)
	if totalPassed != totalBlocks {
		t.Fatalf("Z80 compressor port has failures — see errors above")
	}

	// ── T-state summary ───────────────────────────────────────────────────────
	t.Logf("")
	t.Logf("── Z80 compressor T-state summary (H=512, D=16) ─────────────────────────")
	t.Logf("%-32s  %-8s  %-8s  %-10s  %-8s  %-8s",
		"block", "rawB", "cmpB", "T-states", "T/byte", "ms@6MHz")

	type aggKey int
	aggTS := make(map[aggKey]uint64)
	aggRaw := make(map[aggKey]int)
	aggN := make(map[aggKey]int)

	for _, r := range results {
		tpb := float64(r.tstates) / float64(r.rawLen)
		ms := float64(r.tstates) / samClockHz * 1000
		t.Logf("%-32s  %-8d  %-8d  %-10d  %-8.1f  %-8.3f",
			r.name, r.rawLen, r.z80Len, r.tstates, tpb, ms)
		k := aggKey(r.rawLen / 1024)
		aggTS[k] += r.tstates
		aggRaw[k] += r.rawLen
		aggN[k]++
	}

	t.Logf("")
	t.Logf("── Per-block-size averages ───────────────────────────────────────────────")
	t.Logf("%-8s  %-6s  %-10s  %-10s  %-10s", "size", "n", "avg T/byte", "avg ms/blk", "vs model(283ms)")
	for _, bsz := range []int{1, 2, 4, 8} {
		k := aggKey(bsz)
		n := aggN[k]
		if n == 0 {
			continue
		}
		avgTPB := float64(aggTS[k]) / float64(aggRaw[k])
		avgMs := float64(aggTS[k]) / float64(n) / samClockHz * 1000
		var modelStr string
		if bsz == 4 {
			modelStr = fmt.Sprintf("%+.0f%%", (avgMs-283)/283*100)
		} else {
			modelStr = "—"
		}
		t.Logf("%-8s  %-6d  %-10.1f  %-10.3f  %s",
			fmt.Sprintf("%d KB", bsz), n, avgTPB, avgMs, modelStr)
	}
}
