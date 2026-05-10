// build-screen-disk constructs a SAM Coupé disk image containing
// SAMDOS2 plus a SCREEN$ file carrying a raw mode-4 screen dump.
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
//	LOAD "screen" SCREEN$
//
// LDSCRN at ROM 0xE2C0 (rom-disasm:22523) reads the screen mode from
// the directory's FileTypeInfo[0] byte, switches to that mode if
// needed, calls SELSCRN to page the displayed screen page into
// section C, then loads the body at 0x8000 — which now lands on the
// visible screen. (Plain LOAD CODE 32768 does NOT do the SELSCRN
// step, so the bytes land in whatever page HMPR currently has in
// section C, not the displayed screen.)
//
// samfile has FT_SCREEN as a constant but no AddScreenFile helper,
// so we use AddCodeFile to allocate sectors and write the body, then
// patch three bytes on the resulting disk image to convert the entry
// to a SCREEN$:
//
//   - directory byte 0   : filetype 19 → 20
//   - directory byte 221 : FileTypeInfo[0] ← screen mode (3 = MODE 4)
//   - body  byte 0       : filetype mirror 19 → 20
//
// The image displays with whatever palette is currently active. The
// SAM ROM's SAVE SCREEN$ flow appends 40 bytes PALTAB + a variable-
// length LINICOLS table to capture the palette; LOAD SCREEN$ only
// applies them if the body is longer than the bare-screen size, so
// our 24576-byte body skips palette restore (LDSCRN exits early at
// rom-disasm:22540 / 0xE2F5 `RET Z`).
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

	// Slot 1: "screen" — raw mode-4 pixel data. AddCodeFile writes
	// it as FT_CODE (19); we convert to FT_SCREEN (20) below.
	if err := disk.AddCodeFile("screen", screen, LoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(screen): %v", err)
	}

	// Patch directory entry of slot 1 (bytes 256..511 in the disk
	// image — first dir track sector holds slots 0 and 1) to make it
	// a SCREEN$ entry. Constants from Tech Manual v3-0 directory
	// layout (tech-man:4349-4400) and FT_SCREEN research.
	const (
		slot1Off            = 256
		dirFileTypeOff      = 0
		dirFirstSectorTrack = 13
		dirFirstSectorSec   = 14
		dirFileTypeInfo0Off = 221 // FileTypeInfo[0] = screen mode
		ftScreen            = 20
		internalModeForMode4 = 3 // ROM stores MODE-1 internally as 0..3
	)
	disk[slot1Off+dirFileTypeOff] = ftScreen
	disk[slot1Off+dirFileTypeInfo0Off] = internalModeForMode4

	// Patch the body's 9-byte header (first byte = filetype mirror).
	// Body lives in whatever sector AddCodeFile allocated; read that
	// from the directory entry. MGT sector → byte-offset formula
	// from samfile.go:1055.
	track := disk[slot1Off+dirFirstSectorTrack]
	sector := disk[slot1Off+dirFirstSectorSec]
	bodyOff := int(track>>7)*5120 + (int(sector)-1)*512 + int(track&0x7f)*10240
	disk[bodyOff] = ftScreen

	if err := disk.Save(outputPath); err != nil {
		log.Fatalf("save %s: %v", outputPath, err)
	}

	fmt.Printf("samdos2: %d bytes\n", len(samdos2))
	fmt.Printf("screen:  %d bytes  (mode-4 SCREEN$, body header byte 0 = 20,\n", len(screen))
	fmt.Printf("                    dir FileTypeInfo[0] = 3 = BASIC MODE 4)\n")
	fmt.Printf("                    body at T%d/S%d → disk byte %d\n", track&0x7f, sector, bodyOff)
	fmt.Printf("Built %s\n\n", outputPath)
	fmt.Println("To view: boot disk in SimCoupé (or real SAM), then at the BASIC prompt:")
	fmt.Println("  LOAD \"screen\" SCREEN$")
}
