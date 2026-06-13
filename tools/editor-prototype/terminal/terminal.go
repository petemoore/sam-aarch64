// Package terminal is the interactive backend (spec §2.3, recommendation R1):
// gdamore/tcell driving a fixed cell grid that honours the samscreen
// abstraction strictly. tcell's native model is an addressable cell grid, so
// the adapter is thin and nothing tempts the prototype toward flow layout. The
// backend maps host keys onto the SAM key model and discards anything the SAM
// cannot express (spec §2.2).
package terminal

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
)

// Screen is the tcell-backed interactive backend. It satisfies
// samscreen.Screen. The terminal window being larger than the SAM grid leaves
// a border (spec §2.1: the grid never resizes); a smaller window clips, which
// the prototype surfaces rather than reflowing.
type Screen struct {
	*samscreen.Grid
	ts  tcell.Screen
	pal samscreen.Palette
}

// New initialises a tcell screen for geom. Call Close when done.
func New(geom samscreen.Geometry) (*Screen, error) {
	return newScreen(geom, samscreen.DefaultPalette(), false)
}

// NewWithPalette initialises a tcell screen for geom with a chosen CLUT. When
// offSAM is true the grid lifts the mode's colour ceiling (the lab's
// relax_palette dreaming mode); otherwise the SAM quadruple constraint stands.
func NewWithPalette(geom samscreen.Geometry, pal samscreen.Palette, offSAM bool) (*Screen, error) {
	return newScreen(geom, pal, offSAM)
}

func newScreen(geom samscreen.Geometry, pal samscreen.Palette, offSAM bool) (*Screen, error) {
	ts, err := tcell.NewScreen()
	if err != nil {
		return nil, fmt.Errorf("tcell.NewScreen: %w", err)
	}
	if err := ts.Init(); err != nil {
		return nil, fmt.Errorf("tcell init: %w", err)
	}
	ts.SetStyle(tcell.StyleDefault)
	ts.Clear()
	grid := samscreen.NewGrid(geom)
	if offSAM {
		grid = samscreen.NewOffSAMGrid(geom)
	}
	return &Screen{
		Grid: grid,
		ts:   ts,
		pal:  pal,
	}, nil
}

// Close restores the terminal.
func (s *Screen) Close() { s.ts.Fini() }

// Flush paints the grid into tcell and presents it. Colours are mapped from the
// SAM CLUT to tcell's RGB (the terminal can show 24-bit, but we feed it only
// the SAM palette's sRGB values, so no colour outside the mode's CLUT ever
// appears — the abstraction's colours stay faithful even though the medium
// could show more).
func (s *Screen) Flush() {
	cols, rows := s.Size()
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			cell := s.At(x, y)
			ink := s.pal.RGBA(cell.Ink)
			paper := s.pal.RGBA(cell.Paper)
			st := tcell.StyleDefault.
				Foreground(tcell.NewRGBColor(int32(ink.R), int32(ink.G), int32(ink.B))).
				Background(tcell.NewRGBColor(int32(paper.R), int32(paper.G), int32(paper.B)))
			r := rune(cell.Ch)
			if cell.Ch < 0x20 || cell.Ch >= 0x7f {
				r = ' '
			}
			s.ts.SetContent(x, y, r, nil, st)
		}
	}
	s.ts.Show()
}

// PollKey blocks for the next key event mapped onto the SAM key model, or
// returns KeyNone for a resize/other event the caller should ignore. Events the
// SAM cannot express are discarded (returned as KeyNone), per spec §2.2.
func (s *Screen) PollKey() samscreen.Key {
	ev := s.ts.PollEvent()
	switch ev := ev.(type) {
	case *tcell.EventResize:
		s.ts.Sync()
		return samscreen.Key{Kind: samscreen.KeyNone}
	case *tcell.EventKey:
		return mapKey(ev)
	default:
		return samscreen.Key{Kind: samscreen.KeyNone}
	}
}

// mapKey translates a tcell key event to the SAM key model (spec §2.2). Host
// F1–F10 map to SAM F0–F9; chords the SAM lacks (e.g. Ctrl+arrow) are
// discarded.
func mapKey(ev *tcell.EventKey) samscreen.Key {
	mods := mapMods(ev.Modifiers())
	switch ev.Key() {
	case tcell.KeyRune:
		r := ev.Rune()
		if r >= 0x20 && r < 0x7f {
			return samscreen.Key{Kind: samscreen.KeyPrintable, Rune: byte(r), Mods: mods}
		}
		return samscreen.Key{Kind: samscreen.KeyNone}
	case tcell.KeyEnter:
		return samscreen.Key{Kind: samscreen.KeyReturn, Mods: mods}
	case tcell.KeyEscape:
		return samscreen.Key{Kind: samscreen.KeyEsc, Mods: mods}
	case tcell.KeyUp:
		return samscreen.Key{Kind: samscreen.KeyUp, Mods: mods}
	case tcell.KeyDown:
		return samscreen.Key{Kind: samscreen.KeyDown, Mods: mods}
	case tcell.KeyLeft:
		return samscreen.Key{Kind: samscreen.KeyLeft, Mods: mods}
	case tcell.KeyRight:
		return samscreen.Key{Kind: samscreen.KeyRight, Mods: mods}
	case tcell.KeyDelete, tcell.KeyBackspace, tcell.KeyBackspace2:
		return samscreen.Key{Kind: samscreen.KeyDelete, Mods: mods}
	case tcell.KeyTab:
		return samscreen.Key{Kind: samscreen.KeyTab, Mods: mods}
	case tcell.KeyHome:
		// Host Home → SAM EDIT key (no dedicated host EDIT key).
		return samscreen.Key{Kind: samscreen.KeyEdit, Mods: mods}
	case tcell.KeyPgUp:
		return samscreen.Key{Kind: samscreen.KeyFunc, Fn: 4, Mods: mods}
	case tcell.KeyPgDn:
		return samscreen.Key{Kind: samscreen.KeyFunc, Fn: 5, Mods: mods}
	}
	if ev.Key() >= tcell.KeyF1 && ev.Key() <= tcell.KeyF10 {
		return samscreen.Key{Kind: samscreen.KeyFunc, Fn: int(ev.Key()-tcell.KeyF1) + 0, Mods: mods}
	}
	return samscreen.Key{Kind: samscreen.KeyNone}
}

// mapMods keeps only the SAM-expressible modifiers (SHIFT/CTRL; ALT maps to
// SYMBOL, the SAM's second symbol-shift key). Other host modifiers are dropped.
func mapMods(m tcell.ModMask) samscreen.Mods {
	var out samscreen.Mods
	if m&tcell.ModShift != 0 {
		out |= samscreen.ModShift
	}
	if m&tcell.ModCtrl != 0 {
		out |= samscreen.ModCntrl
	}
	if m&tcell.ModAlt != 0 {
		out |= samscreen.ModSymbol
	}
	return out
}
