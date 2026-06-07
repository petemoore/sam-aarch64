# Repo Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. All subagents: `model: fable` (Pete, 2026-06-10).

**Goal:** Execute `docs/specs/2026-06-10-repo-cleanup-design.md` — five sequential PRs that delete historical docs/spikes, add ARCHITECTURE.md + directory READMEs, consolidate tracking onto the `iN`/`qN` registries, and rename milestone-named artifacts to functional names with a test-inventory manifest proving nothing was dropped.

**Architecture:** All work happens in the worktree `~/git/sam-aarch64-doc-cleanup` (sibling dir, immune to `git clean -fdx` in the main checkout). Each PR: branch → commits → push → `gh pr create` (ready, not draft) → CI green → §3 pre-merge review subagent (fable) → verdict via `gh pr review --comment` → `gh pr merge --merge --delete-branch` → sync worktree to new `main`. PR 5 additionally runs the branch-protection union procedure.

**Tech stack:** `g` (never `git`) for all git ops; commits authored as Pete, no Co-Authored-By; `gh` for PRs/API; bash + grep for sweeps; `go`/`gh api` for the manifest.

**Standing rules (apply to every task):**
- Use `g`, not `git`. PR bodies: one line per paragraph (no hard-wrap), short and human.
- Commit-pinned links use the pre-cleanup main sha `c0f62fa` — format `https://github.com/petemoore/sam-aarch64/blob/c0f62fa/<path>`.
- After each merge: `cd ~/git/sam-aarch64-doc-cleanup && g checkout main && g pull --ff-only && g checkout -b <next-branch>`.
- The rename vocabulary (fixed by the spec): m1→`format`, m2→`encoder`, m3→`core`, m4→`symbols`, m5→`operands`, m6→`paged`, m6-release→`release-gate`.

---

## PR 1 — stale-fact fixes (branch: `repo-cleanup-spec`, already exists with the spec commit)

**Files:** Modify `README.md`, `docs/ROADMAP.md`, `CLAUDE.md`, `.github/workflows/ci.yml`, `tools/README.md`, `docs/notes/item-registry.md`. This plan file is also committed here.

### Task 1.1: Commit this plan

- [ ] **Step 1:** `cd ~/git/sam-aarch64-doc-cleanup && g add docs/plans/2026-06-10-repo-cleanup.md && g commit -m "docs: add repo-cleanup implementation plan"`

### Task 1.2: README.md — correct the Status section and layout

- [ ] **Step 1:** Replace the Status section body (lines 17–42, from `M0–M6 are complete` through the M0 historical-note blockquote) with:

```markdown
M0–M7 are complete; **M8 is the active milestone** (see
`docs/notes/m8-status.md`). The SAM-side Z80 assembler, running in
SimCoupé, byte-matches GNU `as + ld -Ttext=0 + objcopy -O binary`
end-to-end for every fixture corpus, and assembles the **full
spectrum4 `release.bin` (21 752 bytes) byte-identical to GNU** on
real SAM paging. A full aarch64 **disassembler** runs on the SAM
(oracle-verified word-for-word against the Go authority), and the
source format is the compact **`.tbn` v2** instruction-overlay
(44 KB for the full release, with a separable editor region — the
foundation for the Phase 2 on-SAM editor). The `release-gate` CI job
stands guard as a hermetic 3-way byte-match (GNU == our Go toolchain
== our Z80/SAM toolchain). See `docs/ROADMAP.md` for the milestone
index and `docs/specs/` for design documents.

The round-trip gates pass under every environment we exercise:
GitHub Actions on `ubuntu-latest` (inside the dev image published to
`ghcr.io/petemoore/sam-aarch64-dev` on every push), the dev image
locally under Docker on both `linux/amd64` and `linux/arm64`, and
natively on macOS against a locally-built patched SimCoupé.
```

(Note: this text says `release-gate`; until PR 5 lands the job is still `m6-release`. Use `m6-release` in PR 1 and let PR 5's sweep rename it — i.e. write `The `m6-release` CI job` here, and PR 5 Task 5.8 updates it.)

- [ ] **Step 2:** In the Repository layout block (lines 69–90): delete the `docs/aarch64/` and `docs/trinity/` lines; change the `notes/` line to `├── notes/        Technical references (paging, disk format) + the iN/qN registries`; add `scripts/          Build-gate helpers (code budget, release pipeline)` after `tools/`; add `├── samdos/          SAMDOS 2 binary (disk building)` under `reference/`.
- [ ] **Step 3:** In Local development, leave the `make ci-m3 ...` command as-is (renamed in PR 5).
- [ ] **Step 4:** `g add README.md && g commit -m "docs: correct README status (M8 active) and repository layout"`

### Task 1.3: ROADMAP.md — replace the Current State block, fix the milestone table

- [ ] **Step 1:** Replace everything from line 26 (`### Current State & Next Actions`) through line 61 (the `M7 backlog` bullet), i.e. the whole block above `<!-- HANDOVER-PROTOCOL-END -->`, with:

```markdown
### Current State & Next Actions

*Updated in place each session — this is the live handover. Keep it ≤15 lines; history lives in the milestone status docs, the registries, and `git log`.*

- **Milestone:** **M8 active** (`docs/notes/m8-status.md`). M0–M7 ✅ complete. Branch protection requires the status checks defined in `.github/workflows/ci.yml`.
- **Last landed:** i39b-2 editor-region split (PR #153) — compact `.tbn` v2 now has a separable editor region the assembler never reads; binary byte-identity invariant held (GNU == Go == Z80/SAM, 21 752 B).
- **In flight:** repo cleanup i52 (spec: `docs/specs/2026-06-10-repo-cleanup-design.md`, 5 PRs).
- **Open questions:** q1 (i5 graphics — Pete), q8 (LLIST disposition). **Parked:** i50. **Blocked:** i51 (on i40).
- **NEXT ACTION:** i39c (overlay bitfield polish; low priority — fold into the next overlay-decoder touch) then i40 (assembler-side editor-region eviction; unblocks i51). Then the M7 tail: i7 codegen tables, i17 deep reviews, i18 naming.
- Every strand keeps the **assembled-binary byte-identical** invariant (the release-gate 3-way byte-match); the i39 invariant is binary-identity + round-trip + `.tbn`-shrinks-or-holds, NOT `.tbn` byte-identity.
```

- [ ] **Step 2:** Milestone table: change the M7 row State cell to `✅ done — headlines: on-SAM disassembler (PRs #93–#103), compact `.tbn` i1 (PRs #121–#124, −42.3%). Tail items i7/i17/i18 live in the item registry.` Change the M8 row State cell to `⏳ **active** — shipped: i39a v2 overlay (#131), i48a host front-end unification (#141/#142/#144), i39b-1 front-coding (#151), i39b-2 editor-region split (#153). Remaining: i39c, i40 (→ unblocks i51), i48c (SAM-side text→overlay encoder).`
- [ ] **Step 3:** Line 6: replace the literal memory-index path with `the project memory index (auto-loaded at session start)`.
- [ ] **Step 4:** `g add docs/ROADMAP.md && g commit -m "docs: replace ROADMAP current-state accretion log with a slim live block; correct milestone table"`

### Task 1.4: CLAUDE.md — check counts, pointers, memory path

- [ ] **Step 1:** In §2 (merge commits): `requires the 11 CI status checks` → `requires the CI status checks defined in .github/workflows/ci.yml`.
- [ ] **Step 2:** In "Pointers for first-session": `latest docs/notes/m{6,5,4,3}-status.md files` → `docs/notes/m8-status.md (active milestone) + docs/notes/item-registry.md / question-registry.md`.
- [ ] **Step 3:** In "Scope discipline": replace the literal `-Users-pmoore-` memory paths with the host-neutral phrasing `~/.claude/projects/<this-repo's-path-slug>/memory/` and note the path varies by host (Pete now also works from a Linux host). Flag this change explicitly in the PR body for Pete's eyes.
- [ ] **Step 4:** `g add CLAUDE.md && g commit -m "docs: fix CLAUDE.md check-count, milestone pointers, memory paths"`

### Task 1.5: ci.yml header comment + tools/README q8 pointer

- [ ] **Step 1:** Replace ci.yml lines 8–19 (the `Workflow has two jobs` comment) with:

```yaml
# build-image builds the dev image from tools/Dockerfile.dev and pushes it
# to ghcr.io (multi-arch via QEMU; registry-mode buildx cache keeps
# nothing-changed runs cheap). Every other job is a gate that pulls that
# image by sha tag and runs inside it — see the `jobs:` map below for the
# authoritative list (round-trip corpora, disassembler oracle + round-trip,
# static checks, and the release byte-match gate).
```

- [ ] **Step 2:** tools/README.md line 44: `see the LLIST open question in \`docs/notes/m7-status.md\`` → `tracked as **q8** in \`docs/notes/question-registry.md\``.
- [ ] **Step 3:** `g add .github/workflows/ci.yml tools/README.md && g commit -m "docs: refresh stale ci.yml header comment; point LLIST question at q8"`

### Task 1.6: Register the cleanup as i52

- [ ] **Step 1:** Read `docs/notes/item-registry.md`, match its row format exactly, and append: id **i52**, title `Repo cleanup: docs restructure + tracking consolidation + tooling hygiene + functional renames`, status ⏳ in progress, link `docs/specs/2026-06-10-repo-cleanup-design.md`.
- [ ] **Step 2:** `g add docs/notes/item-registry.md && g commit -m "tracking: register repo cleanup as i52"`

### Task 1.7: Ship PR 1

- [ ] **Step 1:** `g push -u origin repo-cleanup-spec`
- [ ] **Step 2:** `gh pr create --title "i52 PR 1/5: repo-cleanup spec + plan + stale-fact fixes" --body "<one-line-per-paragraph body: what the cleanup is, what this PR fixes (stale README/ROADMAP/CLAUDE.md/ci.yml facts), pointer to the spec, note the memory-path wording change for Pete>"`
- [ ] **Step 3:** `gh pr checks <N> --watch` until all green (investigate + fix failures autonomously).
- [ ] **Step 4:** Dispatch the §3 pre-merge review subagent (fable) with the CLAUDE.md checklist; post its verdict via `gh pr review <N> --comment`; any RED blocks merge.
- [ ] **Step 5:** `gh pr merge <N> --merge --delete-branch`, then sync the worktree to main and branch `cleanup-2-docs`.

---

## PR 2 — docs restructure (branch: `cleanup-2-docs`)

**Order matters: salvage first, delete second, then new docs, then the link sweep.**

### Task 2.1: Salvage — merged SAMDOS file-io spec

- [ ] **Step 1:** Read `docs/specs/2026-05-27-samdos-load-idiom.md` + `docs/specs/2026-05-27-samdos-save-idiom.md`. Create `docs/specs/samdos-file-io.md` merging both: background, the HLOAD trampoline READ pattern, the HSAVE WRITE pattern, hook register-clobbering facts, the pre-built Z80 snippets. Present tense, no milestone narrative.
- [ ] **Step 2:** `g rm` the two source specs. Update the ROADMAP "Design notes" table row to point at `docs/specs/samdos-file-io.md`.
- [ ] **Step 3:** `grep -rn 'samdos-load-idiom\|samdos-save-idiom' --include='*.md' --include='Makefile' --include='*.sh' --include='*.yml' .` → update every hit (kept docs only; hits inside files this PR deletes can be ignored).
- [ ] **Step 4:** Commit: `docs: merge SAMDOS load/save idioms into one file-io reference`.

### Task 2.2: Salvage — registry entries for surviving decisions

- [ ] **Step 1:** Bump-arena: add a registry item (next free id, expected i53): `Bump-arena reclamation — ❌ YAGNI per census; revisit only if the census §4 trigger fires (fixed section-D array overflows at >5× kernel scale). Census: https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-29-bump-arena-risk-census.md`.
- [ ] **Step 2:** Synthetic-fixture-sweep recommendation (from the capability-parity note): `grep -n 'synthetic' docs/notes/item-registry.md` — if no item covers it, add one (next free id) citing the blob-pinned note.
- [ ] **Step 3:** Trim the q7 findings appendix from `docs/notes/question-registry.md` (keep q7's one-line resolution row).
- [ ] **Step 4:** Commit: `tracking: preserve bump-arena trigger + parity-sweep recommendation as registry items`.

### Task 2.3: Salvage — CLAUDE.md citations → commit-pinned links

- [ ] **Step 1:** `grep -n 'docs/notes/2026\|docs/notes/m[0-9]' CLAUDE.md` — for each hit whose target this PR deletes (expect: `2026-05-28-test-variant-ci-regression.md`, `2026-05-28-test-harness-bakeoff-evaluation.md`), replace the path with the `blob/c0f62fa/` link.
- [ ] **Step 2:** Commit: `docs: pin CLAUDE.md incident citations to immutable blob links`.

### Task 2.4: Re-sync memory-layout.md

- [ ] **Step 1:** Read the header block of `src/assembler.asm` (the authoritative map) and `docs/notes/memory-layout.md`. Update the mirror to match exactly; set its last-synced date to 2026-06-10.
- [ ] **Step 2:** Commit: `docs: re-sync memory-layout mirror against src/assembler.asm`.

### Task 2.5: Write docs/ARCHITECTURE.md

- [ ] **Step 1:** Write `docs/ARCHITECTURE.md` (~300–500 lines), sections:
  1. **What this is** — one paragraph + the Phase 1/2/3 shape (link vision + phase1 specs).
  2. **System shape** — SAM-side Z80 assembler (`src/`, single translation unit, BUILD_TESTS variants, off-axis payloads) + host Go toolchain (`tools/sam-aarch64` integrated binary over `frontend`/`assemble`/`render` libs).
  3. **The authority model** — Go implements first (`aarch64enc`/`aarch64dec` are the encoding/decoding authorities); the Z80 side is a faithful port; SimCoupé is the only CI gate; the Go harness is a dev tool. Link the relevant CLAUDE.md rules.
  4. **Encoder tables** — salvaged from the M2 design: ARM MRA XML → `enctab-gen` → `enctab.enc` mirrored by `aarch64enc/data.go` + hand-curated `manual_forms.go`; ENCTAB_LEN sync rule.
  5. **Memory + paging model** — section map summary (link `docs/notes/memory-layout.md` + `sam-paging.md`), `paged_call` trampoline, paged IN/OUT (link the two M6 paging specs as rationale).
  6. **The `.tbn` v2 format** — one-page summary (link `2026-06-08-tbn-binary-format-reference.md` as normative + the nextgen design for rationale).
  7. **Build + test pipeline** — Makefile variants, disk build, SimCoupé round-trip drivers, the release 3-way byte-match gate, the code-budget gate, CI job map.
  8. **Dev inner loop** — pyz80 + `go test` + the z80 harness boot test; CI as final gate only (salvage the still-true parts of the M1 design §7–§9 test-pyramid prose).
  Sources: the live specs, `src/README.md`, `tools/README.md`, Makefile, ci.yml. Cross-check every factual claim against the current tree, not the audit summaries.
- [ ] **Step 2:** Commit: `docs: add ARCHITECTURE.md — synthesized system overview`.

### Task 2.6: Deletions

- [ ] **Step 1:** Plans — delete all EXCEPT this plan:
```bash
cd ~/git/sam-aarch64-doc-cleanup/docs/plans
g rm 2026-05-09-m0-toolchain-bootstrap.md 2026-05-20-spike-multi-page-loader.md 2026-05-21-simcoupe-sdl-paste.md 2026-05-24-m1-binary-tokenised-format.md 2026-05-24-m2-encoder-tables.md 2026-05-24-m3-z80-emitter.md 2026-05-24-m4-symbols-multipass.md 2026-05-27-m5-compound-operands-directives.md 2026-05-27-m6-paged-in.md 2026-05-27-m6-paged-out.md 2026-05-27-multi-digit-local-labels.md 2026-05-28-go-aarch64-disassembler.md 2026-05-28-plan-pr3-test-corpus-off-axis.md 2026-05-29-m6-closure-release-bytematch.md 2026-06-07-disasm-round-trip.md 2026-06-07-strand-b-pr4-z80-disassembler-port.md 2026-06-08-i39-phase1-instruction-overlay-plan.md 2026-06-09-i39b-nametable-frontcoding-sidecars.md 2026-06-09-i48a-host-frontend-unification.md
```
- [ ] **Step 2:** Specs — delete the historical ones (the two samdos idioms already went in Task 2.1):
```bash
cd ../specs
g rm 2026-05-14-basic-detokeniser-spike-design.md 2026-05-20-spike-multi-page-loader-design.md 2026-05-21-simcoupe-sdl-paste-design.md 2026-05-23-m1-binary-tokenised-format-design.md 2026-05-24-m2-encoder-tables-design.md 2026-05-24-m3-z80-emitter-design.md 2026-05-24-m4-symbols-multipass-design.md 2026-05-25-macro-expansion-research.md 2026-05-27-compact-tbn-and-disassembler-design.md 2026-05-27-m5-compound-operands-directives-design.md 2026-05-29-bump-arena-risk-census.md 2026-06-07-disasm-round-trip-design.md
```
- [ ] **Step 3:** Notes — delete the archive, closed milestone docs, and historical investigations:
```bash
cd ../notes
g rm -r archive
g rm m0-status.md m1-status.md m2-status.md m3-status.md m4-status.md m5-status.md m6-status.md m7-status.md spectrum4-status.md 2026-05-27-disassembly-canonicalisation-survey.md 2026-05-28-eod-session-handoff.md 2026-05-28-hload-16k-limit-investigation.md 2026-05-28-memory-layout-brainstorm.md 2026-05-28-paged-call-architecture.md 2026-05-28-reader-paged-self-test-investigation.md 2026-05-28-spectrum4-instruction-inventory.md 2026-05-28-test-harness-bakeoff-evaluation.md 2026-05-28-test-harness-spike-briefs.md 2026-05-28-test-variant-ci-regression.md 2026-05-28-z80-bounds-check-audit.md 2026-05-28-z80-table-sizing-census.md 2026-05-29-go-harness-fidelity-investigation.md 2026-05-29-go-harness-paged-trap-rootcause.md 2026-05-29-m6-bytematch-encoder-divergences.md 2026-05-29-repo-audit.md 2026-05-29-test-variant-budget-relief.md 2026-05-29-z80-go-parity-audit.md 2026-06-07-disassembler-page-placement.md 2026-06-08-go-vs-z80-disasm-capability-parity.md 2026-06-08-skipped-tests-and-gaps-audit.md 2026-06-08-z80-go-disasm-parity-i9.md comet-encoding-patterns.md fred-disk-inspection.md samfile-capabilities.md samdos2-auto-run-analysis.md
```
- [ ] **Step 4:** Pre-deletion guard: before committing, sweep `m7-status.md` and `m8-status.md`-adjacent docs for any open item living ONLY in a deleted doc: `g show HEAD:docs/notes/m7-status.md | grep -oE 'i[0-9]+' | sort -u` and confirm each id appears in `item-registry.md`. Any orphan → add a registry row before the deletion commit.
- [ ] **Step 5:** Flatten the one-file subdir: `g mv simcoupe-ideas/2026-05-29-paste-driven-control-plane.md simcoupe-paste-control-plane.md` (updates: grep for the old path).
- [ ] **Step 6:** Commit: `docs: delete executed plans, superseded notes, closed milestone docs (git history is the archive)`.

### Task 2.6b: Evergreen renames for kept docs (spec decision 6 — no dated filenames)

- [ ] **Step 1:**
```bash
cd ~/git/sam-aarch64-doc-cleanup/docs/specs
g mv 2026-05-09-vision.md vision.md
g mv 2026-05-09-phase1-assembler.md phase1-assembler.md
g mv 2026-06-08-tbn-binary-format-reference.md tbn-binary-format-reference.md
g mv 2026-06-08-compact-tbn-nextgen-design.md compact-tbn-nextgen-design.md
g mv 2026-06-08-i48-single-format-syntactic-encoder-design.md i48-syntactic-encoder-design.md
g mv 2026-06-08-editor-edit-model-design.md editor-edit-model-design.md
g mv 2026-05-27-m6-paged-in-design.md paged-in-design.md
g mv 2026-05-27-m6-paged-out-design.md paged-out-design.md
g mv 2026-05-27-phase3-tftp-direct-lan-design.md phase3-tftp-design.md
cd ../notes
g mv 2026-05-28-sam-music-playback-research.md sam-music-playback-research.md
g mv 2026-06-08-armv8-a64-isa-footprint-research.md a64-isa-footprint-research.md
```
(The cleanup's own spec/plan keep their dated names — they are deleted in PR 5 anyway.)
- [ ] **Step 2:** Update every reference to each old name: `for n in <old names>; do grep -rln --exclude-dir=.git "$n" .; done` → fix all hits (ROADMAP doc index, CLAUDE.md, m8-status.md, registries, kept specs' cross-references).
- [ ] **Step 3:** Adjust Task 2.6 Step 5's target accordingly: the simcoupe-ideas note flattens to `docs/notes/simcoupe-paste-control-plane.md` (no date prefix).
- [ ] **Step 4:** Commit: `docs: evergreen filenames for living docs (dates live in git history)`.

### Task 2.6c: Doc link checker (standing guard for the deletions)

- [ ] **Step 1:** Create `tools/check-doc-links.sh` (executable): for every markdown link target in `README.md`, `CLAUDE.md`, `docs/**/*.md`, `src/README.md`, `tools/**/*.md`, `tests/**/*.md` that is a relative path into the repo (starts with `docs/`, `src/`, `tools/`, `tests/`, `reference/`, `scripts/`, or is relative to the file's dir), check the target exists; exit 1 listing any misses. Skip URLs, anchors, and `blob/<sha>` links. Implementation: grep -oE '\]\(([^)#]+)' over the md set, normalise each path against the containing file's dir and the repo root, test -e.
- [ ] **Step 2:** Run it; fix any hits (these are exactly the dangling references the Task 2.8 sweep hunts).
- [ ] **Step 3:** Wire it as an extra step in the existing `staticcheck` CI job (no new required check; ci.yml gets one `run:` line) and a `make check-doc-links` target.
- [ ] **Step 4:** Commit: `tools: add doc link checker; wire into staticcheck job`.

### Task 2.7: Directory READMEs / indexes

**Principle (Pete, 2026-06-10):** stepping into any directory should give you local context — every top-level directory and every Go module carries a README answering *what is this, how does it relate to the whole, where is the canonical deep doc*. Pointer-first: link the canonical doc, never restate its content. The per-corpus `tests/*/` subdirs are covered by the `tests/README.md` table rather than per-dir files (duplication risk outweighs the value at that depth).

- [ ] **Step 1:** Create short (≤30-line) READMEs: `docs/README.md` (map: ARCHITECTURE → specs → notes → vendored refs), `docs/notes/README.md` (references vs registries vs active milestone doc), `docs/specs/README.md` (annotated list of the live specs), `docs/plans/README.md` (3 lines: plans are ephemeral — committed at execution start, deleted by the completing PR; an empty dir is the healthy state), `tests/README.md` (corpora = cumulative feature tiers, table with one line per corpus; how to run a sweep), `reference/README.md` (what's vendored and why).
- [ ] **Step 2:** Create ≤15-line READMEs for the Go modules without one: `tools/sam-aarch64/`, `tools/sam-aarch64-format/`, `tools/aarch64enc/`, `tools/aarch64dec/`, `tools/enctab-gen/`, `tools/build-m3-disk/` (PR 5 renames the dir; the README moves with it): purpose, authority role, key entry points, the make targets that exercise them. Plus `src/slots/README.md` (one-line index of the per-operand-kind encoders and how they're included).
- [ ] **Step 3:** Commit: `docs: add directory READMEs and indexes`.

### Task 2.8: ROADMAP/README pointer repair + link sweep

- [ ] **Step 1:** ROADMAP milestone table: strip Spec/Status-doc/plan links for M0–M7 rows (keep title, key PR numbers, state). M6 row: remove the paged-call-architecture / memory-layout-brainstorm / eod-handoff links. Editor-vision section: remove the canonicalisation-survey sentence. Contract rule 2: replace the `docs/notes/archive/` bullet with `Superseded docs are **deleted** — git history is the archive; pin citations to a blob link if a living doc must cite one.`
- [ ] **Step 2:** Add ARCHITECTURE.md to the CLAUDE.md first-session pointers and to ROADMAP's doc index, as the first read.
- [ ] **Step 3:** Link sweep — must output nothing:
```bash
cd ~/git/sam-aarch64-doc-cleanup
for f in $(g diff --name-only --diff-filter=D main...HEAD); do
  grep -rln --exclude-dir=.git "$(basename "$f")" . | grep -v 'blob/c0f62fa' && echo "DANGLING: $f"
done
```
Fix every DANGLING hit (update or blob-pin), re-run until clean.
- [ ] **Step 4:** Run `bash tools/session-handover.sh` — confirm it exits clean and prints the contract + the ≤15-line state block (no history dump). If the printed span is still bloated, move the `HANDOVER-PROTOCOL-END` marker up to just after the state block.
- [ ] **Step 5:** Commit: `docs: repair links after deletions; index ARCHITECTURE.md as the first read`.

### Task 2.9: Ship PR 2

- [ ] **Step 1:** Push, `gh pr create --title "i52 PR 2/5: docs restructure — salvage, delete, ARCHITECTURE.md, indexes"`. Body lists the salvage table from the spec §"Salvaged" with where each landed.
- [ ] **Step 2:** CI green → §3 review subagent (fable). The review must additionally verify each spec-§salvage row's destination exists in the diff (treat a missing salvage as RED).
- [ ] **Step 3:** Merge, sync worktree, branch `cleanup-3-tracking`.

---

## PR 3 — tracking consolidation (branch: `cleanup-3-tracking`)

### Task 3.1: Retire the deferred-work checklist into the registry

- [ ] **Step 1:** For each **unchecked** ROADMAP checklist row, apply: compact-tbn row → shipped (drop); samdos-load-idiom row → covered by the design-notes table (drop); m2 text2bin operand-kind validation → add registry item (next free id); Cortex-A53 errata → verify an item exists (`grep -in 'cortex' docs/notes/item-registry.md`), else add; multi-section refenc → add item; SpectrumFourLayout extraction → add item; `cls` removal → verify (`grep -in 'cls' docs/notes/item-registry.md`), else add. All **checked** (`[x]`) rows → delete (history lives in git).
- [ ] **Step 2:** Delete the entire "Deferred-work review checklist" section and the M6-prerequisite sub-steps under it.
- [ ] **Step 3:** Rewrite "How to extend this doc": new deferred work → an `iN` registry row (one home), design notes → the doc index.
- [ ] **Step 4:** Contract rule 1: `a one-line entry in this doc (the "Deferred-work review checklist") or the relevant milestone status doc` → `an iN row in the item registry (or qN in the question registry)`. Contract rule 3: drop the checklist-walk sentence; milestone close = registry walk only.
- [ ] **Step 5:** Commit: `tracking: retire the ROADMAP deferred-work checklist into the iN registry`.

### Task 3.2: Move the editor vision out of ROADMAP

- [ ] **Step 1:** `g mv`-equivalent: create `docs/specs/editor-vision.md` with the full "Editor vision" section content (unchanged prose, add a one-line header noting it feeds the Phase 2 spec); delete the section from ROADMAP; link it from the Phase-2 milestone row and the docs/specs README.
- [ ] **Step 2:** Delete the "Achievements worth keeping visible" section (the milestone table + git history carry these).
- [ ] **Step 3:** Commit: `docs: move editor vision to a spec; drop the achievements log from ROADMAP`.

### Task 3.2b: Codify the doc lifecycle + hygiene-as-you-go policy (spec decision 6)

- [ ] **Step 1:** CLAUDE.md — extend the "Where plans and specs go" section with the lifecycle rules:
  - Plans (`docs/plans/`) are ephemeral execution artifacts: committed when execution starts, **deleted in the PR that completes the work** (the completing PR's description links the plan's final blob).
  - `docs/specs/` holds only **living** design docs with **evergreen (undated) filenames**. When a design ships, fold its durable rationale into `docs/ARCHITECTURE.md` or a reference doc and delete the design doc in the same PR.
  - Milestone status docs are deleted at milestone close, after the registry walk (contract rule 3).
  - No `YYYY-MM-DD` filename prefixes under `docs/` — git history carries dates. This deliberately overrides the superpowers skills' dated-filename convention.
  - Name new artifacts (dirs, scripts, make targets, CI jobs) after their **function**, never after the milestone that introduced them.
  - Superseded code/tooling is deleted in the PR that supersedes it; if a deletion needs Pete's sign-off, raise a `qN` immediately rather than parking the artifact indefinitely.
  - Every top-level directory and every Go module carries a ≤30-line README (*what is this, how does it relate, where is the canonical deep doc* — link, never restate). A PR that creates a directory ships its README in the same PR.
- [ ] **Step 2:** CLAUDE.md §3 pre-merge checklist — add item 6, "Repo hygiene": the PR leaves no newly-dead artifacts behind (superseded docs/tooling deleted, plan deleted if the PR completes one, no new dated filenames, no new tracking homes outside the registries, new durable info landed in an existing living doc where one fits). Green: clean. Red: any hygiene debt introduced.
- [ ] **Step 3:** `tools/session-handover.sh` — add two cheap standing warnings after the existing stray-file check: (a) any `docs/**/20[0-9][0-9]-*.md` filename → "dated filename — lifecycle policy violation"; (b) list `docs/plans/*.md` if non-empty as "in-flight plans" so stale plans are visible every session start.
- [ ] **Step 4:** Run `bash tools/session-handover.sh` to confirm the warnings behave (the cleanup's own dated plan/spec will trigger warning (a) — expected and self-resolving in PR 5; note this in the commit message).
- [ ] **Step 5:** Commit: `docs: codify ephemeral-plan/evergreen-spec lifecycle + per-PR hygiene gate`.

### Task 3.3: Verify + ship PR 3

- [ ] **Step 1:** `bash tools/session-handover.sh | wc -l` — expect ≤50 lines total output.
- [ ] **Step 2:** Re-run the Task 2.8 link sweep for files deleted/moved in this PR.
- [ ] **Step 3:** Push, PR (`i52 PR 3/5: tracking consolidation`), CI, §3 review (must confirm every retired checklist row is either dropped-as-done or has a registry id — list them in the verdict), merge, branch `cleanup-4-tooling`.

---

## PR 4 — tooling hygiene (branch: `cleanup-4-tooling`)

### Task 4.1: Delete dormant tooling

- [ ] **Step 1:** Pre-check (expect zero hits outside the deleted set itself):
```bash
grep -rn 'llist\|basic-emulator-spike\|basic-detokeniser\|run-m6-release-stripped\|run-release-sam' Makefile .github/ tests/ src/ scripts/ docs/ --include='*' -l
```
(`docs/notes/basic-detokeniser-spike.md` naming its own former tool dir is fine — update its header to say the code now lives in git history at `blob/c0f62fa/tools/...`.)
- [ ] **Step 2:**
```bash
g rm -r tools/llist-capture tools/llist-normalise tools/llist-sweep tools/basic-emulator-spike tools/basic-detokeniser-spike tools/basic-detokeniser-sweep
g rm tools/llist-capture.sh tools/llist-capture-docker.sh tools/llist-capture-headless.sh tools/llist-vs-b2t.sh tools/run-m6-release-stripped.sh tools/run-release-sam.sh
```
- [ ] **Step 3:** tools/README.md: delete the Spikes + Superseded sections; remove `run-m6-release-stripped.sh`/`run-release-sam.sh` from the production table; change the `session-handover.sh` row's purpose to `Agent-session infrastructure (SessionStart hook via .claude/settings.json) — not build tooling`.
- [ ] **Step 4:** Commit: `tools: delete superseded llist cluster, concluded spikes, orphaned release scripts (q8 resolved: delete)`.

### Task 4.2: Merge scripts/ into tools/

- [ ] **Step 1:** `g mv scripts/check-code-budget.sh tools/ && g mv scripts/build-spectrum4-release.sh tools/`
- [ ] **Step 2:** Update references: Makefile lines 15, 127, 132, 167, 172 (`scripts/` → `tools/`); ci.yml line ~533 comment; `grep -rn 'scripts/' --include='*.md' --include='*.sh' --include='Makefile' --include='*.yml' .` for the rest (README layout block from Task 1.2 included).
- [ ] **Step 3:** `make check-budget` runs clean locally (uses existing `build/` artifacts or document why skipped).
- [ ] **Step 4:** Commit: `tools: fold scripts/ into tools/ (one home for helper scripts)`.

### Task 4.3: Registry + ship PR 4

- [ ] **Step 1:** question-registry: q8 → ✅ resolved (`deleted 2026-06-10, i52 PR 4; sign-off recorded in the cleanup spec`). Item registry: check `i34` (llist) and mark ❌ wontfix/✅ as its wording dictates.
- [ ] **Step 2:** Push, PR (`i52 PR 4/5: tooling hygiene`), CI, §3 review, merge, branch `cleanup-5-renames`.

### Task 4.4: Post-merge memory sweep (orchestrator, not part of the PR)

- [ ] **Step 1:** `grep -rln 'llist\|basic-emulator-spike\|basic-detokeniser-spike\|basic-detokeniser-sweep\|scripts/check-code-budget\|scripts/build-spectrum4' ~/.claude/projects/-home-pmoore-git-sam-aarch64/memory/` — update each hit: spike memories point at the kept findings note + `blob/c0f62fa` code links; path references `scripts/...` → `tools/...`. Update MEMORY.md hooks if their one-liners changed.

---

## PR 5 — functional renames + test-inventory verification (branch: `cleanup-5-renames`)

**Strictly mechanical — no logic changes beyond the documented script unification. The §7 manifest is the gate.**

### Task 5.1: Write the manifest generator

- [ ] **Step 1:** Create `tools/test-manifest.sh` (executable):

```bash
#!/usr/bin/env bash
# Test-inventory manifest: CI jobs (+durations), Go test names (+durations),
# fixture corpora, boot self-tests, round-trip sweep enumeration.
# Usage: tools/test-manifest.sh <output-file> [--skip-ci]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:?usage: test-manifest.sh <output-file> [--skip-ci]}"
SKIP_CI="${2:-}"
exec > "$OUT"

echo "## CI jobs"
if [ "$SKIP_CI" != "--skip-ci" ]; then
  RUN_ID=$(gh run list --workflow ci --limit 1 --status completed --json databaseId -q '.[0].databaseId')
  gh run view "$RUN_ID" --json jobs -q '.jobs[] | "\(.name)\t\(( (.completedAt|fromdate) - (.startedAt|fromdate) ))s"' | sort
  echo "ci-job-count: $(gh run view "$RUN_ID" --json jobs -q '.jobs | length')"
fi

echo; echo "## Go tests"
TOTAL=0
for mod in $(cd "$ROOT" && find tools -name go.mod | sort); do
  dir="$ROOT/$(dirname "$mod")"
  echo "### module: $(dirname "$mod")"
  RESULTS=$( (cd "$dir" && go test -count=1 -v ./... 2>&1 | grep -E '^--- (PASS|FAIL|SKIP):' | sed 's/^--- //' | sort) || true )
  echo "${RESULTS:-"(no tests)"}"
  N=$(printf '%s' "$RESULTS" | grep -c ':' || true)
  TOTAL=$((TOTAL + N))
done
echo "go-test-count: $TOTAL"

echo; echo "## Fixture corpora"
for d in "$ROOT"/tests/*/sources; do
  corpus="$(basename "$(dirname "$d")")"
  echo "### corpus: $corpus ($(ls "$d"/*.s | wc -l) fixtures)"
  ls "$d" | sort
done
echo "### release fixture"
find "$ROOT"/tests -name 'release.s' -o -name 'release.img' | sed "s|$ROOT/||" | sort

echo; echo "## Boot self-tests (BUILD_TESTS wiring)"
grep -hoE 'call[[:space:]]+run_[a-z0-9_]+' "$ROOT"/src/assembler.asm "$ROOT"/src/test_offaxis_cluster.asm "$ROOT"/src/test_mem_offaxis.asm | awk '{print $2}' | sort -u

echo; echo "## Round-trip sweep enumeration"
for w in "$ROOT"/tests/*/run-roundtrip.sh; do
  corpus="$(basename "$(dirname "$w")")"
  echo "### sweep: $corpus"
  ls "$(dirname "$w")"/sources/*.s 2>/dev/null | xargs -rn1 basename | sort
done
```

- [ ] **Step 2:** Verify the boot-self-test grep is faithful: read the three wiring sites and confirm every `test_*.asm` entry point is invoked via a `call run_*` the pattern catches (adjust the pattern if any use `paged_call` dispatch — check `src/assembler.asm`'s disasm self-test invocation specifically). Verify each `tests/*/run-roundtrip.sh` loop really iterates `sources/*.s` unfiltered; if any filters, mirror that filter in the enumeration section.
- [ ] **Step 3:** Generate the BEFORE manifest from clean main: `cd ~/git/sam-aarch64 && g pull --ff-only && ~/git/sam-aarch64-doc-cleanup/tools/test-manifest.sh /tmp/manifest-before.txt` (CI section reads the latest completed main run). Sanity-check: 15 CI jobs; corpora counts 36/9/5/20/19/29; boot self-test list non-empty.
- [ ] **Step 4:** Commit the script: `tools: add test-inventory manifest generator`.

### Task 5.2: go.work

- [ ] **Step 1:** `cd ~/git/sam-aarch64-doc-cleanup && go work init && for m in $(find tools -name go.mod | xargs -n1 dirname); do go work use ./$m; done && g add go.work`
- [ ] **Step 2:** Confirm `.gitignore` doesn't exclude `go.work`; confirm `make staticcheck` still passes; confirm `go.work.sum` (if generated) is committed too.
- [ ] **Step 3:** Commit: `build: add go.work tying the 13 Go modules together`.

### Task 5.3: tests/ renames

- [ ] **Step 1:**
```bash
g mv tests/m1 tests/format
g mv tests/m3 tests/core
g mv tests/m4 tests/symbols
g mv tests/m5 tests/operands
g mv tests/m6 tests/paged
g mv tests/paged/release tests/release
```
- [ ] **Step 2:** Fix self-references inside each `tests/*/run-roundtrip.sh` and `tests/release/README.md`.
- [ ] **Step 3:** Commit: `tests: rename milestone corpora to functional names`.

### Task 5.4: Unify the round-trip drivers

- [ ] **Step 1:** `diff tools/run-m3-roundtrip.sh tools/run-m4-roundtrip.sh` (then m4↔m5, m5↔m6) — table the actual deltas (extra payloads, env vars, fixture handling).
- [ ] **Step 2:** Write `tools/run-roundtrip.sh <corpus> <fixture.s>` reproducing each behavior keyed by corpus (`core|symbols|operands|paged`), preserving every delta found in Step 1. Delete the four old scripts. Update `tests/{core,symbols,operands,paged}/run-roundtrip.sh` to call it.
- [ ] **Step 3:** Smoke-test locally in the dev container: one fixture per corpus through the new driver (`docker exec` per the README quickstart), all four byte-match.
- [ ] **Step 4:** Commit: `tools: unify the four round-trip drivers into run-roundtrip.sh <corpus>`.

### Task 5.5: tools/ + Makefile renames

- [ ] **Step 1:** `g mv tools/build-m3-disk tools/build-disk`; update its `go.mod` module line (`.../tools/build-disk`), any self-imports, and `go.work`.
- [ ] **Step 2:** `g mv tools/run-m6-release-gate.sh tools/run-release-gate.sh && g mv tools/revendor-m6-release.sh tools/revendor-release.sh`; update their internal `tests/m6/release` → `tests/release` paths.
- [ ] **Step 3:** Makefile: rename targets `m3-asm`→`assembler`, `m3-asm-prod`→`assembler-prod`, `m3-disk`→`disk`, `build-m3-disk`→`build-disk`, `test-m1`→`test-format`, `ci-m1`→`ci-format`, `test-m2`/`ci-m2`→`-encoder`, `test-m{3..6}`/`ci-m{3..6}`(`-prod`)→ corpus names, `/tmp/m3-*.log` paths → corpus-named. Sweep: `grep -n 'm[0-9]' Makefile` until every remaining hit is justified (e.g. `m3` inside a historical comment gets rewritten).
- [ ] **Step 4:** Commit: `build: functional names for targets, disk builder, gate scripts`.

### Task 5.6: ci.yml job renames

- [ ] **Step 1:** Rename jobs per the map (`m1`→`format`, `m2`→`encoder`, `m3`→`core`, `m4`→`symbols`, `m4-prod`→`symbols-prod`, `m5`→`operands`, `m5-prod`→`operands-prod`, `m6`→`paged`, `m6-prod`→`paged-prod`, `m6-release`→`release-gate`; `build-image`, `disasm`, `disasm-roundtrip`, `sysreg-sync`, `staticcheck` unchanged). Update `needs:` edges, `make ci-*` invocations, post-mortem `ls tests/...` lines, and the `tools/run-release-gate.sh` call.
- [ ] **Step 2:** Commit: `ci: rename gate jobs to functional names`.

### Task 5.7: Repo-wide token sweep

- [ ] **Step 1:** `grep -rn --exclude-dir=.git --exclude-dir=build -iE 'run-m[0-9]|build-m3-disk|m3-asm|m3-disk|tests/m[0-9]|ci-m[0-9]|test-m[0-9]|m6-release|revendor-m6' .` — update every hit: README (status text + quickstart command), CLAUDE.md (§3 wiring-site names are file paths, untouched; the `m6-release`/`make m3-disk` mentions change), ARCHITECTURE.md, docs/specs + notes survivors, src/README.md, harness docs. Blob-pinned links keep their old paths (immutable).
- [ ] **Step 2:** Re-run until the only hits are deliberate (registry/ROADMAP historical PR descriptions may legitimately mention old names in past tense — leave those).
- [ ] **Step 3:** Commit: `docs: sweep milestone-named tokens for the functional vocabulary`.

### Task 5.8: AFTER manifest + comparison

- [ ] **Step 1:** Local AFTER manifest (CI section deferred): `tools/test-manifest.sh /tmp/manifest-after-local.txt --skip-ci`.
- [ ] **Step 2:** Map the BEFORE manifest to the new vocabulary and diff (durations stripped — names/counts are the contract):
```bash
sed -E 's/^(m1)\t/format\t/; s/^(m2)\t/encoder\t/; s/^m3\t/core\t/; s/^m4-prod\t/symbols-prod\t/; s/^m4\t/symbols\t/; s/^m5-prod\t/operands-prod\t/; s/^m5\t/operands\t/; s/^m6-release\t/release-gate\t/; s/^m6-prod\t/paged-prod\t/; s/^m6\t/paged\t/; s/corpus: m1/corpus: format/; s/corpus: m3/corpus: core/; s/corpus: m4/corpus: symbols/; s/corpus: m5/corpus: operands/; s/corpus: m6/corpus: paged/; s/sweep: m1/sweep: format/; s/sweep: m3/sweep: core/; s/sweep: m4/sweep: symbols/; s/sweep: m5/sweep: operands/; s/sweep: m6/sweep: paged/; s|tools/build-m3-disk|tools/build-disk|' /tmp/manifest-before.txt > /tmp/manifest-before-mapped.txt
diff <(sed -E 's/ \([0-9.]+s\)$//; s/\t[0-9]+s$//' /tmp/manifest-before-mapped.txt) \
     <(sed -E 's/ \([0-9.]+s\)$//; s/\t[0-9]+s$//' /tmp/manifest-after-local.txt)
```
Expected: only the CI-jobs section differs (absent in the local after-manifest) plus the `tests/m6/release` → `tests/release` path lines. **Any missing Go test, fixture, boot self-test, or sweep entry is a merge blocker — fix, don't explain away.**
- [ ] **Step 3:** Push, open the PR (`i52 PR 5/5: functional renames + test-inventory verification`). Body includes the duration-bearing manifests + the clean diff, one paragraph per line.

### Task 5.9: Branch protection union + merge

- [ ] **Step 1:** Read the current required contexts: `gh api repos/petemoore/sam-aarch64/branches/main/protection/required_status_checks --jq '.contexts'`. Save the output verbatim in the PR thread.
- [ ] **Step 2:** Set the union (old + new names) — example shape, substitute the actual current list:
```bash
gh api -X PATCH repos/petemoore/sam-aarch64/branches/main/protection/required_status_checks \
  --input - <<'EOF'
{"strict": true, "contexts": [/* old contexts */ "m3","m4","m4-prod","m5","m5-prod","m6","m6-prod","m6-release","m1","m2", /* new */ "core","symbols","symbols-prod","operands","operands-prod","paged","paged-prod","release-gate","format","encoder"]}
EOF
```
(Preserve `strict` and any unchanged contexts — `disasm-roundtrip`, `sysreg-sync`, `staticcheck`, etc. — exactly as read in Step 1.)
- [ ] **Step 3:** Wait for the PR's CI: all NEW-named jobs green (`gh pr checks --watch`). The old-named required contexts will show "expected" on the PR — that is the union doing its job; merging needs an admin-permitted state, so verify mergeability with `gh pr view --json mergeable,mergeStateStatus`. If the union blocks the merge outright (old contexts never report on this PR), flip the contexts to the NEW list immediately before merging instead of after — the window where `main` requires only-new names while `main`'s tip is pre-rename is acceptable because no other PRs are in flight (verify: `gh pr list`).
- [ ] **Step 4:** Pull the AFTER CI-jobs section once the run completes: `tools/test-manifest.sh /tmp/manifest-after-ci.txt` (now reads the PR run) — append the CI-jobs comparison to the PR.
- [ ] **Step 5:** §3 pre-merge review subagent (fable): standard checklist + independently re-run `tools/test-manifest.sh` and the Task 5.8 diff (a non-empty inventory diff is RED).
- [ ] **Step 6:** Final commit before merge: `g rm docs/plans/2026-06-10-repo-cleanup.md docs/specs/2026-06-10-repo-cleanup-design.md` + mark i52 ✅ done in the registry + ROADMAP state-block update (this is the milestone-policy applied to itself: executed plan/spec are deleted; the spec stays reachable at `blob/<PR-5-head-sha>`). Update the docs/specs README index. Re-run the link sweep.
- [ ] **Step 7:** Merge (`gh pr merge --merge --delete-branch`); set required contexts to the NEW list only (drop old names); trigger/observe the post-merge main run — all required checks green.
- [ ] **Step 8:** Post-merge memory sweep: `grep -rln 'm3-disk\|m3-asm\|build-m3-disk\|run-m6-release\|tests/m[0-9]' ~/.claude/projects/-home-pmoore-git-sam-aarch64/memory/` → update hits (e.g. `feedback_rebuild_offaxis_cluster_after_main_changes`, `feedback_test_variant_fragility`) to the new vocabulary.
- [ ] **Step 9:** Remove the worktree: `g worktree remove ~/git/sam-aarch64-doc-cleanup`.

---

## Self-review notes (run at execution time, per PR)

- PR bodies: short, human, no validation tables (Pete's PR-body minimalism).
- Every deletion commit message names the spec section authorizing it.
- If CI reveals a reference the sweeps missed, fix forward in the same PR — do not weaken any gate.
- If `main` moves under the cleanup (another agent lands work), rebase the active branch and re-run the affected sweeps; the BEFORE manifest must be regenerated from the new main before PR 5.
