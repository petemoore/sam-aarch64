package main

import (
	"flag"
	"fmt"
	"os"

	translate "github.com/petemoore/sam-aarch64/tools/text2bin/internal/translate"
)

func main() {
	var outFlag string
	flag.StringVar(&outFlag, "o", "", "output file (defaults to INPUT.tbn)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: text2bin INPUT.s [-o OUTPUT.tbn]\n")
		os.Exit(2)
	}
	in := flag.Arg(0)
	src, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out, err := translate.Translate(src, in)
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
