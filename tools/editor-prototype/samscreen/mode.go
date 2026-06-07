// Package samscreen is the load-bearing constraint of the editor prototype
// (spec §2): a narrow output/input abstraction a SAM port can implement.
// Everything the prototype renders and reads passes through it, so if the
// prototype ever reaches for a capability the SAM lacks, the type system stops
// it. The interactive terminal backend and the file-rendering mockup backend
// both implement the same Screen; the viewer is written against the interface
// alone and never touches a concrete backend.
//
// Deep doc: docs/specs/editor-tui-prototype-design.md (§2 the abstraction, §3
// the geometry matrix). SAM video facts cite the SAM Coupé Technical Manual
// v3.0 ("TM pN" = printed page N) at docs/sam/sam-coupe_tech-man_v3-0.txt.
package samscreen

import "fmt"

// Mode is a SAM screen mode. The prototype targets MODES 3 and 4 (the editor
// candidates, spec §3); MODES 1 and 2 are modelled for completeness but are
// not candidate editor modes.
type Mode int

const (
	// Mode4 — 256×192, 4 bits/pixel, 16 of 128 colours on screen (TM p15, p20).
	Mode4 Mode = 4
	// Mode3 — 512×192, 2 bits/pixel, 4 of 128 colours on screen; the extra two
	// CLUT-address bits come from HMPR bits 5–6 (TM p15, p17, p20). MODE 3
	// pixels are half-width: 512 pixels in the same active scan width as
	// MODE 4's 256.
	Mode3 Mode = 3
)

// String renders the mode token used in -geometry values.
func (m Mode) String() string {
	switch m {
	case Mode3:
		return "mode3"
	case Mode4:
		return "mode4"
	default:
		return fmt.Sprintf("mode%d", int(m))
	}
}

// PixelWidth is the mode's horizontal pixel resolution (TM p15).
func (m Mode) PixelWidth() int {
	switch m {
	case Mode3:
		return 512
	case Mode4:
		return 256
	default:
		panic(fmt.Sprintf("samscreen: unsupported mode %d", int(m)))
	}
}

// PixelHeight is the mode's vertical pixel resolution: 192 for every SAM
// graphics mode (TM p15).
func (m Mode) PixelHeight() int { return 192 }

// MaxColour is the highest valid ink/paper CLUT index in this mode: 15 for
// MODE 4 (4 bits/pixel), 3 for MODE 3 (2 bits/pixel, one CLUT quadruple at a
// time — TM p20). Passing a colour above this to Screen.Set is a constraint
// violation and panics (spec §2.1).
func (m Mode) MaxColour() uint8 {
	switch m {
	case Mode3:
		return 3
	case Mode4:
		return 15
	default:
		panic(fmt.Sprintf("samscreen: unsupported mode %d", int(m)))
	}
}

// ExportPixelScale is the (x, y) replication applied on PNG export so MODE 3
// and MODE 4 frames come out at the same true proportions (spec §2.4). MODE 4
// is 2×2 per pixel; MODE 3's half-width pixels are 1×2. Both yield a 512×384
// PNG with correct aspect.
func (m Mode) ExportPixelScale() (sx, sy int) {
	switch m {
	case Mode3:
		return 1, 2
	case Mode4:
		return 2, 2
	default:
		panic(fmt.Sprintf("samscreen: unsupported mode %d", int(m)))
	}
}
