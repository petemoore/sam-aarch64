package main

import (
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/terminal"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/viewer"
)

// runInteractive runs the tcell terminal viewer until ESC / q (spec §5 P1).
func runInteractive(doc *viewer.Document, geom samscreen.Geometry) error {
	scr, err := terminal.New(geom)
	if err != nil {
		return err
	}
	defer scr.Close()

	v := viewer.NewView(doc)
	for {
		v.Render(scr)
		scr.Flush()

		k := scr.PollKey()
		switch k.Kind {
		case samscreen.KeyEsc:
			return nil
		case samscreen.KeyUp:
			v.Move(-1)
		case samscreen.KeyDown:
			v.Move(1)
		case samscreen.KeyFunc:
			switch k.Fn {
			case 4: // PgUp → F4 (terminal mapping)
				v.PageUp(scr)
			case 5: // PgDn → F5
				v.PageDown(scr)
			}
		case samscreen.KeyPrintable:
			switch k.Rune {
			case 'q', 'Q':
				return nil
			case 'w', 'W':
				v.Wrap = !v.Wrap
			case 'g':
				v.Top()
			case 'G':
				v.Bottom()
			case ' ':
				v.PageDown(scr)
			case 'b':
				v.PageUp(scr)
			}
		}
	}
}
