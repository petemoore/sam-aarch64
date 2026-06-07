package frontend

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
)

// TestGoldenCorpus assembles every fixture under tests/format/sources/ through the
// in-memory pipeline (Translate → Pass1 → Pass2) and byte-compares the result
// against the GNU oracle (aarch64-none-elf-as + ld -Ttext=0 + objcopy -O
// binary) — the same oracle tests/format/run-refenc-roundtrip.sh uses.
//
// This replaces the former string-matched goldens under tests/format/golden/: a
// re-assemble-and-byte-check makes a frozen-wrong golden impossible (the
// dir_skip_symbolic 80-vs-96 incident). It skips cleanly if the GNU toolchain
// is not on PATH.
func TestGoldenCorpus(t *testing.T) {
	as := toolName("AARCH64_AS", "aarch64-none-elf-as")
	if _, err := exec.LookPath(as); err != nil {
		t.Skipf("missing %s — install the aarch64 GNU toolchain or set AARCH64_AS", as)
	}
	ld := toolName("AARCH64_LD", deriveTool(as, "ld"))
	objcopy := toolName("AARCH64_OBJCOPY", deriveTool(as, "objcopy"))

	matches, err := filepath.Glob("../../../tests/format/sources/*.s")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no source fixtures found")
	}
	for _, src := range matches {
		src := src
		base := filepath.Base(src)
		t.Run(base, func(t *testing.T) {
			input, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			ours, err := assembleInMemory(input, src)
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			want, err := gnuOracle(t, as, ld, objcopy, src, len(ours) == 0)
			if err != nil {
				t.Fatalf("gnu oracle: %v", err)
			}
			if string(ours) != string(want) {
				t.Errorf("byte mismatch (ours=%dB gnu=%dB):\n ours = % X\n gnu  = % X",
					len(ours), len(want), ours, want)
			}
		})
	}
}

// assembleInMemory runs the full source → binary pipeline keeping the symbolic
// IR in memory (never serialized), mirroring sam-aarch64's plain-assemble path.
func assembleInMemory(src []byte, path string) ([]byte, error) {
	f, err := Translate(src, path)
	if err != nil {
		return nil, err
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		return nil, err
	}
	return assemble.Pass2(f, p1)
}

// gnuOracle assembles src with the GNU toolchain and returns the flat binary.
// An empty `ours` binary expects an empty oracle (an empty source has no
// sections, so ld/objcopy would otherwise fail) — matching run-refenc-
// roundtrip.sh's special case.
func gnuOracle(t *testing.T, as, ld, objcopy, src string, ourEmpty bool) ([]byte, error) {
	t.Helper()
	dir := t.TempDir()
	obj := filepath.Join(dir, "gnu.o")
	if out, err := exec.Command(as, src, "-o", obj).CombinedOutput(); err != nil {
		return nil, &cmdError{"as", out, err}
	}
	if ourEmpty {
		return nil, nil
	}
	linked := filepath.Join(dir, "gnu.linked")
	if out, err := exec.Command(ld, "-Ttext=0", obj, "-o", linked).CombinedOutput(); err != nil {
		return nil, &cmdError{"ld", out, err}
	}
	bin := filepath.Join(dir, "gnu.bin")
	if out, err := exec.Command(objcopy, "-O", "binary", linked, bin).CombinedOutput(); err != nil {
		return nil, &cmdError{"objcopy", out, err}
	}
	return os.ReadFile(bin)
}

type cmdError struct {
	tool string
	out  []byte
	err  error
}

func (e *cmdError) Error() string { return e.tool + ": " + e.err.Error() + "\n" + string(e.out) }

func toolName(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

// deriveTool swaps the `-as` suffix of an assembler name for another tool
// (e.g. aarch64-none-elf-as → aarch64-none-elf-ld), matching the fallback in
// run-refenc-roundtrip.sh.
func deriveTool(as, tool string) string {
	if len(as) >= 3 && as[len(as)-3:] == "-as" {
		return as[:len(as)-2] + tool
	}
	return tool
}
