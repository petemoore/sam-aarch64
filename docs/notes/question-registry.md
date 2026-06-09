# Question index — the project-wide `qN` registry

This is the **canonical, milestone-neutral registry** of every **open question for
Pete** (decisions an agent cannot settle itself, or chose to defer rather than
guess). It is the sibling of the item registry (`docs/notes/item-registry.md`): the
item registry tracks *work* (`iN`), this tracks *unresolved decisions* (`qN`).

**Governance is identical to the item registry — see `docs/notes/item-registry.md`
for the shared registry discipline** (stable ids locked once assigned, never
renumbered, never archived, marked-resolved rather than deleted). In one line for
questions: `qN` ids; the table is the **single sure-fire list of what's still open**;
add the moment a question arises (not just in chat — chat questions get lost in
simultaneous-edit races); flip ⏳→✅ with the resolution + where it was decided the
instant it's answered; land on `main` via PR so Pete sees it where he reads.

This doc is **never archived**: when a milestone status doc is superseded, its
questions live on here, so an open question is never stranded in an archived
`m{N}-status` doc. (Created 2026-06-08 by extracting the `qN` list out of
`m7-status.md` — mirroring the i*N* item-registry extraction in PR #128 — because
M7 was winding down while open questions like q1 were still live. See the
`feedback_capture_open_questions` memory.)

| id | question | status | pointer |
|----|----------|--------|---------|
| **q1** | **i5 graphics tooling** — for editor UI mockups at SAM-accurate resolution: (a) a programmatic SAM-faithful renderer I write (PNG / SAM `SCREEN$` at exact MODE 3 / MODE 4 geometry + the real palette), (b) a dedicated retro/pixel-art tool you drive by hand, or (c) a generic AI image generator? I have no native image gen and a generic generator wouldn't respect the hard SAM constraints, so (a) is the honest best for *faithful* mockups, (b) for hand-authored final art. | ⏳ **OPEN — Pete researching tools the agent can drive**; parked on his side, no agent action meanwhile | item registry i5 |
| **q5** | **i48 host packaging** — once the symbolic `.tbn` is gone: two CLIs over a shared front-end library, or one integrated tool? | ✅ RESOLVED (Pete 2026-06-09) — **one integrated tool.** The real decision is the library factoring (text→overlay encoder / overlay→bytes assembler / overlay→text renderer as shared, dual-target, Go-authoritative libs — those are what port to Z80, not the CLI shape); on top, one host tool **mirrors the SAM's single integrated assembler** (the cleanest port reference), staged modes (`--emit-tbn` / assemble-from-`.tbn`) as flags, production = a single pass-1. The two-binary split was an M1–M6 artifact; it stopped being free once the SAM must do text→overlay itself. | `docs/specs/2026-06-08-i48-single-format-syntactic-encoder-design.md` §3, §8; item registry i48a |
| **q6** | **i48 editor model for value-dependent base words** — at edit time a referenced symbol may be unresolved (e.g. `mov Rd, fwd_label` before the label), so the editor can't always pick the final base word: keep the element tentative/symbolic until assemble, or default-then-validate? Interacts with **i41** (edit-model). | ✅ RESOLVED (Pete 2026-06-08, via the i41 design decision #3 + i48b) — the editor holds the **i48 symbolic IR, not base words**, so there is no edit-time base word to pick; the value-dependent bits are computed in the **fold at serialize/assemble** (i48b), where the symbol pass exists. "Keep symbolic until assemble" — the edit-time concern dissolves. | `docs/specs/2026-06-08-editor-edit-model-design.md` §7.3; i48 spec §4; item registry i41, i48b |
| **q7** | **i48 strictness scope** — we forego GNU's silent `ldr→ldur` and `add lsl #12` auto-rewrite (→ syntactic / error). Any other "generous" GNU rewrites in the corpus to treat the same way? | ⏳ OPEN — sweep with **i48a** (the classifier/front-end refactor that lands the symbolic-mem→error strictness; i48b's byte-affecting `FoldMovzAuto` part shipped 2026-06-09 without it) | i48 spec §4, §8; item registry i48a |
| **q8** | **LLIST tool-cluster disposition** (`tools/llist-*` + `llist-*.sh` + the `llist-normalise` binary under i34) — archive / delete / keep? Superseded by the EDIT/EDKY detokeniser spike. | ⏳ OPEN — punted (Pete 2026-05-29); leave the cluster in place until decided | item registry i34; `tools/README.md` |
| **q2** | Next major strand after M7 → **compact-`.tbn` (i1)**. | ✅ RESOLVED (Pete 2026-06-08) — "let's compact .tbn"; shipped as i1 (PRs #121/#122/#124) | item registry i1 |
| **q3** | Formal GitHub reviews for pre-merge review agents? | ✅ RESOLVED (Pete 2026-06-08) — **YES**; record each pre-merge review with `gh pr review <n> --comment` (not `--approve`); codified in project `CLAUDE.md` §3 | project `CLAUDE.md` §3 |
| **q4** | Work tracking → GitHub Issues, or keep the docs? | ✅ RESOLVED (Pete 2026-06-08) — keep the `iN`/`qN` doc registries, **no GitHub Issues** for now; fully reversible | this file; item registry |

*Pre-`qN` resolved questions (historical, kept for the record):* `src/m3`→flat-`src/`
rename — resolved (PR #82); design-strand handling (agent drives design solo, review
later) — resolved (Pete 2026-05-29). These predate the `qN` convention and are done;
no active id.
