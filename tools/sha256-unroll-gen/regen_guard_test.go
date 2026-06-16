package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInlineBlockMatchesGenerator asserts that running gen_sha256_unrolled.py
// reproduces, byte-for-byte, the 8x-unrolled SHA-256 round block committed
// inline in src/netboot/sha256.asm (the content of the `else`
// (non-NETBOOT_TLS_CLIENT) branch of the 64-rounds section). This is the
// guarantee that the inline copy can never silently drift from the generator:
// to change the round logic you edit the script and regenerate; this test fails
// if the two diverge. The NIST KAT (tools/netboot-oracle/z80) remains the
// correctness guardrail on whatever the script emits.
func TestInlineBlockMatchesGenerator(t *testing.T) {
	repoRoot := findRepoRoot(t)

	// Run the generator.
	gen := filepath.Join(repoRoot, "tools", "sha256-unroll-gen", "gen_sha256_unrolled.py")
	cmd := exec.Command("python3", gen)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("python3 %s failed: %v\nstderr:\n%s", gen, err, stderr.String())
	}
	want := strings.TrimRight(out.String(), "\n")

	// Extract the committed inline block: the lines between the unrolled `else`
	// and its matching `endif` in src/netboot/sha256.asm.
	asmPath := filepath.Join(repoRoot, "src", "netboot", "sha256.asm")
	got := extractUnrolledBlock(t, asmPath)

	if got != want {
		// Report the first differing line for a focused failure.
		gl := strings.Split(got, "\n")
		wl := strings.Split(want, "\n")
		n := len(gl)
		if len(wl) < n {
			n = len(wl)
		}
		for i := 0; i < n; i++ {
			if gl[i] != wl[i] {
				t.Fatalf("inline block diverges from generator at line %d:\n inline:    %q\n generator: %q\nHint: regenerate with `python3 tools/sha256-unroll-gen/gen_sha256_unrolled.py` and re-splice into sha256.asm's else branch; never hand-edit the inline phases.",
					i+1, gl[i], wl[i])
			}
		}
		t.Fatalf("inline block and generator differ in length: inline %d lines, generator %d lines", len(gl), len(wl))
	}
}

// extractUnrolledBlock returns the text between the unrolled-branch `else` and
// its `endif` in sha256.asm, identified by the generated header comment that
// follows the `else`.
func extractUnrolledBlock(t *testing.T, asmPath string) string {
	t.Helper()
	f, err := os.Open(asmPath)
	if err != nil {
		t.Fatalf("open %s: %v", asmPath, err)
	}
	defer f.Close()

	var block []string
	inBlock := false
	sawElse := false
	pendingElse := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimRight(line, " \t")
		if !inBlock {
			if trimmed == "                else" {
				pendingElse = true
				continue
			}
			if pendingElse {
				// The unrolled else is the one whose first body line is the
				// generated header comment.
				if strings.Contains(line, "8 groups of 8 phases") {
					inBlock = true
					sawElse = true
					block = append(block, line)
				} else {
					pendingElse = false
				}
				continue
			}
			continue
		}
		if trimmed == "                endif" {
			break
		}
		block = append(block, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", asmPath, err)
	}
	if !sawElse {
		t.Fatalf("could not locate the unrolled `else` block in %s", asmPath)
	}
	return strings.Join(block, "\n")
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root walking up from %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}
