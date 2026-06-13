package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/mockup"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/viewer"
)

// runFramesSmoke is the non-interactive verification mode the spec calls for: it
// renders N viewer frames through the mockup backend (no terminal), scrolling
// one page between frames, and writes each to PNG. It exercises the full
// viewer→abstraction→raster path headlessly, so CI and agents can confirm the
// viewer renders without a TTY. A 6-px geometry with no vendored font renders a
// placeholder note frame (still proves the pipeline).
func runFramesSmoke(doc *viewer.Document, geom samscreen.Geometry, fontDir, outDir string, n int) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	font, real, note := loadFont(geom, fontDir)
	if !real {
		scr := mockup.New(geom, nil, mockup.WithPlaceholder(note))
		path := filepath.Join(outDir, fmt.Sprintf("frame-%s-00.png", geom.Name))
		if err := scr.WritePNGFile(path); err != nil {
			return err
		}
		fmt.Printf("frame %s (placeholder — %s)\n", path, note)
		return nil
	}

	v := viewer.NewView(doc)
	for i := 0; i < n; i++ {
		scr := mockup.New(geom, font)
		v.Render(scr)
		path := filepath.Join(outDir, fmt.Sprintf("frame-%s-%02d.png", geom.Name, i))
		if err := scr.WritePNGFile(path); err != nil {
			return err
		}
		b := scr.Image().Bounds()
		fmt.Printf("frame %s (%dx%d) line %d/%d\n", path, b.Dx(), b.Dy(), v.Cursor()+1, doc.LineCount())
		v.PageDown(scr)
	}
	return nil
}
