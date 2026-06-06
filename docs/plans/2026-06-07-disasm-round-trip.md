# Disassembler round-trip — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement strand-B PR-2: add `-asm` output mode to `aarch64dec` (with synthetic branch labels), a round-trip shell script, and a new `disasm-roundtrip` CI gate.

**Architecture:** Two-pass label generation in the `aarch64dec` Go package (`BranchTarget` + `WriteAsm`); a new `-asm` CLI flag threads through to `WriteAsm`; `tools/run-disasm-roundtrip.sh` drives the full pipeline; a new GHA job gates CI. No SimCoupé or aarch64 binutils required — pure Go pipeline.

**Spec:** `docs/specs/2026-06-07-disasm-round-trip-design.md`

**Tech Stack:** Go 1.26.1; existing `tools/aarch64dec/`, `tools/text2bin/`, `tools/refenc/`; Bash; GitHub Actions.

**Commit discipline:** Use `g` not `git`. Do NOT add Co-Authored-By trailers unless Pete asked.

---

## File map

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `tools/aarch64dec/branchtarget.go` | `BranchTarget(pc, word) (uint64, bool)` — detect and extract direct branch targets |
| Create | `tools/aarch64dec/branchtarget_test.go` | Unit tests for `BranchTarget` |
| Create | `tools/aarch64dec/asm.go` | `WriteAsm(w, base, data)` — two-pass label-annotated output |
| Create | `tools/aarch64dec/asm_test.go` | Unit tests for `WriteAsm` |
| Modify | `tools/aarch64dec/cmd/aarch64dec/main.go` | Add `-asm` flag; call `WriteAsm` |
| Create | `tools/run-disasm-roundtrip.sh` | End-to-end pipeline script |
| Modify | `Makefile` | `ci-disasm-roundtrip` target |
| Modify | `.github/workflows/ci.yml` | `disasm-roundtrip` job |

---

## Task 0 — Create the feature branch

- [ ] **Step 1: Create and switch to the feature branch**

```bash
cd /home/pmoore/git/sam-aarch64
g checkout -b disasm-round-trip
```

All subsequent commits go on this branch; Task 6 pushes and opens the PR.

---

## Task 1 — `BranchTarget`: detect direct branch target addresses

**Files:**
- Create: `tools/aarch64dec/branchtarget.go`
- Create: `tools/aarch64dec/branchtarget_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/aarch64dec/branchtarget_test.go`:

```go
package aarch64dec

import "testing"

func TestBranchTarget(t *testing.T) {
	tests := []struct {
		name   string
		pc     uint64
		word   uint32
		target uint64
		ok     bool
	}{
		// b 0x10 at pc=0: imm26=4 → byteOffset=16=0x10
		{"b 0x10 at pc=0", 0, 0x14000004, 0x10, true},
		// bl 0x8 at pc=0: imm26=2 → byteOffset=8
		{"bl 0x8 at pc=0", 0, 0x94000002, 0x8, true},
		// b.ne 0x10 at pc=0: BranchImm19 imm19=4 → byteOffset=16
		// Encoding: bits[31:24]=0x54, bit[3:0]=cond(ne=1), imm19 at bits[23:5]
		// 0x54000081: bits[31:24]=0x54, imm19=4(at bits[23:5]=0x80>>1=4), Rt=1(ne)
		{"b.ne 0x10 at pc=0", 0, 0x54000081, 0x10, true},
		// cbz w0, 0x10 at pc=0: BranchImm19 imm19=4
		// CBZ w: bits[31:24]=0x34, imm19 at bits[23:5], Rt=0
		{"cbz w0 0x10 at pc=0", 0, 0x34000080, 0x10, true},
		// tbnz w10, #31, 0x310 at pc=0x314: verified from tbranch.go comment
		{"tbnz w10 #31 0x310 at pc=0x314", 0x314, 0x37ffffea, 0x310, true},
		// nop — not a branch
		{"nop", 0, 0xd503201f, 0, false},
		// ret — not a direct branch (it's a register branch, no immediate target)
		{"ret", 0, 0xd65f03c0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := BranchTarget(tt.pc, tt.word)
			if ok != tt.ok {
				t.Errorf("ok: got %v want %v", ok, tt.ok)
				return
			}
			if ok && got != tt.target {
				t.Errorf("target: got %#x want %#x", got, tt.target)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests — expect compilation failure (BranchTarget undefined)**

```bash
cd /home/pmoore/git/sam-aarch64/tools/aarch64dec && go test ./...
```

Expected: compile error — `BranchTarget` undefined.

- [ ] **Step 3: Implement `BranchTarget`**

Create `tools/aarch64dec/branchtarget.go`:

```go
package aarch64dec

import "github.com/petemoore/sam-aarch64/tools/aarch64enc"

// BranchTarget returns the target address for a direct branch at pc,
// or (0, false) for any other instruction.  Covers b, bl, b.<cond>,
// cbz, cbnz, tbz, tbnz.  Does NOT cover indirect branches (blr, br,
// ret) or adrp/adr — those have no fixed compile-time target.
func BranchTarget(pc uint64, word uint32) (uint64, bool) {
	// tbz / tbnz: bits[30:25] == 0b011011 (not in AllForms; hand-rolled
	// in the encoder — same pattern check as decodeTestBranch in tbranch.go).
	if (word>>25)&0x3f == 0b011011 {
		imm14 := (word >> 5) & 0x3fff
		off := signExtend(imm14, 14) << 2
		return pc + uint64(off), true
	}
	// Walk AllForms for entries that carry BranchImm26/19/14 slots.
	// Mirror the arithmetic in decodeBranchImm (slots_branch.go).
	for _, f := range aarch64enc.AllForms() {
		if word&f.Mask != f.Pattern {
			continue
		}
		for _, slot := range f.Slots {
			switch slot.SlotKind {
			case aarch64enc.BranchImm26, aarch64enc.BranchImm19, aarch64enc.BranchImm14:
				bits := extractBits(word, slot.BitPosition, slot.BitWidth)
				instrOff := signExtend32(bits, slot.BitWidth)
				byteOff := int64(instrOff) * 4
				return pc + uint64(byteOff), true
			}
		}
	}
	return 0, false
}
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
cd /home/pmoore/git/sam-aarch64/tools/aarch64dec && go test ./...
```

Expected: `ok  github.com/petemoore/sam-aarch64/tools/aarch64dec`

- [ ] **Step 5: Commit**

```bash
g add tools/aarch64dec/branchtarget.go tools/aarch64dec/branchtarget_test.go
g commit -m "aarch64dec: add BranchTarget for direct-branch target extraction"
```

---

## Task 2 — `WriteAsm`: two-pass labeled assembly output

**Files:**
- Create: `tools/aarch64dec/asm.go`
- Create: `tools/aarch64dec/asm_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/aarch64dec/asm_test.go`:

```go
package aarch64dec

import (
	"strings"
	"testing"
)

func TestWriteAsm_labels(t *testing.T) {
	// nop at 0x0, b 0x8 at 0x4 (branch to ret), ret at 0x8.
	// b 0x8 at pc=0x4: offset=+1 instr (+4 bytes), imm26=1 → 0x14000001.
	data := []byte{
		0x1f, 0x20, 0x03, 0xd5, // nop  at 0x0
		0x01, 0x00, 0x00, 0x14, // b 0x8 at 0x4
		0xc0, 0x03, 0x5f, 0xd6, // ret  at 0x8
	}
	var buf strings.Builder
	if err := WriteAsm(&buf, 0, data); err != nil {
		t.Fatal(err)
	}
	want := "\t.text\n\tnop\n\tb\tL0\nL0:\n\tret\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteAsm:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestWriteAsm_no_branches(t *testing.T) {
	// nop, ret — no branches, so no labels.
	data := []byte{
		0x1f, 0x20, 0x03, 0xd5, // nop
		0xc0, 0x03, 0x5f, 0xd6, // ret
	}
	var buf strings.Builder
	if err := WriteAsm(&buf, 0, data); err != nil {
		t.Fatal(err)
	}
	want := "\t.text\n\tnop\n\tret\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteAsm:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestWriteAsm_declined_word(t *testing.T) {
	// A word aarch64dec declines renders as .inst 0xNNNNNNNN.
	// Use an atomic instruction (LDXR family) which is in the declined set.
	// 0x885f7c00 = ldxr w0, [x0] — atomics are declined.
	data := []byte{0x00, 0x7c, 0x5f, 0x88}
	var buf strings.Builder
	if err := WriteAsm(&buf, 0, data); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, ".inst") {
		t.Errorf("expected .inst for declined word, got: %q", got)
	}
}
```

- [ ] **Step 2: Run tests — expect compilation failure (WriteAsm undefined)**

```bash
cd /home/pmoore/git/sam-aarch64/tools/aarch64dec && go test ./...
```

Expected: compile error — `WriteAsm` undefined.

- [ ] **Step 3: Implement `WriteAsm`**

Create `tools/aarch64dec/asm.go`:

```go
package aarch64dec

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strings"
)

// WriteAsm writes a labeled assembly listing of data to w.  The output
// is valid input for text2bin: one instruction per line, tab-indented,
// with synthetic labels L0/L1/… placed at every direct branch target
// within the binary.  Branch operands that resolve to a label are
// replaced with the label name so the source is safe to edit — inserting
// an instruction updates all dependent branches automatically.
//
// PC-relative non-branch operands (adrp, adr) are left as absolute hex;
// pair detection for adrp/add(:lo12:) is deferred.
//
// Words that cannot be decoded render as `.inst 0xNNNNNNNN`, which is a
// valid text2bin directive.
func WriteAsm(w io.Writer, base uint64, data []byte) error {
	// Pass 1: collect direct branch targets that land within the binary.
	type void struct{}
	targetSet := map[uint64]void{}
	for i := 0; i < len(data); i += 4 {
		pc := base + uint64(i)
		word := binary.LittleEndian.Uint32(data[i : i+4])
		if tgt, ok := BranchTarget(pc, word); ok {
			if tgt >= base && tgt < base+uint64(len(data)) {
				targetSet[tgt] = void{}
			}
		}
	}

	// Build a sorted label map: ascending address order → L0, L1, …
	addrs := make([]uint64, 0, len(targetSet))
	for a := range targetSet {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i] < addrs[j] })
	labelOf := make(map[uint64]string, len(addrs))
	for i, a := range addrs {
		labelOf[a] = fmt.Sprintf("L%d", i)
	}

	// Emit section header.
	if _, err := fmt.Fprintln(w, "\t.text"); err != nil {
		return err
	}

	// Pass 2: emit instructions, inserting label definitions and
	// replacing branch-target hex addresses with label names.
	for i := 0; i < len(data); i += 4 {
		pc := base + uint64(i)
		word := binary.LittleEndian.Uint32(data[i : i+4])

		if label, ok := labelOf[pc]; ok {
			if _, err := fmt.Fprintf(w, "%s:\n", label); err != nil {
				return err
			}
		}

		mnem, ops, ok := DecodeAt(pc, word)
		var line string
		if ok {
			if tgt, hasTgt := BranchTarget(pc, word); hasTgt {
				if label, hasLabel := labelOf[tgt]; hasLabel {
					ops = strings.ReplaceAll(ops, fmt.Sprintf("%#x", tgt), label)
				}
			}
			line = Format(mnem, ops)
		} else {
			line = fmt.Sprintf(".inst\t%#08x", word)
		}
		if _, err := fmt.Fprintf(w, "\t%s\n", line); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
cd /home/pmoore/git/sam-aarch64/tools/aarch64dec && go test ./...
```

Expected: `ok  github.com/petemoore/sam-aarch64/tools/aarch64dec`

- [ ] **Step 5: Commit**

```bash
g add tools/aarch64dec/asm.go tools/aarch64dec/asm_test.go
g commit -m "aarch64dec: add WriteAsm for labeled two-pass assembly output"
```

---

## Task 3 — `-asm` flag in the CLI

**Files:**
- Modify: `tools/aarch64dec/cmd/aarch64dec/main.go`

- [ ] **Step 1: Add the flag and wire up `WriteAsm`**

Replace the current `main.go` with the version below.  The only additions are:
- `var asmMode bool` flag
- a branch in `main` that calls `WriteAsm` instead of `disasmTo` when `-asm` is set
- updated usage string

```go
// aarch64dec disassembles a raw aarch64 binary, emitting one line per
// 4-byte instruction.  Output mirrors `aarch64-elf-objdump -D -b
// binary -m aarch64` line-for-line so that diffs against the oracle
// are mechanical.
//
//	aarch64dec [-base N] [-asm] FILE.bin
//
//	-base N   address of the first instruction (default 0)
//	-asm      emit labeled assembly instead of objdump-format output.
//	          Branch targets within the binary are replaced with
//	          synthetic labels L0/L1/… — safe for editor import and
//	          text2bin re-assembly.
//
// FILE.bin must be a multiple of 4 bytes; trailing partial words are
// rejected.  Words that no Form matches render as `.inst 0xNNNNNNNN`.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/petemoore/sam-aarch64/tools/aarch64dec"
)

func main() {
	var base uint64
	var asmMode bool
	flag.Uint64Var(&base, "base", 0,
		"byte address of the first instruction (default 0)")
	flag.BoolVar(&asmMode, "asm", false,
		"emit labeled assembly (text2bin-compatible) instead of objdump format")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: aarch64dec [-base N] [-asm] FILE.bin")
		os.Exit(2)
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fail(err)
	}
	if len(data)%4 != 0 {
		fail(fmt.Errorf("input length %d is not a multiple of 4", len(data)))
	}
	if asmMode {
		if err := aarch64dec.WriteAsm(os.Stdout, base, data); err != nil {
			fail(err)
		}
		return
	}
	if err := disasmTo(os.Stdout, base, data); err != nil {
		fail(err)
	}
}

// disasmTo writes one line per 4-byte word to w, formatted to match
// objdump's `<addr>:\t<word>\t<mnem>\t<operands>` layout.  The
// address column uses a minimum width of 4 hex chars and grows for
// larger binaries (mirroring objdump).
func disasmTo(w io.Writer, base uint64, data []byte) error {
	addrWidth := addrFieldWidth(base + uint64(len(data)) - 4)
	for i := 0; i < len(data); i += 4 {
		pc := base + uint64(i)
		word := binary.LittleEndian.Uint32(data[i : i+4])
		mnem, ops, ok := aarch64dec.DecodeAt(pc, word)
		var line string
		if ok {
			line = aarch64dec.Format(mnem, ops)
		} else {
			line = fmt.Sprintf(".inst\t%#08x", word)
		}
		if _, err := fmt.Fprintf(w, "%*x:\t%08x \t%s\n",
			addrWidth, pc, word, line); err != nil {
			return err
		}
	}
	return nil
}

// addrFieldWidth returns the right-justification width for the
// address column.  Matches objdump's behaviour: 4 chars minimum,
// growing one char at a time as the maximum address overflows.
func addrFieldWidth(maxAddr uint64) int {
	width := 4
	for (uint64(1) << (uint64(width) * 4)) <= maxAddr {
		width++
	}
	return width
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
```

- [ ] **Step 2: Build and smoke-test the binary**

```bash
cd /home/pmoore/git/sam-aarch64 && make aarch64dec
printf '\x1f\x20\x03\xd5\x01\x00\x00\x14\xc0\x03\x5f\xd6' > /tmp/smoke.bin
build/aarch64dec /tmp/smoke.bin
build/aarch64dec -asm /tmp/smoke.bin
```

Expected objdump output:
```
   0:	d503201f 	nop
   4:	14000001 	b	0x8
   8:	d65f03c0 	ret
```

Expected `-asm` output:
```
	.text
	nop
	b	L0
L0:
	ret
```

- [ ] **Step 3: Run all disassembler tests (including oracle)**

```bash
make test-disasm
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
g add tools/aarch64dec/cmd/aarch64dec/main.go
g commit -m "aarch64dec: add -asm flag for labeled assembly output"
```

---

## Task 4 — `tools/run-disasm-roundtrip.sh`

**Files:**
- Create: `tools/run-disasm-roundtrip.sh`

- [ ] **Step 1: Create the script**

```bash
#!/usr/bin/env bash
# tools/run-disasm-roundtrip.sh — strand-B PR-2 round-trip gate.
#
# Proves that the Go encoder (refenc) and decoder (aarch64dec) are true
# inverses on the spectrum4 release:
#
#   code-only source → text2bin → refenc → v1.bin
#   v1.bin → aarch64dec -asm → disasm.s → text2bin → refenc → v2.bin
#   assert v1.bin == v2.bin
#
# Pure Go pipeline — no aarch64 binutils or SimCoupé required.
# The code-only stripping is identical to tests/disasm/run-oracle-comparison.sh.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

RELEASE_S="${RELEASE_S:-tests/m6/release/release.s}"
[ -f "$RELEASE_S" ] || {
    echo "ERROR: release source not found at $RELEASE_S" >&2
    echo "       Run tools/revendor-m6-release.sh to regenerate it." >&2
    exit 2
}

echo "=== [1/6] Build tools ==="
make -s text2bin refenc aarch64dec

mkdir -p build
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

echo "=== [2/6] Strip data directives → code-only source ==="
grep -vE '^[[:space:]]*\.(hword|byte|asciz|ascii|string|quad|word|2byte|4byte|8byte|double|float|fill|zero|space|skip|octa)([[:space:]]|$)' \
    "$RELEASE_S" \
  | grep -vE '^[[:space:]]*ldr[[:space:]]+[wx][0-9]+[[:space:]]*,[[:space:]]*=' \
    > "$tmp/code-only.s"
echo "    code-only: $(wc -l < "$tmp/code-only.s" | tr -d ' ') lines"

echo "=== [3/6] text2bin + refenc → v1.bin ==="
"$ROOT/build/text2bin" -o "$tmp/code-only.tbn" "$tmp/code-only.s"
"$ROOT/build/refenc"   -o "$tmp/v1.bin"        "$tmp/code-only.tbn"
v1_sz=$(wc -c < "$tmp/v1.bin" | tr -d ' ')
echo "    v1.bin: $v1_sz bytes ($(( v1_sz / 4 )) instructions)"

echo "=== [4/6] aarch64dec -asm → disasm.s ==="
"$ROOT/build/aarch64dec" -asm "$tmp/v1.bin" > "$tmp/disasm.s"
echo "    disasm.s: $(wc -l < "$tmp/disasm.s" | tr -d ' ') lines"

echo "=== [5/6] text2bin + refenc → v2.bin ==="
"$ROOT/build/text2bin" -o "$tmp/disasm.tbn" "$tmp/disasm.s"
"$ROOT/build/refenc"   -o "$tmp/v2.bin"     "$tmp/disasm.tbn"
v2_sz=$(wc -c < "$tmp/v2.bin" | tr -d ' ')
echo "    v2.bin: $v2_sz bytes"

echo "=== [6/6] Compare v1 == v2 ==="
if cmp -s "$tmp/v1.bin" "$tmp/v2.bin"; then
    echo "PASS: round-trip OK — encode→decode→encode is self-consistent"
    echo "      ($v1_sz bytes, $(( v1_sz / 4 )) instructions)"
    exit 0
fi

echo "FAIL: v1.bin != v2.bin" >&2
printf "    v1: %s bytes\n    v2: %s bytes\n" "$v1_sz" "$v2_sz" >&2
first_diff=$(cmp -l "$tmp/v1.bin" "$tmp/v2.bin" 2>/dev/null | head -1 | awk '{print $1}') || true
if [ -n "$first_diff" ]; then
    byte_off=$(( (first_diff - 1) & ~3 ))
    instr_idx=$(( byte_off / 4 ))
    v1_word=$(od -j "$byte_off" -N4 -An -t x4 "$tmp/v1.bin" | tr -d ' \n')
    v2_word=$(od -j "$byte_off" -N4 -An -t x4 "$tmp/v2.bin" | tr -d ' \n')
    echo "    first diff at byte offset $byte_off (instruction $instr_idx):" >&2
    echo "      v1 word: 0x$v1_word" >&2
    echo "      v2 word: 0x$v2_word" >&2
fi
exit 1
```

- [ ] **Step 2: Make it executable**

```bash
chmod +x tools/run-disasm-roundtrip.sh
```

- [ ] **Step 3: Run it locally and verify PASS**

```bash
./tools/run-disasm-roundtrip.sh
```

Expected: `PASS: round-trip OK — encode→decode→encode is self-consistent`

**If it FAILs** — diagnose before continuing:
- If `text2bin` rejects `disasm.s` over the `.text` header: remove the `fmt.Fprintln(w, "\t.text")` line from `WriteAsm` in `asm.go`, re-run `make aarch64dec`, retry.
- If sizes differ (v1 ≠ v2): compare with `diff <(od -c /tmp/v1 ...) <(od -c /tmp/v2 ...)` to find the diverging instruction. The `first diff` message in the failure output gives the byte offset and both word values.
- Do NOT proceed to CI wiring until the local PASS is confirmed.

- [ ] **Step 4: Commit**

```bash
g add tools/run-disasm-roundtrip.sh
g commit -m "tools: add run-disasm-roundtrip.sh (strand-B PR-2 gate)"
```

---

## Task 5 — Makefile target + CI job

**Files:**
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Add `ci-disasm-roundtrip` to the Makefile**

Locate the `ci-disasm` block (around line 43) and add the new target immediately after it:

```make
# Round-trip gate: encode(code-only) → decode → re-encode, assert byte-match.
# Pure Go pipeline — no aarch64 binutils required.  See
# docs/specs/2026-06-07-disasm-round-trip-design.md.
.PHONY: ci-disasm-roundtrip
ci-disasm-roundtrip:
	./tools/run-disasm-roundtrip.sh
```

- [ ] **Step 2: Verify the target works**

```bash
make ci-disasm-roundtrip
```

Expected: `PASS: round-trip OK …`

- [ ] **Step 3: Add the `disasm-roundtrip` GHA job**

In `.github/workflows/ci.yml`, add the new job immediately after the `disasm:` job block.  The existing `disasm` job ends after `run: make ci-disasm`; insert the following after it:

```yaml
  # disasm-roundtrip — strand-B PR-2: encode→decode→encode byte-match.
  # Proves refenc and aarch64dec are true inverses on the code-only release.
  # Pure Go pipeline: no aarch64 binutils or SimCoupé required.
  disasm-roundtrip:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26.1'
      - name: Round-trip gate (encode→decode→encode)
        run: make ci-disasm-roundtrip
```

- [ ] **Step 4: Commit**

```bash
g add Makefile .github/workflows/ci.yml
g commit -m "ci: add disasm-roundtrip gate (strand-B PR-2)"
```

---

## Task 6 — Open the PR and add `disasm-roundtrip` as a required check

- [ ] **Step 1: Push the branch and open the PR**

```bash
g push -u origin disasm-round-trip
gh pr create \
  --title "aarch64dec: add -asm flag + round-trip gate (strand-B PR-2)" \
  --body "$(cat <<'EOF'
Implements strand-B PR-2 from the disassembler plan.

**What this adds:**

- `aarch64dec -asm FILE.bin` — labeled assembly output. Branch targets within the binary get synthetic labels `L0`, `L1`, … in ascending address order; branch operands are replaced with label names. Safe for editor import: inserting an instruction doesn't break branches. `adrp`/`add` pair detection is a tracked follow-up.
- `tools/run-disasm-roundtrip.sh` — end-to-end pipeline: `code-only.s → text2bin → refenc → v1 → aarch64dec -asm → text2bin → refenc → v2`, asserts `v1==v2`. Pure Go — no binutils required.
- `disasm-roundtrip` CI job running `make ci-disasm-roundtrip`.

Spec: `docs/specs/2026-06-07-disasm-round-trip-design.md`

Next: Z80 port of the disassembler (strand-B PR-3).
EOF
)"
```

- [ ] **Step 2: Monitor CI to completion**

```bash
gh pr checks --watch
```

Wait for all checks to go green. If any fail, fix them before proceeding.

- [ ] **Step 3: Add `disasm-roundtrip` as a required branch-protection check**

Once the `disasm-roundtrip` CI job has run at least once (so GitHub knows the check name), add it to the required list.  Fetch the current list first to avoid accidentally removing existing checks:

```bash
current=$(gh api repos/petemoore/sam-aarch64/branches/main/protection \
  --jq '.required_status_checks.contexts | map("--field \"required_status_checks.contexts[]=" + . + "\"") | join(" ")')
echo "Current checks: $current"
```

Then PUT the full updated list (paste all existing check names plus `disasm-roundtrip`):

```bash
gh api repos/petemoore/sam-aarch64/branches/main/protection \
  --method PUT \
  --field required_status_checks.strict=false \
  --field "required_status_checks.contexts[]=build-image" \
  --field "required_status_checks.contexts[]=m1" \
  --field "required_status_checks.contexts[]=m2" \
  --field "required_status_checks.contexts[]=m3" \
  --field "required_status_checks.contexts[]=m4" \
  --field "required_status_checks.contexts[]=m4-prod" \
  --field "required_status_checks.contexts[]=m5" \
  --field "required_status_checks.contexts[]=m5-prod" \
  --field "required_status_checks.contexts[]=m6" \
  --field "required_status_checks.contexts[]=m6-prod" \
  --field "required_status_checks.contexts[]=m6-release" \
  --field "required_status_checks.contexts[]=sysreg-sync" \
  --field "required_status_checks.contexts[]=disasm" \
  --field "required_status_checks.contexts[]=disasm-roundtrip" \
  --field enforce_admins=false \
  --field restrictions=null \
  --field required_pull_request_reviews=null
```

Verify:

```bash
gh api repos/petemoore/sam-aarch64/branches/main/protection \
  --jq '.required_status_checks.contexts[]'
```

Expected: all 14 checks listed, including `disasm-roundtrip`.

- [ ] **Step 4: Merge the PR**

```bash
gh pr merge --merge --delete-branch
```

---

## Task 7 — Update ROADMAP.md and m7-status.md

**Files:**
- Modify: `docs/ROADMAP.md` (Current State block)
- Modify: `docs/notes/m7-status.md`

- [ ] **Step 1: Update ROADMAP.md Current State block**

In the `### Current State & Next Actions` section, update the "THIS SESSION" bullet and the "Remaining M7 strands" line to reflect that strand-B PR-2 is done and the next step is the Z80 port (PR-3).

Key changes:
- Change "THE IMMEDIATE NEXT ACTION: build the strand-B PR-2 byte-level round-trip" to "DONE (PR #N)".
- Update "Suggested next action" to: build the Z80 port of the disassembler (strand-B PR-3).
- Add the PR number to the "Session landed" list.
- Update "branch protection requires **13** status checks" → **14** (added `disasm-roundtrip`).

- [ ] **Step 2: Update m7-status.md strand-B row**

Find the "On-SAM disassembler (strand B)" row and update it:
- Mark PR-2 (round-trip) as ✅ done with the PR number.
- Update the "Next" pointer to PR-3 (Z80 port).

- [ ] **Step 3: Commit and push**

```bash
g add docs/ROADMAP.md docs/notes/m7-status.md
g commit -m "docs: update ROADMAP + m7-status for strand-B PR-2 (round-trip done)"
g push
```
