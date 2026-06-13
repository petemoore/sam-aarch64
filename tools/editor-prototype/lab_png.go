package main

import (
	"fmt"
	"image"
	"path/filepath"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/fonts"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/mockup"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/viewer"
)

// lab_png.go is the zero-translation path from "combo I like" to "how it looks
// on a SAM": it renders the current lab screen (or the screen at a given line)
// through the mockup backend with the config's true 4-colour palette. A relaxed
// (off-SAM) config is refused — it cannot map to a SAM CLUT quadruple.

// labGeometry is the locked editor geometry: MODE 3, 64x24, 8x8. The lab does
// not vary geometry — it varies rendering features over the fixed geometry.
const labGeometry = "mode3-8x8"

// labModelFor parses a document into the srcLine model and a docLines accessor
// the renderer reads comment bodies through.
func labModelFor(doc *viewer.Document) ([]srcLine, *docLines) {
	m := parseDesignDoc(doc)
	dl := &docLines{text: func(i int) string {
		if i < 0 || i >= len(doc.Lines) {
			return ""
		}
		return doc.Lines[i].Text
	}}
	return m, dl
}

// runLabPNG renders the config's screen at document line samLine (clamped to a
// code line) and writes it to outPath as a SAM-faithful PNG. relaxed configs
// are refused with a helpful error.
func runLabPNG(doc *viewer.Document, cfg LabConfig, outPath string, samLine int) error {
	if cfg.RelaxPalette {
		return fmt.Errorf("this config sets relax_palette = true: it dreams beyond the SAM's 4-colour CLUT, so it cannot render a SAM PNG. Constrain the palette (relax_palette = off, roles 0-3) first")
	}
	geom, err := samscreen.LookupGeometry(labGeometry)
	if err != nil {
		return err
	}
	font, err := labFont()
	if err != nil {
		return err
	}

	m, dl := labModelFor(doc)
	cursor := clampCursor(m, samLine)
	r := newLabRenderer(cfg, m, dl)

	scr := mockup.New(geom, font, mockup.WithPalette(cfg.palette()))
	r.renderScreen(scr, cursor, 0, statusFor(doc, m, cursor))
	if err := scr.WritePNGFile(outPath); err != nil {
		return err
	}
	b := scr.Image().Bounds()
	fmt.Printf("%s (%dx%d) line %d/%d\n", outPath, b.Dx(), b.Dy(), cursor+1, len(m))
	return nil
}

// labFont returns the 8x8 vendored font with the lab's custom glyphs injected
// (ellipsis, viewport arrow, comment marker, AND wedge).
func labFont() (samscreen.Font, error) {
	base, err := fonts.Font8x8()
	if err != nil {
		return nil, err
	}
	return withGlyphOverrides(base, labGlyphs)
}

// statusFor builds the status fields for the document at cursor.
func statusFor(doc *viewer.Document, m []srcLine, cursor int) labStatus {
	return labStatus{
		file:  filepath.Base(doc.Path),
		line:  cursor + 1,
		total: len(m),
		mode:  "MODE 3 64x24",
	}
}

// clampCursor clamps an index into the model and skips forward to a code line
// so the screen opens on something meaningful.
func clampCursor(m []srcLine, i int) int {
	if i < 0 {
		i = 0
	}
	if i >= len(m) {
		i = len(m) - 1
	}
	for i < len(m) && !m[i].isCode() {
		i++
	}
	if i >= len(m) {
		i = len(m) - 1
	}
	return i
}

// labFrameImage renders one lab frame to an image (test/smoke helper, no file).
func labFrameImage(doc *viewer.Document, cfg LabConfig, cursor, offset int) (*image.RGBA, error) {
	geom, err := samscreen.LookupGeometry(labGeometry)
	if err != nil {
		return nil, err
	}
	font, err := labFont()
	if err != nil {
		return nil, err
	}
	m, dl := labModelFor(doc)
	r := newLabRenderer(cfg, m, dl)
	scr := mockup.New(geom, font, mockup.WithPalette(cfg.palette()))
	r.renderScreen(scr, clampCursor(m, cursor), offset, statusFor(doc, m, clampCursor(m, cursor)))
	return scr.Image(), nil
}
