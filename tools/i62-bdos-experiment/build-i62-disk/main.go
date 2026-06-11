// build-i62-disk constructs a bootable .mgt for the i62 B-DOS
// storage-backend experiment: a DOS boot file (plain SAMDOS 2 or B-DOS
// AL 1.5a), an auto-RUN BASIC loader, and the i62test probe binary.
//
// Layout mirrors tools/build-disk (the CI-proven boot recipe):
//
//	0  <dos>     T4S1...      ROM BOOT reads T4S1 raw; first file = DOS
//	1  auto      (after)      BASIC AUTO: CLEAR + LOAD "i62test" + CALL
//	2  i62test   (after)      the probe binary (org &8000)
//
// The DOS file's directory-entry start address is replicated from the
// source disk it was extracted from (samdos2: page 29 + offset &8009 =
// 491529; AL-BDOS15a on the worldofsam AL disks: page 1 + offset &8009
// = 32777), including the 0x60 unused-bits pattern both originals
// carry in the start-page byte.
//
// Usage:
//
//	build-i62-disk [-dos-name NAME] [-dos-load ADDR] <dos.bin> <i62test.bin> <out.mgt>
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/petemoore/samfile/v3"
	"github.com/petemoore/samfile/v3/sambasic"
)

// LoadAddress is where the auto BASIC loads and CALLs the probe.
const LoadAddress uint32 = 0x8000

func main() {
	log.SetFlags(0)
	log.SetPrefix("build-i62-disk: ")

	dosName := flag.String("dos-name", "samdos2", "directory-entry name for the DOS boot file")
	dosLoad := flag.Uint("dos-load", 491529, "directory-entry start address for the DOS boot file (samdos2: 491529; AL-BDOS15a: 32777)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [-dos-name NAME] [-dos-load ADDR] <dos.bin> <i62test.bin> <out.mgt>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) != 3 {
		flag.Usage()
		os.Exit(2)
	}
	dosPath, testPath, outputPath := args[0], args[1], args[2]

	dosBin, err := os.ReadFile(dosPath)
	if err != nil {
		log.Fatalf("read dos: %v", err)
	}
	testBin, err := os.ReadFile(testPath)
	if err != nil {
		log.Fatalf("read i62test: %v", err)
	}

	disk := samfile.NewDiskImage()

	// Slot 0: the DOS. ROM BOOT reads T4S1 raw; same layout as
	// tools/build-disk uses for samdos2.
	if err := disk.AddCodeFile(*dosName, dosBin, uint32(*dosLoad), 0); err != nil {
		log.Fatalf("AddCodeFile(%s): %v", *dosName, err)
	}
	if err := disk.SetStartAddressPageUnusedBits(*dosName, 3); err != nil {
		log.Fatalf("SetStartAddressPageUnusedBits(%s): %v", *dosName, err)
	}

	// Slot 1: AUTO BASIC (StartLine=10 marks auto-RUN). B-DOS's HAUTO
	// loads "AUTO*" files exactly like SAMDOS does at boot.
	auto := &sambasic.File{
		StartLine: 10,
		Lines: []sambasic.Line{
			{Number: 10, Tokens: []sambasic.Token{
				sambasic.CLEAR,
				sambasic.Number(uint16(LoadAddress - 1)),
			}},
			{Number: 20, Tokens: []sambasic.Token{
				sambasic.LOAD,
				sambasic.String(`"i62test"`),
				sambasic.CODE,
				sambasic.Number(uint16(LoadAddress)),
			}},
			{Number: 30, Tokens: []sambasic.Token{
				sambasic.CALL,
				sambasic.Number(uint16(LoadAddress)),
			}},
		},
	}
	if err := disk.AddBasicFile("auto", auto); err != nil {
		log.Fatalf("AddBasicFile(auto): %v", err)
	}

	// Slot 2: the probe binary.
	if err := disk.AddCodeFile("i62test", testBin, LoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(i62test): %v", err)
	}

	if err := disk.Save(outputPath); err != nil {
		log.Fatalf("write %s: %v", outputPath, err)
	}
	log.Printf("built %s (dos=%s %d bytes, i62test %d bytes)", outputPath, *dosName, len(dosBin), len(testBin))
}
