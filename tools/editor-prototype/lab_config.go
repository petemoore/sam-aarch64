// The configurable rendering lab (i76 P2): every rendering feature the editor
// design discussion has surfaced is a key in one hand-editable config file, so
// Pete can launch combinations, flip them live, and converge on the perfect
// combination — which then renders as a SAM-faithful PNG from the same config.
//
// This file is the config model: a tiny `key = value` parser (`#` comments,
// zero new deps), the LabConfig struct it fills, role→palette resolution with
// a clear startup error listing every offender, and snapshot serialisation (the
// live lab writes the current effective settings back out as a relaunchable
// config). The rendering rules themselves are the iteration-2 ladder
// (run_iter2.go) — this layer parameterises them, it does not reinvent them.
package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/petemoore/sam-aarch64/tools/editor-prototype/samscreen"
)

// markStyle is how a token class (a register class, or the immediate class) is
// visually marked. The width never changes — marking is colour only.
type markStyle int

const (
	markNone markStyle = iota // no marking (the token paints in the code pen)
	markFg                    // role colour as ink (foreground tint)
	markBg                    // role colour as paper (background block)
	markInverse               // inverse video: code pen as paper, paper as ink
)

func (m markStyle) String() string {
	switch m {
	case markFg:
		return "fg"
	case markBg:
		return "bg"
	case markInverse:
		return "inverse"
	default:
		return "none"
	}
}

func parseMarkStyle(s string) (markStyle, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "off", "":
		return markNone, nil
	case "fg":
		return markFg, nil
	case "bg":
		return markBg, nil
	case "inverse":
		return markInverse, nil
	}
	return markNone, fmt.Errorf("want none|fg|bg|inverse, got %q", s)
}

// expandMode is the cursor-line expansion behaviour (Pete's reading-flow ==
// cursor-flow feature).
type expandMode int

const (
	expandOff   expandMode = iota // no expansion
	expandWrap                    // the cursor line unfolds to <=K rows inline (page below shifts)
	expandPanel                   // K reserved bottom rows always show the cursor line's full comment (no reflow)
)

func (e expandMode) String() string {
	switch e {
	case expandWrap:
		return "wrap"
	case expandPanel:
		return "panel"
	default:
		return "off"
	}
}
// renderConstantBase controls how numeric constants in operands are displayed.
type renderConstantBase int

const (
	rcSource renderConstantBase = iota // keep the source spelling (default)
	rcHex                              // force hex (SAM &FFFF when imm_hex_amp; 0x otherwise)
	rcDec                              // force decimal
)

func (r renderConstantBase) String() string {
	switch r {
	case rcHex:
		return "hex"
	case rcDec:
		return "dec"
	default:
		return "source"
	}
}

func parseRenderConstants(s string) (renderConstantBase, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "source", "":
		return rcSource, nil
	case "hex":
		return rcHex, nil
	case "dec":
		return rcDec, nil
	}
	return rcSource, fmt.Errorf("want source|hex|dec, got %q", s)
}

// commentLayout is where a same-line comment is drawn relative to its code.
type commentLayout int

const (
	clSameline   commentLayout = iota // comment on the same row, at the comment column
	clAbove                           // comment on its own row above the code line
	clMarkerOnly                      // no comment text inline; a gutter marker flags it
)

func (c commentLayout) String() string {
	switch c {
	case clAbove:
		return "above"
	case clMarkerOnly:
		return "marker-only"
	default:
		return "sameline"
	}
}

// LabConfig is the complete, hand-editable rendering configuration. Every field
// maps to one documented key in the starter configs. Zero/false defaults give
// the binutils baseline (R0); the compressed profile turns rules on.
type LabConfig struct {
	// --- Palette -----------------------------------------------------------
	// CLUT holds the four MODE 3 register values (0..127) the code rows share:
	// the one CLUT quadruple. Roles below resolve to one of these four pens.
	CLUT [4]uint8
	// Roles map a render role to a pen index 0..3 (an index into CLUT). They
	// must resolve to the four entries; a violation is a startup error.
	RolePaper      int
	RoleCode       int
	RoleComment    int
	RoleLabel      int
	RoleRegMark    int
	RoleImmMark    int
	RoleChromeFg   int
	RoleChromeBg   int
	RoleCurrentBg  int // the current-line highlight band
	RoleStatus     int // status-text pen (drawn on chrome bg)
	// RelaxPalette lifts the 4-colour constraint: unlimited terminal colours,
	// the status bar permanently shows OFF-SAM PALETTE. For dreaming first,
	// constraining after. A relaxed config cannot render a SAM PNG.
	RelaxPalette bool

	// --- Mnemonic column ---------------------------------------------------
	// MnemonicColumn is "adaptive" (per-page longest mnemonic + 1) or a fixed
	// width N. MnemonicFixed holds N when MnemonicAdaptive is false.
	MnemonicAdaptive bool
	MnemonicFixed    int

	// --- Immediates --------------------------------------------------------
	ImmHexAmp   bool // #0x1F -> &1F
	ImmDropHash bool // decimal #16 -> 16 (the '#' dropped)

	// --- Separators --------------------------------------------------------
	TightCommas bool // ", " -> ","

	// --- Registers ---------------------------------------------------------
	// RegStyle is "off" (x23 stays x23) or "strip" (x23 -> 23, colour carries
	// the register-ness). Per-class marking applies only when stripping.
	RegStrip      bool
	RegXStyle     markStyle // xN register cells
	RegWStyle     markStyle // wN register cells
	RegNamedStyle markStyle // sp/lr/fp/xzr/wzr/wsp

	// MarkImmediates is the iteration-2 insight as a first-class alternative:
	// mark the rarer immediate class instead of flooding every register cell.
	// The report found a yellow register flood swamps dense pages.
	MarkImmediates      bool
	MarkImmediatesStyle markStyle

	// --- Labels ------------------------------------------------------------
	// LabelTruncate is 0 (off) or N: branch/adr label operands longer than N
	// truncate to N chars; LabelEllipsis adds the ellipsis glyph when they do.
	LabelTruncate int
	LabelEllipsis bool

	// --- Comments ----------------------------------------------------------
	CommentLayout    commentLayout
	CommentGap       int  // spaces between code and a sameline comment
	CommentSemicolon bool // prefix the comment text with ';' (era convention)
	// CommentColumnCommentedOnly chooses the column rule: false = the column
	// clears ALL code on the page; true = it clears only comment-carrying code
	// (the report's free +6-points refinement — a wide uncommented line runs
	// past the column harmlessly).
	CommentColumnCommentedOnly bool

	// --- Cursor-line expansion --------------------------------------------
	ExpandCursorLine expandMode
	ExpandK          int // the K in wrap:K / panel:K

	// --- Viewport ----------------------------------------------------------
	Wrap         bool // wrap vs horizontal-shift when a line overflows
	ViewportStep int  // columns per horizontal shift when Wrap is off

	// --- Chrome ------------------------------------------------------------
	CurrentLineHighlight bool   // full-width current-line band (Prometheus-style)
	StatusTemplate       string // status-line content template (see status fields)
	StatusMessage        string // optional second message line (the TMP two-line pattern)

	// --- R6 expression tightening -----------------------------------------
	TightenExprs bool // collapse spaces in parenthesised expressions

	// --- Max instruction width --------------------------------------------
	// MaxInstructionWidth is 0 (off) or N: when a rendered code line exceeds N
	// columns and it is NOT the cursor line, the line is truncated at N−1
	// columns and the existing ellipsis glyph is appended. The cursor line
	// always shows in full — if the full line plus its comment exceeds the
	// screen, the existing expand_cursor_line machinery (wrap:K / panel:K)
	// provides the overflow. Same-line comments are NOT shown on a truncated
	// row (they would float against a fake column); they remain reachable via
	// the cursor-expansion and F7 paths. Width computation for the
	// page-adaptive mnemonic/comment columns uses post-truncation widths so
	// truncated lines do not drag the comment column right.
	//
	// The FORMAT never imposes line-length limits — anything binutils accepts
	// must round-trip cleanly; overflow is a display concern (collapse/expand).
	// Hard format limits are an absolute last resort, effectively never.
	// (Pete, 2026-06-12)
	MaxInstructionWidth int

	// --- Constant base rendering ------------------------------------------
	// RenderConstants controls how numeric constants in operands are displayed.
	// "source" (default) keeps the source spelling, which is honest because the
	// v2 format carries editor-region base hints; "hex" forces SAM-style &FFFF
	// (or 0x when imm_hex_amp is off); "dec" forces decimal. Applied at the
	// expression-rendering layer; imm_hex_amp / imm_drop_hash compose with it.
	RenderConstants renderConstantBase
}

// defaultConfig is the binutils baseline (R0, everything off): the starting
// point a config file edits up from. The CLUT defaults to the iteration-2
// quadruple so a minimal config still has a sane palette.
func defaultConfig() LabConfig {
	return LabConfig{
		CLUT:          [4]uint8{0, 15, 102, 127}, // paper / comment-grey / yellow / white
		RolePaper:     0,
		RoleCode:      3,
		RoleComment:   1,
		RoleLabel:     3,
		RoleRegMark:   2,
		RoleImmMark:   2,
		RoleChromeFg:  0,
		RoleChromeBg:  3,
		RoleCurrentBg: 1,
		RoleStatus:    0,
		MnemonicFixed: 0, // R0: a single space after the mnemonic
		CommentGap:    2,
		CommentLayout: clSameline,
		LabelEllipsis: true,
		Wrap:          true,
		ViewportStep:  8,
		StatusTemplate: "{file}  L{line}/{total}  {pct}  {mode}",
	}
}

// --- the parser -----------------------------------------------------------

// parseLabConfigFile reads a config file: lines of `key = value`, `#` comments,
// blank lines ignored. Unknown keys and bad values are collected and reported
// together so one run surfaces every problem.
func parseLabConfigFile(path string) (LabConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return LabConfig{}, err
	}
	defer f.Close()
	c := defaultConfig()
	var errs []string
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Text()
		if i := strings.IndexByte(raw, '#'); i >= 0 {
			raw = raw[:i]
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		eq := strings.IndexByte(raw, '=')
		if eq < 0 {
			errs = append(errs, fmt.Sprintf("line %d: no '=' in %q", line, raw))
			continue
		}
		key := strings.TrimSpace(raw[:eq])
		val := strings.TrimSpace(raw[eq+1:])
		if err := c.set(key, val); err != nil {
			errs = append(errs, fmt.Sprintf("line %d: %s = %s: %v", line, key, val, err))
		}
	}
	if err := sc.Err(); err != nil {
		return LabConfig{}, err
	}
	if len(errs) > 0 {
		return LabConfig{}, fmt.Errorf("config %s has %d problem(s):\n  %s", path, len(errs), strings.Join(errs, "\n  "))
	}
	if err := c.resolveRoles(); err != nil {
		return LabConfig{}, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("want on|off, got %q", v)
}

func parseInt(v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("want an integer, got %q", v)
	}
	return n, nil
}

// set applies one key=value to the config, returning a per-key error message.
func (c *LabConfig) set(key, val string) error {
	b := func() (bool, error) { return parseBool(val) }
	i := func() (int, error) { return parseInt(val) }
	ms := func() (markStyle, error) { return parseMarkStyle(val) }

	switch key {
	// Palette.
	case "clut":
		fields := strings.Fields(strings.ReplaceAll(val, ",", " "))
		if len(fields) != 4 {
			return fmt.Errorf("want 4 CLUT register values (0-127), got %d", len(fields))
		}
		for k, fld := range fields {
			n, err := strconv.Atoi(fld)
			if err != nil || n < 0 || n > 127 {
				return fmt.Errorf("CLUT entry %d %q must be 0-127", k, fld)
			}
			c.CLUT[k] = uint8(n)
		}
	case "role_paper":
		return setRole(&c.RolePaper, val)
	case "role_code":
		return setRole(&c.RoleCode, val)
	case "role_comment":
		return setRole(&c.RoleComment, val)
	case "role_label":
		return setRole(&c.RoleLabel, val)
	case "role_register_mark":
		return setRole(&c.RoleRegMark, val)
	case "role_immediate_mark":
		return setRole(&c.RoleImmMark, val)
	case "role_chrome_fg":
		return setRole(&c.RoleChromeFg, val)
	case "role_chrome_bg":
		return setRole(&c.RoleChromeBg, val)
	case "role_current_line":
		return setRole(&c.RoleCurrentBg, val)
	case "role_status":
		return setRole(&c.RoleStatus, val)
	case "relax_palette":
		v, err := b()
		c.RelaxPalette = v
		return err

	// Mnemonic column.
	case "mnemonic_column":
		v := strings.ToLower(strings.TrimSpace(val))
		if v == "adaptive" {
			c.MnemonicAdaptive = true
			return nil
		}
		if strings.HasPrefix(v, "fixed:") {
			n, err := parseInt(strings.TrimPrefix(v, "fixed:"))
			if err != nil {
				return err
			}
			c.MnemonicAdaptive = false
			c.MnemonicFixed = n
			return nil
		}
		return fmt.Errorf("want adaptive | fixed:N, got %q", val)

	// Immediates.
	case "imm_hex_amp":
		v, err := b()
		c.ImmHexAmp = v
		return err
	case "imm_drop_hash":
		v, err := b()
		c.ImmDropHash = v
		return err

	// Separators.
	case "tight_commas":
		v, err := b()
		c.TightCommas = v
		return err

	// Registers.
	case "reg_style":
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "off":
			c.RegStrip = false
		case "strip":
			c.RegStrip = true
		default:
			return fmt.Errorf("want off | strip, got %q", val)
		}
	case "reg_x_style":
		v, err := ms()
		c.RegXStyle = v
		return err
	case "reg_w_style":
		v, err := ms()
		c.RegWStyle = v
		return err
	case "reg_named_style":
		v, err := ms()
		c.RegNamedStyle = v
		return err
	case "mark_immediates":
		v, err := b()
		c.MarkImmediates = v
		return err
	case "mark_immediates_style":
		v, err := ms()
		c.MarkImmediatesStyle = v
		return err

	// Labels.
	case "label_truncate":
		if strings.ToLower(strings.TrimSpace(val)) == "off" {
			c.LabelTruncate = 0
			return nil
		}
		v, err := i()
		c.LabelTruncate = v
		return err
	case "label_ellipsis":
		v, err := b()
		c.LabelEllipsis = v
		return err

	// Comments.
	case "comment_layout":
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "sameline":
			c.CommentLayout = clSameline
		case "above":
			c.CommentLayout = clAbove
		case "marker-only":
			c.CommentLayout = clMarkerOnly
		default:
			return fmt.Errorf("want sameline | above | marker-only, got %q", val)
		}
	case "comment_gap":
		v, err := i()
		c.CommentGap = v
		return err
	case "comment_semicolon":
		v, err := b()
		c.CommentSemicolon = v
		return err
	case "comment_column":
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "page":
			c.CommentColumnCommentedOnly = false
		case "commented-lines-only":
			c.CommentColumnCommentedOnly = true
		default:
			return fmt.Errorf("want page | commented-lines-only, got %q", val)
		}

	// Cursor-line expansion.
	case "expand_cursor_line":
		v := strings.ToLower(strings.TrimSpace(val))
		if v == "off" {
			c.ExpandCursorLine = expandOff
			return nil
		}
		switch {
		case strings.HasPrefix(v, "wrap:"):
			n, err := parseInt(strings.TrimPrefix(v, "wrap:"))
			if err != nil {
				return err
			}
			c.ExpandCursorLine, c.ExpandK = expandWrap, n
		case strings.HasPrefix(v, "panel:"):
			n, err := parseInt(strings.TrimPrefix(v, "panel:"))
			if err != nil {
				return err
			}
			c.ExpandCursorLine, c.ExpandK = expandPanel, n
		default:
			return fmt.Errorf("want off | wrap:K | panel:K, got %q", val)
		}

	// Viewport.
	case "wrap":
		v, err := b()
		c.Wrap = v
		return err
	case "viewport_step":
		v, err := i()
		c.ViewportStep = v
		return err

	// Chrome.
	case "current_line_highlight":
		v, err := b()
		c.CurrentLineHighlight = v
		return err
	case "status_line":
		c.StatusTemplate = val
	case "status_message":
		c.StatusMessage = val

	// R6.
	case "tighten_exprs":
		v, err := b()
		c.TightenExprs = v
		return err

	// Max instruction width.
	case "max_instruction_width":
		v := strings.ToLower(strings.TrimSpace(val))
		if v == "off" {
			c.MaxInstructionWidth = 0
			return nil
		}
		n, err := parseInt(val)
		if err != nil {
			return err
		}
		if n < 1 {
			return fmt.Errorf("max_instruction_width must be off or >= 1, got %d", n)
		}
		c.MaxInstructionWidth = n

	// Constant base rendering.
	case "render_constants":
		v, err := parseRenderConstants(val)
		c.RenderConstants = v
		return err

	default:
		return fmt.Errorf("unknown key")
	}
	return nil
}

// setRole parses a pen index 0..3 for a role assignment.
func setRole(dst *int, val string) error {
	n, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil {
		return fmt.Errorf("want a pen index 0-3, got %q", val)
	}
	*dst = n
	return nil
}

// resolveRoles verifies every role resolves to one of the four CLUT entries
// (0..3). It collects every offender and reports them together (relax_palette
// lifts the check). On success it is a no-op; pens are already indices.
func (c *LabConfig) resolveRoles() error {
	if c.RelaxPalette {
		// Relaxed: roles may exceed the quad — they index a synthesised
		// palette built in the renderer. Nothing to enforce here.
		return nil
	}
	roles := []struct {
		key string
		pen int
	}{
		{"role_paper", c.RolePaper},
		{"role_code", c.RoleCode},
		{"role_comment", c.RoleComment},
		{"role_label", c.RoleLabel},
		{"role_register_mark", c.RoleRegMark},
		{"role_immediate_mark", c.RoleImmMark},
		{"role_chrome_fg", c.RoleChromeFg},
		{"role_chrome_bg", c.RoleChromeBg},
		{"role_current_line", c.RoleCurrentBg},
		{"role_status", c.RoleStatus},
	}
	var bad []string
	for _, r := range roles {
		if r.pen < 0 || r.pen > 3 {
			bad = append(bad, fmt.Sprintf("%s = %d (must be 0-3: MODE 3 shows one 4-colour CLUT quadruple)", r.key, r.pen))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("%d role(s) do not resolve to the 4 CLUT entries:\n  %s\n(set relax_palette = true to dream beyond the SAM palette)",
			len(bad), strings.Join(bad, "\n  "))
	}
	return nil
}

// palette builds the samscreen.Palette for the config's CLUT quadruple. The
// four MODE 3 pens 0..3 carry the configured register values; the rest stay at
// the power-on defaults (unused on a MODE 3 screen). For a relaxed config it
// returns the full 16-colour dream palette instead (roles may index 0..15).
func (c *LabConfig) palette() samscreen.Palette {
	if c.RelaxPalette {
		// The full power-on 16-entry CLUT — distinct dream colours for roles
		// that index beyond the SAM quadruple. The first four still honour the
		// config's CLUT so a dreaming palette can pin its base colours.
		p := samscreen.DefaultPalette()
		for i := 0; i < 4; i++ {
			p[i] = c.CLUT[i]
		}
		return p
	}
	p := samscreen.DefaultPalette()
	for i := 0; i < 4; i++ {
		p[i] = c.CLUT[i]
	}
	return p
}

// --- snapshot -------------------------------------------------------------

// Snapshot serialises the live config back to a complete, relaunchable config
// file. The live lab writes this when S is pressed, so a combination Pete likes
// becomes a named, reloadable starting point. Every key the parser reads is
// emitted, so a snapshot fully reconstructs the state.
func (c *LabConfig) Snapshot() string {
	var b strings.Builder
	onoff := func(v bool) string {
		if v {
			return "on"
		}
		return "off"
	}
	fmt.Fprintf(&b, "# lab snapshot — a complete, relaunchable config (editor-prototype -config this)\n\n")

	fmt.Fprintf(&b, "# Palette: 4 SAM CLUT register values (0-127) for the code rows.\n")
	fmt.Fprintf(&b, "clut = %d, %d, %d, %d\n", c.CLUT[0], c.CLUT[1], c.CLUT[2], c.CLUT[3])
	fmt.Fprintf(&b, "role_paper = %d\n", c.RolePaper)
	fmt.Fprintf(&b, "role_code = %d\n", c.RoleCode)
	fmt.Fprintf(&b, "role_comment = %d\n", c.RoleComment)
	fmt.Fprintf(&b, "role_label = %d\n", c.RoleLabel)
	fmt.Fprintf(&b, "role_register_mark = %d\n", c.RoleRegMark)
	fmt.Fprintf(&b, "role_immediate_mark = %d\n", c.RoleImmMark)
	fmt.Fprintf(&b, "role_chrome_fg = %d\n", c.RoleChromeFg)
	fmt.Fprintf(&b, "role_chrome_bg = %d\n", c.RoleChromeBg)
	fmt.Fprintf(&b, "role_current_line = %d\n", c.RoleCurrentBg)
	fmt.Fprintf(&b, "role_status = %d\n", c.RoleStatus)
	fmt.Fprintf(&b, "relax_palette = %s\n\n", onoff(c.RelaxPalette))

	col := "adaptive"
	if !c.MnemonicAdaptive {
		col = fmt.Sprintf("fixed:%d", c.MnemonicFixed)
	}
	fmt.Fprintf(&b, "mnemonic_column = %s\n\n", col)

	fmt.Fprintf(&b, "imm_hex_amp = %s\n", onoff(c.ImmHexAmp))
	fmt.Fprintf(&b, "imm_drop_hash = %s\n\n", onoff(c.ImmDropHash))

	fmt.Fprintf(&b, "tight_commas = %s\n\n", onoff(c.TightCommas))

	regStyle := "off"
	if c.RegStrip {
		regStyle = "strip"
	}
	fmt.Fprintf(&b, "reg_style = %s\n", regStyle)
	fmt.Fprintf(&b, "reg_x_style = %s\n", c.RegXStyle)
	fmt.Fprintf(&b, "reg_w_style = %s\n", c.RegWStyle)
	fmt.Fprintf(&b, "reg_named_style = %s\n", c.RegNamedStyle)
	fmt.Fprintf(&b, "mark_immediates = %s\n", onoff(c.MarkImmediates))
	fmt.Fprintf(&b, "mark_immediates_style = %s\n\n", c.MarkImmediatesStyle)

	trunc := "off"
	if c.LabelTruncate > 0 {
		trunc = strconv.Itoa(c.LabelTruncate)
	}
	fmt.Fprintf(&b, "label_truncate = %s\n", trunc)
	fmt.Fprintf(&b, "label_ellipsis = %s\n\n", onoff(c.LabelEllipsis))

	fmt.Fprintf(&b, "comment_layout = %s\n", c.CommentLayout)
	fmt.Fprintf(&b, "comment_gap = %d\n", c.CommentGap)
	fmt.Fprintf(&b, "comment_semicolon = %s\n", onoff(c.CommentSemicolon))
	colRule := "page"
	if c.CommentColumnCommentedOnly {
		colRule = "commented-lines-only"
	}
	fmt.Fprintf(&b, "comment_column = %s\n\n", colRule)

	exp := "off"
	if c.ExpandCursorLine != expandOff {
		exp = fmt.Sprintf("%s:%d", c.ExpandCursorLine, c.ExpandK)
	}
	fmt.Fprintf(&b, "expand_cursor_line = %s\n\n", exp)

	fmt.Fprintf(&b, "wrap = %s\n", onoff(c.Wrap))
	fmt.Fprintf(&b, "viewport_step = %d\n\n", c.ViewportStep)

	fmt.Fprintf(&b, "current_line_highlight = %s\n", onoff(c.CurrentLineHighlight))
	fmt.Fprintf(&b, "status_line = %s\n", c.StatusTemplate)
	if c.StatusMessage != "" {
		fmt.Fprintf(&b, "status_message = %s\n", c.StatusMessage)
	}
	fmt.Fprintf(&b, "\ntighten_exprs = %s\n", onoff(c.TightenExprs))

	maxW := "off"
	if c.MaxInstructionWidth > 0 {
		maxW = strconv.Itoa(c.MaxInstructionWidth)
	}
	fmt.Fprintf(&b, "max_instruction_width = %s\n", maxW)
	fmt.Fprintf(&b, "render_constants = %s\n", c.RenderConstants)
	return b.String()
}
