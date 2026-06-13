package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/mockup"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/viewer"
)

// runMockupSheets renders the same editor frame (real `.tbn` content) at every
// §3 geometry to a PNG and a SCREEN$ `.mgt` (spec §2.4 / §5: the font/geometry
// comparison sheets for all six options). 8×8 geometries render real glyphs;
// 6-px geometries whose vendored font has not yet landed render a placeholder
// note PNG (no SCREEN$ — that needs a real font), clearly flagged.
func runMockupSheets(doc *viewer.Document, outDir, fontDir string, crt bool) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for _, geom := range samscreen.AllGeometries() {
		font, real, note := loadFont(geom, fontDir)
		pngPath := filepath.Join(outDir, "viewer-"+geom.Name+".png")

		var opts []mockup.Option
		if crt {
			opts = append(opts, mockup.WithCRT())
		}
		if !real {
			opts = append(opts, mockup.WithPlaceholder(note))
			scr := mockup.New(geom, nil, opts...)
			if err := scr.WritePNGFile(pngPath); err != nil {
				return err
			}
			fmt.Printf("%-12s %s (placeholder — %s)\n", geom.Name, pngPath, note)
			continue
		}

		scr := mockup.New(geom, font, opts...)
		v := viewer.NewView(doc)
		v.Render(scr)
		if err := scr.WritePNGFile(pngPath); err != nil {
			return err
		}
		// SCREEN$ on an `.mgt` (spec §2.4: ground-truth readability check).
		mgtPath := filepath.Join(outDir, "viewer-"+geom.Name+".mgt")
		if err := scr.WriteSCREENDisk(mgtPath, "viewer"); err != nil {
			return err
		}
		img := scr.Image().Bounds()
		fmt.Printf("%-12s %s (%dx%d) + %s\n", geom.Name, pngPath, img.Dx(), img.Dy(), mgtPath)
	}
	return nil
}
