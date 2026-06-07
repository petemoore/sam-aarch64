package assemble

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// usage is the package-level Usage tracker. When non-nil, Pass1 / Pass2
// call into its observe* methods at the relevant sites. When nil, the
// observation calls are no-ops (each method has a nil guard at the top).
// main.go installs a tracker behind a --dump-usage flag, leaving the
// nil-tracker path in effect for all other refenc callers (tests, the
// build-spectrum4-release.sh pipeline, etc.).
var usage *Usage

// Usage tracks empirical peak sizes of the data structures used during
// pass-1 + pass-2 of a .tbn file. The Z80-side assembler uses
// fixed-size tables for these; the peaks here are what those tables
// must be sized to absorb (with margin) for the input under test.
//
// All counters are zero-initialised. Hot-path increments are uncondit-
// ional but cheap (a couple of map/int ops); the dump is opt-in via
// the --dump-usage flag in main.go.
type Usage struct {
	// --- Symbol table ---
	// PeakSymbols is the high-water mark of len(Symbols) seen during pass 1.
	// Refenc's symbol map is unbounded; the Z80 SYMTAB has 256 primary
	// buckets + 128 overflow chain entries.
	PeakSymbols int
	// SymNameLens collects every symbol-name length seen for distribution
	// reporting (max / mean / median). Names are added once on first
	// definition in pass 1.
	SymNameLens []int

	// --- Local labels ---
	// PeakLocalDefTotal is the high-water mark of the SUM across all
	// digits (LocalDefs[d]) — Z80 stores them in a single shared list
	// (LOCAL_LABEL_TABLE) capped at LOCAL_LIST_MAX = 180.
	PeakLocalDefTotal int
	// PeakLocalDefPerDigit[d] is the high-water mark of len(LocalDefs[d])
	// for each digit observed.
	PeakLocalDefPerDigit map[byte]int
	// DistinctLocalDigits is the set of digits that received at least
	// one LocalDef record.
	DistinctLocalDigits map[byte]struct{}

	// --- Literal pool ---
	// PeakPoolPending is the maximum number of *active* (pending) pool
	// slots between two flushes. This is the value that
	// LITPOOL_TABLE must accommodate at any single moment.
	PeakPoolPending int
	// MaxDistinctPoolEntries is the cumulative count of pool entries
	// allocated across the WHOLE input (across all .ltorg flushes
	// combined). With the current Z80 design that resets the table at
	// each flush, this is purely informational; with a design that
	// kept the table across flushes it would be the relevant cap.
	MaxDistinctPoolEntries int
	// PoolFlushes counts the number of explicit `.ltorg` flushes. The
	// implicit end-of-input flush is NOT counted here (refenc emits it
	// unconditionally regardless of whether anything's pending).
	PoolFlushes int
	// PoolLdrSites is the cumulative count of LDR-litpool instructions
	// (each one generates a PC_MAP entry on the Z80 side). Z80
	// LITPOOL_PC_MAP is 32 entries; refenc's LdrPoolIdx is unbounded.
	PoolLdrSites int
	// PeakPoolPcMapPending is the maximum number of LDR-litpool sites
	// (pc-map entries) between two flushes. Z80 LITPOOL_PC_MAP is
	// reset at each flush so this is the actual cap, not the total.
	PeakPoolPcMapPending int

	// --- Expression evaluator ---
	// PeakExprStackDepth is the maximum number of values on the evaluator
	// stack at any point during Eval (any call, either pass). The Z80
	// expr_eval module has EXPR_STACK_DEPTH = 8.
	PeakExprStackDepth int
	// PeakExprBytecodeLen is the longest single expression bytecode
	// seen during evaluation (informational; Z80 reads it byte-at-a-
	// time so there's no fixed cap on length).
	PeakExprBytecodeLen int
	// PeakLitpoolExprBytecodeLen is the longest single litpool-expr
	// bytecode (these are stored in LITPOOL_EXPR_BUF on the Z80 side;
	// the relevant size is the SUM of all such exprs, see
	// TotalLitpoolExprBytes).
	PeakLitpoolExprBytecodeLen int
	// TotalLitpoolExprBytes is the total bytes occupied by all
	// litpool-expr bytecodes in PoolEntries. This must fit in the Z80
	// LITPOOL_EXPR_BUF (2 KB post-PR #37).
	TotalLitpoolExprBytes int

	// --- OPVAL operand-buffer width ---
	// PeakInstOperandCount is the maximum number of operands on a single
	// *instruction* record. The Z80 OPVAL_ARRAY is sized for 7 operands
	// × 10 bytes; only instruction records populate it (directives
	// stream one operand at a time).
	PeakInstOperandCount int
	// PeakInstOperandBytes is the maximum total bytes in the Operands
	// payload of a single instruction record.
	PeakInstOperandBytes int
	// PeakDirOperandCount / PeakDirOperandBytes — same for directives.
	// Directives use a single shared payload window (STAGING_BUF) on
	// the Z80 side rather than OPVAL_ARRAY, so the count/bytes peak
	// here is informational.
	PeakDirOperandCount int
	PeakDirOperandBytes int
	// PeakOperandBytes is the maximum total bytes in the Operands
	// payload of any single record. The Z80 STAGING_BUF is 1 KB; the
	// per-record bytes must fit there for paged-IN inputs.
	PeakOperandBytes int

	// --- Record stream ---
	TotalRecords    int
	RecLabelDef     int
	RecLocalDef     int
	RecInst         int
	RecDirective    int
	RecComment      int
	TotalOutBytes   int
}

func newUsage() *Usage {
	return &Usage{
		PeakLocalDefPerDigit: make(map[byte]int),
		DistinctLocalDigits:  make(map[byte]struct{}),
	}
}

// observeSymbolAdd is called from pass1 each time a symbol is inserted
// into the symbol table. It updates PeakSymbols and appends the
// name length.
func (u *Usage) observeSymbolAdd(symbols map[string]int64, name string) {
	if u == nil {
		return
	}
	u.SymNameLens = append(u.SymNameLens, len(name))
	if n := len(symbols); n > u.PeakSymbols {
		u.PeakSymbols = n
	}
}

// observeLocalDefAdd is called each time a local-label record is seen.
func (u *Usage) observeLocalDefAdd(localDefs map[byte][]int64, digit byte) {
	if u == nil {
		return
	}
	u.DistinctLocalDigits[digit] = struct{}{}
	if n := len(localDefs[digit]); n > u.PeakLocalDefPerDigit[digit] {
		u.PeakLocalDefPerDigit[digit] = n
	}
	total := 0
	for _, v := range localDefs {
		total += len(v)
	}
	if total > u.PeakLocalDefTotal {
		u.PeakLocalDefTotal = total
	}
}

// observePoolPending is called after pool entry allocation, before the
// next flush.
func (u *Usage) observePoolPending(pending, pcMapPending int) {
	if u == nil {
		return
	}
	if pending > u.PeakPoolPending {
		u.PeakPoolPending = pending
	}
	if pcMapPending > u.PeakPoolPcMapPending {
		u.PeakPoolPcMapPending = pcMapPending
	}
}

func (u *Usage) observePoolFlush(explicit bool) {
	if u == nil {
		return
	}
	if explicit {
		u.PoolFlushes++
	}
}

func (u *Usage) observePoolEntryAlloc(exprLen int) {
	if u == nil {
		return
	}
	u.MaxDistinctPoolEntries++
	u.TotalLitpoolExprBytes += exprLen
	if exprLen > u.PeakLitpoolExprBytecodeLen {
		u.PeakLitpoolExprBytecodeLen = exprLen
	}
}

func (u *Usage) observePoolLdrSite() {
	if u == nil {
		return
	}
	u.PoolLdrSites++
}

func (u *Usage) observeRecord(rec format.Record) {
	if u == nil {
		return
	}
	u.TotalRecords++
	switch rec.Kind {
	case format.KindLabelDef:
		u.RecLabelDef++
	case format.KindLocalDef:
		u.RecLocalDef++
	case format.KindInst:
		u.RecInst++
		if int(rec.OperandCount) > u.PeakInstOperandCount {
			u.PeakInstOperandCount = int(rec.OperandCount)
		}
		if len(rec.Operands) > u.PeakInstOperandBytes {
			u.PeakInstOperandBytes = len(rec.Operands)
		}
		if len(rec.Operands) > u.PeakOperandBytes {
			u.PeakOperandBytes = len(rec.Operands)
		}
	case format.KindDirective:
		u.RecDirective++
		if int(rec.OperandCount) > u.PeakDirOperandCount {
			u.PeakDirOperandCount = int(rec.OperandCount)
		}
		if len(rec.Operands) > u.PeakDirOperandBytes {
			u.PeakDirOperandBytes = len(rec.Operands)
		}
		if len(rec.Operands) > u.PeakOperandBytes {
			u.PeakOperandBytes = len(rec.Operands)
		}
	case format.KindComment:
		u.RecComment++
	}
}

// EvalUsage runs Eval but tracks the peak stack depth into u. It is a
// drop-in for enc.Eval in instrumented call sites.
//
// We reimplement the evaluator loop here (mirroring aarch64enc/expr.go)
// rather than threading observability into the Eval API itself. The
// counter we want — peak stack depth — is purely the count of values
// currently on the evaluator stack; that's an internal-loop detail that
// matters only when measuring. Keeping the instrumentation here avoids
// perturbing the aarch64enc public API.
func EvalUsage(buf []byte, ctx enc.EvalContext, u *Usage) (int64, error) {
	if u != nil && len(buf) > u.PeakExprBytecodeLen {
		u.PeakExprBytecodeLen = len(buf)
	}
	r := format.NewExprReader(buf)
	stack := make([]int64, 0, 8)
	observe := func() {
		if u != nil && len(stack) > u.PeakExprStackDepth {
			u.PeakExprStackDepth = len(stack)
		}
	}
	for !r.AtEnd() {
		op, operand, err := r.Next()
		if err != nil {
			return 0, err
		}
		switch op {
		case format.OpPushImm8:
			stack = append(stack, int64(int8(operand[0])))
			observe()
		case format.OpPushImm16:
			stack = append(stack, int64(int16(binary.LittleEndian.Uint16(operand))))
			observe()
		case format.OpPushImm32:
			stack = append(stack, int64(int32(binary.LittleEndian.Uint32(operand))))
			observe()
		case format.OpPushImm64:
			stack = append(stack, int64(binary.LittleEndian.Uint64(operand)))
			observe()
		case format.OpPushSym:
			id := binary.LittleEndian.Uint16(operand)
			if ctx.Symbol == nil {
				return 0, fmt.Errorf("eval: PUSH_SYM with no symbol resolver")
			}
			v, ok := ctx.Symbol(id)
			if !ok {
				return 0, fmt.Errorf("eval: undefined symbol id %d", id)
			}
			stack = append(stack, v)
			observe()
		case format.OpPushLocal:
			digit := operand[0]
			dir := operand[1]
			if ctx.LocalLabel == nil {
				return 0, fmt.Errorf("eval: PUSH_LOCAL with no local resolver")
			}
			v, ok := ctx.LocalLabel(digit, dir)
			if !ok {
				dirCh := byte('f')
				if dir == 1 {
					dirCh = 'b'
				}
				return 0, fmt.Errorf("eval: no %d%c", digit, dirCh)
			}
			stack = append(stack, v)
			observe()
		case format.OpPushPC:
			stack = append(stack, ctx.PC)
			observe()
		case format.OpAdd, format.OpSub, format.OpMul, format.OpDiv,
			format.OpAnd, format.OpOr, format.OpXor, format.OpShl, format.OpShr:
			if len(stack) < 2 {
				return 0, fmt.Errorf("eval: stack underflow at %v", op)
			}
			b := stack[len(stack)-1]
			a := stack[len(stack)-2]
			stack = stack[:len(stack)-2]
			stack = append(stack, applyBinaryUsage(op, a, b))
			observe()
		case format.OpNeg:
			stack[len(stack)-1] = -stack[len(stack)-1]
		case format.OpNot:
			stack[len(stack)-1] = ^stack[len(stack)-1]
		case format.OpRelLo12:
			stack[len(stack)-1] = stack[len(stack)-1] & 0xFFF
		case format.OpRelHi12:
			stack[len(stack)-1] = (stack[len(stack)-1] >> 12) & 0xFFF
		case format.OpRelAbsG0, format.OpRelAbsG0NC:
			stack[len(stack)-1] = stack[len(stack)-1] & 0xFFFF
		case format.OpRelAbsG1, format.OpRelAbsG1NC:
			stack[len(stack)-1] = (stack[len(stack)-1] >> 16) & 0xFFFF
		case format.OpRelAbsG2, format.OpRelAbsG2NC:
			stack[len(stack)-1] = (stack[len(stack)-1] >> 32) & 0xFFFF
		case format.OpRelAbsG3:
			stack[len(stack)-1] = (stack[len(stack)-1] >> 48) & 0xFFFF
		default:
			return 0, fmt.Errorf("eval: unknown opcode %v", op)
		}
	}
	if len(stack) != 1 {
		return 0, fmt.Errorf("eval: stack ended with %d values", len(stack))
	}
	return stack[0], nil
}

func applyBinaryUsage(op format.ExprOp, a, b int64) int64 {
	switch op {
	case format.OpAdd:
		return a + b
	case format.OpSub:
		return a - b
	case format.OpMul:
		return a * b
	case format.OpDiv:
		if b == 0 {
			return 0
		}
		return a / b
	case format.OpAnd:
		return a & b
	case format.OpOr:
		return a | b
	case format.OpXor:
		return a ^ b
	case format.OpShl:
		return a << uint64(b)
	case format.OpShr:
		return a >> uint64(b)
	}
	return 0
}

// Dump writes a human-readable summary of u to w.
func (u *Usage) Dump(w io.Writer) {
	fmt.Fprintln(w, "=== sam-aarch64 --dump-usage ===")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Record stream:")
	fmt.Fprintf(w, "  Total records:    %d\n", u.TotalRecords)
	fmt.Fprintf(w, "    Inst:           %d\n", u.RecInst)
	fmt.Fprintf(w, "    LabelDef:       %d\n", u.RecLabelDef)
	fmt.Fprintf(w, "    LocalDef:       %d\n", u.RecLocalDef)
	fmt.Fprintf(w, "    Directive:      %d\n", u.RecDirective)
	fmt.Fprintf(w, "    Comment:        %d\n", u.RecComment)
	fmt.Fprintf(w, "  Total OUT bytes:  %d\n", u.TotalOutBytes)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Symbol table (Z80 SYMTAB = 256 buckets + 128 overflow = 384 entries):")
	fmt.Fprintf(w, "  Peak symbol count:   %d\n", u.PeakSymbols)
	if n := len(u.SymNameLens); n > 0 {
		max, sum := 0, 0
		lens := append([]int(nil), u.SymNameLens...)
		sort.Ints(lens)
		for _, l := range lens {
			if l > max {
				max = l
			}
			sum += l
		}
		mean := float64(sum) / float64(n)
		median := lens[n/2]
		fmt.Fprintf(w, "  Name lengths:        max=%d, mean=%.1f, median=%d (n=%d)\n",
			max, mean, median, n)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Local labels (Z80 LOCAL_LABEL_TABLE: 180 shared-list entries):")
	fmt.Fprintf(w, "  Peak total entries:  %d\n", u.PeakLocalDefTotal)
	fmt.Fprintf(w, "  Distinct digits:     %d\n", len(u.DistinctLocalDigits))
	if len(u.PeakLocalDefPerDigit) > 0 {
		digits := make([]int, 0, len(u.PeakLocalDefPerDigit))
		for d := range u.PeakLocalDefPerDigit {
			digits = append(digits, int(d))
		}
		sort.Ints(digits)
		fmt.Fprintf(w, "  Per-digit peak:\n")
		for _, d := range digits {
			fmt.Fprintf(w, "    digit %d: %d\n", d, u.PeakLocalDefPerDigit[byte(d)])
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Literal pool (Z80 LITPOOL_TABLE = 32 slots, LITPOOL_PC_MAP = 32):")
	fmt.Fprintf(w, "  Peak active slots (between flushes):  %d\n", u.PeakPoolPending)
	fmt.Fprintf(w, "  Peak active PC-map entries:           %d\n", u.PeakPoolPcMapPending)
	fmt.Fprintf(w, "  Total distinct pool entries (input):  %d\n", u.MaxDistinctPoolEntries)
	fmt.Fprintf(w, "  Total LDR-litpool sites:              %d\n", u.PoolLdrSites)
	fmt.Fprintf(w, "  Explicit .ltorg flushes:              %d\n", u.PoolFlushes)
	fmt.Fprintf(w, "  Peak single litpool-expr bytecode:    %d B\n", u.PeakLitpoolExprBytecodeLen)
	fmt.Fprintf(w, "  Total litpool-expr bytecode bytes:    %d (Z80 LITPOOL_EXPR_BUF = 2048 B)\n",
		u.TotalLitpoolExprBytes)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Expression evaluator (Z80 EXPR_STACK_DEPTH = 8):")
	fmt.Fprintf(w, "  Peak evaluator stack depth:  %d\n", u.PeakExprStackDepth)
	fmt.Fprintf(w, "  Peak expression bytecode:    %d B\n", u.PeakExprBytecodeLen)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "OPVAL operand buffer (Z80 OPVAL_ARRAY = 7 operands × 10 B = 70 B):")
	fmt.Fprintf(w, "  Peak operand count per INST record:       %d\n", u.PeakInstOperandCount)
	fmt.Fprintf(w, "  Peak operand-bytes per INST record:       %d\n", u.PeakInstOperandBytes)
	fmt.Fprintf(w, "  Peak operand count per DIRECTIVE record:  %d (informational; directives don't use OPVAL)\n",
		u.PeakDirOperandCount)
	fmt.Fprintf(w, "  Peak operand-bytes per DIRECTIVE record:  %d\n", u.PeakDirOperandBytes)
	fmt.Fprintf(w, "  Peak operand-bytes any record:            %d (Z80 STAGING_BUF = 1024 B)\n",
		u.PeakOperandBytes)
}
