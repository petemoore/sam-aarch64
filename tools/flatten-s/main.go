// flatten-s recursively expands GNU `as` `.include` directives in a
// .s file. Mirrors the assembler's behaviour: each `.include "name"`
// is replaced by the contents of the file found in the include path
// list (searched in order, like `as -I dir1 -I dir2 …`).
//
// `.macro` / `.endm` are NOT expanded — that's a separate problem
// (the spectrum4 macros use parameter substitution, which is GNU as
// frontend-level magic). For now flatten-s emits macros verbatim
// into the output; downstream consumers must either handle them or
// pre-strip them.
//
// Usage:
//
//	flatten-s [-I dir]... [-o output.s] input.s
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(s string) error { *l = append(*l, s); return nil }

func main() {
	var includes stringList
	var outFlag string
	flag.Var(&includes, "I", "include search path (repeatable, like `as -I`)")
	flag.StringVar(&outFlag, "o", "", "output file (default stdout)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: flatten-s [-I dir]... [-o out.s] input.s")
		os.Exit(2)
	}
	in := flag.Arg(0)

	// Implicit search path: the directory of the input file (mimics
	// gas behaviour for relative includes).
	inDir := filepath.Dir(in)
	includes = append([]string{inDir}, includes...)

	var out io.Writer = os.Stdout
	if outFlag != "" {
		f, err := os.Create(outFlag)
		if err != nil {
			fail(err)
		}
		defer f.Close()
		out = f
	}

	seen := map[string]bool{}
	if err := flatten(out, in, includes, seen, 0); err != nil {
		fail(err)
	}
}

var includeRe = regexp.MustCompile(`^\s*\.include\s+"([^"]+)"`)

const maxDepth = 64

func flatten(out io.Writer, path string, includes []string, seen map[string]bool, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("flatten: max include depth %d exceeded at %s", maxDepth, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if seen[abs] {
		// Cyclic include: skip silently (gas allows multiple includes
		// of the same header guard-style file; we choose include-once).
		return nil
	}
	seen[abs] = true

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(out, "// flatten-s: BEGIN %s\n", path)
	defer fmt.Fprintf(out, "// flatten-s: END %s\n", path)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		m := includeRe.FindStringSubmatch(line)
		if m == nil {
			fmt.Fprintln(out, line)
			continue
		}
		incName := m[1]
		incPath, ok := resolve(incName, filepath.Dir(path), includes)
		if !ok {
			return fmt.Errorf("flatten: %s: cannot find include %q in any of: %v",
				path, incName, includes)
		}
		if err := flatten(out, incPath, includes, seen, depth+1); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// resolve searches for incName under the current file's dir first,
// then each entry of `-I`. Mirrors `gas`'s include-path semantics.
func resolve(incName, curDir string, includes []string) (string, bool) {
	// Absolute paths are taken as-is.
	if filepath.IsAbs(incName) {
		if _, err := os.Stat(incName); err == nil {
			return incName, true
		}
		return "", false
	}
	// Try current-file dir.
	candidate := filepath.Join(curDir, incName)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, true
	}
	// Try each include path.
	for _, dir := range includes {
		candidate := filepath.Join(dir, incName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
