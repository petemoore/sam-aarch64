// build-screen-disk constructs a SAM Coupé disk image containing
// SAMDOS2 plus a CODE file carrying a raw mode-4 screen dump.
//
// Usage:
//
//	build-screen-disk <screen.bin> <output.mgt>
//
// The screen.bin must be exactly 24576 bytes — mode-4 pixel data
// (256×192 at 4 bits/pixel), as produced by the basic-emulator-spike
// --screen flag.
//
// To view the screen on real SAM or in SimCoupé:
//
//	(boot disk so SAMDOS2 takes over)
//	MODE 4: LOAD "screen" CODE 32768
//
// The image will display using whatever palette is currently active
// (no CLUT travels with the file in CODE form). For our purpose —
// visually confirming what the spike's video RAM contains at the
// READY breakpoint — pixel structure is the load-bearing thing.
//
// (LOAD "screen" SCREEN$ would auto-switch the mode but requires
// patching the directory entry's filetype byte from 19 to 20 and
// FileTypeInfo[0] to the mode. The CODE path Pete suggested skips
// that complexity entirely.)
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/petemoore/samfile/v3"
)

const (
	// LoadAddress for the screen body. SAM mode-4 screens always live
	// at 0x8000 (start of section C) in the displayed page — see ROM
	// disasm L22536-22539 (SELSCRN). LOAD "x" SCREEN$ also copies to
	// 0x8000 in the current screen page (ROM disasm L22523-22587,
	// LDSCRN).
	LoadAddress uint32 = 0x8000

	// SamdosLoadAddress: as used by build-disk/main.go — the
	// canonical samdos2 start address. See that file for citation.
	SamdosLoadAddress uint32 = 491529

	// ScreenBytes for SAM mode 4 (BASIC MODE 4 = internal mode 3):
	// 256×192 at 4 bits/pixel = 192×128 = 24576 bytes.
	// Source: ROM disasm L22921-22931 (SCRLEN).
	ScreenBytes = 24576
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("build-screen-disk: ")

	if len(os.Args) != 3 {
		log.Fatalf("usage: %s <screen.bin> <output.mgt>", os.Args[0])
	}
	screenPath := os.Args[1]
	outputPath := os.Args[2]

	samdos2, err := os.ReadFile("reference/samdos/samdos2.bin")
	if err != nil {
		log.Fatalf("read samdos2: %v", err)
	}
	if len(samdos2) != 10000 {
		log.Fatalf("samdos2: expected 10000 bytes, got %d", len(samdos2))
	}

	screen, err := os.ReadFile(screenPath)
	if err != nil {
		log.Fatalf("read %s: %v", screenPath, err)
	}
	if len(screen) != ScreenBytes {
		log.Fatalf("screen: expected %d bytes (mode-4 screen), got %d",
			ScreenBytes, len(screen))
	}

	disk := samfile.NewDiskImage()

	// Slot 0: SAMDOS2 — boot loader. Pattern copied verbatim from
	// tools/build-disk/main.go so the disk boots the same way.
	if err := disk.AddCodeFile("samdos2", samdos2, SamdosLoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(samdos2): %v", err)
	}
	if err := disk.SetStartAddressPageUnusedBits("samdos2", 3); err != nil {
		log.Fatalf("SetStartAddressPageUnusedBits(samdos2): %v", err)
	}

	// Slot 1: "screen" — raw mode-4 pixel data, loads to 0x8000.
	if err := disk.AddCodeFile("screen", screen, LoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(screen): %v", err)
	}

	if err := disk.Save(outputPath); err != nil {
		log.Fatalf("save %s: %v", outputPath, err)
	}

	fmt.Printf("samdos2: %d bytes\n", len(samdos2))
	fmt.Printf("screen:  %d bytes  (mode-4 raw, loads to 0x%04X)\n",
		len(screen), LoadAddress)
	fmt.Printf("Built %s\n\n", outputPath)
	fmt.Println("To view: boot disk in SimCoupé (or real SAM), then at the BASIC prompt:")
	fmt.Println("  MODE 4: LOAD \"screen\" CODE 32768")
}
