// z80-test-harness-go — standalone runner.
//
// Usage:
//
//	z80-test-harness-go -assembler <assembler.bin> -enctab <enctab.enc> \
//	    -in <fixture.tbn> [-timeout 10s]
//
// Prints pass/fail, printer capture, OUT hex, and elapsed time.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"time"
)

func main() {
	assemblerPath := flag.String("assembler", "", "path to assembler-prod.bin (or test variant assembler.bin)")
	enctabPath := flag.String("enctab", "", "path to enctab.enc")
	inPath := flag.String("in", "", "path to .tbn input file")
	timeoutStr := flag.String("timeout", "10s", "wall-clock timeout per run")
	testMemPath := flag.String("test-mem", "", "path to off-axis test_mem.bin (BUILD_TESTS variant; page 13)")
	p14Path := flag.String("p14", "", "path to paged_call_test_payload.bin (BUILD_TESTS variant; page 14)")
	flag.Parse()

	if *assemblerPath == "" || *enctabPath == "" || *inPath == "" {
		flag.Usage()
		os.Exit(2)
	}

	timeout, err := time.ParseDuration(*timeoutStr)
	if err != nil {
		log.Fatalf("invalid timeout %q: %v", *timeoutStr, err)
	}

	assemblerBin, err := os.ReadFile(*assemblerPath)
	if err != nil {
		log.Fatalf("read assembler: %v", err)
	}
	enctabData, err := os.ReadFile(*enctabPath)
	if err != nil {
		log.Fatalf("read enctab: %v", err)
	}
	inData, err := os.ReadFile(*inPath)
	if err != nil {
		log.Fatalf("read IN: %v", err)
	}

	// Optional BUILD_TESTS-variant named files (HLOADed at boot into pages
	// 13/14).  The SAMDOS catalogue names are "test_mem" and "p14"
	// (src/m3/loader.asm name_test_mem / name_page14).
	var files []NamedFile
	if *testMemPath != "" {
		data, err := os.ReadFile(*testMemPath)
		if err != nil {
			log.Fatalf("read test-mem: %v", err)
		}
		files = append(files, NamedFile{Name: "test_mem", Content: data, TargetPage: 13})
	}
	if *p14Path != "" {
		data, err := os.ReadFile(*p14Path)
		if err != nil {
			log.Fatalf("read p14: %v", err)
		}
		files = append(files, NamedFile{Name: "p14", Content: data, TargetPage: 14})
	}

	start := time.Now()
	result := RunWithFiles(assemblerBin, enctabData, inData, files, timeout)
	elapsed := time.Since(start)

	fmt.Printf("Exit:    %s\n", result.ExitReason)
	fmt.Printf("Printer: %q\n", result.PrinterCapture)
	fmt.Printf("OUT:     %s\n", hex.EncodeToString(result.OutBytes))
	fmt.Printf("Elapsed: %v\n", elapsed)
	fmt.Printf("Steps:   %d\n", result.Steps)
	fmt.Printf("Regs:    %s\n", result.FaultRegs)
	fmt.Printf("Last PC: %04X\n", lastPCMain(result.Last200PC))

	if result.Passed {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
		fmt.Printf("Last 30 PC (oldest first):\n")
		pcs := result.Last200PC
		start := len(pcs) - 30
		if start < 0 {
			start = 0
		}
		for i, pc := range pcs[start:] {
			fmt.Printf("  [%3d] %04X\n", i, pc)
		}
		os.Exit(1)
	}
}

func lastPCMain(pcs []uint16) uint16 {
	if len(pcs) == 0 {
		return 0
	}
	return pcs[len(pcs)-1]
}
