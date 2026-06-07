package mockup

import (
	"fmt"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/samfile/v3"
)

// ScreenBytes is the SAM SCREEN$ payload (spec §2.4): the frame as mode-correct
// display bytes followed by the 16-byte PALTAB. SAM display memory is 24 KB
// (192 scan lines × 128 bytes/line, TM p15/p29 — "next scan line = +128").
//   - MODE 4: 4 bits/pixel, 2 pixels/byte, MS nibble = first pixel (TM p18).
//   - MODE 3: 2 bits/pixel, 4 pixels/byte, the two MS bits = first pixel (TM p18).
//
// PALTAB is the 16 power-on CLUT register values (TM p18); a real SAM SCREEN$
// carries the palette so the image is self-contained.
const (
	displayBytes = 24576
	bytesPerLine = 128
)

// ScreenBytes rasterises the grid (no aspect scaling — this is real display
// memory) into mode-correct display bytes + the 16-byte PALTAB. It returns the
// concatenation (24576 + 16 bytes). It errors for a font-less placeholder
// screen (no glyph data to pack) — SCREEN$ output needs a real font.
func (s *Screen) ScreenBytes() ([]byte, error) {
	if s.noFont {
		return nil, fmt.Errorf("ScreenBytes: no font (placeholder screen) — SCREEN$ needs a real glyph font")
	}
	geom := s.Geometry()
	// Pixel field: ink CLUT index per pixel (0..15), from the glyph rasterise.
	pw, ph := geom.Mode.PixelWidth(), geom.Mode.PixelHeight()
	field := make([]uint8, pw*ph) // CLUT index per pixel
	fw, fh := geom.Font.W, geom.Font.H
	for cy := 0; cy < geom.Rows; cy++ {
		for cx := 0; cx < geom.Cols; cx++ {
			cell := s.At(cx, cy)
			for ry := 0; ry < fh; ry++ {
				bits := s.font.Row(cell.Ch, ry)
				for rx := 0; rx < fw; rx++ {
					on := (bits>>(uint(fw-1-rx)))&1 == 1
					ci := cell.Paper
					if on {
						ci = cell.Ink
					}
					field[(cy*fh+ry)*pw+(cx*fw+rx)] = ci
				}
			}
		}
	}

	out := make([]byte, displayBytes+16)
	switch geom.Mode {
	case samscreen.Mode4:
		// 2 pixels/byte: high nibble first pixel.
		for y := 0; y < ph; y++ {
			for bx := 0; bx < bytesPerLine; bx++ {
				p0 := field[y*pw+bx*2] & 0x0f
				p1 := field[y*pw+bx*2+1] & 0x0f
				out[y*bytesPerLine+bx] = (p0 << 4) | p1
			}
		}
	case samscreen.Mode3:
		// 4 pixels/byte: two MS bits first pixel.
		for y := 0; y < ph; y++ {
			for bx := 0; bx < bytesPerLine; bx++ {
				var b uint8
				for k := 0; k < 4; k++ {
					p := field[y*pw+bx*4+k] & 0x03
					b |= p << (uint(6 - 2*k))
				}
				out[y*bytesPerLine+bx] = b
			}
		}
	default:
		return nil, fmt.Errorf("ScreenBytes: unsupported mode %v", geom.Mode)
	}

	// PALTAB: 16 CLUT register values (power-on default).
	pal := samscreen.DefaultPalette()
	copy(out[displayBytes:], pal[:])
	return out, nil
}

// WriteSCREENDisk writes a single-file `.mgt` carrying the frame as a Type 20
// SCREEN$ file named name (spec §2.4: "packaged as a Type 20 file ... on an
// `.mgt`"). The mode is recorded in directory byte 0xDD (FileTypeInfo[0]); the
// body is the display bytes + PALTAB. It uses the public samfile API:
// AddCodeFile places the bytes, then the file's directory entry is retyped to
// FT_SCREEN with the mode byte — the documented Type-20 layout
// (docs/notes/sam-file-header.md §Type 20).
func (s *Screen) WriteSCREENDisk(path, name string) error {
	body, err := s.ScreenBytes()
	if err != nil {
		return err
	}
	disk := samfile.NewDiskImage()
	// Display memory loads at the screen page; &8000 is a valid RAM address
	// that satisfies AddCodeFile's ROM guard. The recorded load address is
	// documentary for a SCREEN$ — the ROM LOADs it to the active display page.
	const screenLoad uint32 = 0x8000
	if err := disk.AddCodeFile(name, body, screenLoad, 0); err != nil {
		return fmt.Errorf("AddCodeFile(%s): %w", name, err)
	}
	if err := retypeAsScreen(disk, name, byte(s.Geometry().Mode)); err != nil {
		return err
	}
	if err := disk.Save(path); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	return nil
}

// retypeAsScreen rewrites the named file's directory entry to FT_SCREEN with
// the screen mode in FileTypeInfo[0] (dir byte 0xDD), and mirrors the type into
// the body-header byte. This is the only post-AddCodeFile tweak needed to turn
// a CODE file into a Type-20 SCREEN$ via the public samfile API.
func retypeAsScreen(disk *samfile.DiskImage, name string, mode byte) error {
	dj := disk.DiskJournal()
	for slot, fe := range dj {
		if !fe.Used() || fe.Name.String() != name {
			continue
		}
		fe.Type = samfile.FT_SCREEN
		fe.FileTypeInfo[0] = mode
		disk.WriteFileEntry(dj, slot)
		// Mirror the type byte into the file's body header (first sector
		// byte 0): the on-disk body header byte 0 is the file type.
		(*disk)[fe.FirstSector.Offset()] = byte(samfile.FT_SCREEN)
		return nil
	}
	return fmt.Errorf("retypeAsScreen: file %q not found", name)
}
