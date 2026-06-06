# M1: Binary Tokenised Format + text2bin/bin2text — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the binary tokenised aarch64 source format defined in `docs/specs/2026-05-23-m1-binary-tokenised-format-design.md`, plus Mac-side `text2bin` and `bin2text` tools that round-trip every Phase 1 construct, all verified by a four-layer Mac-side test pyramid in CI.

**Architecture:** Three sibling Go modules under `tools/` matching the repo's existing per-tool module pattern. `sam-aarch64-format` is a library module containing the format primitives (enums, readers, writers, expression evaluator, symbol-table interner). `text2bin` and `bin2text` are CLI binaries that import the library via local `replace` directives. Tests live next to the code they test (Go convention); end-to-end round-trip fixtures live under `tests/m1/`. CI runs the Go test suite plus a GNU-`as` cross-check shell wrapper.

**Tech Stack:** Go 1.26.1 (matches the most recent existing tools in the repo), `aarch64-none-elf-as` for the Layer 4 cross-check, Make for orchestration, GitHub Actions for CI.

**Reference:** All section numbers (§1–§10) in this plan refer to `docs/specs/2026-05-23-m1-binary-tokenised-format-design.md`.

> **Note (historical plan):** the format-enum snippets below capture the v1 `.tbn`
> shape as built during M1. The current normative record/operand/expression
> tables (now including `KindLitInsts`, `OpLitPool`, and the grown directive
> set) live in `docs/specs/2026-06-08-tbn-binary-format-reference.md`; consult
> that for the up-to-date encoding.

---

## File structure

After M1 completes, the repo gains:

```
tools/sam-aarch64-format/         # shared library (Go module)
  go.mod
  format.go                       # magic, version, file-header constants
  kinds.go                        # record-kind enum + names
  operands.go                     # operand-kind enum, shape codes, shift/extend codes, cc table
  expr.go                         # expression-bytecode opcodes + evaluator + folder
  symbols.go                      # symbol-table interner
  mnemonics.go                    # mnemonic_id ↔ name table
  directives.go                   # directive_id ↔ name table
  writer.go                       # high-level file writer
  reader.go                       # high-level file reader
  *_test.go                       # Go unit tests next to each file

tools/text2bin/                   # CLI binary (Go module)
  go.mod
  main.go                         # CLI entry + error wrapper
  lexer.go                        # source bytes → token stream
  lexer_test.go
  parser.go                       # token stream → records via the format library
  parser_test.go
  errors.go                       # file:line:col error helpers

tools/bin2text/                   # CLI binary (Go module)
  go.mod
  main.go                         # CLI entry
  emit.go                         # records → canonical text
  emit_test.go

tests/m1/
  sources/                        # `.s` fixtures, one per construct family
  golden/                         # canonical `.s` outputs
  binary/                         # hand-crafted `.tbn` fixtures (Layer 3)
  run-gnu-as-check.sh             # Layer 4 shell

.github/workflows/ci.yml          # adds the M1 job

Makefile                          # adds text2bin, bin2text, test-m1, ci-m1 targets
```

The format library is a single module to keep its internal cross-package boundaries cheap. Both binaries depend on it via `replace github.com/petemoore/sam-aarch64/tools/sam-aarch64-format => ../sam-aarch64-format` in their `go.mod` (matching the `llist-sweep → llist-capture` pattern already in the repo).

---

## Conventions used throughout this plan

- **Commits use `g`, not `git`.** The repo memory rule (`feedback_use_g_commit`).
- **Tests come first.** Every behaviour is added with a failing test, run-to-confirm-fail, implementation, run-to-confirm-pass, commit. No exceptions.
- **Commit per task.** Each task ends with a single commit. Commit message format: `m1: <subject>`.
- **Working directory:** all `go` and `g` commands run from the repo root unless a task says otherwise.
- **Run Go tests with:** `go test ./...` from inside the module directory.

---

## Task 1: Scaffold the `sam-aarch64-format` Go module

**Files:**
- Create: `tools/sam-aarch64-format/go.mod`
- Create: `tools/sam-aarch64-format/format.go`
- Test: `tools/sam-aarch64-format/format_test.go`

- [ ] **Step 1: Write the failing test**

`tools/sam-aarch64-format/format_test.go`:

```go
package format

import "testing"

func TestMagicAndVersion(t *testing.T) {
	if string(Magic[:]) != "SA64" {
		t.Errorf("Magic = %q, want \"SA64\"", string(Magic[:]))
	}
	if Version != 1 {
		t.Errorf("Version = %d, want 1", Version)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
mkdir -p tools/sam-aarch64-format
cd tools/sam-aarch64-format
go mod init github.com/petemoore/sam-aarch64/tools/sam-aarch64-format
echo 'go 1.26.1' # confirm in go.mod, edit if needed
go test ./...
```

Expected: FAIL — `format.go` does not exist; `Magic`/`Version` undefined.

- [ ] **Step 3: Implement minimal `format.go`**

`tools/sam-aarch64-format/format.go`:

```go
// Package format implements the M1 binary tokenised aarch64 source
// format. Section numbers (§N) in comments refer to
// docs/specs/2026-05-23-m1-binary-tokenised-format-design.md.
package format

// Magic is the 4-byte file header tag (§2).
var Magic = [4]byte{'S', 'A', '6', '4'}

// Version is the on-disk format version (§2). v1 is the only release.
const Version uint16 = 1

// Flags is reserved in v1 and must be zero.
const Flags uint16 = 0
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd tools/sam-aarch64-format && go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: scaffold sam-aarch64-format module with magic + version"
```

---

## Task 2: Record-kind enum

**Files:**
- Create: `tools/sam-aarch64-format/kinds.go`
- Test: `tools/sam-aarch64-format/kinds_test.go`

- [ ] **Step 1: Write the failing test**

`tools/sam-aarch64-format/kinds_test.go`:

```go
package format

import "testing"

func TestRecordKindValues(t *testing.T) {
	cases := []struct {
		k    RecordKind
		want byte
	}{
		{KindInst, 0x01},
		{KindLabelDef, 0x02},
		{KindLocalDef, 0x03},
		{KindDirective, 0x04},
		{KindComment, 0x05},
	}
	for _, c := range cases {
		if byte(c.k) != c.want {
			t.Errorf("%s = 0x%02x, want 0x%02x", c.k.Name(), byte(c.k), c.want)
		}
	}
}

func TestRecordKindNames(t *testing.T) {
	if KindInst.Name() != "INST" {
		t.Errorf("KindInst.Name() = %q, want %q", KindInst.Name(), "INST")
	}
	if KindComment.Name() != "COMMENT" {
		t.Errorf("KindComment.Name() = %q, want %q", KindComment.Name(), "COMMENT")
	}
}

func TestRecordKindIsKnown(t *testing.T) {
	if !KindInst.IsKnown() {
		t.Errorf("KindInst.IsKnown() = false, want true")
	}
	if RecordKind(0xFF).IsKnown() {
		t.Errorf("RecordKind(0xFF).IsKnown() = true, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd tools/sam-aarch64-format && go test ./...
```

Expected: FAIL — `RecordKind`, `KindInst`, etc. undefined.

- [ ] **Step 3: Implement `kinds.go`**

```go
package format

// RecordKind identifies a record's payload shape (§3).
type RecordKind byte

const (
	KindInst      RecordKind = 0x01
	KindLabelDef  RecordKind = 0x02
	KindLocalDef  RecordKind = 0x03
	KindDirective RecordKind = 0x04
	KindComment   RecordKind = 0x05
)

// Name returns the symbolic name of the record kind, or "UNKNOWN" for
// reserved or future kinds.
func (k RecordKind) Name() string {
	switch k {
	case KindInst:
		return "INST"
	case KindLabelDef:
		return "LABEL_DEF"
	case KindLocalDef:
		return "LOCAL_DEF"
	case KindDirective:
		return "DIRECTIVE"
	case KindComment:
		return "COMMENT"
	}
	return "UNKNOWN"
}

// IsKnown reports whether the record kind is defined in format v1.
func (k RecordKind) IsKnown() bool {
	return k.Name() != "UNKNOWN"
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add RecordKind enum"
```

---

## Task 3: Operand-kind enum + shape/shift/extend codes

**Files:**
- Create: `tools/sam-aarch64-format/operands.go`
- Test: `tools/sam-aarch64-format/operands_test.go`

- [ ] **Step 1: Write the failing test**

```go
package format

import "testing"

func TestOperandKindValues(t *testing.T) {
	cases := []struct {
		k    OperandKind
		want byte
	}{
		{OpRegX, 0x01}, {OpRegW, 0x02}, {OpRegXSP, 0x03}, {OpRegWSP, 0x04},
		{OpImmExpr, 0x05}, {OpShiftedReg, 0x06}, {OpExtendedReg, 0x07},
		{OpMem, 0x08}, {OpString, 0x09}, {OpCond, 0x0A}, {OpSysName, 0x0B},
	}
	for _, c := range cases {
		if byte(c.k) != c.want {
			t.Errorf("%s = 0x%02x, want 0x%02x", c.k.Name(), byte(c.k), c.want)
		}
	}
}

func TestMemShapeValues(t *testing.T) {
	if MemBase != 0 || MemBaseOff != 1 || MemBaseOffPre != 2 || MemBaseOffPost != 3 ||
		MemBaseIdx != 4 || MemBaseIdxShifted != 5 || MemBaseIdxExtended != 6 {
		t.Errorf("MemShape constants do not match §4 sub-shapes")
	}
}

func TestShiftKindNames(t *testing.T) {
	if ShiftLSL.Name() != "lsl" || ShiftLSR.Name() != "lsr" ||
		ShiftASR.Name() != "asr" || ShiftROR.Name() != "ror" {
		t.Errorf("ShiftKind names do not match aarch64 syntax")
	}
}

func TestExtendKindNames(t *testing.T) {
	want := []string{"uxtb", "uxth", "uxtw", "uxtx", "sxtb", "sxth", "sxtw", "sxtx"}
	for i, n := range want {
		if ExtendKind(i).Name() != n {
			t.Errorf("ExtendKind(%d).Name() = %q, want %q", i, ExtendKind(i).Name(), n)
		}
	}
}

func TestCondCodeNames(t *testing.T) {
	if CondEQ.Name() != "eq" || CondAL.Name() != "al" || CondNV.Name() != "nv" {
		t.Errorf("cond-code names do not match aarch64 syntax")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement `operands.go`**

```go
package format

// OperandKind tags each operand with its on-disk shape (§4).
type OperandKind byte

const (
	OpRegX        OperandKind = 0x01
	OpRegW        OperandKind = 0x02
	OpRegXSP      OperandKind = 0x03
	OpRegWSP      OperandKind = 0x04
	OpImmExpr     OperandKind = 0x05
	OpShiftedReg  OperandKind = 0x06
	OpExtendedReg OperandKind = 0x07
	OpMem         OperandKind = 0x08
	OpString      OperandKind = 0x09
	OpCond        OperandKind = 0x0A
	OpSysName     OperandKind = 0x0B
)

func (k OperandKind) Name() string {
	switch k {
	case OpRegX:
		return "REG_X"
	case OpRegW:
		return "REG_W"
	case OpRegXSP:
		return "REG_X_SP"
	case OpRegWSP:
		return "REG_W_SP"
	case OpImmExpr:
		return "IMM_EXPR"
	case OpShiftedReg:
		return "SHIFTED_REG"
	case OpExtendedReg:
		return "EXTENDED_REG"
	case OpMem:
		return "MEM"
	case OpString:
		return "STRING"
	case OpCond:
		return "COND"
	case OpSysName:
		return "SYS_NAME"
	}
	return "UNKNOWN"
}

// MemShape sub-codes for the MEM operand (§4).
type MemShape byte

const (
	MemBase            MemShape = 0
	MemBaseOff         MemShape = 1
	MemBaseOffPre      MemShape = 2
	MemBaseOffPost     MemShape = 3
	MemBaseIdx         MemShape = 4
	MemBaseIdxShifted  MemShape = 5
	MemBaseIdxExtended MemShape = 6
)

// ShiftKind for SHIFTED_REG operands.
type ShiftKind byte

const (
	ShiftLSL ShiftKind = 0
	ShiftLSR ShiftKind = 1
	ShiftASR ShiftKind = 2
	ShiftROR ShiftKind = 3
)

func (s ShiftKind) Name() string {
	return [...]string{"lsl", "lsr", "asr", "ror"}[s]
}

// ExtendKind for EXTENDED_REG operands.
type ExtendKind byte

const (
	ExtUXTB ExtendKind = 0
	ExtUXTH ExtendKind = 1
	ExtUXTW ExtendKind = 2
	ExtUXTX ExtendKind = 3
	ExtSXTB ExtendKind = 4
	ExtSXTH ExtendKind = 5
	ExtSXTW ExtendKind = 6
	ExtSXTX ExtendKind = 7
)

func (e ExtendKind) Name() string {
	return [...]string{"uxtb", "uxth", "uxtw", "uxtx", "sxtb", "sxth", "sxtw", "sxtx"}[e]
}

// CondCode for COND operands. Values match the aarch64 encoding.
type CondCode byte

const (
	CondEQ CondCode = 0
	CondNE CondCode = 1
	CondCS CondCode = 2
	CondCC CondCode = 3
	CondMI CondCode = 4
	CondPL CondCode = 5
	CondVS CondCode = 6
	CondVC CondCode = 7
	CondHI CondCode = 8
	CondLS CondCode = 9
	CondGE CondCode = 10
	CondLT CondCode = 11
	CondGT CondCode = 12
	CondLE CondCode = 13
	CondAL CondCode = 14
	CondNV CondCode = 15
)

func (c CondCode) Name() string {
	return [...]string{
		"eq", "ne", "cs", "cc", "mi", "pl", "vs", "vc",
		"hi", "ls", "ge", "lt", "gt", "le", "al", "nv",
	}[c]
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add OperandKind + MemShape + ShiftKind + ExtendKind + CondCode enums"
```

---

## Task 4: Expression-bytecode opcode constants

**Files:**
- Create: `tools/sam-aarch64-format/expr.go`
- Test: `tools/sam-aarch64-format/expr_test.go`

- [ ] **Step 1: Write the failing test**

```go
package format

import "testing"

func TestExprOpcodeValues(t *testing.T) {
	cases := map[ExprOp]byte{
		OpPushImm8: 0x01, OpPushImm16: 0x02, OpPushImm32: 0x03, OpPushImm64: 0x04,
		OpPushSym: 0x05, OpPushLocal: 0x06, OpPushPC: 0x07,
		OpAdd: 0x10, OpSub: 0x11, OpMul: 0x12, OpDiv: 0x13,
		OpAnd: 0x14, OpOr: 0x15, OpXor: 0x16, OpShl: 0x17, OpShr: 0x18,
		OpNeg: 0x20, OpNot: 0x21,
		OpRelLo12: 0x30, OpRelHi12: 0x31,
		OpRelAbsG0: 0x32, OpRelAbsG0NC: 0x33,
		OpRelAbsG1: 0x34, OpRelAbsG1NC: 0x35,
		OpRelAbsG2: 0x36, OpRelAbsG2NC: 0x37,
		OpRelAbsG3: 0x38,
	}
	for op, want := range cases {
		if byte(op) != want {
			t.Errorf("%s = 0x%02x, want 0x%02x", op.Name(), byte(op), want)
		}
	}
}

func TestExprOpcodeName(t *testing.T) {
	if OpAdd.Name() != "ADD" {
		t.Errorf("OpAdd.Name() = %q, want %q", OpAdd.Name(), "ADD")
	}
	if ExprOp(0xFF).Name() != "UNKNOWN" {
		t.Errorf("unknown opcode name should be UNKNOWN")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Implement `expr.go`** (opcode catalogue only; reader/writer/evaluator are added in later tasks)

```go
package format

// ExprOp is one byte of expression bytecode (§5).
type ExprOp byte

const (
	OpPushImm8  ExprOp = 0x01
	OpPushImm16 ExprOp = 0x02
	OpPushImm32 ExprOp = 0x03
	OpPushImm64 ExprOp = 0x04
	OpPushSym   ExprOp = 0x05
	OpPushLocal ExprOp = 0x06
	OpPushPC    ExprOp = 0x07

	OpAdd ExprOp = 0x10
	OpSub ExprOp = 0x11
	OpMul ExprOp = 0x12
	OpDiv ExprOp = 0x13
	OpAnd ExprOp = 0x14
	OpOr  ExprOp = 0x15
	OpXor ExprOp = 0x16
	OpShl ExprOp = 0x17
	OpShr ExprOp = 0x18

	OpNeg ExprOp = 0x20
	OpNot ExprOp = 0x21

	OpRelLo12    ExprOp = 0x30
	OpRelHi12    ExprOp = 0x31
	OpRelAbsG0   ExprOp = 0x32
	OpRelAbsG0NC ExprOp = 0x33
	OpRelAbsG1   ExprOp = 0x34
	OpRelAbsG1NC ExprOp = 0x35
	OpRelAbsG2   ExprOp = 0x36
	OpRelAbsG2NC ExprOp = 0x37
	OpRelAbsG3   ExprOp = 0x38
)

func (o ExprOp) Name() string {
	switch o {
	case OpPushImm8:
		return "PUSH_IMM8"
	case OpPushImm16:
		return "PUSH_IMM16"
	case OpPushImm32:
		return "PUSH_IMM32"
	case OpPushImm64:
		return "PUSH_IMM64"
	case OpPushSym:
		return "PUSH_SYM"
	case OpPushLocal:
		return "PUSH_LOCAL"
	case OpPushPC:
		return "PUSH_PC"
	case OpAdd:
		return "ADD"
	case OpSub:
		return "SUB"
	case OpMul:
		return "MUL"
	case OpDiv:
		return "DIV"
	case OpAnd:
		return "AND"
	case OpOr:
		return "OR"
	case OpXor:
		return "XOR"
	case OpShl:
		return "SHL"
	case OpShr:
		return "SHR"
	case OpNeg:
		return "NEG"
	case OpNot:
		return "NOT"
	case OpRelLo12:
		return "REL_LO12"
	case OpRelHi12:
		return "REL_HI12"
	case OpRelAbsG0:
		return "REL_ABS_G0"
	case OpRelAbsG0NC:
		return "REL_ABS_G0_NC"
	case OpRelAbsG1:
		return "REL_ABS_G1"
	case OpRelAbsG1NC:
		return "REL_ABS_G1_NC"
	case OpRelAbsG2:
		return "REL_ABS_G2"
	case OpRelAbsG2NC:
		return "REL_ABS_G2_NC"
	case OpRelAbsG3:
		return "REL_ABS_G3"
	}
	return "UNKNOWN"
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add ExprOp opcode catalogue"
```

---

## Task 5: Mnemonic-id table

**Files:**
- Create: `tools/sam-aarch64-format/mnemonics.go`
- Test: `tools/sam-aarch64-format/mnemonics_test.go`

The initial table contains exactly the mnemonics text2bin needs to parse the M1 fixture corpus (Task 26). Spec §9.1 records that the long-term source of truth is M2's decision; M1 hand-curates. Starter set targets the small but representative subset: `add`, `sub`, `mov`, `mvn`, `ldr`, `str`, `ldp`, `stp`, `b`, `b.cond` (encoded as `b` + COND operand at the parser layer? — see Task 17 decision), `bl`, `br`, `ret`, `adrp`, `nop`, `and`, `orr`, `eor`, `lsl`, `lsr`, `cmp`, `cbz`, `cbnz`, `tbz`, `tbnz`, `csel`, `csinc`. (Pete: this list is a starting point. Extend during Task 26 as fixtures demand.)

- [ ] **Step 1: Write the failing test**

```go
package format

import "testing"

func TestMnemonicTableLookup(t *testing.T) {
	id, ok := MnemonicID("add")
	if !ok {
		t.Fatalf("MnemonicID(\"add\") not found")
	}
	if MnemonicName(id) != "add" {
		t.Errorf("round-trip failed: %d -> %q", id, MnemonicName(id))
	}
}

func TestMnemonicTableUnknown(t *testing.T) {
	if _, ok := MnemonicID("not_a_real_mnemonic"); ok {
		t.Errorf("MnemonicID returned ok for nonsense input")
	}
	if MnemonicName(0xFFFF) != "" {
		t.Errorf("MnemonicName(0xFFFF) should return empty string")
	}
}

func TestMnemonicIDsStable(t *testing.T) {
	// The initial subset must include at least `nop` and `add`. The
	// exact ID values are append-only by index; we pin the first two
	// here so re-ordering the table cannot land silently.
	id, _ := MnemonicID("nop")
	if id != 0 {
		t.Errorf("MnemonicID(\"nop\") = %d, want 0", id)
	}
	id, _ = MnemonicID("add")
	if id != 1 {
		t.Errorf("MnemonicID(\"add\") = %d, want 1", id)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Implement `mnemonics.go`**

```go
package format

// MnemonicTable is the append-only ID ↔ name map for aarch64
// mnemonics that text2bin recognises. New mnemonics are appended;
// existing IDs never shift (§3, §9.1).
//
// Index in the slice is the on-disk mnemonic_id.
var MnemonicTable = []string{
	"nop", "add", "sub", "mov", "mvn",
	"ldr", "str", "ldp", "stp",
	"b", "bl", "br", "ret",
	"adrp",
	"and", "orr", "eor",
	"lsl", "lsr",
	"cmp",
	"cbz", "cbnz", "tbz", "tbnz",
	"csel", "csinc",
}

var mnemonicIndex = func() map[string]uint16 {
	m := make(map[string]uint16, len(MnemonicTable))
	for i, n := range MnemonicTable {
		m[n] = uint16(i)
	}
	return m
}()

// MnemonicID returns the on-disk ID for a mnemonic name. ok=false if
// the name is not in the table.
func MnemonicID(name string) (uint16, bool) {
	id, ok := mnemonicIndex[name]
	return id, ok
}

// MnemonicName returns the name for an on-disk mnemonic ID, or "" if
// the ID is out of range.
func MnemonicName(id uint16) string {
	if int(id) >= len(MnemonicTable) {
		return ""
	}
	return MnemonicTable[id]
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add mnemonic-id table (initial subset)"
```

---

## Task 6: Directive-id table

**Files:**
- Create: `tools/sam-aarch64-format/directives.go`
- Test: `tools/sam-aarch64-format/directives_test.go`

The directive set is fixed by the Phase 1 spec §2 list. Unlike mnemonics, the directive set is small and unlikely to grow much.

- [ ] **Step 1: Write the failing test**

```go
package format

import "testing"

func TestDirectiveTableLookup(t *testing.T) {
	id, ok := DirectiveID(".byte")
	if !ok {
		t.Fatalf("DirectiveID(\".byte\") not found")
	}
	if DirectiveName(id) != ".byte" {
		t.Errorf("round-trip failed: %d -> %q", id, DirectiveName(id))
	}
}

func TestDirectiveExpectedSet(t *testing.T) {
	want := []string{
		".text", ".data",
		".byte", ".short", ".word", ".quad",
		".ascii", ".asciz",
		".equ", ".set",
		".global",
		".balign",
		".org",
		".skip", ".space",
		".inst",
	}
	for _, n := range want {
		if _, ok := DirectiveID(n); !ok {
			t.Errorf("expected directive %q missing from table", n)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Implement `directives.go`**

```go
package format

// DirectiveTable: append-only ID ↔ name map (§3).
var DirectiveTable = []string{
	".text", ".data",
	".byte", ".short", ".word", ".quad",
	".ascii", ".asciz",
	".equ", ".set",
	".global",
	".balign",
	".org",
	".skip", ".space",
	".inst",
}

var directiveIndex = func() map[string]uint8 {
	m := make(map[string]uint8, len(DirectiveTable))
	for i, n := range DirectiveTable {
		m[n] = uint8(i)
	}
	return m
}()

func DirectiveID(name string) (uint8, bool) {
	id, ok := directiveIndex[name]
	return id, ok
}

func DirectiveName(id uint8) string {
	if int(id) >= len(DirectiveTable) {
		return ""
	}
	return DirectiveTable[id]
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add directive-id table"
```

---

## Task 7: Symbol-table interner

**Files:**
- Create: `tools/sam-aarch64-format/symbols.go`
- Test: `tools/sam-aarch64-format/symbols_test.go`

- [ ] **Step 1: Write the failing test**

```go
package format

import "testing"

func TestSymbolTableInternFirstEncounter(t *testing.T) {
	st := NewSymbolTable()
	id := st.Intern("loop")
	if id != 0 {
		t.Errorf("first intern() = %d, want 0", id)
	}
	if got := st.Intern("loop"); got != 0 {
		t.Errorf("second intern of same name = %d, want 0", got)
	}
	id2 := st.Intern("exit")
	if id2 != 1 {
		t.Errorf("intern second name = %d, want 1", id2)
	}
}

func TestSymbolTableName(t *testing.T) {
	st := NewSymbolTable()
	st.Intern("alpha")
	st.Intern("beta")
	if st.Name(0) != "alpha" || st.Name(1) != "beta" {
		t.Errorf("Name lookup failed")
	}
	if st.Name(99) != "" {
		t.Errorf("out-of-range Name should return \"\"")
	}
}

func TestSymbolTableLen(t *testing.T) {
	st := NewSymbolTable()
	if st.Len() != 0 {
		t.Errorf("empty table Len() = %d, want 0", st.Len())
	}
	st.Intern("a")
	st.Intern("b")
	st.Intern("a") // duplicate
	if st.Len() != 2 {
		t.Errorf("Len() = %d after two distinct interns, want 2", st.Len())
	}
}

func TestSymbolTableNames(t *testing.T) {
	st := NewSymbolTable()
	st.Intern("a")
	st.Intern("b")
	names := st.Names()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Errorf("Names() = %v, want [a b]", names)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Implement `symbols.go`**

```go
package format

// SymbolTable interns label names in first-encounter order; the
// returned ID is the name's index in the table (§2 name table).
type SymbolTable struct {
	names []string
	index map[string]uint16
}

func NewSymbolTable() *SymbolTable {
	return &SymbolTable{index: make(map[string]uint16)}
}

// Intern returns the ID for name, allocating a new ID if it is the
// first time the name has been seen.
func (st *SymbolTable) Intern(name string) uint16 {
	if id, ok := st.index[name]; ok {
		return id
	}
	id := uint16(len(st.names))
	st.names = append(st.names, name)
	st.index[name] = id
	return id
}

// Name returns the interned name for an ID, or "" if the ID is out
// of range.
func (st *SymbolTable) Name(id uint16) string {
	if int(id) >= len(st.names) {
		return ""
	}
	return st.names[id]
}

// Len returns the number of distinct names interned.
func (st *SymbolTable) Len() int {
	return len(st.names)
}

// Names returns the name table in ID order. The caller must not
// mutate the returned slice.
func (st *SymbolTable) Names() []string {
	return st.names
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add symbol-table interner"
```

---

## Task 8: Expression-bytecode writer

**Files:**
- Modify: `tools/sam-aarch64-format/expr.go`
- Test: `tools/sam-aarch64-format/expr_writer_test.go`

The writer emits opcodes into a `[]byte`, with helpers for the variable-length push opcodes that pick the shortest fit.

- [ ] **Step 1: Write the failing test**

```go
package format

import (
	"bytes"
	"testing"
)

func TestExprWriteImmShortestFit(t *testing.T) {
	cases := []struct {
		v    int64
		want []byte
	}{
		{0, []byte{byte(OpPushImm8), 0x00}},
		{127, []byte{byte(OpPushImm8), 0x7F}},
		{-128, []byte{byte(OpPushImm8), 0x80}},
		{128, []byte{byte(OpPushImm16), 0x80, 0x00}},
		{-129, []byte{byte(OpPushImm16), 0x7F, 0xFF}},
		{32767, []byte{byte(OpPushImm16), 0xFF, 0x7F}},
		{32768, []byte{byte(OpPushImm32), 0x00, 0x80, 0x00, 0x00}},
		{int64(1) << 31, []byte{byte(OpPushImm64),
			0x00, 0x00, 0x00, 0x80, 0x00, 0x00, 0x00, 0x00}},
	}
	for _, c := range cases {
		var w ExprWriter
		w.WriteImm(c.v)
		if !bytes.Equal(w.Bytes(), c.want) {
			t.Errorf("WriteImm(%d) = % X, want % X", c.v, w.Bytes(), c.want)
		}
	}
}

func TestExprWriteSymAndLocal(t *testing.T) {
	var w ExprWriter
	w.WriteSym(0x1234)
	w.WriteLocal(3, 0)
	want := []byte{
		byte(OpPushSym), 0x34, 0x12,
		byte(OpPushLocal), 0x03, 0x00,
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}

func TestExprWriteOps(t *testing.T) {
	var w ExprWriter
	w.WriteSym(7)
	w.WriteImm(4)
	w.WriteOp(OpAdd)
	want := []byte{
		byte(OpPushSym), 0x07, 0x00,
		byte(OpPushImm8), 0x04,
		byte(OpAdd),
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — `ExprWriter` undefined.

- [ ] **Step 3: Append writer to `expr.go`**

```go
// ExprWriter builds an expression bytecode stream.
type ExprWriter struct {
	buf []byte
}

// Bytes returns the accumulated bytecode. The caller must not mutate
// the returned slice.
func (w *ExprWriter) Bytes() []byte { return w.buf }

// Reset clears the writer for reuse.
func (w *ExprWriter) Reset() { w.buf = w.buf[:0] }

// WriteOp writes a 0-argument opcode (binary or unary operator).
func (w *ExprWriter) WriteOp(op ExprOp) {
	w.buf = append(w.buf, byte(op))
}

// WriteSym writes PUSH_SYM with a u16 LE symbol id.
func (w *ExprWriter) WriteSym(id uint16) {
	w.buf = append(w.buf, byte(OpPushSym), byte(id), byte(id>>8))
}

// WriteLocal writes PUSH_LOCAL with a digit (1–9) and a direction
// byte (0=f, 1=b).
func (w *ExprWriter) WriteLocal(digit, dir byte) {
	w.buf = append(w.buf, byte(OpPushLocal), digit, dir)
}

// WritePC writes the PUSH_PC opcode (the `.` operator).
func (w *ExprWriter) WritePC() { w.buf = append(w.buf, byte(OpPushPC)) }

// WriteImm writes a literal using the shortest PUSH_IMMn that fits.
func (w *ExprWriter) WriteImm(v int64) {
	switch {
	case v >= -128 && v <= 127:
		w.buf = append(w.buf, byte(OpPushImm8), byte(v))
	case v >= -32768 && v <= 32767:
		w.buf = append(w.buf, byte(OpPushImm16), byte(v), byte(v>>8))
	case v >= -(int64(1)<<31) && v <= (int64(1)<<31)-1:
		w.buf = append(w.buf, byte(OpPushImm32),
			byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	default:
		w.buf = append(w.buf, byte(OpPushImm64),
			byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
			byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add expression-bytecode writer"
```

---

## Task 9: Expression-bytecode reader + constant-only evaluator

The Z80 evaluator is M3 work. M1 needs only enough to support the constant-folder and round-trip tests: a reader that walks the bytecode, plus an `Eval` that succeeds when all push operations are constants.

**Files:**
- Modify: `tools/sam-aarch64-format/expr.go`
- Test: `tools/sam-aarch64-format/expr_eval_test.go`

- [ ] **Step 1: Write the failing test**

```go
package format

import "testing"

func TestExprEvalConstFold(t *testing.T) {
	var w ExprWriter
	w.WriteImm(7)
	w.WriteImm(3)
	w.WriteOp(OpAdd)
	v, ok := EvalConst(w.Bytes())
	if !ok || v != 10 {
		t.Errorf("EvalConst(7+3) = (%d,%v), want (10,true)", v, ok)
	}
}

func TestExprEvalNotConst(t *testing.T) {
	var w ExprWriter
	w.WriteSym(0)
	w.WriteImm(1)
	w.WriteOp(OpAdd)
	if _, ok := EvalConst(w.Bytes()); ok {
		t.Errorf("EvalConst with PUSH_SYM should return ok=false")
	}
}

func TestExprEvalUnary(t *testing.T) {
	var w ExprWriter
	w.WriteImm(5)
	w.WriteOp(OpNeg)
	v, ok := EvalConst(w.Bytes())
	if !ok || v != -5 {
		t.Errorf("EvalConst(-5) = (%d,%v), want (-5,true)", v, ok)
	}
}

func TestExprEvalShift(t *testing.T) {
	var w ExprWriter
	w.WriteImm(1)
	w.WriteImm(8)
	w.WriteOp(OpShl)
	v, ok := EvalConst(w.Bytes())
	if !ok || v != 256 {
		t.Errorf("EvalConst(1<<8) = (%d,%v), want (256,true)", v, ok)
	}
}

func TestExprIterateOpcodes(t *testing.T) {
	var w ExprWriter
	w.WriteSym(0x1234)
	w.WriteImm(7)
	w.WriteOp(OpSub)
	r := NewExprReader(w.Bytes())
	op, _, err := r.Next()
	if err != nil || op != OpPushSym {
		t.Fatalf("first op = %v err=%v, want OpPushSym", op, err)
	}
	op, _, err = r.Next()
	if err != nil || op != OpPushImm8 {
		t.Fatalf("second op = %v, want OpPushImm8", op)
	}
	op, _, err = r.Next()
	if err != nil || op != OpSub {
		t.Fatalf("third op = %v, want OpSub", op)
	}
	if !r.AtEnd() {
		t.Errorf("reader not at end after 3 ops")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Append reader + evaluator to `expr.go`**

```go
import (
	"encoding/binary"
	"fmt"
)

// ExprReader walks a bytecode stream one opcode at a time.
type ExprReader struct {
	buf []byte
	pos int
}

func NewExprReader(buf []byte) *ExprReader {
	return &ExprReader{buf: buf}
}

// Next returns the next opcode plus its inline operand (raw bytes,
// not parsed). For 0-argument opcodes the slice is nil.
func (r *ExprReader) Next() (ExprOp, []byte, error) {
	if r.pos >= len(r.buf) {
		return 0, nil, fmt.Errorf("expr: read past end")
	}
	op := ExprOp(r.buf[r.pos])
	r.pos++
	width := operandWidth(op)
	if width < 0 {
		return 0, nil, fmt.Errorf("expr: unknown opcode 0x%02x", byte(op))
	}
	if r.pos+width > len(r.buf) {
		return 0, nil, fmt.Errorf("expr: truncated operand for %s", op.Name())
	}
	operand := r.buf[r.pos : r.pos+width]
	r.pos += width
	return op, operand, nil
}

// AtEnd reports whether the reader has consumed the whole stream.
func (r *ExprReader) AtEnd() bool { return r.pos >= len(r.buf) }

// operandWidth returns the inline-operand size in bytes for an
// opcode, or -1 for unknown opcodes.
func operandWidth(op ExprOp) int {
	switch op {
	case OpPushImm8:
		return 1
	case OpPushImm16:
		return 2
	case OpPushImm32:
		return 4
	case OpPushImm64:
		return 8
	case OpPushSym:
		return 2
	case OpPushLocal:
		return 2
	case OpPushPC,
		OpAdd, OpSub, OpMul, OpDiv,
		OpAnd, OpOr, OpXor, OpShl, OpShr,
		OpNeg, OpNot,
		OpRelLo12, OpRelHi12,
		OpRelAbsG0, OpRelAbsG0NC,
		OpRelAbsG1, OpRelAbsG1NC,
		OpRelAbsG2, OpRelAbsG2NC,
		OpRelAbsG3:
		return 0
	}
	return -1
}

// EvalConst returns the value of a bytecode stream when every push
// operation is a literal. If any PUSH_SYM / PUSH_LOCAL / PUSH_PC or
// REL_* opcode is encountered, ok=false. Used by text2bin's
// constant-folder.
func EvalConst(buf []byte) (int64, bool) {
	r := NewExprReader(buf)
	stack := make([]int64, 0, 8)
	for !r.AtEnd() {
		op, operand, err := r.Next()
		if err != nil {
			return 0, false
		}
		switch op {
		case OpPushImm8:
			stack = append(stack, int64(int8(operand[0])))
		case OpPushImm16:
			stack = append(stack, int64(int16(binary.LittleEndian.Uint16(operand))))
		case OpPushImm32:
			stack = append(stack, int64(int32(binary.LittleEndian.Uint32(operand))))
		case OpPushImm64:
			stack = append(stack, int64(binary.LittleEndian.Uint64(operand)))
		case OpPushSym, OpPushLocal, OpPushPC,
			OpRelLo12, OpRelHi12,
			OpRelAbsG0, OpRelAbsG0NC,
			OpRelAbsG1, OpRelAbsG1NC,
			OpRelAbsG2, OpRelAbsG2NC,
			OpRelAbsG3:
			return 0, false
		case OpAdd, OpSub, OpMul, OpDiv,
			OpAnd, OpOr, OpXor, OpShl, OpShr:
			if len(stack) < 2 {
				return 0, false
			}
			b := stack[len(stack)-1]
			a := stack[len(stack)-2]
			stack = stack[:len(stack)-2]
			stack = append(stack, applyBinary(op, a, b))
		case OpNeg:
			if len(stack) < 1 {
				return 0, false
			}
			stack[len(stack)-1] = -stack[len(stack)-1]
		case OpNot:
			if len(stack) < 1 {
				return 0, false
			}
			stack[len(stack)-1] = ^stack[len(stack)-1]
		default:
			return 0, false
		}
	}
	if len(stack) != 1 {
		return 0, false
	}
	return stack[0], true
}

func applyBinary(op ExprOp, a, b int64) int64 {
	switch op {
	case OpAdd:
		return a + b
	case OpSub:
		return a - b
	case OpMul:
		return a * b
	case OpDiv:
		if b == 0 {
			return 0
		}
		return a / b
	case OpAnd:
		return a & b
	case OpOr:
		return a | b
	case OpXor:
		return a ^ b
	case OpShl:
		return a << uint64(b)
	case OpShr:
		return a >> uint64(b)
	}
	return 0
}
```

(The existing `package format` and `import "encoding/binary"` lines: keep one `import` block at top of file. If the file already imports `fmt`, do not duplicate.)

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add expression-bytecode reader + constant-only evaluator"
```

---

## Task 10: Operand writer

**Files:**
- Modify: `tools/sam-aarch64-format/operands.go`
- Test: `tools/sam-aarch64-format/operands_writer_test.go`

The writer emits one operand at a time into a `[]byte`, with one helper per operand kind so callers cannot accidentally produce malformed payloads.

- [ ] **Step 1: Write the failing test**

```go
package format

import (
	"bytes"
	"testing"
)

func TestOperandWriteRegX(t *testing.T) {
	var w OperandWriter
	w.WriteReg(OpRegX, 5)
	want := []byte{byte(OpRegX), 5}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}

func TestOperandWriteImmExpr(t *testing.T) {
	var ew ExprWriter
	ew.WriteImm(0x42)
	var ow OperandWriter
	ow.WriteImmExpr(ew.Bytes())
	want := []byte{
		byte(OpImmExpr),
		2, 0,             // expr_len = 2
		byte(OpPushImm8), 0x42,
	}
	if !bytes.Equal(ow.Bytes(), want) {
		t.Errorf("got % X, want % X", ow.Bytes(), want)
	}
}

func TestOperandWriteShiftedReg(t *testing.T) {
	var ew ExprWriter
	ew.WriteImm(4)
	var w OperandWriter
	w.WriteShiftedReg(1, 2, ShiftLSL, ew.Bytes())
	want := []byte{
		byte(OpShiftedReg),
		1, 2, byte(ShiftLSL),
		2, 0,
		byte(OpPushImm8), 4,
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}

func TestOperandWriteMemBaseOff(t *testing.T) {
	var ew ExprWriter
	ew.WriteImm(8)
	var w OperandWriter
	w.WriteMemBaseOff(MemBaseOff, 1, ew.Bytes())
	want := []byte{
		byte(OpMem),
		byte(MemBaseOff),
		1,
		2, 0,
		byte(OpPushImm8), 8,
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}

func TestOperandWriteString(t *testing.T) {
	var w OperandWriter
	w.WriteString([]byte("hi"))
	want := []byte{byte(OpString), 2, 0, 'h', 'i'}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}

func TestOperandWriteCondSysName(t *testing.T) {
	var w OperandWriter
	w.WriteCond(CondNE)
	w.WriteSysName("sctlr_el1")
	want := []byte{
		byte(OpCond), byte(CondNE),
		byte(OpSysName), 9, 0, 's', 'c', 't', 'l', 'r', '_', 'e', 'l', '1',
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Errorf("got % X, want % X", w.Bytes(), want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Append `OperandWriter` to `operands.go`**

```go
import "encoding/binary"

// OperandWriter accumulates encoded operand bytes.
type OperandWriter struct{ buf []byte }

func (w *OperandWriter) Bytes() []byte { return w.buf }
func (w *OperandWriter) Reset()        { w.buf = w.buf[:0] }

// WriteReg writes a register operand; kind must be one of OpRegX,
// OpRegW, OpRegXSP, OpRegWSP.
func (w *OperandWriter) WriteReg(kind OperandKind, reg byte) {
	w.buf = append(w.buf, byte(kind), reg)
}

// WriteImmExpr writes an IMM_EXPR operand carrying the given
// expression bytecode.
func (w *OperandWriter) WriteImmExpr(expr []byte) {
	w.buf = append(w.buf, byte(OpImmExpr))
	w.buf = appendU16(w.buf, uint16(len(expr)))
	w.buf = append(w.buf, expr...)
}

// WriteShiftedReg writes a SHIFTED_REG operand. width: 0=W, 1=X.
func (w *OperandWriter) WriteShiftedReg(width, reg byte, kind ShiftKind, amtExpr []byte) {
	w.buf = append(w.buf, byte(OpShiftedReg), width, reg, byte(kind))
	w.buf = appendU16(w.buf, uint16(len(amtExpr)))
	w.buf = append(w.buf, amtExpr...)
}

// WriteExtendedReg writes an EXTENDED_REG operand. amtExpr may be nil
// or empty (meaning no #N).
func (w *OperandWriter) WriteExtendedReg(width, reg byte, kind ExtendKind, amtExpr []byte) {
	w.buf = append(w.buf, byte(OpExtendedReg), width, reg, byte(kind))
	w.buf = appendU16(w.buf, uint16(len(amtExpr)))
	w.buf = append(w.buf, amtExpr...)
}

// WriteMemBase writes a MEM operand of shape [xn].
func (w *OperandWriter) WriteMemBase(base byte) {
	w.buf = append(w.buf, byte(OpMem), byte(MemBase), base)
}

// WriteMemBaseOff writes a MEM operand of shape [xn, #off], !, or post.
// shape must be MemBaseOff, MemBaseOffPre, or MemBaseOffPost.
func (w *OperandWriter) WriteMemBaseOff(shape MemShape, base byte, offExpr []byte) {
	w.buf = append(w.buf, byte(OpMem), byte(shape), base)
	w.buf = appendU16(w.buf, uint16(len(offExpr)))
	w.buf = append(w.buf, offExpr...)
}

// WriteMemBaseIdx writes a MEM operand of shape [xn, xm]. idxWidth: 0=W, 1=X.
func (w *OperandWriter) WriteMemBaseIdx(base, idx, idxWidth byte) {
	w.buf = append(w.buf, byte(OpMem), byte(MemBaseIdx), base, idx, idxWidth)
}

// WriteMemBaseIdxShifted writes a MEM operand of shape [xn, xm, lsl #N].
func (w *OperandWriter) WriteMemBaseIdxShifted(base, idx, idxWidth, shiftAmt byte) {
	w.buf = append(w.buf, byte(OpMem), byte(MemBaseIdxShifted),
		base, idx, idxWidth, shiftAmt)
}

// WriteMemBaseIdxExtended writes a MEM operand of shape
// [xn, wm/xm, extend #N].
func (w *OperandWriter) WriteMemBaseIdxExtended(base, idx, idxWidth byte, extend ExtendKind, shiftAmt byte) {
	w.buf = append(w.buf, byte(OpMem), byte(MemBaseIdxExtended),
		base, idx, idxWidth, byte(extend), shiftAmt)
}

func (w *OperandWriter) WriteString(s []byte) {
	w.buf = append(w.buf, byte(OpString))
	w.buf = appendU16(w.buf, uint16(len(s)))
	w.buf = append(w.buf, s...)
}

func (w *OperandWriter) WriteCond(c CondCode) {
	w.buf = append(w.buf, byte(OpCond), byte(c))
}

func (w *OperandWriter) WriteSysName(name string) {
	w.buf = append(w.buf, byte(OpSysName))
	w.buf = appendU16(w.buf, uint16(len(name)))
	w.buf = append(w.buf, name...)
}

func appendU16(buf []byte, v uint16) []byte {
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], v)
	return append(buf, tmp[:]...)
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add OperandWriter"
```

---

## Task 11: Operand reader

**Files:**
- Modify: `tools/sam-aarch64-format/operands.go`
- Test: `tools/sam-aarch64-format/operands_reader_test.go`

The reader returns a typed `Operand` struct so bin2text and tests can switch on the kind. Keep the struct flat (no pointer fields) so it is cheap to copy.

- [ ] **Step 1: Write the failing test**

```go
package format

import (
	"bytes"
	"testing"
)

func TestOperandReadRoundtrip(t *testing.T) {
	var ew ExprWriter
	ew.WriteImm(0x42)
	var ow OperandWriter
	ow.WriteReg(OpRegX, 7)
	ow.WriteImmExpr(ew.Bytes())
	ow.WriteCond(CondGT)
	ow.WriteString([]byte("hello"))

	r := NewOperandReader(ow.Bytes())

	o, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != OpRegX || o.Reg != 7 {
		t.Errorf("op0: %+v", o)
	}

	o, err = r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != OpImmExpr || !bytes.Equal(o.Expr, ew.Bytes()) {
		t.Errorf("op1: %+v", o)
	}

	o, err = r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != OpCond || o.Cond != CondGT {
		t.Errorf("op2: %+v", o)
	}

	o, err = r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != OpString || string(o.Str) != "hello" {
		t.Errorf("op3: %+v", o)
	}

	if !r.AtEnd() {
		t.Errorf("reader not at end")
	}
}

func TestOperandReadMemShapes(t *testing.T) {
	var ow OperandWriter
	ow.WriteMemBase(1)
	ow.WriteMemBaseIdx(1, 2, 1)
	ow.WriteMemBaseIdxShifted(1, 2, 1, 3)
	ow.WriteMemBaseIdxExtended(1, 2, 0, ExtUXTW, 0)

	r := NewOperandReader(ow.Bytes())

	o, _ := r.Next()
	if o.Kind != OpMem || o.MemShape != MemBase || o.Base != 1 {
		t.Errorf("MemBase decoded wrong: %+v", o)
	}
	o, _ = r.Next()
	if o.Kind != OpMem || o.MemShape != MemBaseIdx || o.Base != 1 || o.Idx != 2 || o.IdxWidth != 1 {
		t.Errorf("MemBaseIdx decoded wrong: %+v", o)
	}
	o, _ = r.Next()
	if o.MemShape != MemBaseIdxShifted || o.ShiftAmt != 3 {
		t.Errorf("MemBaseIdxShifted decoded wrong: %+v", o)
	}
	o, _ = r.Next()
	if o.MemShape != MemBaseIdxExtended || o.Extend != ExtUXTW {
		t.Errorf("MemBaseIdxExtended decoded wrong: %+v", o)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Append `Operand` + `OperandReader` to `operands.go`**

```go
// Operand is a decoded operand record. Only the fields appropriate
// to Kind are populated; the rest are zero-valued.
type Operand struct {
	Kind OperandKind

	// Register / memory base / index.
	Reg      byte
	Width    byte // for SHIFTED_REG / EXTENDED_REG
	Base     byte
	Idx      byte
	IdxWidth byte

	// Shift / extend.
	ShiftKind ShiftKind
	Extend    ExtendKind
	ShiftAmt  byte // for MemBaseIdxShifted / MemBaseIdxExtended

	// Sub-shape for OpMem.
	MemShape MemShape

	// Expression bytecodes (IMM_EXPR, MEM offset, shift amount expr).
	Expr     []byte
	AmtExpr  []byte

	// String body or system-reg name.
	Str []byte

	// Condition code.
	Cond CondCode
}

// OperandReader walks an operand stream.
type OperandReader struct {
	buf []byte
	pos int
}

func NewOperandReader(buf []byte) *OperandReader {
	return &OperandReader{buf: buf}
}

func (r *OperandReader) AtEnd() bool { return r.pos >= len(r.buf) }

func (r *OperandReader) Next() (Operand, error) {
	if r.AtEnd() {
		return Operand{}, fmt.Errorf("operand: read past end")
	}
	kind := OperandKind(r.buf[r.pos])
	r.pos++
	var o Operand
	o.Kind = kind
	switch kind {
	case OpRegX, OpRegW, OpRegXSP, OpRegWSP:
		o.Reg = r.take(1)[0]
	case OpImmExpr:
		n := r.readLen()
		o.Expr = r.take(n)
	case OpShiftedReg:
		o.Width = r.take(1)[0]
		o.Reg = r.take(1)[0]
		o.ShiftKind = ShiftKind(r.take(1)[0])
		n := r.readLen()
		o.AmtExpr = r.take(n)
	case OpExtendedReg:
		o.Width = r.take(1)[0]
		o.Reg = r.take(1)[0]
		o.Extend = ExtendKind(r.take(1)[0])
		n := r.readLen()
		o.AmtExpr = r.take(n)
	case OpMem:
		o.MemShape = MemShape(r.take(1)[0])
		switch o.MemShape {
		case MemBase:
			o.Base = r.take(1)[0]
		case MemBaseOff, MemBaseOffPre, MemBaseOffPost:
			o.Base = r.take(1)[0]
			n := r.readLen()
			o.Expr = r.take(n)
		case MemBaseIdx:
			o.Base = r.take(1)[0]
			o.Idx = r.take(1)[0]
			o.IdxWidth = r.take(1)[0]
		case MemBaseIdxShifted:
			o.Base = r.take(1)[0]
			o.Idx = r.take(1)[0]
			o.IdxWidth = r.take(1)[0]
			o.ShiftAmt = r.take(1)[0]
		case MemBaseIdxExtended:
			o.Base = r.take(1)[0]
			o.Idx = r.take(1)[0]
			o.IdxWidth = r.take(1)[0]
			o.Extend = ExtendKind(r.take(1)[0])
			o.ShiftAmt = r.take(1)[0]
		default:
			return o, fmt.Errorf("operand: unknown MemShape %d", o.MemShape)
		}
	case OpString, OpSysName:
		n := r.readLen()
		o.Str = r.take(n)
	case OpCond:
		o.Cond = CondCode(r.take(1)[0])
	default:
		return o, fmt.Errorf("operand: unknown kind 0x%02x", byte(kind))
	}
	return o, nil
}

func (r *OperandReader) take(n int) []byte {
	if r.pos+n > len(r.buf) {
		// Caller is responsible for surfacing truncation; we return
		// a slice that is short-enough to provoke a downstream error.
		s := r.buf[r.pos:]
		r.pos = len(r.buf)
		return s
	}
	s := r.buf[r.pos : r.pos+n]
	r.pos += n
	return s
}

func (r *OperandReader) readLen() int {
	if r.pos+2 > len(r.buf) {
		return 0
	}
	n := int(binary.LittleEndian.Uint16(r.buf[r.pos:]))
	r.pos += 2
	return n
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add OperandReader"
```

---

## Task 12: Record writer

**Files:**
- Create: `tools/sam-aarch64-format/writer.go`
- Test: `tools/sam-aarch64-format/writer_test.go`

The record writer emits one record at a time. Each record is `[kind][len u16 LE][payload]`. The writer owns the length-prefix bookkeeping so callers cannot get it wrong.

- [ ] **Step 1: Write the failing test**

```go
package format

import (
	"bytes"
	"testing"
)

func TestRecordWriterLabelDef(t *testing.T) {
	var rw RecordWriter
	rw.WriteLabelDef(42)
	want := []byte{
		byte(KindLabelDef),
		2, 0, // len = 2
		42, 0,
	}
	if !bytes.Equal(rw.Bytes(), want) {
		t.Errorf("got % X, want % X", rw.Bytes(), want)
	}
}

func TestRecordWriterLocalDef(t *testing.T) {
	var rw RecordWriter
	rw.WriteLocalDef(3)
	want := []byte{byte(KindLocalDef), 1, 0, 3}
	if !bytes.Equal(rw.Bytes(), want) {
		t.Errorf("got % X, want % X", rw.Bytes(), want)
	}
}

func TestRecordWriterComment(t *testing.T) {
	var rw RecordWriter
	rw.WriteComment(1, []byte("hi"))
	want := []byte{byte(KindComment), 3, 0, 1, 'h', 'i'}
	if !bytes.Equal(rw.Bytes(), want) {
		t.Errorf("got % X, want % X", rw.Bytes(), want)
	}
}

func TestRecordWriterInst(t *testing.T) {
	var ow OperandWriter
	ow.WriteReg(OpRegX, 0)
	ow.WriteReg(OpRegX, 1)
	var ew ExprWriter
	ew.WriteImm(4)
	ow.WriteImmExpr(ew.Bytes())

	var rw RecordWriter
	id, _ := MnemonicID("add")
	rw.WriteInst(id, 3, ow.Bytes())

	// Header: kind(1) + len(2) ; payload: mnemonic(2) + opcount(1) + operands.
	got := rw.Bytes()
	if got[0] != byte(KindInst) {
		t.Errorf("kind = 0x%02x, want 0x%02x", got[0], byte(KindInst))
	}
	wantLen := uint16(3 + len(ow.Bytes()))
	gotLen := uint16(got[1]) | uint16(got[2])<<8
	if gotLen != wantLen {
		t.Errorf("len = %d, want %d", gotLen, wantLen)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Implement `writer.go`**

```go
package format

import "encoding/binary"

// RecordWriter accumulates records into a byte slice in stream order.
type RecordWriter struct{ buf []byte }

func (w *RecordWriter) Bytes() []byte { return w.buf }
func (w *RecordWriter) Reset()        { w.buf = w.buf[:0] }

func (w *RecordWriter) writeHeader(kind RecordKind, payloadLen int) {
	w.buf = append(w.buf, byte(kind))
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], uint16(payloadLen))
	w.buf = append(w.buf, tmp[:]...)
}

func (w *RecordWriter) WriteLabelDef(symID uint16) {
	w.writeHeader(KindLabelDef, 2)
	w.buf = append(w.buf, byte(symID), byte(symID>>8))
}

func (w *RecordWriter) WriteLocalDef(digit byte) {
	w.writeHeader(KindLocalDef, 1)
	w.buf = append(w.buf, digit)
}

// WriteComment writes a comment record. placement: 0=standalone, 1=trailing.
func (w *RecordWriter) WriteComment(placement byte, body []byte) {
	w.writeHeader(KindComment, 1+len(body))
	w.buf = append(w.buf, placement)
	w.buf = append(w.buf, body...)
}

// WriteInst writes an INST record. operands is the already-encoded
// operand stream produced by OperandWriter.
func (w *RecordWriter) WriteInst(mnemonicID uint16, operandCount byte, operands []byte) {
	payloadLen := 2 + 1 + len(operands)
	w.writeHeader(KindInst, payloadLen)
	w.buf = append(w.buf, byte(mnemonicID), byte(mnemonicID>>8), operandCount)
	w.buf = append(w.buf, operands...)
}

// WriteDirective writes a DIRECTIVE record. operands is the
// already-encoded operand stream.
func (w *RecordWriter) WriteDirective(directiveID, operandCount byte, operands []byte) {
	payloadLen := 1 + 1 + len(operands)
	w.writeHeader(KindDirective, payloadLen)
	w.buf = append(w.buf, directiveID, operandCount)
	w.buf = append(w.buf, operands...)
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add RecordWriter"
```

---

## Task 13: Record reader

**Files:**
- Create: `tools/sam-aarch64-format/reader.go`
- Test: `tools/sam-aarch64-format/reader_test.go`

The reader returns a typed `Record` struct and exposes a streaming iterator. Unknown record kinds are surfaced (not silently skipped) so bin2text can decide how to handle them.

- [ ] **Step 1: Write the failing test**

```go
package format

import "testing"

func TestRecordReaderRoundtrip(t *testing.T) {
	var rw RecordWriter
	rw.WriteLabelDef(7)
	rw.WriteLocalDef(2)
	rw.WriteComment(0, []byte("howdy"))
	rw.WriteInst(0, 0, nil) // nop

	r := NewRecordReader(rw.Bytes())

	rec, err := r.Next()
	if err != nil || rec.Kind != KindLabelDef || rec.SymbolID != 7 {
		t.Fatalf("rec0: %+v err=%v", rec, err)
	}
	rec, err = r.Next()
	if err != nil || rec.Kind != KindLocalDef || rec.Digit != 2 {
		t.Fatalf("rec1: %+v err=%v", rec, err)
	}
	rec, err = r.Next()
	if err != nil || rec.Kind != KindComment || rec.Placement != 0 || string(rec.Body) != "howdy" {
		t.Fatalf("rec2: %+v err=%v", rec, err)
	}
	rec, err = r.Next()
	if err != nil || rec.Kind != KindInst || rec.MnemonicID != 0 || rec.OperandCount != 0 {
		t.Fatalf("rec3: %+v err=%v", rec, err)
	}
	if !r.AtEnd() {
		t.Errorf("reader not at end")
	}
}

func TestRecordReaderUnknownKindSurfaced(t *testing.T) {
	buf := []byte{0xFE, 3, 0, 'a', 'b', 'c'}
	r := NewRecordReader(buf)
	rec, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Kind != 0xFE {
		t.Errorf("kind = 0x%02x, want 0xFE", byte(rec.Kind))
	}
	if string(rec.Raw) != "abc" {
		t.Errorf("raw payload = %q, want %q", string(rec.Raw), "abc")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Implement `reader.go`**

```go
package format

import (
	"encoding/binary"
	"fmt"
)

// Record is a decoded statement-stream record. Only the fields
// appropriate to Kind are populated.
type Record struct {
	Kind RecordKind

	SymbolID     uint16 // KindLabelDef
	Digit        byte   // KindLocalDef
	Placement    byte   // KindComment
	Body         []byte // KindComment
	MnemonicID   uint16 // KindInst
	DirectiveID  byte   // KindDirective
	OperandCount byte   // KindInst, KindDirective
	Operands     []byte // KindInst, KindDirective (operand stream)

	// Raw payload for unknown kinds and forward-compat.
	Raw []byte
}

type RecordReader struct {
	buf []byte
	pos int
}

func NewRecordReader(buf []byte) *RecordReader {
	return &RecordReader{buf: buf}
}

func (r *RecordReader) AtEnd() bool { return r.pos >= len(r.buf) }

func (r *RecordReader) Next() (Record, error) {
	if r.AtEnd() {
		return Record{}, fmt.Errorf("record: read past end")
	}
	if r.pos+3 > len(r.buf) {
		return Record{}, fmt.Errorf("record: truncated header at offset %d", r.pos)
	}
	kind := RecordKind(r.buf[r.pos])
	length := int(binary.LittleEndian.Uint16(r.buf[r.pos+1:]))
	r.pos += 3
	if r.pos+length > len(r.buf) {
		return Record{}, fmt.Errorf("record: truncated payload at offset %d (need %d, have %d)",
			r.pos, length, len(r.buf)-r.pos)
	}
	payload := r.buf[r.pos : r.pos+length]
	r.pos += length

	rec := Record{Kind: kind, Raw: payload}
	switch kind {
	case KindLabelDef:
		if len(payload) != 2 {
			return rec, fmt.Errorf("LABEL_DEF: payload len = %d, want 2", len(payload))
		}
		rec.SymbolID = binary.LittleEndian.Uint16(payload)
	case KindLocalDef:
		if len(payload) != 1 {
			return rec, fmt.Errorf("LOCAL_DEF: payload len = %d, want 1", len(payload))
		}
		rec.Digit = payload[0]
	case KindComment:
		if len(payload) < 1 {
			return rec, fmt.Errorf("COMMENT: payload too short")
		}
		rec.Placement = payload[0]
		rec.Body = payload[1:]
	case KindInst:
		if len(payload) < 3 {
			return rec, fmt.Errorf("INST: payload too short")
		}
		rec.MnemonicID = binary.LittleEndian.Uint16(payload)
		rec.OperandCount = payload[2]
		rec.Operands = payload[3:]
	case KindDirective:
		if len(payload) < 2 {
			return rec, fmt.Errorf("DIRECTIVE: payload too short")
		}
		rec.DirectiveID = payload[0]
		rec.OperandCount = payload[1]
		rec.Operands = payload[2:]
	}
	return rec, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add RecordReader with raw-payload surfacing for unknown kinds"
```

---

## Task 14: File writer (header + name table + records)

**Files:**
- Modify: `tools/sam-aarch64-format/writer.go`
- Test: `tools/sam-aarch64-format/file_writer_test.go`

- [ ] **Step 1: Write the failing test**

```go
package format

import (
	"bytes"
	"testing"
)

func TestWriteFileMinimal(t *testing.T) {
	st := NewSymbolTable()
	st.Intern("loop")
	st.Intern("exit")

	var rw RecordWriter
	rw.WriteLabelDef(0)

	var buf bytes.Buffer
	if err := WriteFile(&buf, st, rw.Bytes()); err != nil {
		t.Fatal(err)
	}

	got := buf.Bytes()
	// Magic + version + flags = 8 bytes.
	if string(got[:4]) != "SA64" {
		t.Errorf("magic = %q", string(got[:4]))
	}
	if got[4] != 1 || got[5] != 0 {
		t.Errorf("version = %d %d, want 1 0", got[4], got[5])
	}
	// Name-table count is at offset 8.
	if got[8] != 2 || got[9] != 0 {
		t.Errorf("name count = %d %d, want 2 0", got[8], got[9])
	}
}

func TestWriteFileEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFile(&buf, NewSymbolTable(), nil); err != nil {
		t.Fatal(err)
	}
	if buf.Len() < 10 {
		t.Errorf("min file should be 10 bytes (magic+ver+flags+count), got %d", buf.Len())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Append `WriteFile` to `writer.go`**

```go
import (
	"io"
)

// WriteFile serialises a complete .tbn file to w: magic, version,
// flags, the symbol table's name list, and the record stream.
func WriteFile(w io.Writer, st *SymbolTable, records []byte) error {
	if _, err := w.Write(Magic[:]); err != nil {
		return err
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint16(hdr[0:2], Version)
	binary.LittleEndian.PutUint16(hdr[2:4], Flags)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}

	names := st.Names()
	var cnt [2]byte
	binary.LittleEndian.PutUint16(cnt[:], uint16(len(names)))
	if _, err := w.Write(cnt[:]); err != nil {
		return err
	}
	for _, n := range names {
		var ln [2]byte
		binary.LittleEndian.PutUint16(ln[:], uint16(len(n)))
		if _, err := w.Write(ln[:]); err != nil {
			return err
		}
		if _, err := w.Write([]byte(n)); err != nil {
			return err
		}
	}

	if _, err := w.Write(records); err != nil {
		return err
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add WriteFile (header + name table + records)"
```

---

## Task 15: File reader

**Files:**
- Modify: `tools/sam-aarch64-format/reader.go`
- Test: `tools/sam-aarch64-format/file_reader_test.go`

- [ ] **Step 1: Write the failing test**

```go
package format

import (
	"bytes"
	"testing"
)

func TestReadFileRoundtrip(t *testing.T) {
	st := NewSymbolTable()
	st.Intern("loop")
	st.Intern("exit")

	var rw RecordWriter
	rw.WriteLabelDef(0)
	rw.WriteLabelDef(1)

	var buf bytes.Buffer
	if err := WriteFile(&buf, st, rw.Bytes()); err != nil {
		t.Fatal(err)
	}

	f, err := ReadFile(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 {
		t.Errorf("version = %d", f.Version)
	}
	if len(f.Names) != 2 || f.Names[0] != "loop" || f.Names[1] != "exit" {
		t.Errorf("names = %v", f.Names)
	}
	if !bytes.Equal(f.Records, rw.Bytes()) {
		t.Errorf("records mismatch:\n got: % X\nwant: % X", f.Records, rw.Bytes())
	}
}

func TestReadFileWrongMagic(t *testing.T) {
	buf := []byte{'B', 'A', 'D', '!', 1, 0, 0, 0, 0, 0}
	if _, err := ReadFile(buf); err == nil {
		t.Errorf("expected error on bad magic")
	}
}

func TestReadFileWrongVersion(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(Magic[:])
	buf.Write([]byte{99, 0, 0, 0, 0, 0}) // version=99
	if _, err := ReadFile(buf.Bytes()); err == nil {
		t.Errorf("expected error on unknown version")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Append `ReadFile` + `File` to `reader.go`**

```go
// File is a decoded .tbn file.
type File struct {
	Version uint16
	Flags   uint16
	Names   []string
	Records []byte
}

func ReadFile(buf []byte) (*File, error) {
	if len(buf) < 8 {
		return nil, fmt.Errorf("file: too short for header")
	}
	if string(buf[0:4]) != "SA64" {
		return nil, fmt.Errorf("file: bad magic %q", string(buf[0:4]))
	}
	version := binary.LittleEndian.Uint16(buf[4:6])
	if version != Version {
		return nil, fmt.Errorf("file: unsupported version %d (want %d)", version, Version)
	}
	flags := binary.LittleEndian.Uint16(buf[6:8])
	pos := 8

	if pos+2 > len(buf) {
		return nil, fmt.Errorf("file: truncated name table count")
	}
	count := int(binary.LittleEndian.Uint16(buf[pos:]))
	pos += 2

	names := make([]string, count)
	for i := 0; i < count; i++ {
		if pos+2 > len(buf) {
			return nil, fmt.Errorf("file: truncated name length at %d", i)
		}
		n := int(binary.LittleEndian.Uint16(buf[pos:]))
		pos += 2
		if pos+n > len(buf) {
			return nil, fmt.Errorf("file: truncated name body at %d", i)
		}
		names[i] = string(buf[pos : pos+n])
		pos += n
	}

	return &File{
		Version: version,
		Flags:   flags,
		Names:   names,
		Records: buf[pos:],
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format
g commit -m "m1: add ReadFile + File type"
```

---

## Task 16: text2bin scaffold + CLI

**Files:**
- Create: `tools/text2bin/go.mod`
- Create: `tools/text2bin/main.go`
- Create: `tools/text2bin/main_test.go`

The CLI accepts `text2bin INPUT.s [-o OUTPUT.tbn]`. For now its parser does nothing and the output is a valid empty file (magic + version + zero name count + no records). Subsequent tasks add real parsing.

- [ ] **Step 1: Write the failing test**

`tools/text2bin/main_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestEmptyFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty.s")
	if err := os.WriteFile(src, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := Translate([]byte(""), "empty.s")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.Write(out)
	f, err := format.ReadFile(buf.Bytes())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if f.Version != 1 || len(f.Names) != 0 || len(f.Records) != 0 {
		t.Errorf("unexpected file shape: %+v", f)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
mkdir -p tools/text2bin
cd tools/text2bin
go mod init github.com/petemoore/sam-aarch64/tools/text2bin
```

Append to `go.mod`:

```
require github.com/petemoore/sam-aarch64/tools/sam-aarch64-format v0.0.0-00010101000000-000000000000

replace github.com/petemoore/sam-aarch64/tools/sam-aarch64-format => ../sam-aarch64-format
```

Then:

```bash
go test ./...
```

Expected: FAIL — `Translate` undefined.

- [ ] **Step 3: Implement minimal `main.go`**

```go
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Translate is the library entry point — accepts source bytes and a
// path (for error messages) and returns the encoded .tbn bytes.
func Translate(src []byte, path string) ([]byte, error) {
	st := format.NewSymbolTable()
	var rw format.RecordWriter
	// TODO: real parser arrives in Task 18.

	var out bytes.Buffer
	if err := format.WriteFile(&out, st, rw.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func main() {
	var outFlag string
	flag.StringVar(&outFlag, "o", "", "output file (defaults to INPUT.tbn)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: text2bin INPUT.s [-o OUTPUT.tbn]\n")
		os.Exit(2)
	}
	in := flag.Arg(0)
	src, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out, err := Translate(src, in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if outFlag == "" {
		outFlag = in + ".tbn"
	}
	if err := os.WriteFile(outFlag, out, 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/text2bin
g commit -m "m1: scaffold text2bin module with empty-file Translate"
```

---

## Task 17: text2bin lexer

**Files:**
- Create: `tools/text2bin/lexer.go`
- Create: `tools/text2bin/errors.go`
- Test: `tools/text2bin/lexer_test.go`

The lexer produces a flat slice of typed tokens. Each token carries line/col so the parser can attach errors to source positions.

- [ ] **Step 1: Write the failing test**

```go
package main

import "testing"

func TestLexBasic(t *testing.T) {
	toks, err := Lex([]byte("add x0, x1, #4\n"), "f.s")
	if err != nil {
		t.Fatal(err)
	}
	want := []TokKind{
		TokIdent,  // add
		TokIdent,  // x0
		TokComma,
		TokIdent,  // x1
		TokComma,
		TokHash,
		TokInt,    // 4
		TokEOL,
		TokEOF,
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d toks, want %d: %+v", len(toks), len(want), toks)
	}
	for i, w := range want {
		if toks[i].Kind != w {
			t.Errorf("tok[%d] = %v, want %v", i, toks[i].Kind, w)
		}
	}
}

func TestLexComments(t *testing.T) {
	toks, _ := Lex([]byte("// hi\nadd /* mid */ x0\n"), "f.s")
	// Expect: TokLineComment, TokEOL, TokIdent, TokBlockComment, TokIdent, TokEOL, TokEOF.
	want := []TokKind{TokLineComment, TokEOL, TokIdent, TokBlockComment, TokIdent, TokEOL, TokEOF}
	for i, w := range want {
		if toks[i].Kind != w {
			t.Errorf("tok[%d] = %v, want %v", i, toks[i].Kind, w)
		}
	}
}

func TestLexNumberBases(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"42", 42},
		{"0x2a", 42},
		{"0b101010", 42},
		{"'A'", 65},
	}
	for _, c := range cases {
		toks, err := Lex([]byte(c.in+"\n"), "f.s")
		if err != nil {
			t.Fatalf("lex %q: %v", c.in, err)
		}
		if toks[0].Kind != TokInt || toks[0].Int != c.want {
			t.Errorf("lex %q: got %+v, want int %d", c.in, toks[0], c.want)
		}
	}
}

func TestLexStringLit(t *testing.T) {
	toks, err := Lex([]byte(`.ascii "hi\n"`+"\n"), "f.s")
	if err != nil {
		t.Fatal(err)
	}
	if toks[0].Kind != TokIdent || toks[0].Text != ".ascii" {
		t.Errorf("tok[0] = %+v", toks[0])
	}
	if toks[1].Kind != TokString || string(toks[1].Bytes) != "hi\n" {
		t.Errorf("tok[1] = %+v", toks[1])
	}
}

func TestLexLocalLabelRef(t *testing.T) {
	toks, _ := Lex([]byte("b 1f\n"), "f.s")
	// b, 1f (lexed as TokLocalRef with digit=1, dir='f'), EOL, EOF.
	if toks[1].Kind != TokLocalRef || toks[1].Digit != 1 || toks[1].LocalDir != 'f' {
		t.Errorf("tok[1] = %+v, want LocalRef 1f", toks[1])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — `Lex`, `Tok*` types undefined.

- [ ] **Step 3: Implement `errors.go`**

```go
package main

import "fmt"

// Position is a source location used in error messages.
type Position struct {
	File string
	Line int
	Col  int
}

func (p Position) String() string {
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
}

// LexError is fail-fast lex/parse errors with location.
type LexError struct {
	Pos Position
	Msg string
}

func (e *LexError) Error() string {
	return fmt.Sprintf("%s: %s", e.Pos.String(), e.Msg)
}

func newErr(p Position, format string, args ...any) *LexError {
	return &LexError{Pos: p, Msg: fmt.Sprintf(format, args...)}
}
```

- [ ] **Step 4: Implement `lexer.go`**

```go
package main

import (
	"unicode"
)

type TokKind byte

const (
	TokEOF TokKind = iota
	TokEOL
	TokIdent
	TokInt
	TokString
	TokComma
	TokHash
	TokColon
	TokBang
	TokDot
	TokLBracket
	TokRBracket
	TokLParen
	TokRParen
	TokPlus
	TokMinus
	TokStar
	TokSlash
	TokAmp
	TokPipe
	TokCaret
	TokTilde
	TokShl
	TokShr
	TokLineComment
	TokBlockComment
	TokLocalRef // e.g. 1f, 2b
)

type Tok struct {
	Kind     TokKind
	Pos      Position
	Text     string  // verbatim source span (ident, number text)
	Int      int64   // value for TokInt
	Bytes    []byte  // decoded body for TokString and TokLineComment / TokBlockComment
	Digit    byte    // digit for TokLocalRef
	LocalDir byte    // 'f' or 'b' for TokLocalRef
}

// Lex tokenises src; "path" is used for error positions.
func Lex(src []byte, path string) ([]Tok, error) {
	l := &lexer{src: src, path: path, line: 1, col: 1}
	var toks []Tok
	for {
		t, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.Kind == TokEOF {
			return toks, nil
		}
	}
}

type lexer struct {
	src       []byte
	path      string
	pos       int
	line, col int
}

func (l *lexer) pos2() Position {
	return Position{File: l.path, Line: l.line, Col: l.col}
}

func (l *lexer) peek() byte {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

func (l *lexer) advance() byte {
	c := l.src[l.pos]
	l.pos++
	if c == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return c
}

func (l *lexer) next() (Tok, error) {
	// Skip blanks (but not newlines — newline is TokEOL).
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == ' ' || c == '\t' || c == '\r' {
			l.advance()
			continue
		}
		break
	}
	if l.pos >= len(l.src) {
		return Tok{Kind: TokEOF, Pos: l.pos2()}, nil
	}
	start := l.pos2()
	c := l.peek()
	switch {
	case c == '\n':
		l.advance()
		return Tok{Kind: TokEOL, Pos: start}, nil
	case c == ',':
		l.advance()
		return Tok{Kind: TokComma, Pos: start}, nil
	case c == '#':
		l.advance()
		return Tok{Kind: TokHash, Pos: start}, nil
	case c == ':':
		l.advance()
		return Tok{Kind: TokColon, Pos: start}, nil
	case c == '!':
		l.advance()
		return Tok{Kind: TokBang, Pos: start}, nil
	case c == '.':
		// '.' could be a directive prefix (e.g. .text) or a standalone
		// PC operator. Treat as ident-start when followed by letter,
		// otherwise return TokDot.
		if l.pos+1 < len(l.src) && isIdentStart(l.src[l.pos+1]) {
			return l.readIdent()
		}
		l.advance()
		return Tok{Kind: TokDot, Pos: start}, nil
	case c == '[':
		l.advance()
		return Tok{Kind: TokLBracket, Pos: start}, nil
	case c == ']':
		l.advance()
		return Tok{Kind: TokRBracket, Pos: start}, nil
	case c == '(':
		l.advance()
		return Tok{Kind: TokLParen, Pos: start}, nil
	case c == ')':
		l.advance()
		return Tok{Kind: TokRParen, Pos: start}, nil
	case c == '+':
		l.advance()
		return Tok{Kind: TokPlus, Pos: start}, nil
	case c == '-':
		l.advance()
		return Tok{Kind: TokMinus, Pos: start}, nil
	case c == '*':
		l.advance()
		return Tok{Kind: TokStar, Pos: start}, nil
	case c == '/':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '/' {
			return l.readLineComment(start)
		}
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '*' {
			return l.readBlockComment(start)
		}
		l.advance()
		return Tok{Kind: TokSlash, Pos: start}, nil
	case c == '&':
		l.advance()
		return Tok{Kind: TokAmp, Pos: start}, nil
	case c == '|':
		l.advance()
		return Tok{Kind: TokPipe, Pos: start}, nil
	case c == '^':
		l.advance()
		return Tok{Kind: TokCaret, Pos: start}, nil
	case c == '~':
		l.advance()
		return Tok{Kind: TokTilde, Pos: start}, nil
	case c == '<':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '<' {
			l.advance()
			l.advance()
			return Tok{Kind: TokShl, Pos: start}, nil
		}
		return Tok{}, newErr(start, "unexpected '<' (did you mean '<<'?)")
	case c == '>':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '>' {
			l.advance()
			l.advance()
			return Tok{Kind: TokShr, Pos: start}, nil
		}
		return Tok{}, newErr(start, "unexpected '>' (did you mean '>>'?)")
	case c == '"':
		return l.readString(start)
	case c == '\'':
		return l.readCharLit(start)
	case unicode.IsDigit(rune(c)):
		return l.readNumberOrLocal(start)
	case isIdentStart(c):
		return l.readIdent()
	}
	return Tok{}, newErr(start, "unexpected character %q", c)
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '.'
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func (l *lexer) readIdent() (Tok, error) {
	start := l.pos2()
	startPos := l.pos
	for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) {
		l.advance()
	}
	return Tok{Kind: TokIdent, Pos: start, Text: string(l.src[startPos:l.pos])}, nil
}

func (l *lexer) readNumberOrLocal(start Position) (Tok, error) {
	// "1f"/"1b" only matches a single digit followed by 'f' or 'b' and a
	// word break — anything else parses as a number literal.
	if l.pos+1 < len(l.src) &&
		(l.src[l.pos+1] == 'f' || l.src[l.pos+1] == 'b') &&
		(l.pos+2 >= len(l.src) || !isIdentCont(l.src[l.pos+2])) {
		d := l.advance() - '0'
		dir := l.advance()
		return Tok{Kind: TokLocalRef, Pos: start, Digit: d, LocalDir: dir}, nil
	}
	return l.readNumber(start)
}

func (l *lexer) readNumber(start Position) (Tok, error) {
	startPos := l.pos
	base := 10
	c := l.peek()
	if c == '0' && l.pos+1 < len(l.src) {
		switch l.src[l.pos+1] {
		case 'x', 'X':
			base = 16
			l.advance()
			l.advance()
			startPos = l.pos
		case 'b', 'B':
			base = 2
			l.advance()
			l.advance()
			startPos = l.pos
		}
	}
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		ok := false
		switch base {
		case 10:
			ok = c >= '0' && c <= '9'
		case 16:
			ok = (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		case 2:
			ok = c == '0' || c == '1'
		}
		if !ok {
			break
		}
		l.advance()
	}
	text := string(l.src[startPos:l.pos])
	v, err := parseIntInBase(text, base)
	if err != nil {
		return Tok{}, newErr(start, "bad integer literal: %s", err)
	}
	return Tok{Kind: TokInt, Pos: start, Int: v, Text: text}, nil
}

func (l *lexer) readCharLit(start Position) (Tok, error) {
	l.advance() // '
	if l.pos >= len(l.src) {
		return Tok{}, newErr(start, "unterminated char literal")
	}
	c := l.advance()
	if c == '\\' {
		if l.pos >= len(l.src) {
			return Tok{}, newErr(start, "unterminated char escape")
		}
		esc := l.advance()
		switch esc {
		case 'n':
			c = '\n'
		case 't':
			c = '\t'
		case '\\':
			c = '\\'
		case '\'':
			c = '\''
		case '"':
			c = '"'
		case '0':
			c = 0
		default:
			return Tok{}, newErr(start, "unknown char escape '\\%c'", esc)
		}
	}
	if l.pos >= len(l.src) || l.advance() != '\'' {
		return Tok{}, newErr(start, "unterminated char literal")
	}
	return Tok{Kind: TokInt, Pos: start, Int: int64(c)}, nil
}

func (l *lexer) readString(start Position) (Tok, error) {
	l.advance() // "
	var body []byte
	for {
		if l.pos >= len(l.src) {
			return Tok{}, newErr(start, "unterminated string literal")
		}
		c := l.advance()
		if c == '"' {
			break
		}
		if c == '\\' {
			if l.pos >= len(l.src) {
				return Tok{}, newErr(start, "unterminated string escape")
			}
			esc := l.advance()
			switch esc {
			case 'n':
				body = append(body, '\n')
			case 't':
				body = append(body, '\t')
			case '\\':
				body = append(body, '\\')
			case '"':
				body = append(body, '"')
			case '\'':
				body = append(body, '\'')
			case '0':
				body = append(body, 0)
			case 'x':
				if l.pos+1 >= len(l.src) {
					return Tok{}, newErr(start, "truncated \\xNN escape")
				}
				hi := hexNibble(l.advance())
				lo := hexNibble(l.advance())
				if hi < 0 || lo < 0 {
					return Tok{}, newErr(start, "bad \\xNN escape")
				}
				body = append(body, byte(hi*16+lo))
			default:
				return Tok{}, newErr(start, "unknown string escape '\\%c'", esc)
			}
			continue
		}
		body = append(body, c)
	}
	return Tok{Kind: TokString, Pos: start, Bytes: body}, nil
}

func (l *lexer) readLineComment(start Position) (Tok, error) {
	l.advance()
	l.advance()
	startBody := l.pos
	for l.pos < len(l.src) && l.src[l.pos] != '\n' {
		l.advance()
	}
	return Tok{Kind: TokLineComment, Pos: start, Bytes: l.src[startBody:l.pos]}, nil
}

func (l *lexer) readBlockComment(start Position) (Tok, error) {
	l.advance()
	l.advance()
	startBody := l.pos
	for {
		if l.pos+1 >= len(l.src) {
			return Tok{}, newErr(start, "unterminated block comment")
		}
		if l.src[l.pos] == '*' && l.src[l.pos+1] == '/' {
			body := l.src[startBody:l.pos]
			l.advance()
			l.advance()
			return Tok{Kind: TokBlockComment, Pos: start, Bytes: body}, nil
		}
		l.advance()
	}
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func parseIntInBase(s string, base int) (int64, error) {
	var v int64
	for _, c := range []byte(s) {
		d := -1
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		}
		if d < 0 || d >= base {
			return 0, fmt.Errorf("invalid digit %q", c)
		}
		v = v*int64(base) + int64(d)
	}
	return v, nil
}
```

(Add `import "fmt"` to `lexer.go` if not already present.)

- [ ] **Step 5: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
g add tools/text2bin
g commit -m "m1: text2bin lexer"
```

---

## Task 18: text2bin parser — labels, local labels, blank lines, comments

**Files:**
- Create: `tools/text2bin/parser.go`
- Modify: `tools/text2bin/main.go`
- Test: `tools/text2bin/parser_test.go`

The parser is line-driven. It walks the token stream until it sees an EOL, classifies the line, and emits zero or more records. This task handles only label-def, local-label-def, comment, and blank lines — instructions and directives land in Task 19+.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"bytes"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func parseHelper(t *testing.T, src string) *format.File {
	t.Helper()
	out, err := Translate([]byte(src), "test.s")
	if err != nil {
		t.Fatal(err)
	}
	f, err := format.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseLabelDef(t *testing.T) {
	f := parseHelper(t, "loop:\n")
	if len(f.Names) != 1 || f.Names[0] != "loop" {
		t.Errorf("names = %v", f.Names)
	}
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindLabelDef || rec.SymbolID != 0 {
		t.Errorf("rec = %+v", rec)
	}
}

func TestParseLocalLabelDef(t *testing.T) {
	f := parseHelper(t, "3:\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindLocalDef || rec.Digit != 3 {
		t.Errorf("rec = %+v", rec)
	}
}

func TestParseStandaloneComment(t *testing.T) {
	f := parseHelper(t, "// banner\n/* block */\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindComment || rec.Placement != 0 || string(rec.Body) != " banner" {
		t.Errorf("rec0 = %+v", rec)
	}
	rec, _ = r.Next()
	if rec.Kind != format.KindComment || rec.Placement != 0 || string(rec.Body) != " block " {
		t.Errorf("rec1 = %+v", rec)
	}
}

func TestParseBlankLinesSkipped(t *testing.T) {
	f := parseHelper(t, "\n\n\n")
	if len(f.Records) != 0 {
		t.Errorf("expected no records, got % X", f.Records)
	}
}

func TestParseLabelOnSameLineAsAnotherToken(t *testing.T) {
	// A label-def on a line by itself emits one LABEL_DEF; trailing
	// junk on the same line would fail because Task 18 cannot yet
	// parse instructions. Use comment-after-label to keep this test
	// independent.
	f := parseHelper(t, "exit: // bye\n")
	rr := format.NewRecordReader(f.Records)
	rec, _ := rr.Next()
	if rec.Kind != format.KindLabelDef {
		t.Errorf("rec0 = %+v", rec)
	}
	rec, _ = rr.Next()
	if rec.Kind != format.KindComment || rec.Placement != 1 || string(rec.Body) != " bye" {
		t.Errorf("rec1 = %+v", rec)
	}
}

func TestParseUnknownTokenFailsWithLocation(t *testing.T) {
	_, err := Translate([]byte("?\n"), "f.s")
	if err == nil {
		t.Fatal("expected error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("f.s:1:1")) {
		t.Errorf("error lacks position: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Implement `parser.go`**

```go
package main

import (
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

type parser struct {
	toks    []Tok
	pos     int
	st      *format.SymbolTable
	rw      format.RecordWriter
}

// Parse turns a token stream into a record stream and the populated
// symbol table.
func Parse(toks []Tok) ([]byte, *format.SymbolTable, error) {
	p := &parser{toks: toks, st: format.NewSymbolTable()}
	for !p.atEOF() {
		if err := p.parseLine(); err != nil {
			return nil, nil, err
		}
	}
	return p.rw.Bytes(), p.st, nil
}

func (p *parser) atEOF() bool { return p.toks[p.pos].Kind == TokEOF }
func (p *parser) cur() Tok    { return p.toks[p.pos] }

func (p *parser) parseLine() error {
	// Skip blank lines.
	for p.cur().Kind == TokEOL {
		p.pos++
	}
	if p.atEOF() {
		return nil
	}

	emittedStatement := false

	for {
		t := p.cur()
		switch t.Kind {
		case TokEOL:
			p.pos++
			return nil
		case TokEOF:
			return nil
		case TokLineComment, TokBlockComment:
			placement := byte(0)
			if emittedStatement {
				placement = 1
			}
			p.rw.WriteComment(placement, t.Bytes)
			p.pos++
			// A trailing comment must immediately precede the EOL.
		case TokInt:
			// Local label definition: digit followed by colon.
			if p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == TokColon && t.Int >= 1 && t.Int <= 9 {
				p.rw.WriteLocalDef(byte(t.Int))
				p.pos += 2
				emittedStatement = true
				continue
			}
			return newErr(t.Pos, "unexpected number at start of statement")
		case TokIdent:
			// Label: identifier followed by colon.
			if p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == TokColon {
				id := p.st.Intern(t.Text)
				p.rw.WriteLabelDef(id)
				p.pos += 2
				emittedStatement = true
				continue
			}
			// Instruction / directive parsing arrives in Task 19+.
			return newErr(t.Pos, "instruction or directive parsing not yet implemented")
		default:
			return newErr(t.Pos, "unexpected token kind %d", t.Kind)
		}
	}
}
```

Update `Translate` in `main.go` to call `Lex` then `Parse`:

```go
func Translate(src []byte, path string) ([]byte, error) {
	toks, err := Lex(src, path)
	if err != nil {
		return nil, err
	}
	records, st, err := Parse(toks)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := format.WriteFile(&out, st, records); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/text2bin
g commit -m "m1: text2bin parser — labels, local labels, comments"
```

---

## Task 19: text2bin parser — instructions with register & immediate operands

**Files:**
- Modify: `tools/text2bin/parser.go`
- Test: `tools/text2bin/parser_inst_test.go`

This task adds instruction parsing for the simplest operand shapes: registers (X/W with SP/ZR variants) and immediate expressions (constants only — symbol references piggy-back via the lexer's TokIdent path and end up as `PUSH_SYM`).

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestParseInstAddRegImm(t *testing.T) {
	f := parseHelper(t, "add x0, x1, #4\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindInst {
		t.Fatalf("rec.Kind = %v", rec.Kind)
	}
	id, _ := format.MnemonicID("add")
	if rec.MnemonicID != id {
		t.Errorf("mnemonic_id = %d, want %d", rec.MnemonicID, id)
	}
	if rec.OperandCount != 3 {
		t.Errorf("operand_count = %d, want 3", rec.OperandCount)
	}
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpRegX || o.Reg != 0 {
		t.Errorf("op0 = %+v", o)
	}
	o, _ = or.Next()
	if o.Kind != format.OpRegX || o.Reg != 1 {
		t.Errorf("op1 = %+v", o)
	}
	o, _ = or.Next()
	if o.Kind != format.OpImmExpr {
		t.Errorf("op2 = %+v", o)
	}
	v, ok := format.EvalConst(o.Expr)
	if !ok || v != 4 {
		t.Errorf("op2 expr = (%d, %v), want (4, true)", v, ok)
	}
}

func TestParseInstZeroOperand(t *testing.T) {
	f := parseHelper(t, "nop\nret\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.MnemonicID != 0 || rec.OperandCount != 0 {
		t.Errorf("nop: %+v", rec)
	}
	rec, _ = r.Next()
	retID, _ := format.MnemonicID("ret")
	if rec.MnemonicID != retID || rec.OperandCount != 0 {
		t.Errorf("ret: %+v", rec)
	}
}

func TestParseInstSPAndZR(t *testing.T) {
	// `mov sp, x0` — first operand is OpRegXSP, second is OpRegX.
	f := parseHelper(t, "mov sp, x0\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpRegXSP || o.Reg != 31 {
		t.Errorf("op0 = %+v", o)
	}
	o, _ = or.Next()
	if o.Kind != format.OpRegX || o.Reg != 0 {
		t.Errorf("op1 = %+v", o)
	}
}

func TestParseInstSymbolRef(t *testing.T) {
	// Branch to a forward-referenced label: text2bin emits PUSH_SYM.
	f := parseHelper(t, "b target\n")
	if len(f.Names) != 1 || f.Names[0] != "target" {
		t.Errorf("names = %v", f.Names)
	}
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpImmExpr {
		t.Errorf("op0 = %+v", o)
	}
	// Stream should be PUSH_SYM 0.
	er := format.NewExprReader(o.Expr)
	op, _, _ := er.Next()
	if op != format.OpPushSym {
		t.Errorf("expr op = %v, want PUSH_SYM", op)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — instruction parsing not implemented.

- [ ] **Step 3: Extend `parser.go`**

Add the instruction path. Replace the `TokIdent` `return newErr(...)` line with a call into `parseInstOrDirective`:

```go
// (Add these methods to parser; the existing parseLine TokIdent
// branch becomes:)
//
//   if name has '.' prefix → parseDirective
//   else mnemonic → parseInst
//
// For Task 19 we only handle the mnemonic path; directives land in
// Task 21.

func (p *parser) parseInstOrDirective(t Tok) error {
	if len(t.Text) > 0 && t.Text[0] == '.' {
		return newErr(t.Pos, "directive parsing arrives in Task 21")
	}
	return p.parseInst(t)
}

func (p *parser) parseInst(t Tok) error {
	id, ok := format.MnemonicID(t.Text)
	if !ok {
		return newErr(t.Pos, "unknown mnemonic %q", t.Text)
	}
	p.pos++
	var ow format.OperandWriter
	count := byte(0)
	for {
		switch p.cur().Kind {
		case TokEOL, TokEOF, TokLineComment, TokBlockComment:
			p.rw.WriteInst(id, count, ow.Bytes())
			return nil
		case TokComma:
			if count == 0 {
				return newErr(p.cur().Pos, "unexpected ','")
			}
			p.pos++
			continue
		}
		if err := p.parseOperand(&ow); err != nil {
			return err
		}
		count++
	}
}

func (p *parser) parseOperand(ow *format.OperandWriter) error {
	t := p.cur()
	switch t.Kind {
	case TokIdent:
		// Register name?
		if kind, reg, ok := matchReg(t.Text); ok {
			ow.WriteReg(kind, reg)
			p.pos++
			return nil
		}
		// Otherwise, treat as the start of an expression (symbol).
		expr, err := p.parseExpression()
		if err != nil {
			return err
		}
		ow.WriteImmExpr(expr)
		return nil
	case TokHash, TokInt, TokMinus, TokTilde, TokLParen, TokDot, TokLocalRef:
		expr, err := p.parseExpression()
		if err != nil {
			return err
		}
		ow.WriteImmExpr(expr)
		return nil
	}
	return newErr(t.Pos, "unexpected token in operand")
}

// matchReg returns the operand kind and register index for a textual
// register name, or ok=false if it is not a register.
func matchReg(name string) (format.OperandKind, byte, bool) {
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

// parseExpression consumes tokens until it hits a comma, EOL, EOF, or
// a closing bracket. It returns the bytecode for the expression.
// Implements precedence climbing.
func (p *parser) parseExpression() ([]byte, error) {
	var w format.ExprWriter
	if err := p.parseExprPrec(&w, 0); err != nil {
		return nil, err
	}
	// Fold if entirely constant.
	if v, ok := format.EvalConst(w.Bytes()); ok {
		var folded format.ExprWriter
		folded.WriteImm(v)
		return folded.Bytes(), nil
	}
	return w.Bytes(), nil
}

// Precedence levels (lowest to highest):
//   0: | ^
//   1: &
//   2: << >>
//   3: + -
//   4: * /
//   5: unary - ~ (and primaries)
func tokPrec(k TokKind) int {
	switch k {
	case TokPipe, TokCaret:
		return 0
	case TokAmp:
		return 1
	case TokShl, TokShr:
		return 2
	case TokPlus, TokMinus:
		return 3
	case TokStar, TokSlash:
		return 4
	}
	return -1
}

func (p *parser) parseExprPrec(w *format.ExprWriter, minPrec int) error {
	if err := p.parseExprPrimary(w); err != nil {
		return err
	}
	for {
		k := p.cur().Kind
		prec := tokPrec(k)
		if prec < minPrec {
			return nil
		}
		opTok := p.cur()
		p.pos++
		if err := p.parseExprPrec(w, prec+1); err != nil {
			return err
		}
		switch opTok.Kind {
		case TokPlus:
			w.WriteOp(format.OpAdd)
		case TokMinus:
			w.WriteOp(format.OpSub)
		case TokStar:
			w.WriteOp(format.OpMul)
		case TokSlash:
			w.WriteOp(format.OpDiv)
		case TokAmp:
			w.WriteOp(format.OpAnd)
		case TokPipe:
			w.WriteOp(format.OpOr)
		case TokCaret:
			w.WriteOp(format.OpXor)
		case TokShl:
			w.WriteOp(format.OpShl)
		case TokShr:
			w.WriteOp(format.OpShr)
		}
	}
}

func (p *parser) parseExprPrimary(w *format.ExprWriter) error {
	t := p.cur()
	switch t.Kind {
	case TokHash:
		p.pos++
		return p.parseExprPrimary(w)
	case TokInt:
		w.WriteImm(t.Int)
		p.pos++
		return nil
	case TokIdent:
		id := p.st.Intern(t.Text)
		w.WriteSym(id)
		p.pos++
		return nil
	case TokDot:
		w.WritePC()
		p.pos++
		return nil
	case TokLocalRef:
		dir := byte(0)
		if t.LocalDir == 'b' {
			dir = 1
		}
		w.WriteLocal(t.Digit, dir)
		p.pos++
		return nil
	case TokMinus:
		p.pos++
		if err := p.parseExprPrimary(w); err != nil {
			return err
		}
		w.WriteOp(format.OpNeg)
		return nil
	case TokTilde:
		p.pos++
		if err := p.parseExprPrimary(w); err != nil {
			return err
		}
		w.WriteOp(format.OpNot)
		return nil
	case TokLParen:
		p.pos++
		if err := p.parseExprPrec(w, 0); err != nil {
			return err
		}
		if p.cur().Kind != TokRParen {
			return newErr(p.cur().Pos, "missing ')'")
		}
		p.pos++
		return nil
	}
	return newErr(t.Pos, "unexpected token in expression")
}
```

In `parseLine`, replace the unimplemented `TokIdent` branch with:

```go
case TokIdent:
    if p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == TokColon {
        id := p.st.Intern(t.Text)
        p.rw.WriteLabelDef(id)
        p.pos += 2
        emittedStatement = true
        continue
    }
    if err := p.parseInstOrDirective(t); err != nil {
        return err
    }
    emittedStatement = true
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/text2bin
g commit -m "m1: text2bin parser — registers and immediate expressions"
```

---

## Task 20: text2bin parser — shifted, extended, and memory operands

**Files:**
- Modify: `tools/text2bin/parser.go`
- Test: `tools/text2bin/parser_mem_test.go`

This task wires `lsl/lsr/asr/ror`/`uxt*/sxt*` and the six memory shapes from §4.

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func firstInst(t *testing.T, src string) format.Record {
	t.Helper()
	f := parseHelper(t, src)
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindInst {
		t.Fatalf("not INST: %+v", rec)
	}
	return rec
}

func TestParseShiftedReg(t *testing.T) {
	rec := firstInst(t, "add x0, x1, x2, lsl #4\n")
	or := format.NewOperandReader(rec.Operands)
	or.Next() // x0
	or.Next() // x1
	o, _ := or.Next()
	if o.Kind != format.OpShiftedReg || o.Width != 1 || o.Reg != 2 || o.ShiftKind != format.ShiftLSL {
		t.Errorf("op2 = %+v", o)
	}
	v, ok := format.EvalConst(o.AmtExpr)
	if !ok || v != 4 {
		t.Errorf("shift amt = (%d,%v)", v, ok)
	}
}

func TestParseExtendedReg(t *testing.T) {
	rec := firstInst(t, "add x0, x1, w2, uxtw #2\n")
	or := format.NewOperandReader(rec.Operands)
	or.Next()
	or.Next()
	o, _ := or.Next()
	if o.Kind != format.OpExtendedReg || o.Width != 0 || o.Reg != 2 || o.Extend != format.ExtUXTW {
		t.Errorf("op2 = %+v", o)
	}
}

func TestParseMemShapes(t *testing.T) {
	cases := []struct {
		src   string
		shape format.MemShape
	}{
		{"ldr x0, [x1]\n", format.MemBase},
		{"ldr x0, [x1, #8]\n", format.MemBaseOff},
		{"ldr x0, [x1, #8]!\n", format.MemBaseOffPre},
		{"ldr x0, [x1], #8\n", format.MemBaseOffPost},
		{"ldr x0, [x1, x2]\n", format.MemBaseIdx},
		{"ldr x0, [x1, x2, lsl #3]\n", format.MemBaseIdxShifted},
		{"ldr x0, [x1, w2, uxtw #2]\n", format.MemBaseIdxExtended},
	}
	for _, c := range cases {
		rec := firstInst(t, c.src)
		or := format.NewOperandReader(rec.Operands)
		or.Next() // x0
		o, _ := or.Next()
		if o.Kind != format.OpMem || o.MemShape != c.shape {
			t.Errorf("%q: got %+v, want shape %v", c.src, o, c.shape)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Extend `parseOperand`**

Add the shifted-register / extended-register / memory branches:

```go
// In parseOperand, after the register-match branch but before the
// expression fallback, handle:
//   - SHIFTED_REG / EXTENDED_REG: a register followed by ", <shift>"
//   - MEM: a '[' token.

// (Implementation outline. Insert into parseOperand:)

case TokLBracket:
    return p.parseMem(ow)

// And in the TokIdent register branch, after matchReg succeeds,
// look ahead for ", <shift_kind>".
```

Full code to add:

```go
func (p *parser) parseMem(ow *format.OperandWriter) error {
	p.pos++ // consume '['
	baseTok := p.cur()
	if baseTok.Kind != TokIdent {
		return newErr(baseTok.Pos, "expected register after '['")
	}
	baseKind, base, ok := matchReg(baseTok.Text)
	if !ok || (baseKind != format.OpRegX && baseKind != format.OpRegXSP) {
		return newErr(baseTok.Pos, "expected X register after '['")
	}
	p.pos++

	if p.cur().Kind == TokRBracket {
		p.pos++
		// Post-index? `[base], #imm`
		if p.cur().Kind == TokComma && p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == TokHash {
			p.pos++ // ,
			expr, err := p.parseExpression()
			if err != nil {
				return err
			}
			ow.WriteMemBaseOff(format.MemBaseOffPost, base, expr)
			return nil
		}
		ow.WriteMemBase(base)
		return nil
	}

	if p.cur().Kind != TokComma {
		return newErr(p.cur().Pos, "expected ',' or ']'")
	}
	p.pos++

	// Now either an index register or an immediate offset.
	if p.cur().Kind == TokIdent {
		idxKind, idx, ok := matchReg(p.cur().Text)
		if ok && (idxKind == format.OpRegX || idxKind == format.OpRegW) {
			width := byte(0)
			if idxKind == format.OpRegX {
				width = 1
			}
			p.pos++
			// Optional ", lsl #N" or extend
			if p.cur().Kind == TokComma {
				p.pos++
				modTok := p.cur()
				if modTok.Kind != TokIdent {
					return newErr(modTok.Pos, "expected shift/extend keyword")
				}
				if modTok.Text == "lsl" {
					p.pos++
					if p.cur().Kind != TokHash {
						return newErr(p.cur().Pos, "expected '#'")
					}
					p.pos++
					if p.cur().Kind != TokInt {
						return newErr(p.cur().Pos, "shift amount must be literal")
					}
					amt := byte(p.cur().Int)
					p.pos++
					if err := p.expect(TokRBracket); err != nil {
						return err
					}
					ow.WriteMemBaseIdxShifted(base, idx, width, amt)
					return nil
				}
				ext, ok := matchExtend(modTok.Text)
				if !ok {
					return newErr(modTok.Pos, "unknown extend %q", modTok.Text)
				}
				p.pos++
				amt := byte(0)
				if p.cur().Kind == TokHash {
					p.pos++
					if p.cur().Kind != TokInt {
						return newErr(p.cur().Pos, "extend amount must be literal")
					}
					amt = byte(p.cur().Int)
					p.pos++
				}
				if err := p.expect(TokRBracket); err != nil {
					return err
				}
				ow.WriteMemBaseIdxExtended(base, idx, width, ext, amt)
				return nil
			}
			if err := p.expect(TokRBracket); err != nil {
				return err
			}
			ow.WriteMemBaseIdx(base, idx, width)
			return nil
		}
	}

	// Otherwise: immediate offset.
	expr, err := p.parseExpression()
	if err != nil {
		return err
	}
	if err := p.expect(TokRBracket); err != nil {
		return err
	}
	if p.cur().Kind == TokBang {
		p.pos++
		ow.WriteMemBaseOff(format.MemBaseOffPre, base, expr)
		return nil
	}
	ow.WriteMemBaseOff(format.MemBaseOff, base, expr)
	return nil
}

func (p *parser) expect(k TokKind) error {
	if p.cur().Kind != k {
		return newErr(p.cur().Pos, "expected token %d, got %d", k, p.cur().Kind)
	}
	p.pos++
	return nil
}

func matchExtend(name string) (format.ExtendKind, bool) {
	for i := 0; i < 8; i++ {
		if format.ExtendKind(i).Name() == name {
			return format.ExtendKind(i), true
		}
	}
	return 0, false
}

func matchShiftKind(name string) (format.ShiftKind, bool) {
	for i := 0; i < 4; i++ {
		if format.ShiftKind(i).Name() == name {
			return format.ShiftKind(i), true
		}
	}
	return 0, false
}
```

Then in the existing `parseOperand` register branch, after `matchReg` succeeds, look ahead for a shift or extend keyword:

```go
case TokIdent:
    if kind, reg, ok := matchReg(t.Text); ok {
        p.pos++
        // Try ", <shift>/<extend>" continuation.
        if p.cur().Kind == TokComma && p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == TokIdent {
            next := p.toks[p.pos+1].Text
            if sk, ok := matchShiftKind(next); ok && (kind == format.OpRegX || kind == format.OpRegW) {
                p.pos += 2
                if p.cur().Kind != TokHash {
                    return newErr(p.cur().Pos, "expected '#' after shift")
                }
                p.pos++
                amt, err := p.parseExpression()
                if err != nil {
                    return err
                }
                width := byte(0)
                if kind == format.OpRegX {
                    width = 1
                }
                ow.WriteShiftedReg(width, reg, sk, amt)
                return nil
            }
            if ek, ok := matchExtend(next); ok && (kind == format.OpRegX || kind == format.OpRegW) {
                p.pos += 2
                var amt []byte
                if p.cur().Kind == TokHash {
                    p.pos++
                    a, err := p.parseExpression()
                    if err != nil {
                        return err
                    }
                    amt = a
                }
                width := byte(0)
                if kind == format.OpRegX {
                    width = 1
                }
                ow.WriteExtendedReg(width, reg, ek, amt)
                return nil
            }
        }
        ow.WriteReg(kind, reg)
        return nil
    }
    // (existing expression fallback)
```

Care: when the parser sees `add x0, x1, x2, lsl #4`, the lookahead must distinguish "next operand begins with x2 alone" vs "x2 carries a shift". The above branch handles the second case; the comma between operands when the next token is an identifier that is *not* `lsl/lsr/asr/ror/uxt*/sxt*` falls through to the normal `parseInst` operand loop, which advances past the comma and starts a new operand.

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/text2bin
g commit -m "m1: text2bin parser — shifted/extended/memory operands"
```

---

## Task 21: text2bin parser — directives, strings, cond codes, sysnames, PC-rel operators

**Files:**
- Modify: `tools/text2bin/parser.go`
- Test: `tools/text2bin/parser_directives_test.go`

This wraps up text2bin's vocabulary. Directives mirror instructions but key off the directive table. Strings come straight from the lexer's `TokString`. Cond codes are recognised when they appear as bare identifiers in an operand position where the mnemonic expects one (we treat them syntactically: if a bare `eq/ne/cs/...` is the only token before a comma/EOL, it's a `COND` operand). PC-rel operators (`:lo12:label` etc.) are pre-lexer-level — handle them as a primary-expression branch that recognises `':' IDENT ':' IDENT`.

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestParseDirectiveByte(t *testing.T) {
	f := parseHelper(t, ".byte 1, 2, 3\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindDirective {
		t.Fatalf("kind = %v", rec.Kind)
	}
	id, _ := format.DirectiveID(".byte")
	if rec.DirectiveID != id || rec.OperandCount != 3 {
		t.Errorf("rec = %+v", rec)
	}
}

func TestParseDirectiveAscii(t *testing.T) {
	f := parseHelper(t, ".ascii \"hi\"\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	o, _ := or.Next()
	if o.Kind != format.OpString || string(o.Str) != "hi" {
		t.Errorf("op0 = %+v", o)
	}
}

func TestParseBCondOperand(t *testing.T) {
	// `b.eq target` — text2bin treats "b.eq" as the mnemonic (we add
	// it to the table) and "target" as IMM_EXPR.
	// Alternative: lexer splits b.cond as "b" + COND + label. For M1
	// we go with the table-of-b.cond approach to keep parsing simple.
	f := parseHelper(t, "b.eq target\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	id, _ := format.MnemonicID("b.eq")
	if rec.MnemonicID != id {
		t.Errorf("mnemonic_id = %d, want %d", rec.MnemonicID, id)
	}
}

func TestParseCselWithCond(t *testing.T) {
	f := parseHelper(t, "csel x0, x1, x2, ne\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	or.Next()
	or.Next()
	or.Next()
	o, _ := or.Next()
	if o.Kind != format.OpCond || o.Cond != format.CondNE {
		t.Errorf("op3 = %+v", o)
	}
}

func TestParseLo12(t *testing.T) {
	f := parseHelper(t, "add x0, x1, :lo12:msg\n")
	r := format.NewRecordReader(f.Records)
	rec, _ := r.Next()
	or := format.NewOperandReader(rec.Operands)
	or.Next()
	or.Next()
	o, _ := or.Next()
	if o.Kind != format.OpImmExpr {
		t.Errorf("op2 kind = %v", o.Kind)
	}
	// Stream should be: PUSH_SYM 0, REL_LO12.
	er := format.NewExprReader(o.Expr)
	op, _, _ := er.Next()
	if op != format.OpPushSym {
		t.Errorf("first op = %v", op)
	}
	op, _, _ = er.Next()
	if op != format.OpRelLo12 {
		t.Errorf("second op = %v", op)
	}
}
```

Note: the `b.eq` case requires extending the mnemonic table. Add the 16 `b.<cond>` mnemonics to `MnemonicTable` (append-only).

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 3: Implement**

In `tools/sam-aarch64-format/mnemonics.go`, append `b.eq`, `b.ne`, `b.cs`, `b.cc`, `b.mi`, `b.pl`, `b.vs`, `b.vc`, `b.hi`, `b.ls`, `b.ge`, `b.lt`, `b.gt`, `b.le`, `b.al`, `b.nv` to `MnemonicTable`. (Their IDs are append-only and become whatever the next free slot is.)

In `tools/text2bin/parser.go`:

- Extend `parseInstOrDirective` to dispatch on the leading `.`:

```go
func (p *parser) parseInstOrDirective(t Tok) error {
	if len(t.Text) > 0 && t.Text[0] == '.' {
		return p.parseDirective(t)
	}
	return p.parseInst(t)
}

func (p *parser) parseDirective(t Tok) error {
	id, ok := format.DirectiveID(t.Text)
	if !ok {
		return newErr(t.Pos, "unknown directive %q", t.Text)
	}
	p.pos++
	var ow format.OperandWriter
	count := byte(0)
	for {
		switch p.cur().Kind {
		case TokEOL, TokEOF, TokLineComment, TokBlockComment:
			p.rw.WriteDirective(id, count, ow.Bytes())
			return nil
		case TokComma:
			if count == 0 {
				return newErr(p.cur().Pos, "unexpected ','")
			}
			p.pos++
			continue
		}
		if p.cur().Kind == TokString {
			ow.WriteString(p.cur().Bytes)
			p.pos++
			count++
			continue
		}
		if err := p.parseOperand(&ow); err != nil {
			return err
		}
		count++
	}
}
```

Extend `parseOperand` for cond codes: after the register-match attempt, before the expression fallback, try a cond match:

```go
if t.Kind == TokIdent {
    if c, ok := matchCond(t.Text); ok {
        ow.WriteCond(c)
        p.pos++
        return nil
    }
}
```

```go
func matchCond(name string) (format.CondCode, bool) {
	for i := 0; i < 16; i++ {
		if format.CondCode(i).Name() == name {
			return format.CondCode(i), true
		}
	}
	return 0, false
}
```

Extend `parseExprPrimary` for `TokColon` (PC-rel operators):

```go
case TokColon:
    // ':<name>:<expr>' — relocation operator.
    p.pos++
    if p.cur().Kind != TokIdent {
        return newErr(p.cur().Pos, "expected relocation name after ':'")
    }
    name := p.cur().Text
    p.pos++
    if p.cur().Kind != TokColon {
        return newErr(p.cur().Pos, "expected ':' after relocation name")
    }
    p.pos++
    if err := p.parseExprPrimary(w); err != nil {
        return err
    }
    op, ok := relocOp(name)
    if !ok {
        return newErr(t.Pos, "unknown relocation %q", name)
    }
    w.WriteOp(op)
    return nil
```

```go
func relocOp(name string) (format.ExprOp, bool) {
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
```

Also extend `parseOperand` to accept `OpSysName` for the `msr`/`mrs` family — but those mnemonics are not in the M1 starter set, so defer the actual `WriteSysName` wiring until a fixture needs it. Leave the operand-kind in the operand reader/writer (already done) so M3 doesn't need format changes.

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/sam-aarch64-format tools/text2bin
g commit -m "m1: text2bin parser — directives, cond codes, PC-rel operators"
```

---

## Task 22: text2bin integration smoke test

**Files:**
- Create: `tools/text2bin/integration_test.go`

A single Go test that runs text2bin against a multi-construct fixture string and asserts the file decodes without error, covers the expected record kinds, and uses the expected symbol-table names. This is a sanity check before bin2text exists — Layer 2 (Task 26) is the real round-trip.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"strings"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestIntegrationAllConstructs(t *testing.T) {
	src := strings.Join([]string{
		"// banner",
		".text",
		".global main",
		"main:",
		"    mov x0, #0",
		"    add x0, x0, #1 // inline",
		"    cmp x0, #10",
		"    b.lt 1f",
		"    ret",
		"1:",
		"    b 1b",
		".data",
		"msg:",
		"    .ascii \"hi\\n\"",
		"",
	}, "\n") + "\n"

	out, err := Translate([]byte(src), "x.s")
	if err != nil {
		t.Fatal(err)
	}
	f, err := format.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 {
		t.Errorf("version = %d", f.Version)
	}
	// "main" and "msg" should be interned.
	found := map[string]bool{}
	for _, n := range f.Names {
		found[n] = true
	}
	for _, n := range []string{"main", "msg"} {
		if !found[n] {
			t.Errorf("expected name %q missing from %v", n, f.Names)
		}
	}
	// Count record kinds.
	counts := map[format.RecordKind]int{}
	rr := format.NewRecordReader(f.Records)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			t.Fatal(err)
		}
		counts[rec.Kind]++
	}
	if counts[format.KindLabelDef] < 2 {
		t.Errorf("LABEL_DEF count = %d, want ≥ 2", counts[format.KindLabelDef])
	}
	if counts[format.KindLocalDef] < 1 {
		t.Errorf("LOCAL_DEF count = %d, want ≥ 1", counts[format.KindLocalDef])
	}
	if counts[format.KindInst] < 6 {
		t.Errorf("INST count = %d, want ≥ 6", counts[format.KindInst])
	}
	if counts[format.KindDirective] < 3 {
		t.Errorf("DIRECTIVE count = %d, want ≥ 3", counts[format.KindDirective])
	}
	if counts[format.KindComment] < 1 {
		t.Errorf("COMMENT count = %d, want ≥ 1", counts[format.KindComment])
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Expected: PASS — every component already exists from Tasks 16–21.

- [ ] **Step 3: If it fails**, the failure pinpoints which construct still lacks parser support; fix in the relevant earlier task's code and re-run.

- [ ] **Step 4: Commit**

```bash
g add tools/text2bin
g commit -m "m1: text2bin all-constructs integration smoke test"
```

---

## Task 23: bin2text scaffold

**Files:**
- Create: `tools/bin2text/go.mod`
- Create: `tools/bin2text/main.go`
- Create: `tools/bin2text/emit.go`
- Test: `tools/bin2text/emit_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"bytes"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestEmitEmpty(t *testing.T) {
	var buf bytes.Buffer
	format.WriteFile(&buf, format.NewSymbolTable(), nil)
	out, err := Emit(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("emit of empty file = %q, want empty", string(out))
	}
}

func TestEmitLabelDef(t *testing.T) {
	st := format.NewSymbolTable()
	st.Intern("loop")
	var rw format.RecordWriter
	rw.WriteLabelDef(0)
	var buf bytes.Buffer
	format.WriteFile(&buf, st, rw.Bytes())
	out, err := Emit(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	want := "loop:\n"
	if string(out) != want {
		t.Errorf("emit = %q, want %q", string(out), want)
	}
}

func TestEmitLocalDef(t *testing.T) {
	var rw format.RecordWriter
	rw.WriteLocalDef(3)
	var buf bytes.Buffer
	format.WriteFile(&buf, format.NewSymbolTable(), rw.Bytes())
	out, _ := Emit(buf.Bytes())
	if string(out) != "3:\n" {
		t.Errorf("emit = %q, want %q", string(out), "3:\n")
	}
}

func TestEmitCommentPlacement(t *testing.T) {
	var rw format.RecordWriter
	rw.WriteComment(0, []byte(" standalone"))
	rw.WriteLabelDef(0)
	rw.WriteComment(1, []byte(" trailing"))
	st := format.NewSymbolTable()
	st.Intern("x")
	var buf bytes.Buffer
	format.WriteFile(&buf, st, rw.Bytes())
	out, _ := Emit(buf.Bytes())
	want := "// standalone\nx: // trailing\n"
	if string(out) != want {
		t.Errorf("got %q, want %q", string(out), want)
	}
}
```

- [ ] **Step 2: Scaffold module**

```bash
mkdir -p tools/bin2text
cd tools/bin2text
go mod init github.com/petemoore/sam-aarch64/tools/bin2text
```

Append to `go.mod`:

```
require github.com/petemoore/sam-aarch64/tools/sam-aarch64-format v0.0.0-00010101000000-000000000000

replace github.com/petemoore/sam-aarch64/tools/sam-aarch64-format => ../sam-aarch64-format
```

- [ ] **Step 3: Run test to verify it fails**

Expected: FAIL.

- [ ] **Step 4: Implement `emit.go`**

```go
package main

import (
	"bytes"
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// Emit reads .tbn bytes and returns canonically-formatted text.
func Emit(in []byte) ([]byte, error) {
	f, err := format.ReadFile(in)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	rr := format.NewRecordReader(f.Records)
	var prevWasStatement bool
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			return nil, err
		}
		switch rec.Kind {
		case format.KindLabelDef:
			if prevWasStatement {
				out.WriteByte('\n')
			}
			fmt.Fprintf(&out, "%s:", f.Names[rec.SymbolID])
			prevWasStatement = true
		case format.KindLocalDef:
			if prevWasStatement {
				out.WriteByte('\n')
			}
			fmt.Fprintf(&out, "%d:", rec.Digit)
			prevWasStatement = true
		case format.KindComment:
			if rec.Placement == 1 && prevWasStatement {
				out.WriteByte(' ')
				fmt.Fprintf(&out, "//%s", string(rec.Body))
				out.WriteByte('\n')
				prevWasStatement = false
				continue
			}
			if prevWasStatement {
				out.WriteByte('\n')
			}
			fmt.Fprintf(&out, "//%s\n", string(rec.Body))
			prevWasStatement = false
		case format.KindInst, format.KindDirective:
			if prevWasStatement {
				out.WriteByte('\n')
			}
			if err := emitStatement(&out, f, rec); err != nil {
				return nil, err
			}
			prevWasStatement = true
		default:
			fmt.Fprintf(&out, "// [skipped unknown record kind 0x%02x, %d bytes]\n",
				byte(rec.Kind), len(rec.Raw))
			prevWasStatement = false
		}
	}
	if prevWasStatement {
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// emitStatement is filled in by Task 24.
func emitStatement(out *bytes.Buffer, f *format.File, rec format.Record) error {
	return fmt.Errorf("emitStatement: not yet implemented for kind %v", rec.Kind)
}
```

`main.go`:

```go
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var outFlag string
	flag.StringVar(&outFlag, "o", "", "output file (default stdout)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: bin2text INPUT.tbn [-o OUTPUT.s]")
		os.Exit(2)
	}
	in, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out, err := Emit(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if outFlag == "" {
		os.Stdout.Write(out)
		return
	}
	if err := os.WriteFile(outFlag, out, 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Expected: PASS for the four tests above (instructions/directives stub out via `emitStatement` — they are exercised in Task 24).

- [ ] **Step 6: Commit**

```bash
g add tools/bin2text
g commit -m "m1: bin2text scaffold — labels, local labels, comments"
```

---

## Task 24: bin2text — instruction & directive printers (including operands & expressions)

**Files:**
- Modify: `tools/bin2text/emit.go`
- Test: `tools/bin2text/emit_inst_test.go`

This is the biggest single bin2text task — it covers every operand kind and the expression-bytecode pretty printer.

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"bytes"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func emitFile(t *testing.T, st *format.SymbolTable, rw format.RecordWriter) string {
	t.Helper()
	var buf bytes.Buffer
	format.WriteFile(&buf, st, rw.Bytes())
	out, err := Emit(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestEmitNop(t *testing.T) {
	var rw format.RecordWriter
	rw.WriteInst(0, 0, nil) // nop
	got := emitFile(t, format.NewSymbolTable(), rw)
	if got != "  nop\n" {
		t.Errorf("got %q, want %q", got, "  nop\n")
	}
}

func TestEmitAddRegImm(t *testing.T) {
	var ow format.OperandWriter
	ow.WriteReg(format.OpRegX, 0)
	ow.WriteReg(format.OpRegX, 1)
	var ew format.ExprWriter
	ew.WriteImm(4)
	ow.WriteImmExpr(ew.Bytes())
	var rw format.RecordWriter
	id, _ := format.MnemonicID("add")
	rw.WriteInst(id, 3, ow.Bytes())
	got := emitFile(t, format.NewSymbolTable(), rw)
	if got != "  add x0, x1, #4\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitSymbolBranch(t *testing.T) {
	st := format.NewSymbolTable()
	id := st.Intern("target")
	var ew format.ExprWriter
	ew.WriteSym(id)
	var ow format.OperandWriter
	ow.WriteImmExpr(ew.Bytes())
	bid, _ := format.MnemonicID("b")
	var rw format.RecordWriter
	rw.WriteInst(bid, 1, ow.Bytes())
	got := emitFile(t, st, rw)
	if got != "  b target\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitDirectiveByte(t *testing.T) {
	var ow format.OperandWriter
	for _, v := range []int64{1, 2, 3} {
		var ew format.ExprWriter
		ew.WriteImm(v)
		ow.WriteImmExpr(ew.Bytes())
	}
	id, _ := format.DirectiveID(".byte")
	var rw format.RecordWriter
	rw.WriteDirective(id, 3, ow.Bytes())
	got := emitFile(t, format.NewSymbolTable(), rw)
	if got != "  .byte 1, 2, 3\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitShiftedReg(t *testing.T) {
	var ow format.OperandWriter
	ow.WriteReg(format.OpRegX, 0)
	ow.WriteReg(format.OpRegX, 1)
	var ew format.ExprWriter
	ew.WriteImm(4)
	ow.WriteShiftedReg(1, 2, format.ShiftLSL, ew.Bytes())
	var rw format.RecordWriter
	id, _ := format.MnemonicID("add")
	rw.WriteInst(id, 3, ow.Bytes())
	got := emitFile(t, format.NewSymbolTable(), rw)
	if got != "  add x0, x1, x2, lsl #4\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitMemShapes(t *testing.T) {
	cases := []struct {
		build func(*format.OperandWriter)
		want  string
	}{
		{func(ow *format.OperandWriter) { ow.WriteMemBase(1) }, "[x1]"},
		{
			func(ow *format.OperandWriter) {
				var ew format.ExprWriter
				ew.WriteImm(8)
				ow.WriteMemBaseOff(format.MemBaseOff, 1, ew.Bytes())
			},
			"[x1, #8]",
		},
		{
			func(ow *format.OperandWriter) {
				var ew format.ExprWriter
				ew.WriteImm(8)
				ow.WriteMemBaseOff(format.MemBaseOffPre, 1, ew.Bytes())
			},
			"[x1, #8]!",
		},
		{
			func(ow *format.OperandWriter) {
				var ew format.ExprWriter
				ew.WriteImm(8)
				ow.WriteMemBaseOff(format.MemBaseOffPost, 1, ew.Bytes())
			},
			"[x1], #8",
		},
		{func(ow *format.OperandWriter) { ow.WriteMemBaseIdx(1, 2, 1) }, "[x1, x2]"},
		{
			func(ow *format.OperandWriter) { ow.WriteMemBaseIdxShifted(1, 2, 1, 3) },
			"[x1, x2, lsl #3]",
		},
		{
			func(ow *format.OperandWriter) {
				ow.WriteMemBaseIdxExtended(1, 2, 0, format.ExtUXTW, 2)
			},
			"[x1, w2, uxtw #2]",
		},
	}
	for _, c := range cases {
		var ow format.OperandWriter
		ow.WriteReg(format.OpRegX, 0)
		c.build(&ow)
		var rw format.RecordWriter
		id, _ := format.MnemonicID("ldr")
		rw.WriteInst(id, 2, ow.Bytes())
		got := emitFile(t, format.NewSymbolTable(), rw)
		want := "  ldr x0, " + c.want + "\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestEmitImmDecimalVsHex(t *testing.T) {
	// < 256 → decimal; ≥ 256 → 0x hex.
	cases := []struct {
		v    int64
		want string
	}{
		{42, "#42"},
		{255, "#255"},
		{256, "#0x100"},
		{-1, "#-1"},
	}
	for _, c := range cases {
		var ew format.ExprWriter
		ew.WriteImm(c.v)
		var ow format.OperandWriter
		ow.WriteReg(format.OpRegX, 0)
		ow.WriteImmExpr(ew.Bytes())
		var rw format.RecordWriter
		id, _ := format.MnemonicID("mov")
		rw.WriteInst(id, 2, ow.Bytes())
		got := emitFile(t, format.NewSymbolTable(), rw)
		want := "  mov x0, " + c.want + "\n"
		if got != want {
			t.Errorf("v=%d: got %q, want %q", c.v, got, want)
		}
	}
}

func TestEmitExpressionWithSymbol(t *testing.T) {
	st := format.NewSymbolTable()
	id := st.Intern("msg")
	var ew format.ExprWriter
	ew.WriteSym(id)
	ew.WriteImm(8)
	ew.WriteOp(format.OpAdd)
	var ow format.OperandWriter
	ow.WriteReg(format.OpRegX, 0)
	ow.WriteImmExpr(ew.Bytes())
	var rw format.RecordWriter
	mid, _ := format.MnemonicID("mov")
	rw.WriteInst(mid, 2, ow.Bytes())
	got := emitFile(t, st, rw)
	if got != "  mov x0, (msg + 8)\n" {
		t.Errorf("got %q", got)
	}
}

func TestEmitLo12(t *testing.T) {
	st := format.NewSymbolTable()
	id := st.Intern("msg")
	var ew format.ExprWriter
	ew.WriteSym(id)
	ew.WriteOp(format.OpRelLo12)
	var ow format.OperandWriter
	ow.WriteReg(format.OpRegX, 0)
	ow.WriteReg(format.OpRegX, 1)
	ow.WriteImmExpr(ew.Bytes())
	var rw format.RecordWriter
	aid, _ := format.MnemonicID("add")
	rw.WriteInst(aid, 3, ow.Bytes())
	got := emitFile(t, st, rw)
	if got != "  add x0, x1, :lo12:msg\n" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Expected: FAIL.

- [ ] **Step 3: Implement `emitStatement` + helpers**

Replace the stub in `emit.go`:

```go
func emitStatement(out *bytes.Buffer, f *format.File, rec format.Record) error {
	out.WriteString("  ")
	switch rec.Kind {
	case format.KindInst:
		name := format.MnemonicName(rec.MnemonicID)
		if name == "" {
			return fmt.Errorf("unknown mnemonic_id %d", rec.MnemonicID)
		}
		out.WriteString(name)
	case format.KindDirective:
		name := format.DirectiveName(rec.DirectiveID)
		if name == "" {
			return fmt.Errorf("unknown directive_id %d", rec.DirectiveID)
		}
		out.WriteString(name)
	}
	if rec.OperandCount == 0 {
		return nil
	}
	out.WriteByte(' ')
	or := format.NewOperandReader(rec.Operands)
	first := true
	for !or.AtEnd() {
		o, err := or.Next()
		if err != nil {
			return err
		}
		if !first {
			out.WriteString(", ")
		}
		first = false
		if err := emitOperand(out, f, o); err != nil {
			return err
		}
	}
	return nil
}

func emitOperand(out *bytes.Buffer, f *format.File, o format.Operand) error {
	switch o.Kind {
	case format.OpRegX:
		writeRegX(out, o.Reg)
	case format.OpRegW:
		writeRegW(out, o.Reg)
	case format.OpRegXSP:
		writeRegXSP(out, o.Reg)
	case format.OpRegWSP:
		writeRegWSP(out, o.Reg)
	case format.OpImmExpr:
		return emitExprAsImmediate(out, f, o.Expr)
	case format.OpShiftedReg:
		if o.Width == 1 {
			writeRegX(out, o.Reg)
		} else {
			writeRegW(out, o.Reg)
		}
		fmt.Fprintf(out, ", %s ", o.ShiftKind.Name())
		return emitExprAsImmediate(out, f, o.AmtExpr)
	case format.OpExtendedReg:
		if o.Width == 1 {
			writeRegX(out, o.Reg)
		} else {
			writeRegW(out, o.Reg)
		}
		fmt.Fprintf(out, ", %s", o.Extend.Name())
		if len(o.AmtExpr) > 0 {
			out.WriteString(" ")
			return emitExprAsImmediate(out, f, o.AmtExpr)
		}
	case format.OpMem:
		return emitMem(out, f, o)
	case format.OpString:
		out.WriteByte('"')
		writeEscapedString(out, o.Str)
		out.WriteByte('"')
	case format.OpCond:
		out.WriteString(o.Cond.Name())
	case format.OpSysName:
		out.Write(o.Str)
	default:
		return fmt.Errorf("emitOperand: unsupported kind %v", o.Kind)
	}
	return nil
}

func writeRegX(out *bytes.Buffer, r byte) {
	switch r {
	case 29:
		out.WriteString("fp")
	case 30:
		out.WriteString("lr")
	case 31:
		out.WriteString("xzr")
	default:
		fmt.Fprintf(out, "x%d", r)
	}
}

func writeRegW(out *bytes.Buffer, r byte) {
	if r == 31 {
		out.WriteString("wzr")
		return
	}
	fmt.Fprintf(out, "w%d", r)
}

func writeRegXSP(out *bytes.Buffer, r byte) {
	if r == 31 {
		out.WriteString("sp")
		return
	}
	writeRegX(out, r)
}

func writeRegWSP(out *bytes.Buffer, r byte) {
	if r == 31 {
		out.WriteString("wsp")
		return
	}
	writeRegW(out, r)
}

func emitMem(out *bytes.Buffer, f *format.File, o format.Operand) error {
	out.WriteByte('[')
	writeRegXSP(out, o.Base)
	switch o.MemShape {
	case format.MemBase:
		out.WriteByte(']')
	case format.MemBaseOff:
		out.WriteString(", ")
		if err := emitExprAsImmediate(out, f, o.Expr); err != nil {
			return err
		}
		out.WriteByte(']')
	case format.MemBaseOffPre:
		out.WriteString(", ")
		if err := emitExprAsImmediate(out, f, o.Expr); err != nil {
			return err
		}
		out.WriteByte(']')
		out.WriteByte('!')
	case format.MemBaseOffPost:
		out.WriteByte(']')
		out.WriteString(", ")
		if err := emitExprAsImmediate(out, f, o.Expr); err != nil {
			return err
		}
	case format.MemBaseIdx:
		out.WriteString(", ")
		if o.IdxWidth == 1 {
			writeRegX(out, o.Idx)
		} else {
			writeRegW(out, o.Idx)
		}
		out.WriteByte(']')
	case format.MemBaseIdxShifted:
		out.WriteString(", ")
		writeRegX(out, o.Idx)
		fmt.Fprintf(out, ", lsl #%d]", o.ShiftAmt)
	case format.MemBaseIdxExtended:
		out.WriteString(", ")
		if o.IdxWidth == 1 {
			writeRegX(out, o.Idx)
		} else {
			writeRegW(out, o.Idx)
		}
		fmt.Fprintf(out, ", %s", o.Extend.Name())
		if o.ShiftAmt != 0 {
			fmt.Fprintf(out, " #%d", o.ShiftAmt)
		}
		out.WriteByte(']')
	}
	return nil
}

// emitExprAsImmediate prints an expression in immediate (`#…`) context.
// If the expression is a single PUSH_SYM or PUSH_LOCAL or a REL_*
// chain whose root operand is a symbol, print without the '#'.
func emitExprAsImmediate(out *bytes.Buffer, f *format.File, expr []byte) error {
	if v, ok := format.EvalConst(expr); ok {
		if v >= 0 && v < 256 {
			fmt.Fprintf(out, "#%d", v)
		} else if v < 0 && v > -256 {
			fmt.Fprintf(out, "#%d", v)
		} else {
			fmt.Fprintf(out, "#0x%x", v)
		}
		return nil
	}
	// Non-constant: render as a parenthesised expression (for general
	// math) or as a bare label/reloc when the stream is a recognised
	// simple shape.
	if simple, text, ok := simpleSymRef(expr, f); ok {
		_ = simple
		out.WriteString(text)
		return nil
	}
	out.WriteByte('(')
	if err := printExpr(out, f, expr); err != nil {
		return err
	}
	out.WriteByte(')')
	return nil
}

// simpleSymRef recognises:
//   PUSH_SYM N                         → "<name>"
//   PUSH_SYM N ; REL_LO12              → ":lo12:<name>"
//   PUSH_LOCAL d, dir                  → "<d>f" or "<d>b"
// Returns text and ok=true if matched.
func simpleSymRef(expr []byte, f *format.File) (bool, string, bool) {
	r := format.NewExprReader(expr)
	op, operand, err := r.Next()
	if err != nil {
		return false, "", false
	}
	switch op {
	case format.OpPushSym:
		id := uint16(operand[0]) | uint16(operand[1])<<8
		name := f.Names[id]
		if r.AtEnd() {
			return true, name, true
		}
		op2, _, _ := r.Next()
		if !r.AtEnd() {
			return false, "", false
		}
		switch op2 {
		case format.OpRelLo12:
			return true, ":lo12:" + name, true
		case format.OpRelHi12:
			return true, ":hi12:" + name, true
		}
	case format.OpPushLocal:
		d := operand[0]
		dir := byte('f')
		if operand[1] == 1 {
			dir = 'b'
		}
		if r.AtEnd() {
			return true, fmt.Sprintf("%d%c", d, dir), true
		}
	}
	return false, "", false
}

// printExpr renders an arbitrary bytecode stream as infix text. This
// is the slow general path; simpleSymRef handles the common cases.
func printExpr(out *bytes.Buffer, f *format.File, expr []byte) error {
	r := format.NewExprReader(expr)
	stack := make([]string, 0, 4)
	for !r.AtEnd() {
		op, operand, err := r.Next()
		if err != nil {
			return err
		}
		switch op {
		case format.OpPushImm8:
			stack = append(stack, fmt.Sprintf("%d", int8(operand[0])))
		case format.OpPushImm16:
			v := int16(uint16(operand[0]) | uint16(operand[1])<<8)
			stack = append(stack, fmt.Sprintf("%d", v))
		case format.OpPushImm32:
			v := int32(uint32(operand[0]) | uint32(operand[1])<<8 | uint32(operand[2])<<16 | uint32(operand[3])<<24)
			stack = append(stack, fmt.Sprintf("%d", v))
		case format.OpPushSym:
			id := uint16(operand[0]) | uint16(operand[1])<<8
			stack = append(stack, f.Names[id])
		case format.OpPushLocal:
			dir := 'f'
			if operand[1] == 1 {
				dir = 'b'
			}
			stack = append(stack, fmt.Sprintf("%d%c", operand[0], dir))
		case format.OpPushPC:
			stack = append(stack, ".")
		case format.OpAdd, format.OpSub, format.OpMul, format.OpDiv,
			format.OpAnd, format.OpOr, format.OpXor, format.OpShl, format.OpShr:
			if len(stack) < 2 {
				return fmt.Errorf("printExpr: stack underflow at %v", op)
			}
			b := stack[len(stack)-1]
			a := stack[len(stack)-2]
			stack = stack[:len(stack)-2]
			stack = append(stack, fmt.Sprintf("%s %s %s", a, opSym(op), b))
		case format.OpNeg:
			if len(stack) < 1 {
				return fmt.Errorf("printExpr: stack underflow at NEG")
			}
			stack[len(stack)-1] = "-" + stack[len(stack)-1]
		case format.OpNot:
			if len(stack) < 1 {
				return fmt.Errorf("printExpr: stack underflow at NOT")
			}
			stack[len(stack)-1] = "~" + stack[len(stack)-1]
		case format.OpRelLo12, format.OpRelHi12,
			format.OpRelAbsG0, format.OpRelAbsG0NC,
			format.OpRelAbsG1, format.OpRelAbsG1NC,
			format.OpRelAbsG2, format.OpRelAbsG2NC, format.OpRelAbsG3:
			if len(stack) < 1 {
				return fmt.Errorf("printExpr: stack underflow at %v", op)
			}
			stack[len(stack)-1] = ":" + relName(op) + ":" + stack[len(stack)-1]
		default:
			return fmt.Errorf("printExpr: unknown opcode %v", op)
		}
	}
	if len(stack) != 1 {
		return fmt.Errorf("printExpr: stack ended with %d values", len(stack))
	}
	out.WriteString(stack[0])
	return nil
}

func opSym(op format.ExprOp) string {
	switch op {
	case format.OpAdd:
		return "+"
	case format.OpSub:
		return "-"
	case format.OpMul:
		return "*"
	case format.OpDiv:
		return "/"
	case format.OpAnd:
		return "&"
	case format.OpOr:
		return "|"
	case format.OpXor:
		return "^"
	case format.OpShl:
		return "<<"
	case format.OpShr:
		return ">>"
	}
	return "?"
}

func relName(op format.ExprOp) string {
	switch op {
	case format.OpRelLo12:
		return "lo12"
	case format.OpRelHi12:
		return "hi12"
	case format.OpRelAbsG0:
		return "abs_g0"
	case format.OpRelAbsG0NC:
		return "abs_g0_nc"
	case format.OpRelAbsG1:
		return "abs_g1"
	case format.OpRelAbsG1NC:
		return "abs_g1_nc"
	case format.OpRelAbsG2:
		return "abs_g2"
	case format.OpRelAbsG2NC:
		return "abs_g2_nc"
	case format.OpRelAbsG3:
		return "abs_g3"
	}
	return "?"
}

func writeEscapedString(out *bytes.Buffer, body []byte) {
	for _, b := range body {
		switch b {
		case '\\':
			out.WriteString(`\\`)
		case '"':
			out.WriteString(`\"`)
		case '\n':
			out.WriteString(`\n`)
		case '\t':
			out.WriteString(`\t`)
		case 0:
			out.WriteString(`\0`)
		default:
			if b < 0x20 || b >= 0x7F {
				fmt.Fprintf(out, "\\x%02x", b)
			} else {
				out.WriteByte(b)
			}
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
g add tools/bin2text
g commit -m "m1: bin2text — instructions, directives, operands, expressions"
```

---

## Task 25: Layer 1 unit-test sweep + idempotency

**Files:**
- Create: `tools/sam-aarch64-format/idempotency_test.go`
- Create: `tools/text2bin/idempotency_test.go`

A sweep test that pushes a representative `.s` snippet through `Translate` then `Emit` then `Translate` and asserts the second `.tbn` is byte-identical to the first. This catches any non-canonical formatting that produced a different binary on the second pass.

- [ ] **Step 1: Write the failing test**

`tools/text2bin/idempotency_test.go`:

```go
package main

import (
	"bytes"
	"testing"

	bin2text "github.com/petemoore/sam-aarch64/tools/bin2text" // requires a replace
	_ = bin2text                                                // (use indirectly via package main)
)

// NOTE: cross-tool import requires a replace directive in go.mod:
//
//   replace github.com/petemoore/sam-aarch64/tools/bin2text => ../bin2text
//
// Add it as part of this task.

func TestIdempotency(t *testing.T) {
	sources := []string{
		"  nop\n",
		"main:\n  add x0, x1, #4\n",
		"  ldr x0, [x1, #8]\n",
		"  add x0, x1, x2, lsl #4\n",
		"  b.lt 1f\n1:\n",
		"  add x0, x1, :lo12:msg\n",
		".byte 1, 2, 3\n",
		".ascii \"hi\"\n",
	}
	for _, src := range sources {
		bin1, err := Translate([]byte(src), "test.s")
		if err != nil {
			t.Errorf("first Translate of %q: %v", src, err)
			continue
		}
		// Re-emit and re-translate.
		// Since bin2text is a separate package main, call its Emit via a build-tag indirection.
		// In practice the helper is moved into a shared internal package for testability — see Step 3.
		_ = bin1
		t.Skip("see Step 3 — restructure Emit into an importable package")
	}
}
```

**Reality check**: `package main` packages cannot be imported. Step 3 fixes this.

- [ ] **Step 2: Restructure `bin2text` so `Emit` is importable**

Move `tools/bin2text/emit.go` (and its tests) into `tools/bin2text/internal/emit/emit.go` (`package emit`). `main.go` now imports `emit` and calls `emit.Emit`. The shared `tools/sam-aarch64-format` library is unchanged.

Update test imports accordingly.

- [ ] **Step 3: Write the idempotency test using the shared package**

```go
package main

import (
	"bytes"
	"testing"

	emit "github.com/petemoore/sam-aarch64/tools/bin2text/internal/emit"
)

// In tools/text2bin/go.mod add:
//   replace github.com/petemoore/sam-aarch64/tools/bin2text/internal/emit => ../bin2text/internal/emit

func TestIdempotency(t *testing.T) {
	sources := []string{
		"  nop\n",
		"main:\n  add x0, x1, #4\n",
		"  ldr x0, [x1, #8]\n",
		"  add x0, x1, x2, lsl #4\n",
		"  b.lt 1f\n1:\n",
		"  add x0, x1, :lo12:msg\n",
		".byte 1, 2, 3\n",
		".ascii \"hi\"\n",
	}
	for _, src := range sources {
		bin1, err := Translate([]byte(src), "test.s")
		if err != nil {
			t.Errorf("first Translate %q: %v", src, err)
			continue
		}
		canon, err := emit.Emit(bin1)
		if err != nil {
			t.Errorf("Emit %q: %v", src, err)
			continue
		}
		bin2, err := Translate(canon, "test.s")
		if err != nil {
			t.Errorf("second Translate %q: %v", src, err)
			continue
		}
		if !bytes.Equal(bin1, bin2) {
			t.Errorf("idempotency failed for %q:\n bin1 = % X\n bin2 = % X\n canon = %q",
				src, bin1, bin2, string(canon))
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Expected: PASS. If it fails, the failure points to a non-canonical formatting choice — pick the canonical one and re-run.

- [ ] **Step 5: Commit**

```bash
g add tools/bin2text tools/text2bin
g commit -m "m1: idempotency sweep (text2bin → bin2text → text2bin byte-identical)"
```

---

## Task 26: Layer 2 — `.s` round-trip golden corpus

**Files:**
- Create: `tests/m1/sources/*.s` (one fixture per construct family)
- Create: `tests/m1/golden/*.s` (canonical outputs, written first by `-update`)
- Create: `tools/text2bin/golden_test.go`

The golden harness loads each `.s` file under `tests/m1/sources/`, runs `Translate → Emit`, and compares against `tests/m1/golden/<basename>.s`. A `-update` flag regenerates goldens.

- [ ] **Step 1: Author fixtures**

Create one fixture per construct family. Names are deliberate and stable. Below is the minimum required corpus; add more as gaps appear.

```
tests/m1/sources/
  empty.s               # empty file
  labels.s              # global labels + .global
  local_labels.s        # 1: / 1f / 1b chain
  comments.s            # standalone + trailing + block
  inst_nop_ret.s        # zero-operand instructions
  inst_reg_imm.s        # add/sub/mov with reg+imm
  inst_shifted.s        # add with shifted register
  inst_extended.s       # add with extended register
  inst_mem_simple.s     # ldr [xn], [xn,#imm]
  inst_mem_indexed.s    # ldr [xn,xm], [xn,xm,lsl #N]
  inst_mem_extended.s   # ldr [xn,wm,uxtw #N]
  inst_mem_preindex.s   # ldr [xn,#imm]! and ldr [xn],#imm
  inst_bcond.s          # b.eq / b.ne / b.lt
  inst_csel.s           # csel with cond
  expr_simple.s         # mov x0, #(1+2*3)
  expr_pcrel.s          # adrp + add :lo12:
  dir_data.s            # .byte/.short/.word/.quad
  dir_string.s          # .ascii / .asciz
  dir_equ.s             # .equ FOO, 4
  dir_align_skip.s      # .balign / .skip
```

Each fixture should be small (≤ 10 lines) and self-contained.

Sample `inst_reg_imm.s`:

```asm
main:
  mov x0, #0
  add x0, x0, #1
  sub x0, x0, #2
```

Sample `expr_pcrel.s`:

```asm
.text
  adrp x0, msg
  add x0, x0, :lo12:msg
.data
msg:
  .ascii "hi"
```

- [ ] **Step 2: Implement the golden harness**

`tools/text2bin/golden_test.go`:

```go
package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	emit "github.com/petemoore/sam-aarch64/tools/bin2text/internal/emit"
)

var updateGoldens = flag.Bool("update", false, "rewrite golden files")

func TestGoldenCorpus(t *testing.T) {
	matches, err := filepath.Glob("../../tests/m1/sources/*.s")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no source fixtures found")
	}
	for _, src := range matches {
		src := src
		base := filepath.Base(src)
		t.Run(base, func(t *testing.T) {
			input, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			bin, err := Translate(input, src)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			out, err := emit.Emit(bin)
			if err != nil {
				t.Fatalf("Emit: %v", err)
			}
			goldenPath := filepath.Join("../../tests/m1/golden", base)
			if *updateGoldens {
				if err := os.WriteFile(goldenPath, out, 0644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden: %v (run with -update to create)", err)
			}
			if string(out) != string(want) {
				t.Errorf("golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", out, want)
			}
		})
	}
}
```

- [ ] **Step 3: Generate goldens, then run with goldens locked**

```bash
mkdir -p tests/m1/golden
cd tools/text2bin
go test -run TestGoldenCorpus -update
cd ../..
g add tests/m1/golden
```

Inspect the generated files manually. They should be canonical-format versions of the sources — same constructs, normalised whitespace, comments preserved.

Then run without `-update`:

```bash
cd tools/text2bin && go test -run TestGoldenCorpus
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
g add tests/m1 tools/text2bin
g commit -m "m1: Layer 2 — round-trip golden corpus"
```

---

## Task 27: Layer 3 — hand-crafted `.tbn` reachability

**Files:**
- Create: `tests/m1/binary/binary_test.go`

For each record kind and each operand kind, build a `.tbn` via the writer API directly, run `Emit` then `Translate`, and assert byte-equality with the original. This catches encodable shapes that bin2text can produce but text2bin cannot.

- [ ] **Step 1: Write the failing test**

This test file lives inside the format module so it can use the writers directly:

`tools/sam-aarch64-format/reachability_test.go`:

```go
package format

import (
	"bytes"
	"testing"

	emit "github.com/petemoore/sam-aarch64/tools/bin2text/internal/emit"
	t2b "github.com/petemoore/sam-aarch64/tools/text2bin"
)

// requires replace directives in tools/sam-aarch64-format/go.mod for
// both bin2text/internal/emit and text2bin.

func handCraftedFiles() [][]byte {
	var out [][]byte

	// File 1: every register operand kind.
	{
		st := NewSymbolTable()
		var ow OperandWriter
		ow.WriteReg(OpRegX, 0)
		ow.WriteReg(OpRegW, 1)
		ow.WriteReg(OpRegXSP, 31)
		ow.WriteReg(OpRegWSP, 31)
		var rw RecordWriter
		id, _ := MnemonicID("mov")
		rw.WriteInst(id, 4, ow.Bytes())
		var buf bytes.Buffer
		WriteFile(&buf, st, rw.Bytes())
		out = append(out, buf.Bytes())
	}
	// File 2: every cond code.
	{
		st := NewSymbolTable()
		var rw RecordWriter
		id, _ := MnemonicID("csel")
		for c := byte(0); c < 16; c++ {
			var ow OperandWriter
			ow.WriteReg(OpRegX, 0)
			ow.WriteReg(OpRegX, 1)
			ow.WriteReg(OpRegX, 2)
			ow.WriteCond(CondCode(c))
			rw.WriteInst(id, 4, ow.Bytes())
		}
		var buf bytes.Buffer
		WriteFile(&buf, st, rw.Bytes())
		out = append(out, buf.Bytes())
	}
	// Files 3..N: extend with other operand kinds (memory shapes,
	// shifted, extended, strings, PC-rel ops, etc.).
	return out
}

func TestReachabilityRoundtrip(t *testing.T) {
	for i, bin := range handCraftedFiles() {
		canon, err := emit.Emit(bin)
		if err != nil {
			t.Errorf("file %d: emit %v", i, err)
			continue
		}
		bin2, err := t2b.Translate(canon, "synth.s")
		if err != nil {
			t.Errorf("file %d: translate %v", i, err)
			continue
		}
		if !bytes.Equal(bin, bin2) {
			t.Errorf("file %d not round-trippable:\n bin  = % X\n bin2 = % X\n canon = %s",
				i, bin, bin2, string(canon))
		}
	}
}
```

- [ ] **Step 2: Wire `Translate` for import**

`text2bin/main.go`'s `Translate` is in `package main`. Move it into `tools/text2bin/internal/translate/translate.go` (mirror the `bin2text` restructure) so the format module can import it. Update `text2bin/main.go`'s CLI accordingly.

- [ ] **Step 3: Run test to verify it passes for files 1 and 2; extend `handCraftedFiles` until every operand kind and expression opcode is covered**

Iterate: add a file shape, run, fix any text2bin or bin2text gap, repeat.

- [ ] **Step 4: Commit**

```bash
g add tools/sam-aarch64-format tools/text2bin tools/bin2text
g commit -m "m1: Layer 3 — hand-crafted .tbn reachability sweep"
```

---

## Task 28: Layer 4 — GNU `as` cross-check

**Files:**
- Create: `tests/m1/run-gnu-as-check.sh`

For every fixture under `tests/m1/sources/`, invoke `aarch64-none-elf-as -o /dev/null <fixture>`. Non-zero exit = failure.

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
AS="${AARCH64_AS:-aarch64-none-elf-as}"

if ! command -v "$AS" >/dev/null; then
    echo "missing $AS — install aarch64-none-elf-as or set AARCH64_AS"
    exit 2
fi

fail=0
for f in "$ROOT"/tests/m1/sources/*.s; do
    if ! "$AS" -o /dev/null "$f" 2>/dev/null; then
        echo "FAIL: GNU as rejected $f"
        "$AS" -o /dev/null "$f" || true
        fail=1
    fi
done

if [ "$fail" -ne 0 ]; then
    echo "GNU as cross-check failed"
    exit 1
fi

echo "GNU as cross-check passed for $(ls "$ROOT"/tests/m1/sources/*.s | wc -l) fixtures"
```

- [ ] **Step 2: Make executable and test**

```bash
chmod +x tests/m1/run-gnu-as-check.sh
./tests/m1/run-gnu-as-check.sh
```

Expected: passes for every fixture. If GNU `as` rejects a fixture, the dialect has drifted — fix the fixture (preferred) or, if the construct is genuinely valid aarch64 syntax that GNU `as` rejects only under certain flags, document the divergence.

- [ ] **Step 3: Commit**

```bash
g add tests/m1/run-gnu-as-check.sh
g commit -m "m1: Layer 4 — GNU as cross-check shell"
```

---

## Task 29: Makefile + CI integration

**Files:**
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Add Makefile targets**

Append to `Makefile`:

```makefile
.PHONY: text2bin bin2text test-m1 ci-m1

text2bin:
	cd tools/text2bin && go build -o $(CURDIR)/$(BUILD)/text2bin .

bin2text:
	cd tools/bin2text && go build -o $(CURDIR)/$(BUILD)/bin2text .

test-m1: text2bin bin2text
	cd tools/sam-aarch64-format && go test ./...
	cd tools/text2bin && go test ./...
	cd tools/bin2text && go test ./...
	./tests/m1/run-gnu-as-check.sh

ci-m1: test-m1
```

- [ ] **Step 2: Verify locally**

```bash
make ci-m1
```

Expected: PASS.

- [ ] **Step 3: Add CI step**

Edit `.github/workflows/ci.yml`. Identify the existing M0 job and add a parallel `m1` job (or a step to the existing job). Example minimal step:

```yaml
  m1:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26.1'
      - name: Install aarch64 binutils
        run: sudo apt-get update && sudo apt-get install -y binutils-aarch64-linux-gnu
      - name: Run M1 tests
        env:
          AARCH64_AS: aarch64-linux-gnu-as
        run: make ci-m1
```

- [ ] **Step 4: Commit and push**

```bash
g add Makefile .github/workflows/ci.yml
g commit -m "m1: Makefile + CI integration"
```

Wait for CI; fix any failures autonomously per Pete's PR-workflow rules.

---

## Task 30: M1 done — declare complete

**Files:**
- Modify: `README.md`
- Create: `docs/notes/m1-status.md`

- [ ] **Step 1: Update README status**

Edit the existing M0 status block in `README.md` to say:

```
## Status

M0 (toolchain bootstrap) complete; M1 (binary tokenised source format
+ text2bin/bin2text) complete. See `docs/specs/` for design and
`docs/plans/` for milestone plans. Next milestone: M2 (encoder tables).
```

- [ ] **Step 2: Add `docs/notes/m1-status.md`**

A handoff doc analogous to `m0-status.md` summarising: where the format spec lives, where the tools live, how to run the test suite, what's verified, what's known-incomplete (e.g., msr/mrs not yet in mnemonic table; placeholder for M2).

- [ ] **Step 3: Sanity check**

```bash
make ci-m1
```

Expected: green.

- [ ] **Step 4: Commit**

```bash
g add README.md docs/notes/m1-status.md
g commit -m "docs: M1 complete"
```

---

## What M1 explicitly does NOT include

- Any Z80 reader / parser / emitter (M3).
- Any aarch64 machine-code emission (M3).
- Encoder tables / form lookup (M2).
- `msr` / `mrs` mnemonics. `OpSysName` is reserved in the format but
  the parser path is not wired until a fixture demands it.
- Macros, conditional assembly, `.section` beyond `.text`/`.data`,
  multi-file `.include` (Phase 1 spec defers all of these).
- CRC / signature in the file header.

## Open items inherited from spec §9

1. Mnemonic-id table source of truth — M1 hand-curates. M2 picks the
   long-term mechanism (likely ARM MRA XML filtered).
2. Full set of PC-rel relocation operators — initial set in `expr.go`
   covers `:lo12:`/`:hi12:` and the `abs_g*` family. Grep `~/git/spectrum4`
   during M2 to confirm coverage.
3. `PUSH_IMMn` numeric-base hint — deferred to v2 (only added if an
   M3 fixture demands deterministic hex output).
4. SAM file type byte — proposed `0x07` (CODE) in the spec; M1 does
   not write to SAM disks so the value is not yet exercised.

---

## Self-review

This plan implements every requirement in the M1 spec:

- §1 goal/boundaries — Tasks 16, 23, 29, 30.
- §2 file container — Tasks 1, 14, 15.
- §3 record kinds — Tasks 2, 12, 13, 18, 21.
- §4 operand kinds — Tasks 3, 10, 11, 19, 20, 21, 24.
- §5 expression bytecode — Tasks 4, 8, 9, 19, 21, 24.
- §6 local labels & comments — Tasks 18, 23, 24.
- §7 text2bin / bin2text — Tasks 16–22 (text2bin), 23–24 (bin2text).
- §8 testing — Tasks 22, 25, 26, 27, 28.
- §9 open items — surfaced in the "Open items" section above.
- §10 done criteria — Tasks 26 (fixture corpus), 29 (CI), 30 (declare).




