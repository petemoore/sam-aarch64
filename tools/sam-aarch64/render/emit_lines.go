package render

import (
	"bytes"
	"fmt"
	"strings"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Line is one rendered source line tagged with the index (into f.Records) of
// the record that produced it. RecordIdx is -1 for lines produced by the
// editor-region sidecar (header-table label/local definitions and the comment
// sidecar) rather than by a record in the statement stream — those have no
// originating record. The viewer uses RecordIdx to map screen rows back to
// records (the i41-shaped record↔row mapping, spec §4 R4).
type Line struct {
	RecordIdx int
	Text      string
}

// EmitLines renders a decoded File to the same canonical text EmitFile
// produces, but split into lines each tagged with its originating record
// index (spec §4: a per-record render adapter so the viewer gets a
// []Line{recordIdx, text} mapping instead of one flat byte slice). It runs
// the identical emit logic as EmitFile — the helper functions are shared, so
// the two cannot diverge.
//
// Attribution rule: bytes a record's emit writes are tagged with that record's
// index. Sidecar content (header label/local definitions, comment sidecar
// entries) is flushed lazily at the PC of the next byte-emitting record; it is
// tagged -1 (it has no statement-stream record). A trailing comment (placement
// 1) is appended to the preceding statement's last line and stays tagged with
// that statement's record — matching how a reader sees `add x0, x1 // note`.
func EmitLines(f *format.File) ([]Line, error) {
	var out bytes.Buffer
	var prevWasStatement bool

	// spans records, in emit order, the [start,end) byte range each emit wrote
	// into out. A span with idx == -1 is sidecar (comment/label) content.
	var spans []rangeIdx
	// emitSpan runs fn (which appends to out) and records the byte range it
	// produced against idx.
	emitSpan := func(idx int, fn func() error) error {
		start := out.Len()
		if err := fn(); err != nil {
			return err
		}
		if out.Len() > start {
			spans = append(spans, rangeIdx{idx: idx, start: start, end: out.Len()})
		}
		return nil
	}

	for _, id := range f.GlobalNameIDs {
		if err := emitSpan(-1, func() error {
			fmt.Fprintf(&out, "  .global %s\n", f.Names[id])
			return nil
		}); err != nil {
			return nil, err
		}
	}

	hd := newHeaderDefs(f)
	cc := newSidecarCursor(f)
	trackPC := !hd.empty() || !cc.empty()
	var pc int64
	var originVMA int64

	emitDef := func(d headerDef) {
		if prevWasStatement {
			out.WriteByte('\n')
		}
		if d.isLocal {
			fmt.Fprintf(&out, "%d:", d.digit)
		} else {
			fmt.Fprintf(&out, "%s:", d.name)
		}
		prevWasStatement = true
	}
	emitComment := func(c format.CommentRow) {
		emitCommentBody(&out, c.Body, c.Placement, &prevWasStatement)
	}
	emitBlank := func(b format.BlankRun) {
		emitBlankRun(&out, b.RunLen, &prevWasStatement)
	}
	// flushComments / flush drain sidecar entries (comments + blank runs) due at
	// the current PC. They are tagged -1 via emitSpan: the calling record
	// records its own bytes separately, below.
	flushComments := func() {
		_ = emitSpan(-1, func() error { cc.flushAt(pc, emitComment, emitBlank); return nil })
	}
	flush := func() {
		flushComments()
		_ = emitSpan(-1, func() error { hd.flushAt(pc, emitDef); return nil })
	}

	for i, rec := range f.Records {
		i := i
		rec := rec
		switch rec.Kind {
		case format.KindLabelDef:
			if err := emitSpan(i, func() error {
				if prevWasStatement {
					out.WriteByte('\n')
				}
				fmt.Fprintf(&out, "%s:", f.Names[rec.SymbolID])
				prevWasStatement = true
				return nil
			}); err != nil {
				return nil, err
			}
		case format.KindLocalDef:
			if err := emitSpan(i, func() error {
				if prevWasStatement {
					out.WriteByte('\n')
				}
				fmt.Fprintf(&out, "%d:", rec.Digit)
				prevWasStatement = true
				return nil
			}); err != nil {
				return nil, err
			}
		case format.KindComment:
			if err := emitSpan(i, func() error {
				emitCommentBody(&out, rec.Body, rec.Placement, &prevWasStatement)
				return nil
			}); err != nil {
				return nil, err
			}
		case format.KindBlankRun:
			if err := emitSpan(i, func() error {
				emitBlankRun(&out, rec.RunLen, &prevWasStatement)
				return nil
			}); err != nil {
				return nil, err
			}
		case format.KindInst:
			flush()
			if err := emitSpan(i, func() error {
				if prevWasStatement {
					out.WriteByte('\n')
				}
				if err := emitStatement(&out, f, rec); err != nil {
					return err
				}
				prevWasStatement = true
				return nil
			}); err != nil {
				return nil, err
			}
			pc += 4
		case format.KindDirective:
			if format.DirectiveName(rec.DirectiveID) != ".org" {
				flush()
			} else {
				flushComments()
			}
			if err := emitSpan(i, func() error {
				if prevWasStatement {
					out.WriteByte('\n')
				}
				return emitStatement(&out, f, rec)
			}); err != nil {
				return nil, err
			}
			prevWasStatement = true
			if trackPC {
				if format.DirectiveName(rec.DirectiveID) == ".org" {
					or := format.NewOperandReader(rec.Operands)
					o, oerr := or.Next()
					if oerr != nil {
						return nil, oerr
					}
					target, ok := format.EvalConst(o.Expr)
					if !ok {
						return nil, fmt.Errorf(".org: non-constant target in compact stream")
					}
					if pc == 0 && originVMA == 0 {
						originVMA = target
					} else {
						pc = target - originVMA
					}
				} else {
					n, derr := directiveByteSize(rec, pc, originVMA)
					if derr != nil {
						return nil, derr
					}
					pc += n
				}
			}
		case format.KindInsnRun:
			if err := emitSpan(i, func() error {
				return emitInsnRun(&out, f, rec, &prevWasStatement, &pc, flush)
			}); err != nil {
				return nil, err
			}
		case format.KindLitInsts:
			if err := emitSpan(i, func() error {
				emitLitInsts(&out, rec, &prevWasStatement, &pc, flush)
				return nil
			}); err != nil {
				return nil, err
			}
		case format.KindLitData:
			flush()
			if err := emitSpan(i, func() error {
				if err := emitLitData(&out, rec, &prevWasStatement); err != nil {
					return err
				}
				return nil
			}); err != nil {
				return nil, err
			}
			pc += int64(len(rec.LitData))
		default:
			if err := emitSpan(i, func() error {
				fmt.Fprintf(&out, "// [skipped unknown record kind 0x%02x, %d bytes]\n",
					byte(rec.Kind), len(rec.Raw))
				prevWasStatement = false
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}
	flush()
	if leftover := hd.remaining(); len(leftover) > 0 {
		return nil, fmt.Errorf("render: %d header definition(s) not placed by PC walk (first at offset %#x)",
			len(leftover), leftover[0].offset)
	}
	if leftover := cc.remaining(); len(leftover) > 0 {
		return nil, fmt.Errorf("render: %d sidecar row(s) not placed by PC walk (first at offset %#x)",
			len(leftover), leftover[0].Anchor())
	}
	if prevWasStatement {
		out.WriteByte('\n')
	}

	return spansToLines(out.Bytes(), spans), nil
}

// rangeIdx pairs a byte range with the record index it belongs to.
type rangeIdx struct {
	idx        int
	start, end int
}

// spansToLines splits the rendered bytes into Lines, tagging each line with
// the record index of the span that contains the line's terminating newline
// (or, for a final line without a newline, the span containing its last byte).
// A line that straddles spans — a trailing comment appended to a statement —
// is tagged with the statement's record (the first span's index), since the
// newline that ends the line is written by the comment's span but the reader
// attributes the whole physical line to the statement it annotates.
func spansToLines(buf []byte, ranges []rangeIdx) []Line {
	// idxAt returns the record index whose span covers byte offset pos.
	idxAt := func(pos int) int {
		for _, r := range ranges {
			if pos >= r.start && pos < r.end {
				return r.idx
			}
		}
		return -1
	}

	var lines []Line
	lineStart := 0
	for pos := 0; pos < len(buf); pos++ {
		if buf[pos] != '\n' {
			continue
		}
		text := string(buf[lineStart:pos])
		lines = append(lines, Line{RecordIdx: lineStartIdx(idxAt, lineStart, pos), Text: text})
		lineStart = pos + 1
	}
	if lineStart < len(buf) {
		text := string(buf[lineStart:])
		lines = append(lines, Line{RecordIdx: lineStartIdx(idxAt, lineStart, len(buf)-1), Text: text})
	}
	return lines
}

// lineStartIdx picks the record index for a physical line spanning
// [start,end]. It prefers the index at the line's first byte (the statement a
// trailing comment annotates), falling back to the index at the line's end if
// the start is sidecar-only.
func lineStartIdx(idxAt func(int) int, start, end int) int {
	if i := idxAt(start); i >= 0 {
		return i
	}
	return idxAt(end)
}

// EmitLinesFromBytes is a convenience wrapper: ReadFile then EmitLines.
func EmitLinesFromBytes(in []byte) ([]Line, error) {
	f, err := format.ReadFile(in)
	if err != nil {
		return nil, err
	}
	return EmitLines(f)
}

// JoinLines reassembles Lines back into the flat text EmitFile produces, for
// the round-trip equivalence test.
func JoinLines(lines []Line) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
