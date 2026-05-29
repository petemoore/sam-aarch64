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
	"strconv"
	"time"
)

func main() {
	assemblerPath := flag.String("assembler", "", "path to assembler-prod.bin (or test variant assembler.bin)")
	enctabPath := flag.String("enctab", "", "path to enctab.enc")
	inPath := flag.String("in", "", "path to .tbn input file")
	timeoutStr := flag.String("timeout", "10s", "wall-clock timeout per run")
	testMemPath := flag.String("test-mem", "", "path to off-axis test_mem.bin (BUILD_TESTS variant; page 13)")
	p14Path := flag.String("p14", "", "path to paged_call_test_payload.bin (BUILD_TESTS variant; page 14)")
	sysregDataPath := flag.String("sysreg-data", "", "path to sysreg_data.bin (prod feature; CODE file \"sd13\"; page 13)")
	trigStr := flag.String("trig", "", "trigger-PC (hex, e.g. AED6): capture register snapshot + 200-PC backtrace the first time PC reaches this address")
	dumpStr := flag.String("dump", "", "comma-separated hex logical addresses to hex-dump at the trigger (requires -trig)")
	dumpLenFlag := flag.Int("dump-len", 16, "bytes to dump per -dump address")
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
	// (src/loader.asm name_test_mem / name_page14).
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
	if *sysregDataPath != "" {
		data, err := os.ReadFile(*sysregDataPath)
		if err != nil {
			log.Fatalf("read sysreg-data: %v", err)
		}
		// SAMDOS catalogue name is "sd13" (src/loader.asm name_sysreg_data).
		files = append(files, NamedFile{Name: "sd13", Content: data, TargetPage: 13})
	}

	var trigPC uint16
	if *trigStr != "" {
		v, err := strconv.ParseUint(*trigStr, 16, 16)
		if err != nil {
			log.Fatalf("invalid -trig %q: %v", *trigStr, err)
		}
		trigPC = uint16(v)
	}

	var dumpAddrs []uint16
	dumpLen := 0
	if *dumpStr != "" {
		dumpLen = *dumpLenFlag
		for _, s := range splitCSV(*dumpStr) {
			v, err := strconv.ParseUint(s, 16, 16)
			if err != nil {
				log.Fatalf("invalid -dump addr %q: %v", s, err)
			}
			dumpAddrs = append(dumpAddrs, uint16(v))
		}
	}

	start := time.Now()
	result, _, trig := RunConfig(Config{
		AssemblerBin:  assemblerBin,
		EnctabData:    enctabData,
		InData:        inData,
		Files:         files,
		Timeout:       timeout,
		TrigPC:        trigPC,
		TrigDumpAddrs: dumpAddrs,
		TrigDumpLen:   dumpLen,
	})
	elapsed := time.Since(start)

	fmt.Printf("Exit:    %s\n", result.ExitReason)
	fmt.Printf("Printer: %q\n", result.PrinterCapture)
	fmt.Printf("OUT:     %s\n", hex.EncodeToString(result.OutBytes))
	fmt.Printf("Elapsed: %v\n", elapsed)
	fmt.Printf("Steps:   %d\n", result.Steps)
	fmt.Printf("Regs:    %s\n", result.FaultRegs)
	fmt.Printf("Last PC: %04X\n", lastPCMain(result.Last200PC))
	if len(result.UnservedFiles) > 0 {
		fmt.Printf("Unserved HGTHD files: %v "+
			"(their HLOAD was a no-op; the target page is empty — "+
			"a frequent cause of a downstream &0038 trap)\n",
			result.UnservedFiles)
	}

	if trigPC != 0 {
		if trig.Hit {
			fmt.Printf("\n=== TRIGGER PC %04X hit at step %d ===\n", trigPC, trig.StepAtTrig)
			fmt.Printf("Regs at trigger: %s\n", trig.Regs)
			fmt.Printf("Backtrace (last 40 PCs before trigger, oldest first):\n")
			bt := trig.Backtrace
			s := len(bt) - 40
			if s < 0 {
				s = 0
			}
			for i, pc := range bt[s:] {
				fmt.Printf("  [%3d] %04X\n", i, pc)
			}
			if len(trig.Dump) > 0 {
				fmt.Printf("Memory dump at trigger:\n")
				for _, a := range dumpAddrs {
					fmt.Printf("  &%04X: %s\n", a, hex.EncodeToString(trig.Dump[a]))
				}
			}
		} else {
			fmt.Printf("\n=== TRIGGER PC %04X never reached ===\n", trigPC)
		}
	}

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

func splitCSV(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
		} else if r != ' ' {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func lastPCMain(pcs []uint16) uint16 {
	if len(pcs) == 0 {
		return 0
	}
	return pcs[len(pcs)-1]
}
