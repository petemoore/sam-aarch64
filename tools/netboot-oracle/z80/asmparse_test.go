// asmparse_test.go — host-verification of src/asmparse.asm (i48c: the aarch64
// assembler-source parser; Bricks B2a–B2c).
//
// B2a (mnemonic_lookup): drives the lookup under the flat-memory koron-go/z80
// harness and asserts the returned ID matches format.MnemonicID for every name
// in MnemonicTable, plus a batch of non-mnemonics (asserting not-found).
//
// B2b/B2c (parse_run / parse_inst): drives the instruction parse and compares
// the emitted INST records against a faithful Go reference. The operand-byte
// authority is the real format.OperandWriter + format.ExprWriter (imported
// directly); the lexing reuses refLex (already authority-validated in
// asmlex_test.go); only the small control-flow port (matchReg + the generic
// operand loop) is transcribed — refMatchReg below is verbatim parser.go
// matchReg. The B2b/B2c domain is instruction lines with register and
// single-literal-#imm operands (no blank-runs/labels/comments/directives/multi-
// term-or-symbol expressions/mem operands, and none of the special-form
// mnemonics parseInst intercepts before its generic loop), so refParse mirrors
// Parse exactly on that domain.
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

// refParse is a faithful transcription of parser.go's Parse restricted to B2b's
// domain (register-only instruction lines). It lexes via refLex (the
// authority-validated lexer reference), looks up mnemonics via format.MnemonicID,
// and builds operand bytes via the real format.OperandWriter. Returns ok=false
// on any out-of-domain construct, matching how asmparse sets PARSE_ERR.
func refParse(src []byte) (recs []parseRec, ok bool) {
	toks, lok := refLex(src)
	if !lok {
		return nil, false
	}
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
					if !isReg {
						return nil, false
					}
					ow.WriteReg(rk, reg)
					count++
					pos++
				case tHash:
					// `#` immediate prefix: must be followed by an int literal
					// (a single-literal immediate; expressions are B3).
					pos++
					if pos >= len(toks) || toks[pos].kind != tInt {
						return nil, false
					}
					fallthrough
				case tInt:
					var ew format.ExprWriter
					ew.WriteImm(int64(toks[pos].val))
					ow.WriteImmExpr(ew.Bytes())
					count++
					pos++
				default:
					return nil, false // mem operand / symbol expr -> later brick
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

// opspec describes one operand for the mixed reg/imm hand-case builder.
type opspec struct {
	isImm bool
	k     format.OperandKind // register kind (isImm == false)
	r     byte               // register number
	imm   int64              // immediate value (isImm == true)
}

func rOp(k format.OperandKind, r byte) opspec { return opspec{k: k, r: r} }
func iOp(v int64) opspec                      { return opspec{isImm: true, imm: v} }

// mkRec2 builds the expected INST record for a mnemonic with mixed register and
// immediate operands, using the authority (format.MnemonicID + the real
// format.OperandWriter / format.ExprWriter — WriteImm picks the shortest width).
func mkRec2(t *testing.T, mnem string, ops ...opspec) parseRec {
	t.Helper()
	id, ok := format.MnemonicID(mnem)
	if !ok {
		t.Fatalf("mkRec2: %q is not a mnemonic", mnem)
	}
	var ow format.OperandWriter
	for _, o := range ops {
		if o.isImm {
			var ew format.ExprWriter
			ew.WriteImm(o.imm)
			ow.WriteImmExpr(ew.Bytes())
		} else {
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

// TestParseInstError checks that out-of-B2b-domain lines set PARSE_ERR (and
// that refParse agrees). Each is a real parser path with teeth: an unknown
// mnemonic, a non-register operand (immediate), a leading comma, and a
// line-leading non-identifier.
func TestParseInstError(t *testing.T) {
	mac := loadAsmparse(t)
	cases := []struct {
		name string
		src  string
	}{
		{"unknown mnemonic", "frobnicate x0, x1\n"},
		{"multi-term expression (B3, not B2c)", "add x0, x1, #4+1\n"},
		{"bare # without an int", "add x0, #\n"},
		{"symbol / non-register ident operand (B3)", "add x0, foo\n"},
		{"leading comma", "add , x0\n"},
		{"line-leading number", "5 x0\n"},
		{"directive (B6, not B2b)", ".text\n"},
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
