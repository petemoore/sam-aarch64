// harness_test.go — unit tests for the Z80 test harness spike.
//
// Runs the target fixture (tests/m3/sources/inst_nop_ret.s) end-to-end and
// asserts that the assembled OUT bytes match the GNU-as oracle.
//
// The test expects:
//   - build/assembler-prod.bin (built with `make m3-asm-prod`)
//   - build/enctab.enc         (built with `make enctab`)
//   - build/sam-aarch64         (built with `make sam-aarch64`)
//   - tests/m3/sources/inst_nop_ret.s  (source fixture; always present)
//
// Run from the repo root:
//
//	cd tools/z80-test-harness-go && go test -v -timeout 60s
//
// or from the repo root:
//
//	go test ./tools/z80-test-harness-go/ -v -timeout 60s
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// repoRoot walks up from the test binary's location to find the repo root
// (identified by the presence of Makefile).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no Makefile found walking up from cwd)")
		}
		dir = parent
	}
}

// TestInstNopRet runs the inst_nop_ret.s fixture end-to-end and checks that
// the output bytes match the GNU-as oracle.
func TestInstNopRet(t *testing.T) {
	root := repoRoot(t)

	// Paths.
	assemblerBinPath := filepath.Join(root, "build", "assembler-prod.bin")
	enctabPath := filepath.Join(root, "build", "enctab.enc")
	samPath := filepath.Join(root, "build", "sam-aarch64")
	fixturePath := filepath.Join(root, "tests", "m3", "sources", "inst_nop_ret.s")

	// Check prerequisites exist.
	for _, path := range []string{assemblerBinPath, enctabPath, samPath, fixturePath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("prerequisite missing: %s\n  run `make m3-asm-prod enctab sam-aarch64` from repo root", path)
		}
	}

	// Assemble the fixture source to a binary + compact .tbn — the SAM
	// assembler consumes the compact v2 .tbn (INSN_RUN decoder), which
	// sam-aarch64 emits directly via --emit-tbn.
	tmp := t.TempDir()
	tbnPath := filepath.Join(tmp, "inst_nop_ret.compact.tbn")
	goImgPath := filepath.Join(tmp, "inst_nop_ret.go.img")
	cmd := exec.Command(samPath, "-o", goImgPath, "-emit-tbn", tbnPath, fixturePath)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sam-aarch64 failed: %v\n%s", err, out)
	}

	// Read all inputs.
	assemblerBin, err := os.ReadFile(assemblerBinPath)
	if err != nil {
		t.Fatalf("read assembler-prod.bin: %v", err)
	}
	enctabData, err := os.ReadFile(enctabPath)
	if err != nil {
		t.Fatalf("read enctab.enc: %v", err)
	}
	inData, err := os.ReadFile(tbnPath)
	if err != nil {
		t.Fatalf("read .tbn: %v", err)
	}

	t.Logf("assembler-prod.bin: %d bytes", len(assemblerBin))
	t.Logf("enctab.enc:         %d bytes", len(enctabData))
	t.Logf("inst_nop_ret.compact.tbn: %d bytes", len(inData))

	// Run the harness with a 10-second timeout.
	start := time.Now()
	result := Run(assemblerBin, enctabData, inData, 10*time.Second)
	elapsed := time.Since(start)

	t.Logf("Exit reason:    %s", result.ExitReason)
	t.Logf("Printer output: %q", result.PrinterCapture)
	t.Logf("OUT bytes:      %X", result.OutBytes)
	t.Logf("Elapsed:        %v", elapsed)
	t.Logf("Last PC in trace: %04X (last 10: %v)", lastPC(result.Last200PC), last10PCStr(result.Last200PC))

	// Timing measurement.
	fmt.Printf("\n=== TIMING: inst_nop_ret.s: %d ms per fixture ===\n\n", elapsed.Milliseconds())

	// Expected OUT bytes for `nop; ret`:
	//   nop = 0xD503201F  (little-endian: 1F 20 03 D5)
	//   ret = 0xD65F03C0  (little-endian: C0 03 5F D6)
	// Reference: aarch64-none-elf-as + objcopy -O binary.
	expectedOut := []byte{0x1F, 0x20, 0x03, 0xD5, 0xC0, 0x03, 0x5F, 0xD6}

	if !result.Passed {
		t.Errorf("harness reported FAIL; printer output: %q", result.PrinterCapture)
		t.Logf("Last 200 PC trace (oldest first):")
		for i, pc := range result.Last200PC {
			t.Logf("  [%3d] %04X", i, pc)
		}
	}

	if result.OutBytes == nil {
		t.Errorf("HSAVE never called — OUT bytes not captured")
	} else if !bytes.Equal(result.OutBytes, expectedOut) {
		t.Errorf("OUT bytes mismatch:\n  got:  %X\n  want: %X",
			result.OutBytes, expectedOut)
	}
}

func lastPC(pcs []uint16) uint16 {
	if len(pcs) == 0 {
		return 0
	}
	return pcs[len(pcs)-1]
}

func last10PCStr(pcs []uint16) string {
	start := len(pcs) - 10
	if start < 0 {
		start = 0
	}
	var sb []string
	for _, pc := range pcs[start:] {
		sb = append(sb, fmt.Sprintf("%04X", pc))
	}
	return "[" + joinStrings(sb) + "]"
}

func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += " "
		}
		result += s
	}
	return result
}
