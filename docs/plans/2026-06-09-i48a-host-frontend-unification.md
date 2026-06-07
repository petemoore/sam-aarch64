# i48a — Host front-end unification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge the three host Go tools (`text2bin`, `refenc`, `bin2text`) into one integrated `sam-aarch64` binary so the intermediate *symbolic* `.tbn` is an in-memory IR, never written to disk — mirroring the SAM's single text→overlay→bytes assembler.

**Architecture:** Three capabilities become Go-authoritative shared library packages in one new module `tools/sam-aarch64` — `frontend` (text→symbolic IR: preprocess/lex/parse + strip), `assemble` (symbolic IR→bytes + overlay compaction: pass1/pass2/compact/overlay), `render` (overlay/.tbn→text). Built in **three phased PRs** (Pete, 2026-06-09): **PR1** extracts the libraries while keeping the three existing binaries as thin wrappers (byte-neutral, zero call-site churn); **PR2** adds the integrated `sam-aarch64` binary, rewires all ~150 call sites, and drops the on-disk symbolic `.tbn`; **PR3** applies the Decision-B strictness items + the q7 GNU-rewrite sweep + the i48d overlay-only doc rewrite.

**Tech Stack:** Go 1.26 (multi-module via local `replace` directives, no `go.work`), the koron-go/z80 harness for inner-loop Z80 verification, SimCoupé-under-Docker as the CI gate. The **m6-release 3-way byte-match** (GNU == Go == Z80/SAM) and the harness are the byte-neutrality guard at every step.

**Authority:** `docs/specs/2026-06-08-i48-single-format-syntactic-encoder-design.md` (decisions A+B, §6 touchpoints, §7 strands), `docs/notes/item-registry.md` (i48a/i48b/i48c/i48d rows), `docs/notes/m8-status.md`.

**Hard invariant (all PRs):** the overlay `.tbn` bytes and the assembled binary stay byte-identical to today. PR1 and PR2 are *byte-neutral by construction* (same code, relocated / same records, in-memory instead of on-disk). PR3's strictness changes are designed to be zero-corpus-cost (the spec measured 0 affected sites).

---

## Current architecture (the seam to remove)

```
.s ──text2bin──▶ symbolic .tbn (DISK) ──refenc──▶ {binary, compact overlay .tbn}
                                                          │
                              compact .tbn ──bin2text──▶ .s text
```

| Tool | Module | Today's shape | Capability |
|---|---|---|---|
| `text2bin` | `tools/text2bin` | `main` + `internal/translate` (package) + `strip.go` (in main) | text → symbolic IR (+ flatten/strip/`-E`) |
| `refenc` | `tools/refenc` | **all `package main`** (pass1/pass2/compact/overlay/usage) | symbolic IR → bytes + overlay |
| `bin2text` | `tools/bin2text` | `main` + `emit` (package) | overlay/.tbn → text |

Shared libs (unchanged by this work): `tools/sam-aarch64-format`, `tools/aarch64enc`, `tools/aarch64dec`.

Key facts that shape the move:
- `internal/translate` is `internal/` → only `text2bin` can import it; must move to a shareable location.
- `refenc`'s logic is all `package main` → **not importable**; must move to a library package.
- `bin2text/emit` is already a library package.
- `text2bin`'s dependency on `bin2text/emit` is **test-only** (`frontend` round-trip tests: `golden_test.go`, `idempotency_test.go`, `reachability_test.go`). After the move both live in the new module as sibling packages — no import cycle (`emit`/`render` does not import `translate`/`frontend`).
- `refenc` uses a package-global `var usage *Usage` set in `main`; preserve via exported `EnableUsage()`/`DumpUsage()`.

## Target module layout (after PR1)

```
tools/sam-aarch64/                 NEW module github.com/petemoore/sam-aarch64/tools/sam-aarch64
  go.mod                           requires+replaces ../sam-aarch64-format ../aarch64enc ../aarch64dec
  frontend/    (package frontend)  ← text2bin/internal/translate/*.go + text2bin/strip.go
  assemble/    (package assemble)  ← refenc/{pass1,pass2,compact,overlay,usage}.go (+ tests)
  render/      (package render)    ← bin2text/emit/*.go
  (main.go added in PR2)
tools/text2bin/   main.go (thin wrapper over frontend) + go.mod (replace sam-aarch64)
tools/refenc/     main.go (thin wrapper over assemble) + go.mod (replace sam-aarch64)
tools/bin2text/   main.go (thin wrapper over render)   + go.mod (replace sam-aarch64)
```

Package renames at move time (the design names the three capabilities): `translate`→`frontend`, `emit`→`render`, `refenc`'s `main`→`assemble`. Done once now while these become public libs.

---

# PR1 — byte-neutral library extraction (this is the detailed, executable phase)

**Branch:** `i48a-host-frontend-unification` (single feature branch; CLAUDE.md §5).

**Done-criterion:** all three binaries build, every existing Go test passes from its new home, the harness boot/compact-tbn/release-paged tests are green, the m6-release Go arm + disasm-roundtrip Go legs byte-match, and CI's full SimCoupé matrix is green. **No `.s`/`.tbn`/`.img` byte changes anywhere.**

### Task 1: Create the branch and the new module skeleton

**Files:**
- Create: `tools/sam-aarch64/go.mod`

- [ ] **Step 1: Branch off a fresh main**

```bash
g checkout main && g pull --ff-only
g checkout -b i48a-host-frontend-unification
```

- [ ] **Step 2: Write the new module's go.mod**

Create `tools/sam-aarch64/go.mod`:

```
module github.com/petemoore/sam-aarch64/tools/sam-aarch64

go 1.26.1

require (
	github.com/petemoore/sam-aarch64/tools/aarch64dec v0.0.0-00010101000000-000000000000
	github.com/petemoore/sam-aarch64/tools/aarch64enc v0.0.0-00010101000000-000000000000
	github.com/petemoore/sam-aarch64/tools/sam-aarch64-format v0.0.0-00010101000000-000000000000
)

replace (
	github.com/petemoore/sam-aarch64/tools/aarch64dec => ../aarch64dec
	github.com/petemoore/sam-aarch64/tools/aarch64enc => ../aarch64enc
	github.com/petemoore/sam-aarch64/tools/sam-aarch64-format => ../sam-aarch64-format
)
```

### Task 2: Move the renderer (`bin2text/emit` → `sam-aarch64/render`)

**Files:**
- Move: `tools/bin2text/emit/{emit,overlay,pc}.go` + tests → `tools/sam-aarch64/render/`
- Modify: package clause `emit` → `render` in every moved file

- [ ] **Step 1: git mv the package**

```bash
mkdir -p tools/sam-aarch64/render
g mv tools/bin2text/emit/*.go tools/sam-aarch64/render/
rmdir tools/bin2text/emit
```

- [ ] **Step 2: Rename the package clause** in every file under `tools/sam-aarch64/render/`: change `package emit` → `package render`. (No intra-package qualified references to update — they use unqualified names.)

- [ ] **Step 3: Build the new module's render package**

Run: `cd tools/sam-aarch64 && go build ./render/`
Expected: PASS (render imports only format/aarch64dec/aarch64enc, all covered by the replace block).

### Task 3: Move the front-end (`text2bin/internal/translate` + `strip.go` → `sam-aarch64/frontend`)

**Files:**
- Move: `tools/text2bin/internal/translate/*.go` → `tools/sam-aarch64/frontend/`
- Move: `tools/text2bin/strip.go` (+ `strip_test.go`) → `tools/sam-aarch64/frontend/`
- Modify: package clause `translate` → `frontend` in every moved file; `package main` → `package frontend` in `strip.go`/`strip_test.go`; export `stripCommentRecords`→`StripCommentRecords`, `stripDataRecords`→`StripDataRecords`; update the three round-trip tests' import of `bin2text/emit` → `sam-aarch64/render` (alias `emit`→`render` or keep an alias).

- [ ] **Step 1: git mv translate + strip**

```bash
mkdir -p tools/sam-aarch64/frontend
g mv tools/text2bin/internal/translate/*.go tools/sam-aarch64/frontend/
g mv tools/text2bin/strip.go tools/sam-aarch64/frontend/
g mv tools/text2bin/strip_test.go tools/sam-aarch64/frontend/
rmdir tools/text2bin/internal/translate tools/text2bin/internal 2>/dev/null || true
```

- [ ] **Step 2: Rename package clauses.** In every file under `tools/sam-aarch64/frontend/`: `package translate` → `package frontend`; in `strip.go`/`strip_test.go`: `package main` → `package frontend`.

- [ ] **Step 3: Export the strip helpers.** In `frontend/strip.go` rename `func stripCommentRecords` → `func StripCommentRecords` and `func stripDataRecords` → `func StripDataRecords`. Update the call(s) in `frontend/strip_test.go` to the exported names. (`dataDirectiveSet`, `instHasLitPoolOperand` stay unexported.)

- [ ] **Step 4: Repoint the round-trip tests' renderer import.** In `frontend/{golden_test,idempotency_test,reachability_test}.go` change the import
  `emit "github.com/petemoore/sam-aarch64/tools/bin2text/emit"` →
  `emit "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"` (keep the `emit` alias so `emit.Emit(...)` call sites are unchanged; the package is now named `render` but the alias preserves the local name).

- [ ] **Step 5: Add render/format/enc deps to the new module if missing**, then build:

Run: `cd tools/sam-aarch64 && go build ./frontend/ ./render/`
Expected: PASS. (frontend non-test imports format + aarch64enc; tests additionally import render — same module.)

### Task 4: Move the assembler (`refenc` libs → `sam-aarch64/assemble`)

**Files:**
- Move: `tools/refenc/{pass1,pass2,compact,overlay,usage}.go` + `{pass1_test,pass2_test,compact_test}.go` → `tools/sam-aarch64/assemble/`
- Modify: package clause `main` → `assemble` in every moved file
- Add: `tools/sam-aarch64/assemble/api.go` — exported `EnableUsage()`, `DumpUsage()`, `CompactTBNBytes()`

- [ ] **Step 1: git mv the refenc libs (NOT main.go)**

```bash
mkdir -p tools/sam-aarch64/assemble
g mv tools/refenc/pass1.go tools/refenc/pass2.go tools/refenc/compact.go \
     tools/refenc/overlay.go tools/refenc/usage.go \
     tools/refenc/pass1_test.go tools/refenc/pass2_test.go tools/refenc/compact_test.go \
     tools/sam-aarch64/assemble/
```

- [ ] **Step 2: Rename package clauses** in every file under `tools/sam-aarch64/assemble/`: `package main` → `package assemble`.

- [ ] **Step 3: Add the thin API shim** so the `refenc` wrapper keeps the same behavior. Create `tools/sam-aarch64/assemble/api.go`:

```go
package assemble

import (
	"bytes"
	"io"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// EnableUsage turns on the peak-usage census (refenc --dump-usage).
// It must be called before Pass1.
func EnableUsage() { usage = newUsage() }

// DumpUsage writes the census to w, recording the total output size.
// No-op if EnableUsage was not called.
func DumpUsage(w io.Writer, totalOut int) {
	if usage == nil {
		return
	}
	usage.TotalOutBytes = totalOut
	usage.Dump(w)
}

// CompactTBNBytes compacts f's record stream (using p1's pass-1 results)
// and returns the serialized compact v2 .tbn bytes: the name table is
// rebuilt by interning f.Names in ID order (reproducing the same IDs the
// records reference), and label/local definitions move to the header
// tables. Byte-for-byte identical to refenc's former -emit-compact-tbn
// output.
func CompactTBNBytes(f *format.File, p1 *Pass1Result) ([]byte, error) {
	compacted, err := Compact(f, p1)
	if err != nil {
		return nil, err
	}
	st := format.NewSymbolTable()
	for _, n := range f.Names {
		st.Intern(n)
	}
	labels, locals := headerRows(f, p1)
	var buf bytes.Buffer
	if err := format.WriteFile(&buf, st, labels, locals, compacted); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
```

(`Pass1`, `Pass2`, `Compact`, `Pass1Result` are already capitalized in the moved files; `headerRows`, `usage`, `newUsage`, `Usage` move with them. Nothing else needs exporting.)

- [ ] **Step 4: Build + test the assemble package**

Run: `cd tools/sam-aarch64 && go build ./... && go test ./assemble/`
Expected: PASS (the moved `pass1_test/pass2_test/compact_test` run from their new home).

- [ ] **Step 5: Full new-module test sweep**

Run: `cd tools/sam-aarch64 && go test ./...`
Expected: PASS — frontend (incl. the golden/idempotency/reachability round-trips through `render`), assemble, render all green.

### Task 5: Rewrite the three wrappers + fix their go.mods

**Files:**
- Rewrite: `tools/text2bin/main.go`, `tools/refenc/main.go`, `tools/bin2text/main.go`
- Modify: `tools/text2bin/go.mod`, `tools/refenc/go.mod`, `tools/bin2text/go.mod`

- [ ] **Step 1: Rewrite `tools/refenc/main.go`** to call the `assemble` library:

```go
package main

import (
	"flag"
	"fmt"
	"os"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
)

func main() {
	var outFlag string
	var dumpUsage bool
	var emitCompact string
	flag.StringVar(&outFlag, "o", "", "output binary")
	flag.BoolVar(&dumpUsage, "dump-usage", false,
		"after assembly, print a peak-usage census of all internal "+
			"data structures (symbol table, local labels, literal "+
			"pool, expr evaluator, OPVAL buffer, record stream) to "+
			"stderr — used for sizing the Z80-side fixed tables.")
	flag.StringVar(&emitCompact, "emit-compact-tbn", "",
		"also write a compacted v2 .tbn to this path: instructions are "+
			"collapsed into INSN_RUN records (assembled base words plus a "+
			"sparse overlay patch for symbol/PC-bearing fields), shrinking "+
			"the file while assembling to the identical binary. The normal "+
			"-o binary is unaffected.")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: refenc INPUT.tbn -o OUTPUT.bin [--dump-usage]")
		os.Exit(2)
	}
	if dumpUsage {
		assemble.EnableUsage()
	}
	in, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fail(err)
	}
	f, err := format.ReadFile(in)
	if err != nil {
		fail(err)
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		fail(err)
	}
	if emitCompact != "" {
		b, err := assemble.CompactTBNBytes(f, p1)
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(emitCompact, b, 0644); err != nil {
			fail(err)
		}
	}
	out, err := assemble.Pass2(f, p1)
	if err != nil {
		fail(err)
	}
	if dumpUsage {
		assemble.DumpUsage(os.Stderr, len(out))
	}
	if outFlag == "" {
		os.Stdout.Write(out)
		return
	}
	if err := os.WriteFile(outFlag, out, 0644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
```

- [ ] **Step 2: Rewrite `tools/text2bin/main.go`** to import `frontend` instead of `translate`. Mechanically: change the import to `frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"`, replace every `translate.` with `frontend.`, and replace the two strip calls `stripCommentRecords(out)`/`stripDataRecords(out)` with `frontend.StripCommentRecords(out)`/`frontend.StripDataRecords(out)`. Keep `parseInt64` and `includeDirsFlag` local to the wrapper. (Everything else — flag definitions, `-E`/`-flatten`/`-strip-*` logic — stays verbatim.)

- [ ] **Step 3: `tools/bin2text/main.go`** — change the import `emit "…/bin2text/emit"` → `render "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"` and the call `emit.Emit(in)` → `render.Emit(in)`. Nothing else changes.

- [ ] **Step 4: Fix the three wrapper go.mods.** For each of `text2bin`, `refenc`, `bin2text`: add a require + `replace …/tools/sam-aarch64 => ../sam-aarch64`, drop the now-unused `bin2text` require/replace from `text2bin`, then let `go mod tidy` settle the require set. The replace block of each wrapper must cover **every** local module in its build tree (Go honors only the main module's replaces):

```bash
for m in text2bin refenc bin2text; do (cd tools/$m && go mod tidy); done
```

If `go mod tidy` reports a missing replace, add it (each wrapper needs `sam-aarch64` + transitively `sam-aarch64-format`, `aarch64enc`, and for `bin2text`/`render` also `aarch64dec`).

- [ ] **Step 5: Build all three binaries via make** (the real build path):

Run: `make text2bin refenc bin2text`
Expected: PASS — three binaries land in `build/`.

### Task 6: Verify byte-neutrality (the regression guard)

- [ ] **Step 1: All Go tests across all modules**

Run: `for m in sam-aarch64 sam-aarch64-format aarch64enc aarch64dec text2bin refenc bin2text; do (cd tools/$m && go test ./...) || echo "FAIL $m"; done`
Expected: every module PASS (wrappers now have no tests; the logic tests pass from `tools/sam-aarch64`).

- [ ] **Step 2: Harness — boot self-test, compact-tbn, release-paged**

Run: `cd tools/z80-test-harness-go && go test ./... -run 'TestBootSelfTestsPass|TestCompactTbnAssembly|TestReleasePagedInLoad|TestDisasmOracle' -count=1`
Expected: PASS (the harness shells out to the freshly-built `build/text2bin`/`build/refenc`; identical bytes ⇒ identical results). If Docker/pyz80 prerequisites are missing for a given test, note it and rely on CI.

- [ ] **Step 3: m6-release 3-way gate (Go arm at minimum) + disasm-roundtrip**

Run: `tools/run-m6-release-gate.sh` and `tools/run-disasm-roundtrip.sh` (locally these need pyz80; the Go-arm byte-matches always run). 
Expected: the Go `release.img` and the compact-tbn-derived `release.img` both equal the vendored GNU `release.img`; 104/104 disasm round-trips. If SimCoupé/Docker is unavailable locally, the Go arms passing + the harness passing is the inner-loop proxy; CI runs the SimCoupé arm.

- [ ] **Step 4: Diff-sanity** — confirm the change is a pure move + rewire:

Run: `g diff --stat main`
Expected: renames dominate; the only non-move edits are package clauses, the three wrapper `main.go` files, `assemble/api.go`, and the four `go.mod` files. No edits to `tools/sam-aarch64-format`, `aarch64enc`, `aarch64dec`, the shell gates, the Makefile recipes, or the harness.

### Task 7: Land PR1

- [ ] **Step 1: Commit** (single logical commit, or a few: render-move / frontend-move / assemble-move / wrappers):

```bash
g add -A
g commit -m "i48a PR1: extract text2bin/refenc/bin2text into shared libs (byte-neutral)

Move the three host capabilities into one new module tools/sam-aarch64 as
library packages — frontend (text→symbolic IR + strip), assemble (pass1/
pass2/compact/overlay), render (overlay/.tbn→text). The three existing
binaries become thin main wrappers over them; output is byte-identical.
First step of i48a (host front-end unification); no call sites change.

Refs: docs/specs/2026-06-08-i48-single-format-syntactic-encoder-design.md"
```

- [ ] **Step 2: Push + open PR ready-for-review** (not draft; repo override):

```bash
g push -u origin i48a-host-frontend-unification
gh pr create --title "i48a PR1: extract host tools into shared libs (byte-neutral)" --body "<see template below>"
```

- [ ] **Step 3: Monitor CI to green** (all 14 checks incl. the SimCoupé matrix + m6-release). Fix failures autonomously, force-push, re-watch (global workflow rules 3–5).

- [ ] **Step 4: Run the §3 mandatory pre-merge review** (subagent, the CLAUDE.md checklist). Record the verdict natively with `gh pr review <n> --comment`. For this PR the relevant items: #1 (test-wiring — N/A, no `src/test_*.asm` touched), #3 (description matches diff — assert byte-neutral), #5 (no gap-masking skips). Treat any RED as a blocker.

- [ ] **Step 5: Merge** once green + review PASS:

```bash
gh pr merge --merge --delete-branch
```

---

# PR2 — integrated `sam-aarch64` binary + rewire + drop the on-disk symbolic `.tbn`

**Detailed steps to be written after PR1 lands** (the exact code depends on PR1's landed package boundaries). Scope, locked by the design + the Pete-approved phasing:

1. **Add `tools/sam-aarch64/main.go`** — the integrated binary. Modes (staged invocations as flags; q5 → one tool):
   - `sam-aarch64 SRC.s -o OUT.img [--emit-tbn OUT.tbn] [-I dir]... [-flatten] [-origin N] [-strip-comments] [-strip-data]` — the production single pass: `frontend.Translate*` → in-memory symbolic records → `assemble.Pass1` → (`assemble.Compact`/`CompactTBNBytes` for `--emit-tbn`) → `assemble.Pass2` → binary. **No symbolic `.tbn` touches disk.**
   - `sam-aarch64 IN.tbn -o OUT.img` — assemble an existing (compact overlay) `.tbn`: `format.ReadFile` → `assemble.Pass1`/`Pass2`.
   - `sam-aarch64 --render IN.tbn [-o OUT.s]` — `render.Emit` (the old `bin2text`).
   - `sam-aarch64 -E SRC.s` — preprocess-only (the old `text2bin -E`, used by `revendor-m6-release.sh`).
   - Input-mode detection: source vs `.tbn` by the `.tbn` magic header (fall back to explicit mode flag).
2. **Thread the symbolic IR in-memory.** Replace the `text2bin` `Translate*` → `[]byte` (serialized) → `refenc` `format.ReadFile` round-trip with a direct path. Two acceptable shapes, byte-identical either way: (a) keep `frontend` returning serialized record bytes and have the integrated tool `format.ReadFile` them from an in-memory buffer (trivially byte-safe; eliminates the **disk** handoff immediately); (b) have `frontend` return a `*format.File` directly (no serialize/parse round-trip at all — the cleaner end state). Start with (a) for a green checkpoint, then move to (b) and re-verify the gates. The flatten/strip passes operate on the in-memory record stream.
3. **Rewire all ~150 call sites** to `build/sam-aarch64` (inventory below). Each `text2bin -o X.tbn src; refenc -emit-compact-tbn X.compact.tbn X.tbn` pair collapses to one `sam-aarch64 src -o X.img --emit-tbn X.compact.tbn`. `bin2text X` → `sam-aarch64 --render X`. Preserve the inspectable overlay `.tbn` the gates compare.
4. **Delete the three old binaries** (`tools/text2bin`, `tools/refenc`, `tools/bin2text` `main.go` + their modules) and their Makefile build rules; add the `sam-aarch64` build rule.
5. **Drop dead serialization** that only the symbolic-on-disk handoff used: the standalone Go `KindLitInsts` reader arm (already dead — `INSN_RUN` mode 0 subsumes it; `compact_test.go` asserts none survive).
6. **Verify** the m6-release 3-way gate + harness + disasm-roundtrip + full SimCoupé matrix stay byte-identical and green.

### Call-site inventory to rewire (from the PR-planning sweep)

- **Makefile** (`Makefile`): build rules L24/L27/L76; `release-stripped-tbn` recipe L363–L364; many `test-m*`/`release-*` target prereqs listing `text2bin`/`refenc`/`bin2text`.
- **Shell gates** (`tools/`): `run-m{3,4,5,6}-roundtrip.sh`, `run-m6-release-gate.sh` (L62/L65/L69/L72), `run-m6-release-stripped.sh`, `run-disasm-roundtrip.sh` (~22 invocations: L82/134/181/200/243/256/309/318/358/364 text2bin; L88/140/186/204/248/260/312/321/360/366 refenc; L315/362 bin2text), `run-release-sam.sh`, `revendor-m6-release.sh` (L72–73).
- **Test round-trip scripts** (`tests/`): `m1/run-refenc-roundtrip.sh` (L37/38/54/55), `spectrum4/run-roundtrip.sh` (L32/33/48/49), `m{3,4,5,6}/run-roundtrip.sh` (make-target prereqs).
- **Go harness** (`tools/z80-test-harness-go/`): `harness_test.go` (L59/60/77/82), `synthetic_parity_test.go` (L258/262/281/284), `compact_tbn_test.go` (L36/37/53/60), `test_variant_test.go` (L41/42/57/62), `sweep_test.go` (L38/39/107/113), `release_paged_test.go` (L47/48/66/74), `boot_self_test_test.go` (L66/67/85/90).
- **CI** (`.github/workflows/ci.yml`): all via `make`/scripts (no direct tool calls) — green follows automatically once the scripts/Makefile are rewired.
- **scripts**: `scripts/build-spectrum4-release.sh` (L53/57/70).

---

# PR3 — Decision-B strictness + q7 sweep + i48d overlay-only docs

**Detailed steps after PR2.** Scope (all non-byte-affecting on the corpus — the spec measured 0 affected sites):

1. **Syntactic mem classification** (`assemble`/`frontend`): a symbolic mem operand maps to `FoldMemImm12` by syntax (the `ldr`/`str` mnemonic), never by evaluating the offset; if it resolves to something only the unscaled form holds (negative/unaligned) → a **clear error** ("use `ldur`"), not a silent imm9 rewrite. Remove `classifyMem`'s value-eval imm12-vs-imm9 branch for symbolic operands. (Constant operands keep their parse-time choice.)
2. **add/sub large-immediate `lsl #12`** stays syntactic/explicit (never auto-rewritten) for symbolic operands.
3. **`mov #x` family**: default `mov`→`movz` syntactically; fall back to `orr`(bitmask)/`movn` **only at assemble time** when the resolved value isn't movz-able.
4. **q7 sweep**: scan the corpus for any *other* GNU "generous" silent rewrites and treat them the same (error/explicit, not silent). Record findings in `docs/notes/question-registry.md` (q7).
5. **i48d overlay-only doc rewrite**: bring `docs/specs/2026-06-08-tbn-binary-format-reference.md` from "two profiles" to **overlay-only** (the symbolic intermediate no longer exists on disk after PR2). Drop the symbolic-record-kind sections; keep `INSN_RUN`/`LIT_DATA`/`DIRECTIVE`/`COMMENT` + header tables. Update the M1/i1 banners.
6. **Drop the symbolic record kinds from the format library** once nothing serializes them: `KindInst`/`KindLabelDef`/`KindLocalDef`/`KindLitInsts` consts + read/write paths in `tools/sam-aarch64-format/{kinds,reader,writer,litinsts}.go` (and their tests). The symbolic kinds remain only as the in-memory IR types in `frontend`/`assemble`.
7. **Convert the M1 string-matched goldens** (`tests/m1/golden/`) to re-assemble-and-byte-check round-trips so a frozen-wrong golden can't slip again (the `dir_skip_symbolic` 80-vs-96 incident).

---

## Tracking obligations (handover contract rule 1)

- On PR1 merge: flip the i48a row in `docs/notes/item-registry.md` and `docs/notes/m8-status.md` to "PR1 done; PR2 next"; update `docs/ROADMAP.md` "Current State".
- Keep q7 (`docs/notes/question-registry.md`) live until the PR3 sweep resolves it.

## Self-review notes

- **Spec coverage:** Decision A (eliminate serialized symbolic) → PR2 step 2 + PR3 step 6. Decision B (syntactic encode, fold value-work) → PR3 steps 1–3 (the byte-affecting `FoldMovzAuto` part already landed as i48b, commit `0162f52`). q5 (one tool) → PR2 step 1. i48d (docs) → PR3 step 5. The three shared libs (§7 i48a) → PR1.
- **Byte invariant:** PR1 = same code relocated; PR2 = same records in-memory vs on-disk; PR3 = measured 0-cost. The m6-release 3-way gate + harness guard every step.
- **No placeholders in PR1:** every PR1 step has exact paths, commands, and the full wrapper/api code. PR2/PR3 are deliberately scoped-not-stepped because their exact code depends on PR1's landed boundaries; they will be expanded to bite-sized steps when reached.
