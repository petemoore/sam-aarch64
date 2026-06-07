// sweep_test.go — THROWAWAY full-corpus divergence sweep (investigation only).
//
// Runs every M3-M6 fixture (the same sources the SimCoupé ci-m{3,4,5,6}
// matrix runs) through the Go harness in BOTH the prod and the BUILD_TESTS
// variant, and compares the harness OUT bytes against the GNU oracle
// (aarch64-*-as + ld -Ttext=0 + objcopy -O binary).  Produces a divergence
// map for https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-29-go-harness-fidelity-investigation.md.
//
// Not a CI gate.  Gated behind SWEEP=1 so normal `go test ./...` skips it.
//
//	SWEEP=1 go test -run TestCorpusSweep -v ./... 2>&1
//
// Assumes prerequisites already built into ../../build:
//
//	build/sam-aarch64 build/enctab.enc build/assembler-prod.bin build/assembler.bin
//	build/test_mem.bin build/paged_call_test_payload.bin
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestCorpusSweep(t *testing.T) {
	if os.Getenv("SWEEP") != "1" {
		t.Skip("set SWEEP=1 to run the full-corpus divergence sweep")
	}

	root := "../.."
	build := filepath.Join(root, "build")

	sam := filepath.Join(build, "sam-aarch64")
	if _, e := os.Stat(sam); e != nil {
		t.Skipf("build/sam-aarch64 not built (run `make sam-aarch64`): %v", e)
	}
	enctab, err := os.ReadFile(filepath.Join(build, "enctab.enc"))
	if err != nil {
		t.Fatalf("read enctab: %v", err)
	}
	asmProd, err := os.ReadFile(filepath.Join(build, "assembler-prod.bin"))
	if err != nil {
		t.Fatalf("read assembler-prod.bin: %v", err)
	}
	asmTest, err := os.ReadFile(filepath.Join(build, "assembler.bin"))
	if err != nil {
		t.Fatalf("read assembler.bin: %v", err)
	}
	testMem, _ := os.ReadFile(filepath.Join(build, "test_mem.bin"))
	cluster, _ := os.ReadFile(filepath.Join(build, "test_cluster.bin"))
	pagedCall, _ := os.ReadFile(filepath.Join(build, "paged_call_test_payload.bin"))
	sysregData, _ := os.ReadFile(filepath.Join(build, "sysreg_data.bin"))

	// Toolchain: prefer aarch64-none-elf-*, fall back to linux-gnu.
	as, ld, objcopy := "aarch64-none-elf-as", "aarch64-none-elf-ld", "aarch64-none-elf-objcopy"
	if _, e := exec.LookPath(as); e != nil {
		as, ld, objcopy = "aarch64-linux-gnu-as", "aarch64-linux-gnu-ld", "aarch64-linux-gnu-objcopy"
	}

	// BUILD_TESTS named files: sd13/test_mem on page 13 (time-multiplexed),
	// cluster on page 12, p14 on page 14 (off-axis layout from plan-PR 3 /
	// paged-call architecture + M6 budget-relief cluster).  sd13 listed
	// before test_mem so test_mem wins the initial page-13 pre-deposit
	// (run_mem_self_tests runs first); load_page13_payload later HLOADs
	// sd13 over page 13 before the sysreg self-tests read it.
	testFiles := []NamedFile{
		{Name: "sd13", Content: sysregData, TargetPage: 13},
		{Name: "test_mem", Content: testMem, TargetPage: 13},
		{Name: "cluster", Content: cluster, TargetPage: 12},
		{Name: "p14", Content: pagedCall, TargetPage: 14},
	}

	type row struct {
		milestone string
		fixture   string
		variant   string
		harnessOK bool
		bytematch bool
		gnuLen    int
		ourLen    int
		exit      string
		note      string
	}
	var rows []row

	milestones := []string{"m3", "m4", "m5", "m6"}
	tmp := t.TempDir()

	for _, m := range milestones {
		srcGlob := filepath.Join(root, "tests", m, "sources", "*.s")
		srcs, _ := filepath.Glob(srcGlob)
		sort.Strings(srcs)
		for _, src := range srcs {
			base := filepath.Base(src)
			base = base[:len(base)-len(".s")]

			// Assemble to a binary + compact .tbn (sam-aarch64 --emit-tbn):
			// the SAM assembler consumes the compact v2 .tbn (INSN_RUN
			// decoder).
			tbnPath := filepath.Join(tmp, base+".compact.tbn")
			goImgPath := filepath.Join(tmp, base+".go.img")
			if out, e := exec.Command(sam, "-o", goImgPath, "-emit-tbn", tbnPath, src).CombinedOutput(); e != nil {
				rows = append(rows, row{m, base, "-", false, false, 0, 0, "sam-aarch64-fail", string(out)})
				continue
			}
			tbn, _ := os.ReadFile(tbnPath)

			oPath := filepath.Join(tmp, base+".o")
			elfPath := filepath.Join(tmp, base+".elf")
			gnuPath := filepath.Join(tmp, base+".gnu.bin")
			gnuOK := true
			if _, e := exec.Command(as, src, "-o", oPath).CombinedOutput(); e != nil {
				gnuOK = false
			}
			if gnuOK {
				_ = exec.Command(ld, "-Ttext=0", "-o", elfPath, oPath).Run()
				_ = exec.Command(objcopy, "-O", "binary", elfPath, gnuPath).Run()
			}
			gnu, _ := os.ReadFile(gnuPath)

			for _, v := range []struct {
				name  string
				bin   []byte
				files []NamedFile
			}{
				{"prod", asmProd, nil},
				{"test", asmTest, testFiles},
			} {
				res := RunWithFiles(v.bin, enctab, tbn, v.files, 10*time.Second)
				hok := res.Passed
				match := hok && bytes.Equal(res.OutBytes, gnu)
				note := ""
				if !hok {
					note = "banner=" + truncate(res.PrinterCapture, 16)
				} else if !match {
					note = "OK-but-bytes-differ"
				}
				rows = append(rows, row{
					m, base, v.name, hok, match, len(gnu), len(res.OutBytes),
					res.ExitReason, note,
				})
			}
		}
	}

	fmt.Println("\n=== CORPUS SWEEP RESULTS ===")
	fmt.Println("| milestone | fixture | variant | harnessOK | bytematch | gnuLen | ourLen | exit | note |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|")
	var nProd, nProdOK, nProdMatch, nTest, nTestOK, nTestMatch int
	for _, r := range rows {
		fmt.Printf("| %s | %s | %s | %v | %v | %d | %d | %s | %s |\n",
			r.milestone, r.fixture, r.variant, r.harnessOK, r.bytematch,
			r.gnuLen, r.ourLen, r.exit, r.note)
		switch r.variant {
		case "prod":
			nProd++
			if r.harnessOK {
				nProdOK++
			}
			if r.bytematch {
				nProdMatch++
			}
		case "test":
			nTest++
			if r.harnessOK {
				nTestOK++
			}
			if r.bytematch {
				nTestMatch++
			}
		}
	}
	fmt.Println("\n=== SUMMARY ===")
	fmt.Printf("PROD variant: %d fixtures, %d harness-OK, %d byte-match-GNU\n", nProd, nProdOK, nProdMatch)
	fmt.Printf("TEST variant: %d fixtures, %d harness-OK, %d byte-match-GNU\n", nTest, nTestOK, nTestMatch)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
