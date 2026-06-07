# Repo cleanup — design (2026-06-10)

**Status:** approved design, pending implementation plan.
**Goal:** make the repo read like a lean open-source project — clear architecture
docs, one roadmap, one tracking system, conventional structure — without losing
any information that is still load-bearing. Git history is the archive: nothing
is lost by deletion, only moved out of the working tree.

**Non-goals:** no behaviour changes to the assembler, tools, or tests; no
changes to the `iN`/`qN` registry system (it works); no restructuring of
`src/` (its flat layout + intermingled `test_*.asm` is deliberate and
documented in `src/README.md`).

## Decisions (Pete, 2026-06-10)

1. **Historical docs are deleted**, not archived in-tree. Incident docs still
   cited by CLAUDE.md get commit-pinned links or short inline summaries.
2. **All six dormant spike dirs are deleted** (llist cluster — this is the
   sign-off it was waiting for — plus the three basic-* spikes). Findings stay
   in the kept notes and in memory.
3. **Full rename** of milestone-named code/CI artifacts to functional names,
   including CI job names, with branch protection updated in lockstep.
4. **`docs/ARCHITECTURE.md` is a synthesized overview**, not just an index.
5. **Phase-5 verification is a test-inventory comparison**, not "CI green
   before and after" (see §8).

## 1. Docs end-state

`docs/` shrinks from ~115 files / ~36k lines to ~25 files / ~6k lines.

### Kept — specs (the living design docs)

- `2026-05-09-vision.md` — north star.
- `2026-05-09-phase1-assembler.md` — phase 1/2/3 charter.
- `2026-06-08-tbn-binary-format-reference.md` — normative `.tbn` v2 reference.
- `2026-06-08-compact-tbn-nextgen-design.md` — M8 design (i39c, i40/i51 pending).
- `2026-06-08-i48-single-format-syntactic-encoder-design.md` — drives future i48c.
- `2026-06-08-editor-edit-model-design.md` — Phase-2 editor design.
- `2026-05-27-m6-paged-in-design.md` + `2026-05-27-m6-paged-out-design.md` —
  only home of the paging-architecture rationale.
- `2026-05-27-phase3-tftp-direct-lan-design.md` — Phase 3 direction.
- `samdos-file-io.md` — **new**, merging `2026-05-27-samdos-load-idiom.md` +
  `2026-05-27-samdos-save-idiom.md` (durable HSAVE/HLOAD hook semantics).

### Kept — notes (technical references + tracking)

- References: `sam-paging.md`, `sam-disk-format.md`, `sam-file-header.md`,
  `sam-basic-save-format.md`, `memory-layout.md` (re-synced against
  `src/assembler.asm` first), `headless-simcoupe.md`, `test-mgt-byte-layout.md`,
  `sam-stub-audit.md`, `basic-detokeniser-spike.md` (durable findings record
  backing side-project memory; survives its tool's deletion).
- Unconsumed research feeding future decisions:
  `2026-05-28-sam-music-playback-research.md`,
  `2026-06-08-armv8-a64-isa-footprint-research.md`,
  `simcoupe-ideas/2026-05-29-paste-driven-control-plane.md`.
- Tracking: `item-registry.md`, `question-registry.md`, `m8-status.md` (the
  single active milestone doc).
- Vendored third-party material under `docs/sam/`, `docs/comet/`,
  `docs/saa1099/` stays untouched.

### Salvaged before deletion (fold into living docs, then delete the source)

| Source (deleted) | Durable content | Destination |
|---|---|---|
| `specs/2026-05-24-m2-encoder-tables-design.md` | enctab.enc / ARM-MRA / aarch64enc table architecture | ARCHITECTURE.md toolchain section |
| `specs/2026-05-27-samdos-{load,save}-idiom.md` | SAMDOS hook semantics | new `specs/samdos-file-io.md` |
| `specs/2026-05-29-bump-arena-risk-census.md` | YAGNI decision + §4 revisit trigger | one-paragraph note in `m8-status.md` references or an `iN` entry |
| `specs/2026-05-23-m1-binary-tokenised-format-design.md` | §7–§9 (tooling philosophy, test pyramid) | ARCHITECTURE.md, where still true |
| `notes/2026-05-28-test-variant-ci-regression.md` (PR #43 incident) | merge-commit rule rationale | CLAUDE.md citation → commit-pinned link or 2-line inline summary |
| `notes/2026-06-08-go-vs-z80-disasm-capability-parity.md` | open recommendation (synthetic fixture sweep) | confirm it has an `iN` id; add if missing |
| `notes/question-registry.md` q7 appendix | resolved sweep findings | trim from registry; registry keeps the one-line resolution |

### Deleted (everything else)

- All 19 files in `docs/plans/` (every plan fully executed).
- The pure-history specs: detokeniser-spike design, multi-page-loader design,
  simcoupe-sdl-paste design, `2026-05-27-compact-tbn-and-disassembler-design.md`
  (superseded by v2), `2026-06-07-disasm-round-trip-design.md`, plus the
  salvage-source specs above and
  `2026-05-25-macro-expansion-research.md`,
  `2026-05-24-m{3,4,5}-*-design.md` (internals now better described by code +
  `src/README.md`).
- All of `docs/notes/archive/`.
- `m0`–`m7-status.md`, `spectrum4-status.md`,
  `2026-05-28-eod-session-handoff.md`.
- All dated investigation/audit notes (2026-05-27 → 2026-06-08) not listed as
  kept above, including `comet-encoding-patterns.md`, `fred-disk-inspection.md`,
  `samfile-capabilities.md`, `samdos2-auto-run-analysis.md`.

### New docs

- **`docs/ARCHITECTURE.md`** — synthesized ~300–500 line overview written from
  the current code + live specs: system shape (SAM-side Z80 assembler, host Go
  toolchain), the Go-is-authority / Z80-is-port split, memory + paging model,
  `.tbn` v2 format summary, build/test/CI pipeline, dev inner loop. Each
  section links its deep spec/reference. This is the first doc a newcomer reads.
- **Directory READMEs / indexes** for `docs/`, `docs/notes/`, `docs/specs/`,
  `tests/`, `reference/`, and the five undocumented Go packages
  (`tools/sam-aarch64`, `sam-aarch64-format`, `aarch64enc`, `aarch64dec`,
  `enctab-gen`). Short — a few lines each saying what lives there and where the
  deep docs are.

## 2. Tracking end-state

One system, three artifacts:

- **`item-registry.md` / `question-registry.md`** — unchanged governance.
- **ROADMAP.md slimmed**: the contract, a strict replace-in-place
  "Current state" block (≤15 lines, no dated bullets — history lives in
  milestone docs and git), the milestone table (corrected: M8 active, M0–M7
  closed), and the doc index. The "Deferred-work review checklist" is retired:
  every row gets an `iN` id (some already have one) and the checklist is
  deleted.
- **The active milestone doc** (`m8-status.md`) — per-strand state.

Mechanical fixes folded in: HANDOVER-PROTOCOL markers shrink so
`tools/session-handover.sh` prints the contract (~25 lines), not the history;
hardcoded check counts ("11"/"14") become "the required checks defined in
`.github/workflows/ci.yml`"; CLAUDE.md first-session pointers updated
(ARCHITECTURE.md + m8-status.md + registries); root README rewritten
(accurate status through M8, real repository layout, current tool vocabulary);
stale `ci.yml` header comment rewritten; `tools/README.md` q8 pointer fixed.

## 3. Tooling hygiene

- Delete: `tools/llist-capture/`, `tools/llist-normalise/` (incl. the committed
  binary), `tools/llist-sweep/`, the four `llist-*.sh` scripts,
  `tools/basic-emulator-spike/`, `tools/basic-detokeniser-spike/`,
  `tools/basic-detokeniser-sweep/`, `tools/run-m6-release-stripped.sh`,
  `tools/run-release-sam.sh`.
- Merge `scripts/` (2 files) into `tools/`; update Makefile + ci.yml references.
- `tools/session-handover.sh` stays (wired as a `.claude` SessionStart hook);
  tools/README notes it as agent infrastructure, not build tooling.
- After merge: update memory entries that point at deleted paths (spike
  memories point at tools dirs; they should point at the kept findings notes
  and git history instead).

## 4. Renames (functional names replace milestone names)

| Current | New |
|---|---|
| `tests/m1` | `tests/format` (`.tbn` round-trip fixtures) |
| `tests/m3` | `tests/core` |
| `tests/m4` | `tests/symbols` |
| `tests/m5` | `tests/operands` |
| `tests/m6` | `tests/paged` |
| `tests/m6/release` | `tests/release` |
| `tools/run-m{3..6}-roundtrip.sh` (chained) | single `tools/run-roundtrip.sh <corpus>` |
| `tools/build-m3-disk/` | `tools/build-disk/` |
| Make: `m3-asm` / `m3-asm-prod` / `m3-disk` | `assembler` / `assembler-prod` / `disk` |
| Make: `test-m{N}` / `ci-m{N}`(`-prod`) | `test-<corpus>` / `ci-<corpus>`(`-prod`) |
| CI jobs `m1 m2 m3 m4 m4-prod m5 m5-prod m6 m6-prod m6-release` | `format encoder core symbols symbols-prod operands operands-prod paged paged-prod release-gate` |
| `tools/run-m6-release-gate.sh`, `revendor-m6-release.sh` | `run-release-gate.sh`, `revendor-release.sh` |
| 13 loose Go modules | + `go.work` at repo root (modules stay separate) |

Exact corpus vocabulary is adjustable at plan time; the corpora are cumulative
feature tiers, so names describe the *new* capability each tier exercises.

**Branch-protection procedure** (the lockstep risk): via `gh api`, set the
required-checks list to the *union* of old + new job names; merge the rename
PR; then drop the old names from the list. At no point is `main` mergeable
without the full matrix.

## 5. Execution

- Worktree `~/git/sam-aarch64-doc-cleanup` (sibling dir — immune to
  `git clean -fdx` in the main checkout), sequential branches.
- Five PRs, each through the normal CI + mandatory pre-merge review gate
  (CLAUDE.md §3), merge commits:
  1. **Stale-fact fixes** — README, ROADMAP state block + milestone table,
     CLAUDE.md counts/pointers, ci.yml header comment, tools/README q8 pointer.
  2. **Docs restructure** — salvage, then delete; ARCHITECTURE.md; directory
     READMEs; handover-marker shrink.
  3. **Tracking consolidation** — checklist → `iN`; single live-state
     discipline.
  4. **Tooling hygiene** — spike/orphan deletion, `scripts/` merge.
  5. **Renames** — strictly mechanical, no logic changes; branch-protection
     union procedure; deep verification per §8.
- This spec rides with PR 1.

## 6. Verification — PRs 1–4

- Full CI matrix green.
- `tools/session-handover.sh` run manually; output is the slimmed contract and
  the script exits clean.
- Link sweep: grep over all kept docs proving no kept doc references a deleted
  path (`docs/plans/`, deleted note/spec filenames, deleted tool dirs).
- PR 2 additionally: the salvage table above is a checklist — each row's
  destination content must exist in the diff before the source deletion is
  approved.

## 7. Verification — PR 5 (test-inventory comparison, not green→green)

"CI green before and after" only proves the *surviving* tests pass; it cannot
catch a corpus silently dropping out of the matrix. PR 5 instead produces a
**before/after test manifest** and diffs them modulo the rename map. Any test
present before and absent after (after name-mapping) is a merge blocker.

The manifest enumerates, with counts and approximate durations:

1. **CI jobs** — names + per-job wall-clock from the actual GitHub Actions
   runs (`gh api`/`gh run view` on the pre-rename baseline run on `main` and
   the rename-PR run). Expected: 15 jobs before ↔ 15 jobs after (mapped).
2. **Go tests** — `go test -v ./...` across every module (via `go.work`),
   parsed to individual test names with per-test durations and a total count.
3. **Fixture corpora** — `ls tests/*/sources/*.s` per corpus, full filename
   list + per-corpus count (m1: 36, m3: 9, m4: 5, m5: 20, m6: 19,
   spectrum4: 29 at time of writing) plus the vendored release fixture pair.
4. **Boot self-tests** — the `test_*.asm` entry points wired into the
   BUILD_TESTS boot path (the three wiring sites per CLAUDE.md §3.1), listed
   by name; before/after lists must be identical (these files are not renamed).
5. **Roundtrip sweeps** — the per-fixture invocation list each
   `run-roundtrip` wrapper would execute (dry enumeration), so the corpus →
   driver wiring is proven, not assumed.

The manifest generator is a small script committed with PR 5
(`tools/test-manifest.sh`), so the comparison is reproducible and the
pre-merge review subagent can re-run it. Both manifests and the diff are
attached to the PR description.

## 8. Risks

- **Branch protection lockstep** (PR 5) — mitigated by the union procedure;
  worst case is a briefly over-strict required-check list.
- **Salvage misses** — mitigated by the §6 PR-2 checklist rule and the
  pre-merge review gate.
- **Memory/doc cross-references** — memory entries citing deleted paths are
  updated post-merge (§3); CLAUDE.md citations are updated in the same PR that
  deletes their target.
- **Chained roundtrip scripts** — `run-m{4..6}` layer on `run-m3`; collapsing
  to one parameterised script is the only rename with real logic motion, and
  gets the §7 dry-enumeration check specifically to cover it.
