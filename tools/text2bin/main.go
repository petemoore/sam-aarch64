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
		outFlag     string
		incDirs     includeDirsFlag
		flattenFlag bool
		originStr   string
	)
	flag.StringVar(&outFlag, "o", "", "output file (defaults to INPUT.tbn)")
	flag.Var(&incDirs, "I", "directory to search for .include (repeatable)")
	flag.BoolVar(&flattenFlag, "flatten", false,
		"after parsing, apply the linker-equivalent flatten pass "+
			"(spectrum4 layout: .text + BSS labels as .equ).")
	flag.StringVar(&originStr, "origin", "0xfffffff000000000",
		"origin VMA emitted at the start of -flatten output "+
			"(the linker script's `. = N`); ignored without -flatten.")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: text2bin [-I dir]... [-flatten [-origin N]] INPUT.s [-o OUTPUT.tbn]\n")
		os.Exit(2)
	}
	in := flag.Arg(0)
	src, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var out []byte
	if flattenFlag {
		origin, err := parseInt64(originStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad -origin %q: %v\n", originStr, err)
			os.Exit(2)
		}
		out, err = translate.TranslateAndFlatten(src, in,
			translate.PreprocessOptions{IncludeDirs: incDirs},
			translate.FlattenOptions{OriginVMA: origin})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	} else {
		out, err = translate.TranslateWithOptions(src, in,
			translate.PreprocessOptions{IncludeDirs: incDirs})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if outFlag == "" {
		outFlag = in + ".tbn"
	}
	if err := os.WriteFile(outFlag, out, 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// parseInt64 accepts decimal, 0x-prefixed hex, or 0b-prefixed binary;
// addresses up to 2^64-1 round-trip via uint64→int64 bit-cast.
func parseInt64(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	} else if s[0] == '+' {
		s = s[1:]
	}
	base := 10
	switch {
	case len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X"):
		base = 16
		s = s[2:]
	case len(s) > 2 && (s[:2] == "0b" || s[:2] == "0B"):
		base = 2
		s = s[2:]
	}
	var v uint64
	for _, c := range []byte(s) {
		d := -1
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		}
		if d < 0 || d >= base {
			return 0, fmt.Errorf("invalid digit %q", c)
		}
		v = v*uint64(base) + uint64(d)
	}
	r := int64(v)
	if neg {
		r = -r
	}
	return r, nil
}
