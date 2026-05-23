package main

import (
	"flag"
	"fmt"
	"os"

	emit "github.com/petemoore/sam-aarch64/tools/bin2text/emit"
)

func main() {
	var outFlag string
	flag.StringVar(&outFlag, "o", "", "output file (default stdout)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: bin2text INPUT.tbn [-o OUTPUT.s]")
		os.Exit(2)
	}
	in, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out, err := emit.Emit(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if outFlag == "" {
		os.Stdout.Write(out)
		return
	}
	if err := os.WriteFile(outFlag, out, 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
