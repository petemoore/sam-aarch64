package main

import (
	"flag"
	"fmt"
	"os"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
)

func main() {
	var outFlag string
	var dumpUsage bool
	var emitCompact string
	flag.StringVar(&outFlag, "o", "", "output binary")
	flag.BoolVar(&dumpUsage, "dump-usage", false,
		"after assembly, print a peak-usage census of all internal "+
			"data structures (symbol table, local labels, literal "+
			"pool, expr evaluator, OPVAL buffer, record stream) to "+
			"stderr — used for sizing the Z80-side fixed tables.")
	flag.StringVar(&emitCompact, "emit-compact-tbn", "",
		"also write a compacted v2 .tbn to this path: instructions are "+
			"collapsed into INSN_RUN records (assembled base words plus a "+
			"sparse overlay patch for symbol/PC-bearing fields), shrinking "+
			"the file while assembling to the identical binary. The normal "+
			"-o binary is unaffected.")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: refenc INPUT.tbn -o OUTPUT.bin [--dump-usage]")
		os.Exit(2)
	}
	if dumpUsage {
		assemble.EnableUsage()
	}
	in, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fail(err)
	}
	f, err := format.ReadFile(in)
	if err != nil {
		fail(err)
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		fail(err)
	}
	if emitCompact != "" {
		b, err := assemble.CompactTBNBytes(f, p1)
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(emitCompact, b, 0644); err != nil {
			fail(err)
		}
	}
	out, err := assemble.Pass2(f, p1)
	if err != nil {
		fail(err)
	}
	if dumpUsage {
		assemble.DumpUsage(os.Stderr, len(out))
	}
	if outFlag == "" {
		os.Stdout.Write(out)
		return
	}
	if err := os.WriteFile(outFlag, out, 0644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
