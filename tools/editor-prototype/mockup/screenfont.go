package mockup

import (
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/fonts"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
)

// placeholderFont is the always-available 8×8 vendored font, used to draw the
// note on a font-less (6-px, pending-vendor) sheet.
func placeholderFont() (samscreen.Font, error) { return fonts.Font8x8() }
