# M9 — current status (read me first)

Entry point for any session picking up M9. **M9 = the editor era — Phase 2
foundations.** M8 (the `.tbn` v2 overlay format + prefix-only load path) closed
complete on 2026-06-12; it existed to be the editor's storage foundation. M9
builds the first editor-era bricks on top of it.

**Items use the project-wide `iN` registry at `docs/notes/item-registry.md`**
(the id space is project-wide, not per-milestone). This doc is the M9 per-strand
source of truth; the ROADMAP "Current State" block is the live session view.

## Why M9 (vs staying in M8)

M8 was the next-gen compact `.tbn` **v2** — the instruction-overlay format
(Format B). Its done-criterion was met end-to-end: the v2 overlay ships, the
SAM loads only the assembler-facing prefix, and the full unstripped `release.s`
flows through the release-gate 3-way byte-match. M8 existed to be the editor's
storage foundation, and that foundation is now laid. M9 is the **editor era** —
it turns the format into a usable on-SAM development surface, beginning with the
first foundational bricks. A clean milestone boundary: M8 finished a format,
M9 starts an editor.

## M9 scope / strands

Legend: ✅ done · ⏳ in progress · 📋 plan-ready · 🧭 idea

| Strand | Status | Source |
|---|---|---|
| **i7** — codegen sysreg/mnemonic/form tables from the Go authority (the **first brick**) | 📋 **spec approved** (Pete 2026-06-12; PR #184) — phases A–C queued now; phase D = i74 | `docs/specs/codegen-tables-design.md`; item registry i7, i74 |
| **i74** — i7 phase D: at/ic/barrier operand-table codegen (spec §6 Q3) | 📋 queued — after i7 A–C land | `docs/specs/codegen-tables-design.md` §6; i7 |
| **i75** — B-DOS boot-disk swap (the q10 resolution, incremental) | ✅ done — B-DOS-booted gate suite proven green locally (no Atom Lite attached), shipped/CI default flipped to B-DOS, samdos2 retained via flags | item registry i75; q10; `docs/notes/bdos-trinity-fork-analysis.md` |
| **i41** — editor edit-model implementation (paged block-list) | 📋 design agreed (Pete 2026-06-08, §7 — 5 decisions locked) | `docs/specs/editor-edit-model-design.md` §7; item registry i41 |
| **i48c** — Z80 text→overlay encoder (SAM-side editor input path; absorbs i39c) | 🧭 M9 strand — Go front-end i48b is the authority | `docs/specs/i48-syntactic-encoder-design.md` §2; item registry i48c, i39c |
| **i78** — source-structure preservation (blank lines + comment paragraphs round-trip exactly) | ⏳ **design landed** — blank-run row kind + multi-line-comment grouping decided; indentation deferred behind the i76 interface sign-off. Build host-first (encoding authority) with a corpus round-trip CI gate; Z80 half rides i48c | `docs/specs/source-structure-preservation-design.md`; item registry i78, i76, i48c |
| **i76** — host-side Go TUI editor prototype at SAM-faithful geometry (the functional UX authority; terminal + PNG/SCREEN$ mockup backends; **i4/i5/i6 fold into it** — P1 = the i4 viewer, the mockup backend = i5/q1, the geometry flag + P3 memo = i6) | 🚧 **P1a delivered** — `tools/editor-prototype`: `samscreen` abstraction + tcell terminal + PNG/SCREEN$ mockup backends + i4-parity viewer + full `-geometry` matrix; mockup sheets for all six geometries (`make editor-mockups`). **P1b real-SAM 6×6 font-proof in flight separately.** | `docs/specs/editor-tui-prototype-design.md`; `tools/editor-prototype/`; item registry i76 |
| **i4** — read-only listing/scroll viewer (precursor to i3) | 🧭 carried forward by **i76 P1** | item registry i4, i76; ROADMAP "Editor vision" |
| **i6 / i5** — SAM screen-mode decision + UI visual mockups | 🧭 carried forward by **i76** (q1 resolved 2026-06-12 → the i76 mockup backend) | item registry i5, i6, i76; q1 |
| **i65** — SAM-side save-without-comments (later editor-phase) | 🧭 editor phase | item registry i65; q9 |

## The first brick — i7 codegen tables

The spec (`docs/specs/codegen-tables-design.md`) is approved (Pete 2026-06-12,
PR #184 merged). It generates the Z80 sysreg/mnemonic/form tables from the Go
authority so the Z80 constants follow the encoder tables automatically instead
of being hand-maintained. Approving the spec approved phases A–C2 as specified;
implementation starts now. **Phase D** (at/ic/barrier operand tables) requires
first refactoring `aarch64dec/sys.go` switches into exported data, so it is
deferred by sequencing only — tracked as **i74**, queued after A–C land, so it
automatically gets done rather than silently dropped.

## i75 — B-DOS boot-disk swap (q10 resolution)

q10 resolved (Pete 2026-06-12): **incremental swap.** The mechanics prep landed
in i72 (PR #187 — the `-dos`/`-dos-name`/`-dos-load` flags on `tools/build-disk`)
and the reference binary was vendored in i71 (PR #186 — `reference/bdos/`).
i75 executed the swap: (1) built B-DOS boot-disk variants via the `-dos` flag;
(2) proved the full SimCoupé gate suite green booting B-DOS — including AL 1.5a
behaviour with **no Atom Lite attached** (the previously-untested corner: a
plain floppy machine, no HDF); (3) flipped the shipped/CI default to B-DOS,
retaining samdos2 build capability via the flags.

**Done (i75 PR).** `tools/build-disk`'s default DOS is now B-DOS AL 1.5a
(`reference/bdos/al-bdos15a.bin`, recorded name `bdos`, start address 32777);
the SAMDOS 2 path stays fully functional via `-dos reference/samdos/samdos2.bin
-dos-name samdos2 -dos-load 491529`. Because every gate script calls
`build-disk` with the default DOS, the PR's own CI matrix (all SimCoupé jobs)
boots B-DOS — that matrix is the formal proof. Locally, the full suite was
green booting B-DOS on a plain floppy machine under SimCoupé v1.2.16 (no Atom
Lite / no HDF attached): core 9/9, symbols 5/5 + symbols-prod 5/5, operands
22/22 + operands-prod 22/22, paged + paged-prod, and the release-gate 3-way
byte-match (GNU == Go == Z80) — plus `go test ./...`, the boot self-tests, and
`make check-budget` unchanged.

## i41 — editor edit-model (paged block-list)

Design agreed (opus, Pete 2026-06-08; `editor-edit-model-design.md` §7, 5
decisions locked): **paged block-list** (block = page, intra-block gap buffer,
record-id side-tables) — worst-case edit ~30–80 ms at 400 KB, every op 0–2 page
swaps. The edit-model records **are** the i48 in-memory symbolic IR; the
block-list serialises to the `.tbn` on save/assemble (the `.tbn` is never
mutated in place). M9 builds the core block-list directly (clean serialize seam,
no throwaway MVP).

## i48c — SAM-side text→overlay encoder

The Z80 mirror of the host front-end. The Go i48b syntactic encoder is the
authority — port, don't redesign (CLAUDE.md §6). This is the editor's input
path: text typed on the SAM → overlay records → `.tbn`. It absorbs **i39c**
(overlay slot bitfield-packing polish). The proof point is text→overlay on the
SAM byte-matching the host over the fixture corpus.

## Open questions for Pete (M9)

Open questions live in the milestone-neutral **`docs/notes/question-registry.md`**
(`qN`). None are currently ⏳: **q1 resolved 2026-06-12** (Pete, via the i76
brief — a programmatic SAM-faithful renderer, built as the i76 mockup backend),
which ungates the i4/i6/i5 UI strand via i76. The i76 spec gate cleared
2026-06-12 (approved with amendments — decisions recorded in
`docs/specs/editor-tui-prototype-design.md` §8); the strand is unblocked.

(q1 resolved → i76; q10 resolved at M8 close → i75; q5/q6/q7 resolved during M8.)

## Done-criterion (PROPOSED — Pete may adjust)

A *proposal*, not yet locked. M9 is done when:

- **i7 phases A–C landed** — the Z80 sysreg/mnemonic/form tables are generated
  from the Go authority (i74/phase D may trail);
- **i75 done** — the B-DOS-booted SimCoupé matrix is green and the shipped/CI
  default is flipped to B-DOS;
- **i41 core block-list proven in the harness** — the paged block-list edit model
  serialises round-trip-clean;
- **i48c proven** — text→overlay on the SAM byte-matches the host for the fixture
  corpus;
- **i4 minimal viewer** runs over the real `release.s` source on SimCoupé.

## Standing invariants

- The **release-gate 3-way byte-match** holds throughout (GNU == our Go toolchain
  == our Z80/SAM toolchain over the vendored flattened release source).
- The `.tbn` invariant is **binary-identity + round-trip + `.tbn`-shrinks-or-holds**
  (NOT `.tbn` byte-identity).

## Authoritative references

- Editor edit-model design: `docs/specs/editor-edit-model-design.md`.
- Comment storage design: `docs/specs/comment-storage-design.md`.
- Syntactic encoder (i48) design: `docs/specs/i48-syntactic-encoder-design.md`.
- Codegen tables design (i7): `docs/specs/codegen-tables-design.md`.
- B-DOS Trinity-fork analysis: `docs/notes/bdos-trinity-fork-analysis.md`.
- Global item registry: `docs/notes/item-registry.md`.
- Open-questions registry: `docs/notes/question-registry.md`.
