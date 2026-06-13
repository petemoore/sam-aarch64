package viewer

import (
	"fmt"
	"path/filepath"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
)

// Colours (CLUT indices). MODE 3 has only 0–3 on screen, so the viewer uses a
// four-colour scheme that is valid in every candidate geometry (spec §8 R2:
// "MODE 3's 4 colours suffice for an editor — text/comment/status/error").
const (
	colPaper   = 0 // background
	colText    = 3 // code text (white-ish in the default CLUT's first quad)
	colComment = 1 // comments (blue)
	colStatus  = 2 // status line (red)
)

// View is the read-only viewer's scroll state over a Document, rendered against
// any samscreen.Screen. The cursor is centre-locked (spec §5 / i4: the cursor
// line stays at the screen's vertical centre while the document scrolls under
// it), clamped at the document ends.
type View struct {
	Doc    *Document
	Wrap   bool // wrap vs truncate long lines (toggle, spec §5)
	cursor int  // current source line index (0-based)
}

// NewView builds a viewer over doc, wrapping enabled by default (spec §8:
// wrapping is mandatory core UX at every geometry).
func NewView(doc *Document) *View {
	return &View{Doc: doc, Wrap: true}
}

// textRows is the number of grid rows available for document text (all rows
// except the bottom status line).
func textRows(s samscreen.Screen) int {
	_, h := s.Size()
	return h - 1
}

// Render paints the current view onto s: the document text window with the
// centre-locked cursor highlighted, then the status line. It clears first, so
// it is safe to call every frame.
func (v *View) Render(s samscreen.Screen) {
	s.Clear(samscreen.Cell{Ch: ' ', Ink: colText, Paper: colPaper})
	cols, _ := s.Size()
	rows := textRows(s)
	if rows < 1 {
		return
	}

	// Build the display rows for the window: walk source lines from the top of
	// the window, expanding each to its wrapped display rows. The window's top
	// source line is chosen so the cursor's first display row sits at the
	// vertical centre (centre-locked cursor).
	centre := rows / 2

	// Expand every source line once is too slow for 9k lines per frame at
	// scale, so expand only the lines needed to fill the window around the
	// cursor. Walk up from the cursor to fill the rows above centre, then down.
	above := v.rowsAbove(cols, centre)
	winTop := v.cursor - above // first source line shown
	if winTop < 0 {
		winTop = 0
	}

	// Now emit display rows from winTop downward until the window is full.
	row := 0
	for src := winTop; src < v.Doc.LineCount() && row < rows; src++ {
		drs := wrapLine(src, v.Doc.Lines[src].Text, cols, v.Wrap)
		for _, dr := range drs {
			if row >= rows {
				break
			}
			v.paintRow(s, row, dr, src == v.cursor, cols)
			row++
		}
		// In truncate mode, mark an overflowing line.
		if !v.Wrap && truncated(v.Doc.Lines[src].Text, cols) && row-1 >= 0 {
			s.Set(cols-1, row-1, samscreen.Cell{Ch: '>', Ink: colStatus, Paper: colPaper})
		}
	}

	v.renderStatus(s)
}

// rowsAbove returns how many source lines strictly above the cursor are needed
// to fill `want` display rows (so the cursor's first display row lands at the
// vertical centre). Near the top of the document it returns fewer, pinning the
// window to line 0.
func (v *View) rowsAbove(cols, want int) int {
	srcCount := 0
	rowsAcc := 0
	for src := v.cursor - 1; src >= 0 && rowsAcc < want; src-- {
		rowsAcc += len(wrapLine(src, v.Doc.Lines[src].Text, cols, v.Wrap))
		srcCount++
	}
	return srcCount
}

func (v *View) paintRow(s samscreen.Screen, row int, dr dispRow, isCursor bool, cols int) {
	ink := colText
	if isComment(dr.text) {
		ink = colComment
	}
	paper := colPaper
	if isCursor {
		// Cursor row drawn in reverse video (the cursor is a cell we draw
		// ourselves in reverse ink/paper — spec §2.1).
		ink, paper = paper, ink
		if ink == colPaper {
			ink = colText
		}
	}
	for i := 0; i < cols; i++ {
		ch := byte(' ')
		if i < len(dr.text) {
			ch = dr.text[i]
		}
		if !isPrintable(ch) {
			ch = ' '
		}
		s.Set(i, row, samscreen.Cell{Ch: ch, Ink: uint8(ink), Paper: uint8(paper)})
	}
	if !dr.first {
		// Continuation marker on wrapped rows.
		s.Set(0, row, samscreen.Cell{Ch: '\\', Ink: uint8(colComment), Paper: uint8(paper)})
	}
}

func (v *View) renderStatus(s samscreen.Screen) {
	cols, h := s.Size()
	statusY := h - 1
	geom := s.Geometry()
	mode := "trunc"
	if v.Wrap {
		mode = "wrap"
	}
	// file · record kind · line/total · geometry (spec §5).
	name := filepath.Base(v.Doc.Path)
	status := fmt.Sprintf(" %s · %s · %d/%d · %s · %s",
		name, v.Doc.RecordKindAt(v.cursor), v.cursor+1, v.Doc.LineCount(), geom.Name, mode)
	for i := 0; i < cols; i++ {
		ch := byte(' ')
		if i < len(status) {
			ch = status[i]
		}
		if !isPrintable(ch) {
			ch = ' '
		}
		s.Set(i, statusY, samscreen.Cell{Ch: ch, Ink: uint8(colPaper), Paper: uint8(colStatus)})
	}
}

// Cursor returns the current source line index (0-based).
func (v *View) Cursor() int { return v.cursor }

// SetCursor clamps and sets the cursor source line.
func (v *View) SetCursor(i int) {
	if i < 0 {
		i = 0
	}
	if i >= v.Doc.LineCount() {
		i = v.Doc.LineCount() - 1
	}
	v.cursor = i
}

// Move shifts the cursor by delta source lines (clamped).
func (v *View) Move(delta int) { v.SetCursor(v.cursor + delta) }

// Top / Bottom jump the cursor to the document ends.
func (v *View) Top()    { v.SetCursor(0) }
func (v *View) Bottom() { v.SetCursor(v.Doc.LineCount() - 1) }

// PageDown / PageUp move the cursor by one screenful of source lines (spec §5:
// page up/down). The page size is the text-row count; with wrapping a screenful
// of rows holds fewer source lines, but moving by row-count source lines is a
// predictable, document-position-stable page step.
func (v *View) PageDown(s samscreen.Screen) { v.Move(textRows(s)) }
func (v *View) PageUp(s samscreen.Screen)   { v.Move(-textRows(s)) }

func isComment(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			continue
		}
		return i+1 < len(s) && s[i] == '/' && s[i+1] == '/'
	}
	return false
}

func isPrintable(ch byte) bool { return ch >= 0x20 && ch < 0x7f }
