package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Translate is the library entry point — accepts source bytes and a
// path (for error messages) and returns the encoded .tbn bytes.
func Translate(src []byte, path string) ([]byte, error) {
	st := format.NewSymbolTable()
	var rw format.RecordWriter
	// Real parser arrives in Task 18.

	var out bytes.Buffer
	if err := format.WriteFile(&out, st, rw.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

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
	out, err := Translate(src, in)
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
