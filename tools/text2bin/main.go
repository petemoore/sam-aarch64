package main

import (
	"flag"
	"fmt"
	"os"

	translate "github.com/petemoore/sam-aarch64/tools/text2bin/internal/translate"
)

// includeDirsFlag is a repeatable -I flag specifying directories to search
// for .include "file" directives.
type includeDirsFlag []string

func (i *includeDirsFlag) String() string     { return fmt.Sprint(*i) }
func (i *includeDirsFlag) Set(v string) error { *i = append(*i, v); return nil }

func main() {
	var (
		outFlag string
		incDirs includeDirsFlag
	)
	flag.StringVar(&outFlag, "o", "", "output file (defaults to INPUT.tbn)")
	flag.Var(&incDirs, "I", "directory to search for .include (repeatable)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: text2bin [-I dir]... INPUT.s [-o OUTPUT.tbn]\n")
		os.Exit(2)
	}
	in := flag.Arg(0)
	src, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out, err := translate.TranslateWithOptions(src, in, translate.PreprocessOptions{IncludeDirs: incDirs})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if outFlag == "" {
		outFlag = in + ".tbn"
	}
	if err := os.WriteFile(outFlag, out, 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
