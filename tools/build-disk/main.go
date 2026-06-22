// build-disk constructs the round-trip disk image.
//
// Layout (per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m3-z80-emitter-design.md §2.2):
//
//	0  <dos>      T4S1..T5S10  (20 sectors; ROM BOOT reads T4S1 raw;
//	                            B-DOS AL 1.5a by default, -dos swaps in a
//	                            hook-compatible image such as SAMDOS 2)
//	1  auto       T6S1..T6S2   (BASIC AUTO: CLEAR + LOAD "assembler" + CALL)
//	2  assembler  T6S3         (the M3 Z80 assembler binary)
//	3  enctab.enc T6S4         (encoder table produced by tables-gen)
//	4  IN         (after)      (the .tbn source file, if provided)
//	5  test_mem   (after)      (off-axis test_mem.bin, if provided —
//	                            plan-PR 3 of the paging architecture)
//	6  p14        (after)      (paged_call self-test payload, if
//	                            provided — plan-PR 1 of the paging
//	                            architecture)
//	7  sd13       (after)      (page-13 sysreg lookup data, if provided —
//	                            PR-2; deposited for BOTH variants since
//	                            sysreg lookups are a production feature)
//	8  d15        (after)      (page-15 disassembler binary, if provided —
//	                            strand-B PR-3; deposited for BOTH variants;
//	                            needed by editor at runtime)
//	9  zx013      (after)      (page-13 zx0 compressor+decoder payload at
//	                            &8400, if provided — i68; deposited for
//	                            BOTH variants; needed by the editor's
//	                            comment-block compress/decode paths)
//
// The AUTO BASIC references "assembler" (not "stub" as in M0).
//
// Usage:
//
//	build-disk [-dos <path>] [-dos-name <name>] [-dos-load <addr>] \
//	    [-test-mem <path>] [-paged-call <path>] \
//	    <assembler.bin> <enctab.enc> [<in.tbn>] <output.mgt>
//
// -dos / -dos-name / -dos-load select the boot DOS (default B-DOS AL 1.5a
// at reference/bdos/al-bdos15a.bin, recorded name "bdos", load 32777).
// SAMDOS 2 implements the same RST-8 hook interface, so it boots the same
// recipe — pass -dos reference/samdos/samdos2.bin -dos-name samdos2
// -dos-load 491529 for a SAMDOS 2 compatibility build. B-DOS became the
// shipped/CI boot DOS in i75 (q10 resolution); the swap is hook-portable
// (verified i62) and floppy-capable with no mass storage attached
// (the no-Atom-Lite corner proven in i75).
//
// Three-positional form (legacy): no IN file is added — used by
// Task-3 boot tests where the assembler exits before reading IN.
//
// Four-positional form: adds IN as a CODE file at load address &B000
// (matches IN_BUF in src/main_loop.asm).
//
// -test-mem <path>: deposits an off-axis test payload (test_mem.bin)
// as a CODE file named "test_mem".  Required for the BUILD_TESTS
// variant of the assembler from plan-PR 3 onwards (the test binary
// HLOADs this file into physical page 13 at boot via
// load_test_mem_off_axis).  Production builds omit this file.
//
// -paged-call <path>: deposits the paged_call boot self-test target
// payload as a CODE file named "p14".  Required for the BUILD_TESTS
// variant from plan-PR 1 onwards (the test binary HLOADs this file
// into physical page 14 at boot via load_page14_payload, then
// invokes run_paged_call_self_tests against it).  Production builds
// omit this file.
//
// The two BUILD_TESTS-only flags (-test-mem, -paged-call) are
// intentionally kept as bespoke per-page flags rather than
// generalised to `-page N FILE`: there are only two callers at
// present, and the per-page Go-side bookkeeping (UIFA name +
// load_*_payload routine) is per-page anyway in the loader.asm
// pairings.  If a third use case appears we can refactor; until
// then the bespoke form keeps the call-site obvious.
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
	// This matches src/assembler.asm's `org &8000`.
	LoadAddress uint32 = 0x8000

	// SamdosPath is the SAMDOS 2 reference binary. Passing -dos with this
	// exact path selects the compatibility build and triggers the byte-exact
	// size guard below.
	SamdosPath = "reference/samdos/samdos2.bin"

	// SamdosLoadAddress is the directory-entry start address recorded in the
	// samdos2 body header (page 29 + offset &8009). Retained for the SAMDOS 2
	// compatibility path (-dos reference/samdos/samdos2.bin -dos-load 491529).
	// Inherited from the original M0 build-disk tool (since deleted; see
	// git history for tools/build-disk/main.go).
	SamdosLoadAddress uint32 = 491529

	// DefaultDosPath / DefaultDosName / DefaultDosLoad are the shipped boot
	// DOS: B-DOS AL 1.5a (Edwin Blink's freeware "Improved SAMDOS"), at the
	// directory-entry start address 32777 (page 1 + offset &8009) the
	// worldofsam AL disks record — the same convention the i62 rig verified
	// (docs/notes/bdos-version-landscape.md §"Empirical verification"). B-DOS
	// implements the same RST-8 hook interface as SAMDOS 2 (verified i62,
	// static-verified for the Trinity 1.5t fork in i71), is floppy-capable
	// with no mass storage attached (the no-Atom-Lite corner proven in i75),
	// and is licence-clean to ship. The SAMDOS 2 path stays fully functional
	// via -dos/-dos-name/-dos-load (reference/samdos/samdos2.bin, "samdos2",
	// 491529). See docs/specs/samdos-file-io.md.
	DefaultDosPath = "reference/bdos/al-bdos15a.bin"
	DefaultDosName = "bdos"
	DefaultDosLoad = BdosLoadAddress

	// BdosLoadAddress is the directory-entry start address for B-DOS AL 1.5a:
	// page 1 + offset &8009 = 32777, the value the worldofsam AL disks carry
	// (and the 0x60 start-page unused-bits pattern, set below via
	// SetStartAddressPageUnusedBits). Verified by the i62 rig.
	BdosLoadAddress uint32 = 32777

	// SamdosExactSize is the byte-exact length of the shipped samdos2.bin.
	// The hard equality check applies only when -dos is the SAMDOS 2
	// reference file — for any other DOS image (the B-DOS default included) a
	// generous boot-region sanity bound is used instead.
	SamdosExactSize = 10000

	// DosMaxSize is the sanity ceiling for a DOS image that is not the
	// byte-exact samdos2.bin reference: well above any real SAM DOS (B-DOS
	// AL 1.5a is 10701 B) yet small enough to catch a wrong file passed by
	// mistake. samfile.AddCodeFile still does the authoritative free-sector
	// check.
	DosMaxSize = 16384
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("build-disk: ")

	dosPath := flag.String("dos", DefaultDosPath, "path to the boot DOS binary (default B-DOS AL 1.5a; SAMDOS 2 is hook-compatible: reference/samdos/samdos2.bin)")
	dosName := flag.String("dos-name", DefaultDosName, "directory-entry name for the boot DOS file")
	dosLoad := flag.Uint("dos-load", uint(DefaultDosLoad), "directory-entry start address for the boot DOS file (samdos2: 491529; B-DOS AL 1.5a: 32777)")
	testMemPath := flag.String("test-mem", "", "path to off-axis test_mem.bin (BUILD_TESTS only; plan-PR 3)")
	pagedCallPath := flag.String("paged-call", "", "path to the paged_call self-test page-14 payload (BUILD_TESTS only; plan-PR 1)")
	clusterPath := flag.String("cluster", "", "path to the off-axis page-12 M5+misc encoder self-test cluster (build/test_cluster.bin; BUILD_TESTS only; M6 budget-relief)")
	encFixPath := flag.String("enc-fix", "", "path to the off-axis page-11 encode_inst fixture data payload (build/enc_fix_payload.bin; BUILD_TESTS only; i69)")
	sysregDataPath := flag.String("sysreg-data", "", "path to the page-13 sysreg lookup data (build/sysreg_data.bin; PRODUCTION + test; PR-2)")
	disasmPath := flag.String("disasm", "", "path to the page-15 disassembler binary (build/disasm.bin; PRODUCTION + test; strand-B PR-3)")
	zx0Path := flag.String("zx0", "", "path to the page-13 zx0 compressor+decoder payload (build/zx0.bin; PRODUCTION + test; i68)")
	netbootPath := flag.String("netboot", "", "path to a standalone netboot CODE binary (org &8000); builds a minimal bootable disk (DOS + AUTO that LOADs+CALLs it) and ignores the assembler positional args")
	netbootName := flag.String("netboot-name", "netboot", "directory-entry name for the -netboot CODE file (the AUTO BASIC LOADs this name)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr,
			"usage: %s [-dos <path>] [-dos-name <name>] [-dos-load <addr>] [-test-mem <path>] [-paged-call <path>] [-cluster <path>] [-enc-fix <path>] [-sysreg-data <path>] [-disasm <path>] [-zx0 <path>] <assembler.bin> <enctab.enc> [<in.tbn>] <output.mgt>\n   or: %s -netboot <code.bin> [-netboot-name <name>] [-dos ...] <output.mgt>\n",
			os.Args[0], os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()

	// -netboot mode: a minimal bootable disk that auto-runs a single standalone
	// CODE binary (the Phase-3 netboot programs — the smoke test, later the
	// server/client). Same boot mechanism as the assembler disk (ROM BOOT reads
	// the DOS at T4S1; the AUTO BASIC CLEARs, LOADs the CODE file, and CALLs it),
	// but with one arbitrary CODE file in place of the assembler + enctab + tests.
	if *netbootPath != "" {
		if len(args) != 1 {
			flag.Usage()
			os.Exit(2)
		}
		if err := buildNetbootDisk(*dosPath, *dosName, uint32(*dosLoad), *netbootPath, *netbootName, args[0]); err != nil {
			log.Fatal(err)
		}
		return
	}

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
		flag.Usage()
		os.Exit(2)
	}

	dosBin, err := os.ReadFile(*dosPath)
	if err != nil {
		log.Fatalf("read dos: %v", err)
	}
	if *dosPath == SamdosPath {
		// The shipped samdos2.bin is byte-exact; a wrong size means a
		// corrupt or wrong reference file.
		if len(dosBin) != SamdosExactSize {
			log.Fatalf("samdos2: expected %d bytes, got %d", SamdosExactSize, len(dosBin))
		}
	} else if len(dosBin) > DosMaxSize {
		// Any other DOS image (the B-DOS default included): a generous
		// boot-region sanity bound (samfile.AddCodeFile does the
		// authoritative free-sector check).
		log.Fatalf("dos %s: %d bytes exceeds the %d-byte boot-region sanity bound", *dosPath, len(dosBin), DosMaxSize)
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

	// Slot 0: the boot DOS. ROM BOOT reads T4S1 raw; same layout as M0.
	// B-DOS AL 1.5a by default; -dos swaps in a hook-compatible image (SAMDOS 2).
	if err := disk.AddCodeFile(*dosName, dosBin, uint32(*dosLoad), 0); err != nil {
		log.Fatalf("AddCodeFile(%s): %v", *dosName, err)
	}
	if err := disk.SetStartAddressPageUnusedBits(*dosName, 3); err != nil {
		log.Fatalf("SetStartAddressPageUnusedBits(%s): %v", *dosName, err)
	}

	// Slot 1: AUTO BASIC.
	// StartLine=10 marks the entry as auto-RUN (SAM ROM checks dir byte 0xF2=0
	// to dispatch BASIC start-line auto-RUN; inherited from the original M0
	// build-disk tool, since deleted — see git history).
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

	// Slot 3: enctab.enc CODE file. Loaded at startup by src/loader.asm
	// via SAMDOS HGTHD + trampoline_hload into physical page 4 (outside
	// section C — see src/trampoline.asm and docs/specs/samdos-file-io.md
	// for the trampoline pattern that makes this
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

	// Slot 4 (optional): IN .tbn file.  Loaded at runtime by
	// src/main_loop.asm::load_in_file via HGTHD+HLOAD into IN_BUF at
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

	// Slot 5 (optional): off-axis test_mem.bin.  Loaded at runtime by
	// src/loader.asm::load_test_mem_off_axis via HGTHD+trampoline
	// into physical page 13 (section A at &0000 when LMPR = LMPR_TEST_MEM).
	// BUILD_TESTS variant only; production builds omit -test-mem.
	// The on-disk LOAD address is documentary (the trampoline supplies
	// its own HL = &8000 and target page = 13); we record &8000 because
	// (a) samfile's AddCodeFile rejects addresses below 16384 as ROM,
	// (b) &8000 is the section-C window the HLOAD trampoline actually
	// uses.  Citation: tools/samfile-equivalent guard from upstream.
	if *testMemPath != "" {
		testMemData, err := os.ReadFile(*testMemPath)
		if err != nil {
			log.Fatalf("read test_mem: %v", err)
		}
		const TestMemLoadAddress uint32 = 0x8000
		if err := disk.AddCodeFile("test_mem", testMemData, TestMemLoadAddress, 0); err != nil {
			log.Fatalf("AddCodeFile(test_mem): %v", err)
		}
	}

	// Slot 5b (optional): off-axis "M5 + misc encoder" cluster (cluster).
	// Loaded at boot by src/loader.asm::load_offaxis_cluster via
	// HGTHD+trampoline into physical page 12 (section A at &0000 when
	// LMPR = LMPR_TEST_CLUSTER), then invoked via one LMPR swap
	// (cluster_dispatch).  Holds the relocated pc_rel / directives /
	// ror_imm / shifted_reg / extended_reg / litpool self-test suites.
	// BUILD_TESTS variant only; production builds omit -cluster.  M6
	// budget-relief PR (2026-05-29) — mirrors the test_mem off-axis
	// pattern.  Recorded load address is documentary (the trampoline
	// supplies HL = &8000 and target page = 12).
	if *clusterPath != "" {
		clusterData, err := os.ReadFile(*clusterPath)
		if err != nil {
			log.Fatalf("read cluster: %v", err)
		}
		const ClusterLoadAddress uint32 = 0x8000
		if err := disk.AddCodeFile("cluster", clusterData, ClusterLoadAddress, 0); err != nil {
			log.Fatalf("AddCodeFile(cluster): %v", err)
		}
	}

	// Slot 5c (optional): off-axis encode_inst fixture data payload (enc_fix).
	// Loaded at boot by src/loader.asm::load_enc_fix_payload via
	// HGTHD+trampoline into physical page 11 (section A at &0000 when
	// LMPR = LMPR_ENC_FIX = &2B).  At the start of run_encode_inst_self_tests
	// the driver bulk-copies the payload into section-D RAM at
	// ENC_FIX_TABLE_RAM (&E100) via LDIR.  Holds enc_fix_table rows +
	// operand streams (~528 B), assembled from src/test_encode_inst_payload.asm
	// with org &E100 so row fixture ptrs are section-D absolute addresses.
	// BUILD_TESTS variant only (i69 lever 3).  Recorded load address
	// is documentary (the trampoline supplies HL = &8000 and target page = 11).
	if *encFixPath != "" {
		encFixData, err := os.ReadFile(*encFixPath)
		if err != nil {
			log.Fatalf("read enc-fix payload: %v", err)
		}
		const EncFixLoadAddress uint32 = 0x8000
		if err := disk.AddCodeFile("enc_fix", encFixData, EncFixLoadAddress, 0); err != nil {
			log.Fatalf("AddCodeFile(enc_fix): %v", err)
		}
	}

	// Slot 6 (optional): paged_call self-test payload (p14).  Loaded at
	// boot by src/loader.asm::load_page14_payload via HGTHD +
	// trampoline into physical page 14, then exercised by
	// run_paged_call_self_tests (src/test_paged_call.asm).
	// BUILD_TESTS variant only; production builds omit -paged-call.
	// Recorded load address mirrors enctab.enc / test_mem: documentary,
	// since the loader supplies HL = &8000 and target page = 14 when
	// calling the trampoline.
	if *pagedCallPath != "" {
		pagedCallData, err := os.ReadFile(*pagedCallPath)
		if err != nil {
			log.Fatalf("read paged-call payload: %v", err)
		}
		const PagedCallLoadAddress uint32 = 0x8000
		if err := disk.AddCodeFile("p14", pagedCallData, PagedCallLoadAddress, 0); err != nil {
			log.Fatalf("AddCodeFile(p14): %v", err)
		}
	}

	// Slot 7 (optional): page-13 sysreg lookup data (sd13).  Loaded at
	// boot by src/loader.asm::load_page13_payload via HGTHD +
	// trampoline into physical page 13, then read by the four
	// sysname_lookup_* routines (src/sysname.asm) via paged_call.
	// PRODUCTION feature — sysreg/dc/tlbi/pstate operands appear in
	// shipping sources, so this file is deposited for BOTH variants
	// (unlike -test-mem / -paged-call which are BUILD_TESTS only).
	// Recorded load address mirrors enctab.enc: documentary, since the
	// loader supplies HL = &8000 and target page = 13 to the trampoline.
	if *sysregDataPath != "" {
		sysregData, err := os.ReadFile(*sysregDataPath)
		if err != nil {
			log.Fatalf("read sysreg-data payload: %v", err)
		}
		const SysregDataLoadAddress uint32 = 0x8000
		if err := disk.AddCodeFile("sd13", sysregData, SysregDataLoadAddress, 0); err != nil {
			log.Fatalf("AddCodeFile(sd13): %v", err)
		}
	}

	// Slot 8 (optional): page-15 disassembler binary (d15). Loaded at boot
	// by src/loader.asm::load_page15_payload via HGTHD + trampoline into
	// physical page 15 (section C at &8000 when HMPR = DISASM_PAGE = 15).
	// Exercised at boot by run_disasm_paged_self_tests (BUILD_TESTS only)
	// and at runtime by the on-SAM editor via paged_call. PRODUCTION feature
	// — deposited for BOTH variants. Recorded load address is documentary
	// (the trampoline supplies HL = &8000 and target page = 15).
	if *disasmPath != "" {
		disasmData, err := os.ReadFile(*disasmPath)
		if err != nil {
			log.Fatalf("read disasm payload: %v", err)
		}
		const DisasmLoadAddress uint32 = 0x8000
		if err := disk.AddCodeFile("d15", disasmData, DisasmLoadAddress, 0); err != nil {
			log.Fatalf("AddCodeFile(d15): %v", err)
		}
	}

	// Slot 9 (optional): page-13 zx0 payload (zx013). Loaded at boot by
	// src/loader.asm::load_zx0_payload via HGTHD + trampoline into
	// physical page 13 at &8400 — alongside the sd13 sysreg data at
	// &8000 (the two co-reside at disjoint offsets; see
	// src/zx0_comm.inc). Holds the greedy ZX0 compressor (&8400) + the
	// turbo decoder (&8B00) per docs/specs/comment-storage-design.md §5.
	// PRODUCTION feature — deposited for BOTH variants (the editor's
	// compress-at-save / decode-at-read paths need it); the test disk
	// ships the BUILD_TESTS variant (zx0-test.bin) carrying the &AFA0
	// boot self-test + baked fixture. Recorded load address &8400 is
	// documentary but matches the runtime HL the loader passes.
	if *zx0Path != "" {
		zx0Data, err := os.ReadFile(*zx0Path)
		if err != nil {
			log.Fatalf("read zx0 payload: %v", err)
		}
		const Zx0LoadAddress uint32 = 0x8400
		if err := disk.AddCodeFile("zx013", zx0Data, Zx0LoadAddress, 0); err != nil {
			log.Fatalf("AddCodeFile(zx013): %v", err)
		}
	}

	if err := disk.Save(outputPath); err != nil {
		log.Fatalf("save %s: %v", outputPath, err)
	}

	fmt.Printf("%-12s%d bytes  T4S1-T5S10\n", *dosName+":", len(dosBin))
	fmt.Printf("auto:       %d bytes   T6S1-T6S2  (PROG=%d, +VARS=%d, +GAP=%d)\n",
		len(auto.Bytes()), auto.NVARSOffset(),
		auto.NUMENDOffset()-auto.NVARSOffset(),
		auto.SAVARSOffset()-auto.NUMENDOffset())
	fmt.Printf("assembler:  %d bytes     T6S3\n", len(assemblerBin))
	fmt.Printf("enctab.enc: %d bytes     T6S4\n", len(enctabData))
	if inPath != "" {
		inSize, _ := os.Stat(inPath)
		fmt.Printf("IN:         %d bytes\n", inSize.Size())
	}
	if *testMemPath != "" {
		testMemSize, _ := os.Stat(*testMemPath)
		fmt.Printf("test_mem:   %d bytes\n", testMemSize.Size())
	}
	if *clusterPath != "" {
		clusterSize, _ := os.Stat(*clusterPath)
		fmt.Printf("cluster:    %d bytes\n", clusterSize.Size())
	}
	if *pagedCallPath != "" {
		pagedCallSize, _ := os.Stat(*pagedCallPath)
		fmt.Printf("p14:        %d bytes\n", pagedCallSize.Size())
	}
	if *encFixPath != "" {
		encFixSize, _ := os.Stat(*encFixPath)
		fmt.Printf("enc_fix:    %d bytes\n", encFixSize.Size())
	}
	if *sysregDataPath != "" {
		sysregDataSize, _ := os.Stat(*sysregDataPath)
		fmt.Printf("sd13:       %d bytes\n", sysregDataSize.Size())
	}
	if *disasmPath != "" {
		disasmSize, _ := os.Stat(*disasmPath)
		fmt.Printf("d15:        %d bytes\n", disasmSize.Size())
	}
	if *zx0Path != "" {
		zx0Size, _ := os.Stat(*zx0Path)
		fmt.Printf("zx013:      %d bytes\n", zx0Size.Size())
	}
	fmt.Printf("Built %s\n", outputPath)
}

// buildNetbootDisk writes a minimal bootable disk that auto-runs a single
// standalone netboot CODE binary (org &8000). The boot mechanism is identical
// to the assembler disk — ROM BOOT reads the DOS at T4S1, then the AUTO BASIC
// (StartLine 10) does CLEAR &7FFF : LOAD "<name>" CODE 32768 : CALL 32768 — but
// the payload is one arbitrary CODE file instead of the assembler + enctab +
// test cluster. Used by the Phase-3 netboot programs (the i94 bring-up smoke
// test, later the server/client) so Pete can boot one on real Trinity hardware.
func buildNetbootDisk(dosPath, dosName string, dosLoad uint32, codePath, codeName, outputPath string) error {
	dosBin, err := os.ReadFile(dosPath)
	if err != nil {
		return fmt.Errorf("read dos: %w", err)
	}
	if dosPath == SamdosPath {
		if len(dosBin) != SamdosExactSize {
			return fmt.Errorf("samdos2: expected %d bytes, got %d", SamdosExactSize, len(dosBin))
		}
	} else if len(dosBin) > DosMaxSize {
		return fmt.Errorf("dos %s: %d bytes exceeds the %d-byte boot-region sanity bound", dosPath, len(dosBin), DosMaxSize)
	}

	codeBin, err := os.ReadFile(codePath)
	if err != nil {
		return fmt.Errorf("read netboot code: %w", err)
	}
	// The AUTO BASIC does LOAD "<name>" CODE 32768 : CALL 32768, so the image
	// loads at LoadAddress (&8000) and must end at or before &10000 — the top of
	// the Z80 address space. A larger image runs past &FFFF and cannot load on a
	// real SAM (the SimCoupé matrix only checks the disk *builds*, so without this
	// guard an over-budget boot image would pass CI; it caught a >&10000 i99
	// regression, 2026-06-15).
	if end := LoadAddress + uint32(len(codeBin)); end > 0x10000 {
		return fmt.Errorf("netboot code %s: %d bytes loads at &%04X..&%05X, past the &10000 ceiling by %d bytes (won't load on a real SAM)",
			codePath, len(codeBin), LoadAddress, end, end-0x10000)
	}

	disk := samfile.NewDiskImage()

	// Slot 0: the boot DOS (ROM BOOT reads T4S1 raw).
	if err := disk.AddCodeFile(dosName, dosBin, dosLoad, 0); err != nil {
		return fmt.Errorf("AddCodeFile(%s): %w", dosName, err)
	}
	if err := disk.SetStartAddressPageUnusedBits(dosName, 3); err != nil {
		return fmt.Errorf("SetStartAddressPageUnusedBits(%s): %w", dosName, err)
	}

	// Slot 1: AUTO BASIC — CLEAR &7FFF : LOAD "<name>" CODE 32768 : CALL 32768.
	auto := &sambasic.File{
		StartLine: 10,
		Lines: []sambasic.Line{
			{Number: 10, Tokens: []sambasic.Token{
				sambasic.CLEAR,
				sambasic.Number(uint16(LoadAddress - 1)),
			}},
			{Number: 20, Tokens: []sambasic.Token{
				sambasic.LOAD,
				sambasic.String(`"` + codeName + `"`),
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
		return fmt.Errorf("AddBasicFile(auto): %w", err)
	}

	// Slot 2: the netboot CODE file at &8000 (matches the program's org &8000).
	if err := disk.AddCodeFile(codeName, codeBin, LoadAddress, 0); err != nil {
		return fmt.Errorf("AddCodeFile(%s): %w", codeName, err)
	}

	if err := disk.Save(outputPath); err != nil {
		return fmt.Errorf("save %s: %w", outputPath, err)
	}

	fmt.Printf("%-12s%d bytes  T4S1-T5S10\n", dosName+":", len(dosBin))
	fmt.Printf("auto:       %d bytes   (LOAD \"%s\" CODE 32768 : CALL 32768)\n", len(auto.Bytes()), codeName)
	fmt.Printf("%-12s%d bytes\n", codeName+":", len(codeBin))
	fmt.Printf("Built %s (bootable netboot disk)\n", outputPath)
	return nil
}
