package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var outFlag string
	flag.StringVar(&outFlag, "o", "", "output binary")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: refenc INPUT.tbn -o OUTPUT.bin")
		os.Exit(2)
	}
	// Pass 2 wiring lands in Task 19.
	_ = outFlag
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
