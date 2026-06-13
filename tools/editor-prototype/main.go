// Command editor-prototype is the host-side functional UX authority for the
// SAM editor (i76, spec docs/specs/editor-tui-prototype-design.md). It opens a
// real compact `.tbn`, renders it at SAM-faithful geometry through the
// samscreen abstraction, and runs in three modes:
//
//   - interactive (default): a tcell terminal viewer, scrollable with cursor
//     keys (spec §5 P1, i4 parity).
//   - -mockup: generate the §2.4 font/geometry sheets — a PNG + a SCREEN$
//     `.mgt` per §3 geometry — for the readability comparison (i5/q1).
//   - -frames N: a non-interactive smoke render — paint N viewer frames to PNG
//     via the mockup backend, no terminal needed (CI-friendly verification).
//
// Demo (spec §5):
//
//	make release-unstripped-tbn
//	go run ./tools/editor-prototype -tbn build/release-unstripped.tbn
//	go run ./tools/editor-prototype -tbn build/release-unstripped.tbn -geometry mode4-8x8
//	go run ./tools/editor-prototype -mockup -tbn build/release-unstripped.tbn -o build/mockups/
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/fonts"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/viewer"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("editor-prototype: ")

	tbnPath := flag.String("tbn", "", "path to the compact `.tbn` to view (required)")
	geomName := flag.String("geometry", samscreen.DefaultGeometry, "screen geometry: "+joinGeoms())
	mockup := flag.Bool("mockup", false, "generate mockup sheets (PNG + SCREEN$) for all geometries instead of the interactive viewer")
	outDir := flag.String("o", "build/mockups/", "output directory for -mockup / -frames")
	frames := flag.Int("frames", 0, "non-interactive smoke: render N viewer frames to PNG (scrolls one page between frames) and exit")
	crt := flag.Bool("crt", false, "mockup: apply the scanline-CRT darkening variant")
	fontDir := flag.String("font-dir", "tools/editor-prototype/fonts", "directory holding vendored 6-px fonts (font6x8.sam / font6x6.sam)")
	flag.Parse()

	if *tbnPath == "" {
		flag.Usage()
		log.Fatal("a -tbn path is required")
	}
	data, err := os.ReadFile(*tbnPath)
	if err != nil {
		log.Fatalf("read tbn: %v", err)
	}
	doc, err := viewer.LoadDocument(*tbnPath, data)
	if err != nil {
		log.Fatalf("load: %v", err)
	}

	switch {
	case *mockup:
		if err := runMockupSheets(doc, *outDir, *fontDir, *crt); err != nil {
			log.Fatalf("mockup: %v", err)
		}
	case *frames > 0:
		geom, err := samscreen.LookupGeometry(*geomName)
		if err != nil {
			log.Fatal(err)
		}
		if err := runFramesSmoke(doc, geom, *fontDir, *outDir, *frames); err != nil {
			log.Fatalf("frames: %v", err)
		}
	default:
		geom, err := samscreen.LookupGeometry(*geomName)
		if err != nil {
			log.Fatal(err)
		}
		if err := runInteractive(doc, geom); err != nil {
			log.Fatalf("interactive: %v", err)
		}
	}
}

func joinGeoms() string {
	names := samscreen.GeometryNames()
	out := ""
	for i, n := range names {
		if i > 0 {
			out += " | "
		}
		out += n
	}
	return out
}

// loadFont returns the font for geom and whether it is a real glyph font
// (false → placeholder, 6-px pending vendor).
func loadFont(geom samscreen.Geometry, fontDir string) (samscreen.Font, bool, string) {
	f, err := fonts.FontFor(geom.Font, fontDir)
	if err != nil {
		if _, ok := err.(*fonts.ErrFontNotVendored); ok {
			return nil, false, fmt.Sprintf("%s: font pending P1b font-proof", geom.Name)
		}
		log.Fatalf("font for %s: %v", geom.Name, err)
	}
	return f, true, ""
}
