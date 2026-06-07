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
// zx0_compress.bin assembles at org &8400 — its page-13 product address
// (src/zx0_comm.inc ZX0_COMPRESS_ENTRY, comment-storage-design §5) —
// so the harness loads and calls it there:
//
//	0x0800         trampoline: LD IX/BC/DE/HL ; CALL 0x8400 ; HALT
//	0x0900–0x28FF  src block (max 8 KB input)
//	0x2900–0x48FF  dst buffer (max 8 KB + overhead)
//	0x8400–0x8AFF  zx0_compress.bin code + aligned hash tables
//	0x9000–...     workspace (HASH_SIZE*2 + blockLen*2 + 30 bytes;
//	               17,438 B at 8 KB blocks → ends 0xD41D)
//
// Stack: SP = 0x08FE (descends; above trampoline, below src).
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
	// zx0_compress.bin (code + aligned hash tables) must fit between its
	// org and the workspace base.
	zxcCodeBase       = 0x8400 // zx0_compress.bin loaded here (org &8400)
	zxcTrampolineBase = 0x0800 // LD IX/BC/DE/HL setup + CALL zxcCodeBase + HALT
	zxcSrcBase        = 0x0900 // HL on entry (src block)
	zxcDstBase        = 0x2900 // BC on entry (dst buffer)
	zxcWsBase         = 0x9000 // IX on entry (scratch workspace)
	zxcInitialSP      = 0x08FE // stack descends from here
)

// runZX0Compressor runs the Z80 zx0_compress.bin over src and returns
// (tstates, compressed, err).
//
// compressorBin: assembled zx0_compress.bin bytes (loaded at zxcCodeBase)
// src:           raw input block
// maxSteps:      step limit (to detect infinite loops)
func runZX0Compressor(compressorBin, src []byte, maxSteps uint64) (tstates uint64, compressed []byte, err error) {
	if zxcCodeBase+len(compressorBin) > zxcWsBase {
		return 0, nil, fmt.Errorf("compressor too large (%d B) to fit below the workspace at 0x%04X", len(compressorBin), zxcWsBase)
	}
	if len(src) > zxcDstBase-zxcSrcBase {
		return 0, nil, fmt.Errorf("src block (%d B) too large for harness layout", len(src))
	}

	mem := &flatRAM{}

	// Load compressor at its org (zxcCodeBase).
	copy(mem.mem[zxcCodeBase:], compressorBin)

	// Trampoline at zxcTrampolineBase:
	//   LD IX, wsBase      (DD 21 lo hi)   ; 4 bytes
	//   LD BC, dstBase     (01 lo hi)       ; 3 bytes
	//   LD DE, srcLen      (11 lo hi)       ; 3 bytes
	//   LD HL, srcBase     (21 lo hi)       ; 3 bytes
	//   CALL zxcCodeBase   (CD lo hi)       ; 3 bytes
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
	// CALL zxcCodeBase
	mem.mem[t+0] = 0xCD
	mem.mem[t+1] = byte(zxcCodeBase & 0xFF)
	mem.mem[t+2] = byte(zxcCodeBase >> 8)
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
		name    string
		rawLen  int
		goLen   int
		z80Len  int
		tstates uint64
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

// zx0CompressStackDepth runs the compressor over src and returns the
// maximum stack depth in bytes, measured from the compressor's entry SP
// (i.e. excluding the CALL's own return-address push).
func zx0CompressStackDepth(compressorBin, src []byte, maxSteps uint64) (int, error) {
	mem := &flatRAM{}
	copy(mem.mem[zxcCodeBase:], compressorBin)
	tramp := []byte{
		0xDD, 0x21, byte(zxcWsBase & 0xFF), byte(zxcWsBase >> 8), // LD IX,ws
		0x01, byte(zxcDstBase & 0xFF), byte(zxcDstBase >> 8), // LD BC,dst
		0x11, byte(len(src) & 0xFF), byte(len(src) >> 8), // LD DE,srcLen
		0x21, byte(zxcSrcBase & 0xFF), byte(zxcSrcBase >> 8), // LD HL,src
		0xCD, byte(zxcCodeBase & 0xFF), byte(zxcCodeBase >> 8), // CALL zxcCodeBase
		0x76, // HALT
	}
	copy(mem.mem[zxcTrampolineBase:], tramp)
	copy(mem.mem[zxcSrcBase:], src)

	cpu := &z80.CPU{Memory: mem, IO: zx0NullIO{}}
	cpu.PC = zxcTrampolineBase
	cpu.SP = zxcInitialSP

	// Entry SP inside the compressor = initial SP minus the CALL's
	// return-address push.
	entrySP := uint16(zxcInitialSP - 2)
	minSP := entrySP
	var steps uint64
	for steps < maxSteps && !cpu.HALT {
		if cpu.SP < minSP {
			minSP = cpu.SP
		}
		cpu.Step()
		steps++
	}
	if !cpu.HALT {
		return 0, fmt.Errorf("compressor exceeded step limit (%d) without HALT (PC=%04X)", maxSteps, cpu.PC)
	}
	return int(entrySP - minSP), nil
}

// TestZX0CompressStackDepth bounds the compressor's stack use against the
// paged_call safe-stack budget.
//
// The i68 boot self-test invokes the compressor under paged_call on the
// section-B safe stack descending from TRAMP_SAFE_SP (&7F00): the driver
// enters at SP=&7EFE, the compressor at SP=&7EFC, and the deepest stack
// byte must stay above PAGED_CALL_SP_SAVE's last byte (&7ED2) — a 41-byte
// budget for the compressor itself (src/zx0_payload.asm, "Stack:" note).
// This test measures the real maximum depth across every corpus block so
// a future compressor change that deepens the stack fails HERE with a
// number, not in CI as a corrupted-HMPR boot hang.
func TestZX0CompressStackDepth(t *testing.T) {
	root := repoRoot(t)

	compressorBin, err := os.ReadFile(filepath.Join(root, "build", "zx0_compress.bin"))
	if err != nil {
		t.Skipf("build/zx0_compress.bin not found — run 'make zx0-compress-payload' first: %v", err)
	}
	blocksDir := filepath.Join(root, "build", "zx0-blocks")
	matches, err := filepath.Glob(filepath.Join(blocksDir, "block_*.raw"))
	if err != nil || len(matches) == 0 {
		t.Skipf("no ZX0 test blocks at %s — run 'make zx0-blocks' first", blocksDir)
	}
	sort.Strings(matches)

	// 41 B is the hard budget; assert a safety margin below it.
	const hardBudget = 41
	const assertCeiling = 32

	maxDepth := 0
	worst := ""
	for _, rawPath := range matches {
		raw, err := os.ReadFile(rawPath)
		if err != nil {
			t.Fatalf("read %s: %v", rawPath, err)
		}
		depth, err := zx0CompressStackDepth(compressorBin, raw, 200_000_000)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(rawPath), err)
		}
		if depth > maxDepth {
			maxDepth = depth
			worst = filepath.Base(rawPath)
		}
	}
	t.Logf("max compressor stack depth across %d blocks: %d bytes (worst: %s; hard paged_call budget %d B)",
		len(matches), maxDepth, worst, hardBudget)
	if maxDepth > assertCeiling {
		t.Errorf("compressor stack depth %d B exceeds the asserted ceiling %d B (hard paged_call budget %d B) — "+
			"re-derive the src/zx0_payload.asm stack note before shipping", maxDepth, assertCeiling, hardBudget)
	}
}
