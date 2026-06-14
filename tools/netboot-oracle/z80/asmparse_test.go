// asmparse_test.go — host-verification of src/asmparse.asm (i48c: the aarch64
// assembler-source parser; Bricks B2a–B4).
//
// B2a (mnemonic_lookup): drives the lookup under the flat-memory koron-go/z80
// harness and asserts the returned ID matches format.MnemonicID for every name
// in MnemonicTable, plus a batch of non-mnemonics (asserting not-found).
//
// B2b/B2c/B3a..B3c (parse_run / parse_inst): drives the instruction parse and
// compares the emitted INST records against a faithful Go reference. The
// operand-byte authority is the real format.OperandWriter + format.ExprWriter +
// format.EvalConst + format.SymbolTable (imported directly); the lexing reuses
// refLex (already authority-validated in asmlex_test.go); only the small
// control-flow port (matchReg + the generic operand loop + the precedence-
// climbing expression parser) is transcribed — refMatchReg / refTokPrec /
// refExprParser below are verbatim parser.go matchReg / tokPrec / parseExprPrec
// + parseExprPrimary, covering the complete binary-operator set
// `+ - & | ^ << >> * /` + unary `- ~` + parens + symbol identifiers (interned
// via a per-document format.SymbolTable) + PC (`.`) + local-refs + reloc ops.
// The remaining out-of-domain constructs are blank-runs/labels/comments/
// directives, and the special-form mnemonics parseInst intercepts before its
// generic loop; on the in-domain subset refParse mirrors Parse exactly. B4
// (memory operands) is covered by refParseWithMem / TestParseMemHandCases /
// TestParseMemFuzz below.
//
// Unlike asmlex_test.go (which transcribes the heavyweight frontend lexer), the
// authority here is the pure-stdlib leaf package sam-aarch64-format, imported
// directly. The committed src/mnemonic_names.inc is itself generated from that
// same authority (tables-gen) and guarded against drift by `make tables-sync-check`.
package z80_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

const (
	apBinPath = "../../../build/asmparse.bin"
	apMapPath = "../../../build/asmparse.map"
)

func loadAsmparse(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(apBinPath); err != nil {
		t.Skipf("asmparse binary not built (%s); run `make asmparse-z80`", apBinPath)
	}
	mac, err := z80h.Load(apBinPath, apMapPath)
	if err != nil {
		t.Fatalf("load asmparse: %v", err)
	}
	return mac
}

// lookupZ80 writes name into AP_NAMEBUF and runs mnemonic_lookup, returning the
// found flag (A==1) and the returned ID (HL, valid only when found).
func lookupZ80(t *testing.T, mac *z80h.Machine, name string) (found bool, id uint16) {
	t.Helper()
	buf, _ := mac.Sym("AP_NAMEBUF")
	mac.Write(buf, []byte(name))
	res, err := mac.CallEntry("mnemonic_lookup", z80h.Entry{HL: buf, BC: uint16(len(name))})
	if err != nil {
		t.Fatalf("mnemonic_lookup(%q): %v", name, err)
	}
	return res.A == 1, res.HL
}

// TestMnemonicLookupAll drives mnemonic_lookup over every name in the Go
// authority's MnemonicTable and asserts the Z80 returns its on-disk ID (the
// table index), cross-checked against format.MnemonicID.
func TestMnemonicLookupAll(t *testing.T) {
	mac := loadAsmparse(t)
	for wantID, name := range format.MnemonicTable {
		// Sanity: the authority agrees the index is the ID.
		if gotID, ok := format.MnemonicID(name); !ok || int(gotID) != wantID {
			t.Fatalf("authority inconsistency: MnemonicID(%q) = %d,%v want %d", name, gotID, ok, wantID)
		}
		found, id := lookupZ80(t, mac, name)
		if !found {
			t.Errorf("mnemonic_lookup(%q): not found, want id %d", name, wantID)
			continue
		}
		if int(id) != wantID {
			t.Errorf("mnemonic_lookup(%q): id = %d, want %d", name, id, wantID)
		}
	}
	t.Logf("verified %d mnemonics resolve to their MnemonicTable index", len(format.MnemonicTable))
}

// TestMnemonicLookupNonMnemonics asserts that strings which are NOT mnemonics
// resolve to not-found — including registers, near-misses (prefixes/suffixes of
// real mnemonics), the empty string, and identifiers the lexer would produce
// but that are not in the table. Each is cross-checked against the authority.
func TestMnemonicLookupNonMnemonics(t *testing.T) {
	mac := loadAsmparse(t)
	nonMnemonics := []string{
		"", "x0", "w5", "sp", "lr", "loop", "_start", "foo", ".text",
		"ad", "ad2", "addd", "adda", "nopp", "no", "sub2", "movx",
		"b.zz", "b.e", "b.eqq", "ccm", "csne", "csnegg", "ABCD", "Add",
	}
	for _, name := range nonMnemonics {
		// The authority must agree these are not mnemonics; if a future table
		// adds one, this test (not just the Z80) should be updated.
		if _, ok := format.MnemonicID(name); ok {
			t.Fatalf("test bug: %q is actually a mnemonic in the authority", name)
		}
		found, id := lookupZ80(t, mac, name)
		if found {
			t.Errorf("mnemonic_lookup(%q): found id %d, want not-found", name, id)
		}
	}
}

// ---------------------------------------------------------------------------
// B2b — register-instruction parse (parse_run / parse_inst).
// ---------------------------------------------------------------------------

// parseRec is one decoded INST record: mnemonic ID, operand count, and the raw
// operand byte stream (format.OperandWriter form).
type parseRec struct {
	mnemonicID uint16
	count      byte
	ops        []byte
}

// refMatchReg is a verbatim port of parser.go's matchReg (the register-name
// recogniser). Returns the operand kind, register number, and ok.
func refMatchReg(name string) (format.OperandKind, byte, bool) {
	switch name {
	case "sp":
		return format.OpRegXSP, 31, true
	case "wsp":
		return format.OpRegWSP, 31, true
	case "xzr":
		return format.OpRegX, 31, true
	case "wzr":
		return format.OpRegW, 31, true
	case "fp":
		return format.OpRegX, 29, true
	case "lr":
		return format.OpRegX, 30, true
	}
	if len(name) < 2 {
		return 0, 0, false
	}
	prefix := name[0]
	if prefix != 'x' && prefix != 'w' {
		return 0, 0, false
	}
	num := 0
	for _, c := range []byte(name[1:]) {
		if c < '0' || c > '9' {
			return 0, 0, false
		}
		num = num*10 + int(c-'0')
		if num > 30 {
			return 0, 0, false
		}
	}
	if prefix == 'x' {
		return format.OpRegX, byte(num), true
	}
	return format.OpRegW, byte(num), true
}

// refTokPrec is a transcription of parser.go's tokPrec over the full B3a..B3a4
// operator set: `| ^` (0), `&` (1), `<< >>` (2), `+ -` (3), `* /` (4). With
// division added this is now verbatim parser.go tokPrec for every operator the
// lexer emits.
func refTokPrec(k int) int {
	switch k {
	case tPipe, tCaret:
		return 0
	case tAmp:
		return 1
	case tShl, tShr:
		return 2
	case tPlus, tMinus:
		return 3
	case tStar, tSlash:
		return 4
	}
	return -1
}

// refRelocOp is a verbatim port of parser.go's relocOp: maps a relocation name
// string to its ExprOp byte. Returns (op, true) on success; (0, false) otherwise.
func refRelocOp(name string) (format.ExprOp, bool) {
	switch name {
	case "lo12":
		return format.OpRelLo12, true
	case "hi12":
		return format.OpRelHi12, true
	case "abs_g0":
		return format.OpRelAbsG0, true
	case "abs_g0_nc":
		return format.OpRelAbsG0NC, true
	case "abs_g1":
		return format.OpRelAbsG1, true
	case "abs_g1_nc":
		return format.OpRelAbsG1NC, true
	case "abs_g2":
		return format.OpRelAbsG2, true
	case "abs_g2_nc":
		return format.OpRelAbsG2NC, true
	case "abs_g3":
		return format.OpRelAbsG3, true
	}
	return 0, false
}

// refExprParser is a precedence-climbing expression parser over a refTok slice,
// a verbatim transcription of parser.go's parseExprPrec + parseExprPrimary.
// It builds bytecode into a real format.ExprWriter and interns symbol identifiers
// via the real format.SymbolTable, exactly as the Go authority does.
type refExprParser struct {
	toks []refTok
	pos  int
	st   *format.SymbolTable
}

func (p *refExprParser) cur() refTok {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return refTok{kind: tEOF}
}

func (p *refExprParser) parsePrec(w *format.ExprWriter, minPrec int) bool {
	if !p.parsePrimary(w) {
		return false
	}
	for {
		k := p.cur().kind
		prec := refTokPrec(k)
		if prec < minPrec {
			return true
		}
		opKind := k
		p.pos++
		if !p.parsePrec(w, prec+1) {
			return false
		}
		switch opKind {
		case tPlus:
			w.WriteOp(format.OpAdd)
		case tMinus:
			w.WriteOp(format.OpSub)
		case tAmp:
			w.WriteOp(format.OpAnd)
		case tPipe:
			w.WriteOp(format.OpOr)
		case tCaret:
			w.WriteOp(format.OpXor)
		case tShl:
			w.WriteOp(format.OpShl)
		case tShr:
			w.WriteOp(format.OpShr)
		case tStar:
			w.WriteOp(format.OpMul)
		case tSlash:
			w.WriteOp(format.OpDiv)
		}
	}
}

func (p *refExprParser) parsePrimary(w *format.ExprWriter) bool {
	switch p.cur().kind {
	case tHash:
		p.pos++
		return p.parsePrimary(w)
	case tInt:
		w.WriteImm(int64(p.cur().val))
		p.pos++
		return true
	case tIdent:
		id := p.st.Intern(string(p.cur().span))
		w.WriteSym(id)
		p.pos++
		return true
	case tDot:
		w.WritePC()
		p.pos++
		return true
	case tLocalRef:
		digit := byte(p.cur().val)
		dir := byte(0)
		if p.cur().base == int('b') {
			dir = 1
		}
		w.WriteLocal(digit, dir)
		p.pos++
		return true
	case tColon:
		p.pos++ // consume ':'
		if p.cur().kind != tIdent {
			return false
		}
		name := string(p.cur().span)
		p.pos++ // consume name
		if p.cur().kind != tColon {
			return false
		}
		p.pos++ // consume ':'
		if !p.parsePrimary(w) {
			return false
		}
		op, ok := refRelocOp(name)
		if !ok {
			return false
		}
		w.WriteOp(op)
		return true
	case tMinus:
		p.pos++
		if !p.parsePrimary(w) {
			return false
		}
		w.WriteOp(format.OpNeg)
		return true
	case tTilde:
		p.pos++
		if !p.parsePrimary(w) {
			return false
		}
		w.WriteOp(format.OpNot)
		return true
	case tLParen:
		p.pos++
		if !p.parsePrec(w, 0) {
			return false
		}
		if p.cur().kind != tRParen {
			return false
		}
		p.pos++
		return true
	}
	return false
}

// refParseExpr parses one expression operand starting at toks[pos], interning
// symbols into st, and returns the operand bytecode (a folded immediate when the
// expression is constant; otherwise the raw stream, matching parseExpression +
// parse_operand_expr/expr_fold), the position after the expression, and ok.
func refParseExpr(toks []refTok, pos int, st *format.SymbolTable) (expr []byte, newPos int, ok bool) {
	p := refExprParser{toks: toks, pos: pos, st: st}
	var w format.ExprWriter
	if !p.parsePrec(&w, 0) {
		return nil, pos, false
	}
	if v, ok := format.EvalConst(w.Bytes()); ok {
		var folded format.ExprWriter
		folded.WriteImm(v)
		return folded.Bytes(), p.pos, true
	}
	return w.Bytes(), p.pos, true
}

// refParse is a faithful transcription of parser.go's Parse over the in-domain
// subset (register operands + constant/symbol expression operands). It lexes via
// refLex (the authority-validated lexer reference), looks up mnemonics via
// format.MnemonicID, interns symbols into a per-document format.SymbolTable, and
// builds operand bytes via the real format.OperandWriter / format.ExprWriter /
// format.EvalConst. Returns ok=false on any out-of-domain construct, matching
// how asmparse sets PARSE_ERR.
func refParse(src []byte) (recs []parseRec, ok bool) {
	toks, lok := refLex(src)
	if !lok {
		return nil, false
	}
	st := format.NewSymbolTable() // one symbol table per document, like parse_run
	pos := 0
	for {
		if pos >= len(toks) {
			return recs, true
		}
		switch toks[pos].kind {
		case tEOF:
			return recs, true
		case tEOL:
			pos++
		case tIdent:
			id, found := format.MnemonicID(string(toks[pos].span))
			if !found {
				return nil, false // directive / label / unknown -> out of domain
			}
			pos++
			var ow format.OperandWriter
			count := byte(0)
			for {
				if pos >= len(toks) {
					return nil, false
				}
				k := toks[pos].kind
				if k == tEOL || k == tEOF || k == tLineComment || k == tBlockComment {
					break
				}
				if k == tComma {
					if count == 0 {
						return nil, false
					}
					pos++
					continue
				}
				switch k {
				case tIdent:
					rk, reg, isReg := refMatchReg(string(toks[pos].span))
					if isReg {
						pos++ // consume register
						// B4b shift/extend lookahead: only for X or W registers.
						if (rk == format.OpRegX || rk == format.OpRegW) &&
							pos < len(toks) && toks[pos].kind == tComma &&
							pos+1 < len(toks) && toks[pos+1].kind == tIdent {
							next := string(toks[pos+1].span)
							if sk, skOk := refMatchShiftKind(next); skOk {
								pos += 2 // consume comma + shift keyword
								if pos >= len(toks) || toks[pos].kind != tHash {
									return nil, false
								}
								pos++ // consume '#'
								expr, npos, ok2 := refParseExpr(toks, pos, st)
								if !ok2 {
									return nil, false
								}
								pos = npos
								width := byte(0)
								if rk == format.OpRegX {
									width = 1
								}
								ow.WriteShiftedReg(width, reg, sk, expr)
								count++
								break
							}
							if ek, ekOk := refMatchExtend(next); ekOk {
								pos += 2 // consume comma + extend keyword
								var amt []byte
								if pos < len(toks) && toks[pos].kind == tHash {
									pos++ // consume '#'
									a, npos, ok2 := refParseExpr(toks, pos, st)
									if !ok2 {
										return nil, false
									}
									amt = a
									pos = npos
								}
								width := byte(0)
								if rk == format.OpRegX {
									width = 1
								}
								ow.WriteExtendedReg(width, reg, ek, amt)
								count++
								break
							}
						}
						// plain register (no shift/extend, or XSP/WSP)
						ow.WriteReg(rk, reg)
						count++
						break
					}
					// A non-register identifier is a symbol expression (B3b).
					fallthrough
				case tHash, tInt, tMinus, tTilde, tLParen,
					tDot, tLocalRef, tColon:
					// An expression-leading operand: constant/symbol/PC/local/reloc.
					// Constant exprs fold to an immediate; non-constant keep raw
					// bytecode.
					expr, npos, ok := refParseExpr(toks, pos, st)
					if !ok {
						return nil, false
					}
					ow.WriteImmExpr(expr)
					count++
					pos = npos
				default:
					return nil, false // mem operand / label / directive -> later brick
				}
			}
			recs = append(recs, parseRec{mnemonicID: id, count: count, ops: ow.Bytes()})
		default:
			return nil, false
		}
	}
}

// parseZ80 writes src to LEX_SRC, runs parse_run, and reads back the emitted
// INST records plus the PARSE_ERR flag.
func parseZ80(t *testing.T, mac *z80h.Machine, src []byte) (recs []parseRec, errFlag bool) {
	t.Helper()
	symSrc, _ := mac.Sym("LEX_SRC")
	symRecs, _ := mac.Sym("PARSE_RECS")
	symErr, _ := mac.Sym("PARSE_ERR")

	mac.Write(symSrc, src)
	res, err := mac.CallEntry("parse_run", z80h.Entry{BC: uint16(len(src))})
	if err != nil {
		t.Fatalf("parse_run: %v", err)
	}
	count := int(res.BC)
	addr := symRecs
	for i := 0; i < count; i++ {
		hdr := mac.Read(addr, 5)
		mnem := uint16(hdr[0]) | uint16(hdr[1])<<8
		opc := hdr[2]
		opslen := int(uint16(hdr[3]) | uint16(hdr[4])<<8)
		var ops []byte
		if opslen > 0 {
			ops = mac.Read(addr+5, opslen)
		}
		recs = append(recs, parseRec{mnemonicID: mnem, count: opc, ops: ops})
		addr += uint16(5 + opslen)
	}
	errFlag = mac.Read(symErr, 1)[0] != 0
	return recs, errFlag
}

func dumpRecs(recs []parseRec) string {
	s := ""
	for _, r := range recs {
		s += fmt.Sprintf("[%s c=%d ops=%x] ", format.MnemonicName(r.mnemonicID), r.count, r.ops)
	}
	return s
}

func compareRecs(t *testing.T, label string, got, want []parseRec) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d records, want %d\n got:  %s\n want: %s",
			label, len(got), len(want), dumpRecs(got), dumpRecs(want))
	}
	for i := range want {
		if got[i].mnemonicID != want[i].mnemonicID {
			t.Errorf("%s: rec[%d] mnemonic = %s, want %s", label, i,
				format.MnemonicName(got[i].mnemonicID), format.MnemonicName(want[i].mnemonicID))
		}
		if got[i].count != want[i].count {
			t.Errorf("%s: rec[%d] (%s) operand count = %d, want %d", label, i,
				format.MnemonicName(want[i].mnemonicID), got[i].count, want[i].count)
		}
		if !bytes.Equal(got[i].ops, want[i].ops) {
			t.Errorf("%s: rec[%d] (%s) ops = %x, want %x", label, i,
				format.MnemonicName(want[i].mnemonicID), got[i].ops, want[i].ops)
		}
	}
}

// reg is a register-operand description for the hand-case builder.
type reg struct {
	k format.OperandKind
	r byte
}

// mkRec builds the expected INST record for `mnem` with register operands,
// using the authority (format.MnemonicID + the real format.OperandWriter).
func mkRec(t *testing.T, mnem string, regs ...reg) parseRec {
	t.Helper()
	id, ok := format.MnemonicID(mnem)
	if !ok {
		t.Fatalf("mkRec: %q is not a mnemonic", mnem)
	}
	var ow format.OperandWriter
	for _, rg := range regs {
		ow.WriteReg(rg.k, rg.r)
	}
	return parseRec{mnemonicID: id, count: byte(len(regs)), ops: ow.Bytes()}
}

// TestParseInstHandCases pins explicit INST records (authority-built via
// format.MnemonicID + format.OperandWriter) for representative register
// instruction lines, then also full-checks against refParse.
func TestParseInstHandCases(t *testing.T) {
	mac := loadAsmparse(t)
	x := func(n byte) reg { return reg{format.OpRegX, n} }
	w := func(n byte) reg { return reg{format.OpRegW, n} }
	cases := []struct {
		src  string
		want []parseRec
	}{
		// zero-operand, one-operand, two-operand, three-operand
		{"ret\n", []parseRec{mkRec(t, "ret")}},
		{"br x0\n", []parseRec{mkRec(t, "br", x(0))}},
		{"mov x0, x1\n", []parseRec{mkRec(t, "mov", x(0), x(1))}},
		{"add x0, x1, x2\n", []parseRec{mkRec(t, "add", x(0), x(1), x(2))}},
		// commas are optional separators (parser.go) — same record as above
		{"add x0 x1 x2\n", []parseRec{mkRec(t, "add", x(0), x(1), x(2))}},
		// W registers
		{"sub w3, w4, w5\n", []parseRec{mkRec(t, "sub", w(3), w(4), w(5))}},
		// two-digit register numbers
		{"add x29, x30, x0\n", []parseRec{mkRec(t, "add", x(29), x(30), x(0))}},
		// named special registers (sp/wsp/xzr/wzr/fp/lr)
		{"mov sp, x0\n", []parseRec{mkRec(t, "mov", reg{format.OpRegXSP, 31}, x(0))}},
		{"add x0, xzr, x1\n", []parseRec{mkRec(t, "add", x(0), x(31), x(1))}},
		{"mov fp, lr\n", []parseRec{mkRec(t, "mov", reg{format.OpRegX, 29}, reg{format.OpRegX, 30})}},
		{"sub wsp, wsp, wzr\n", []parseRec{mkRec(t, "sub", reg{format.OpRegWSP, 31}, reg{format.OpRegWSP, 31}, reg{format.OpRegW, 31})}},
		// multi-line: two records
		{"mov x0, x1\nret\n", []parseRec{mkRec(t, "mov", x(0), x(1)), mkRec(t, "ret")}},
		// blank-free multi-line with assorted arities
		{"add x0, x1, x2\nsub x3, x4, x5\nmov x6, x7\n", []parseRec{
			mkRec(t, "add", x(0), x(1), x(2)),
			mkRec(t, "sub", x(3), x(4), x(5)),
			mkRec(t, "mov", x(6), x(7)),
		}},
	}
	for _, c := range cases {
		got, errFlag := parseZ80(t, mac, []byte(c.src))
		if errFlag {
			t.Errorf("%q: PARSE_ERR set unexpectedly", c.src)
			continue
		}
		compareRecs(t, fmt.Sprintf("%q", c.src), got, c.want)
		// Cross-check the reference parser agrees with the hand-built records.
		ref, ok := refParse([]byte(c.src))
		if !ok {
			t.Fatalf("%q: refParse reported error on a valid case", c.src)
		}
		compareRecs(t, fmt.Sprintf("%q (vs refParse)", c.src), got, ref)
	}
}

// opspec describes one operand for the mixed-operand hand-case builder: a
// register, a folded immediate, a single symbol reference, or a raw expression
// bytecode stream (built by a closure, for non-constant symbol expressions).
type opspec struct {
	isImm   bool
	isSym   bool
	isExpr  bool
	k       format.OperandKind // register kind (plain register operand)
	r       byte               // register number
	imm     int64              // immediate value (isImm)
	symID   uint16             // symbol id (isSym)
	exprRaw []byte             // raw expression bytecode (isExpr)
}

func rOp(k format.OperandKind, r byte) opspec { return opspec{k: k, r: r} }
func iOp(v int64) opspec                      { return opspec{isImm: true, imm: v} }

// sOp is a single-symbol operand carrying the given (first-encounter) id.
func sOp(id uint16) opspec { return opspec{isSym: true, symID: id} }

// xOp is an arbitrary expression operand; build runs against a real
// format.ExprWriter to produce the exact (unfolded) bytecode.
func xOp(build func(w *format.ExprWriter)) opspec {
	var ew format.ExprWriter
	build(&ew)
	return opspec{isExpr: true, exprRaw: ew.Bytes()}
}

// mkRec2 builds the expected INST record for a mnemonic with mixed register,
// immediate, symbol, and raw-expression operands, using the authority
// (format.MnemonicID + the real format.OperandWriter / format.ExprWriter).
func mkRec2(t *testing.T, mnem string, ops ...opspec) parseRec {
	t.Helper()
	id, ok := format.MnemonicID(mnem)
	if !ok {
		t.Fatalf("mkRec2: %q is not a mnemonic", mnem)
	}
	var ow format.OperandWriter
	for _, o := range ops {
		switch {
		case o.isImm:
			var ew format.ExprWriter
			ew.WriteImm(o.imm)
			ow.WriteImmExpr(ew.Bytes())
		case o.isSym:
			var ew format.ExprWriter
			ew.WriteSym(o.symID)
			ow.WriteImmExpr(ew.Bytes())
		case o.isExpr:
			ow.WriteImmExpr(o.exprRaw)
		default:
			ow.WriteReg(o.k, o.r)
		}
	}
	return parseRec{mnemonicID: id, count: byte(len(ops)), ops: ow.Bytes()}
}

// TestParseImmHandCases pins INST records for register/#imm instruction lines,
// exercising each PUSH_IMMn width boundary (8/16/32/64-bit) and the signed
// wraparound of large hex literals, then full-checks against refParse.
func TestParseImmHandCases(t *testing.T) {
	mac := loadAsmparse(t)
	X := format.OpRegX
	cases := []struct {
		src  string
		want []parseRec
	}{
		{"add x0, x1, #4\n", []parseRec{mkRec2(t, "add", rOp(X, 0), rOp(X, 1), iOp(4))}},
		{"mov x0, #0\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		{"sub sp, sp, #16\n", []parseRec{mkRec2(t, "sub", rOp(format.OpRegXSP, 31), rOp(format.OpRegXSP, 31), iOp(16))}},
		// bare immediate (no '#') is also a valid operand
		{"mov x0, 255\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(255))}},
		// width boundaries: 127→imm8, 128→imm16, 32768→imm16, 65536→imm32, 2^31→imm32, 2^32→imm64
		{"mov x0, #127\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(127))}},
		{"mov x0, #128\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(128))}},
		{"mov x0, #32768\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(32768))}},
		{"mov x0, #0x10000\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0x10000))}},
		{"mov x0, #0x80000000\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0x80000000))}},
		// large hex wraps to a negative int64 -> WriteImm picks the short form
		{"mov x0, #0xffffffffffffffff\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-1))}},
		{"mov x0, #0x7fffffffffffffff\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0x7fffffffffffffff))}},
		// char-literal immediate
		{"mov x0, #'A'\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp('A'))}},
		// immediate-only operand and a reg after an imm (commas optional)
		{"add x0 #1 x2\n", []parseRec{mkRec2(t, "add", rOp(X, 0), iOp(1), rOp(X, 2))}},
	}
	for _, c := range cases {
		got, errFlag := parseZ80(t, mac, []byte(c.src))
		if errFlag {
			t.Errorf("%q: PARSE_ERR set unexpectedly", c.src)
			continue
		}
		compareRecs(t, fmt.Sprintf("%q", c.src), got, c.want)
		ref, ok := refParse([]byte(c.src))
		if !ok {
			t.Fatalf("%q: refParse reported error on a valid case", c.src)
		}
		compareRecs(t, fmt.Sprintf("%q (vs refParse)", c.src), got, ref)
	}
}

// ---------------------------------------------------------------------------
// B3a — constant-expression operands (parse_expr_prec / parse_expr_primary /
// expr_fold).
// ---------------------------------------------------------------------------

// TestParseExprHandCases pins INST records for constant-expression operands,
// exercising the B3a operators (`+ - & | ^`, unary `- ~`), operator precedence,
// parentheses, and the fold's width re-selection. Every B3a expression is
// pure-constant, so each folds to a single immediate; the expected record is
// authority-built (format.ExprWriter.WriteImm of the folded value). Cross-checks
// against refParse.
func TestParseExprHandCases(t *testing.T) {
	mac := loadAsmparse(t)
	X := format.OpRegX
	cases := []struct {
		src  string
		want []parseRec
	}{
		// binary operators, each width-folding to a single immediate
		{"add x0, x1, #4+1\n", []parseRec{mkRec2(t, "add", rOp(X, 0), rOp(X, 1), iOp(5))}},
		{"mov x0, #1+2+3\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(6))}},
		{"mov x0, #8-3-2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(3))}}, // left-assoc
		{"mov x0, #0xff & 0x0f\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0x0f))}},
		{"mov x0, #1 | 2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(3))}},
		{"mov x0, #5 ^ 1\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(4))}},
		// unary operators
		{"mov x0, ~0\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-1))}},
		{"mov x0, -5\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-5))}},
		{"mov x0, #-5\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-5))}},
		{"mov x0, #--5\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(5))}}, // double unary
		{"mov x0, #-5+3\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-2))}},
		// precedence: & binds tighter than |, + tighter than &
		{"mov x0, #1 | 2 & 3\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(3))}},
		{"mov x0, #1+2 & 3\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(3))}},
		// parentheses override precedence / associativity
		{"mov x0, #(1+2)&7\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(3))}},
		{"mov x0, -(1+2)\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-3))}},
		{"mov x0, ~(0xf0 | 0x0f)\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-256))}},
		{"mov x0, #(8-3)-2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(3))}},
		// the fold re-selects the shortest width for the result
		{"mov x0, #0x7fff+1\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0x8000))}},   // imm16
		{"mov x0, #0xffff+1\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0x10000))}},  // imm32
		{"mov x0, #0x7fffffff+1\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0x80000000))}},
		{"mov x0, #0-1\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-1))}},
		// expression as a non-final operand (commas optional)
		{"add x0 #1+1 x2\n", []parseRec{mkRec2(t, "add", rOp(X, 0), iOp(2), rOp(X, 2))}},
	}
	for _, c := range cases {
		got, errFlag := parseZ80(t, mac, []byte(c.src))
		if errFlag {
			t.Errorf("%q: PARSE_ERR set unexpectedly", c.src)
			continue
		}
		compareRecs(t, fmt.Sprintf("%q", c.src), got, c.want)
		ref, ok := refParse([]byte(c.src))
		if !ok {
			t.Fatalf("%q: refParse reported error on a valid case", c.src)
		}
		compareRecs(t, fmt.Sprintf("%q (vs refParse)", c.src), got, ref)
	}
}

// TestParseShiftHandCases pins INST records for the B3a2 shift operators,
// exercising small counts, the width boundary, the >=64-count clamp (logical
// `<<` -> 0; arithmetic `>>` -> sign extension), arithmetic-shift sign
// preservation, and shift precedence (tighter than `+`, looser than `&`).
func TestParseShiftHandCases(t *testing.T) {
	mac := loadAsmparse(t)
	X := format.OpRegX
	cases := []struct {
		src  string
		want []parseRec
	}{
		{"mov x0, #1<<4\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(16))}},
		{"mov x0, #256>>2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(64))}},
		{"mov x0, #1<<8\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(256))}},     // imm16
		{"mov x0, #1<<31\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0x80000000))}},
		{"mov x0, #1<<63\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-9223372036854775808))}}, // MinInt64
		// arithmetic right shift preserves the sign bit
		{"mov x0, ~0>>1\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-1))}},
		{"mov x0, #-256>>4\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-16))}},
		{"mov x0, #-1>>20\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-1))}},
		// the >=64-count clamp
		{"mov x0, #1<<64\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		{"mov x0, #1<<100\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		{"mov x0, #255>>64\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},      // positive -> 0
		{"mov x0, ~0>>64\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-1))}},       // negative -> -1
		{"mov x0, #8>>200\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		// counts >= 256: byte0 alone wraps, so these exercise the high-byte
		// path of the count check (a byte0-only impl would shift by 0).
		{"mov x0, #1<<256\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		{"mov x0, #1<<1000\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		{"mov x0, #-256>>256\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-1))}},
		{"mov x0, #256>>512\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		// count 0 is the identity
		{"mov x0, #5<<0\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(5))}},
		// precedence: `+` tighter than `<<`, `<<` tighter than `&`
		{"mov x0, #1+1<<2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(8))}},   // (1+1)<<2
		{"mov x0, #1<<2 & 4\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(4))}}, // (1<<2)&4
		{"mov x0, #1<<3>>1\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(4))}},  // left-assoc: (1<<3)>>1
	}
	for _, c := range cases {
		got, errFlag := parseZ80(t, mac, []byte(c.src))
		if errFlag {
			t.Errorf("%q: PARSE_ERR set unexpectedly", c.src)
			continue
		}
		compareRecs(t, fmt.Sprintf("%q", c.src), got, c.want)
		ref, ok := refParse([]byte(c.src))
		if !ok {
			t.Fatalf("%q: refParse reported error on a valid case", c.src)
		}
		compareRecs(t, fmt.Sprintf("%q (vs refParse)", c.src), got, ref)
	}
}

// TestParseMulHandCases pins INST records for the B3a3 multiply operator,
// exercising small products, signed operands, the mod-2^64 wrap on large
// products, multiply-by-zero, left-associativity, and multiply's precedence
// (tighter than every other operator).
func TestParseMulHandCases(t *testing.T) {
	mac := loadAsmparse(t)
	X := format.OpRegX
	cases := []struct {
		src  string
		want []parseRec
	}{
		{"mov x0, #2*3\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(6))}},
		{"add x0, x1, #(1+2)*4\n", []parseRec{mkRec2(t, "add", rOp(X, 0), rOp(X, 1), iOp(12))}},
		{"mov x0, #-2*3\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-6))}},
		{"mov x0, #-2*-3\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(6))}},
		{"mov x0, #5*0\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		{"mov x0, #0*1234\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		// width-growing product (no wrap)
		{"mov x0, #0x10000*0x10000\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0x100000000))}},
		// products that wrap mod 2^64
		{"mov x0, #0x100000000*0x100000000\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}}, // 2^64 -> 0
		{"mov x0, #0xffffffffffffffff*2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-2))}},    // -1 * 2
		{"mov x0, #0xffffffffffffffff*0xffffffffffffffff\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(1))}}, // (-1)^2
		// left-associative and precedence (binds tighter than + << &)
		{"mov x0, #2*3*4\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(24))}},
		{"mov x0, #2+3*4\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(14))}},   // 2 + (3*4)
		{"mov x0, #3*4+2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(14))}},   // (3*4) + 2
		{"mov x0, #1*2<<3\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(16))}},  // (1*2)<<3
		{"mov x0, #(2+3)*(4+1)\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(25))}},
	}
	for _, c := range cases {
		got, errFlag := parseZ80(t, mac, []byte(c.src))
		if errFlag {
			t.Errorf("%q: PARSE_ERR set unexpectedly", c.src)
			continue
		}
		compareRecs(t, fmt.Sprintf("%q", c.src), got, c.want)
		ref, ok := refParse([]byte(c.src))
		if !ok {
			t.Fatalf("%q: refParse reported error on a valid case", c.src)
		}
		compareRecs(t, fmt.Sprintf("%q (vs refParse)", c.src), got, ref)
	}
}

// TestParseDivHandCases pins INST records for the B3a4 division operator,
// exercising truncation toward zero, all four sign combinations, divide-by-zero
// (-> 0), the MinInt64/-1 two's-complement overflow (-> MinInt64), left-
// associativity, and division's precedence (same as `*`, tighter than `+`).
func TestParseDivHandCases(t *testing.T) {
	mac := loadAsmparse(t)
	X := format.OpRegX
	cases := []struct {
		src  string
		want []parseRec
	}{
		{"mov x0, #7/2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(3))}},   // truncates
		{"mov x0, #10/3\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(3))}},
		{"mov x0, #10/5\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(2))}},
		{"mov x0, #5/10\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		// all four sign combinations (truncation is toward zero)
		{"mov x0, #-7/2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-3))}},
		{"mov x0, #7/-2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-3))}},
		{"mov x0, #-7/-2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(3))}},
		// divide by zero yields 0 (matches format.EvalConst's guard)
		{"mov x0, #5/0\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		{"mov x0, #0/5\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		{"mov x0, #100/0\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		{"mov x0, #-100/0\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0))}},
		// large / signed values
		{"mov x0, #0xffffffffffffffff/1\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-1))}},          // -1/1
		{"mov x0, #0xffffffffffffffff/0xffffffffffffffff\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(1))}}, // -1/-1
		{"mov x0, #0x10000000/0x1000\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(0x10000))}},
		// the MinInt64/-1 two's-complement overflow case -> MinInt64
		{"mov x0, #0x8000000000000000/-1\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-9223372036854775808))}},
		{"mov x0, #0x8000000000000000/1\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(-9223372036854775808))}},
		// precedence (prec 4, same as *) and left-associativity
		{"mov x0, #2+10/2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(7))}},   // 2 + (10/2)
		{"mov x0, #10/2*3\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(15))}},  // (10/2)*3
		{"mov x0, #100/10/2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(5))}}, // (100/10)/2
		{"mov x0, #(6+4)/2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), iOp(5))}},
	}
	for _, c := range cases {
		got, errFlag := parseZ80(t, mac, []byte(c.src))
		if errFlag {
			t.Errorf("%q: PARSE_ERR set unexpectedly", c.src)
			continue
		}
		compareRecs(t, fmt.Sprintf("%q", c.src), got, c.want)
		ref, ok := refParse([]byte(c.src))
		if !ok {
			t.Fatalf("%q: refParse reported error on a valid case", c.src)
		}
		compareRecs(t, fmt.Sprintf("%q (vs refParse)", c.src), got, ref)
	}
}

// TestParseSymHandCases pins INST records for symbol-reference operands (B3b):
// a non-register identifier interns into the per-document symbol table and emits
// PUSH_SYM,id (first-encounter 0-based id); a symbol makes the expression
// non-constant, so the operand keeps the raw bytecode rather than folding. Ids
// accumulate across operands and lines in encounter order.
func TestParseSymHandCases(t *testing.T) {
	mac := loadAsmparse(t)
	X := format.OpRegX
	add := format.OpAdd
	cases := []struct {
		src  string
		want []parseRec
	}{
		// a lone symbol operand -> PUSH_SYM with id 0
		{"mov x0, foo\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), sOp(0))}},
		{"add x0, x1, foo\n", []parseRec{mkRec2(t, "add", rOp(X, 0), rOp(X, 1), sOp(0))}},
		// the '#' prefix is consumed; a bare symbol follows
		{"mov x0, #foo\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), sOp(0))}},
		// two distinct symbols -> ids 0 and 1 in encounter order
		{"add x0, foo, bar\n", []parseRec{mkRec2(t, "add", rOp(X, 0), sOp(0), sOp(1))}},
		// a repeated symbol reuses its id
		{"add x0, foo, foo\n", []parseRec{mkRec2(t, "add", rOp(X, 0), sOp(0), sOp(0))}},
		// ids accumulate across lines (foo=0, bar=1, foo reused)
		{"mov x0, foo\nmov x1, bar\nmov x2, foo\n", []parseRec{
			mkRec2(t, "mov", rOp(X, 0), sOp(0)),
			mkRec2(t, "mov", rOp(X, 1), sOp(1)),
			mkRec2(t, "mov", rOp(X, 2), sOp(0)),
		}},
		// an identifier that is not a valid register is a symbol: "x" (too
		// short), "x99" (number out of range)
		{"mov x0, x\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), sOp(0))}},
		{"mov x0, x99\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), sOp(0))}},
		// symbol + constant: a non-constant expression keeps raw bytecode
		{"mov x0, foo+4\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), xOp(func(w *format.ExprWriter) {
			w.WriteSym(0)
			w.WriteImm(4)
			w.WriteOp(add)
		}))}},
		// symbol in a parenthesised sub-expression with mixed operators
		{"mov x0, (foo+1)*2\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), xOp(func(w *format.ExprWriter) {
			w.WriteSym(0)
			w.WriteImm(1)
			w.WriteOp(format.OpAdd)
			w.WriteImm(2)
			w.WriteOp(format.OpMul)
		}))}},
		// difference of two symbols (a common assembler idiom)
		{"mov x0, end-start\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), xOp(func(w *format.ExprWriter) {
			w.WriteSym(0) // end
			w.WriteSym(1) // start
			w.WriteOp(format.OpSub)
		}))}},
	}
	for _, c := range cases {
		got, errFlag := parseZ80(t, mac, []byte(c.src))
		if errFlag {
			t.Errorf("%q: PARSE_ERR set unexpectedly", c.src)
			continue
		}
		compareRecs(t, fmt.Sprintf("%q", c.src), got, c.want)
		ref, ok := refParse([]byte(c.src))
		if !ok {
			t.Fatalf("%q: refParse reported error on a valid case", c.src)
		}
		compareRecs(t, fmt.Sprintf("%q (vs refParse)", c.src), got, ref)
	}
}

// apSymPool are symbol names for the symbol fuzz; a small pool so repeats occur
// and id-reuse is exercised. Includes identifiers that look like near-miss
// registers ("x", "x99", "wq") to confirm they intern as symbols.
var apSymPool = []string{"foo", "bar", "baz", "start", "end", "loop", "x", "x99", "wq", "_l"}

// randSymExpr builds a random expression mixing literals and symbol names (so
// the result is generally non-constant -> raw bytecode), reusing randExpr's
// operator set.
func randSymExpr(rng *rand.Rand, depth int) string {
	if depth <= 0 || rng.Intn(3) == 0 {
		if rng.Intn(2) == 0 {
			return apSymPool[rng.Intn(len(apSymPool))]
		}
		return apExprLeaves[rng.Intn(len(apExprLeaves))]
	}
	switch rng.Intn(6) {
	case 0:
		return "-" + randSymExpr(rng, depth-1)
	case 1:
		return "~" + randSymExpr(rng, depth-1)
	case 2:
		return "(" + randSymExpr(rng, depth-1) + ")"
	default:
		op := apExprBinOps[rng.Intn(len(apExprBinOps))]
		return randSymExpr(rng, depth-1) + op + randSymExpr(rng, depth-1)
	}
}

// TestParseSymFuzz compares asmparse against refParse over random expressions
// mixing symbols and literals across multiple lines, exercising the document
// symbol table's first-encounter id assignment and reuse. refParse (the real
// format.SymbolTable + ExprWriter + EvalConst) is the oracle; asmparse's
// sym_intern must assign byte-identical ids in the same encounter order.
func TestParseSymFuzz(t *testing.T) {
	mac := loadAsmparse(t)
	for _, seed := range []int64{2, 13, 53, 211, 9001} {
		rng := rand.New(rand.NewSource(seed))
		var src []byte
		lines := 8 + rng.Intn(8)
		for li := 0; li < lines; li++ {
			src = append(src, "mov x0, "...)
			src = append(src, randSymExpr(rng, 3)...)
			src = append(src, '\n')
		}
		want, ok := refParse(src)
		if !ok {
			t.Fatalf("seed %d: refParse reported error on generated B3b source:\n%s", seed, src)
		}
		got, errFlag := parseZ80(t, mac, src)
		if errFlag {
			t.Fatalf("seed %d: PARSE_ERR set on valid B3b source:\n%s", seed, src)
		}
		compareRecs(t, fmt.Sprintf("seed%d", seed), got, want)
		t.Logf("seed=%d: %d source bytes, %d records matched", seed, len(src), len(got))
	}
}

// apExprLeaves are literal leaves for the expression fuzz, spanning every
// PUSH_IMMn width plus char literals.
var apExprLeaves = []string{
	"0", "1", "2", "3", "7", "15", "0xff", "0x100",
	"127", "128", "255", "256", "1000", "0x7fff", "0x8000",
	"0xffff", "0x10000", "0x7fffffff", "0xdeadbeef",
	"'A'", "'0'",
}

// apExprBinOps are the full B3a..B3a4 binary operator set. The shifts `<< >>`
// exercise both the small-count bit-loop and the >=64-count clamp (the literal
// leaves include values far larger than 63 used as counts); `*` exercises the
// 64-bit shift-add multiply including the mod-2^64 wrap; `/` exercises the
// signed long division and the divide-by-zero -> 0 path (the leaves include 0).
var apExprBinOps = []string{"+", "-", "&", "|", "^", "<<", ">>", "*", "/"}

// randExpr builds a random valid B3a constant expression string of bounded
// depth (literals combined with the B3a operators, unary `- ~`, and parens).
func randExpr(rng *rand.Rand, depth int) string {
	if depth <= 0 || rng.Intn(3) == 0 {
		return apExprLeaves[rng.Intn(len(apExprLeaves))]
	}
	switch rng.Intn(6) {
	case 0:
		return "-" + randExpr(rng, depth-1)
	case 1:
		return "~" + randExpr(rng, depth-1)
	case 2:
		return "(" + randExpr(rng, depth-1) + ")"
	default:
		op := apExprBinOps[rng.Intn(len(apExprBinOps))]
		sep := ""
		if rng.Intn(2) == 0 {
			sep = " "
		}
		return randExpr(rng, depth-1) + sep + op + sep + randExpr(rng, depth-1)
	}
}

// TestParseExprFuzz compares asmparse against refParse over random constant
// expressions embedded as `#imm` operands. refParse (the real format.ExprWriter
// + format.EvalConst) is the oracle for the folded operand bytes; asmparse's
// expr_fold must agree bit-for-bit, including the wrap-around of 64-bit
// arithmetic and the fold's width re-selection.
func TestParseExprFuzz(t *testing.T) {
	mac := loadAsmparse(t)
	for _, seed := range []int64{3, 11, 29, 101, 5000} {
		rng := rand.New(rand.NewSource(seed))
		var src []byte
		lines := 8 + rng.Intn(8)
		for li := 0; li < lines; li++ {
			src = append(src, "mov x0, #"...)
			src = append(src, randExpr(rng, 3)...)
			src = append(src, '\n')
		}
		want, ok := refParse(src)
		if !ok {
			t.Fatalf("seed %d: refParse reported error on generated B3a source:\n%s", seed, src)
		}
		got, errFlag := parseZ80(t, mac, src)
		if errFlag {
			t.Fatalf("seed %d: PARSE_ERR set on valid B3a source:\n%s", seed, src)
		}
		compareRecs(t, fmt.Sprintf("seed%d", seed), got, want)
		t.Logf("seed=%d: %d source bytes, %d expr records matched", seed, len(src), len(got))
	}
}

// apImmPool are immediate operands spanning every PUSH_IMMn width plus the
// signed wraparound of large hex literals and char literals.
var apImmPool = []string{
	"#0", "#1", "#4", "#15", "#127", "#128", "#255", "#256",
	"#1000", "#32767", "#32768", "#65535", "#0x10000",
	"#0x7fffffff", "#0x80000000", "#0xdeadbeef", "#0xffffffff",
	"#0x100000000", "#0x7fffffffffffffff", "#0xffffffffffffffff",
	"#'A'", "#'0'",
}

// apRegPool are register operands match_reg must recognise.
var apRegPool = []string{
	"x0", "x1", "x2", "x5", "x9", "x10", "x19", "x28", "x29", "x30",
	"w0", "w1", "w7", "w12", "w20", "w30",
	"sp", "wsp", "xzr", "wzr", "fp", "lr",
}

// apMnemPool are mnemonics that parse through parseInst's GENERIC operand loop
// (deliberately excluding the special-form mnemonics it intercepts first:
// movk/movz/movn/movl/dsb/dmb/isb/mrs/msr/dc/tlbi, and ldr/ror which have
// special parse paths). Parsing does not check operand arity, so any register
// count is valid syntax here.
var apMnemPool = []string{
	"add", "sub", "adds", "subs", "and", "orr", "eor", "bic",
	"mov", "mvn", "mul", "udiv", "sdiv", "cmp", "tst",
	"ret", "br", "blr", "b", "bl", "sxtw",
}

// ---------------------------------------------------------------------------
// B3c — PC (`.`), local-ref, and reloc primaries.
// ---------------------------------------------------------------------------

// pOp builds an expression operand whose bytecode is produced by a closure
// over a real format.ExprWriter (like xOp but named for the primary tests).
func pOp(build func(w *format.ExprWriter)) opspec {
	var ew format.ExprWriter
	build(&ew)
	return opspec{isExpr: true, exprRaw: ew.Bytes()}
}

// TestParsePrimaryHandCases pins INST records for B3c primaries: PC (`.`),
// local-refs (`1f`, `2b`), and reloc prefixes (`:lo12:foo`). These primaries
// produce non-constant expression bytecode (PC/local/reloc), so the operand
// keeps the raw bytecode rather than folding. Cross-checks against refParse.
func TestParsePrimaryHandCases(t *testing.T) {
	mac := loadAsmparse(t)
	X := format.OpRegX
	cases := []struct {
		src  string
		want []parseRec
	}{
		// PC primary: '.' emits PUSH_PC
		{"mov x0, .\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), pOp(func(w *format.ExprWriter) {
			w.WritePC()
		}))}},
		// PC in an arithmetic expression: `. + 4`
		{"add x0, x1, .+4\n", []parseRec{mkRec2(t, "add", rOp(X, 0), rOp(X, 1), pOp(func(w *format.ExprWriter) {
			w.WritePC()
			w.WriteImm(4)
			w.WriteOp(format.OpAdd)
		}))}},
		// local-ref forward: `1f` (digit=1, dir=0)
		{"b 1f\n", []parseRec{mkRec2(t, "b", pOp(func(w *format.ExprWriter) {
			w.WriteLocal(1, 0)
		}))}},
		// local-ref backward: `2b` (digit=2, dir=1)
		{"b 2b\n", []parseRec{mkRec2(t, "b", pOp(func(w *format.ExprWriter) {
			w.WriteLocal(2, 1)
		}))}},
		// local-ref with a larger digit: `9f`
		{"br 9f\n", []parseRec{mkRec2(t, "br", pOp(func(w *format.ExprWriter) {
			w.WriteLocal(9, 0)
		}))}},
		// reloc primary: `:lo12:foo` — symbol interns as id 0
		{"mov x0, :lo12:foo\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), pOp(func(w *format.ExprWriter) {
			w.WriteSym(0)
			w.WriteOp(format.OpRelLo12)
		}))}},
		// reloc primary: `:hi12:bar` — symbol interns as id 0 (fresh document)
		{"mov x0, :hi12:bar\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), pOp(func(w *format.ExprWriter) {
			w.WriteSym(0)
			w.WriteOp(format.OpRelHi12)
		}))}},
		// reloc primary: `:abs_g0:label` — id 0
		{"mov x0, :abs_g0:label\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), pOp(func(w *format.ExprWriter) {
			w.WriteSym(0)
			w.WriteOp(format.OpRelAbsG0)
		}))}},
		// reloc primary: `:abs_g0_nc:label`
		{"mov x0, :abs_g0_nc:label\n", []parseRec{mkRec2(t, "mov", rOp(X, 0), pOp(func(w *format.ExprWriter) {
			w.WriteSym(0)
			w.WriteOp(format.OpRelAbsG0NC)
		}))}},
		// multiple reloc operands on one line — each gets an id
		{"add x0, :lo12:foo, :hi12:foo\n", []parseRec{mkRec2(t, "add", rOp(X, 0),
			pOp(func(w *format.ExprWriter) { w.WriteSym(0); w.WriteOp(format.OpRelLo12) }),
			pOp(func(w *format.ExprWriter) { w.WriteSym(0); w.WriteOp(format.OpRelHi12) }),
		)}},
	}
	for _, c := range cases {
		got, errFlag := parseZ80(t, mac, []byte(c.src))
		if errFlag {
			t.Errorf("%q: PARSE_ERR set unexpectedly", c.src)
			continue
		}
		compareRecs(t, fmt.Sprintf("%q", c.src), got, c.want)
		ref, ok := refParse([]byte(c.src))
		if !ok {
			t.Fatalf("%q: refParse reported error on a valid case", c.src)
		}
		compareRecs(t, fmt.Sprintf("%q (vs refParse)", c.src), got, ref)
	}
}

// ---------------------------------------------------------------------------
// B4 — memory operands (parse_operand_mem / match_extend).
// ---------------------------------------------------------------------------

// refMatchExtend is a verbatim port of parser.go's matchExtend (parser.go:1398)
// over format.ExtendKind.Name(). Returns the ExtendKind and true on a match.
func refMatchExtend(name string) (format.ExtendKind, bool) {
	for i := 0; i < 8; i++ {
		if format.ExtendKind(i).Name() == name {
			return format.ExtendKind(i), true
		}
	}
	return 0, false
}

// refParseMem is a verbatim transcription of parseMem (parser.go:1278-1388).
// It consumes from toks[pos] (which must be tLBracket), writes into ow, and
// returns the new pos and ok. Interns symbols into st (for offset expressions).
func refParseMem(toks []refTok, pos int, st *format.SymbolTable, ow *format.OperandWriter) (newPos int, ok bool) {
	pos++ // consume '['
	if pos >= len(toks) || toks[pos].kind != tIdent {
		return pos, false
	}
	baseKind, base, isReg := refMatchReg(string(toks[pos].span))
	if !isReg || (baseKind != format.OpRegX && baseKind != format.OpRegXSP) {
		return pos, false
	}
	pos++

	if pos < len(toks) && toks[pos].kind == tRBracket {
		pos++ // consume ']'
		// Post-index? [base], #imm or [base], imm
		if pos < len(toks) && toks[pos].kind == tComma && pos+1 < len(toks) {
			next := toks[pos+1].kind
			if next == tHash || next == tInt || next == tMinus ||
				next == tIdent || next == tLParen {
				pos++ // consume ','
				expr, npos, ok2 := refParseExpr(toks, pos, st)
				if !ok2 {
					return pos, false
				}
				ow.WriteMemBaseOff(format.MemBaseOffPost, base, expr)
				return npos, true
			}
		}
		ow.WriteMemBase(base)
		return pos, true
	}

	if pos >= len(toks) || toks[pos].kind != tComma {
		return pos, false
	}
	pos++ // consume ','

	if pos < len(toks) && toks[pos].kind == tIdent {
		idxKind, idx, idxOk := refMatchReg(string(toks[pos].span))
		if idxOk && (idxKind == format.OpRegX || idxKind == format.OpRegW) {
			idxWidth := byte(0)
			if idxKind == format.OpRegX {
				idxWidth = 1
			}
			pos++
			if pos < len(toks) && toks[pos].kind == tComma {
				pos++ // consume ','
				if pos >= len(toks) || toks[pos].kind != tIdent {
					return pos, false
				}
				modName := string(toks[pos].span)
				if modName == "lsl" {
					pos++ // consume 'lsl'
					if pos >= len(toks) || toks[pos].kind != tHash {
						return pos, false
					}
					pos++ // consume '#'
					if pos >= len(toks) || toks[pos].kind != tInt {
						return pos, false
					}
					amt := byte(toks[pos].val)
					pos++
					if pos >= len(toks) || toks[pos].kind != tRBracket {
						return pos, false
					}
					pos++ // consume ']'
					ow.WriteMemBaseIdxShifted(base, idx, idxWidth, amt)
					return pos, true
				}
				ext, extOk := refMatchExtend(modName)
				if !extOk {
					return pos, false
				}
				pos++ // consume extend keyword
				amt := byte(0)
				if pos < len(toks) && toks[pos].kind == tHash {
					pos++ // consume '#'
					if pos >= len(toks) || toks[pos].kind != tInt {
						return pos, false
					}
					amt = byte(toks[pos].val)
					pos++
				}
				if pos >= len(toks) || toks[pos].kind != tRBracket {
					return pos, false
				}
				pos++ // consume ']'
				ow.WriteMemBaseIdxExtended(base, idx, idxWidth, ext, amt)
				return pos, true
			}
			if pos >= len(toks) || toks[pos].kind != tRBracket {
				return pos, false
			}
			pos++ // consume ']'
			ow.WriteMemBaseIdx(base, idx, idxWidth)
			return pos, true
		}
	}

	// General case: offset expression
	expr, npos, ok2 := refParseExpr(toks, pos, st)
	if !ok2 {
		return pos, false
	}
	pos = npos
	if pos >= len(toks) || toks[pos].kind != tRBracket {
		return pos, false
	}
	pos++ // consume ']'
	if pos < len(toks) && toks[pos].kind == tBang {
		pos++ // consume '!'
		ow.WriteMemBaseOff(format.MemBaseOffPre, base, expr)
		return pos, true
	}
	ow.WriteMemBaseOff(format.MemBaseOff, base, expr)
	return pos, true
}

// refParseWithMem mirrors refParse but with the B4 memory-operand case wired in.
func refParseWithMem(src []byte) (recs []parseRec, ok bool) {
	toks, lok := refLex(src)
	if !lok {
		return nil, false
	}
	st := format.NewSymbolTable()
	pos := 0
	for {
		if pos >= len(toks) {
			return recs, true
		}
		switch toks[pos].kind {
		case tEOF:
			return recs, true
		case tEOL:
			pos++
		case tIdent:
			id, found := format.MnemonicID(string(toks[pos].span))
			if !found {
				return nil, false
			}
			pos++
			var ow format.OperandWriter
			count := byte(0)
			for {
				if pos >= len(toks) {
					return nil, false
				}
				k := toks[pos].kind
				if k == tEOL || k == tEOF || k == tLineComment || k == tBlockComment {
					break
				}
				if k == tComma {
					if count == 0 {
						return nil, false
					}
					pos++
					continue
				}
				switch k {
				case tLBracket:
					npos, memOk := refParseMem(toks, pos, st, &ow)
					if !memOk {
						return nil, false
					}
					count++
					pos = npos
				case tIdent:
					if rk, reg, isReg := refMatchReg(string(toks[pos].span)); isReg {
						ow.WriteReg(rk, reg)
						count++
						pos++
						break
					}
					fallthrough
				case tHash, tInt, tMinus, tTilde, tLParen,
					tDot, tLocalRef, tColon:
					expr, npos, ok2 := refParseExpr(toks, pos, st)
					if !ok2 {
						return nil, false
					}
					ow.WriteImmExpr(expr)
					count++
					pos = npos
				default:
					return nil, false
				}
			}
			recs = append(recs, parseRec{mnemonicID: id, count: count, ops: ow.Bytes()})
		default:
			return nil, false
		}
	}
}

// immExprBytes builds the folded immediate expression byte slice for value v.
func immExprBytes(v int64) []byte {
	var ew format.ExprWriter
	ew.WriteImm(v)
	return ew.Bytes()
}

// TestParseMemHandCases pins explicit hand-authored expected operand bytes for
// all 7 MEM shapes plus edge cases. The expected bytes are computed directly
// from the layout spec in operands.go to guard against shared transcription bugs
// between refParseMem and the Z80.
func TestParseMemHandCases(t *testing.T) {
	mac := loadAsmparse(t)
	X := format.OpRegX

	// Hand-compute expected operand bytes per the layout spec in operands.go.
	// Layout constants: OP_KIND_MEM=0x08; MemBase=0, MemBaseOff=1,
	// MemBaseOffPre=2, MemBaseOffPost=3, MemBaseIdx=4,
	// MemBaseIdxShifted=5, MemBaseIdxExtended=6.
	memBase := func(base byte) []byte {
		return []byte{0x08, 0, base}
	}
	memBaseOff := func(shape, base byte, expr []byte) []byte {
		b := []byte{0x08, shape, base, byte(len(expr)), byte(len(expr) >> 8)}
		return append(b, expr...)
	}
	memBaseIdx := func(base, idx, w byte) []byte {
		return []byte{0x08, 4, base, idx, w}
	}
	memBaseIdxShifted := func(base, idx, w, amt byte) []byte {
		return []byte{0x08, 5, base, idx, w, amt}
	}
	memBaseIdxExtended := func(base, idx, w, ext, amt byte) []byte {
		return []byte{0x08, 6, base, idx, w, ext, amt}
	}

	// register operand bytes
	regOp := func(k format.OperandKind, n byte) []byte { return []byte{byte(k), n} }

	cases := []struct {
		desc string
		src  string
		want []parseRec
	}{
		// MemBase
		{
			"ldr x0, [x1]",
			"ldr x0, [x1]\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "ldr"), count: 2,
				ops: append(regOp(X, 0), memBase(1)...)}},
		},
		// MemBase with sp alias (XSP base register, x0 destination)
		{
			"ldr x0, [sp]",
			"ldr x0, [sp]\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "ldr"), count: 2,
				ops: append(regOp(X, 0), memBase(31)...)}},
		},
		// MemBase with fp alias (x29)
		{
			"ldr x0, [fp]",
			"ldr x0, [fp]\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "ldr"), count: 2,
				ops: append(regOp(X, 0), memBase(29)...)}},
		},
		// MemBaseOff positive (#8 -> PUSH_IMM8 = [0x01, 0x08])
		{
			"ldr x0, [x1, #8]",
			"ldr x0, [x1, #8]\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "ldr"), count: 2,
				ops: append(regOp(X, 0), memBaseOff(1, 1, immExprBytes(8))...)}},
		},
		// MemBaseOff negative (#-8)
		{
			"ldr x0, [x1, #-8]",
			"ldr x0, [x1, #-8]\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "ldr"), count: 2,
				ops: append(regOp(X, 0), memBaseOff(1, 1, immExprBytes(-8))...)}},
		},
		// MemBaseOffPre
		{
			"str x0, [x1, #8]!",
			"str x0, [x1, #8]!\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "str"), count: 2,
				ops: append(regOp(X, 0), memBaseOff(2, 1, immExprBytes(8))...)}},
		},
		// MemBaseOffPost
		{
			"ldr x0, [x1], #8",
			"ldr x0, [x1], #8\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "ldr"), count: 2,
				ops: append(regOp(X, 0), memBaseOff(3, 1, immExprBytes(8))...)}},
		},
		// MemBaseIdx X index (width=1)
		{
			"ldr x0, [x1, x2]",
			"ldr x0, [x1, x2]\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "ldr"), count: 2,
				ops: append(regOp(X, 0), memBaseIdx(1, 2, 1)...)}},
		},
		// MemBaseIdx W index (width=0)
		{
			"ldr x0, [x1, w2]",
			"ldr x0, [x1, w2]\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "ldr"), count: 2,
				ops: append(regOp(X, 0), memBaseIdx(1, 2, 0)...)}},
		},
		// MemBaseIdxShifted
		{
			"ldr x0, [x1, x2, lsl #3]",
			"ldr x0, [x1, x2, lsl #3]\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "ldr"), count: 2,
				ops: append(regOp(X, 0), memBaseIdxShifted(1, 2, 1, 3)...)}},
		},
		// MemBaseIdxExtended uxtw #2 (ext=2=EXT_UXTW)
		{
			"ldr x0, [x1, w2, uxtw #2]",
			"ldr x0, [x1, w2, uxtw #2]\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "ldr"), count: 2,
				ops: append(regOp(X, 0), memBaseIdxExtended(1, 2, 0, 2, 2)...)}},
		},
		// MemBaseIdxExtended sxtw no amount (ext=6=EXT_SXTW, amt=0)
		{
			"ldr x0, [x1, w2, sxtw]",
			"ldr x0, [x1, w2, sxtw]\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "ldr"), count: 2,
				ops: append(regOp(X, 0), memBaseIdxExtended(1, 2, 0, 6, 0)...)}},
		},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got, errFlag := parseZ80(t, mac, []byte(c.src))
			if errFlag {
				t.Fatalf("PARSE_ERR set unexpectedly")
			}
			compareRecs(t, "Z80 vs hand", got, c.want)
			ref, refOk := refParseWithMem([]byte(c.src))
			if !refOk {
				t.Fatalf("refParseWithMem failed on valid case")
			}
			compareRecs(t, "Z80 vs ref", got, ref)
		})
	}
}

// mustMnemID returns the mnemonic ID for name or fatals the test.
func mustMnemID(t *testing.T, name string) uint16 {
	t.Helper()
	id, ok := format.MnemonicID(name)
	if !ok {
		t.Fatalf("mustMnemID: %q is not a mnemonic", name)
	}
	return id
}

// TestParseMemSymbol checks a symbolic offset [x1, FOO] produces non-folded
// expression bytecode, and Z80 and refParseWithMem agree.
func TestParseMemSymbol(t *testing.T) {
	mac := loadAsmparse(t)
	src := "ldr x0, [x1, FOO]\n"
	got, errFlag := parseZ80(t, mac, []byte(src))
	if errFlag {
		t.Fatalf("PARSE_ERR set for symbolic offset")
	}
	ref, ok := refParseWithMem([]byte(src))
	if !ok {
		t.Fatalf("refParseWithMem failed for symbolic offset")
	}
	compareRecs(t, "symbolic offset", got, ref)
	// Sanity: ops[2] = OP_KIND_MEM, ops[3] = MemBaseOff(1), ops[4] = base x1=1
	ops := got[0].ops
	if len(ops) < 5 || ops[2] != 0x08 || ops[3] != 1 || ops[4] != 1 {
		t.Fatalf("unexpected mem header bytes in symbolic case: %x", ops)
	}
}

// TestParseMemSymExpr checks [x1, #FOO+4] — a mixed symbol+constant expression
// as the offset. Z80 and refParseWithMem must agree.
func TestParseMemSymExpr(t *testing.T) {
	mac := loadAsmparse(t)
	src := "ldr x0, [x1, #FOO+4]\n"
	got, errFlag := parseZ80(t, mac, []byte(src))
	if errFlag {
		t.Fatalf("PARSE_ERR set for sym+const offset")
	}
	ref, ok := refParseWithMem([]byte(src))
	if !ok {
		t.Fatalf("refParseWithMem failed for sym+const offset")
	}
	compareRecs(t, "sym+const offset", got, ref)
}

// apMemBases are valid base registers for memory operand fuzz.
var apMemBases = []string{"x0", "x1", "x2", "x5", "x9", "x10", "x19", "x28", "x29", "x30", "sp", "fp"}

// apMemIdxX are valid X-width index registers for memory operands.
var apMemIdxX = []string{"x0", "x1", "x2", "x5", "x9", "x10", "x19", "x28", "x29", "x30"}

// apMemIdxW are valid W-width index registers for memory operands.
var apMemIdxW = []string{"w0", "w1", "w2", "w5", "w9", "w10", "w19", "w28", "w29", "w30"}

// apExtendNames are the 8 extend keywords match_extend recognises.
var apExtendNames = []string{"uxtb", "uxth", "uxtw", "uxtx", "sxtb", "sxth", "sxtw", "sxtx"}

// TestParseMemFuzz compares asmparse against refParseWithMem over random memory
// operands covering all 7 shapes, random valid registers, random small offsets.
func TestParseMemFuzz(t *testing.T) {
	mac := loadAsmparse(t)
	for _, seed := range []int64{7, 31, 97, 503, 4099} {
		rng := rand.New(rand.NewSource(seed))
		var src []byte
		lines := 10 + rng.Intn(10)
		for li := 0; li < lines; li++ {
			base := apMemBases[rng.Intn(len(apMemBases))]
			var mem string
			switch rng.Intn(7) {
			case 0: // MemBase
				mem = "[" + base + "]"
			case 1: // MemBaseOff
				off := rng.Intn(256) - 128
				mem = fmt.Sprintf("[%s, #%d]", base, off)
			case 2: // MemBaseOffPre
				off := rng.Intn(128)
				mem = fmt.Sprintf("[%s, #%d]!", base, off)
			case 3: // MemBaseOffPost
				off := rng.Intn(128)
				mem = fmt.Sprintf("[%s], #%d", base, off)
			case 4: // MemBaseIdx
				if rng.Intn(2) == 0 {
					mem = "[" + base + ", " + apMemIdxX[rng.Intn(len(apMemIdxX))] + "]"
				} else {
					mem = "[" + base + ", " + apMemIdxW[rng.Intn(len(apMemIdxW))] + "]"
				}
			case 5: // MemBaseIdxShifted
				idx := apMemIdxX[rng.Intn(len(apMemIdxX))]
				amt := rng.Intn(5)
				mem = fmt.Sprintf("[%s, %s, lsl #%d]", base, idx, amt)
			case 6: // MemBaseIdxExtended
				ext := apExtendNames[rng.Intn(len(apExtendNames))]
				var idx string
				if ext == "uxtx" || ext == "sxtx" {
					idx = apMemIdxX[rng.Intn(len(apMemIdxX))]
				} else {
					idx = apMemIdxW[rng.Intn(len(apMemIdxW))]
				}
				if rng.Intn(2) == 0 {
					amt := rng.Intn(5)
					mem = fmt.Sprintf("[%s, %s, %s #%d]", base, idx, ext, amt)
				} else {
					mem = fmt.Sprintf("[%s, %s, %s]", base, idx, ext)
				}
			}
			src = append(src, "ldr x0, "...)
			src = append(src, mem...)
			src = append(src, '\n')
		}
		want, ok := refParseWithMem(src)
		if !ok {
			t.Fatalf("seed %d: refParseWithMem reported error on generated B4 source:\n%s", seed, src)
		}
		got, errFlag := parseZ80(t, mac, src)
		if errFlag {
			t.Fatalf("seed %d: PARSE_ERR set on valid B4 source:\n%s", seed, src)
		}
		compareRecs(t, fmt.Sprintf("seed%d", seed), got, want)
		t.Logf("seed=%d: %d source bytes, %d records matched", seed, len(src), len(got))
	}
}

// TestParseInstFuzz compares asmparse against refParse over random register
// instruction lines (no blanks/comments/labels/immediates).
func TestParseInstFuzz(t *testing.T) {
	mac := loadAsmparse(t)
	for _, seed := range []int64{1, 7, 42, 137, 2024} {
		rng := rand.New(rand.NewSource(seed))
		var src []byte
		lines := 5 + rng.Intn(12)
		for li := 0; li < lines; li++ {
			src = append(src, apMnemPool[rng.Intn(len(apMnemPool))]...)
			nops := rng.Intn(4) // 0..3 operands
			for oi := 0; oi < nops; oi++ {
				src = append(src, ' ')
				// ~30% immediates, ~70% registers (both valid B2c operands).
				if rng.Intn(10) < 3 {
					src = append(src, apImmPool[rng.Intn(len(apImmPool))]...)
				} else {
					src = append(src, apRegPool[rng.Intn(len(apRegPool))]...)
				}
				// ~70% of separators are commas; the rest bare spaces (both legal).
				if oi < nops-1 && rng.Intn(10) < 7 {
					src = append(src, ',')
				}
			}
			src = append(src, '\n')
		}
		want, ok := refParse(src)
		if !ok {
			t.Fatalf("seed %d: refParse reported error on generated B2b source:\n%s", seed, src)
		}
		got, errFlag := parseZ80(t, mac, src)
		if errFlag {
			t.Fatalf("seed %d: PARSE_ERR set on valid B2b source:\n%s", seed, src)
		}
		compareRecs(t, fmt.Sprintf("seed%d", seed), got, want)
		t.Logf("seed=%d: %d source bytes, %d records matched", seed, len(src), len(got))
	}
}

// TestParseInstError checks that out-of-domain lines set PARSE_ERR (and that
// refParse agrees). Each is a real parser path with teeth: an unknown mnemonic,
// malformed expressions, and B3c-specific error cases (unknown reloc name,
// malformed `:name:` syntax).
func TestParseInstError(t *testing.T) {
	mac := loadAsmparse(t)
	cases := []struct {
		name string
		src  string
	}{
		{"unknown mnemonic", "frobnicate x0, x1\n"},
		{"bare # without an int", "add x0, #\n"},
		{"leading comma", "add , x0\n"},
		{"line-leading number", "5 x0\n"},
		{"directive (B6, not B2b)", ".text\n"},
		// malformed expressions
		{"unbalanced paren", "mov x0, #(1+2\n"},
		{"empty parens", "mov x0, ()\n"},
		{"trailing binary operator", "mov x0, #4+\n"},
		{"trailing shift operator", "mov x0, #4<<\n"},
		{"trailing divide operator", "mov x0, #4/\n"},
		{"double unary then nothing", "mov x0, -\n"},
		// B3c error cases: reloc primaries with bad structure
		{"unknown reloc name", "mov x0, :bogus:foo\n"},
		{"reloc missing second colon", "mov x0, :lo12 foo\n"},
		{"reloc colon with no name", "mov x0, ::foo\n"},
	}
	for _, c := range cases {
		_, errFlag := parseZ80(t, mac, []byte(c.src))
		if !errFlag {
			t.Errorf("%s (%q): PARSE_ERR not set", c.name, c.src)
		}
		if _, ok := refParse([]byte(c.src)); ok {
			t.Errorf("%s (%q): refParse should report an error", c.name, c.src)
		}
	}
}

// ---------------------------------------------------------------------------
// B4b — register shift/extend operand suffix (parse_operand_reg shift/extend
// lookahead, WriteShiftedReg/WriteExtendedReg).
// ---------------------------------------------------------------------------

// refMatchShiftKind is a verbatim port of parser.go's matchShiftKind
// (parser.go:1407). Returns the ShiftKind and true on a match.
func refMatchShiftKind(name string) (format.ShiftKind, bool) {
	for i := 0; i < 4; i++ {
		if format.ShiftKind(i).Name() == name {
			return format.ShiftKind(i), true
		}
	}
	return 0, false
}

// refParseWithShiftExt mirrors refParse but with the B4b shift/extend
// lookahead wired into the register-operand path, mirroring parseOperand
// (parser.go:1032-1073).
func refParseWithShiftExt(src []byte) (recs []parseRec, ok bool) {
	toks, lok := refLex(src)
	if !lok {
		return nil, false
	}
	st := format.NewSymbolTable()
	pos := 0
	for {
		if pos >= len(toks) {
			return recs, true
		}
		switch toks[pos].kind {
		case tEOF:
			return recs, true
		case tEOL:
			pos++
		case tIdent:
			id, found := format.MnemonicID(string(toks[pos].span))
			if !found {
				return nil, false
			}
			pos++
			var ow format.OperandWriter
			count := byte(0)
			for {
				if pos >= len(toks) {
					return nil, false
				}
				k := toks[pos].kind
				if k == tEOL || k == tEOF || k == tLineComment || k == tBlockComment {
					break
				}
				if k == tComma {
					if count == 0 {
						return nil, false
					}
					pos++
					continue
				}
				switch k {
				case tLBracket:
					npos, memOk := refParseMem(toks, pos, st, &ow)
					if !memOk {
						return nil, false
					}
					count++
					pos = npos
				case tIdent:
					rk, reg, isReg := refMatchReg(string(toks[pos].span))
					if isReg {
						pos++ // consume register
						// shift/extend lookahead: X or W only
						if (rk == format.OpRegX || rk == format.OpRegW) &&
							pos < len(toks) && toks[pos].kind == tComma &&
							pos+1 < len(toks) && toks[pos+1].kind == tIdent {
							next := string(toks[pos+1].span)
							if sk, skOk := refMatchShiftKind(next); skOk {
								pos += 2 // consume comma + shift keyword
								if pos >= len(toks) || toks[pos].kind != tHash {
									return nil, false
								}
								pos++ // consume '#'
								expr, npos, ok2 := refParseExpr(toks, pos, st)
								if !ok2 {
									return nil, false
								}
								pos = npos
								width := byte(0)
								if rk == format.OpRegX {
									width = 1
								}
								ow.WriteShiftedReg(width, reg, sk, expr)
								count++
								break
							}
							if ek, ekOk := refMatchExtend(next); ekOk {
								pos += 2 // consume comma + extend keyword
								var amt []byte
								if pos < len(toks) && toks[pos].kind == tHash {
									pos++ // consume '#'
									a, npos, ok2 := refParseExpr(toks, pos, st)
									if !ok2 {
										return nil, false
									}
									amt = a
									pos = npos
								}
								width := byte(0)
								if rk == format.OpRegX {
									width = 1
								}
								ow.WriteExtendedReg(width, reg, ek, amt)
								count++
								break
							}
						}
						// plain register
						ow.WriteReg(rk, reg)
						count++
						break
					}
					// non-register identifier -> symbol expression
					fallthrough
				case tHash, tInt, tMinus, tTilde, tLParen,
					tDot, tLocalRef, tColon:
					expr, npos, ok2 := refParseExpr(toks, pos, st)
					if !ok2 {
						return nil, false
					}
					ow.WriteImmExpr(expr)
					count++
					pos = npos
				default:
					return nil, false
				}
			}
			recs = append(recs, parseRec{mnemonicID: id, count: count, ops: ow.Bytes()})
		default:
			return nil, false
		}
	}
}

// shiftedRegBytes hand-builds expected bytes for a SHIFTED_REG operand per the
// layout spec in operands.go:
//   [OP_KIND_SHIFTED_REG=0x06, width, reg, shiftKind, len_lo, len_hi, expr...]
func shiftedRegBytes(width, reg byte, sk format.ShiftKind, amtExpr []byte) []byte {
	b := []byte{0x06, width, reg, byte(sk), byte(len(amtExpr)), byte(len(amtExpr) >> 8)}
	return append(b, amtExpr...)
}

// extendedRegBytes hand-builds expected bytes for an EXTENDED_REG operand per
// the layout spec in operands.go:
//   [OP_KIND_EXTENDED_REG=0x07, width, reg, extKind, len_lo, len_hi, expr...]
func extendedRegBytes(width, reg byte, ek format.ExtendKind, amtExpr []byte) []byte {
	b := []byte{0x07, width, reg, byte(ek), byte(len(amtExpr)), byte(len(amtExpr) >> 8)}
	return append(b, amtExpr...)
}

// TestParseShiftExtHandCases pins explicit hand-authored expected operand bytes
// for shift/extend register operands (B4b), then cross-checks against
// refParseWithShiftExt. Every expected operand byte array is computed
// independently from the format spec as a separate authority.
func TestParseShiftExtHandCases(t *testing.T) {
	mac := loadAsmparse(t)
	X := format.OpRegX
	W := format.OpRegW

	// folded immediate expressions for small amounts
	imm := func(v int64) []byte { return immExprBytes(v) }

	cases := []struct {
		desc string
		src  string
		want []parseRec
	}{
		// --- ShiftedReg cases ---
		// add x0, x1, x2, lsl #3 -> X(0), X(1), ShiftedReg(width=1, reg=2, sk=LSL=0, amt=3)
		{
			"x reg lsl #3",
			"add x0, x1, x2, lsl #3\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 3,
				ops: concat(
					[]byte{byte(X), 0},
					[]byte{byte(X), 1},
					shiftedRegBytes(1, 2, format.ShiftLSL, imm(3)),
				)}},
		},
		// lsr #1
		{
			"x reg lsr #1",
			"add x0, x1, x2, lsr #1\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 3,
				ops: concat(
					[]byte{byte(X), 0},
					[]byte{byte(X), 1},
					shiftedRegBytes(1, 2, format.ShiftLSR, imm(1)),
				)}},
		},
		// asr #2
		{
			"x reg asr #2",
			"add x0, x1, x2, asr #2\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 3,
				ops: concat(
					[]byte{byte(X), 0},
					[]byte{byte(X), 1},
					shiftedRegBytes(1, 2, format.ShiftASR, imm(2)),
				)}},
		},
		// ror #4
		{
			"x reg ror #4",
			"add x0, x1, x2, ror #4\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 3,
				ops: concat(
					[]byte{byte(X), 0},
					[]byte{byte(X), 1},
					shiftedRegBytes(1, 2, format.ShiftROR, imm(4)),
				)}},
		},
		// W register: add w0, w1, w2, lsl #1 -> width=0
		{
			"w reg lsl #1 (width 0)",
			"add w0, w1, w2, lsl #1\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 3,
				ops: concat(
					[]byte{byte(W), 0},
					[]byte{byte(W), 1},
					shiftedRegBytes(0, 2, format.ShiftLSL, imm(1)),
				)}},
		},
		// shift with expression amount: lsl #(1+2) folds to 3
		{
			"x reg lsl #(1+2)",
			"add x0, x1, x2, lsl #(1+2)\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 3,
				ops: concat(
					[]byte{byte(X), 0},
					[]byte{byte(X), 1},
					shiftedRegBytes(1, 2, format.ShiftLSL, imm(3)),
				)}},
		},
		// --- ExtendedReg cases ---
		// add x0, x1, w2, uxtw #2 -> ExtendedReg(width=0 from w2, reg=2, ek=UXTW=2, amt=2)
		{
			"w reg uxtw #2",
			"add x0, x1, w2, uxtw #2\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 3,
				ops: concat(
					[]byte{byte(X), 0},
					[]byte{byte(X), 1},
					extendedRegBytes(0, 2, format.ExtUXTW, imm(2)),
				)}},
		},
		// add x0, x1, x2, sxtx -> ExtendedReg(width=1, reg=2, ek=SXTX=7, no #amt => len=0)
		{
			"x reg sxtx no amt",
			"add x0, x1, x2, sxtx\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 3,
				ops: concat(
					[]byte{byte(X), 0},
					[]byte{byte(X), 1},
					extendedRegBytes(1, 2, format.ExtSXTX, nil),
				)}},
		},
		// add x0, x1, w2, uxtw -> extend, no amt (len 0)
		{
			"w reg uxtw no amt",
			"add x0, x1, w2, uxtw\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 3,
				ops: concat(
					[]byte{byte(X), 0},
					[]byte{byte(X), 1},
					extendedRegBytes(0, 2, format.ExtUXTW, nil),
				)}},
		},
		// --- Plain register cases (no suffix) ---
		// add x0, x1, x2 -> three plain regs
		{
			"plain regs no suffix",
			"add x0, x1, x2\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 3,
				ops: []byte{byte(X), 0, byte(X), 1, byte(X), 2}}},
		},
		// add x0, x1, sp -> sp is XSP, no extend attempted
		{
			"sp plain reg no extend",
			"add x0, x1, sp\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 3,
				ops: []byte{byte(X), 0, byte(X), 1, byte(format.OpRegXSP), 31}}},
		},
		// add x0, x1, sp, lsl #3: sp is XSP so X/W-only guard fires; sp emits
		// as plain reg and comma+lsl+#3 become separate operands (sym+imm).
		{
			"xsp guard: sp comma lsl not a shifted reg",
			"add x0, x1, sp, lsl #3\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 5,
				ops: concat(
					[]byte{byte(X), 0},
					[]byte{byte(X), 1},
					[]byte{byte(format.OpRegXSP), 31},
					immExprWithSym(0),    // lsl -> symbol id 0
					immExprOperand(3),    // #3 as full IMM_EXPR operand
				)}},
		},
		// add x0, x1, x2, foo where foo is not a shift/extend kw -> plain reg x2 + sym foo
		{
			"unknown suffix -> plain reg + symbol",
			"add x0, x1, x2, foo\n",
			[]parseRec{{mnemonicID: mustMnemID(t, "add"), count: 4,
				ops: concat(
					[]byte{byte(X), 0},
					[]byte{byte(X), 1},
					[]byte{byte(X), 2},
					immExprWithSym(0), // foo -> symbol id 0
				)}},
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got, errFlag := parseZ80(t, mac, []byte(c.src))
			if errFlag {
				t.Fatalf("PARSE_ERR set unexpectedly")
			}
			compareRecs(t, "Z80 vs hand", got, c.want)
			ref, refOk := refParseWithShiftExt([]byte(c.src))
			if !refOk {
				t.Fatalf("refParseWithShiftExt failed on valid case")
			}
			compareRecs(t, "Z80 vs ref", got, ref)
		})
	}
}

// TestParseShiftExtError checks that `add x0, x1, x2, lsl x3` (no '#' after
// lsl) sets PARSE_ERR in the Z80 and refParseWithShiftExt reports error.
func TestParseShiftExtError(t *testing.T) {
	mac := loadAsmparse(t)
	src := "add x0, x1, x2, lsl x3\n"
	_, errFlag := parseZ80(t, mac, []byte(src))
	if !errFlag {
		t.Errorf("%q: PARSE_ERR not set (expected error: no # after lsl)", src)
	}
	if _, ok := refParseWithShiftExt([]byte(src)); ok {
		t.Errorf("%q: refParseWithShiftExt should report error", src)
	}
}

// apShiftNames are the four shift keywords.
var apShiftNames = []string{"lsl", "lsr", "asr", "ror"}

// apExtendNamesX are extend names valid with X-width index registers.
var apExtendNamesX = []string{"uxtx", "sxtx"}

// apExtendNamesW are extend names valid with W-width index registers.
var apExtendNamesW = []string{"uxtb", "uxth", "uxtw", "sxtb", "sxth", "sxtw"}

// TestParseShiftExtFuzz compares asmparse against refParseWithShiftExt over
// random add/sub lines with random base regs, random shift/extend kinds, and
// random small expression amounts (randomly omitted for extend).
func TestParseShiftExtFuzz(t *testing.T) {
	mac := loadAsmparse(t)
	xRegs := []string{"x0", "x1", "x2", "x5", "x9", "x10", "x19", "x28"}
	wRegs := []string{"w0", "w1", "w2", "w5", "w9", "w10", "w19", "w28"}
	mnems := []string{"add", "sub"}

	for _, seed := range []int64{17, 53, 101, 409, 2027} {
		rng := rand.New(rand.NewSource(seed))
		var src []byte
		lines := 10 + rng.Intn(10)
		for li := 0; li < lines; li++ {
			mnem := mnems[rng.Intn(len(mnems))]
			dst := xRegs[rng.Intn(len(xRegs))]
			base := xRegs[rng.Intn(len(xRegs))]
			var suffix string
			switch rng.Intn(4) {
			case 0: // shift with X reg
				idx := xRegs[rng.Intn(len(xRegs))]
				sk := apShiftNames[rng.Intn(len(apShiftNames))]
				amt := rng.Intn(64)
				suffix = fmt.Sprintf("%s, %s #%d", idx, sk, amt)
			case 1: // extend with X reg (uxtx/sxtx)
				idx := xRegs[rng.Intn(len(xRegs))]
				ek := apExtendNamesX[rng.Intn(len(apExtendNamesX))]
				if rng.Intn(2) == 0 {
					amt := rng.Intn(8)
					suffix = fmt.Sprintf("%s, %s #%d", idx, ek, amt)
				} else {
					suffix = fmt.Sprintf("%s, %s", idx, ek)
				}
			case 2: // extend with W reg
				idx := wRegs[rng.Intn(len(wRegs))]
				ek := apExtendNamesW[rng.Intn(len(apExtendNamesW))]
				if rng.Intn(2) == 0 {
					amt := rng.Intn(8)
					suffix = fmt.Sprintf("%s, %s #%d", idx, ek, amt)
				} else {
					suffix = fmt.Sprintf("%s, %s", idx, ek)
				}
			case 3: // plain reg, no suffix
				idx := xRegs[rng.Intn(len(xRegs))]
				suffix = idx
			}
			line := fmt.Sprintf("%s %s, %s, %s\n", mnem, dst, base, suffix)
			src = append(src, line...)
		}
		want, ok := refParseWithShiftExt(src)
		if !ok {
			t.Fatalf("seed %d: refParseWithShiftExt reported error on generated B4b source:\n%s", seed, src)
		}
		got, errFlag := parseZ80(t, mac, src)
		if errFlag {
			t.Fatalf("seed %d: PARSE_ERR set on valid B4b source:\n%s", seed, src)
		}
		compareRecs(t, fmt.Sprintf("seed%d", seed), got, want)
		t.Logf("seed=%d: %d source bytes, %d records matched", seed, len(src), len(got))
	}
}

// concat concatenates multiple byte slices.
func concat(slices ...[]byte) []byte {
	var result []byte
	for _, s := range slices {
		result = append(result, s...)
	}
	return result
}

// immExprWithSym builds a PUSH_SYM,id expression byte slice for symbol id.
func immExprWithSym(id uint16) []byte {
	var ew format.ExprWriter
	ew.WriteSym(id)
	expr := ew.Bytes()
	// wrap in OP_KIND_IMM_EXPR header: [0x05, len_lo, len_hi, expr...]
	b := []byte{0x05, byte(len(expr)), byte(len(expr) >> 8)}
	return append(b, expr...)
}

// immExprOperand builds a complete OP_KIND_IMM_EXPR operand for a literal
// integer v: [0x05, len_lo, len_hi, expr_bytes...].
func immExprOperand(v int64) []byte {
	expr := immExprBytes(v)
	b := []byte{0x05, byte(len(expr)), byte(len(expr) >> 8)}
	return append(b, expr...)
}
