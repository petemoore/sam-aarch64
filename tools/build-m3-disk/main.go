// build-m3-disk constructs the M3 round-trip disk image.
//
// Layout (per docs/specs/2026-05-24-m3-z80-emitter-design.md §2.2):
//
//	0  samdos2    T4S1..T5S10  (20 sectors; ROM BOOT reads T4S1 raw)
//	1  auto       T6S1..T6S2   (BASIC AUTO: CLEAR + LOAD "assembler" + CALL)
//	2  assembler  T6S3         (the M3 Z80 assembler binary)
//	3  enctab.enc T6S4         (encoder table produced by enctab-gen)
//
// The AUTO BASIC references "assembler" (not "stub" as in M0).
//
// Usage:
//
//	build-m3-disk <assembler.bin> <enctab.enc> <output.mgt>
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/petemoore/samfile/v3"
	"github.com/petemoore/samfile/v3/sambasic"
)

const (
	// LoadAddress is the SAM address the assembler loads to.
	// The AUTO BASIC does `CLEAR&7FFF: LOAD "assembler" CODE 32768: CALL 32768`.
	// This matches src/m3/assembler.asm's `org &8000`.
	LoadAddress uint32 = 0x8000

	// SamdosLoadAddress is the address recorded in the samdos2 body header.
	// Same as the M0 build-disk; citation: tools/build-disk/main.go.
	SamdosLoadAddress uint32 = 491529
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("build-m3-disk: ")

	if len(os.Args) != 4 {
		log.Fatalf("usage: %s <assembler.bin> <enctab.enc> <output.mgt>", os.Args[0])
	}
	assemblerPath := os.Args[1]
	enctabPath := os.Args[2]
	outputPath := os.Args[3]

	samdos2, err := os.ReadFile("reference/samdos/samdos2.bin")
	if err != nil {
		log.Fatalf("read samdos2: %v", err)
	}
	if len(samdos2) != 10000 {
		log.Fatalf("samdos2: expected 10000 bytes, got %d", len(samdos2))
	}

	assemblerBin, err := os.ReadFile(assemblerPath)
	if err != nil {
		log.Fatalf("read assembler: %v", err)
	}

	enctabData, err := os.ReadFile(enctabPath)
	if err != nil {
		log.Fatalf("read enctab: %v", err)
	}

	disk := samfile.NewDiskImage()

	// Slot 0: samdos2. ROM BOOT reads T4S1 raw; same layout as M0.
	if err := disk.AddCodeFile("samdos2", samdos2, SamdosLoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(samdos2): %v", err)
	}
	if err := disk.SetStartAddressPageUnusedBits("samdos2", 3); err != nil {
		log.Fatalf("SetStartAddressPageUnusedBits(samdos2): %v", err)
	}

	// Slot 1: AUTO BASIC.
	// StartLine=10 marks the entry as auto-RUN (SAM ROM checks dir byte 0xF2=0
	// to dispatch BASIC start-line auto-RUN; citation: tools/build-disk/main.go).
	auto := &sambasic.File{
		StartLine: 10,
		Lines: []sambasic.Line{
			{Number: 10, Tokens: []sambasic.Token{
				sambasic.CLEAR,
				sambasic.Number(uint16(LoadAddress - 1)),
			}},
			{Number: 20, Tokens: []sambasic.Token{
				sambasic.LOAD,
				sambasic.String(`"assembler"`),
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

	// Slot 2: assembler CODE file (M3 Z80 binary).
	if err := disk.AddCodeFile("assembler", assemblerBin, LoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(assembler): %v", err)
	}

	// Slot 3: enctab.enc CODE file. Loaded at startup by src/m3/loader.asm
	// via SAMDOS HLOAD. Load address matches ENCTAB_BUF in loader.asm (&9000),
	// which sits inside section C (&8000-&BFFF) — the address range the
	// SAMDOS auto-wrap-fix in ctas enforces for HLOAD destinations.
	// executionAddress=0 means no auto-exec.
	const EnctabLoadAddress uint32 = 0x9000
	if err := disk.AddCodeFile("enctab.enc", enctabData, EnctabLoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(enctab.enc): %v", err)
	}

	if err := disk.Save(outputPath); err != nil {
		log.Fatalf("save %s: %v", outputPath, err)
	}

	fmt.Printf("samdos2:    %d bytes  T4S1-T5S10\n", len(samdos2))
	fmt.Printf("auto:       %d bytes   T6S1-T6S2  (PROG=%d, +VARS=%d, +GAP=%d)\n",
		len(auto.Bytes()), auto.NVARSOffset(),
		auto.NUMENDOffset()-auto.NVARSOffset(),
		auto.SAVARSOffset()-auto.NUMENDOffset())
	fmt.Printf("assembler:  %d bytes     T6S3\n", len(assemblerBin))
	fmt.Printf("enctab.enc: %d bytes     T6S4\n", len(enctabData))
	fmt.Printf("Built %s\n", outputPath)
}
