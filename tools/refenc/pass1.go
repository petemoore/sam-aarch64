package main

import (
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// PoolEntry is one slot in the literal pool. It is shared by all
// `ldr Xn|Wn, =expr` instructions whose (width, expr-bytes) match.
type PoolEntry struct {
	Width byte   // 4 or 8
	Expr  []byte // expression bytecode (LSB of the dedup key)
	PC    int64  // where this entry's bytes end up after layout

	// EvalPC is the PC of the *first* `ldr` instruction that
	// references this entry. It is the resolution context for local
	// label references in Expr (e.g. `=10f` resolves relative to the
	// `ldr` site, not the pool slot itself). GNU's behaviour,
	// verified against aarch64-none-elf-as.
	EvalPC int64
}

// Pass1Result holds the symbol table and total program size produced
// by the first pass.
type Pass1Result struct {
	Symbols   map[string]int64
	LocalDefs map[byte][]int64
	TotalSize int64

	// PoolEntries is the list of literal-pool slots in emit order.
	// Pass2 walks this when emitting the bytes at flush points.
	PoolEntries []PoolEntry

	// LdrPoolIdx maps the PC of each `ldr Xn|Wn, =expr` instruction
	// to its index in PoolEntries (so pass2 can read PoolEntries[i].PC).
	LdrPoolIdx map[int64]int

	// PoolFlushAtPC[pc] is the PC at which a flush completes (i.e. the
	// PC immediately after the last pool byte written at that flush).
	// Keyed by the *pre-flush* PC. For a `.ltorg` directive, the
	// directive sits at the pre-flush PC; for end-of-input the
	// pre-flush PC is the final pc value.
	PoolFlushAtPC map[int64]int64

	// PoolFlushEntries[preFlushPC] is the list of pool-entry indices
	// to emit at that flush point.
	PoolFlushEntries map[int64][]int
}

// Pass1 walks records and assigns PC to each instruction / data
// directive, populating the symbol table.
func Pass1(f *format.File) (*Pass1Result, error) {
	res := &Pass1Result{
		Symbols:          make(map[string]int64),
		LocalDefs:        make(map[byte][]int64),
		LdrPoolIdx:       make(map[int64]int),
		PoolFlushAtPC:    make(map[int64]int64),
		PoolFlushEntries: make(map[int64][]int),
	}
	var pc int64
	ldrID, _ := format.MnemonicID("ldr")

	// pendingPool tracks pool entries created since the last flush,
	// keyed by (width, string(expr)). Deduping is by full expression
	// bytes — same as GNU's behaviour (verified empirically against
	// aarch64-none-elf-as: e.g. two `ldr x0,=msg` share one slot).
	type poolKey struct {
		Width byte
		Expr  string
	}
	pending := make(map[poolKey]int) // → index into res.PoolEntries
	var pendingOrder []int           // indices in encounter order

	flushPool := func(flushPC int64) {
		if len(pendingOrder) == 0 {
			return
		}
		// Partition by width: 4-byte entries first (encounter order),
		// then padding to 8 if any 8-byte entries follow, then 8-byte
		// entries (encounter order). GNU's behaviour, verified
		// against aarch64-none-elf-as.
		var fours, eights []int
		for _, i := range pendingOrder {
			if res.PoolEntries[i].Width == 4 {
				fours = append(fours, i)
			} else {
				eights = append(eights, i)
			}
		}
		layout := pc
		if len(fours) > 0 && layout%4 != 0 {
			// Pad to 4-byte alignment before 4-byte literals.
			layout += 4 - (layout % 4)
		}
		for _, i := range fours {
			res.PoolEntries[i].PC = layout
			layout += 4
		}
		if len(eights) > 0 && layout%8 != 0 {
			// Pad to 8-byte alignment before 8-byte literals.
			layout += 8 - (layout % 8)
		}
		for _, i := range eights {
			res.PoolEntries[i].PC = layout
			layout += 8
		}
		ordered := append([]int(nil), pendingOrder...)
		res.PoolFlushEntries[flushPC] = ordered
		res.PoolFlushAtPC[flushPC] = layout
		pc = layout
		pending = make(map[poolKey]int)
		pendingOrder = pendingOrder[:0]
	}

	rr := format.NewRecordReader(f.Records)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			return nil, err
		}
		switch rec.Kind {
		case format.KindLabelDef:
			res.Symbols[f.Names[rec.SymbolID]] = pc
		case format.KindLocalDef:
			res.LocalDefs[rec.Digit] = append(res.LocalDefs[rec.Digit], pc)
		case format.KindInst:
			// Detect `ldr Xn|Wn, =expr` (LitPool form) and register a
			// pool entry. The instruction itself still consumes 4 bytes.
			if rec.MnemonicID == ldrID {
				if litOp, ok := litPoolOperand(rec); ok {
					key := poolKey{Width: litOp.Width, Expr: string(litOp.Expr)}
					idx, seen := pending[key]
					if !seen {
						idx = len(res.PoolEntries)
						res.PoolEntries = append(res.PoolEntries, PoolEntry{
							Width:  litOp.Width,
							Expr:   litOp.Expr,
							EvalPC: pc,
						})
						pending[key] = idx
						pendingOrder = append(pendingOrder, idx)
					}
					res.LdrPoolIdx[pc] = idx
				}
			}
			pc += 4
		case format.KindDirective:
			name := format.DirectiveName(rec.DirectiveID)
			switch name {
			case ".equ", ".set":
				// .equ NAME, value — add to symbol table as a constant.
				if err := resolveEquDirective(rec, f, res); err != nil {
					return nil, fmt.Errorf(".equ: %w", err)
				}
			case ".ltorg":
				flushPool(pc)
			default:
				n, err := directiveSizeAtPC(rec, pc)
				if err != nil {
					return nil, err
				}
				pc += n
			}
		}
	}
	// Implicit flush at end of input.
	flushPool(pc)

	res.TotalSize = pc
	return res, nil
}

// litPoolOperand returns the OpLitPool operand of an `ldr` instruction
// record if present, otherwise ok=false. By construction the parser
// emits at most one OpLitPool per record (as the second operand).
func litPoolOperand(rec format.Record) (format.Operand, bool) {
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

// resolveEquDirective handles .equ/.set directives by evaluating the value
// expression and adding the symbol to the symbol table. The first operand
// of .equ is a symbol-reference expression (PUSH_SYM nameID); the second
// is the constant value expression.
func resolveEquDirective(rec format.Record, f *format.File, res *Pass1Result) error {
	or := format.NewOperandReader(rec.Operands)
	// Operand 1: the symbol being defined.
	symOp, err := or.Next()
	if err != nil {
		return fmt.Errorf("missing symbol operand: %w", err)
	}
	// Evaluate the symbol-ref expression; it should resolve to its own ID
	// via PUSH_SYM. We just need the name, so use EvalConst on the expr:
	// If PUSH_SYM evaluates to 0 (because the symbol isn't in the table yet),
	// we use the expr bytes directly to extract the name ID.
	nameID, ok := extractSymID(symOp.Expr)
	if !ok {
		return fmt.Errorf("first operand of .equ must be a symbol reference")
	}
	name := f.Names[nameID]
	// Operand 2: the value.
	valOp, err := or.Next()
	if err != nil {
		return fmt.Errorf("missing value operand: %w", err)
	}
	v, ok := format.EvalConst(valOp.Expr)
	if !ok {
		return fmt.Errorf(".equ %s: value is not a constant expression", name)
	}
	res.Symbols[name] = v
	return nil
}

// extractSymID returns the symbol ID from an expression that is exactly a
// PUSH_SYM instruction (opcode 0x05 followed by 2-byte LE ID). Returns false
// if the expression doesn't match that shape.
func extractSymID(expr []byte) (uint16, bool) {
	// A PUSH_SYM expr is [0x05, lo, hi] (3 bytes).
	if len(expr) != 3 || format.ExprOp(expr[0]) != format.OpPushSym {
		return 0, false
	}
	return uint16(expr[1]) | uint16(expr[2])<<8, true
}

func directiveSize(rec format.Record) (int64, error) {
	return directiveSizeAtPC(rec, 0)
}

// directiveSizeAtPC computes the byte contribution of a directive
// record at the given PC. For most directives pc is ignored; for
// .balign it is needed to compute the padding.
func directiveSizeAtPC(rec format.Record, pc int64) (int64, error) {
	name := format.DirectiveName(rec.DirectiveID)
	switch name {
	case ".byte":
		return int64(rec.OperandCount), nil
	case ".short":
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
		v, ok := format.EvalConst(o.Expr)
		if !ok {
			return 0, fmt.Errorf(".skip with non-constant operand")
		}
		return v, nil
	case ".inst":
		return 4, nil
	case ".text", ".data", ".global", ".equ", ".set":
		return 0, nil
	case ".section":
		// .section is parsed for syntactic completeness but the current
		// refenc layout emits everything into a single flat stream. See
		// docs/notes/m2-status.md for the multi-section layout gap.
		return 0, nil
	case ".arch", ".cpu":
		// .arch / .cpu are architecture/CPU selection directives. The
		// encoder targets a fixed AArch64 profile and does not
		// feature-gate instructions on these values, so they are
		// treated as zero-byte no-ops.
		return 0, nil
	case ".ltorg":
		// .ltorg's byte contribution is computed by the pool flush
		// logic in Pass1; the directive itself emits no bytes.
		return 0, nil
	case ".balign":
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		align, ok := format.EvalConst(o.Expr)
		if !ok {
			return 0, fmt.Errorf(".balign with non-constant operand")
		}
		if align <= 1 {
			return 0, nil
		}
		pad := (align - (pc % align)) % align
		return pad, nil
	case ".align":
		// aarch64 GNU as convention: `.align N` aligns to 2^N bytes.
		or := format.NewOperandReader(rec.Operands)
		o, _ := or.Next()
		n, ok := format.EvalConst(o.Expr)
		if !ok {
			return 0, fmt.Errorf(".align with non-constant operand")
		}
		if n <= 0 {
			return 0, nil
		}
		align := int64(1) << uint64(n)
		pad := (align - (pc % align)) % align
		return pad, nil
	case ".org":
		return 0, nil
	}
	return 0, fmt.Errorf("unknown directive %q in pass1", name)
}
