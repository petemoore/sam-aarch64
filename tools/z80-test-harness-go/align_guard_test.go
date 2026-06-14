// align_guard_test.go — proves the Z80 `.align N` exponent guard fires.
//
// `.align N` aligns to 2^N bytes. The SAM OUT buffer is 32 KB (pages
// 5+6), so only N in 1..15 (alignment 2..32768) is representable and
// reachable; N >= 16 needs a >= 64 KB pad the buffer can never hold, and
// negatives / N >= 256 are nonsense. compute_dir_size_align must FAIL
// those with tag a0 rather than letting the 1<<N shift overflow its
// 16-bit register to a silent no-op (i73 L6).
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// buildAlignTbn assembles a tiny .tbn: one nop, then `.align n`. The nop
// gives a non-zero PC so a valid align emits a real pad.
func buildAlignTbn(t *testing.T, n int64) []byte {
	t.Helper()
	st := format.NewSymbolTable()
	var rw format.RecordWriter

	// nop (so PASS cases produce a real, non-empty output)
	rw.WriteInsnRun(1, []format.InsnElement{{BaseWord: 0xd503201f}})

	alignID, ok := format.DirectiveID(".align")
	if !ok {
		t.Fatal("no .align directive id")
	}
	var nExpr format.ExprWriter
	nExpr.WriteImm(n)
	var ow format.OperandWriter
	ow.WriteImmExpr(nExpr.Bytes())
	rw.WriteDirective(alignID, 1, ow.Bytes())

	var buf bytes.Buffer
	if err := format.WriteFile(&buf, st, nil, nil, rw.Bytes(), nil, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return buf.Bytes()
}

func TestAlignExponentGuard(t *testing.T) {
	build := filepath.Join(repoRoot(t), "build")
	asm, _ := os.ReadFile(filepath.Join(build, "assembler-prod.bin"))
	enc, _ := os.ReadFile(filepath.Join(build, "enctab.enc"))

	cases := []struct {
		name    string
		n       int64
		wantTag string // "" => should assemble cleanly
	}{
		{"align 4 (16-byte)", 4, ""},
		{"align 12 (4096-byte)", 12, ""},
		{"align 15 (32768-byte, the max)", 15, ""},
		{"align 16 (64 KB, exceeds OUT buffer)", 16, "FAILa0"},
		{"align 31 (GNU rejects too)", 31, "FAILa0"},
		{"align 256 (high byte set)", 256, "FAILa0"},
		{"align -1 (negative)", -1, "FAILa0"},
	}

	for _, c := range cases {
		tbn := buildAlignTbn(t, c.n)
		res := RunWithFiles(asm, enc, tbn, nil, 10*time.Second)
		if c.wantTag == "" {
			if !res.Passed {
				t.Errorf("%s: expected clean assembly, got printer=%q exit=%s",
					c.name, res.PrinterCapture, res.ExitReason)
			} else {
				t.Logf("%s: assembled as expected", c.name)
			}
			continue
		}
		if res.Passed {
			t.Errorf("%s: expected %s, but assembly PASSED (silent no-op!)", c.name, c.wantTag)
			continue
		}
		if !strings.Contains(res.PrinterCapture, c.wantTag) {
			t.Errorf("%s: expected printer to contain %q, got %q (exit=%s)",
				c.name, c.wantTag, res.PrinterCapture, res.ExitReason)
		} else {
			t.Logf("%s: %s as expected", c.name, c.wantTag)
		}
	}
}
