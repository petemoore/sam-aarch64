// Package viewer is the read-only scrolling viewer (spec §5 P1, i4 parity):
// open a compact `.tbn` via the format lib, render its records + comments at
// the active geometry, scroll with cursor keys, show a status line. It is
// written entirely against samscreen.Screen, so the same viewer runs on the
// interactive terminal backend and the file-rendering mockup backend.
package viewer

import (
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
	"github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
)

// Document is the prototype's in-memory model of a `.tbn` for read-only
// viewing (spec §4: wired via format.ReadFile + the per-record render adapter,
// not by parsing rendered text). It holds the decoded File and the per-record
// rendered lines, so each source line knows its originating record — the
// i41-shaped record↔row mapping, real from day one.
type Document struct {
	Path  string
	File  *format.File
	Lines []render.Line

	// kindCounts summarises record kinds for the status line.
	commentLines int
}

// LoadDocument reads and renders a `.tbn` into a Document.
func LoadDocument(path string, data []byte) (*Document, error) {
	f, err := format.ReadFile(data)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	lines, err := render.EmitLines(f)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", path, err)
	}
	d := &Document{Path: path, File: f, Lines: lines}
	for _, l := range lines {
		if len(l.Text) >= 2 && l.Text[0] == '/' && l.Text[1] == '/' {
			d.commentLines++
		} else if idx := indexComment(l.Text); idx >= 0 {
			d.commentLines++
		}
	}
	return d, nil
}

// indexComment returns the byte index of a trailing `//` comment in s, or -1.
// (Heuristic, for the status-line count only; the renderer already placed
// comments correctly.)
func indexComment(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '/' && s[i+1] == '/' {
			return i
		}
	}
	return -1
}

// LineCount is the number of source lines (records-rendered).
func (d *Document) LineCount() int { return len(d.Lines) }

// RecordKindAt returns a short label for the record kind that produced source
// line i, for the status line (spec §5: "record kind"). Sidecar lines (-1)
// report "comment".
func (d *Document) RecordKindAt(i int) string {
	if i < 0 || i >= len(d.Lines) {
		return "-"
	}
	idx := d.Lines[i].RecordIdx
	if idx < 0 || idx >= len(d.File.Records) {
		return "comment"
	}
	return d.File.Records[idx].Kind.Name()
}
