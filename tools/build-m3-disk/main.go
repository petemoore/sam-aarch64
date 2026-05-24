// build-m3-disk constructs the M3 round-trip disk image.
//
// Layout (per docs/specs/2026-05-24-m3-z80-emitter-design.md §2.2):
//
//	0  samdos2    T4S1..T5S10  (20 sectors; ROM BOOT reads T4S1 raw)
//	1  auto       T6S1..T6S2   (BASIC AUTO: CLEAR + LOAD "assembler" + CALL)
//	2  assembler  T6S3         (the M3 Z80 assembler binary)
//	3  enctab.enc T6S4         (encoder table produced by enctab-gen)
//	4  p13        (after)      (paged_call self-test payload, if -p13)
//	5  IN         (after)      (the .tbn source file, if provided)
//
// The AUTO BASIC references "assembler" (not "stub" as in M0).
//
// Usage:
//
//	build-m3-disk [-p13 <p13.bin>] <assembler.bin> <enctab.enc> [<in.tbn>] <output.mgt>
//
// The -p13 flag is optional; when present, the named file is added as a
// CODE file called "p13" on the disk image.  Consumed at boot by the
// BUILD_TESTS-only load_page13_payload (src/m3/loader.asm) for the
// paged_call self-test in src/m3/test_paged_call.asm.  Per
// docs/notes/2026-05-28-paged-call-architecture.md plan-PR 1.
//
// Three positional args (without optional -p13 / in.tbn): no IN file is
// added — used by Task-3 boot tests where the assembler exits before
// reading IN.
//
// Four positional args: adds IN as a CODE file at load address &B000
// (matches IN_BUF in src/m3/main_loop.asm).
package main

import (
	"flag"
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

	p13Path := flag.String("p13", "", "optional: path to the paged_call self-test page-13 payload binary; "+
		"when set, added as the CODE file 'p13' on the disk image")
	flag.Parse()
	args := flag.Args()

	var assemblerPath, enctabPath, inPath, outputPath string
	switch len(args) {
	case 3:
		assemblerPath = args[0]
		enctabPath = args[1]
		outputPath = args[2]
	case 4:
		assemblerPath = args[0]
		enctabPath = args[1]
		inPath = args[2]
		outputPath = args[3]
	default:
		log.Fatalf("usage: %s [-p13 <p13.bin>] <assembler.bin> <enctab.enc> [<in.tbn>] <output.mgt>", os.Args[0])
	}

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
	//
	// Pad the body so the FILE occupies an integer number of full tracks
	// from the point where it starts (T6S3).  This guarantees the next
	// file (enctab.enc) starts at the beginning of a new track, which in
	// turn guarantees no track-step occurs during enctab.enc's HLOAD —
	// a prerequisite to keep the HLOAD destination invariant tight (see
	// loader.asm header comment for the ctas wrap rule).
	//
	// We size the assembler slot to occupy ALL OF TRACK 6 from S3..S10
	// (8 sectors) PLUS the next full track.  M3 Task 16+ added several
	// new source files (reader/encoder/expr_eval/ml/form_lookup/main_loop)
	// growing assembler.bin past the 4 KB old budget; we now budget two
	// tracks = 20 sectors = 10180 bytes.
	const SectorUseful = 510
	const FileHeaderSize = 9
	const AssemblerSectors = 40 // T6S3..T6S10 (8) + T7..T9 full (30) +
	//                              T10S1..T10S2 (2) = 40 sectors = 4 "full
	//                              tracks" from T6S3.  Bumped from 30
	//                              in M5 PR-E to fit the test variant
	//                              (15291 → 20391 byte budget) after
	//                              the OpLitPool encoder + .ltorg flush
	//                              + Layer-1 tests landed.
	targetBodyLen := AssemblerSectors*SectorUseful - FileHeaderSize
	if len(assemblerBin) > targetBodyLen {
		log.Fatalf("assembler.bin (%d bytes) exceeds the %d-byte budget for %d sectors",
			len(assemblerBin), targetBodyLen, AssemblerSectors)
	}
	padded := make([]byte, targetBodyLen)
	copy(padded, assemblerBin)
	if err := disk.AddCodeFile("assembler", padded, LoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(assembler): %v", err)
	}

	// Slot 3: enctab.enc CODE file. Loaded at startup by src/m3/loader.asm
	// via SAMDOS HGTHD + trampoline_hload into physical page 4 (outside
	// section C — see src/m3/trampoline.asm and docs/specs/2026-05-27-
	// samdos-load-idiom.md for the trampoline pattern that makes this
	// possible).  The recorded load address here is documentary only:
	// HGTHD reads it into DIFA at runtime but our loader.asm supplies
	// its own HL (= &8000) and HMPR-target-page (= 4) values when
	// calling the trampoline, so the recorded value is never honoured
	// directly.  We record &8000 (the trampoline's section-C window)
	// to make the on-disk catalogue match the runtime HL value.
	// executionAddress=0 means no auto-exec.
	const EnctabLoadAddress uint32 = 0x8000
	if err := disk.AddCodeFile("enctab.enc", enctabData, EnctabLoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(enctab.enc): %v", err)
	}

	// Slot 4 (optional): p13 — paged_call self-test page-13 payload.
	// Loaded at boot by src/m3/loader.asm::load_page13_payload into
	// physical page 13 via the HLOAD trampoline.  Consumed by
	// run_paged_call_self_tests in src/m3/test_paged_call.asm.  See
	// docs/notes/2026-05-28-paged-call-architecture.md plan-PR 1.
	//
	// load_page13_payload is BUILD_TESTS-only, so production builds
	// (`-p13` flag absent) skip this file.
	if *p13Path != "" {
		p13Data, err := os.ReadFile(*p13Path)
		if err != nil {
			log.Fatalf("read p13: %v", err)
		}
		// LoadAddress &8000 mirrors enctab.enc: the on-disk address
		// is documentary; the loader supplies its own HMPR-target-page
		// (= 13) when calling the trampoline.
		const P13LoadAddress uint32 = 0x8000
		if err := disk.AddCodeFile("p13", p13Data, P13LoadAddress, 0); err != nil {
			log.Fatalf("AddCodeFile(p13): %v", err)
		}
	}

	// Slot 5 (optional): IN .tbn file.  Loaded at runtime by
	// src/m3/main_loop.asm::load_in_file via HGTHD+HLOAD into IN_BUF at
	// &B000.  The on-disk LOAD address is purely documentary — HLOAD's
	// caller specifies HL = &B000 — but recording a consistent value
	// makes the on-disk catalogue readable.
	if inPath != "" {
		inData, err := os.ReadFile(inPath)
		if err != nil {
			log.Fatalf("read IN: %v", err)
		}
		const InLoadAddress uint32 = 0xB000
		if err := disk.AddCodeFile("IN", inData, InLoadAddress, 0); err != nil {
			log.Fatalf("AddCodeFile(IN): %v", err)
		}
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
	if *p13Path != "" {
		p13Size, _ := os.Stat(*p13Path)
		fmt.Printf("p13:        %d bytes\n", p13Size.Size())
	}
	if inPath != "" {
		inSize, _ := os.Stat(inPath)
		fmt.Printf("IN:         %d bytes\n", inSize.Size())
	}
	fmt.Printf("Built %s\n", outputPath)
}
