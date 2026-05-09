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
// hybrid. Slots 1-3 are constructed via the v3 samfile API
// (AddBasicFile / AddCodeFile from samfile PR #11). Slot 0 also uses
// AddCodeFile.
//
// Two byte-level patches are applied after the samfile calls, both
// tracked as upstream gaps in samfile (see UPSTREAM-GAPS in the
// commit body / PR description):
//
//  1. For every type-19 CODE file, body-header bytes 5-6 are set to
//     0xff 0xff. samfile's FileHeader.Raw() hard-codes these to
//     0x00 0x00, but ROM's LOAD-CODE auto-exec gate at
//     rom-disasm:22471-22484 requires BOTH dir byte 0xF2 AND body-
//     header byte 6 to be 0xff to suppress auto-execution. With
//     byte 6 = 0x00, our AUTO line's `LOAD "stub" CODE 32768` would
//     try to auto-exec at a garbage address rather than returning
//     to BASIC for the subsequent `: CALL`.
//
//  2. For samdos2 specifically, body-header byte 8 (StartPage) is
//     forced to 0x7d to match the canonical FRED 02 / Defender
//     install. AddCodeFile derives this from the load address
//     (giving 0x01); the 0x60 bits are decorative and ROM masks to
//     0x1f when reading, so this is byte-parity rather than a
//     functional fix.
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
	// header. ROM BOOT reads T4S1 raw to 0x8000 and JPs to 0x8009
	// after locating the "BOOT" magic at sector offset 256, so this
	// address is only consulted by SAMDOS's later self-bookkeeping.
	// 0x8009 matches what the canonical samdos2-on-FRED-02 records.
	SamdosLoadAddress uint32 = 0x8009

	// SamdosCanonicalStartPage is the decorative StartPage byte at
	// body-header offset 8 in the canonical samdos2 binary (the
	// 0x60 bits are "decorative" — anything that reads the field
	// masks to 0x1f). AddCodeFile derives this from the load
	// address (giving 0x01); we post-patch it for byte-parity.
	SamdosCanonicalStartPage byte = 0x7d
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
	patchAutoExecGate(disk, "samdos2")
	patchStartPage(disk, "samdos2", SamdosCanonicalStartPage)
	patchMGTFutureAndPast(disk, "samdos2")

	// Slot 1: AUTO BASIC. AUTO-RUN line 10 reads:
	//   10 CLEAR 32767: LOAD "stub" CODE 32768: CALL 32768
	//
	// sambasic.File produces the on-disk tokenised body, the
	// default 92-byte numeric-vars area + 512-byte gap (matching
	// the canonical real-SAVE convention — see
	// sam-basic-save-format.md for the ROM citations), the 0xFF
	// end-of-program sentinel, and the dir-entry triplets at
	// 0xDD/0xE0/0xE3 encoding the program-section sizes in 8000H
	// REL PAGE FORM. StartLine=10 marks the entry as auto-RUN;
	// SAM ROM at rom-disasm:22471-22484 checks dir byte 0xF2 = 0
	// to dispatch BASIC start-line auto-RUN.
	auto := &sambasic.File{
		StartLine: 10,
		Lines: []sambasic.Line{{
			Number: 10,
			Tokens: []sambasic.Token{
				sambasic.CLEAR,
				sambasic.Number(uint16(LoadAddress - 1)),
				sambasic.Literal(':'),
				sambasic.LOAD,
				sambasic.Literal('"'),
				sambasic.String("stub"),
				sambasic.Literal('"'),
				sambasic.CODE,
				sambasic.Number(uint16(LoadAddress)),
				sambasic.Literal(':'),
				sambasic.CALL,
				sambasic.Number(uint16(LoadAddress)),
			},
		}},
	}
	if err := disk.AddBasicFile("auto", auto); err != nil {
		log.Fatalf("AddBasicFile(auto): %v", err)
	}
	// AddBasicFile sets MGTFutureAndPast[6..7] = 0xff in the dir
	// entry, but the body header on disk is still produced by
	// FileHeader.Raw() which hard-codes bytes 5-6 to zero. Patch
	// for canonical parity. (BASIC auto-RUN doesn't use the
	// LOAD-CODE auto-exec gate, so this is byte-parity rather than
	// a functional fix.)
	patchAutoExecGate(disk, "auto")

	// Slot 2: stub CODE file. AUTO-RUN's `LOAD "stub" CODE 32768`
	// resolves this by name; the loaded body's auto-exec gate at
	// dir byte 0xF2 + body-header byte 6 must both be 0xFF for
	// LOAD CODE to return cleanly to BASIC so the subsequent
	// `: CALL` invokes the stub. AddCodeFile sets the dir byte;
	// patchAutoExecGate fixes the body-header byte.
	if err := disk.AddCodeFile("stub", stubBin, LoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(stub): %v", err)
	}
	patchAutoExecGate(disk, "stub")
	patchMGTFutureAndPast(disk, "stub")

	// Slot 3: IN data file. Read by the stub via SAMDOS HGFLE.
	if err := disk.AddCodeFile("IN", in, LoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(IN): %v", err)
	}
	patchAutoExecGate(disk, "IN")
	patchMGTFutureAndPast(disk, "IN")

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

// patchAutoExecGate sets the body-header auto-exec gate bytes for
// the named CODE file to 0xff 0xff. Required because samfile's
// FileHeader.Raw() hard-codes bytes 5-6 to 0x00 0x00 instead of
// emitting the ExecutionAddress mirror — a samfile gap (see
// UPSTREAM-GAPS in the commit body / PR description).
func patchAutoExecGate(disk *samfile.DiskImage, name string) {
	_, fe := findFileEntry(disk, name)
	off := sectorByteOffset(fe.FirstSector.Track, fe.FirstSector.Sector)
	disk[off+5] = 0xff
	disk[off+6] = 0xff
}

// patchStartPage forces the body-header StartPage byte (offset 8)
// AND its mirror in the dir entry (raw[0xEC]) for the named file
// to value. AddCodeFile derives StartPage from the load address;
// some canonical disks (e.g. FRED 02 samdos2) use decorative high
// bits that ROM masks off but that matter for byte-perfect parity.
func patchStartPage(disk *samfile.DiskImage, name string, value byte) {
	slot, fe := findFileEntry(disk, name)
	off := sectorByteOffset(fe.FirstSector.Track, fe.FirstSector.Sector)
	disk[off+8] = value
	disk[slot*256+0xEC] = value
}

// patchMGTFutureAndPast mirrors the 9-byte body header into the
// directory entry's MGTFutureAndPast field (dir bytes 0xD3..0xDB).
// AddCodeFile leaves this region zeroed, but the canonical SAMDOS
// SAVE convention populates it; AddBasicFile already does the right
// thing internally, so this is only needed after AddCodeFile.
func patchMGTFutureAndPast(disk *samfile.DiskImage, name string) {
	slot, fe := findFileEntry(disk, name)
	bodyOff := sectorByteOffset(fe.FirstSector.Track, fe.FirstSector.Sector)
	dirOff := slot * 256
	for i := 0; i < 9; i++ {
		disk[dirOff+0xD3+i] = disk[bodyOff+i]
	}
}

func findFileEntry(disk *samfile.DiskImage, name string) (int, *samfile.FileEntry) {
	for i, fe := range disk.DiskJournal() {
		if fe.Used() && fe.Name.String() == name {
			return i, fe
		}
	}
	log.Fatalf("file %q not found in disk journal", name)
	panic("unreachable")
}

// sectorByteOffset reports the byte offset within the disk image
// where the named sector's data begins. SAM .mgt geometry is
// cylinder-interleaved: each cylinder = side-0 (5120 B) + side-1
// (5120 B). The track-byte's high bit selects the side; the low
// 7 bits select the cylinder.
func sectorByteOffset(track, sector byte) int {
	side := int(track) >> 7
	cyl := int(track) & 0x7f
	return side*5120 + (int(sector)-1)*512 + cyl*10240
}
