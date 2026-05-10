// build-screen-disk constructs a SAM Coupé disk image containing
// SAMDOS2 plus a SCREEN$ file carrying a raw mode-4 screen dump.
//
// Usage:
//
//	build-screen-disk <screen.bin> [<screen.paltab>] <output.mgt>
//
// The screen.bin must be exactly 24576 bytes — mode-4 pixel data
// (256×192 at 4 bits/pixel), as produced by the basic-emulator-spike
// --screen flag.
//
// The optional screen.paltab is a 40-byte PALTAB block (also emitted
// by the spike's --screen flag). When provided, it is appended to
// the SCREEN$ body so LDSCRN restores the palette on load and the
// image renders with the same colours the ROM had on screen —
// otherwise SimCoupé's current CLUT determines the colour mapping
// and dark-on-dark surprises are possible (LDSCRN palette-restore
// path: rom-disasm L22552-22573 / 0xE2F7-0xE30E).
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
	"github.com/petemoore/samfile/v3/sambasic"
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

	var screenPath, paltabPath, outputPath string
	switch len(os.Args) {
	case 3:
		screenPath, outputPath = os.Args[1], os.Args[2]
	case 4:
		screenPath, paltabPath, outputPath = os.Args[1], os.Args[2], os.Args[3]
	default:
		log.Fatalf("usage: %s <screen.bin> [<screen.paltab>] <output.mgt>", os.Args[0])
	}

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

	// If a paltab was provided, append it. LDSCRN computes
	// palette_extra = file_len_mod_16K - SCRLEN(mode); if > 0 it
	// copies 40 bytes to PALTAB. Total body = pixels + paltab.
	body := screen
	if paltabPath != "" {
		paltab, err := os.ReadFile(paltabPath)
		if err != nil {
			log.Fatalf("read %s: %v", paltabPath, err)
		}
		if len(paltab) != 40 {
			log.Fatalf("paltab: expected 40 bytes, got %d", len(paltab))
		}
		body = make([]byte, 0, ScreenBytes+40)
		body = append(body, screen...)
		body = append(body, paltab...)
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

	// Slot 1: auto-run BASIC. StartLine=10 so SAM auto-RUNs it on
	// boot (per the ROM auto-run gate at rom-disasm:22471-22484
	// checking dir byte 0xF2). It loads the SCREEN$ then PAUSE 0,
	// so BASIC's "OK" prompt never prints over the bottom rows of
	// our captured screen.
	auto := &sambasic.File{
		StartLine: 10,
		Lines: []sambasic.Line{
			{Number: 10, Tokens: []sambasic.Token{
				sambasic.MODE,
				sambasic.Number(4),
			}},
			{Number: 20, Tokens: []sambasic.Token{
				sambasic.LOAD,
				sambasic.String(`"screen"`),
				sambasic.SCREEN_2B,
			}},
			{Number: 30, Tokens: []sambasic.Token{
				sambasic.PAUSE,
				sambasic.Number(0),
			}},
		},
	}
	if err := disk.AddBasicFile("auto", auto); err != nil {
		log.Fatalf("AddBasicFile(auto): %v", err)
	}

	// Slot 2: "screen" — raw mode-4 pixel data (+ optional 40-byte
	// PALTAB suffix). AddCodeFile writes it as FT_CODE (19); we
	// convert to FT_SCREEN (20) below.
	if err := disk.AddCodeFile("screen", body, LoadAddress, 0); err != nil {
		log.Fatalf("AddCodeFile(screen): %v", err)
	}

	// Patch directory entry of slot 2 (bytes 512..767 in the disk
	// image — slots 0/1 occupy track 0 sector 1, slot 2 lives in
	// track 0 sector 2 at byte 512) to make the screen file a
	// SCREEN$ entry. Constants from Tech Manual v3-0 directory
	// layout (tech-man:4349-4400) and FT_SCREEN research.
	const (
		slot2Off            = 512
		dirFileTypeOff      = 0
		dirFirstSectorTrack = 13
		dirFirstSectorSec   = 14
		dirFileTypeInfo0Off = 221 // FileTypeInfo[0] = screen mode
		ftScreen            = 20
		internalModeForMode4 = 3 // ROM stores MODE-1 internally as 0..3
	)
	disk[slot2Off+dirFileTypeOff] = ftScreen
	disk[slot2Off+dirFileTypeInfo0Off] = internalModeForMode4

	// Patch the body's 9-byte header (first byte = filetype mirror).
	// Body lives in whatever sector AddCodeFile allocated; read that
	// from the directory entry. MGT sector → byte-offset formula
	// from samfile.go:1055.
	track := disk[slot2Off+dirFirstSectorTrack]
	sector := disk[slot2Off+dirFirstSectorSec]
	bodyOff := int(track>>7)*5120 + (int(sector)-1)*512 + int(track&0x7f)*10240
	disk[bodyOff] = ftScreen

	if err := disk.Save(outputPath); err != nil {
		log.Fatalf("save %s: %v", outputPath, err)
	}

	fmt.Printf("samdos2: %d bytes\n", len(samdos2))
	fmt.Printf("screen:  %d bytes (pixels=%d + paltab=%d)\n",
		len(body), len(screen), len(body)-len(screen))
	fmt.Printf("         body at T%d/S%d → disk byte %d\n", track&0x7f, sector, bodyOff)
	fmt.Printf("         FT_SCREEN, mode 3 (BASIC MODE 4)\n")
	fmt.Printf("Built %s\n\n", outputPath)
	fmt.Println("To view: boot disk in SimCoupé (or real SAM), then at the BASIC prompt:")
	fmt.Println("  LOAD \"screen\" SCREEN$")
}
