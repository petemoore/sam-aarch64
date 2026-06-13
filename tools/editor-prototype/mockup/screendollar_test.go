package mockup

import (
	"path/filepath"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/fonts"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/samfile/v3"
)

// TestMode4PixelPacking decodes the SCREEN$ display bytes back to a CLUT-index
// field and checks it matches the rasterised glyph — guarding the 4bpp pack
// (2 pixels/byte, high nibble first, TM p18).
func TestMode4PixelPacking(t *testing.T) {
	f8, err := fonts.Font8x8()
	if err != nil {
		t.Fatal(err)
	}
	s := New(geom(t, "mode4-8x8"), f8)
	s.Set(0, 0, samscreen.Cell{Ch: 'A', Ink: 5, Paper: 2})
	b, err := s.ScreenBytes()
	if err != nil {
		t.Fatal(err)
	}
	// Decode the first 8 scan lines of cell column 0 (8 px wide).
	for y := 0; y < 8; y++ {
		bits := f8.Row('A', y)
		// byte 0 of the line holds pixels 0,1; byte 1 holds 2,3; etc.
		for px := 0; px < 8; px++ {
			byteIdx := y*128 + px/2
			val := b[byteIdx]
			var clut uint8
			if px%2 == 0 {
				clut = val >> 4
			} else {
				clut = val & 0x0f
			}
			on := (bits>>(uint(7-px)))&1 == 1
			want := uint8(2)
			if on {
				want = 5
			}
			if clut != want {
				t.Fatalf("MODE4 pixel (%d,%d) clut=%d want=%d", px, y, clut, want)
			}
		}
	}
}

// TestWriteSCREENDiskType20 writes a SCREEN$ `.mgt` and confirms it reads back
// as a Type-20 file with the mode in FileTypeInfo[0] (spec §2.4: dir byte 0xDD
// = screen MODE; docs/notes/sam-file-header.md §Type 20).
func TestWriteSCREENDiskType20(t *testing.T) {
	f8, err := fonts.Font8x8()
	if err != nil {
		t.Fatal(err)
	}
	s := New(geom(t, "mode3-8x8"), f8)
	s.Set(0, 0, samscreen.Cell{Ch: 'Z', Ink: 3, Paper: 0})
	path := filepath.Join(t.TempDir(), "screen.mgt")
	if err := s.WriteSCREENDisk(path, "viewer"); err != nil {
		t.Fatal(err)
	}
	disk, err := samfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	dj := disk.DiskJournal()
	var found bool
	for _, fe := range dj {
		if fe.Used() && fe.Name.String() == "viewer" {
			found = true
			if fe.Type != samfile.FT_SCREEN {
				t.Errorf("file type = %v, want Screen", fe.Type)
			}
			if fe.FileTypeInfo[0] != byte(samscreen.Mode3) {
				t.Errorf("mode byte = %d, want %d", fe.FileTypeInfo[0], byte(samscreen.Mode3))
			}
		}
	}
	if !found {
		t.Fatal("SCREEN$ file not found on disk")
	}
}

// TestPlaceholderRefusesScreenBytes confirms a font-less screen cannot emit
// SCREEN$ bytes (no glyphs to pack).
func TestPlaceholderRefusesScreenBytes(t *testing.T) {
	s := New(geom(t, "mode3-6x6"), nil, WithPlaceholder("pending"))
	if _, err := s.ScreenBytes(); err == nil {
		t.Fatal("expected error for placeholder ScreenBytes")
	}
}
