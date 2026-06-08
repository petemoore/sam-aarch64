# M8 — current status (read me first)

Entry point for any session picking up M8. **M8 = the next-gen compact
`.tbn` v2 format** — the *instruction-overlay* redesign (Format B) agreed
2026-06-08. It supersedes the i1 LIT_INSTS/LIT_DATA compaction (M7) and is
the storage foundation for the on-SAM editor.

**Items still use the global `iN` registry in `docs/notes/m7-status.md`**
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

## M8 scope / strands

Legend: ✅ done · ⏳ in progress · 📋 plan-ready · 🧭 idea

| Strand | Status | Source |
|---|---|---|
| **i39a** — Phase 1: instruction overlay (unify literal/symbolic INST into one run) + header label/offset table; v2 format flip | 📋 plan in progress | `docs/plans/2026-06-08-i39-phase1-instruction-overlay-plan.md` (being written) |
| **i39b** — Phase 2: name-table front-coding + comment/`.global`/base-hint editor sidecars (evictable region) | 🧭 designed (design §3.6/§3.7) | design §5 phase 2 |
| **i39c** — Phase 3: bitfield-packing polish on the overlay slot bytes | 🧭 designed (low priority) | design §3.1 |
| **i40** — assembler-side editor-region eviction (write editor region/`.tbn` to disk before assembling, reuse RAM as OUT/scratch, reload to restore) | 🧭 future (editor phase) | design §7 decision 1 |

## Open questions for Pete (M8)

None blocking — the 5 i39 design questions are all resolved (design §7).
The Phase-1 (i39a) plan may surface execution-level decisions (e.g. the
v1→v2 flip mechanics, whether the m6-release fixture needs re-vendoring);
those will be captured here + in the plan when it lands.

## Authoritative references

- Design (Format B + decisions): `docs/specs/2026-06-08-compact-tbn-nextgen-design.md`.
- v1 baseline encoding: `docs/specs/2026-06-08-tbn-binary-format-reference.md`.
- Phase-1 plan: `docs/plans/2026-06-08-i39-phase1-instruction-overlay-plan.md` (in progress).
- Global item registry: `docs/notes/m7-status.md` "Item index".
- Predecessor (i1 compaction this builds on): `docs/notes/m7-status.md` i1 rows.
