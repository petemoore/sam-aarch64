package render

// PC tracking for header-table label/local placement.
//
// In a compact `.tbn` v2 (M8 / i39a) the position-labels and numeric-local
// def sites no longer appear as LABEL_DEF/LOCAL_DEF records — they live in
// the header label/local tables keyed by byte offset from origin. To render
// them back to text at the right point, Emit walks the record stream while
// tracking the running PC (in offset-from-origin space) and emits a label/
// local definition immediately before the statement whose PC equals the
// definition's offset.
//
// The PC-advance rules mirror refenc's Pass1 exactly (each INSN_RUN element
// is 4 bytes; LIT_DATA is its raw byte count; the directive sizes match
// directiveSizeAtPC). Alignment (.align/.balign) padding depends on the
// absolute VMA, so the injected leading `.org` sets originVMA and alignment
// is computed against originVMA+pc. A symbolic `.tbn` has empty header tables,
// so headerDefs is inert and the LABEL_DEF/LOCAL_DEF records render inline as
// before.

import (
	"fmt"
	"sort"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// headerDef is one merged header-table definition (a named label or a
// numeric local) at a byte offset from origin.
type headerDef struct {
	offset int64
	isLocal bool
	name   string // label name (isLocal == false)
	digit  byte   // local digit (isLocal == true)
}

// headerDefs merges the label and local header tables into one offset-ordered
// list and emits each definition when the running PC reaches its offset.
type headerDefs struct {
	defs   []headerDef
	cursor int
}

// newHeaderDefs builds the merged, offset-sorted definition list from the
// file's header tables. Ties at the same offset emit labels before locals,
// then by name/digit — a stable order so re-rendering is deterministic.
func newHeaderDefs(f *format.File) *headerDefs {
	var defs []headerDef
	for _, lr := range f.Labels {
		defs = append(defs, headerDef{offset: lr.Offset, name: f.Names[lr.NameID]})
	}
	for _, lr := range f.Locals {
		defs = append(defs, headerDef{offset: lr.Offset, isLocal: true, digit: lr.Digit})
	}
	sort.SliceStable(defs, func(i, j int) bool {
		if defs[i].offset != defs[j].offset {
			return defs[i].offset < defs[j].offset
		}
		if defs[i].isLocal != defs[j].isLocal {
			return !defs[i].isLocal // labels before locals at the same offset
		}
		if defs[i].isLocal {
			return defs[i].digit < defs[j].digit
		}
		return defs[i].name < defs[j].name
	})
	return &headerDefs{defs: defs}
}

// empty reports whether there are no header-table definitions (the symbolic
// `.tbn` path, where labels/locals are still LABEL_DEF/LOCAL_DEF records).
func (h *headerDefs) empty() bool { return len(h.defs) == 0 }

// flushAt emits every header definition whose offset equals pc, advancing the
// cursor. Definitions are visited in offset order, so once pc passes an
// offset with no match it is skipped (a label whose PC no record lands on is a
// malformed table — flushAt surfaces it via remaining()).
func (h *headerDefs) flushAt(pc int64, em func(d headerDef)) {
	for h.cursor < len(h.defs) && h.defs[h.cursor].offset == pc {
		em(h.defs[h.cursor])
		h.cursor++
	}
}

// remaining returns any definitions whose offset was never reached by the PC
// walk (a corrupt header table or a PC-accounting bug); used to fail loudly
// rather than silently drop a label.
func (h *headerDefs) remaining() []headerDef {
	if h.cursor >= len(h.defs) {
		return nil
	}
	return h.defs[h.cursor:]
}

// sidecarCursor walks the editor-region sidecar (compact `.tbn` v2, M8 /
// i39b-2 + i78) in anchor order, emitting each row when the renderer's PC walk
// reaches its anchor offset. Comments and blank-line runs no longer appear as
// inline COMMENT / BLANK_RUN records — they live in the editor region keyed by
// output PC, in source order, exactly like the header label table. Within one
// anchor, stored order is source order, so interleaved comments and blank runs
// render in the order the author wrote them.
type sidecarCursor struct {
	rows   []format.SidecarRow
	cursor int
}

func newSidecarCursor(f *format.File) *sidecarCursor {
	// ReadFile already returns them anchor-sorted (the sidecar is delta-coded
	// in ascending order, stable in source order at a tie); copy defensively.
	return &sidecarCursor{rows: append([]format.SidecarRow(nil), f.Sidecar...)}
}

func (c *sidecarCursor) empty() bool { return len(c.rows) == 0 }

// flushAt emits every row whose anchor is at or before pc, advancing the
// cursor. The "or before" (rather than exact ==) is robustness: a row is always
// anchored to a statement-boundary PC the walk hits, but draining at-or-before
// guarantees nothing is stranded if PC accounting ever drifts.
func (c *sidecarCursor) flushAt(pc int64, emComment func(format.CommentRow), emBlank func(format.BlankRun)) {
	for c.cursor < len(c.rows) && c.rows[c.cursor].Anchor() <= pc {
		r := c.rows[c.cursor]
		if r.Kind == format.SidecarBlank {
			emBlank(r.Blank)
		} else {
			emComment(r.Comment)
		}
		c.cursor++
	}
}

func (c *sidecarCursor) remaining() []format.SidecarRow {
	if c.cursor >= len(c.rows) {
		return nil
	}
	return c.rows[c.cursor:]
}

// directiveByteSize returns the byte contribution of a DIRECTIVE record at the
// given PC (offset from origin) and the current origin VMA. It mirrors
// refenc's directiveSizeAtPC for every directive that can appear in a compact
// stream; alignment uses originVMA+pc so padding matches the assembler.
func directiveByteSize(rec format.Record, pc, originVMA int64) (int64, error) {
	name := format.DirectiveName(rec.DirectiveID)
	switch name {
	case ".byte":
		return int64(rec.OperandCount), nil
	case ".short", ".hword":
		return int64(rec.OperandCount) * 2, nil
	case ".word":
		return int64(rec.OperandCount) * 4, nil
	case ".quad":
		return int64(rec.OperandCount) * 8, nil
	case ".ascii", ".asciz":
		or := format.NewOperandReader(rec.Operands)
		o, err := or.Next()
		if err != nil {
			return 0, err
		}
		n := int64(len(o.Str))
		if name == ".asciz" {
			n++
		}
		return n, nil
	case ".skip", ".space":
		or := format.NewOperandReader(rec.Operands)
		o, err := or.Next()
		if err != nil {
			return 0, err
		}
		v, ok := format.EvalConst(o.Expr)
		if !ok {
			return 0, fmt.Errorf("%s: non-constant size in compact stream", name)
		}
		return v, nil
	case ".inst":
		return 4, nil
	case ".balign":
		or := format.NewOperandReader(rec.Operands)
		o, err := or.Next()
		if err != nil {
			return 0, err
		}
		align, ok := format.EvalConst(o.Expr)
		if !ok || align <= 1 {
			return 0, nil
		}
		return alignPad(originVMA+pc, uint64(align)), nil
	case ".align":
		or := format.NewOperandReader(rec.Operands)
		o, err := or.Next()
		if err != nil {
			return 0, err
		}
		n, ok := format.EvalConst(o.Expr)
		if !ok || n <= 0 {
			return 0, nil
		}
		return alignPad(originVMA+pc, uint64(int64(1)<<uint64(n))), nil
	case ".text", ".data", ".global", ".equ", ".set",
		".section", ".arch", ".cpu", ".org", ".ltorg":
		// Zero-byte directives. .org is handled specially by the caller
		// (it sets the origin / advances PC); the rest emit no bytes.
		return 0, nil
	}
	return 0, fmt.Errorf("render: unknown directive %q for PC sizing", name)
}

// alignPad returns the padding bytes needed to align addr up to a power-of-two
// boundary, using unsigned modular arithmetic (the spectrum4 origin VMA has
// its high bit set and would be negative as a signed value).
func alignPad(addr int64, align uint64) int64 {
	if align == 0 {
		return 0
	}
	return int64((align - (uint64(addr) % align)) % align)
}
