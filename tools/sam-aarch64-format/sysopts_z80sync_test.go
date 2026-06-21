package format

// Go↔Z80 sync guard for the AT / IC / barrier-option tables.
//
// The Z80 disassembler's at/ic/barrier tables live in the GENERATED file
// src/disasm_sysopts.inc, projected by tools/tables-gen from the Go authority
// in this package (atOps / icOps / barrierOptions). The freshness guard
// `make tables-sync-check` ties the committed .inc to the emitter output, and
// the disasm oracle (TestDisasmOracle) covers the BARRIER options behaviourally
// over the release corpus.
//
// But the AT and IC ops do NOT appear in release.img — the oracle cannot see
// them. This test is their guard: it decodes the committed .inc bytes the same
// way the Z80 walker (disasm_sys_find_atic) does and asserts full, two-way
// parity with the Go atOps / icOps maps. A Go-authority edit that the emitter
// mis-projects for at/ic (which no other CI gate would catch) fails here.
//
// The barrier pointer table is also checked: every CRm slot must point at the
// shared-option name the Go barrierOptions map assigns it (dsb-only ssbb/pssbb
// are NOT in this table — disasm_sys_emit_baropt handles them directly).

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// sysoptsPath resolves src/disasm_sysopts.inc relative to this test file.
func sysoptsPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate test source")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	abs, err := filepath.Abs(filepath.Join(repoRoot, "src", "disasm_sysopts.inc"))
	if err != nil {
		t.Fatalf("resolving disasm_sysopts.inc path: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("disasm_sysopts.inc not found at %s: %v", abs, err)
	}
	return abs
}

func readSysopts(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(sysoptsPath(t))
	if err != nil {
		t.Fatalf("reading disasm_sysopts.inc: %v", err)
	}
	return string(data)
}

// aticEntry is one decoded AT/IC record.
type aticEntry struct {
	op1, crm, op2 byte
	needsXt       bool
	name          string
}

// decodeATIC walks a flattened [op1][CRm][op2][NeedsXt][len][name...] stream
// the way disasm_sys_find_atic does, stopping at the 0xFF sentinel.
func decodeATIC(t *testing.T, label string, b []byte) []aticEntry {
	t.Helper()
	var out []aticEntry
	i := 0
	for {
		if i >= len(b) {
			t.Fatalf("table %q: ran off end without a 0xFF sentinel", label)
		}
		if b[i] == 0xff {
			break
		}
		if i+5 > len(b) {
			t.Fatalf("table %q: truncated record header at offset %d", label, i)
		}
		op1, crm, op2, xt := b[i], b[i+1], b[i+2], b[i+3]
		nameLen := int(b[i+4])
		i += 5
		if i+nameLen > len(b) {
			t.Fatalf("table %q: truncated name (len=%d, %d bytes left)", label, nameLen, len(b)-i)
		}
		name := string(b[i : i+nameLen])
		i += nameLen
		out = append(out, aticEntry{op1, crm, op2, xt == 1, name})
	}
	return out
}

func TestSysoptsZ80SyncAT(t *testing.T) {
	src := readSysopts(t)
	z80 := decodeATIC(t, "disasm_sys_at_tbl", parseTableBytes(t, src, "disasm_sys_at_tbl"))
	if len(z80) == 0 {
		t.Fatal("disasm_sys_at_tbl parsed to zero entries; parser is broken")
	}
	checkATICParity(t, "at", z80, atToATIC(atOps))
}

func TestSysoptsZ80SyncIC(t *testing.T) {
	src := readSysopts(t)
	z80 := decodeATIC(t, "disasm_sys_ic_tbl", parseTableBytes(t, src, "disasm_sys_ic_tbl"))
	if len(z80) == 0 {
		t.Fatal("disasm_sys_ic_tbl parsed to zero entries; parser is broken")
	}
	checkATICParity(t, "ic", z80, icToATIC(icOps))
}

func atToATIC(m map[string]ATOp) map[string]aticEntry {
	out := make(map[string]aticEntry, len(m))
	for n, op := range m {
		out[n] = aticEntry{op.Op1, op.CRm, op.Op2, op.NeedsXt, n}
	}
	return out
}

func icToATIC(m map[string]ICOp) map[string]aticEntry {
	out := make(map[string]aticEntry, len(m))
	for n, op := range m {
		out[n] = aticEntry{op.Op1, op.CRm, op.Op2, op.NeedsXt, n}
	}
	return out
}

// checkATICParity asserts the Z80 records and the Go map agree exactly, both
// directions (these families have no generic fallback, so full parity is the
// invariant — a missing entry is a real decode gap).
func checkATICParity(t *testing.T, fam string, z80 []aticEntry, goMap map[string]aticEntry) {
	t.Helper()
	var problems []string
	seen := make(map[string]bool, len(z80))
	for _, e := range z80 {
		seen[e.name] = true
		want, ok := goMap[e.name]
		if !ok {
			problems = append(problems, fmt.Sprintf("  %q present in Z80 %s table but ABSENT from Go", e.name, fam))
			continue
		}
		if e != want {
			problems = append(problems, fmt.Sprintf("  %q %s encoding mismatch: Z80=%+v Go=%+v", e.name, fam, e, want))
		}
	}
	for n := range goMap {
		if !seen[n] {
			problems = append(problems, fmt.Sprintf("  %q present in Go %s map but ABSENT from the Z80 table", n, fam))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Errorf("%s table: Z80 src/disasm_sysopts.inc disagrees with Go tools/sam-aarch64-format "+
			"(both are projected from the same authority — run `make tables`):\n%s",
			fam, strings.Join(problems, "\n"))
	}
}

// TestSysoptsZ80SyncBarrier checks the 16-entry disasm_sys_baropt_tbl pointer
// table: each CRm slot must reference the shared-option name Go assigns it, or
// be a null (0) slot for absent / dsb-only CRm values.
func TestSysoptsZ80SyncBarrier(t *testing.T) {
	src := readSysopts(t)
	got := parseBaroptTbl(t, src) // CRm → name ("" for a null slot)
	if len(got) != 16 {
		t.Fatalf("disasm_sys_baropt_tbl: expected 16 slots, got %d", len(got))
	}
	for crm := 0; crm < 16; crm++ {
		want := ""
		if opt, ok := barrierOptions[byte(crm)]; ok && !opt.DsbOnly {
			want = opt.Name
		}
		if got[crm] != want {
			t.Errorf("disasm_sys_baropt_tbl[%d]: Z80=%q Go=%q", crm, got[crm], want)
		}
	}
}

// parseBaroptTbl reads the disasm_sys_baropt_tbl section and returns, per CRm
// slot, the referenced barrier-option name (the disasm_sys_bo_<name> label
// suffix), or "" where the slot is `defw 0`.
func parseBaroptTbl(t *testing.T, src string) []string {
	t.Helper()
	lines := strings.Split(src, "\n")
	start := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "disasm_sys_baropt_tbl:" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("disasm_sys_baropt_tbl: label not found in disasm_sysopts.inc")
	}
	var out []string
	for i := start + 1; i < len(lines); i++ {
		raw := lines[i]
		if idx := strings.IndexByte(raw, ';'); idx >= 0 {
			raw = raw[:idx]
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if strings.ToLower(fields[0]) != "defw" {
			break // next section
		}
		operand := strings.TrimSpace(trimmed[len(fields[0]):])
		if operand == "0" {
			out = append(out, "")
			continue
		}
		name := strings.TrimPrefix(operand, "disasm_sys_bo_")
		if name == operand {
			t.Fatalf("disasm_sys_baropt_tbl: unexpected operand %q", operand)
		}
		out = append(out, name)
	}
	return out
}
