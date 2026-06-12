# sam-aarch64 roadmap

<!-- HANDOVER-PROTOCOL-START -->
## START HERE — session handover protocol (read this first)

**This file is the canonical entry point and the agent→agent handover contract.** If you are an agent picking up this project: read this section, then the **"Current State & Next Actions"** block below, then the current milestone status doc, then the project memory index (auto-loaded at session start). That is the complete start-up read — everything else is reached via links from here.

### The contract (always inherited, always maintained)

1. **Tracking is part of "done."** A new idea, decision, or deferred item is not captured until it is an `iN` row in the item registry (or `qN` in the question registry) — **in the same change that introduces it**. Treat a missing tracking entry like a missing test: reviewers flag it. This is the rule that stops ideas being lost to context drift.
2. **One home per kind of thing** (this is how the docs stay un-sprawled):
   - **ROADMAP.md** (this file) — the index, the backlog, and the live "Current State" below.
   - **Item registry** (`docs/notes/item-registry.md`) — the project-wide, milestone-neutral `iN` id↔item map. Never archived. Its header documents the **shared registry discipline for both `iN` items and `qN` questions**.
   - **Question registry** (`docs/notes/question-registry.md`) — the project-wide, milestone-neutral `qN` open-questions-for-Pete map. Never archived; sibling of the item registry (same discipline). Every question for Pete goes here the moment it arises.
   - **Milestone status docs** (`docs/notes/m{N}-status.md`) — the per-milestone source of truth.
   - **Memory** (`~/.claude/projects/.../memory/`) — cross-session preferences, facts, feedback. *Not* work-tracking.
   - Superseded docs are **deleted** — git history is the archive; pin citations to a blob link if a living doc must cite one.
3. **At milestone close**, walk the item and question registries and ask of each item: still deferred, or does it now fold into the milestone in flight? **The `iN` item registry and the `qN` question registry are both milestone-neutral (`docs/notes/item-registry.md`, `docs/notes/question-registry.md`) and never archived:** at milestone close, every item belonging to that milestone must be marked ✅ done or ❌ wontfix in the registry, or migrated (re-pointed) to the active milestone's strands — no item may be left living only in a closing `m{N}-status` doc. Open questions for Pete live only in the question registry (`qN`) for the same reason. **The milestone status doc is deleted at close** (git history is the archive) after the registry walk completes.
4. **Delegate to subagents to preserve orchestration context.** The agent driving a session (the *orchestrator*) should push self-contained, well-scoped work out to subagents — codebase searches, mechanical edits, doc drafting, focused investigations, even whole low-risk PRs (implement → verify locally → commit → push → open PR) — via the `Agent` tool (and `Explore` / `Plan`), keeping only the conclusions in its own window. A subagent's full transcript is discarded; only its final summary returns, so the orchestrator's context lasts far longer. The orchestrator keeps the judgement calls itself: sequencing, monitoring CI, merge/launch decisions, and anything user-facing. The durable state lives in subagents-plus-these-docs, not in any one agent's window — so when context *does* fill, the handover (update "Current State", below) lets the next agent resume as orchestrator with nothing lost. Prefer launching independent subagents in parallel (one message, multiple `Agent` calls) when the work has no shared state.

### Session hygiene (the minimal ritual)

- **Closing a session:** **no magic words** — *any* wind-down phrasing ("close the session", "let's wrap up", "prepare for handover", "goodnight, what's next?") triggers this. The agent then updates the **"Current State & Next Actions"** block below *in place* — what landed, what's in flight (open PRs / branches / running agents), and the single immediate next action; confirms tracking is current (rule 1); and leaves the working copy on a clean, current `main`. Do **not** write a new dated handoff doc — update the standing block. **Get this update merged to `main` before handover** — a quick docs PR + CI, since branch protection blocks direct pushes. Never hand over with the live state stuck in an open PR, or the next session reads stale context.
- **Starting a session:** the standard start prompt is just **"Continue per docs/ROADMAP.md."** A SessionStart hook (`tools/session-handover.sh`) runs first: it surfaces this section and, **when you are on a clean `main`, auto-`fetch`es and fast-forwards to the latest** so the doc you read is current. If it instead **warns** (you are on a feature branch, have uncommitted changes, or `main` can't fast-forward), refresh deliberately before relying on the doc: stash/commit your work, then `git checkout main && git pull --ff-only`. Then read this section → Current State → resume; no bespoke per-session prompt is needed.

### Current State & Next Actions

*Updated in place each session — this is the live handover. Keep it ≤15 lines; history lives in the milestone status docs, the registries, and `git log`.*

- **Milestone:** **M9 active — the editor era** (`docs/notes/m9-status.md`). M0–M8 ✅ complete. Branch protection requires the status checks defined in `.github/workflows/ci.yml`.
- **Last landed:** M8 closed + M9 opened (#188, this PR) after the 2026-06-12 triage; i7 spec approved + merged (#184). Overnight batch before that: i54 (#183), i17 fixes (#185), i71 B-DOS reference (#186), i72 reorientation (#187).
- **In flight:** i7 phase A + i75 B-DOS-boot proof — launching as agent PRs immediately after this merges.
- **Open questions:** q1 (i5 graphics — Pete; gates the i4/i6 UI strand only).
- **NEXT ACTION:** land i7 phases A–C (then i74) and i75; then i41 edit-model implementation + i48c per `docs/notes/m9-status.md`.
- Every strand keeps the **assembled-binary byte-identical** invariant (the `release-gate` 3-way byte-match); the i39 invariant is binary-identity + round-trip + `.tbn`-shrinks-or-holds, NOT `.tbn` byte-identity.
<!-- HANDOVER-PROTOCOL-END -->

Canonical index of milestones, design specs, and deferred work. **Update this any time a design doc is added, a milestone changes state, or deferred work gets folded into a milestone.**

## Vision

A SAM Coupé Z80 program that hosts a complete aarch64 development workflow — editor, assembler, disassembler, TFTP shipper — and produces byte-identical binaries to GNU `aarch64-none-elf-as` for the spectrum4 kernel. Daily-driver SAM-as-development-machine for Raspberry Pi 400 bare-metal work.

See `docs/specs/vision.md` for the long-form pitch and `docs/specs/phase1-assembler.md` for the Phase 1 (assembler) shape.

## Milestone status

| M | Title | Spec | Status doc | State |
|---|---|---|---|---|
| M0 | Toolchain bootstrap (pyz80 → SimCoupé → samfile → GNU `as` round-trip) | — | — | ✅ done (PR #1) |
| M1 | Binary tokenised source format (`.tbn`) + text2bin / bin2text | — | — | ✅ done (PR #6) |
| M2 | Encoder tables + Mac-side refenc; 20/20 M1 fixtures byte-match GNU | — | — | ✅ done (PR #7, #8); extended via #11, #14, #15, #17 |
| M3 | Z80 emitter: read `.tbn`, encode, HSAVE output (no symbol table; constant-only) | — | — | ✅ done (PRs #9, #12, #13, #16, #17, #19); 9/9 fixtures byte-match GNU end-to-end via SimCoupé |
| M4 | Symbol table, multi-pass, full expression evaluator on Z80 | — | — | ✅ done (PRs #21, #22, #23); 4/4 M4 fixtures byte-match GNU end-to-end via SimCoupé |
| M5 | Compound operands (`OpShiftedReg`, `OpExtendedReg`, `OpMem`, `OpSysName`, `OpLitPool`) + remaining directives (`.set`/`.equ`, `.balign`/`.align`, `.org`, `.skip`/`.space`, `.inst`, `.ltorg`) + `ror`-imm intercept | — | — | ✅ done (PRs #29–#34); 19/19 M5 fixtures byte-match GNU end-to-end via SimCoupé |
| M6 | Paged OUT + paged IN + compact `.tbn` format + built-in disassembler (real spectrum4 fixture round-tripping byte-identical via SAM) | `docs/specs/paged-out-design.md` + `docs/specs/paged-in-design.md` | — | ✅ done (PR #76) — paged OUT/IN, `paged_call`, multi-digit local labels, off-axis tables, release-stripped flatten, Go harness all landed (PRs #29-#73).  **Headline "spectrum4 release.bin byte-match on SAM" DONE 2026-05-29**: the full 88 KB release-stripped flows through SimCoupé and the SAM prod assembler HSAVEs a 21752-byte OUT byte-identical to GNU (8 encoder bug classes found+fixed, 358→0; PR #73).  **Closed by PR #76**: the `release-gate` CI gate (a hermetic 3-way byte-match — GNU == our Go toolchain == our Z80/SAM toolchain — over a vendored flattened release source, `tools/run-release-gate.sh`) + the `&C000` code-budget assertion now stand guard.  Compact `.tbn` + on-SAM disassembler + codegen tables move to M7. |
| M7 | Post-M6 consolidation: on-SAM disassembler (strand B) + compact `.tbn` (i1) + codegen tables + repo-cleanup/housekeeping + parity/robustness + Go-harness fidelity | — | — | ✅ done — headlines: on-SAM disassembler (PRs #93–#103), compact `.tbn` i1 (PRs #121–#124, −42.3%). Tail items i7/i17/i18 live in the item registry. |
| M8 | Next-gen compact `.tbn` **v2** — the instruction-overlay format (Format B): assembled-word + zeroed-bits + sparse expression overlay unifying literal/symbolic instructions; header label/offset table; one file with an evictable editor region; name front-coding. Foundation for the on-SAM editor. | `docs/specs/compact-tbn-nextgen-design.md` (design, §7 decisions) + `docs/specs/tbn-binary-format-reference.md` (v1 baseline) | — | ✅ done (closed 2026-06-12) — v2 overlay format shipped end-to-end: i39a/#131, i39b-1/#151 + i39b-2/#153, i40 + i51/#181, i48 host side/#141–#144. The B-DOS era opened at close: i71/#186 (reference vendored) + i72/#187 (`build-disk -dos` prep). Residuals migrated to M9 (i39c → i48c). |
| M9 | Editor era — Phase 2 foundations: codegen tables (i7), B-DOS boot-disk swap (i75), paged block-list edit model (i41), SAM-side text→overlay encoder (i48c, absorbs i39c), read-only viewer (i4), UI decisions (i6/i5/q1) | `docs/specs/editor-edit-model-design.md` + `docs/specs/comment-storage-design.md` + `docs/specs/codegen-tables-design.md` | `docs/notes/m9-status.md` | ⏳ active |
| (Phase 2) | On-SAM editor | `docs/specs/phase1-assembler.md` §editor + `docs/specs/editor-vision.md` (design pointers) + future Phase-2 spec | — | ⏳ started — M9 carries the foundations |
| (Phase 3) | TFTP shipper to Pi 400 (Quazar Trinity) over direct LAN cable | `docs/specs/phase3-tftp-design.md` | — | 📋 design direction captured; reference: `simonowen/trinload` |

Legend: ✅ done · ⏳ in progress · 📋 designed, not started

## Design notes not strictly inside a milestone

These are patterns or research findings that get applied *within* milestones rather than constituting their own milestone:

| Note | When to apply | Status |
|---|---|---|
| `docs/ARCHITECTURE.md` | **The first read**: the synthesized system overview (system shape, authority model, encoder tables, memory + paging, `.tbn` v2, build/test pipeline, dev inner loop). Each section links its deep spec. | ✅ living overview |
| `docs/specs/tbn-binary-format-reference.md` | The single authoritative reference for the complete `.tbn` binary encoding (header, records, operands, expr bytecode, directives, compaction levels). Read/cite this whenever touching the format; supersedes the retired M1 spec's tables. | ✅ living reference |
| `docs/specs/samdos-file-io.md` | Reference for the DOS file-I/O hook layer (SAMDOS 2 / B-DOS): the HLOAD trampoline pattern (READ), the HSAVE UIFA pattern (WRITE), hook register-clobbering facts, pre-built Z80 snippets. | ✅ living reference |
| `docs/notes/sam-stub-audit.md` | Already applied (PR #13 loader fix used findings). Keep as reference for any future DOS hook work. | ✅ applied; reference |
| `docs/notes/sam-paging.md` | Reference for any LMPR/HMPR work. | ✅ reference |

## How to extend this doc

When new deferred work or a design decision arises:
1. Add an `iN` row in `docs/notes/item-registry.md` (or `qN` in `docs/notes/question-registry.md` for open questions). That is the one home — do not add a checklist here.
2. Add the new design note to the appropriate table above (milestone, or "Design notes not strictly inside a milestone") and link it from `docs/specs/README.md` if it lives under `docs/specs/`.

The goal: no design discussion is ever lost to context drift. If it's worth writing down, it's worth a registry row.
