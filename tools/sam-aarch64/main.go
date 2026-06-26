// Command sam-aarch64 is the integrated host assembler for the SAM-Coupé
// aarch64 toolchain. It mirrors the SAM's single text→overlay→bytes
// assembler: one tool does source→{binary, compact .tbn}, .tbn→binary,
// and .tbn→text, with the "symbolic" record stream living only in memory
// (never serialized to disk — see the i48 design, decision A).
//
// Modes:
//
//	sam-aarch64 SRC.s -o OUT.img [--emit-tbn OUT.tbn] [-I dir]... \
//	            [-flatten [-origin N]] [-strip-comments] [-strip-data] [--dump-usage]
//	    Assemble source: preprocess+parse → in-memory symbolic IR →
//	    pass-1/pass-2 → binary. With --emit-tbn, also write the compact
//	    overlay .tbn (what the SAM loads).
//
//	sam-aarch64 IN.tbn -o OUT.img [--dump-usage]
//	    Assemble an existing (compact overlay) .tbn → binary.
//
//	sam-aarch64 --render IN.tbn [-o OUT.s]
//	    Render a .tbn back to text (the former bin2text).
//
//	sam-aarch64 -E SRC.s [-I dir]... [-o OUT.s]
//	    Preprocess only: emit expanded source (includes/macros/.if), like cpp -E.
//
// The input kind (source vs .tbn) is detected from the .tbn magic; -E and
// --render are explicit mode flags.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
	render "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
)

// includeDirsFlag is a repeatable -I flag specifying directories to search
// for .include "file" directives.
type includeDirsFlag []string

// String renders the accumulated include directories for flag display.
func (i *includeDirsFlag) String() string { return fmt.Sprint(*i) }

// Set appends one -I directory to the list each time the flag is given,
// implementing flag.Value so -I is repeatable.
func (i *includeDirsFlag) Set(v string) error { *i = append(*i, v); return nil }

func main() {
	var (
		outFlag       string
		emitTBN       string
		incDirs       includeDirsFlag
		flattenFlag   bool
		originStr     string
		layoutFile    string
		stripComments bool
		thinComments  int
		stripData     bool
		preprocess    bool
		renderMode    bool
		dumpUsage     bool
		fix835769     bool
		fix843419     bool
	)
	flag.StringVar(&outFlag, "o", "", "output file (default stdout)")
	flag.StringVar(&emitTBN, "emit-tbn", "",
		"when assembling source, also write the compact overlay .tbn to "+
			"this path (the form the SAM loads). Ignored when the input is "+
			"already a .tbn.")
	flag.Var(&incDirs, "I", "directory to search for .include (repeatable)")
	flag.BoolVar(&flattenFlag, "flatten", false,
		"after parsing, apply the linker-equivalent flatten pass "+
			"(spectrum4 layout: .text + BSS labels as .equ).")
	flag.StringVar(&layoutFile, "layout", "",
		"JSON section-layout file for -flatten (default: the built-in "+
			"spectrum4 layout; see frontend/testdata/spectrum4.layout.json).")
	flag.StringVar(&originStr, "origin", "0xfffffff000000000",
		"origin VMA emitted at the start of -flatten output "+
			"(the linker script's `. = N`); ignored without -flatten.")
	flag.BoolVar(&stripComments, "strip-comments", false,
		"after parsing, remove all COMMENT records (fits the .tbn within "+
			"the SAM assembler's IN-buffer ceiling for the release source).")
	flag.IntVar(&thinComments, "thin-comments", 0,
		"keep only one in every N COMMENT records (N>1), stripping the rest; "+
			"0/1 keeps all. Used by the m6/SAM gate to flow a bounded comment "+
			"subset through the Z80 round-trip under the IN-buffer ceiling.")
	flag.BoolVar(&stripData, "strip-data", false,
		"after parsing, remove all data-emitting records (.word/.quad/etc "+
			"and ldr Xn,=expr): a code-only input for the disasm round-trip gate.")
	flag.BoolVar(&preprocess, "E", false,
		"preprocess only: emit the expanded SOURCE (includes resolved, "+
			"macros expanded, .if evaluated) and stop. Mirrors cpp -E.")
	flag.BoolVar(&renderMode, "render", false,
		"render a .tbn back to canonical text (the former bin2text).")
	flag.BoolVar(&dumpUsage, "dump-usage", false,
		"after assembly, print a peak-usage census of all internal data "+
			"structures to stderr — used for sizing the Z80-side tables.")
	flag.BoolVar(&fix835769, "fix-cortex-a53-835769", false,
		"apply the Cortex-A53 erratum 835769 workaround to the binary: "+
			"insert a NOP between a memory op and an immediately following "+
			"64-bit multiply-accumulate. Off by default (only A53/Pi 3 is "+
			"affected); does not alter the emitted .tbn.")
	flag.BoolVar(&fix843419, "fix-cortex-a53-843419", false,
		"apply the Cortex-A53 erratum 843419 workaround to the binary: "+
			"rewrite an ADRP at a page-boundary slot (in an affected ld/st "+
			"sequence) to an ADR. Off by default; does not alter the .tbn.")
	flag.Parse()
	if flag.NArg() != 1 {
		usageErr()
	}
	if layoutFile != "" && !flattenFlag {
		fail(fmt.Errorf("-layout has no effect without -flatten"))
	}
	in := flag.Arg(0)
	src, err := os.ReadFile(in)
	if err != nil {
		fail(err)
	}

	switch {
	case renderMode:
		runRender(src, outFlag)
	case preprocess:
		if flattenFlag || stripComments || thinComments > 0 || stripData || emitTBN != "" {
			fail(fmt.Errorf("-E cannot be combined with -flatten/-strip-comments/-thin-comments/-strip-data/--emit-tbn"))
		}
		runPreprocess(src, in, incDirs, outFlag)
	default:
		runAssemble(src, in, incDirs, flattenFlag, originStr, layoutFile,
			stripComments, thinComments, stripData, emitTBN, outFlag, dumpUsage,
			assemble.ErrataOptions{Fix835769: fix835769, Fix843419: fix843419})
	}
}

// isTBN reports whether buf is a serialized .tbn (begins with the magic).
func isTBN(buf []byte) bool {
	return len(buf) >= len(format.Magic) && bytes.Equal(buf[:len(format.Magic)], format.Magic[:])
}

// runAssemble turns SRC (source text or a .tbn) into a binary, optionally
// also emitting the compact overlay .tbn. When the input is source the
// symbolic record IR lives only in memory — the front-end hands the
// *format.File straight to pass-1/pass-2, never serializing it (i48
// decision A). Only an existing on-disk `.tbn` (overlay format) is decoded
// via format.ReadFile.
func runAssemble(src []byte, path string, incDirs includeDirsFlag,
	flatten bool, originStr, layoutFile string, stripComments bool, thinComments int, stripData bool,
	emitTBN, outFlag string, dumpUsage bool, errata assemble.ErrataOptions) {

	tbnIsInput := isTBN(src)
	var f *format.File
	if tbnIsInput {
		if emitTBN != "" {
			fail(fmt.Errorf("--emit-tbn ignored: input %q is already a .tbn", path))
		}
		var err error
		f, err = format.ReadFile(src)
		if err != nil {
			fail(err)
		}
	} else {
		var err error
		if flatten {
			origin, perr := parseInt64(originStr)
			if perr != nil {
				fail(fmt.Errorf("bad -origin %q: %v", originStr, perr))
			}
			var layout []frontend.SectionLayout
			if layoutFile != "" {
				layout, err = frontend.LoadLayoutFile(layoutFile)
				if err != nil {
					fail(err)
				}
			}
			f, err = frontend.TranslateAndFlatten(src, path,
				frontend.PreprocessOptions{IncludeDirs: incDirs},
				frontend.FlattenOptions{OriginVMA: origin, Layout: layout})
		} else {
			f, err = frontend.TranslateWithOptions(src, path,
				frontend.PreprocessOptions{IncludeDirs: incDirs})
		}
		if err != nil {
			fail(err)
		}
		if stripComments {
			f.Records = frontend.StripCommentRecords(f.Records)
		} else if thinComments > 1 {
			f.Records = frontend.ThinComments(f.Records, thinComments)
		}
		if stripData {
			f.Records = frontend.StripDataRecords(f.Records)
		}
	}

	if dumpUsage {
		assemble.EnableUsage()
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		fail(err)
	}
	if emitTBN != "" {
		b, err := assemble.CompactTBNBytes(f, p1)
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(emitTBN, b, 0644); err != nil {
			fail(err)
		}
	}
	out, err := assemble.Pass2WithErrata(f, p1, errata)
	if err != nil {
		fail(err)
	}
	if dumpUsage {
		assemble.DumpUsage(os.Stderr, len(out))
	}
	writeOut(outFlag, out)
}

// runRender renders a .tbn back to canonical text.
func runRender(tbn []byte, outFlag string) {
	out, err := render.Emit(tbn)
	if err != nil {
		fail(err)
	}
	writeOut(outFlag, out)
}

// runPreprocess emits expanded source (includes/macros/.if resolved).
func runPreprocess(src []byte, path string, incDirs includeDirsFlag, outFlag string) {
	expanded, err := frontend.Preprocess(src, path,
		frontend.PreprocessOptions{IncludeDirs: incDirs})
	if err != nil {
		fail(err)
	}
	writeOut(outFlag, expanded)
}

// writeOut writes data to outFlag, or stdout when outFlag is empty.
func writeOut(outFlag string, data []byte) {
	if outFlag == "" {
		if _, err := os.Stdout.Write(data); err != nil {
			fail(err)
		}
		return
	}
	if err := os.WriteFile(outFlag, data, 0644); err != nil {
		fail(err)
	}
}

func usageErr() {
	fmt.Fprintln(os.Stderr,
		"usage:\n"+
			"  sam-aarch64 SRC.s -o OUT.img [--emit-tbn OUT.tbn] [-I dir]... [-flatten [-origin N]] [-strip-comments] [-strip-data] [--dump-usage]\n"+
			"  sam-aarch64 IN.tbn -o OUT.img [--dump-usage]\n"+
			"  sam-aarch64 --render IN.tbn [-o OUT.s]\n"+
			"  sam-aarch64 -E SRC.s [-I dir]... [-o OUT.s]")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
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
