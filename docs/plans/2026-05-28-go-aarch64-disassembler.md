# Go-side aarch64 disassembler — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go-side aarch64 disassembler that takes a 4-byte machine-code word and produces text equivalent to GNU `objdump -d -b binary -m aarch64` for the subset of instructions our project uses. Validated against binutils on the spectrum4 corpus. Becomes the oracle for the eventual Z80-side disassembler and unlocks the editor's "render the .tbn" feature for M6 strand B.

**Architecture:** Symmetric inverse of `tools/aarch64enc/`. Every Form in our encoder has a Pattern and Mask — a disassembler walks the form list, finds the one whose `(word & Mask) == Pattern`, then decodes each Slot's bits into human-readable operand text. The same form table that encodes also decodes; no duplicate data.

**Tech Stack:** Go 1.21+; binutils `aarch64-*-objdump` as the comparison oracle; `tools/sam-aarch64-format/` for shared types.

**Strand B sequencing (per Pete 2026-05-28):**
1. **This PR: Go disassembler** (form-table-driven; binutils oracle)
2. Round-trip test (`text2bin → refenc → disasm → text2bin → byte-match`)
3. Z80 port of disassembler
4. Compact `.tbn` (informed by what disassembler outputs)
5. Editor integration

---

## Why disassembler-first

Pete's framing (2026-05-28): "if we have that sitting in the wings, it will be easier to call into it when we have the presentation layer showing what is in the source. we will also want the roundtrip test that the instructions that appear in the editor match the original source we imported from spectrum4 project."

Concretely:
- Round-trip test requires disassembler — can't be implemented before this
- Compression decisions are informed by **what's displayed** — we'd be guessing without seeing output
- binutils as continuous oracle gives a reliable CI gate from day one
- The same form table powers both directions — no new data to design

## Why this can be lossless w.r.t. user input

Per the canonical-aliases survey (`docs/notes/2026-05-27-disassembly-canonicalisation-survey.md`):

- **0.37% mnemonic-changing** transformations vs binutils on the spectrum4 corpus
- **5 transformations are simplifications** (wins — e.g. `bfi → bfxil`)
- **27 are cosmetic synonyms** (`b.hs ↔ b.cs`, `str → stur` for negative offsets) — we adopt binutils' choice
- **1 was jarring** (`mov Xn, Xn, lsl #n` → `orr Xn, xzr, Xn, lsl #n`) — patched away in spectrum4 source

Saving "align with binutils canonical forms" lives in `feedback_align_with_binutils.md`. The disassembler matches objdump's choices exactly.

## File structure

- **Create**: `tools/aarch64dec/` — the disassembler module
  - `tools/aarch64dec/disasm.go` — `Decode(word uint32) (mnem string, operands string, ok bool)`
  - `tools/aarch64dec/operands.go` — per-SlotKind decoder helpers
  - `tools/aarch64dec/aliases.go` — alias recognition (`orr xN, xzr, xM` → `mov xN, xM` etc.)
  - `tools/aarch64dec/disasm_test.go` — unit tests
- **Create**: `tools/aarch64dec/cmd/aarch64dec/main.go` — CLI tool: reads bytes from stdin or file, emits one line per instruction
- **Create**: `tests/disasm/run-oracle-comparison.sh` — runs our disasm vs objdump on the spectrum4 release.bin, diffs
- **Create**: `tests/disasm/sources/` — small fixtures exercising each Form / SlotKind
- **Modify**: `.github/workflows/ci.yml` — add `disasm` job that runs `tests/disasm/run-oracle-comparison.sh`
- **Modify**: `Makefile` — `make aarch64dec`, `make ci-disasm`
- **Modify**: `docs/ROADMAP.md` — flip strand B to "disassembler in progress"

---

### Task 1: Skeleton + core form-matching

**Files:**
- Create: `tools/aarch64dec/disasm.go`
- Create: `tools/aarch64dec/disasm_test.go`

- [ ] **Step 1: API**

```go
package aarch64dec

import "github.com/petemoore/sam-aarch64/tools/aarch64enc"

// Decode walks the form list and returns the first match.  ok=false
// if no form matches (caller can fall back to ".inst 0xNNNNNNNN").
func Decode(word uint32) (mnem string, operands string, ok bool) {
    for _, f := range aarch64enc.AllForms() {
        if word & f.Mask == f.Pattern {
            return decodeForm(word, f)
        }
    }
    return "", "", false
}
```

`aarch64enc.AllForms()` should already exist or be trivially addable; check `tools/aarch64enc/forms.go`.

- [ ] **Step 2: Per-form decoding**

```go
func decodeForm(word uint32, f aarch64enc.Form) (mnem string, operands string, ok bool) {
    mnem = aarch64enc.MnemonicName(f.MnemonicID)
    var ops []string
    for _, slot := range f.Slots {
        bits := (word >> slot.BitPosition) & ((1 << slot.BitWidth) - 1)
        ops = append(ops, decodeSlot(slot, bits))
    }
    return mnem, strings.Join(ops, ", "), true
}
```

- [ ] **Step 3: Initial SlotKind decoders**

Cover the kinds used by the simplest instructions first — `Xreg`, `Wreg`, `XregOrSp`, `WregOrSp`, `Imm12Shifted`, `CondCode`. One function per kind:

```go
func decodeSlot(slot aarch64enc.OperandSlot, bits uint32) string {
    switch slot.SlotKind {
    case aarch64enc.SlotXreg:
        if bits == 31 { return "xzr" }
        return fmt.Sprintf("x%d", bits)
    case aarch64enc.SlotXregOrSp:
        if bits == 31 { return "sp" }
        return fmt.Sprintf("x%d", bits)
    // ... etc.
    default:
        panic(fmt.Sprintf("unhandled slot kind: %v", slot.SlotKind))
    }
}
```

- [ ] **Step 4: Tests**

For each existing M3..M6 fixture, encode → decode → compare against the source line. Start with `tests/m3/sources/inst_alu_single.s`. Should round-trip cleanly for simple forms.

- [ ] **Step 5: Commit**

```
g add tools/aarch64dec/
g commit -m "aarch64dec: skeleton + core SlotKind decoders"
```

---

### Task 2: All remaining SlotKinds

**Files:**
- Modify: `tools/aarch64dec/operands.go` (expand with all remaining kinds)

Enumerate every SlotKind in `tools/sam-aarch64-format/kinds.go` (or wherever the enum lives). For each, write a decoder.

Tricky kinds to handle carefully:
- `Imm12Shifted` — `imm12 << shift` where `shift` is 0 or 12
- `Imm16Shifted` (movz/movk family) — output `#imm, lsl #N` format
- `LogicalImm` — N:imms:immr decoded to bitmask, then formatted as hex `#0x...`
- `BitfieldImm` — immr/imms with mnemonic-specific computation (bfm/sbfm/ubfm)
- `Imm5` — context-dependent: unsigned for ccmp, signed for stur, etc. Let each Form annotate signedness if needed, OR always emit decimal and let the caller decide
- `MemOffsetScaled` (str/ldr scaled) — `[Xn, #imm]`, `[Xn]` if zero, etc.
- `MemPostIndex` / `MemPreIndex` — `[Xn], #imm` / `[Xn, #imm]!`
- `BranchImm19` / `BranchImm26` — signed PC-relative offset; for now emit `0xNNNNNNNN` absolute address (caller can post-process to symbol)
- `CondCode` — 4-bit → "eq", "ne", "cs", "cc", "mi", ..., "al", "nv"

Reference: ARMv8 ARM C-section per-instruction encoding rules. Where ambiguity exists, defer to objdump's choice.

- [ ] **Step 1: Implement all kinds**

One commit per ~5 kinds is fine. Each kind has a unit test.

- [ ] **Step 2: Validate against existing fixtures**

For every `tests/m{3,4,5,6}/sources/*.s` fixture, encode via `aarch64enc` then disassemble. Compare textual output against the source (modulo whitespace).

---

### Task 3: Alias recognition

**Files:**
- Create: `tools/aarch64dec/aliases.go`

Per the canonical-aliases survey, these aliases should be emitted preferentially:

| Underlying | Alias | Condition |
|---|---|---|
| `orr Xd, xzr, Xm` | `mov Xd, Xm` | unshifted |
| `subs xzr, Xn, Xm` | `cmp Xn, Xm` | dest is xzr |
| `subs xzr, Xn, #imm` | `cmp Xn, #imm` | dest is xzr |
| `ands xzr, Xn, Xm` | `tst Xn, Xm` | dest is xzr |
| `csinc Xd, Xn, Xn, INVCOND` | `cinc Xd, Xn, COND` | both source regs equal + condition invertable |
| `csinv Xd, Xn, Xn, INVCOND` | `cinv Xd, Xn, COND` | same |
| `csneg Xd, Xn, Xn, INVCOND` | `cneg Xd, Xn, COND` | same |
| `hint #0` | `nop` | imm is 0 |
| `hint #1` | `yield` | |
| `hint #2` | `wfe` | |
| `hint #3` | `wfi` | |
| `hint #4` | `sev` | |
| `hint #5` | `sevl` | |
| `ubfm Xd, Xn, #shift, #63` | `lsr Xd, Xn, #shift` | imms = 63 (or 31 for W) |
| `ubfm Xd, Xn, #..., #...` | `lsl Xd, Xn, #N` | specific bit combo |
| `ubfm Xd, Xn, #lsb, #lsb+width-1` | `ubfx Xd, Xn, #lsb, #width` | bfxil/etc. |
| `sbfm` | `asr`, `sbfx`, `sxtb`, `sxth`, `sxtw` | various |
| `bfm` | `bfi`, `bfxil` | per survey |

Discovery: extract this list mechanically by feeding 4-byte words to `objdump -d -b binary -m aarch64` and `objdump -d -b binary -m aarch64 -M no-aliases` — every difference is an alias rule we need.

- [ ] **Step 1: Build the alias table mechanically**

Write `tests/disasm/cmd/discover-aliases/main.go` that brute-forces a small set of "interesting" 32-bit words (every instruction we have a Form for, plus their alias-triggering variants), runs both objdump invocations, diffs, and emits a markdown table.

- [ ] **Step 2: Implement the alias rules**

```go
// AliasFor returns (mnem, operands, true) if word matches a canonical alias.
// Caller should try AliasFor before falling back to direct Form decoding.
func AliasFor(word uint32, f aarch64enc.Form) (string, string, bool) {
    // Per ALIAS_TABLE entries; first match wins.
}
```

Hook into `Decode`: try `AliasFor` first; if not, fall back to direct Form decode.

- [ ] **Step 3: Validate**

Re-run all M3..M6 fixtures through disassembler. They should output the same canonical forms as objdump (which is what spectrum4 source already uses since we patched the jarring case).

---

### Task 4: CLI tool

**Files:**
- Create: `tools/aarch64dec/cmd/aarch64dec/main.go`
- Modify: `Makefile`

- [ ] **Step 1: CLI surface**

```
aarch64dec [-base N] FILE.bin
```

Reads `FILE.bin` as raw aarch64 machine code (4 bytes per instruction, little-endian). Emits one line per instruction:

```
<addr-hex>: <bytes-hex>   <mnem>  <operands>
```

`-base N` sets the address shown in the first column (default 0).

Format mirrors `objdump -d -b binary -m aarch64` so diffs are mechanical.

- [ ] **Step 2: Makefile target**

```
.PHONY: aarch64dec
aarch64dec: $(BUILD)/aarch64dec

$(BUILD)/aarch64dec: $(shell find tools/aarch64dec -name '*.go') $(shell find tools/aarch64enc -name '*.go')
	cd tools/aarch64dec/cmd/aarch64dec && go build -o $(CURDIR)/$@ .
```

- [ ] **Step 3: Commit**

---

### Task 5: Oracle comparison test against spectrum4 corpus

**Files:**
- Create: `tests/disasm/run-oracle-comparison.sh`
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Test driver**

```bash
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

make -s aarch64dec

# Build release.bin via spectrum4's own pipeline (Pete has a script; check
# scripts/build-spectrum4-release.sh).
RELEASE_BIN="${RELEASE_BIN:-/Users/pmoore/git/spectrum4/src/spectrum4/targets/release.img}"
if [ ! -f "$RELEASE_BIN" ]; then
    echo "ERROR: release.img not found at $RELEASE_BIN" >&2
    exit 2
fi

# Toolchain
OBJDUMP="${AARCH64_OBJDUMP:-aarch64-linux-gnu-objdump}"

# Run both
"$OBJDUMP" -d -b binary -m aarch64 "$RELEASE_BIN" \
    | grep -E '^[[:space:]]+[0-9a-f]+:' \
    | sed -E 's/^ +//' \
    > /tmp/objdump.txt

./build/aarch64dec "$RELEASE_BIN" > /tmp/ours.txt

# Diff with reasonable whitespace tolerance
if diff -u /tmp/objdump.txt /tmp/ours.txt; then
    echo "PASS: disasm matches objdump byte-for-byte on $RELEASE_BIN"
else
    echo "FAIL: disasm differs from objdump" >&2
    exit 1
fi
```

- [ ] **Step 2: CI job**

Add to `.github/workflows/ci.yml`:

```yaml
disasm:
  needs: build-image
  runs-on: ubuntu-latest
  container: ghcr.io/petemoore/sam-aarch64-dev:latest
  steps:
    - uses: actions/checkout@v4
    - run: make ci-disasm

# in Makefile:
ci-disasm:
	./tests/disasm/run-oracle-comparison.sh
```

- [ ] **Step 3: First run**

The comparison will likely fail initially — list the diffs, classify them (real bugs in our decoder vs. presentation differences like base address formatting), iterate until clean.

---

### Task 6: Documentation + PR

- [ ] **Step 1: Update strand-B-related docs**

In `docs/ROADMAP.md`, update M6 strand B's row to reflect the sequencing decided 2026-05-28:

```
| M6 strand B | Disassembler (this PR) → round-trip test → compact .tbn → Z80 disassembler | ⏳ in progress |
```

- [ ] **Step 2: Open draft PR**

```
g push -u origin <branch>
gh pr create --draft --title "tools/aarch64dec: Go-side aarch64 disassembler (M6 strand B-1)" --body "..."
```

PR body should reference:
- The strand-B sequencing decision
- The canonical-aliases survey (validates 0.37% jarring rate)
- The new CI gate (disasm vs objdump on release.bin)

---

## Out of scope (queued for follow-up PRs)

- **Round-trip test**: `text2bin → refenc → disasm → text2bin → byte-match-vs-original`. Needs the disassembler done first. Will land as M6 strand B PR 2.
- **Symbol/label reconstruction**: A real disassembler emits `b some_label` instead of `b 0xfff...`. Defer until we have label-name input (M6 strand B PR 3).
- **Z80 port**: Once Go disassembler stabilises, port to Z80. Follows naturally — same form table.
- **Compact `.tbn` format**: Strand B PR 4+. Defer until we see what the disassembler outputs in the editor (informs compression decisions).
- **Header dedup at flatten time** (Pete's 2026-05-28 observation): spectrum4 copyright header appears in every included file's comments — dedupe at text2bin's flatten layer. Small companion PR within strand B.
- **Comment compression**: deferred per Pete's 2026-05-28 framing — preserved structure intact; compressed lazily-decompressed.

## What "done" looks like for this plan

- `tools/aarch64dec/` exists, with form-table-driven decoding + alias support
- `make ci-disasm` runs in CI and passes against the spectrum4 release.bin
- Strand B PR-1 merged to main
- ROADMAP reflects M6 strand B in progress
- Next session can immediately start on the round-trip test (PR 2)
