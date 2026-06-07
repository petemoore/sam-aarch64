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
| **i39a** — Phase 1: instruction overlay (unify literal/symbolic INST into one run) + header label/offset table; v2 format flip | ✅ **MERGED to `main`** — PR #131, merge commit `e68e0bf` (all 14 CI checks green incl. the SimCoupé matrix; §3 review = MERGE). PR(a)+(b)+i48b+(c)+(d) + i48d. i48a split to its own follow-up PR (see below). | `docs/plans/2026-06-08-i39-phase1-instruction-overlay-plan.md`; PR #131 (merged) |
| **i39b** — Phase 2: name-table front-coding + comment/`.global`/base-hint editor sidecars (evictable region) | 📋 **plan-ready** — implementation plan written (suggests splitting into **i39b-1** front-coding + **i39b-2** sidecars/page-split; the invariant shifts from i39a's "`.tbn` byte-identical" to "assembled binary identical + round-trip holds + `.tbn` shrinks") | `docs/plans/2026-06-09-i39b-nametable-frontcoding-sidecars.md`; design §3.5/§3.6/§3.7, §4 Format B, §5 |
| **i39c** — Phase 3: bitfield-packing polish on the overlay slot bytes | 🧭 designed (low priority) | design §3.1 |
| **i40** — assembler-side editor-region eviction (write editor region/`.tbn` to disk before assembling, reuse RAM as OUT/scratch, reload to restore) | 🧭 future (editor phase) | design §7 decision 1 |
| **i48** — single serialized format + pass-free syntactic encoder (refines i39/i39a). **A:** overlay is the *only* serialized `.tbn`; symbolic kinds become in-memory IR (old format buried, in no head doc). **B:** text→overlay is syntactic (no symbol pass); value-bits computed in the fold; forego GNU's silent `ldr→ldur`/`add lsl#12` rewrite (→ syntactic/error); narrow `mov`→`movz`/`orr`/`movn` assemble-time fallback. Driver: the SAM must do text→overlay too (editor), so the host should mirror that flow. | ✅ **host side DONE** — **i48a** (#141/#142/#144) host front-end unification + in-memory IR + symbolic-serialization removal · **i48b** syntactic encoder + fold value-work · **i48d** overlay-only doc rewrite — all merged. **i48c** (Z80 text→overlay encoder) is future (editor phase). | `docs/specs/2026-06-08-i48-single-format-syntactic-encoder-design.md`; item registry i48 |

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

**Feature-branch CI state (PR #131): ALL 14 checks GREEN as of PR(c)
(2026-06-09).** The full SimCoupé matrix — **m3, m4, m4-prod, m5, m5-prod, m6,
m6-prod, m6-release** — plus build-image, disasm, disasm-roundtrip, m1, m2,
sysreg-sync. (Between PR(a) and PR(c) the Z80 jobs were red, expected — the v2
version bump tripped the Z80 reader's `version == 1` check before PR(c) added the
v2 reader + `INSN_RUN` decoder.)

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

**✅ i48b — `FoldMovzAuto` computes hw in the fold (DONE, 2026-06-09; commit `0162f52`).**
The one i48b change with a format-byte effect: `ZeroSlot` clears the movz-auto
`hw` (bits 22:21) and `Fold` computes hw from the resolved value (the lowest
non-zero 16-bit chunk, mirroring `tryEncodeMovImm`). Verified: release
byte-matches GNU through the v2 compact `.tbn`; 104/104 disasm round-trips. The
non-byte-affecting Decision-B strictness items (symbolic mem → error, add/sub
`lsl#12` syntactic, mov→movz fallback) + the q7 sweep stay with **i48a**.

**✅ PR(c) — Z80 v2 reader + `INSN_RUN` decoder; symbolic path retired (DONE,
2026-06-09; commit `5e3e8eb`; all 14 CI checks GREEN incl. the SimCoupé matrix).**
`src/reader.asm` version 1→2; `src/insn_run.asm` (new) decodes `INSN_RUN` (0x09)
— mode-0 memcpy (via `main_handle_lit_insts`), mode-1 per element `base | fold`
over patches with a 13-rule fold dispatch (faithful ports of `overlay.go::Fold`,
reusing the proven slot encoders + inline field-place for mem/pair/movk; litpool
via `litpool_register`/`litpool_lookup`). The symbolic `KindInst` form-table path,
`LIT_INSTS` dispatch, and `encode_inst` dispatcher are **deleted** — net **~370 B
smaller** (test `&BE7F`, 385 B headroom). Gates + harness feed the compact `.tbn`.
**Result:** the full release assembles **byte-identically to GNU on the SAM**
(21752 B) and all **53 M3–M6 fixtures byte-match GNU**. *(The dead Go-side
`KindLitInsts` reader arm is left for i48a, per the i48 design — the Z80 budget-
critical removal is done.)*

**✅ PR(d) — header label/offset table DONE (commits `f6d15ba` Go + `61f4a08` Z80).**
Label/local defs move out of the record stream into two delta-varint header
tables (`[count u16]` + `[name_id u16|digit u8][offset_delta uvarint]`, LEB128,
offset = symbolVMA − OriginVMA, sorted) between the name table and the records,
so the instruction run spans labels. **Go side** (`f6d15ba`): `format` emits/parses
the tables (`header_tables.go`); `refenc` drops LABEL_DEF/LOCAL_DEF from `Compact`
(flushing only the data run), builds rows from `p1` (a new `LabelDefs` provenance
set excludes `.equ`), and seeds `Symbols`/`LocalDefs` from the tables at
end-of-pass1; `bin2text` renders each def at its PC. Compact `.tbn` **47,067 →
45,189 B** (−4.0%); Go arms byte-match (compact == symbolic == GNU release.img).
**Z80 side** (`61f4a08`): `src/reader.asm` parses the tables at `reader_init` and
seeds the symbol/local tables (gated on `PASS_MODE == PASS_PASS1`; a page-safe byte
reader + a LEB128 uvarint decoder + 32-bit accumulate; stored offset == the symbol
value because every target/fixture origin is 4GB-aligned — the m6 byte-match is the
backstop); `src/main_loop.asm` deletes the LABEL_DEF/LOCAL_DEF dispatch + handlers +
the now-dead `copy_pass_pc_to_*` helpers + orphaned equates; `reader_init_done` now
normalises a page-boundary cursor (a latent large-source bug fixed at source).
Verified on the koron-go/z80 harness (`TestCompactTbnAssembly` +
`TestReleasePagedInLoad` byte-match release.img over multi-byte varints + hundreds
of labels) + the booted reader-paged self-test; `make check-budget` test **&BF68
(152 B headroom)**, prod 2043 B — under `&C000`, nothing ratcheted. SimCoupé
deferred to CI (no Docker locally; the harness is the faithful inner-loop proxy).

**✅ i48d — `.tbn` format reference → v2 DONE (commit `dc2b993`).** Scoped to
**v2-accurate** (NOT the overlay-only rewrite, since i48a is now deferred — see
below): documents the INSN_RUN record + the header tables + the 13 FoldSlots, framed
as **two profiles of one v2 container** — the symbolic intermediate (host-only
`text2bin` handoff, empty header tables, pending i48a) vs the compact overlay (the
SAM/shipped form). `LIT_INSTS` (0x07) marked retained-but-unproduced. Superseded
banners on the M1/i1 design docs.

**↪ i48a — host front-end unification, in 3 phased PRs (Pete, 2026-06-09).
✅ ALL THREE MERGED (2026-06-09 #5) — i48a is COMPLETE.** Plan:
`docs/plans/2026-06-09-i48a-host-frontend-unification.md`.
- **✅ PR1 (#141, merge commit `271b202`) — byte-neutral library extraction.** The
  three host tools became thin wrappers over one new module `tools/sam-aarch64`
  with three Go-authoritative shared libs: `frontend` (text→symbolic-IR + strip,
  from `text2bin/internal/translate` + `strip.go`), `assemble` (pass1/pass2/compact/
  overlay, from `refenc`), `render` (overlay→text, from `bin2text/emit`). Zero
  call-site churn. All 14 CI checks green; §3 review = MERGE.
- **✅ PR2 (#142, merge commit `1c2c0f9`) — integrated tool + drop on-disk symbolic
  `.tbn`.** `tools/sam-aarch64/main.go`: `source → {binary, compact .tbn}`,
  `.tbn → binary`, `--render`, `-E`; `SA64`-magic input detection; the **symbolic
  record stream lives only in memory** (frontend → in-memory buffer → `format.ReadFile`
  → pass1/pass2 — never on disk; i48 decision A). Rewired all ~150 call sites
  (Makefile + 14 shell gates + 7 koron-go/z80 harness tests + `scripts/`); deleted
  the three old wrapper modules; `release-stripped-tbn` now emits the compact `.tbn`
  the SAM reads. Byte-neutral across 89 M1–M6 fixtures + release (== GNU, 21752 B;
  compact `.tbn` 45,189 B) + render + `-E`. All 14 CI checks green incl. the SimCoupé
  matrix; §3 review = MERGE.
- **✅ PR3 (#144, merge commit `d07d627`) — symbolic records are in-memory IR only;
  format lib serializes overlay only.** (1) **The currency change** (shape-b): `format.File.Records`
  `[]byte`→`[]Record`; the front-end builds `[]format.Record` structs directly and hands
  the `*File` to pass1/pass2 — no serialize→`ReadFile` round-trip; `ReadFile` decodes the
  on-disk overlay stream into the slice. (2) **Format-lib removal:** `WriteInst`/
  `WriteLabelDef`/`WriteLocalDef`/`WriteLitInsts` + the four symbolic `Next()` decode-arms
  deleted; the `RecordKind` consts + `Record` fields stay (in-memory IR vocabulary);
  `Compact` re-serialises its DIRECTIVE/COMMENT pass-throughs from struct fields
  (byte-identical to the old `WriteRaw`). (3) **Decision-B strictness** — symbolic mem →
  `FoldMemImm12`-or-error (no silent imm9 rewrite); add/sub `lsl #12` syntactic (sh stays
  in the base word, fold fills only imm12, errors on overflow; `ZeroSlot` split from
  `FoldLogical`); `mov`→`movz` default. (4) **q7 sweep — resolved** (no GNU silent
  form-rewrite in the corpus beyond ldr→ldur / add `lsl#12`). (5) **i48d** overlay-only
  rewrite of `tbn-binary-format-reference.md`. (6) **item 7** — the M1 string-goldens →
  re-assemble-and-byte-check vs GNU (`golden_test.go`); the 36 dead goldens removed.
  Byte-neutral: GNU == Go(source) == Go(compact .tbn) (21752 B / 45,189 B), all Go
  suites + the koron-go/z80 harness + disasm-roundtrip (104/104), and **all 14 CI checks
  green incl. the SimCoupé matrix + m6-release 3-way byte-match**; §3 review = MERGE.
  Post-merge cleanup (#146): the review's `WriteRaw` follow-up + a dead-code
  sweep dropped `WriteRaw`, three orphaned `Reset()` helpers, and
  `SymbolTable.Name`/`Len`; a `staticcheck` (unused/U1000) CI gate now guards
  the core Go modules against re-accumulation.

**✅ MERGED (PR #131, merge commit `e68e0bf`, 2026-06-09 #3).** All 14 CI checks
green incl. the full SimCoupé matrix + the m6-release 3-way byte-match (GNU == Go ==
Z80/SAM — the first authoritative SimCoupé verdict on PR(d), confirming the harness);
the §3 pre-merge review returned MERGE (all items PASS, recorded on the PR). **The
i48a follow-up then landed in full: PR1 (#141) + PR2 (#142) + PR3 (#144) — i48a is
COMPLETE.** Decision A (overlay-only serialized; symbolic = in-memory IR) and Decision
B (syntactic encode) are realised on the host; only i48c (the Z80 text→overlay encoder,
editor phase) remains. See the i48a entry above.

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
