package format

// Go↔Z80 sysreg sync guard (repo-audit 2026-05-29 §5 / §6 item #9).
//
// There are two hand-maintained copies of the AArch64 system-register /
// PSTATE / DC / TLBI name→encoding tables:
//
//   - The Go authority in this package (namedSysRegs, pstateFields,
//     dcOps, tlbiOps).
//   - The Z80 page-13 payload src/m3/sysreg_data.asm, which the SAM
//     assembler HLOADs and walks at runtime.
//
// The audit flagged this hand-sync as a real drift hazard: adding or
// editing a register on one side and forgetting the other would pass
// each side's own tests but silently diverge (the release byte-match
// only catches it if a fixture happens to exercise the changed entry).
//
// This test closes that gap. The Z80 table is intentionally a SUBSET of
// the Go table (it carries only the entries the M5/M6 fixtures exercise;
// everything else is handled at runtime by the generic Sn_op1_Cm_Cn_op2
// parser). So the invariant we enforce is:
//
//	every entry present in src/m3/sysreg_data.asm MUST appear in the
//	corresponding Go map with a BYTE-IDENTICAL encoding.
//
// A Z80 entry whose name is absent from Go, or whose (op0,op1,CRn,CRm,
// op2) / (op1,op2) / (op1,CRn,CRm,op2) / (op1,CRn,CRm,op2,NeedsXt) fields
// disagree with Go, fails the test with a precise diff.
//
// Parsing strategy: we read sysreg_data.asm, isolate each `<name>_table:`
// section, flatten every `defb`/`defm` byte in source order into one byte
// stream, then decode records exactly the way the Z80 matcher (do_match in
// sysreg_data.asm) does — `[name_len u8][name bytes][N field bytes]`,
// terminated by name_len == 0, with N fixed per table. Decoding the same
// bytes the SAM assembler consumes makes the guard faithful to runtime.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// asmPath returns the absolute path to src/m3/sysreg_data.asm, resolved
// relative to this test file's own location so it works regardless of the
// caller's working directory (go test sets cwd to the package dir).
func asmPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate test source")
	}
	// thisFile = <repo>/tools/sam-aarch64-format/sysregs_z80sync_test.go
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	p := filepath.Join(repoRoot, "src", "m3", "sysreg_data.asm")
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("resolving asm path: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("sysreg_data.asm not found at %s: %v", abs, err)
	}
	return abs
}

// z80Entry is one decoded table record: a lower-case name and its field
// bytes (length is table-dependent).
type z80Entry struct {
	name   string
	fields []byte
}

// parseTableBytes extracts the flat byte stream of a single
// `<label>:` ... section from the .asm source. It starts at the line
// `<label>:` and consumes `defb`/`defm` directives until the next
// column-0 label (a line beginning `someident:`) or EOF. Each `defb`
// contributes its (decimal/hex) byte values; each `defm "..."`
// contributes the literal string's bytes. Comments (`;`...) and blank
// lines are ignored.
func parseTableBytes(t *testing.T, src, label string) []byte {
	t.Helper()
	lines := strings.Split(src, "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), label+":") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("table label %q not found in sysreg_data.asm", label)
	}

	var out []byte
	for i := start + 1; i < len(lines); i++ {
		raw := lines[i]
		// Strip trailing comment.
		if idx := strings.IndexByte(raw, ';'); idx >= 0 {
			raw = raw[:idx]
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		// A new column-0 label terminates this table. Column-0 labels
		// are non-indented and end with ':' on their first token.
		if raw[0] != ' ' && raw[0] != '\t' {
			if tok := strings.Fields(trimmed)[0]; strings.HasSuffix(tok, ":") {
				break
			}
		}

		fields := strings.Fields(trimmed)
		op := strings.ToLower(fields[0])
		rest := strings.TrimSpace(trimmed[len(fields[0]):])
		switch op {
		case "defb":
			for _, tok := range strings.Split(rest, ",") {
				out = append(out, parseByte(t, label, strings.TrimSpace(tok)))
			}
		case "defm":
			out = append(out, parseDefm(t, label, rest)...)
		default:
			t.Fatalf("table %q: unexpected directive %q on line %d: %q",
				label, op, i+1, trimmed)
		}
	}
	return out
}

// parseByte parses a pyz80 byte literal: decimal (`14`) or hex (`&0f`).
func parseByte(t *testing.T, label, tok string) byte {
	t.Helper()
	var v uint64
	var err error
	switch {
	case strings.HasPrefix(tok, "&"):
		v, err = strconv.ParseUint(tok[1:], 16, 16)
	case strings.HasPrefix(tok, "0x"), strings.HasPrefix(tok, "0X"):
		v, err = strconv.ParseUint(tok[2:], 16, 16)
	default:
		v, err = strconv.ParseUint(tok, 10, 16)
	}
	if err != nil || v > 0xff {
		t.Fatalf("table %q: cannot parse byte literal %q (val=%d err=%v)",
			label, tok, v, err)
	}
	return byte(v)
}

// parseDefm extracts the bytes of a single double-quoted string operand
// (the only defm form used in this file).
func parseDefm(t *testing.T, label, rest string) []byte {
	t.Helper()
	first := strings.IndexByte(rest, '"')
	last := strings.LastIndexByte(rest, '"')
	if first < 0 || last <= first {
		t.Fatalf("table %q: defm operand is not a quoted string: %q", label, rest)
	}
	return []byte(rest[first+1 : last])
}

// decodeRecords walks a flattened table byte stream as
// `[name_len][name bytes][nFields field bytes]` records, terminated by
// name_len == 0. Names are returned as-is (the .asm stores them
// lower-case, matching the Go maps' lower-case keys).
func decodeRecords(t *testing.T, label string, b []byte, nFields int) []z80Entry {
	t.Helper()
	var out []z80Entry
	i := 0
	for {
		if i >= len(b) {
			t.Fatalf("table %q: ran off end of byte stream without a 0 terminator", label)
		}
		nameLen := int(b[i])
		i++
		if nameLen == 0 {
			break // terminator
		}
		if i+nameLen+nFields > len(b) {
			t.Fatalf("table %q: truncated record (name_len=%d, want %d fields, %d bytes left)",
				label, nameLen, nFields, len(b)-i)
		}
		name := string(b[i : i+nameLen])
		i += nameLen
		fields := append([]byte(nil), b[i:i+nFields]...)
		i += nFields
		out = append(out, z80Entry{name: name, fields: fields})
	}
	return out
}

func readAsm(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(asmPath(t))
	if err != nil {
		t.Fatalf("reading sysreg_data.asm: %v", err)
	}
	return string(data)
}

// checkSubset asserts every Z80 entry name is present in goNames with
// byte-identical fields (goFields). Reports every divergence, not just
// the first, so a single run surfaces all drift.
func checkSubset(t *testing.T, table string, z80 []z80Entry, goFields func(name string) ([]byte, bool)) {
	t.Helper()
	var problems []string
	for _, e := range z80 {
		want, ok := goFields(e.name)
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"  %-14q present in Z80 %s but ABSENT from the Go table", e.name, table))
			continue
		}
		if !bytesEqual(e.fields, want) {
			problems = append(problems, fmt.Sprintf(
				"  %-14q encoding mismatch in %s: Z80=%v Go=%v", e.name, table, e.fields, want))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Errorf("%s: Z80 src/m3/sysreg_data.asm disagrees with Go tools/sam-aarch64-format:\n%s\n\n"+
			"These two tables are hand-synced — update BOTH sides (see the header "+
			"comments in sysreg_data.asm and sysregs.go).", table, strings.Join(problems, "\n"))
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSysregZ80SyncSysreg(t *testing.T) {
	src := readAsm(t)
	z80 := decodeRecords(t, "sysreg_table", parseTableBytes(t, src, "sysreg_table"), 5)
	if len(z80) == 0 {
		t.Fatal("sysreg_table parsed to zero entries; parser is broken")
	}
	checkSubset(t, "sysreg_table", z80, func(name string) ([]byte, bool) {
		sr, ok := namedSysRegs[name]
		if !ok {
			return nil, false
		}
		return []byte{sr.Op0, sr.Op1, sr.CRn, sr.CRm, sr.Op2}, true
	})
}

func TestSysregZ80SyncPState(t *testing.T) {
	src := readAsm(t)
	z80 := decodeRecords(t, "pstate_table", parseTableBytes(t, src, "pstate_table"), 2)
	if len(z80) == 0 {
		t.Fatal("pstate_table parsed to zero entries; parser is broken")
	}
	checkSubset(t, "pstate_table", z80, func(name string) ([]byte, bool) {
		pf, ok := pstateFields[name]
		if !ok {
			return nil, false
		}
		return []byte{pf.Op1, pf.Op2}, true
	})
}

func TestSysregZ80SyncDC(t *testing.T) {
	src := readAsm(t)
	z80 := decodeRecords(t, "dc_table", parseTableBytes(t, src, "dc_table"), 4)
	if len(z80) == 0 {
		t.Fatal("dc_table parsed to zero entries; parser is broken")
	}
	checkSubset(t, "dc_table", z80, func(name string) ([]byte, bool) {
		op, ok := dcOps[name]
		if !ok {
			return nil, false
		}
		return []byte{op.Op1, op.CRn, op.CRm, op.Op2}, true
	})
}

func TestSysregZ80SyncTLBI(t *testing.T) {
	src := readAsm(t)
	z80 := decodeRecords(t, "tlbi_table", parseTableBytes(t, src, "tlbi_table"), 5)
	if len(z80) == 0 {
		t.Fatal("tlbi_table parsed to zero entries; parser is broken")
	}
	checkSubset(t, "tlbi_table", z80, func(name string) ([]byte, bool) {
		op, ok := tlbiOps[name]
		if !ok {
			return nil, false
		}
		needsXt := byte(0)
		if op.NeedsXt {
			needsXt = 1
		}
		return []byte{op.Op1, op.CRn, op.CRm, op.Op2, needsXt}, true
	})
}
