// aarch64dec disassembles a raw aarch64 binary, emitting one line per
// 4-byte instruction.  Output mirrors `aarch64-elf-objdump -D -b
// binary -m aarch64` line-for-line so that diffs against the oracle
// are mechanical.
//
//	aarch64dec [-base N] [-asm] FILE.bin
//
//	-base N   address of the first instruction (default 0)
//	-asm      emit labeled assembly instead of objdump-format output.
//	          Branch targets within the binary are replaced with
//	          synthetic labels L0/L1/… — safe for editor import and
//	          text2bin re-assembly.
//
// FILE.bin must be a multiple of 4 bytes; trailing partial words are
// rejected.  Words that no Form matches render as `.inst 0xNNNNNNNN`.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/petemoore/sam-aarch64/tools/aarch64dec"
)

func main() {
	var base uint64
	var asmMode bool
	flag.Uint64Var(&base, "base", 0,
		"byte address of the first instruction (default 0)")
	flag.BoolVar(&asmMode, "asm", false,
		"emit labeled assembly (text2bin-compatible) instead of objdump format")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: aarch64dec [-base N] [-asm] FILE.bin")
		os.Exit(2)
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fail(err)
	}
	if len(data)%4 != 0 {
		fail(fmt.Errorf("input length %d is not a multiple of 4", len(data)))
	}
	if asmMode {
		if err := aarch64dec.WriteAsm(os.Stdout, base, data); err != nil {
			fail(err)
		}
		return
	}
	if err := disasmTo(os.Stdout, base, data); err != nil {
		fail(err)
	}
}

// disasmTo writes one line per 4-byte word to w, formatted to match
// objdump's `<addr>:\t<word>\t<mnem>\t<operands>` layout.  The
// address column uses a minimum width of 4 hex chars and grows for
// larger binaries (mirroring objdump).
func disasmTo(w io.Writer, base uint64, data []byte) error {
	addrWidth := addrFieldWidth(base + uint64(len(data)) - 4)
	for i := 0; i < len(data); i += 4 {
		pc := base + uint64(i)
		word := binary.LittleEndian.Uint32(data[i : i+4])
		mnem, ops, ok := aarch64dec.DecodeAt(pc, word)
		var line string
		if ok {
			line = aarch64dec.Format(mnem, ops)
		} else {
			line = fmt.Sprintf(".inst\t%#08x", word)
		}
		if _, err := fmt.Fprintf(w, "%*x:\t%08x \t%s\n",
			addrWidth, pc, word, line); err != nil {
			return err
		}
	}
	return nil
}

// addrFieldWidth returns the right-justification width for the
// address column.  Matches objdump's behaviour: 4 chars minimum,
// growing one char at a time as the maximum address overflows.
func addrFieldWidth(maxAddr uint64) int {
	width := 4
	for (uint64(1) << (uint64(width) * 4)) <= maxAddr {
		width++
	}
	return width
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
