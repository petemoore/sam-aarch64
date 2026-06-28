// coverage_test.go — item i111: the read-coverage / dead-code report.
//
// TestCoverageReport boots the BUILD_TESTS assembler (build/assembler.bin)
// through the SAME paged boot path as TestBootSelfTestsPass, but with
// read-coverage recording on.  It then maps the touched bytes back through
// build/assembler.sym and prints which named code/data ranges of the binary
// were NEVER read — the dead-code map that drives the i205 footprint pass.
//
// It UNIONS two runs for a more complete map:
//
//  1. The full boot self-test corpus (exercises most slot/fold paths before
//     main_assemble; the encode_inst family boots in the enc-tests variant,
//     a different binary, so it is out of scope for this map — i234).
//  2. A representative real assembly of several tests/core fixtures (covers
//     code that only runs during actual assembly, not boot self-tests).
//
// Run it explicitly:
//
//	cd tools/z80-test-harness-go
//	go test -run TestCoverageReport -v .
//
// It writes build/coverage-report.txt (the artefact i205 consumes) and logs the
// ranked report.  It is NOT a pass/fail gate — it asserts only that the run
// produced a plausible, non-empty report (so a broken harness can't silently
// emit an empty map).  SimCoupé remains the sole CI gate.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCoverageReport(t *testing.T) {
	root := repoRoot(t)

	asmPath := filepath.Join(root, "build", "assembler.bin")
	symPath := filepath.Join(root, "build", "assembler.sym")
	encPath := filepath.Join(root, "build", "enctab.enc")
	sd13Path := filepath.Join(root, "build", "sysreg_data.bin")
	d15Path := filepath.Join(root, "build", "disasm-test.bin")
	tmPath := filepath.Join(root, "build", "test_mem.bin")
	clusterPath := filepath.Join(root, "build", "test_cluster.bin")
	p14Path := filepath.Join(root, "build", "paged_call_test_payload.bin")
	zx0Path := filepath.Join(root, "build", "zx0-test.bin")
	samPath := filepath.Join(root, "build", "sam-aarch64")

	for _, p := range []string{asmPath, symPath, encPath, sd13Path, d15Path, tmPath, clusterPath, p14Path, zx0Path, samPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("prerequisite missing: %s\n  run `make assembler enctab sysreg-data disasm-test-payload zx0-test-payload test-mem-offaxis cluster-offaxis paged-call-payload sam-aarch64`", p)
		}
	}

	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	sd13, _ := os.ReadFile(sd13Path)
	d15, _ := os.ReadFile(d15Path)
	tm, _ := os.ReadFile(tmPath)
	cluster, _ := os.ReadFile(clusterPath)
	p14, _ := os.ReadFile(p14Path)
	zx0, _ := os.ReadFile(zx0Path)

	files := []NamedFile{
		{Name: "sd13", Content: sd13, TargetPage: 13},
		{Name: "test_mem", Content: tm, TargetPage: 13},
		{Name: "cluster", Content: cluster, TargetPage: 12},
		{Name: "p14", Content: p14, TargetPage: 14},
		{Name: "zx013", Content: zx0, TargetPage: 13, LoadOffset: 0x0400},
		{Name: "d15", Content: d15, TargetPage: 15},
	}

	// Union coverage across one boot self-test run plus assembling several
	// representative fixtures.  Each run records into its own coverage map;
	// we OR them together.
	merged := newCoverage()

	// Run 1: the full boot self-test corpus, driven with a trivial fixture
	// (same as TestBootSelfTestsPass).  Reaching a clean OK assemble proves
	// the boot ran every self-test, so the recorded coverage spans them all.
	{
		in := assembleFixtureTBN(t, root, samPath, filepath.Join(root, "tests", "core", "sources", "inst_nop_ret.s"))
		_, _, _, hw := RunConfigHW(Config{
			AssemblerBin: asm, EnctabData: enc, InData: in,
			Files: files, Timeout: 10 * time.Second, EnableCoverage: true,
		})
		mergeCoverage(merged, hw.cov)
	}

	// Run 2+: assemble every real aarch64 fixture across the test corpus to
	// cover assembly-only code paths the boot self-tests don't reach (operand
	// decoders, family encoders, directive handlers, the emit path).  We
	// discover fixtures by glob (drift-proof — no hand-maintained list) and
	// skip any the host tool or the SAM assembler can't consume (a fixture
	// that doesn't assemble simply contributes no coverage, not a failure).
	assembled, skipped := 0, 0
	for _, src := range coverageFixtures(root) {
		in, ok := tryAssembleFixtureTBN(t, root, samPath, src)
		if !ok {
			skipped++
			continue
		}
		res, _, _, hw := RunConfigHW(Config{
			AssemblerBin: asm, EnctabData: enc, InData: in,
			Files: files, Timeout: 10 * time.Second, EnableCoverage: true,
		})
		// A fixture that trapped/timed out still contributes whatever it read
		// before halting; only its coverage matters here, not pass/fail.
		_ = res
		mergeCoverage(merged, hw.cov)
		assembled++
	}
	t.Logf("union: 1 boot self-test run + %d fixture assembly runs (%d fixtures unassemblable, skipped)",
		assembled, skipped)

	// Build the report from the union.
	syms, err := loadSymbolsSorted(symPath)
	if err != nil {
		t.Fatalf("loadSymbolsSorted: %v", err)
	}
	binLen := len(asm)
	ranges := buildSymRanges(syms, binLen)
	if len(ranges) == 0 {
		t.Fatalf("no symbol ranges built from %s — sym parse or image mapping is broken", symPath)
	}
	tallyCoverage(ranges, merged, defaultExclusions())
	rep := computeReport(ranges, binLen)

	var sb strings.Builder
	rep.render(&sb)
	body := sb.String()
	t.Log("\n" + body)

	// Persist the artefact for the i205 optimization pass.
	outPath := filepath.Join(root, "build", "coverage-report.txt")
	if err := writeReportFile(outPath, body); err != nil {
		t.Logf("could not write %s: %v", outPath, err)
	} else {
		t.Logf("wrote coverage report to %s", outPath)
	}

	// Plausibility guards (NOT a gate on the assembler — a guard on the TOOL):
	// a working run touches the bulk of the resident code, so coverage must be
	// substantial and the report non-empty.  If these trip, the harness or the
	// coverage hook is broken, not the assembler.
	if rep.touchedBytes == 0 {
		t.Fatalf("coverage recorded ZERO touched bytes — the read hook is not firing")
	}
	if rep.totalBytes == 0 {
		t.Fatalf("no named bytes in the image — symbol/image mapping is broken")
	}
	covFrac := float64(rep.touchedBytes) / float64(rep.totalBytes)
	if covFrac < 0.30 {
		t.Fatalf("implausibly low coverage %.1f%% (%d/%d) — likely a harness/hook bug, not real dead code",
			100*covFrac, rep.touchedBytes, rep.totalBytes)
	}
	t.Logf("summary: %d/%d named bytes read (%.1f%%); reclaimable estimate %d bytes across %d untouched ranges",
		rep.touchedBytes, rep.totalBytes, 100*covFrac, rep.reclaimable, len(rep.untouched))
}

// coverageFixtures discovers the aarch64 fixture sources to assemble in run 2+
// to widen coverage beyond the boot self-tests.  It globs the assembler test
// corpora (core / operands / format / symbols) — drift-proof, no hand list.
// The spectrum4 / paged corpora are excluded: spectrum4 holds whole-program
// SAM/BASIC sources (not single-instruction aarch64 fixtures) and paged needs a
// special harness setup; both would just fail to assemble and be skipped anyway.
func coverageFixtures(root string) []string {
	var out []string
	for _, dir := range []string{"core", "operands", "format", "symbols"} {
		matches, _ := filepath.Glob(filepath.Join(root, "tests", dir, "sources", "*.s"))
		out = append(out, matches...)
	}
	return out
}

// assembleFixtureTBN runs the host sam-aarch64 tool to emit the compact .tbn the
// SAM assembler consumes, returning its bytes.  Mirrors TestBootSelfTestsPass.
func assembleFixtureTBN(t *testing.T, root, samPath, src string) []byte {
	t.Helper()
	in, ok := tryAssembleFixtureTBN(t, root, samPath, src)
	if !ok {
		t.Fatalf("sam-aarch64 could not assemble %s", src)
	}
	return in
}

// tryAssembleFixtureTBN is the non-fatal form: ok is false when the host tool
// can't assemble the fixture (e.g. a whole-program SAM source rather than an
// aarch64 fixture), so the caller can skip it.
func tryAssembleFixtureTBN(t *testing.T, root, samPath, src string) (data []byte, ok bool) {
	t.Helper()
	tmp := t.TempDir()
	base := strings.TrimSuffix(filepath.Base(src), ".s")
	tbnPath := filepath.Join(tmp, base+".compact.tbn")
	imgPath := filepath.Join(tmp, base+".go.img")
	cmd := exec.Command(samPath, "-o", imgPath, "-emit-tbn", tbnPath, src)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("skip %s (host assemble failed: %v)\n%s", filepath.Base(src), err, clampBytes(out, 200))
		return nil, false
	}
	in, err := os.ReadFile(tbnPath)
	if err != nil {
		return nil, false
	}
	return in, true
}

// clampBytes caps a byte slice for log output.
func clampBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// mergeCoverage ORs src's touched bits into dst.
func mergeCoverage(dst, src *coverage) {
	if src == nil {
		return
	}
	for page, bits := range src.pages {
		if bits == nil {
			continue
		}
		d := dst.pages[page]
		if d == nil {
			d = new([pageSize]bool)
			dst.pages[page] = d
		}
		for i, v := range bits {
			if v {
				d[i] = true
			}
		}
	}
}
