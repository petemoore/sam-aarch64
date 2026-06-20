// samsym.go — parsing the pyz80 --exportfile symbol table (build/assembler.sym)
// and resolving an address to its nearest preceding code symbol.
//
// Shared by the FAIL-banner diagnostic (fail_diag_test.go) and the i111
// read-coverage report (coverage.go), so it lives in non-test code.
package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// codeOrigin is the assembler's code ORIGIN (&8000).  Symbols below it are
// build flags / data constants, never code call sites, so resolvers ignore them.
const codeOrigin = 0x8000

// loadSAMSymbols parses a pyz80 --exportfile symbol table (a text-protocol
// Python pickle of a flat {name: int} dict) into name→address.  The stream is
// a sequence of `[s]V<name>\n p<n>\n I<addr>\n` entries; we pair each V-key
// with the I-value that follows it and ignore every other opcode.
func loadSAMSymbols(path string) (map[string]uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	syms := make(map[string]uint32)
	var pending string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		// A leading `s` is the SETITEM marker for the PREVIOUS pair, glued
		// onto this line's opcode (e.g. "sVNAME", "s."); strip it.
		if strings.HasPrefix(line, "s") {
			line = line[1:]
		}
		switch {
		case strings.HasPrefix(line, "V"):
			pending = line[1:]
		case strings.HasPrefix(line, "I") && pending != "":
			if v, err := strconv.ParseInt(line[1:], 10, 64); err == nil {
				syms[pending] = uint32(v)
			}
			pending = ""
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return syms, nil
}

// resolveNearestSymbol returns the code-region symbol with the greatest
// address ≤ pc, i.e. the label the PC sits inside.  ok is false when pc is
// below the code origin (an off-axis logical address build/assembler.sym
// cannot name).
func resolveNearestSymbol(syms map[string]uint32, pc uint16) (name string, off uint32, ok bool) {
	if uint32(pc) < codeOrigin {
		return "", 0, false
	}
	var bestAddr uint32
	for n, addr := range syms {
		if addr >= codeOrigin && addr <= uint32(pc) && (name == "" || addr > bestAddr) {
			name, bestAddr = n, addr
		}
	}
	if name == "" {
		return "", 0, false
	}
	return name, uint32(pc) - bestAddr, true
}
