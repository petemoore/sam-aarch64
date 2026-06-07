package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes body to a temp .config and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.config")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestParseValidConfig parses a representative config and checks the fields the
// keys set, including the value forms (fixed:N, wrap:K, on/off, marks).
func TestParseValidConfig(t *testing.T) {
	p := writeConfig(t, `
# a comment
clut = 0, 15, 102, 127
mnemonic_column = fixed:12
imm_hex_amp = on
imm_drop_hash = off
tight_commas = on
reg_style = strip
reg_x_style = bg
reg_w_style = inverse
mark_immediates = on
mark_immediates_style = fg
label_truncate = 16
label_ellipsis = off
comment_layout = above
comment_gap = 3
comment_semicolon = on
comment_column = commented-lines-only
expand_cursor_line = wrap:3
wrap = off
viewport_step = 6
current_line_highlight = on
status_line = {file} {pct}
status_message = hi
tighten_exprs = on
`)
	c, err := parseLabConfigFile(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"MnemonicAdaptive", c.MnemonicAdaptive, false},
		{"MnemonicFixed", c.MnemonicFixed, 12},
		{"ImmHexAmp", c.ImmHexAmp, true},
		{"ImmDropHash", c.ImmDropHash, false},
		{"TightCommas", c.TightCommas, true},
		{"RegStrip", c.RegStrip, true},
		{"RegXStyle", c.RegXStyle, markBg},
		{"RegWStyle", c.RegWStyle, markInverse},
		{"MarkImmediates", c.MarkImmediates, true},
		{"MarkImmediatesStyle", c.MarkImmediatesStyle, markFg},
		{"LabelTruncate", c.LabelTruncate, 16},
		{"LabelEllipsis", c.LabelEllipsis, false},
		{"CommentLayout", c.CommentLayout, clAbove},
		{"CommentGap", c.CommentGap, 3},
		{"CommentSemicolon", c.CommentSemicolon, true},
		{"CommentColumnCommentedOnly", c.CommentColumnCommentedOnly, true},
		{"ExpandCursorLine", c.ExpandCursorLine, expandWrap},
		{"ExpandK", c.ExpandK, 3},
		{"Wrap", c.Wrap, false},
		{"ViewportStep", c.ViewportStep, 6},
		{"CurrentLineHighlight", c.CurrentLineHighlight, true},
		{"StatusMessage", c.StatusMessage, "hi"},
		{"TightenExprs", c.TightenExprs, true},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("%s = %v, want %v", ck.name, ck.got, ck.want)
		}
	}
}

// TestUnknownKeyReported collects an unknown key as a clear error.
func TestUnknownKeyReported(t *testing.T) {
	p := writeConfig(t, "no_such_key = 1\n")
	_, err := parseLabConfigFile(p)
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("want unknown-key error, got %v", err)
	}
}

// TestBadValueReported reports a bad value with the key and line.
func TestBadValueReported(t *testing.T) {
	p := writeConfig(t, "imm_hex_amp = maybe\n")
	_, err := parseLabConfigFile(p)
	if err == nil || !strings.Contains(err.Error(), "imm_hex_amp") {
		t.Fatalf("want value error mentioning the key, got %v", err)
	}
}

// TestRoleViolationListsOffenders rejects out-of-quad roles and lists each.
func TestRoleViolationListsOffenders(t *testing.T) {
	p := writeConfig(t, "role_code = 7\nrole_comment = 9\n")
	_, err := parseLabConfigFile(p)
	if err == nil {
		t.Fatal("want a role-resolution error")
	}
	msg := err.Error()
	for _, want := range []string{"role_code", "role_comment", "0-3"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

// TestRelaxPaletteSkipsRoleCheck lets relaxed configs use roles beyond the quad.
func TestRelaxPaletteSkipsRoleCheck(t *testing.T) {
	p := writeConfig(t, "relax_palette = on\nrole_code = 7\nrole_comment = 9\n")
	c, err := parseLabConfigFile(p)
	if err != nil {
		t.Fatalf("relaxed config should parse: %v", err)
	}
	if c.RoleCode != 7 || c.RoleComment != 9 {
		t.Errorf("relaxed roles not preserved: code=%d comment=%d", c.RoleCode, c.RoleComment)
	}
}

// TestSnapshotRoundTrip serialises a config, reparses it, and checks the
// reparsed config matches — a snapshot is a complete, relaunchable config.
func TestSnapshotRoundTrip(t *testing.T) {
	orig := defaultConfig()
	orig.MnemonicAdaptive = true
	orig.ImmHexAmp = true
	orig.ImmDropHash = true
	orig.TightCommas = true
	orig.RegStrip = true
	orig.RegXStyle = markBg
	orig.RegWStyle = markInverse
	orig.RegNamedStyle = markFg
	orig.MarkImmediates = true
	orig.MarkImmediatesStyle = markFg
	orig.LabelTruncate = 16
	orig.LabelEllipsis = false
	orig.CommentLayout = clMarkerOnly
	orig.CommentGap = 4
	orig.CommentSemicolon = true
	orig.CommentColumnCommentedOnly = true
	orig.ExpandCursorLine = expandPanel
	orig.ExpandK = 4
	orig.Wrap = false
	orig.ViewportStep = 5
	orig.CurrentLineHighlight = true
	orig.StatusTemplate = "{file} L{line}/{total} {pct} {mode}"
	orig.StatusMessage = "round trip"
	orig.TightenExprs = true

	p := writeConfig(t, orig.Snapshot())
	got, err := parseLabConfigFile(p)
	if err != nil {
		t.Fatalf("reparse snapshot: %v", err)
	}
	if got != orig {
		t.Errorf("snapshot round-trip mismatch:\n got  %+v\n want %+v", got, orig)
	}
}

// TestStarterConfigsParse parses every shipped starter config — they are the
// documented entry points, so a typo in one is a real defect.
func TestStarterConfigsParse(t *testing.T) {
	for _, name := range []string{
		"binutils-baseline", "compressed", "comet-minimal", "dreamer",
	} {
		path := filepath.Join("configs", name+".config")
		if _, err := parseLabConfigFile(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}
