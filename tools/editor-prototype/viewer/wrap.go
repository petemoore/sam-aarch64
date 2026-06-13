package viewer

// dispRow is one display row: a slice of one source line, with the source line
// index it came from and whether it is the first display row of that line (used
// to draw a continuation marker on wrapped rows).
type dispRow struct {
	srcLine int
	text    string
	first   bool
}

// wrapLine splits a source line into display rows of at most width columns.
// With wrap=false it truncates to one row (the overflow is dropped from the
// display, signalled by the caller's truncate marker). With wrap=true it hard-
// wraps at the column boundary (no word breaking — the corpus has 1,693-char
// lines and arbitrary punctuation; a column wrap is predictable and matches
// what a fixed-cell SAM renderer does). An empty source line yields one empty
// display row so blank lines occupy a row.
func wrapLine(srcIdx int, text string, width int, wrap bool) []dispRow {
	if width <= 0 {
		width = 1
	}
	if !wrap {
		if len(text) > width {
			text = text[:width]
		}
		return []dispRow{{srcLine: srcIdx, text: text, first: true}}
	}
	if len(text) == 0 {
		return []dispRow{{srcLine: srcIdx, text: "", first: true}}
	}
	var rows []dispRow
	for off := 0; off < len(text); off += width {
		end := off + width
		if end > len(text) {
			end = len(text)
		}
		rows = append(rows, dispRow{
			srcLine: srcIdx,
			text:    text[off:end],
			first:   off == 0,
		})
	}
	return rows
}

// truncated reports whether a source line overflows width (so the viewer can
// draw a truncation marker in truncate mode).
func truncated(text string, width int) bool { return len(text) > width }
