# Linker-script parsing / `SpectrumFourLayout` extraction — assessment (i210)

**Purpose.** This is the i210 input to **q42**. Pete asked whether parsing the
linker script (so the toolchain serves a *different* project's memory layout
without baked-in spectrum4 assumptions) is worth doing, with the same caveat as
the macro question: if it means rewriting a lot of code or a real performance
cost, flag it; otherwise it's good to do. This document **scopes** that work
(items i56 / i20) — it does **not** implement it. It enumerates every place the
spectrum4 layout is coupled, defines what a `SpectrumFourLayout` abstraction
would carry, weighs `-layout` flag vs `.ld` parser vs both, and estimates cost +
perf + byte-match risk. The go/no-go on q42 stays with Pete.

The real linker script being modelled is
`~/git/spectrum4/src/spectrum4/kernel/spectrum4.ld`: it sets
`. = 0xfffffff000000000;` then orders C-identifier sections `.text` / `.data` /
`bss_roms` / `bss_kernel` / `bss_userheap` / `bss_coherent` / `text_tests` /
`bss_tests`, each with an `ALIGN(...)`. The C-identifier names make GNU ld
synthesise `__start_<name>`/`__stop_<name>` boundary symbols for the test
harness.

## 1. The layout coupling today (touch-point table)

There are **two distinct layout domains**, and conflating them is the central
finding of this report:

- **Domain A — the GNU-ld VMA layout** (origin + section order + alignments).
  This is what a linker script describes, and it is **already almost fully
  decoupled** in the Go toolchain.
- **Domain B — the SAM physical-page / section-window placement** (which Z80
  RAM page each payload loads into, the `&0000`/`&8000`/`&C000` window
  conventions). A GNU linker script **says nothing about this** — it is a
  SAM-target property of *this* program's runtime, not of any `.ld`.

| # | Touch point | File:line | What it hardcodes | Domain |
|---|---|---|---|---|
| 1 | `OriginVMA` (the VMA of output byte 0) | `tools/sam-aarch64/assemble/pass1.go:45`, used at `:291`, `:323`, `:327` | Nothing fixed — `OriginVMA` is a *field*, set from a leading `.org`/`. =` in the source. The unsigned-VMA math (`uint64(pc)%N`) at `:147`, `:155`, `:291`, `:489` exists because spectrum4 VMAs exceed 2^63; it is layout-value-agnostic. | A |
| 2 | `-origin` default | `tools/sam-aarch64/main.go:72-74` | The **default** origin string `"0xfffffff000000000"` when `-flatten` is used. Already a CLI flag — any project passes its own. | A |
| 3 | `FlattenOptions.OriginVMA` | `tools/sam-aarch64/frontend/flatten.go:39-43`, consumed at `:195`, `:228` | Carries the origin into the flatten pass; sourced from the `-origin` flag. Not hardcoded. | A |
| 4 | **`SpectrumFourLayout` section table** | `tools/sam-aarch64/frontend/flatten.go:54-66` | THE hardcoded `.ld` model: the 8-section ordered list with each `StartAlign`/`TrailingAlign`. Mirrors `spectrum4.ld` by hand. This is the i20 "spectrum4.ld hardcoded in flatten" coupling. | A |
| 5 | Flatten section dispatch | `flatten.go:91-121`, `:135-148`, `:232-267` | Assumes `sections[0]==.text` (emitted as bytes), `sections[1]==.data` (bytes after a `.balign 0x10`), and `sections[2..]` are NOBITS (only `.equ LABEL,VMA` emitted, bodies dropped). Encodes the spectrum4 "only `.text` materialises, the rest are trailing NOBITS like `objcopy -O binary`" decision. | A |
| 6 | Unknown-section guard | `flatten.go:144-147` | Hard error if a source uses a section name not in `SpectrumFourLayout`. A second project with different section names fails here. | A |
| 7 | `.section` sizing no-op note | `pass1.go:462-466` | pass1's own `.section` handling is a 0-byte no-op (the real layout work is upstream in flatten). Not a hardcode, but documents that pass1 assumes a single flat stream. | A |
| 8 | release-gate `ORIGIN` | `tools/run-release-gate.sh:54` | The spectrum4 origin literal, passed to `-flatten -origin`. CI-gate value, not toolchain code. | A |
| 9 | Makefile `-origin` literals | `Makefile:1543`, `:1557` | Same spectrum4 origin in the `release-stripped-tbn` / `release-unstripped-tbn` targets. Build-recipe values. | A |
| 10 | `revendor-release.sh` flatten step | `tools/revendor-release.sh:20`, `:71` | Re-flattens `release.target` → `release.s` via `-E`; spectrum4-specific because it consumes the spectrum4 source tree. | A |
| 11 | **SAM physical-page `equ`s** | `src/trampoline.asm:212` (`ENCTAB_PAGE=4`), `:289` (`TEST_MEM_PAGE=13`), `:324` (`ENC_FIX_PAGE=11`), `:347` (`TEST_CLUSTER_PAGE=12`), `:365` (`PAGED_CALL_TEST_PAGE=14`), `:395` (`SYSREG_DATA_PAGE=13`), `:437` (`DISASM_PAGE=15`), `src/zx0_comm.inc:28` (`ZX0_PAGE=13`) | The off-axis page map: which physical RAM page each payload occupies. **Not in any linker script** — a property of the SAM Z80 program's runtime paging. | B |
| 12 | SAM section-window `equ`s | `src/trampoline.asm` (`LMPR_ENCTAB`, `LMPR_IN_BASE`, the other `LMPR_*`, `HMPR_SAVE`) | The `&0000`(section A) / `&4000`(B) / `&8000`(C) / `&C000`(D) window-to-LMPR/HMPR mapping. SAM hardware geometry, not a `.ld` concept. (The OUT window has no `equ`: `emit_byte` computes its LMPR from the pool-allocated run.) | B |
| 13 | SAM memory-map narrative | `src/loader.asm:21-39`, `:83-92` | The `&8000` HLOAD window, `STACK_TOP=&C100`, the 12 KB code budget — the assembler's own load layout. Independent of the assembled *program's* VMA layout. | B |
| 14 | build-disk page-payload wiring | `tools/build-disk/main.go:87` (`LoadAddress=0x8000`), `:304`, `:319`, `:339`, `:360`, `:381`, `:400`, `:420`, `:438`, `:460` | The on-disk catalogue load addresses + which payload goes to which slot. Mirrors Domain B; describes *this* SAM disk, not a generic target's `.ld`. | B |
| 15 | `&C000` budget check | `Makefile` `check-budget` (release-gate `:61`) | The assembler binary must fit under `&C000`. SAM-target build constraint, Domain B. | B |

**Headline from the table:** Domain A (the actual linker-script content) is
*one* hardcoded table — `SpectrumFourLayout` at `flatten.go:54-66` — plus a
handful of build-recipe origin literals. Everything else in column A is already
parameterised (the origin is a flag; the unsigned-VMA math is value-agnostic).
Domain B (the SAM page map) is genuinely hardcoded across `trampoline.asm` /
`build-disk` / `loader.asm`, but **a linker script does not and cannot describe
it** — so "parse the linker script" does not touch Domain B at all.

## 2. What `SpectrumFourLayout` would abstract

A layout abstraction generalising today's hardcoded pieces would carry:

**Script-derived (a real GNU `.ld` provides these):**
- **Origin VMA** — the `. = N` assignment. *Already a field/flag* (`OriginVMA`,
  `-origin`); a parser would just read it from the script instead of the flag.
- **Section list, in order**, each with: name, `StartAlign` (the `ALIGN(n)` on
  the `:` line), and any trailing in-section `. = ALIGN(m)` (today's
  `TrailingAlign`). This is exactly the `SectionLayout` struct already at
  `flatten.go:48-52` and the `SpectrumFourLayout` slice at `:54-66`.
- **Which sections are PROGBITS vs NOBITS** — today hardcoded as
  "`.text`/`.data` emit bytes, the rest are NOBITS" (`flatten.go:232-267`). A
  general model needs this per-section (BSS-style sections drop bodies, emit
  only boundary `.equ`s).
- **Boundary-symbol generation** — `__start_<name>`/`__stop_<name>`. spectrum4
  relies on GNU ld synthesising these; our flatten emits `.equ LABEL,VMA` for
  in-section labels but does **not** today synthesise the `__start_/__stop_`
  pair. A faithful general layout would generate them per section. (Not a
  current gap for the release-gate, because the *materialised* output is
  `.text`-only and the harness symbols are a test-build concern.)

**SAM-target-specific (NO linker script carries these — Domain B):**
- The physical-page→payload map (touch point 11).
- The section-window/LMPR/HMPR conventions (touch point 12).
- The HLOAD window, stack top, code budget (touch points 13, 15).
- The on-disk slot/load-address wiring (touch point 14).

**This is the crux for q42:** a GNU linker script describes Domain A only. If the
goal is "another project can reuse the toolchain with its own layout," the
useful abstraction is a **layout config** that covers *both* domains — and only
the Domain-A half of it can ever come from a `.ld` file. So a literal `.ld`
parser solves the smaller, already-mostly-solved half and solves none of the
SAM-paging half.

### `-layout` flag vs `.ld` parser vs both

- **`-layout <config>` flag (recommended primary).** A small structured config
  (e.g. JSON/YAML/Go-struct-literal) listing origin + ordered sections
  (name, align, progbits/nobits) — i.e. serialise today's `SpectrumFourLayout`
  + `-origin` into one file the user supplies. Covers Domain A cleanly and can
  be *extended* to carry Domain-B knobs (page map, windows) that a `.ld` could
  never express. Lowest code churn: thread the parsed struct into
  `FlattenOptions` and drop the `var SpectrumFourLayout` global.
- **`.ld` parser.** Parse a *subset* of GNU ld script syntax (`SECTIONS { . = N;
  name : ALIGN(n) { *(name) } ... }`). Higher cost (a new mini-parser for a
  fiddly grammar with many features we'd ignore), and it only ever fills the
  Domain-A half. Its one advantage: a project that *already has* a `.ld`
  (spectrum4 does) gets reuse without re-authoring a config. But it cannot
  describe SAM paging, so a Domain-B config is still required alongside it.
- **Both.** `.ld` parser feeds the Domain-A fields of the same layout struct the
  `-layout` flag populates; `-layout` (or dedicated SAM flags) still supplies
  Domain B. This is the fullest answer but the most code.

**Recommendation:** if anything, do the **`-layout` flag** — it is the cheaper,
more complete abstraction (it can express what a `.ld` cannot). A `.ld` parser
is the more expensive option that solves strictly less. "Both" only if a second
project genuinely arrives carrying a hand-maintained `.ld` it wants to reuse
verbatim.

## 3. Cost, performance, and byte-match risk

**Cost — Domain A only (the realistic scope of "parse the linker script"):**
This is a **clean extraction, not a rewrite.** The work is:
- Replace the `var SpectrumFourLayout` global (`flatten.go:54-66`) and
  `canonicalSectionIndex` (`:70-77`) with a layout value passed through
  `FlattenOptions` (add a `Sections []SectionLayout` field, source it from a new
  `-layout`/parser path in `main.go`). The flatten algorithm itself
  (`flatten.go:85-270`) already reads from the `sections`/`SpectrumFourLayout`
  slice — it would read from the passed-in slice instead with near-zero logic
  change.
- Generalise the hardcoded "`sections[0]`/`[1]` are PROGBITS, `[2..]` NOBITS"
  dispatch (`flatten.go:232-267`) to a per-section progbits flag.
- A `-layout` flag: add the flag + a ~40-line config loader. A `.ld` parser
  instead: a ~150–300-line subset parser (the expensive alternative).
- Rough estimate: **`-layout` route ≈ 100–200 LOC across ~3 files**
  (`flatten.go`, `main.go`, one new config file), no change to `pass1.go`/
  `compact.go`/`render`. **`.ld`-parser route ≈ +150–300 LOC** for the parser on
  top. The existing struct shape (`SectionLayout`) means the data model is
  already right — this is plumbing, not redesign.

**Cost — Domain B (only if the *real* goal is full multi-project reuse):**
Larger and of a different kind — it touches **Z80 source** (`trampoline.asm`
`equ`s, `loader.asm`) and `build-disk`, which are assembled/built per-target.
Threading a page map through Z80 `equ`s means parameterising the assembler build
(`-D` defines or a generated `.inc`) — feasible but a separate, bigger effort,
and **out of scope for "parse the linker script"** since no `.ld` describes it.
This is the part to flag to Pete as "this is where the cost lives if the ambition
is true layout-independence, and it is not what linker-script parsing buys."

**Performance:** negligible. A layout config / linker script is read **once at
startup**, before any of the per-record passes. Parsing a tiny file (the real
`spectrum4.ld` is 74 lines) is microseconds against an ~88 KB two-pass assembly
that already takes ~20 s on SimCoupé / well under a second in the Go tool. There
is **no per-instruction or per-record cost** — the layout only affects section
*start* computation, done once in `flatten.go:195-204`. Confirmed: no assemble-
time perf concern.

**Byte-match risk:** **low and fully testable.** The invariant is that feeding
the spectrum4 layout must still produce the byte-identical release. The
release-gate (`tools/run-release-gate.sh`, the 3-way GNU==Go==Z80 match) is
exactly the regression guard. A correct extraction is a pure refactor: the
default `-layout` (or the parsed `spectrum4.ld`) must yield the same
`SpectrumFourLayout` values, and the release-gate proves it byte-for-byte. The
risk is the ordinary refactor risk of a subtle transcription error, caught
immediately by the existing gate. No new failure mode is introduced.

## 4. Is it worth doing now? / cheap partial step

i56's own registry note says *"Not worth doing unless a second project
surfaces."* That remains the honest read for the **full** abstraction:

- The **value** (serving a different project's layout) only materialises when a
  second project exists. Today there is exactly one consumer (spectrum4), so the
  abstraction would be unused generality — and the *expensive* half (Domain B,
  the SAM page map in Z80 source) is the part a second project would most need,
  yet the part linker-script parsing does **not** address.
- A `.ld` **parser** specifically is the worst value-for-cost option: most cost,
  solves only Domain A, which is already one struct away from parameterised.

**Cheap partial step that de-risks a future second project at low cost:**
The Domain-A extraction is genuinely cheap (~100–200 LOC, no rewrite, no perf
cost, fully guarded by the release-gate) because the data model
(`SectionLayout`) and the origin flag already exist. A low-cost, high-leverage
move is:

1. Lift `SpectrumFourLayout` + `OriginVMA` into a single **`-layout` config**
   (struct serialised to one file), defaulting to the spectrum4 values, with the
   release-gate proving byte-identity. This converts the hardcode into a
   supplied input **without** a `.ld` parser.
2. **Do not** build the `.ld` parser or touch Domain B until a second project
   actually surfaces with a concrete layout — at which point the config format
   can be extended (and, if that project insists on its existing `.ld`, a parser
   added to feed the same struct).

That partial step is the "extract the constants into one struct without a
parser" option: it removes the i20 "spectrum4.ld hardcoded in flatten" smell,
keeps the door open, and costs little. The full parser is the part to defer.

## 5. Go/no-go inputs for Pete (q42)

- **What linker-script parsing actually buys:** decoupling **Domain A only**
  (origin + section table), which is already one hardcoded struct
  (`flatten.go:54-66`) plus a few build-recipe literals — the origin is *already*
  a flag.
- **What it does NOT buy:** Domain B — the SAM physical-page map and section
  windows (`trampoline.asm`/`loader.asm`/`build-disk`) — which is where real
  layout-independence cost lives and which **no linker script can describe**.
- **`-layout` flag beats a `.ld` parser:** cheaper, and expresses Domain B too;
  a `.ld` parser is more code for strictly less coverage.
- **Cost:** Domain-A `-layout` extraction ≈ 100–200 LOC across ~3 Go files, no
  rewrite, **no perf cost**, byte-match guarded by the release-gate. A `.ld`
  parser adds ~150–300 LOC for no extra coverage. Domain-B reuse is a separate,
  larger Z80-build effort, out of scope for "parse the linker script."
- **Recommendation:** **No-go on a `.ld` parser now.** If Pete wants the smell
  gone cheaply, **green-light the small `-layout`/struct extraction** (the cheap
  partial step in §4) as the i56 deliverable and keep i20 (Domain-B reuse) and
  any `.ld` parser deferred until a second project surfaces — matching i56's
  existing "not worth doing unless a second project surfaces" note, but with the
  cheap de-risking step now available if desired.

This satisfies Pete's caveat directly: the worthwhile version is **not** a large
rewrite and has **no** performance cost; the large-rewrite/expensive version
(`.ld` parser + Domain-B reuse) is the one to flag and defer.
