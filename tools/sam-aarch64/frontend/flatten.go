package frontend

// Flatten implements the section-flattening pass that moves the
// linker-equivalent work upstream of refenc. Spectrum4 today uses GNU's
// separate-compilation + ld linker-script model; our refenc encoder is a
// single-pass-on-single-source 8-bit-style assembler. Rather than build
// multi-section + linker-script support into refenc, this pass folds
// the linker-script layout into a single linear record stream:
//
//  1. All records are bucketed by section (default `.text`).
//  2. Each section's records are walked to compute label offsets within
//     the section (a mini sizing pass mirroring refenc's Pass1).
//  3. Section start VMAs are computed from the linker-script layout
//     rules (hard-coded for spectrum4 — see SpectrumFourLayout below).
//  4. A new record stream is emitted:
//       • `.org ORIGIN_VMA`
//       • All `.text` records in source order
//       • A trailing `.ltorg` so the literal pool flushes inside `.text`
//       • For each non-.text/.data section, `.equ LABEL, VMA` records
//         for every label defined in that section. No bytes are emitted
//         for those sections — the spectrum4 release.img only
//         materialises `.text`, mirroring `objcopy -O binary` on the
//         linked ELF where all post-text sections are NOBITS.
//
// The flatten step intentionally drops `.data` records as well in the
// release build because release.target excludes the only `.data`
// producer (demo/screen.s). If a future build introduces real `.data`
// content the flatten will need to emit those records after `.text`
// with proper `.balign 0x10`.

import (
	"fmt"

	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// FlattenOptions configures the flatten pass.
type FlattenOptions struct {
	// OriginVMA is the virtual address of the first emitted byte
	// (mirroring the linker script `. = 0xfffffff000000000`).
	OriginVMA int64
	// Layout is the section-ordering table the flatten pass lays out
	// against. When nil it defaults to SpectrumFourLayout, so the host
	// flatten path is not spectrum4-locked: a different target supplies
	// its own table (e.g. via the `-layout` flag → LoadLayoutFile). The
	// table's role convention is fixed (see SectionLayout): slot 0 is the
	// code section, slot 1 the data section, slots 2+ are address-only.
	Layout []SectionLayout
}

// SectionLayout describes one entry in a target's linker-script section
// ordering. Alignment is applied *before* the section starts (i.e.
// section.start = ALIGN(prev_section.end, alignment)).
//
// The flatten pass assigns roles by POSITION in the layout slice: slot 0
// is the CODE section (emitted in source order, with a trailing `.ltorg`
// that flushes its literal pool); slot 1 is the DATA section (emitted
// after the code with a leading `.balign 0x10`, only when it has content);
// slots 2+ are ADDRESS-ONLY (trailing NOBITS — their labels become
// `.equ NAME, VMA` records and their bytes are dropped, mirroring
// `objcopy -O binary` on the linked ELF where post-data sections are
// NOBITS).
type SectionLayout struct {
	Name          string `json:"name"`
	StartAlign    int64  `json:"start_align"`
	TrailingAlign int64  `json:"trailing_align"` // ALIGN applied to PC at end of section (0 = none)
}

// SpectrumFourLayout encodes the linker-script section order used by
// kernel/spectrum4.ld. It is the default FlattenOptions.Layout. Section
// sizing and start computations consult the active layout table.
var SpectrumFourLayout = []SectionLayout{
	{Name: ".text", StartAlign: 0x10},
	{Name: ".data", StartAlign: 0x10},
	{Name: "bss_roms", StartAlign: 0x1000, TrailingAlign: 0x10},
	{Name: "bss_kernel", StartAlign: 0x10},
	{Name: "bss_userheap", StartAlign: 0x10},
	{Name: "bss_coherent", StartAlign: 0x200000},
	{Name: "text_tests", StartAlign: 0x1000, TrailingAlign: 0x10},
	{Name: "bss_tests", StartAlign: 0x10},
}

// layoutIndex returns the index of name in the layout table, or -1 if
// name is not a known section.
func layoutIndex(layout []SectionLayout, name string) int {
	for i, s := range layout {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// Flatten takes the raw record stream produced by Parse and rewrites it
// into the linker-equivalent flat layout described in the file-level
// comment. names is the symbol-table name list; the rewritten stream
// reuses ids from the same name table (no symbols are added to it —
// label names already interned during parsing are reused). Returns the
// new record stream.
func Flatten(records []format.Record, names []string, opts FlattenOptions) ([]format.Record, error) {
	// The active section-ordering table. Defaults to spectrum4 so the
	// common path is unchanged; a non-spectrum4 target passes its own.
	layout := opts.Layout
	if layout == nil {
		layout = SpectrumFourLayout
	}
	if len(layout) == 0 {
		return nil, fmt.Errorf("flatten: layout has no sections")
	}

	// Bucket records by current section. The walker observes
	// `.text` / `.data` / `.section X` directives and switches the
	// current bucket; those directive records are *consumed* (not
	// emitted into the output).
	bucketByName := map[string][]format.Record{}
	currentSection := ".text"

	textID, _ := format.DirectiveID(".text")
	dataID, _ := format.DirectiveID(".data")
	sectionID, _ := format.DirectiveID(".section")

	for _, rec := range records {
		if rec.Kind == format.KindDirective {
			switch rec.DirectiveID {
			case textID:
				currentSection = ".text"
				continue
			case dataID:
				currentSection = ".data"
				continue
			case sectionID:
				// First operand is OpSysName(section_name).
				or := format.NewOperandReader(rec.Operands)
				o, err := or.Next()
				if err != nil {
					return nil, fmt.Errorf("flatten: %w", err)
				}
				if o.Kind != format.OpSysName {
					return nil, fmt.Errorf("flatten: .section first operand is not OpSysName (kind=%v)", o.Kind)
				}
				currentSection = string(o.Str)
				continue
			}
		}
		bucketByName[currentSection] = append(bucketByName[currentSection], rec)
	}

	// Compute section sizes and starts. Sizing walks each section's
	// records and applies a refenc-Pass1-equivalent sizer (instructions
	// = 4 bytes, directives sized via sectionSizer). The `.text`
	// section sizer also accounts for the literal-pool flush at the
	// trailing `.ltorg`.
	type sec struct {
		layout     SectionLayout
		records    []format.Record
		start      int64
		size       int64
		hasContent bool
	}
	sections := make([]*sec, len(layout))
	for i, lay := range layout {
		recs := bucketByName[lay.Name]
		sections[i] = &sec{layout: lay, records: recs, hasContent: len(recs) > 0}
	}

	// Detect any unknown sections that were used but aren't part of
	// the active layout — surface as an error to avoid silently
	// dropping content.
	for n := range bucketByName {
		if layoutIndex(layout, n) < 0 {
			return nil, fmt.Errorf("flatten: section %q not in the layout", n)
		}
	}

	// Pre-pass: collect every `.set`/`.equ`-defined constant across
	// all sections into a single global table. spectrum4 defines its
	// build-time knobs (RAM_DISK_SIZE, HEAP_SIZE, …) at the top of
	// release.target — those records land in the `.text` bucket but
	// are referenced from BSS `.space` directives. A flat global
	// table mirrors GNU as's symbol scope (sections share symbols).
	globalSymbols := map[string]int64{}
	for _, s := range sections {
		for _, rec := range s.records {
			if rec.Kind != format.KindDirective {
				continue
			}
			name := format.DirectiveName(rec.DirectiveID)
			if name != ".equ" && name != ".set" {
				continue
			}
			if err := captureEqu(rec, globalSymbols, names); err != nil {
				return nil, fmt.Errorf("flatten: %s: %w", s.layout.Name, err)
			}
		}
	}

	// Sizing pass. For the .text section we also need to size the
	// implicit literal-pool flush — we mirror refenc's Pass1
	// pool-grouping logic so the computed size matches what refenc
	// will emit. Other sections only contain data/space directives;
	// instructions in BSS sections are not expected (and would be a
	// linker error in GNU as too).
	for i, s := range sections {
		size, err := sectionSize(s.records, i == 0, names, globalSymbols)
		if err != nil {
			return nil, fmt.Errorf("flatten: sizing %s: %w", s.layout.Name, err)
		}
		if s.layout.TrailingAlign > 0 {
			size = alignUp(size, s.layout.TrailingAlign)
		}
		s.size = size
	}

	// Section starts: chain through the layout list, applying each
	// section's StartAlign (bumped to the max alignment requested by
	// any `.align`/`.balign` directive inside the section — GNU as's
	// "section's required alignment is the max of any in-section
	// alignment directive" rule, honoured by the linker when laying
	// the section).
	pc := opts.OriginVMA
	for _, s := range sections {
		startAlign := s.layout.StartAlign
		if a := sectionRequiredAlign(s.records); a > startAlign {
			startAlign = a
		}
		pc = alignUp(pc, startAlign)
		s.start = pc
		pc += s.size
	}

	// Build the output record stream.
	var out []format.Record

	orgID, ok := format.DirectiveID(".org")
	if !ok {
		return nil, fmt.Errorf("flatten: internal: .org directive ID not registered")
	}
	equID, ok := format.DirectiveID(".equ")
	if !ok {
		return nil, fmt.Errorf("flatten: internal: .equ directive ID not registered")
	}
	ltorgID, ok := format.DirectiveID(".ltorg")
	if !ok {
		return nil, fmt.Errorf("flatten: internal: .ltorg directive ID not registered")
	}

	// 1. `.org ORIGIN_VMA`.
	{
		var ow format.OperandWriter
		var ew format.ExprWriter
		ew.WriteImm(opts.OriginVMA)
		ow.WriteImmExpr(ew.Bytes())
		out = append(out, directiveRecord(orgID, 1, ow.Bytes()))
	}

	// 2. All `.text` records in source order.
	out = append(out, sections[0].records...)

	// 3. Trailing `.ltorg` to flush the literal pool inside `.text`.
	//    (refenc would flush at EOF anyway, but we want the pool bytes
	//    to count toward `.text` size for VMA computation correctness.)
	out = append(out, directiveRecord(ltorgID, 0, nil))

	// 4. `.data` records (if any) — emit with a leading `.balign 0x10`
	//    to match the linker-script's `.data : ALIGN(0x10)` clause.
	if sections[1].hasContent {
		// .balign 0x10
		balignID, _ := format.DirectiveID(".balign")
		var ow format.OperandWriter
		var ew format.ExprWriter
		ew.WriteImm(0x10)
		ow.WriteImmExpr(ew.Bytes())
		out = append(out, directiveRecord(balignID, 1, ow.Bytes()))
		out = append(out, sections[1].records...)
	}

	// 5. For each non-.text/.data section, emit `.equ NAME, VMA` for
	//    every label defined in that section. The section's content
	//    (bytes / .space / etc.) is *dropped* — for the release build
	//    those sections are trailing NOBITS and don't contribute to
	//    release.img.
	for i := 2; i < len(sections); i++ {
		s := sections[i]
		labels, err := sectionLabels(s.records, s.layout.Name, names, globalSymbols)
		if err != nil {
			return nil, fmt.Errorf("flatten: labels in %s: %w", s.layout.Name, err)
		}
		for _, lbl := range labels {
			vma := s.start + lbl.Offset
			out = append(out, equRecord(equID, lbl.SymbolID, vma))
		}
	}

	return out, nil
}

// directiveRecord builds a DIRECTIVE record from its fields.
func directiveRecord(directiveID, operandCount byte, operands []byte) format.Record {
	return format.Record{
		Kind:         format.KindDirective,
		DirectiveID:  directiveID,
		OperandCount: operandCount,
		Operands:     operands,
	}
}

// equRecord builds a `.equ NAME, VMA` directive record where NAME is the
// symbol-table entry for the given symbol id.
func equRecord(equID byte, symID uint16, vma int64) format.Record {
	var ow format.OperandWriter
	// Operand 1: PUSH_SYM(symID) — the symbol being defined.
	var nameExpr format.ExprWriter
	nameExpr.WriteSym(symID)
	ow.WriteImmExpr(nameExpr.Bytes())
	// Operand 2: PUSH_IMM(vma) — the value.
	var valExpr format.ExprWriter
	valExpr.WriteImm(vma)
	ow.WriteImmExpr(valExpr.Bytes())
	return directiveRecord(equID, 2, ow.Bytes())
}

func alignUp(v, align int64) int64 {
	if align <= 1 {
		return v
	}
	// Use unsigned arithmetic so high VMAs round up correctly.
	u := uint64(v)
	a := uint64(align)
	r := u % a
	if r == 0 {
		return v
	}
	return int64(u + (a - r))
}

// sectionRequiredAlign returns the maximum alignment requested by any
// `.align`/`.balign` directive inside records. This is what GNU as
// publishes as the section's required alignment, which the linker
// then honours when chaining sections in the output layout.
//
// `.align N`  → 1 << N (aarch64 GNU as convention)
// `.balign N` → N
//
// Returns 1 when there are no alignment directives.
func sectionRequiredAlign(records []format.Record) int64 {
	max := int64(1)
	for _, rec := range records {
		if rec.Kind != format.KindDirective {
			continue
		}
		name := format.DirectiveName(rec.DirectiveID)
		if name != ".align" && name != ".balign" {
			continue
		}
		or := format.NewOperandReader(rec.Operands)
		o, err := or.Next()
		if err != nil {
			continue
		}
		// Best-effort: only literal-folded immediates are honoured;
		// non-literal alignments would surface as sizing errors
		// elsewhere.
		ctx := enc.EvalContext{}
		v, err := enc.Eval(o.Expr, ctx)
		if err != nil {
			continue
		}
		var a int64
		if name == ".align" {
			if v <= 0 || v > 30 {
				continue
			}
			a = int64(1) << uint64(v)
		} else {
			a = v
		}
		if a > max {
			max = a
		}
	}
	return max
}

// labelOffset records a label's offset within its enclosing section.
type labelOffset struct {
	SymbolID uint16
	Offset   int64
}

// sectionLabels walks a section's records, applying directive sizing
// to track the section-local PC, and records each label's offset from
// the section start. globalSymbols is the table of all `.set`/`.equ`
// constants in the input (collected by the global pre-pass) — entries
// from this section may shadow them, but in practice spectrum4 does
// not redefine constants per-section.
func sectionLabels(records []format.Record, sectionName string, names []string, globalSymbols map[string]int64) ([]labelOffset, error) {
	var labels []labelOffset
	// Start from a copy of globalSymbols so that this section's own
	// `.equ`s can shadow without mutating the caller's map.
	symbols := make(map[string]int64, len(globalSymbols))
	for k, v := range globalSymbols {
		symbols[k] = v
	}
	var pc int64
	for _, rec := range records {
		switch rec.Kind {
		case format.KindLabelDef:
			labels = append(labels, labelOffset{SymbolID: rec.SymbolID, Offset: pc})
		case format.KindInst:
			pc += 4
		case format.KindDirective:
			n, err := directiveSizeForFlatten(rec, pc, symbols, names)
			if err != nil {
				return nil, err
			}
			// Handle .equ/.set side-effects on the local symbol table.
			if name := format.DirectiveName(rec.DirectiveID); name == ".equ" || name == ".set" {
				if err := captureEqu(rec, symbols, names); err != nil {
					return nil, err
				}
			}
			pc += n
		}
	}
	return labels, nil
}

// sectionSize returns the total byte size of records (with the
// section-local PC starting at zero). For the code section (isCode) it
// ALSO appends the size of the literal-pool flush so the result equals
// refenc's emitted byte count (including the trailing `.ltorg`).
// globalSymbols is the table of all `.set`/`.equ` constants in the input.
func sectionSize(records []format.Record, isCode bool, names []string, globalSymbols map[string]int64) (int64, error) {
	symbols := make(map[string]int64, len(globalSymbols))
	for k, v := range globalSymbols {
		symbols[k] = v
	}
	var pc int64
	type poolKey struct {
		Width byte
		Expr  string
	}
	var pending []poolEntry
	dedup := map[poolKey]bool{}

	ldrID, _ := format.MnemonicID("ldr")

	for _, rec := range records {
		switch rec.Kind {
		case format.KindInst:
			if rec.MnemonicID == ldrID {
				if litOp, ok := litPoolOperandFromRec(rec); ok {
					key := poolKey{Width: litOp.Width, Expr: string(litOp.Expr)}
					if !dedup[key] {
						dedup[key] = true
						pending = append(pending, poolEntry{Width: litOp.Width, Expr: append([]byte(nil), litOp.Expr...)})
					}
				}
			}
			pc += 4
		case format.KindDirective:
			n, err := directiveSizeForFlatten(rec, pc, symbols, names)
			if err != nil {
				return 0, err
			}
			if name := format.DirectiveName(rec.DirectiveID); name == ".equ" || name == ".set" {
				if err := captureEqu(rec, symbols, names); err != nil {
					return 0, err
				}
			}
			if name := format.DirectiveName(rec.DirectiveID); name == ".ltorg" {
				pc = flushPoolSize(pc, pending)
				pending = nil
				dedup = map[poolKey]bool{}
				continue
			}
			pc += n
		}
	}
	// Implicit flush at end-of-section for the code section — the Flatten
	// output always emits a trailing .ltorg, so include that flush's bytes
	// in the section size.
	if isCode {
		pc = flushPoolSize(pc, pending)
	}
	return pc, nil
}

// poolEntry mirrors refenc's PoolEntry minus the resolved PC, used
// only by the flatten sizer to track pending pool entries before a
// flush.
type poolEntry struct {
	Width byte
	Expr  []byte
}

// flushPoolSize returns the PC after a literal-pool flush, accounting
// for the same 4-then-8 grouping that refenc uses.
func flushPoolSize(pc int64, entries []poolEntry) int64 {
	var fours, eights int
	for _, e := range entries {
		if e.Width == 4 {
			fours++
		} else {
			eights++
		}
	}
	if fours > 0 && pc%4 != 0 {
		pc += 4 - (pc % 4)
	}
	pc += int64(fours) * 4
	if eights > 0 && pc%8 != 0 {
		pc += 8 - (pc % 8)
	}
	pc += int64(eights) * 8
	return pc
}

// litPoolOperandFromRec returns the OpLitPool operand of an `ldr`
// instruction record if present, otherwise ok=false. Mirrors refenc's
// litPoolOperand.
func litPoolOperandFromRec(rec format.Record) (format.Operand, bool) {
	or := format.NewOperandReader(rec.Operands)
	for !or.AtEnd() {
		o, err := or.Next()
		if err != nil {
			return format.Operand{}, false
		}
		if o.Kind == format.OpLitPool {
			return o, true
		}
	}
	return format.Operand{}, false
}

// directiveSizeForFlatten returns the byte size of a directive in a
// section-local context. Mirrors refenc's directiveSizeAtPC but uses
// a simpler symbol context (only previously-seen .equ/.set constants
// in the same section).
func directiveSizeForFlatten(rec format.Record, pc int64, symbols map[string]int64, names []string) (int64, error) {
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
		o, _ := or.Next()
		v, err := evalLocalExpr(o.Expr, pc, symbols, names)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return v, nil
	case ".inst":
		return 4, nil
	case ".text", ".data", ".global", ".equ", ".set", ".section",
		".arch", ".cpu", ".ltorg":
		return 0, nil
	case ".balign":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		align, err := evalLocalExpr(o.Expr, pc, symbols, names)
		if err != nil {
			return 0, fmt.Errorf(".balign: %w", err)
		}
		if align <= 1 {
			return 0, nil
		}
		pad := (align - (pc % align)) % align
		return pad, nil
	case ".align":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		n, err := evalLocalExpr(o.Expr, pc, symbols, names)
		if err != nil {
			return 0, fmt.Errorf(".align: %w", err)
		}
		if n <= 0 {
			return 0, nil
		}
		align := int64(1) << uint64(n)
		pad := (align - (pc % align)) % align
		return pad, nil
	case ".org":
		// Section-local .org: target relative to section start.
		// (We don't expect .org inside BSS sections in spectrum4, but
		// handle it correctly anyway.)
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		v, err := evalLocalExpr(o.Expr, pc, symbols, names)
		if err != nil {
			return 0, fmt.Errorf(".org: %w", err)
		}
		if v < pc {
			return 0, fmt.Errorf(".org: target 0x%x < current 0x%x", v, pc)
		}
		return v - pc, nil
	}
	return 0, fmt.Errorf("unknown directive %q in flatten sizer", name)
}

// evalLocalExpr evaluates an expression in a section-local context.
// Only constants and previously-defined .set/.equ symbols (in the
// same section) resolve; label references would be forward refs at
// sizing time and surface as "undefined symbol" errors.
func evalLocalExpr(expr []byte, pc int64, symbols map[string]int64, names []string) (int64, error) {
	ctx := enc.EvalContext{
		PC: pc,
		Symbol: func(id uint16) (int64, bool) {
			if int(id) >= len(names) {
				return 0, false
			}
			v, ok := symbols[names[id]]
			return v, ok
		},
	}
	return enc.Eval(expr, ctx)
}

// captureEqu evaluates a `.equ`/`.set` directive and records the
// (name, value) into symbols. Used by the flatten sizer to allow
// section-local references to constants defined earlier in the same
// section.
func captureEqu(rec format.Record, symbols map[string]int64, names []string) error {
	or := format.NewOperandReader(rec.Operands)
	nameOp, err := or.Next()
	if err != nil {
		return err
	}
	id, ok := extractSymIDFromExpr(nameOp.Expr)
	if !ok {
		return fmt.Errorf("first operand of .equ must be a symbol reference")
	}
	if int(id) >= len(names) {
		return fmt.Errorf("symbol id %d out of range", id)
	}
	name := names[id]
	valOp, err := or.Next()
	if err != nil {
		return err
	}
	v, err := evalLocalExpr(valOp.Expr, 0, symbols, names)
	if err != nil {
		return fmt.Errorf(".equ %s: %w", name, err)
	}
	symbols[name] = v
	return nil
}

// extractSymIDFromExpr returns the symbol ID from an expression that
// is exactly a PUSH_SYM instruction. Mirrors refenc's extractSymID.
func extractSymIDFromExpr(expr []byte) (uint16, bool) {
	if len(expr) != 3 || format.ExprOp(expr[0]) != format.OpPushSym {
		return 0, false
	}
	return uint16(expr[1]) | uint16(expr[2])<<8, true
}
