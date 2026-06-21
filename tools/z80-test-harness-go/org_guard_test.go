// org_guard_test.go — proves the Z80 pass-1 backward-`.org` guard fires.
//
// `.org N` sets PASS_PC := N. A backward .org (N < current PC) is an
// error. Pass 2 always caught it, but pass 1 used to set PASS_PC
// backward silently — computing garbage label addresses before pass 2's
// mirror check fired. compute / main_dir_org_pass1 now rejects it in
// pass 1 with tag a1 (i73 L7), matching Go pass1.go:297. A leading
// origin .org (low 32 == PASS_PC == 0 at pass start) is not backward and
// still assembles.
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

type orgStep struct {
	isOrg  bool
	target int64
}

func orgNop() orgStep        { return orgStep{} }
func orgSet(n int64) orgStep { return orgStep{isOrg: true, target: n} }

// buildOrgTbn assembles a tiny .tbn from an ordered program of nops and
// `.org N` directives.
func buildOrgTbn(t *testing.T, program ...orgStep) []byte {
	t.Helper()
	st := format.NewSymbolTable()
	var rw format.RecordWriter
	orgID, ok := format.DirectiveID(".org")
	if !ok {
		t.Fatal("no .org directive id")
	}
	for _, s := range program {
		if s.isOrg {
			var e format.ExprWriter
			e.WriteImm(s.target)
			var ow format.OperandWriter
			ow.WriteImmExpr(e.Bytes())
			rw.WriteDirective(orgID, 1, ow.Bytes())
		} else {
			rw.WriteInsnRun(1, []format.InsnElement{{BaseWord: 0xd503201f}}) // nop
		}
	}
	var buf bytes.Buffer
	if err := format.WriteFile(&buf, st, nil, nil, rw.Bytes(), nil, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return buf.Bytes()
}

func TestOrgBackwardGuard(t *testing.T) {
	build := filepath.Join(repoRoot(t), "build")
	asm, _ := os.ReadFile(filepath.Join(build, "assembler-prod.bin"))
	enc, _ := os.ReadFile(filepath.Join(build, "enctab.enc"))

	// spectrum4's origin VMA: low 32 bits are 0, so a leading .org to it
	// is not backward from PASS_PC == 0.
	const originVMA = int64(-0x0000001000000000) // 0xfffffff0_00000000

	cases := []struct {
		name    string
		program []orgStep
		wantTag string // "" => should assemble cleanly
	}{
		{"leading origin .org (low32 == 0)", []orgStep{orgSet(originVMA), orgNop()}, ""},
		{"forward .org (delta 4)", []orgStep{orgNop(), orgSet(8)}, ""},
		{"equal .org (delta 0)", []orgStep{orgNop(), orgSet(4)}, ""},
		{"backward .org to 0", []orgStep{orgNop(), orgSet(0)}, "FAILa1"},
		{"backward .org to 2", []orgStep{orgNop(), orgSet(2)}, "FAILa1"},
	}

	for _, c := range cases {
		tbn := buildOrgTbn(t, c.program...)
		res := runProdComplete(t, asm, enc, tbn, 10*time.Second)
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
			t.Errorf("%s: expected %s, but assembly PASSED (pass-1 set PASS_PC backward!)", c.name, c.wantTag)
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
