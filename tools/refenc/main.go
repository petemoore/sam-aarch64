package main

import (
	"flag"
	"fmt"
	"os"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func main() {
	var outFlag string
	flag.StringVar(&outFlag, "o", "", "output binary")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: refenc INPUT.tbn -o OUTPUT.bin")
		os.Exit(2)
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
	out, err := Pass2(f, p1)
	if err != nil {
		fail(err)
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
