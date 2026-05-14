// llist-capture builds a test disk that, when booted in SimCoupé,
// runs the SAM ROM's LLIST routine on a named BASIC file from a
// source corpus disk and writes the result through SimCoupé's
// parallel-port-to-file mechanism. The output is the canonical
// LIST rendering of the program — exactly what the SAM ROM would
// produce — providing ground truth for samfile basic-to-text.
//
// Design (docs/notes/sambasic-llist-capture.md eventually):
//
//   The control flow is driven by an injected line at line number
//   65279 appended to the target's program. The target's auto-run
//   flag is forced to 65279 so that BASIC's LOAD will jump to this
//   line. The injected line is:
//
//       65279 LLIST 1 TO 65278: CALL 16384
//
//   LLIST 1 TO 65278 emits the target's original lines (which all
//   sit at line numbers ≤ 65278 — corpus check is left to the
//   caller) to stream 3 = printer. The CALL 16384 transfers to a
//   2-byte halt stub (DI; HALT = F3 76) that was loaded into the
//   screen file at 0x4000 by the auto-run BASIC. SimCoupé's
//   -exitonhalt 1 patch then quits cleanly.
//
//   Disk layout:
//     samdos2      — boot loader (reference/samdos/samdos2.bin)
//     AUTO         — BASIC, auto-RUN, 2 lines:
//                       10 LOAD "STUB" CODE 16384
//                       20 LOAD "TARGET"
//                    (target's auto-run takes over after LOAD)
//     STUB         — CODE, 2 bytes (DI; HALT), load address 16384
//     TARGET       — BASIC, target's original body bytes + injected
//                    line at 65279; auto-run forced to 65279
//
// Usage:
//
//	llist-capture -source <disk.mgt> -file <name> -output <test.mgt>
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
	// SamdosLoadAddress is the canonical samdos2 load address — see
	// ~/git/sam-aarch64/tools/build-disk/main.go for derivation.
	SamdosLoadAddress uint32 = 491529

	// HaltStubAddress is the load address of the 2-byte DI;HALT stub.
	// 0x4000 = 16384 = top-left of the SAM mode-1 screen file in
	// section B. The screen file isn't touched by LOAD CODE
	// (other than where it draws), and LOAD's status messages go
	// to lower screen rows — so the halt-stub bytes survive across
	// both LOADs (STUB and TARGET).
	HaltStubAddress uint16 = 16384

	// InjectedLineNumber is the line number we add to the target.
	// 65279 = 0xFEFF is the maximum line number SAM accepts
	// (grammar §2.3 / ROM EVALLINO L4079). Choosing the maximum
	// minimises the chance of colliding with an existing line in
	// the target.
	InjectedLineNumber uint16 = 65279

	// LListUpperBound caps the LLIST range so our injected line
	// doesn't appear in the captured output.
	LListUpperBound uint16 = 65278
)

// haltStub is the 2-byte stub at HaltStubAddress: DI; HALT.
// SimCoupé's -exitonhalt 1 flag exits when the Z80 executes HALT
// with interrupts disabled.
var haltStub = []byte{0xF3, 0x76}

func main() {
	log.SetFlags(0)
	log.SetPrefix("llist-capture: ")

	var (
		sourcePath = flag.String("source", "", "source disk (.mgt) containing the BASIC file to capture")
		fileName   = flag.String("file", "", "name of BASIC file in source disk")
		outputPath = flag.String("output", "", "output path for the constructed test disk")
		samdosPath = flag.String("samdos", "", "path to samdos2.bin (default: <repo>/reference/samdos/samdos2.bin)")
	)
	flag.Parse()
	if *sourcePath == "" || *fileName == "" || *outputPath == "" {
		log.Fatalf("usage: llist-capture -source <disk> -file <name> -output <test.mgt>")
	}

	samdos2Path := *samdosPath
	if samdos2Path == "" {
		samdos2Path = "reference/samdos/samdos2.bin"
	}
	samdos2, err := os.ReadFile(samdos2Path)
	if err != nil {
		log.Fatalf("read samdos2 (%s): %v", samdos2Path, err)
	}
	if len(samdos2) != 10000 {
		log.Fatalf("samdos2: expected 10000 bytes, got %d", len(samdos2))
	}

	src, err := samfile.Load(*sourcePath)
	if err != nil {
		log.Fatalf("load source disk %s: %v", *sourcePath, err)
	}
	target, srcFE, err := loadBasicFile(src, *fileName)
	if err != nil {
		log.Fatalf("read %s/%s: %v", *sourcePath, *fileName, err)
	}

	modifiedBody, newNVARS, newNUMEND, newSAVARS, err := injectControlLine(target, srcFE)
	if err != nil {
		log.Fatalf("inject control line: %v", err)
	}

	disk := samfile.NewDiskImage()

	// Slot 0: samdos2. ROM BOOT reads T4S1 raw — see build-disk for the
	// load-address rationale.
	if err := disk.AddCodeFile("samdos2", samdos2, SamdosLoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(samdos2): %v", err)
	}
	if err := disk.SetStartAddressPageUnusedBits("samdos2", 3); err != nil {
		log.Fatalf("SetStartAddressPageUnusedBits(samdos2): %v", err)
	}

	// Slot 1: AUTO BASIC. Two lines:
	//   10 LOAD "STUB" CODE 16384   -- place halt stub in screen file
	//   20 LOAD "TARGET"            -- replaces AUTO in memory; target's
	//                                 own auto-RUN (forced to 65279) fires
	auto := &sambasic.File{
		StartLine: 10,
		Lines: []sambasic.Line{
			{Number: 10, Tokens: []sambasic.Token{
				sambasic.LOAD,
				sambasic.String(`"STUB"`),
				sambasic.CODE,
				sambasic.Number(HaltStubAddress),
			}},
			{Number: 20, Tokens: []sambasic.Token{
				sambasic.LOAD,
				sambasic.String(`"TARGET"`),
			}},
		},
	}
	if err := disk.AddBasicFile("AUTO", auto); err != nil {
		log.Fatalf("AddBasicFile(AUTO): %v", err)
	}

	// Slot 2: STUB CODE — 2 bytes, DI; HALT, at HaltStubAddress.
	if err := disk.AddCodeFile("STUB", haltStub, uint32(HaltStubAddress), 0); err != nil {
		log.Fatalf("AddCodeFile(STUB): %v", err)
	}

	// Slot 3: TARGET BASIC — original body bytes with injected line and
	// forced auto-run.
	if err := disk.AddBasicFileBody("TARGET", modifiedBody, newNVARS, newNUMEND, newSAVARS, InjectedLineNumber); err != nil {
		log.Fatalf("AddBasicFileBody(TARGET): %v", err)
	}

	if err := disk.Save(*outputPath); err != nil {
		log.Fatalf("save %s: %v", *outputPath, err)
	}

	fmt.Printf("samdos2: %d bytes\n", len(samdos2))
	fmt.Printf("AUTO:    %d bytes (auto-run line 10)\n", len(auto.Bytes()))
	fmt.Printf("STUB:    %d bytes (load address 0x%04X)\n", len(haltStub), HaltStubAddress)
	fmt.Printf("TARGET:  %d bytes (auto-run line %d)\n", len(modifiedBody), InjectedLineNumber)
	fmt.Printf("Built %s\n", *outputPath)
}

// loadBasicFile reads the named file from disk and confirms it's a
// SAM BASIC file. Returns the File plus its FileEntry (for the
// offsets in FileTypeInfo).
func loadBasicFile(disk *samfile.DiskImage, name string) (*samfile.File, *samfile.FileEntry, error) {
	var entry *samfile.FileEntry
	for _, fe := range disk.DiskJournal() {
		if fe == nil || !fe.Used() {
			continue
		}
		if fe.Name.String() == name {
			entry = fe
			break
		}
	}
	if entry == nil {
		return nil, nil, fmt.Errorf("file %q not found", name)
	}
	if entry.Type != samfile.FT_SAM_BASIC {
		return nil, nil, fmt.Errorf("file %q is type %v, not FT_SAM_BASIC", name, entry.Type)
	}
	f, err := disk.File(name)
	if err != nil {
		return nil, nil, fmt.Errorf("read %q: %w", name, err)
	}
	return f, entry, nil
}

// injectControlLine constructs the modified target body. It splices
// the injected line 65279 in BEFORE the 0xFF program-end marker and
// returns the new body plus the updated section-boundary offsets.
//
// Original body layout:
//   [lines]              0..NVARSOffset-1      (ends with 0xFF)
//   [numeric vars]       NVARSOffset..NUMENDOffset-1
//   [gap]                NUMENDOffset..SAVARSOffset-1
//   [string/array vars]  SAVARSOffset..end
//
// We insert (InjectedLineLen) bytes at offset (NVARSOffset-1, i.e.
// immediately BEFORE the 0xFF) and shift every section offset by that
// amount.
func injectControlLine(target *samfile.File, entry *samfile.FileEntry) ([]byte, uint32, uint32, uint32, error) {
	body := target.Body

	// Compute section offsets from FileTypeInfo (page-form, see
	// samfile FileEntry.NVARSOffset etc.). We need the absolute byte
	// offsets within the body — these correspond to the cumulative
	// lengths recorded in FileTypeInfo[0..8].
	nvarsOff := pageFormLengthFromBytes(entry.FileTypeInfo[0:3])
	numendOff := pageFormLengthFromBytes(entry.FileTypeInfo[3:6])
	savarsOff := pageFormLengthFromBytes(entry.FileTypeInfo[6:9])

	if nvarsOff == 0 {
		return nil, 0, 0, 0, fmt.Errorf("body has zero program length (NVARSOffset=0)")
	}
	if int(savarsOff) > len(body) {
		return nil, 0, 0, 0, fmt.Errorf("SAVARSOffset %d exceeds body length %d", savarsOff, len(body))
	}
	if body[nvarsOff-1] != 0xFF {
		return nil, 0, 0, 0, fmt.Errorf("expected 0xFF program-end at offset %d, got 0x%02X", nvarsOff-1, body[nvarsOff-1])
	}

	injected := buildInjectedLine()
	injectedLen := uint32(len(injected))

	out := make([]byte, 0, len(body)+len(injected))
	out = append(out, body[:nvarsOff-1]...)  // lines, no 0xFF
	out = append(out, injected...)           // injected line bytes
	out = append(out, body[nvarsOff-1:]...)  // 0xFF + vars sections

	return out, nvarsOff + injectedLen, numendOff + injectedLen, savarsOff + injectedLen, nil
}

// pageFormLengthFromBytes decodes the 3-byte page-form length used in
// FileTypeInfo. Matches samfile.pageFormLength internally.
func pageFormLengthFromBytes(b []byte) uint32 {
	page := uint32(b[0])
	offset := uint16(b[1]) | uint16(b[2])<<8
	// per samfile, offset bit 15 = 0x8000 marker — mask the low 14 bits.
	off := uint32(offset & 0x3FFF)
	return page*16384 + off
}

// buildInjectedLine constructs the bytes for the injected control
// line at InjectedLineNumber:
//
//	65279 LLIST 1 TO 65278: CALL 16384
//
// On-disk layout:
//   [MSB LSB LenLo LenHi]  4-byte line header
//   <token bytes>          tokens for "LLIST 1 TO 65278: CALL 16384"
//   0x0D                   line terminator
func buildInjectedLine() []byte {
	line := sambasic.Line{
		Number: InjectedLineNumber,
		Tokens: []sambasic.Token{
			sambasic.LLIST,                    // 0xBE
			sambasic.Number(1),                // "1" + FP form for 1
			sambasic.TO,                       // 0x8E
			sambasic.Number(LListUpperBound),  // "65278" + FP form
			sambasic.String(":"),              // statement separator
			sambasic.CALL,                     // 0xE4
			sambasic.Number(HaltStubAddress),  // "16384" + FP form
		},
	}
	return line.Bytes()
}
