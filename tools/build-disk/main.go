// build-disk constructs the M0 round-trip disk image.
//
// Layout, semantics and citations:
//
//	docs/notes/test-mgt-byte-layout.md   ← byte-by-byte reference
//	docs/notes/sam-basic-save-format.md  ← BASIC vars/gap invariant
//
// Slots:
//
//	0  samdos2   T4S1..T5S10  (20 sectors; ROM BOOT reads T4S1 raw)
//	1  auto      T6S1..T6S2   (BASIC AUTO: CLEAR + LOAD + CALL)
//	2  stub      T6S3         (the assembler stub)
//	3  IN        T6S4         (assembly source fixture)
//
// This program replaces the earlier tools/build-disk.sh bash+Python
// hybrid. All four slots are constructed via the v3 samfile API
// (AddBasicFile / AddCodeFile). The only post-call tweak is
// SetStartAddressPageUnusedBits for samdos2's decorative high bits.
//
// Usage:
//
//	build-disk <input.s> <output.mgt>
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/petemoore/samfile/v3"
	"github.com/petemoore/samfile/v3/sambasic"
)

const (
	// LoadAddress is the SAM address the stub and IN fixture load to.
	// The AUTO BASIC line does `LOAD "stub" CODE 32768: CALL 32768`.
	LoadAddress uint32 = 0x8000

	// SamdosLoadAddress is the address recorded in the samdos2 body
	// header — this is what `samfile ls` reports as `Start` for the
	// canonical samdos2 across the FRED 02 / Defender / pete-made
	// installs and most other SAM disks that include SAMDOS. It
	// decomposes to page 29 (low 5 bits of the StartPage byte) plus
	// offset 9 in 0x8000-0xBFFF page-offset form:
	//   (29 + 1) * 16384 + 9 = 491529
	// ROM BOOT itself reads T4S1 raw to 0x8000 and JPs to 0x8009
	// after locating the "BOOT" magic at sector offset 256; this
	// address is what SAMDOS's later self-bookkeeping consults.
	SamdosLoadAddress uint32 = 491529
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("build-disk: ")

	if len(os.Args) != 3 {
		log.Fatalf("usage: %s <input.s> <output.mgt>", os.Args[0])
	}
	inputPath := os.Args[1]
	outputPath := os.Args[2]

	samdos2, err := os.ReadFile("reference/samdos/samdos2.bin")
	if err != nil {
		log.Fatalf("read samdos2: %v", err)
	}
	if len(samdos2) != 10000 {
		log.Fatalf("samdos2: expected 10000 bytes, got %d", len(samdos2))
	}

	stubBin, err := os.ReadFile("build/stub.bin")
	if err != nil {
		log.Fatalf("read stub: run 'make stub' first: %v", err)
	}

	in, err := os.ReadFile(inputPath)
	if err != nil {
		log.Fatalf("read %s: %v", inputPath, err)
	}

	disk := samfile.NewDiskImage()

	// Slot 0: samdos2. ROM BOOT lives at D8CD-D97D; it reads T4S1
	// raw to 0x8000 and JPs to 0x8009 after confirming the literal
	// "BOOT" at sector offset 256 (= body offset 247 in the binary,
	// since the 9-byte file header occupies T4S1 bytes 0..8). The
	// samdos2 binary places "BOOT" so this matches.
	//
	// AddCodeFile allocates the lowest free directory slot (0) and
	// the lowest free sectors (T4S1 onwards) — exactly what BOOT
	// requires.
	if err := disk.AddCodeFile("samdos2", samdos2, SamdosLoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(samdos2): %v", err)
	}
	if err := disk.SetStartAddressPageUnusedBits("samdos2", 3); err != nil {
		log.Fatalf("SetStartAddressPageUnusedBits(samdos2): %v", err)
	}

	// Slot 1: AUTO BASIC. StartLine=10 marks the entry as auto-RUN;
	// SAM ROM at rom-disasm:22471-22484 checks dir byte 0xF2 = 0
	// to dispatch BASIC start-line auto-RUN.
	auto := &sambasic.File{
		StartLine: 10,
		Lines: []sambasic.Line{
			{Number: 10, Tokens: []sambasic.Token{
				sambasic.CLEAR,
				sambasic.Number(uint16(LoadAddress - 1)),
			}},
			{Number: 20, Tokens: []sambasic.Token{
				sambasic.LOAD,
				sambasic.String(`"stub"`),
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

	// Slot 2: stub CODE file. AUTO-RUN's `LOAD "stub" CODE 32768`
	// resolves this by name; the auto-exec gate (dir byte 0xF2 +
	// body-header byte 6 both 0xFF) tells LOAD CODE to return to
	// BASIC so the subsequent `: CALL` invokes the stub.
	if err := disk.AddCodeFile("stub", stubBin, LoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(stub): %v", err)
	}

	// Slot 3: IN data file. Read by the stub via SAMDOS HGFLE.
	if err := disk.AddCodeFile("IN", in, LoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(IN): %v", err)
	}

	if err := disk.Save(outputPath); err != nil {
		log.Fatalf("save %s: %v", outputPath, err)
	}

	fmt.Printf("samdos2: %d bytes  T4S1-T5S10\n", len(samdos2))
	fmt.Printf("auto:    %d bytes   T6S1-T6S2  (PROG=%d, +VARS=%d, +GAP=%d)\n",
		len(auto.Bytes()), auto.NVARSOffset(),
		auto.NUMENDOffset()-auto.NVARSOffset(),
		auto.SAVARSOffset()-auto.NUMENDOffset())
	fmt.Printf("stub:    %d bytes     T6S3\n", len(stubBin))
	fmt.Printf("IN:      %d bytes     T6S4\n", len(in))
	fmt.Printf("Built %s\n", outputPath)
}
