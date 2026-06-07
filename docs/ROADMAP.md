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

- **Milestone:** **M8 active** (`docs/notes/m8-status.md`). M0–M7 ✅ complete. Branch protection requires the status checks defined in `.github/workflows/ci.yml`.
- **Last landed:** i39b-2 editor-region split (PR #153) — compact `.tbn` v2 now has a separable editor region the assembler never reads; binary byte-identity invariant held (GNU == Go == Z80/SAM, 21 752 B).
- **In flight:** repo cleanup i52 (spec: `docs/specs/2026-06-10-repo-cleanup-design.md`, 5 PRs).
- **Open questions:** q1 (i5 graphics — Pete), q8 (LLIST disposition). **Parked:** i50. **Blocked:** i51 (on i40).
- **NEXT ACTION:** i39c (overlay bitfield polish; low priority — fold into the next overlay-decoder touch) then i40 (assembler-side editor-region eviction; unblocks i51). Then the M7 tail: i7 codegen tables, i17 deep reviews, i18 naming.
- Every strand keeps the **assembled-binary byte-identical** invariant (the `m6-release` 3-way byte-match); the i39 invariant is binary-identity + round-trip + `.tbn`-shrinks-or-holds, NOT `.tbn` byte-identity.
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
| M6 | Paged OUT + paged IN + compact `.tbn` format + built-in disassembler (real spectrum4 fixture round-tripping byte-identical via SAM) | `docs/specs/paged-out-design.md` + `docs/specs/paged-in-design.md` | — | ✅ done (PR #76) — paged OUT/IN, `paged_call`, multi-digit local labels, off-axis tables, release-stripped flatten, Go harness all landed (PRs #29-#73).  **Headline "spectrum4 release.bin byte-match on SAM" DONE 2026-05-29**: the full 88 KB release-stripped flows through SimCoupé and the SAM prod assembler HSAVEs a 21752-byte OUT byte-identical to GNU (8 encoder bug classes found+fixed, 358→0; PR #73).  **Closed by PR #76**: the `m6-release` CI gate (a hermetic 3-way byte-match — GNU == our Go toolchain == our Z80/SAM toolchain — over a vendored flattened release source, `tools/run-m6-release-gate.sh`) + the `&C000` code-budget assertion now stand guard.  Compact `.tbn` + on-SAM disassembler + codegen tables move to M7. |
| M7 | Post-M6 consolidation: on-SAM disassembler (strand B) + compact `.tbn` (i1) + codegen tables + repo-cleanup/housekeeping + parity/robustness + Go-harness fidelity | — | — | ✅ done — headlines: on-SAM disassembler (PRs #93–#103), compact `.tbn` i1 (PRs #121–#124, −42.3%). Tail items i7/i17/i18 live in the item registry. |
| M8 | Next-gen compact `.tbn` **v2** — the instruction-overlay format (Format B): assembled-word + zeroed-bits + sparse expression overlay unifying literal/symbolic instructions; header label/offset table; one file with an evictable editor region; name front-coding. Foundation for the on-SAM editor. | `docs/specs/compact-tbn-nextgen-design.md` (design, §7 decisions) + `docs/specs/tbn-binary-format-reference.md` (v1 baseline) | `docs/notes/m8-status.md` | ⏳ **active** — shipped: i39a v2 overlay (#131), i48a host front-end unification (#141/#142/#144), i39b-1 front-coding (#151), i39b-2 editor-region split (#153). Remaining: i39c, i40 (→ unblocks i51), i48c (SAM-side text→overlay encoder). |
| (Phase 2) | On-SAM editor (see "Editor vision" below) | `docs/specs/phase1-assembler.md` §editor + future spec | — | 📋 sketched (M8's v2 format is its foundation) |
| (Phase 3) | TFTP shipper to Pi 400 (Quazar Trinity) over direct LAN cable | `docs/specs/phase3-tftp-design.md` | — | 📋 design direction captured; reference: `simonowen/trinload` |

Legend: ✅ done · ⏳ in progress · 📋 designed, not started

## Editor vision (Phase 2 — design pointers, not yet spec'd)

Captured 2026-05-27 from Pete during the compact-`.tbn` design conversation. These shape the editor's design but aren't on the immediate critical path. Use as inputs when the Phase 2 spec gets written.

- **Inline instruction explanation on demand.** Cursor lands on an instruction; a function-key toggle (e.g. F1) opens an explanation panel showing: what the instruction does in prose, which flags it sets, which registers it reads/writes, the exact bit-fields the encoding occupies. Passive status-line variant: a one-line summary always visible for the current cursor line, expandable on keystroke. Useful both for learning aarch64 and for verifying intent during code review. Synergy with the disassembler-as-inverse-encoder: the same form table that decodes the 4-byte word also tells us the operand semantics, so this is one extra layer over the disassembler rather than a separate database.

- **Register simulator with user-chosen seeds.** Step through instructions one at a time; let the user inject arbitrary bit patterns into source registers (or randomise) and see what the destination registers + flags + relevant memory look like afterwards. Doesn't need to be a full SAM-Coupé-internal Z80 simulator — just a small aarch64 instruction emulator covering the subset we generate. Compounds with the explanation feature: "you typed `lsr x0, x1, #4`; if x1 = 0xCAFEBABE, then x0 becomes 0x0CAFEBAB".

- **Retro UI affordances**: chiptune background music, period-appropriate fonts, animations when entering instructions. The SAM Coupé hardware specifically supports this (256 KB RAM, SAA sound chip via SAASound, palette / mode 3 / 24K screen tricks). The editor is a SAM-resident program, so embracing its native aesthetic is a free win and frames the whole product as a love letter to the platform rather than a transplanted modern IDE. Background-music mechanics researched 2026-05-28; see `docs/notes/sam-music-playback-research.md`.

- **"Why is this instruction here?" navigation**: select an instruction, see all sites that branch to it or read from a register it writes. Inverse-flow visualisation. Same data structures that drive the symbol table already give us this.

- **"Did you mean a simpler instruction?" rewrite hint.** Recognise when an instruction has a *different-encoding* but *result-equivalent* alternative (e.g. `mov Xd, Xn, lsl #n` is equivalent to `lsl Xd, Xn, #n` even though they assemble to different opcodes — ORR vs UBFM). When the cursor sits on such an instruction, the status line flags the equivalence; a dedicated keystroke (e.g. `R` for Rewrite) accepts the substitution and bytes change, anything else leaves the original encoding intact. Educational ("huh, these are interchangeable"), not a performance hint — modern OoO ARM cores treat both as 1-cycle ALU ops with negligible runtime difference. Captured 2026-05-27 from the canonical-aliases survey discussion; the only actual occurrence in the spectrum4 corpus was patched away (`libextra/_display_sysvar.s:126`) so it's truly a future-editor feature, not an active pain point.

- **System-register documentation surfaced from the cursor line.** When the cursor sits on `msr` / `mrs` / `dc` / `tlbi`, a function key (e.g. F2) opens a panel showing the structured ARM definition: bit-field layout of the named register, reset value, accessibility per Exception Level, trap conditions, prose semantics per field. Within the panel, arrow keys navigate between fields; ESC closes. Source: ARM's freely-licensed Machine-Readable Architecture (MRA) XML at `developer.arm.com/architectures/cpu-architecture/a-profile/exploration-tools` — same data Linux's `arch/arm64/tools/sysreg` script consumes. Two layers: (a) **embed the MRA subset** the spectrum4 corpus touches (~30-50 regs: SCTLR_EL1, TTBR0/1_EL1, MAIR_EL1, TCR_EL1, VBAR_EL1, ESR_EL1, FAR_EL1, ELR_EL1, SPSR_EL1, MIDR_EL1, MPIDR_EL1, ID_AA64* family, CNTFRQ_EL0, CNTVCT_EL0, CNTP_*, DAIF, DC/IC/TLBI op vocabulary) for authoritative coverage; (b) **handcrafted editorial overlay** for the ~10 most-touched regs, with opinionated prose that's friendlier than ARM's spec language. Composes with the explanation panel — `msr SCTLR_EL1, x0` with F1 shows what the *instruction* does, F2 dives into what SCTLR_EL1's *bits* mean (with the current x0 value highlighted across them if you've stepped a simulation that far).

- **The unifying north star**: the editor is **not just a mechanical tool but a thoughtful guide**. Each feature above (explanation, simulator, register docs, did-you-mean) advances that framing. Design decisions through the M6 → Phase 2 transition should ask "does this help someone *learn* aarch64 by working in the editor, or just produce bytes?". When in doubt, favour the former even at small code/perf cost — the SAM Coupé is a hobbyist machine and the audience is people who *want* to understand the metal.

- **Interaction model: 1980s keyboard-driven, not pointer-era.** The SAM has a mouse port but mouse use was rare and software support is thin; the audience for a SAM-resident editor reads as keyboard-native. Affordances throughout should be: cursor lands on a line → status-line / side-panel shows context passively; function keys (F0-F9) toggle deeper views; single-letter modal commands trigger actions; ESC closes overlays; no pointer cursor, no hover, no click. Idiom references: BBC BASIC line editor, Tasword, original ZX `EDIT` (Caps Shift+1) line edit. The on-SAM UI should feel like it belongs to its decade.

- **Replay-on-edit**: when an instruction is changed, re-run the register simulator from the most recent label and show what changed downstream. Tight feedback loop for understanding the blast radius of an edit.

- **Edit-model design pointer (i41).** The `.tbn` is the *serialized* storage/assembly form; the editor edits a separate insertion-friendly in-memory document model (gap buffer / piece table / record linked-list) and serializes to `.tbn` on save — the `.tbn` is never mutated in place. This keeps insert latency local (no whole-buffer byte-shift) within the ~1 s/edit bound even at ~400 KB source. See **i41** in `docs/notes/item-registry.md` for the rationale.

These should NOT compete with the M6 / Phase 1 critical path. They're explicitly Phase 2+ surface.

## Design notes not strictly inside a milestone

These are patterns or research findings that get applied *within* milestones rather than constituting their own milestone:

| Note | When to apply | Status |
|---|---|---|
| `docs/ARCHITECTURE.md` | **The first read**: the synthesized system overview (system shape, authority model, encoder tables, memory + paging, `.tbn` v2, build/test pipeline, dev inner loop). Each section links its deep spec. | ✅ living overview |
| `docs/specs/tbn-binary-format-reference.md` | The single authoritative reference for the complete `.tbn` binary encoding (header, records, operands, expr bytecode, directives, compaction levels). Read/cite this whenever touching the format; supersedes the retired M1 spec's tables. | ✅ living reference |
| `docs/specs/samdos-file-io.md` | Reference for any SAMDOS file read/write: the HLOAD trampoline pattern (READ), the HSAVE UIFA pattern (WRITE), hook register-clobbering facts, pre-built Z80 snippets. | ✅ living reference |
| `docs/notes/sam-stub-audit.md` | Already applied (PR #13 loader fix used findings). Keep as reference for any future SAMDOS hook work. | ✅ applied; reference |
| `docs/notes/sam-paging.md` | Reference for any LMPR/HMPR work. | ✅ reference |

## Achievements worth keeping visible

- **2026-05-29: M6 complete** — spectrum4 release.bin byte-match on SAM (PRs #29-#73 mechanism + encoder fixes; **#76** the CI gate).  `release-stripped.tbn` (88 644 B Mac-side `text2bin -flatten -strip-comments`) flows through the SAM prod assembler (paged IN across pages 7-12, ENCTAB on page 4, OUT on pages 5-6, sysreg tables on page 13 via `paged_call`) and HSAVEs a 21 752-byte OUT byte-identical to GNU `as + ld + objcopy`.  The `m6-release` GH Actions job is the standing gate — a hermetic **3-way byte-match** (`tools/run-m6-release-gate.sh`): the vendored flattened release source (`tests/m6/release/release.s`, `text2bin -E` output) is assembled by both our Go toolchain (text2bin + refenc) and our Z80/SAM toolchain (SimCoupé), and both are `cmp`'d against the vendored GNU `release.img` — needs no spectrum4 checkout / `tup` / aarch64 binutils.  The same job runs `make check-budget`, which fails the build if either assembler variant grows into the `&C000` stack page (the silent-boot-hang cliff).  Refresh the fixture with `tools/revendor-m6-release.sh`.  See `https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/m6-status.md`.
- **2026-05-27: M5 complete** (PRs #29, #30, #31, #32, #33, this PR). The SAM-side Z80 assembler now handles the full compound-operand grammar (shifted register, extended register, all seven memory addressing shapes, system registers, literal pool) plus the remaining directives (`.set` / `.equ`, `.balign` / `.align`, `.org`, `.skip` / `.space`, `.inst`, `.ltorg`, plus `.global` / `.section` / `.arch` / `.cpu` no-ops), and the `ror`-imm intercept.  19/19 M5 fixtures byte-match GNU end-to-end via SimCoupé.  Code budget lever (PR #31) paged ENCTAB out of section C, freeing &A000-&AFFF for code (8 → 12 KB production budget).  CI gates via `m5` (test) + `m5-prod` (production) GH Actions jobs.  See `https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/m5-status.md`.
- **2026-05-27: production/test M3 build split** (PR #25). `m3-asm` (test variant, includes self-tests) coexists with `m3-asm-prod` (no self-tests, 1977 bytes smaller). Production has 2115 bytes free in the 8 KB code budget (vs 138 bytes for test); the gap is M5's runway. Both variants byte-match GNU on every fixture; CI verifies via `m4` (test) + `m4-prod` (production).
- **2026-05-27: printer-channel fast-fail** (PR #24). `fail:` now emits "FAIL\n" via PRINTL1 and `DI; HALT`s cleanly instead of spinning until the wrapper's 30 s timeout. Failure detection in ci-m3 drops from ~270 s to ~12 s (23×). Wrapper captures the banner via SimCoupé `-parallel1 1 -outpath` and the round-trip scripts grep `^OK$` before bothering with the byte compare.
- **2026-05-27: M4 complete** (PRs #21, #22, #23). The SAM-side Z80 assembler now does symbol resolution, local-label resolution, full expression evaluation (PUSH_SYM / PUSH_LOCAL / PUSH_PC / REL_*), two-pass assembly, and PC-relative branch / adrp encoding. 4/4 M4 fixtures byte-match GNU `as + ld -Ttext=0 + objcopy` end-to-end via SimCoupé. See `https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/m4-status.md`.
- **2026-05-26: byte-identical spectrum4 release.img** (PR #15). Our toolchain produces a 21,752-byte `release.bin` that exactly matches GNU `as → ld → objcopy -O binary` on the release target. See `docs/notes/2026-05-26-release-bytematch.md` (TBD) and the memory entry `[spectrum4-release-bytematch-achieved]`.
- **2026-05-26: full spectrum4 preprocessing** (PR #14). `text2bin` consumes `release.target` end-to-end via `.include` / `.if` / `.macro` / `\arg`.
- **2026-05-26: M3 Tasks 1-12** — all 9 slot encoders ported to Z80 (PRs #9, #12, #16).
- **2026-05-26: M3 loader fixed** (PR #13). HGTHD+HLOAD replaces broken HGFLE+LBYT.

## How to extend this doc

When new deferred work or a design decision arises:
1. Add an `iN` row in `docs/notes/item-registry.md` (or `qN` in `docs/notes/question-registry.md` for open questions). That is the one home — do not add a checklist here.
2. Add the new design note to the appropriate table above (milestone, or "Design notes not strictly inside a milestone") and link it from `docs/specs/README.md` if it lives under `docs/specs/`.

The goal: no design discussion is ever lost to context drift. If it's worth writing down, it's worth a registry row.
