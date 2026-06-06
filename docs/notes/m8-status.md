# M8 — current status (read me first)

Entry point for any session picking up M8. **M8 = the next-gen compact
`.tbn` v2 format** — the *instruction-overlay* redesign (Format B) agreed
2026-06-08. It supersedes the i1 LIT_INSTS/LIT_DATA compaction (M7) and is
the storage foundation for the on-SAM editor.

**Items use the project-wide `iN` registry at `docs/notes/item-registry.md`**
(the id space is project-wide, not per-milestone). M8's items are `i39`
(the design) with implementation phases **i39a / i39b / i39c**, plus `i40`
(editor-region eviction). This doc is the M8 per-strand source of truth;
the ROADMAP "Current State" block is the live session view.

## Why M8 (vs staying in M7)

M7 was post-M6 consolidation (disassembler + compact `.tbn` i1 +
housekeeping). Its headlines are done. The next-gen format is a
**format-breaking v2 rewrite** with its own done-criterion (Format B
shipped, ~−32% resident vs the i1 51 KB) and a forward arc toward the
editor era — a clean milestone boundary, marked by the version bump.

## The agreed design (Format B)

Full design + reasoning: `docs/specs/2026-06-08-compact-tbn-nextgen-design.md`
(§3.2 overlay, §3.4 header label table, §5 phased path, **§7 Pete's
decisions**). Headline: store every instruction as its assembled 4-byte
word with relocated bitfields **zeroed** + a sparse overlay
`{slot, expression-bytecode}`; pass 2 evaluates the expression and ORs it
into the zeroed field — unifying literal and symbol-bearing instructions
into one run, and *faster* to decode than today's form-table path. Plus a
header label/offset table (labels don't break runs), name-table
front-coding, and an evictable editor region.

**Decisions locked (Pete 2026-06-08, design §7):** resident RAM is the
driver (file size secondary); one `.tbn` file with a contiguous evictable
editor region; clean breaking **v2**; numeric-base/spelling hints in the
editor region; uniform symbol resolution in the assembler (numeric locals
`1f`/`1b` stay first-class; `.global` preserved non-destructively as a
~1-bit/symbol flag). Projected **~38.6 KB file / ~34.5 KB
assembler-resident (−32% vs the i1 51 KB)**, in 3 phases behind the
m6-release byte-match gate.

**Editing principle (do NOT assume in-place `.tbn` editing; i41).** The `.tbn`
is the *serialized* storage/assembly form; the editor edits a separate
insertion-friendly in-memory document model and serializes to `.tbn` on save —
the `.tbn` is never mutated in place. The v2/i39 work designs the on-disk and
assembler-resident encoding only; it must not bake in any assumption that edits
shift `.tbn` bytes (a contiguous edit buffer would blow the ~1 s/edit bound at
~400 KB source). See **i41** for the edit-model rationale.

## M8 scope / strands

Legend: ✅ done · ⏳ in progress · 📋 plan-ready · 🧭 idea

| Strand | Status | Source |
|---|---|---|
| **i39a** — Phase 1: instruction overlay (unify literal/symbolic INST into one run) + header label/offset table; v2 format flip | ⏳ **PR(a)+(b) done; PR(c)/(d) remaining** (see below) | `docs/plans/2026-06-08-i39-phase1-instruction-overlay-plan.md`; branch `i39a-instruction-overlay`, PR #131 (draft) |
| **i39b** — Phase 2: name-table front-coding + comment/`.global`/base-hint editor sidecars (evictable region) | 🧭 designed (design §3.6/§3.7) | design §5 phase 2 |
| **i39c** — Phase 3: bitfield-packing polish on the overlay slot bytes | 🧭 designed (low priority) | design §3.1 |
| **i40** — assembler-side editor-region eviction (write editor region/`.tbn` to disk before assembling, reuse RAM as OUT/scratch, reload to restore) | 🧭 future (editor phase) | design §7 decision 1 |
| **i48** — single serialized format + pass-free syntactic encoder (refines i39/i39a). **A:** overlay is the *only* serialized `.tbn`; symbolic kinds become in-memory IR (old format buried, in no head doc). **B:** text→overlay is syntactic (no symbol pass); value-bits computed in the fold; forego GNU's silent `ldr→ldur`/`add lsl#12` rewrite (→ syntactic/error); narrow `mov`→`movz`/`orr`/`movn` assemble-time fallback. Driver: the SAM must do text→overlay too (editor), so the host should mirror that flow. | 📋 **design agreed** (Pete 2026-06-08) — strands **i48a** host front-end unification · **i48b** syntactic encoder + fold value-work (lands before/with PR(c)) · **i48c** Z80 text→overlay encoder (future) · **i48d** doc unification (rewrite the tbn format reference to overlay-only) | `docs/specs/2026-06-08-i48-single-format-syntactic-encoder-design.md`; item registry i48 |

**i48 ↔ i39a interaction.** i48b refines a fold-rule with a **format-byte effect**
(`FoldMovzAuto` computes `hw` instead of reading it from a pre-baked base word), so its
Go change must land **before/with i39a PR(c)** — PR(c)'s Z80 fold jump-table ports the
*refined* folds. i48a (host front-end unification) is independent of the Z80. i48d (the
doc scrub, incl. rewriting `tbn-binary-format-reference.md` to overlay-only) lands with
the v2 merge so no head doc describes a format the code doesn't produce.

## i39a Phase-1 progress (the v2 overlay flip)

All on branch **`i39a-instruction-overlay`** (PR **#131**, draft — single
branch holds PR(a)-(d), merges once the m6-release 3-way byte-match is green
for the full v2 stack; CLAUDE.md §5 long-lived-branch-until-green).

**✅ PR(a) — format v2 + overlay emit + Go assemble (done; Go byte-match verified).**
- `format`: `Version 1→2` (clean break, reader rejects others); `KindInsnRun`
  (0x09) — mode 0 packs bare 4-byte words (the LIT_INSTS floor), mode 1 stores
  base word (relocated field zeroed) + sparse `{slot, expr_len, expr}` patches.
  Reader→`Record.Elements`/`InsnElement`/`InsnPatch`; `WriteInsnRun`. Golden-byte
  + round-trip tests.
- `aarch64enc/overlay.go`: `FoldSlot` enum + `Fold(slot,value,pc,base)` +
  `ZeroSlot` + `FoldSlotForKind`. Each fold reuses the exact existing slot
  encoder / pass2 PC-conversion (no drift). Per-slot hand-computed vectors.
- `refenc/overlay.go`: `encodeInstOverlay` mirrors `encodeInst` dispatch to
  classify slot+expr, computes `base = ZeroSlot(encode@truePC)`, and **asserts
  `base|Fold == the literal encoder` per instruction** — a deterministic
  byte-match guard that surfaced (and pinned) every fold subtlety at compaction.
  `compact.go` accumulates `InsnElement`s and packs INSN_RUN frames (mode-0/
  mode-1, byte-match-invariant). `pass1` (PC + litpool from LITPOOL19 patches +
  `InstPC`) / `pass2` (per-element `base|Fold`) decode INSN_RUN.
- **Result:** spectrum4 release **byte-identical to GNU** via the v2 compact
  `.tbn` — all **1151 symbolic instructions** (10 slots). File **88,644→47,067 B**
  (−47% vs symbolic; −7.9% vs the i1 51,117; toward ~44 KB once PR(d) un-splits
  runs and/or `litBreak` is tuned). All Go unit tests green; disasm-roundtrip
  green (unaffected — it uses the symbolic `.tbn`).

**Feature-branch CI state (verified on PR #131, expected — CLAUDE.md §5).** ALL
Z80/SimCoupé fixture jobs are red — **m3, m4, m4-prod, m5, m5-prod, m6, m6-prod,
m6-release** — because the v2 version bump trips the Z80 reader's `version == 1`
check (`src/reader.asm:91`): the SAM assembler rejects every v2 `.tbn` at
`reader_init`, before any record (status `FAIL00` on every fixture, even
`empty`). This is the clean-break v2 consequence, not a Go bug. **GREEN:**
build-image, disasm, disasm-roundtrip, m1, m2, sysreg-sync (Go-only / symbolic-
path jobs) + every Go unit suite. PR(c) makes the Z80 jobs green again.

**Findings worth carrying forward (the plan §8 "needs Pete = NONE" was off by two):**
- The §3 ten-slot table **omitted two real fold-rules** that release.s uses:
  `FoldAddSubImm12` (the dominant `adrp`+`add #:lo12:` idiom + symbol-diff
  immediates, 44×) and `FoldPairImm7` (one `stp` with a symbol-diff offset).
  Both added — mechanical ports (CLAUDE.md §6), not design decisions.
- `movz`/`mov` needs **two folds**: explicit `movz/movk` carry the hw shift
  packed in bits 17:16 of the value (`imm16 = value&0xFFFF`, hw in the base) →
  `FoldMovkImm16`; `mov Rd,#sym` auto-movz passes the full value and the base's
  hw selects the chunk → `FoldMovzAuto`. The per-instruction assertion caught
  the conflated case.
- `FoldLogical` / `FoldMemImm9` are implemented + unit-tested but **not
  exercised by release.s** (no symbolic logical-imm / unscaled-mem there) — so
  the m6 gate doesn't cover them; PR(b)'s disasm-roundtrip over M3–M6 fixtures
  is where to confirm them.
- Header label table (PR(d)) needs label-vs-`.equ` provenance from
  `KindLabelDef` records (the merged `p1.Symbols` can't distinguish them; add a
  `LabelDefs` set in pass1).

**✅ PR(b) — overlay-aware render + round-trip fidelity gate (done).**
- `tools/bin2text/emit/overlay.go`: bin2text now renders a compact v2 `.tbn`
  back to text — walks `INSN_RUN`, decodes each element's base word via
  `aarch64dec`, and splices the `{FoldSlot, expr}` patch into the zeroed slot
  (branch/adr targets, `:lo12:` add, movz/movk, `ldr =pool`, scaled/unscaled
  mem offsets, symbol-diff operands). `LIT_DATA`→source data directives,
  `LIT_INSTS`→plain disasm. Put in **bin2text** (the file-level `.tbn`→text
  renderer with real symbol names + label/directive walking), calling
  aarch64dec only for the word-level base decode — the cleaner split than the
  plan's "aarch64dec rendering", and the exact bytes→text path the editor uses.
- `tools/run-disasm-roundtrip.sh` extended (`[2d/3]`+`[2e/3]`): assemble →
  `refenc -emit-compact-tbn` → bin2text → re-assemble, byte-identical over all
  M3–M6 fixtures **and the full release (== GNU release.img)**. The slot/fold
  *rendering* fidelity guard for PR(a). **104 round-trips pass, 0 fail**; the
  overlay renderer handles data + litpool natively, so it skips no fixtures.
  Per-slot unit vectors in `overlay_test.go` (incl. the logical-imm sentinel
  path: an all-zero bitmask is invalid, so the base decodes via a folded #1).
- **Three latent bin2text bugs fixed** (release-scale render exposed them —
  bin2text couldn't render release before, `OpPushImm64` blocked it):
  (1) `printExpr` dropped sub-expression parens, so `(a-b)*c` re-parsed with
  wrong precedence — now parenthesises non-atomic operands; this also corrected
  golden `dir_skip_symbolic.s`, whose `.skip (SIZE_A+SIZE_B)*2` had rendered as
  `(SIZE_A + SIZE_B * 2)` → **80 bytes not 96** (a real round-trip bug the
  golden had locked in). (2) `printExpr` ignored `OpPushImm64` (64-bit litpool
  constants). (3) a negative immediate rendered the malformed `0x-N` → now
  `-0xN`. `bin2text`/`text2bin` go.mod gained the `aarch64dec`/`aarch64enc`
  deps. All Go suites + `ci-m1` (incl. the GNU-as cross-check of the corrected
  golden) + `disasm-roundtrip` green; Z80 jobs stay red until PR(c).

**⏳ Remaining (same feature branch):**
- **PR(c)** — Z80 v2 reader. Two parts: **(i)** bump the reader version check
  `src/reader.asm:91` from 1 to 2 (one-line; without it the SAM rejects every v2
  `.tbn` — this is why all Z80 fixture jobs are currently red); **(ii)** the
  `INSN_RUN` decoder in `src/main_loop.asm` (mode-0 memcpy; mode-1 per element
  `base | fold` over patches via a fold jump-table = ports of the pass2
  conversions; litpool value from the LITPOOL_PC_MAP lookup; delete the
  form-table symbolic path). Makes **all** Z80 jobs green (m3-m6 + m6-release) +
  the Go-harness `compact_tbn_test`. Measure `&C000` budget (don't ratchet).
  **The large one — best on fresh context, with SimCoupé.** Also delete the
  now-dead **`KindLitInsts` (0x07)** handlers (Z80 `REC_KIND_LIT_INSTS` in
  `src/main_loop.asm` + the Go `reader.go` arm) — INSN_RUN mode 0 subsumes it,
  the compactor no longer emits it (`compact_test.go` asserts none survive),
  so both handlers are unreachable (Pete flagged 2026-06-08).
- **PR(d)** — header label/offset table (delta-varint); labels stop splitting
  runs; closes toward the ~44 KB target.

## Open questions for Pete (M8)

The 5 i39 design questions are all resolved (design §7), and PR(a) confirmed the
v1→v2 flip needs **no re-vendoring** (the m6 gate re-derives the `.tbn` from
`release.s`). Open questions live in the milestone-neutral **`docs/notes/question-registry.md`**
(`qN`). The i48 ones are mostly resolved: **q5** host packaging → **one integrated tool**
(Pete 2026-06-09); **q6** editor value-dependent base words → resolved by i41 decision #3
(editor holds the symbolic IR, not base words). Remaining: **q7** any other GNU rewrites
to forego (sweep at i48b). None blocking i39a/PR(c).

## Authoritative references

- Design (Format B + decisions): `docs/specs/2026-06-08-compact-tbn-nextgen-design.md`.
- v1 baseline encoding: `docs/specs/2026-06-08-tbn-binary-format-reference.md`.
- Phase-1 plan: `docs/plans/2026-06-08-i39-phase1-instruction-overlay-plan.md` (written; PR(a) executed — see "i39a Phase-1 progress" above).
- Global item registry: `docs/notes/item-registry.md`.
- Predecessor (i1 compaction this builds on): `docs/notes/item-registry.md` i1 row; `docs/notes/m7-status.md` i1 scope row.
