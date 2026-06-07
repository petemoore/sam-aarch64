package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/terminal"
	"github.com/petemoore/sam-aarch64/tools/editor-prototype/viewer"
)

// lab_interactive.go is the live lab: an interactive terminal viewer at the
// locked MODE 3 64x24 geometry over the real .tbn, where every rendering
// feature is a live toggle/parameter. Editor-faithful keys (cursor up/down,
// page up/down) stay SAM-expressible; lab keys (the feature toggles, snapshot)
// are clearly lab-only and may be anything. S snapshots the current effective
// settings to a relaunchable config file. ? shows the help overlay.

// labState is the live lab's mutable state.
type labState struct {
	doc    *viewer.Document
	m      []srcLine
	dl     *docLines
	cfg    LabConfig
	cursor int
	offset int // horizontal viewport shift (cells)
	snaps  int // snapshots taken this session
	help   bool
}

// runLab runs the interactive configurable lab until ESC / q.
func runLab(doc *viewer.Document, cfg LabConfig) error {
	geom, err := samscreen.LookupGeometry(labGeometry)
	if err != nil {
		return err
	}
	// The live lab is an off-SAM dreaming instrument: its grid lifts the colour
	// ceiling so the relax_palette flag can flip live (p) without a relaunch.
	// A bounded config still paints only pens 0-3, so it looks identical; the
	// SAM-faithfulness gate lives on the PNG-export path (bounded grid + a
	// relax_palette refusal), not here.
	scr, err := terminal.NewWithPalette(geom, cfg.palette(), true)
	if err != nil {
		return err
	}
	defer scr.Close()

	m, dl := labModelFor(doc)
	st := &labState{doc: doc, m: m, dl: dl, cfg: cfg, cursor: clampCursor(m, 0)}

	for {
		r := newLabRenderer(st.cfg, st.m, st.dl)
		r.renderScreen(scr, st.cursor, st.offset, statusFor(doc, st.m, st.cursor))
		if st.help {
			drawHelpOverlay(scr)
		}
		scr.Flush()

		k := scr.PollKey()
		if quit := st.handleKey(k); quit {
			return nil
		}
	}
}

// handleKey applies one key event to the live state; returns true to quit.
func (st *labState) handleKey(k samscreen.Key) bool {
	// Editor-faithful navigation (SAM-expressible).
	switch k.Kind {
	case samscreen.KeyEsc:
		if st.help {
			st.help = false
			return false
		}
		return true
	case samscreen.KeyUp:
		st.moveCursor(-1)
		return false
	case samscreen.KeyDown:
		st.moveCursor(1)
		return false
	case samscreen.KeyLeft:
		if !st.cfg.Wrap && st.offset > 0 {
			st.offset -= st.cfg.ViewportStep
			if st.offset < 0 {
				st.offset = 0
			}
		}
		return false
	case samscreen.KeyRight:
		if !st.cfg.Wrap {
			st.offset += st.cfg.ViewportStep
		}
		return false
	case samscreen.KeyFunc:
		switch k.Fn {
		case 4: // PgUp
			st.moveCursor(-22)
		case 5: // PgDn
			st.moveCursor(22)
		}
		return false
	}

	if k.Kind != samscreen.KeyPrintable {
		return false
	}
	// Lab keys (lab-only; may be any key).
	switch k.Rune {
	case 'q', 'Q':
		return true
	case '?':
		st.help = !st.help
	case 'S':
		st.snapshot()

	// Feature toggles.
	case 'm': // mnemonic column: adaptive <-> fixed:10
		if st.cfg.MnemonicAdaptive {
			st.cfg.MnemonicAdaptive = false
			if st.cfg.MnemonicFixed == 0 {
				st.cfg.MnemonicFixed = 10
			}
		} else if st.cfg.MnemonicFixed == 0 {
			st.cfg.MnemonicAdaptive = true
		} else {
			st.cfg.MnemonicFixed = 0 // -> R0 (one space)
		}
	case 'h': // imm_hex_amp
		st.cfg.ImmHexAmp = !st.cfg.ImmHexAmp
	case 'd': // imm_drop_hash
		st.cfg.ImmDropHash = !st.cfg.ImmDropHash
	case 't': // tight_commas
		st.cfg.TightCommas = !st.cfg.TightCommas
	case 'r': // reg_style off <-> strip
		st.cfg.RegStrip = !st.cfg.RegStrip
	case 'x': // cycle reg_x_style
		st.cfg.RegXStyle = cycleMark(st.cfg.RegXStyle)
	case 'X': // cycle reg_w_style
		st.cfg.RegWStyle = cycleMark(st.cfg.RegWStyle)
	case 'i': // mark_immediates
		st.cfg.MarkImmediates = !st.cfg.MarkImmediates
		if st.cfg.MarkImmediates && st.cfg.MarkImmediatesStyle == markNone {
			st.cfg.MarkImmediatesStyle = markBg
		}
	case 'n': // label_truncate N--
		if st.cfg.LabelTruncate > 0 {
			st.cfg.LabelTruncate--
		}
	case 'N': // label_truncate N++ (0 = off -> 8 to start)
		if st.cfg.LabelTruncate == 0 {
			st.cfg.LabelTruncate = 8
		} else {
			st.cfg.LabelTruncate++
		}
	case 'c': // cycle comment_layout
		st.cfg.CommentLayout = (st.cfg.CommentLayout + 1) % 3
	case 'C': // comment_column rule toggle
		st.cfg.CommentColumnCommentedOnly = !st.cfg.CommentColumnCommentedOnly
	case ';': // comment_semicolon
		st.cfg.CommentSemicolon = !st.cfg.CommentSemicolon
	case 'e': // cycle expand_cursor_line off -> wrap:3 -> panel:4 -> off
		st.cfg.ExpandCursorLine, st.cfg.ExpandK = cycleExpand(st.cfg.ExpandCursorLine, st.cfg.ExpandK)
	case 'w': // wrap toggle
		st.cfg.Wrap = !st.cfg.Wrap
		if st.cfg.Wrap {
			st.offset = 0
		}
	case 'l': // current_line_highlight
		st.cfg.CurrentLineHighlight = !st.cfg.CurrentLineHighlight
	case 'p': // relax_palette toggle (off-SAM dreaming) — live: the lab grid is
		// already off-SAM, so this flips the OFF-SAM PALETTE banner immediately.
		// (Roles beyond pens 0-3 come from the config file; edit + relaunch to
		// dream in those colours.)
		st.cfg.RelaxPalette = !st.cfg.RelaxPalette
	case 'E': // tighten_exprs
		st.cfg.TightenExprs = !st.cfg.TightenExprs
	}
	return false
}

func (st *labState) moveCursor(delta int) {
	st.cursor += delta
	if st.cursor < 0 {
		st.cursor = 0
	}
	if st.cursor >= len(st.m) {
		st.cursor = len(st.m) - 1
	}
}

func cycleMark(m markStyle) markStyle { return (m + 1) % 4 }

func cycleExpand(e expandMode, k int) (expandMode, int) {
	switch e {
	case expandOff:
		return expandWrap, 3
	case expandWrap:
		return expandPanel, 4
	default:
		return expandOff, 0
	}
}

// snapshot writes the current effective settings to lab-snapshot-<n>.config.
func (st *labState) snapshot() {
	st.snaps++
	name := fmt.Sprintf("lab-snapshot-%d.config", st.snaps)
	if err := os.WriteFile(name, []byte(st.cfg.Snapshot()), 0o644); err != nil {
		st.cfg.StatusMessage = "snapshot failed: " + err.Error()
		return
	}
	st.cfg.StatusMessage = "snapshot -> " + name
}

// helpLines is the in-app lab-keys cheat sheet (key ?).
var helpLines = []string{
	" LAB KEYS  (lab-only; editor keys are SAM-faithful)         ",
	"                                                            ",
	" arrows  cursor up/down, viewport shift (when wrap off)     ",
	" PgUp/Dn page    q/ESC quit    ?  toggle this help          ",
	"                                                            ",
	" m mnemonic column   h hex& imm    d drop # imm             ",
	" t tight commas      r reg strip   x/X cycle x/w mark       ",
	" i mark immediates   n/N label trunc -/+                    ",
	" c comment layout    C comment column rule  ; semicolon     ",
	" e expand cursor line  w wrap   l current-line band         ",
	" p relax palette     E tighten exprs                        ",
	"                                                            ",
	" S  snapshot current settings -> lab-snapshot-N.config      ",
	"                                                            ",
	"            ESC or ? to close                               ",
}

// drawHelpOverlay paints the cheat sheet centred over the current frame.
func drawHelpOverlay(scr samscreen.Screen) {
	cols, rows := scr.Size()
	w := 0
	for _, l := range helpLines {
		if len(l) > w {
			w = len(l)
		}
	}
	x0 := (cols - w) / 2
	if x0 < 0 {
		x0 = 0
	}
	y0 := (rows - len(helpLines)) / 2
	if y0 < 0 {
		y0 = 0
	}
	// Overlay drawn in the SAM quadruple (white on black) so it stays valid on
	// a bounded grid; off-SAM grids accept it too.
	for i, l := range helpLines {
		y := y0 + i
		if y >= rows {
			break
		}
		text := l
		if len(text) < w {
			text += strings.Repeat(" ", w-len(text))
		}
		for j := 0; j < len(text); j++ {
			x := x0 + j
			if x >= cols {
				break
			}
			ch := text[j]
			if ch < 0x20 || ch > 0x7e {
				ch = ' '
			}
			scr.Set(x, y, samscreen.Cell{Ch: ch, Ink: 0, Paper: 3})
		}
	}
}
