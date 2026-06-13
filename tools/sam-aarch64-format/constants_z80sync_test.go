package format

// Go↔Z80 constants sync guard (i7 phase C1).
//
// src/tbn_constants.inc carries the OP_KIND_* / REC_KIND_* / DIR_* equates the
// Z80 assembler dispatches on. It is GENERATED from this package by
// tools/tables-gen (make tables), and `make tables-sync-check` already fails on
// any drift between the committed copy and a fresh regeneration. This test is
// the complementary EMITTER-FAITHFULNESS guard: it decodes the committed
// equates and asserts they agree value-for-value with the Go authority
// (operands.go OperandKind, kinds.go RecordKind, directives.go DirectiveTable).
//
// Together with the emitter's own value-mismatch errors, this makes a Go-side
// renumber that someone "fixed" by hand-editing the .inc impossible to land:
// the regen diff catches the stale file, and this test catches a generator that
// emitted wrong bytes. The invariants:
//
//	OP_KIND_*   every Go OperandKind MUST have a matching equate at its value.
//	REC_KIND_*  the Z80 carries a 6-of-9 SUBSET (it omits LABEL_DEF/LOCAL_DEF/
//	            BLANK_RUN); every equate present MUST match the Go value, and the
//	            three omitted kinds MUST be absent.
//	DIR_*       every DirectiveTable entry MUST have a matching equate at its ID,
//	            and there MUST be no extra DIR_* equates.

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// constantsIncPath resolves src/tbn_constants.inc relative to this test file,
// so it works regardless of the caller's working directory.
func constantsIncPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate test source")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	p, err := filepath.Abs(filepath.Join(repoRoot, "src", "tbn_constants.inc"))
	if err != nil {
		t.Fatalf("resolving tbn_constants.inc path: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("tbn_constants.inc not found at %s: %v", p, err)
	}
	return p
}

// parseEquates reads `NAME: equ VALUE` lines from tbn_constants.inc and returns
// name→value. Values are decimal or `&XX` hex; comments and blanks are ignored.
func parseEquates(t *testing.T) map[string]int {
	t.Helper()
	f, err := os.Open(constantsIncPath(t))
	if err != nil {
		t.Fatalf("opening tbn_constants.inc: %v", err)
	}
	defer f.Close()

	out := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if idx := strings.IndexByte(line, ';'); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		// Expect: NAME: equ VALUE
		if len(fields) < 3 || !strings.HasSuffix(fields[0], ":") || strings.ToLower(fields[1]) != "equ" {
			continue
		}
		name := strings.TrimSuffix(fields[0], ":")
		v, err := parseAsmInt(fields[2])
		if err != nil {
			t.Fatalf("equate %s: cannot parse value %q: %v", name, fields[2], err)
		}
		out[name] = v
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning tbn_constants.inc: %v", err)
	}
	return out
}

// parseAsmInt parses a pyz80 integer literal: `&XX` hex or plain decimal.
func parseAsmInt(s string) (int, error) {
	if strings.HasPrefix(s, "&") {
		v, err := strconv.ParseInt(s[1:], 16, 32)
		return int(v), err
	}
	v, err := strconv.ParseInt(s, 10, 32)
	return int(v), err
}

func TestTbnConstantsZ80Sync(t *testing.T) {
	eq := parseEquates(t)

	// OP_KIND_* — every OperandKind value present in the equates, with the Z80
	// spelling of the SP kinds (REG_XSP / REG_WSP, not REG_X_SP / REG_W_SP).
	opSuffix := map[OperandKind]string{
		OpRegX: "REG_X", OpRegW: "REG_W",
		OpRegXSP: "REG_XSP", OpRegWSP: "REG_WSP",
		OpImmExpr: "IMM_EXPR", OpShiftedReg: "SHIFTED_REG",
		OpExtendedReg: "EXTENDED_REG", OpMem: "MEM",
		OpString: "STRING", OpCond: "COND",
		OpSysName: "SYS_NAME", OpLitPool: "LIT_POOL",
	}
	for k, suffix := range opSuffix {
		name := "OP_KIND_" + suffix
		got, ok := eq[name]
		if !ok {
			t.Errorf("%s: missing from tbn_constants.inc", name)
			continue
		}
		if got != int(k) {
			t.Errorf("%s = %d; Go OperandKind = %d", name, got, int(k))
		}
	}

	// REC_KIND_* — the 6-of-9 subset the Z80 dispatches on.
	recPresent := map[RecordKind]string{
		KindInst: "INST", KindDirective: "DIRECTIVE", KindComment: "COMMENT",
		KindLitInsts: "LIT_INSTS", KindLitData: "LIT_DATA", KindInsnRun: "INSN_RUN",
	}
	for k, suffix := range recPresent {
		name := "REC_KIND_" + suffix
		got, ok := eq[name]
		if !ok {
			t.Errorf("%s: missing from tbn_constants.inc", name)
			continue
		}
		if got != int(k) {
			t.Errorf("%s = %d; Go RecordKind = %d", name, got, int(k))
		}
	}
	for _, suffix := range []string{"LABEL_DEF", "LOCAL_DEF", "BLANK_RUN"} {
		name := "REC_KIND_" + suffix
		if _, ok := eq[name]; ok {
			t.Errorf("%s: present, but the Z80 deliberately omits this kind (6-of-9 subset)", name)
		}
	}

	// DIR_* — full parity with DirectiveTable: every entry at its ID, no extras.
	wantDir := map[string]int{}
	for id, dname := range DirectiveTable {
		suffix := strings.ToUpper(strings.TrimPrefix(dname, "."))
		name := "DIR_" + suffix
		wantDir[name] = id
		got, ok := eq[name]
		if !ok {
			t.Errorf("%s: missing from tbn_constants.inc", name)
			continue
		}
		if got != id {
			t.Errorf("%s = %d; DirectiveTable index = %d", name, got, id)
		}
	}
	for name := range eq {
		if strings.HasPrefix(name, "DIR_") {
			if _, ok := wantDir[name]; !ok {
				t.Errorf("%s: present in tbn_constants.inc but absent from DirectiveTable", name)
			}
		}
	}
}
