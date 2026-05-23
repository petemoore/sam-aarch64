package translate

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	emit "github.com/petemoore/sam-aarch64/tools/bin2text/emit"
)

var updateGoldens = flag.Bool("update", false, "rewrite golden files")

func TestGoldenCorpus(t *testing.T) {
	matches, err := filepath.Glob("../../../../tests/m1/sources/*.s")
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
			bin, err := Translate(input, src)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			out, err := emit.Emit(bin)
			if err != nil {
				t.Fatalf("Emit: %v", err)
			}
			goldenPath := filepath.Join("../../../../tests/m1/golden", base)
			if *updateGoldens {
				if err := os.WriteFile(goldenPath, out, 0644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden: %v (run with -update to create)", err)
			}
			if string(out) != string(want) {
				t.Errorf("golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", out, want)
			}
		})
	}
}
