// llist-capture builds a test disk that, when booted in SimCoupé,
// runs the SAM ROM's LLIST routine on a named BASIC file from a
// source corpus disk and writes the result through SimCoupé's
// parallel-port-to-file mechanism. The output is the canonical
// LIST rendering of the program — exactly what the SAM ROM would
// produce — providing ground truth for samfile basic-to-text.
//
// This binary is a thin CLI wrapper around package builder; in-process
// Go consumers (llist-sweep) call builder.BuildTestDisk directly.
//
// Usage:
//
//	llist-capture -source <disk.mgt> -file <name> -output <test.mgt>
//
// On success, prints `injected-line: <N>` to stdout (followed by a
// human-readable summary). Shell consumers parse the first line to
// learn which BASIC line was added — the same line must be filtered
// from the captured LLIST output before comparison.
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/petemoore/sam-aarch64/tools/llist-capture/builder"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("llist-capture: ")

	var (
		sourcePath = flag.String("source", "", "source disk (.mgt) containing the BASIC file to capture")
		fileName   = flag.String("file", "", "name of BASIC file in source disk")
		outputPath = flag.String("output", "", "output path for the constructed test disk")
		samdosPath = flag.String("samdos", "reference/samdos/samdos2.bin", "path to samdos2.bin")
	)
	flag.Parse()
	if *sourcePath == "" || *fileName == "" || *outputPath == "" {
		log.Fatalf("usage: llist-capture -source <disk> -file <name> -output <test.mgt>")
	}

	res, err := builder.BuildTestDisk(*sourcePath, *fileName, *outputPath, *samdosPath)
	if err != nil {
		log.Fatalf("%v", err)
	}

	fmt.Printf("injected-line: %d\n", res.InjectedLine)
	fmt.Printf("samdos2: %d bytes\n", res.SamdosBytes)
	fmt.Printf("AUTO:    %d bytes (auto-run line %d)\n", res.AutoBytes, res.InjectedLine)
	fmt.Printf("Built %s\n", *outputPath)
}
