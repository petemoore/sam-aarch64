package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
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
		usage = newUsage()
	}
	in, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fail(err)
	}
	f, err := format.ReadFile(in)
	if err != nil {
		fail(err)
	}
	p1, err := Pass1(f)
	if err != nil {
		fail(err)
	}
	if emitCompact != "" {
		if err := writeCompactTBN(emitCompact, f, p1); err != nil {
			fail(err)
		}
	}
	out, err := Pass2(f, p1)
	if err != nil {
		fail(err)
	}
	if dumpUsage {
		usage.TotalOutBytes = len(out)
		usage.Dump(os.Stderr)
	}
	if outFlag == "" {
		os.Stdout.Write(out)
		return
	}
	if err := os.WriteFile(outFlag, out, 0644); err != nil {
		fail(err)
	}
}

// writeCompactTBN compacts f's record stream and writes it as a new
// .tbn at path, reusing f's name table (rebuilt by interning the names
// in ID order, which reproduces the same IDs the records reference).
func writeCompactTBN(path string, f *format.File, p1 *Pass1Result) error {
	compacted, err := Compact(f, p1)
	if err != nil {
		return err
	}
	st := format.NewSymbolTable()
	for _, n := range f.Names {
		st.Intern(n)
	}
	labels, locals := headerRows(f, p1)
	var buf bytes.Buffer
	if err := format.WriteFile(&buf, st, labels, locals, compacted); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
