# M7 — current status (read me first)

Entry point for any session picking up M7. The M7 backlog (gathered from
notes + memory so nothing was lost in the M6 → M7 transition) is the scope
table below; it will keep being refined / re-prioritised as M7 proceeds.

**M7 — ACTIVE (opened 2026-05-29; M6 closed via PR #76).** Housekeeping
batch 1 has landed (PRs #78–#82: dead-code removal, sysreg sync guard,
index READMEs + memory-layout doc, decision bookkeeping, the `src/m3` →
flat `src/` rename). See the ROADMAP "Current State" block for the live
session view; this doc is the per-strand source of truth.

## M7 scope (planning snapshot)

Legend: ✅ done · ⏳ in progress · 📋 designed/plan-ready · 🧭 idea/not-yet-designed · ❌ won't-do (YAGNI)

| Strand | Status | Spec/Source | Notes |
|---|---|---|---|
| Bump-arena allocator (Go-slices-vs-fixed-arrays for the Z80 data structures) | ❌ YAGNI | `docs/specs/2026-05-29-bump-arena-risk-census.md` (the census); Pete 2026-05-29 (accepted) | **NOT building it.** The risk-census (PR #84) found no fixed section-D array is a real overrun time-bomb — all SAFE today, only 3 go at-risk at a full ~5× kernel and are trivially bumpable in place. Pete confirmed YAGNI. The genuinely tight ceiling is elsewhere — the IN/OUT paged byte buffers (next row), fixed by claiming free pages, not an allocator. **Revisit trigger** (census §4): a measured at-risk overrun, or the symbol table heading toward ~2× its 512 cap. |
| IN/OUT paged-buffer ceiling (claim more SAM pages) | 🧭 | `docs/specs/2026-05-29-bump-arena-risk-census.md` §IN/OUT | The real near-term constraint surfaced by the bump-arena census: the **IN `.tbn` buffer is at 92% of its 96 KB / 6-page cap today** (88,644 B; fail tag `03`), OUT at 68% of 32 KB (fail tag `b0`). Physical pages 15..31 (~272 KB) are free, so the fix is a bounds bump (more pages), not an allocator. Not urgent (release fits), but the closest ceiling — do before a substantially larger source lands. Pairs with the compact-`.tbn` strand. |
| Codegen sysreg / mnemonic / form tables from Go authority | 📋 | M6-closure plan §M7 sketch (PR-A), `tools/sam-aarch64-format/sysregs.go` | Kills hand-sync drift. Depends on M6 PR-2's page-13 binary build glue. |
| On-SAM disassembler (strand B) | 📋 | `docs/plans/2026-05-28-go-aarch64-disassembler.md`; branch `strand-b-1-disassembler` (5 commits, parked) | Resume per Pete's redirect after M6 closes. |
| **Source compression / compact `.tbn`** ⭐ NEXT-SESSION PRIORITY | 📋 | `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`; Pete 2026-05-29 (framing + priority) | **Pete's stated next priority.** Framing: make the internal representation **the assembled bytes** for instructions that carry no expressions (vs the current verbose record stream). Pete expects **huge memory savings** (directly relieves the IN-buffer ceiling — currently 92% — and the section-D tables) **and gets us much closer to a full on-SAM Z80 disassembler** (bytes-in → text-out is the inverse). Expression-bearing instructions still need their symbolic form for 2-pass resolution, so the format is hybrid. Design first (refine the existing spec with this framing), then implement. |
| Editor groundwork (Phase 2) | 🧭 | `docs/ROADMAP.md` "Editor vision" section | Instruction-explanation panel, register simulator, sysreg docs, did-you-mean. Not yet spec'd. |
| Repo-cleanup / README housekeeping track | ⏳ | `docs/notes/2026-05-29-repo-audit.md` §6 (prioritised plan) | Track underway. **Landed: PR #78** (dead Go symbols + orphan stub removed), **PR #80** (`tools/` + `src/README.md` index READMEs). Remaining: deep reviews of `main_loop.asm` (#11) / `litpool.asm` (#12), and the `SYMTAB_*` `equ` sentinels (#8). Does NOT block other M7 work. |
| Directory naming & logical organisation (rename `src/m3`) | ⏳ | Pete 2026-05-29 (decision delegated to agent) | **`src/m3` → flat `src/` DONE (PR #82).** The dir is gone; all live references updated. The wider naming review (`tests/m{3..6}`, `ci-m{N}` targets, `build-m3-disk` tool, milestone-named fixtures) remains as later M7 cleanup. |
| Subagent worktree-isolation leak (stray git ops in shared checkout) | 🧭 | Observed 2026-05-29 (Pete flagged) | Worktree-isolated subagents (`Agent` tool, `isolation: worktree`) have twice run `git checkout -b` / `git reset --hard` against the **shared** checkout instead of staying in their worktree (recovered both times, no work lost — the orchestrator commits before dispatching). Investigate why isolation leaks (likely the subagent `cd`s to the repo root, which resolves to the shared dir) and harden: e.g. instruct subagents to operate only via their worktree path, or verify/restore the shared checkout's branch after each subagent returns. Process hazard, not yet causing damage. |
| Canonical memory-layout reference doc | ✅ | PR #80 → `docs/notes/memory-layout.md` | Done. Mirrors the authoritative `src/assembler.asm` header map (source of truth; doc points to it, no drift). |
| Sysreg Go↔Z80 sync guard | ✅ | PR #79 → `tools/sam-aarch64-format/sysregs_z80sync_test.go` + `sysreg-sync` CI job (now a required check) | Done. Go test parses `src/sysreg_data.asm` and asserts every Z80 entry byte-matches the Go authority (the Z80 table is an intentional subset). |
| Linker-layout coupling (flatten hardcodes `spectrum4.ld`) | 🧭 | Pete 2026-05-29; `tools/text2bin/internal/translate/flatten.go` `SpectrumFourLayout` | Byte-equivalence on the release relies on text2bin's flatten pass **hardcoding** spectrum4.ld's section order + ALIGNs + origin (we do NOT parse the `.ld`). The m6-release 3-way gate guards drift (a `.ld` change → re-vendor → gate fails until flatten is updated), but it's an implicit coupling. Consider: parse `spectrum4.ld` directly, or vendor it + a checked cross-reference, or at minimum document the coupling beside `SpectrumFourLayout`. |
| Go-harness fidelity follow-ups | ⏳ | `docs/notes/2026-05-29-go-harness-fidelity-investigation.md` Q4; paged-trap root-cause `docs/notes/2026-05-29-go-harness-paged-trap-rootcause.md` | **Paged-path trap ✅ RESOLVED (PR #88):** NOT a fidelity bug — the 6-page paged-IN HLOAD is faithful; the `&0038` trap was a missing `-sysreg-data` (empty page 13 → NOP-slide). With it supplied, **the harness runs the full release `.tbn` and byte-matches the vendored `release.img`.** Fixed diagnostically (loud "unserved HGTHD file" message) + regression test. **Remaining (lower priority):** write-watchpoint activation, `make harness-sweep` target, USAGE.md ledger. NOT real-ROM execution. |
| Z80↔Go encoding/operator parity audit | ✅ | PR #87 → `docs/notes/2026-05-29-z80-go-parity-audit.md` | **Done — and parity is essentially COMPLETE, not a subset.** The generic encoder consumes `enctab.enc` generated from the same Go `Form` table (can't drift); all 17 slot kinds dispatched; every refenc special-case encoder mirrored 1:1; 27/27 expr ops, 22/22 directives, 12/12 operand kinds. Remaining work is **robustness/scale, not features** → see the seeds row below. |
| Parity robustness seeds (from the #87 audit) | 🧭 | `docs/notes/2026-05-29-z80-go-parity-audit.md` §summary | Three follow-ups the parity audit surfaced: (1) the sysreg named-table subset has one fail-hard path (`src/sysname.asm:421` "no generic form" — likely some PSTATE/DC/TLBI op names) — make it fail-soft or extend the subset; (2) fixed Z80 caps vs unbounded Go (the IN/OUT-ceiling + bump-arena-YAGNI story already covers most); (3) **untested-form-combination sweep** — parity today is *structural*, not empirically tested beyond the M3–M6 fixtures; a fixture sweep through the byte-match harness would convert structural→verified parity (and the harness can now run the full release, per #88). |
| SAM screen-mode decision (editor) | 🧭 | Pete 2026-05-29; ROADMAP "Editor vision" | MODE 3 currently assumed. Decide mode(s) by colour-vs-resolution + aesthetics nearer the editor; the choice consumes display RAM / pages, so it feeds the memory-layout doc (the ✅ row above). **Pete 2026-05-29:** consider offering it as a **user preference** — high-resolution/fewer-colours vs lower-resolution/more-colours — rather than a fixed choice. |
| Full ARMv8-A instruction-set footprint — research | 🧭 (research) | Pete 2026-05-29 | **Isolated research project:** estimate how much additional memory (encoder tables + Z80 code) it would take to support the **full ARMv8.0-A A64 instruction set** (A64 only — no AArch32/Thumb; ARMv8.0 only, not later v8.x), vs today's spectrum4-release-only subset. Include the **FP + Advanced SIMD/NEON** extensions (Pete: "is that NEON? — yes"). Motivation: decide whether broad-ISA support is worth it for future kernel dev (more ops than spectrum4 uses) or ingesting LLVM-compiled output (extract asm → modify on SAM). Output: an informed size estimate + feasibility note; no implementation. Builds on the #87 parity audit (which found the *current* coverage is structurally complete for the subset). |
| Trinity SD/flash storage → bigger-kernel architecture | 🧭 *(beyond-M7)* | Pete 2026-05-29; `memory/trinity_hardware.md` | Trinity's SD/MMC slot lifts the implicit single-floppy ceiling, enabling much larger kernels/debug builds (spectrum4 may be ~5× when complete). The binding constraint eventually shifts from code budget to storage. Quazar docs to be scanned. Distant future. |

## Open questions for Pete (awaiting input)

These are decisions an autonomous M7 session is blocked on (or chose to
defer rather than guess). Logged here so they survive context churn — the
agent works around them and Pete answers when available. Remove an entry
once resolved.

1. **`src/m3` rename — ✅ RESOLVED (Pete 2026-05-29).** Pete delegated the
   final structure choice to the agent. **Decision: flatten `src/m3/` up
   into `src/`** (no component subfolder). Rationale: the stub is gone
   (PR #78), so `src/` is just the SAM monolith + the live `src/sam_io.inc`
   (`src/m3/io.asm` includes `../sam_io.inc`); `tools/` already separates
   all non-SAM/host code; and with no editor/music components yet, a
   speculative `src/assembler/` would force a fuzzy assembler-vs-core split
   now for no benefit (YAGNI). Component subfolders become a deliberate
   re-org when those components arrive with real boundaries. **Done in
   PR #82** (mechanical: `git mv` all of `src/m3/*` → `src/`, including
   `src/slots/`; `../sam_io.inc` → `sam_io.inc`; all references updated).
2. **LLIST tool cluster disposition** (`tools/llist-*` + `llist-*.sh`).
   **⏸ PUNTED (Pete 2026-05-29) — revisit later.** Pete hasn't decided;
   leave the cluster exactly in place. `tools/README.md` (PR #80) marks it
   "superseded by the EDIT/EDKY detokeniser spike — disposition pending".
   Nothing to do until Pete chooses archive/delete/keep.
3. **Design-strand handling — ✅ RESOLVED (Pete 2026-05-29).** Pete is
   happy for the agent to drive design + brainstorming solo (review later).
   Editor/screen-mode aesthetic input can come at review time. See the
   bump-arena strand below for Pete's substantive framing on that one.
4. **`src/stub.asm` / the M0 stub chain — ✅ EXECUTED (2026-05-29, branch
   `m7-delete-m0-stub-chain`).** Pete: "delete all the stub chain stuff, it
   is completely subsumed, i agree." The M0 nop-to-disk round-trip oracle is
   fully subsumed by the M3–M6 fixture corpus + the m6-release 3-way gate.
   **Deleted:** `src/stub.asm`, `tools/build-stub.sh`, `tools/build-disk.sh`,
   `tools/build-disk/` (Go tool), `tools/run-roundtrip.sh` (the M0 root one —
   *not* the per-milestone `tests/m{N}/run-roundtrip.sh`), `tools/extract-output.sh`,
   `tools/diff-vs-gnu.sh`, `tools/check-toolchain.sh` (M0-scoped per its own
   header, orphaned once `check` went), the M0 nop fixture + dir
   (`tests/fixtures/nop.s`), and all six M0 dev tests (`tests/test-{build-stub,
   stub-emits-nop,simcoupe-runs,diff-vs-gnu,diff-vs-gnu-unit,check-toolchain}.sh`).
   Makefile `stub`/`disk`/`run`/`extract`/`diff`/`test`/`check`/`ci` targets
   removed; `all:` now builds `m3-asm m3-asm-prod`. The `test` CI job removed
   from `.github/workflows/ci.yml`. Docs swept (README, tools/README).
   **KEPT (reused, verified):** `tools/run-simcoupe.sh` (used by every
   `run-m{3..6}-roundtrip.sh` + the m6-release gate). The historical M0 design
   docs (`docs/plans/2026-05-09-m0-toolchain-bootstrap.md`, `docs/notes/m0-status.md`,
   `docs/notes/sam-stub-audit.md`) were kept as archival reference.
   **⚠ STILL PENDING at merge time:** remove `test` from `main`'s
   branch-protection required-status-checks (`gh api … required_status_checks/contexts`
   — drops 14 → 13). Must be done or merges block on a check that no longer
   reports. (The orchestrator handles this when merging the PR.)
5. **`tools/llist-normalise/llist-normalise` is a committed binary** (an
   accidental check-in spotted during the rename). Folds into the punted
   LLIST-cluster disposition (open question 2) — handle together.

## Strands in detail

### Bump-arena allocator (headline structural item) — ❌ YAGNI (closed 2026-05-29)

**Outcome:** the risk-census (PR #84, `docs/specs/2026-05-29-bump-arena-risk-census.md`)
concluded no general bump-arena is needed, and **Pete confirmed YAGNI
(2026-05-29)** — happy not to implement it. The section below is retained
as the original framing/context; the census doc has the evidence and the
precise revisit-trigger. The real near-term ceiling it surfaced (the IN/OUT
paged buffers) is now its own scope-table strand.

The durable answer to the fixed-table-overflow class. Today the SAM-side
assembler uses **fixed-capacity arrays** for every internal data structure
— the symbol table, OPVAL_ARRAY, the literal pool, local labels, the
STAGING_BUF. Each is bumped to a hand-tuned capacity sized against the
peak demand a particular `release.tbn` placed on it (the census at
`docs/notes/2026-05-28-z80-table-sizing-census.md` records those peaks —
e.g. symbol table peaked at 474 entries against a 256+128 cap, and the
litpool PC-map peaked at 44 against a cap of 32). That works for today's
input but is fragile: if the spectrum4 source grows, the caps need
re-tuning, and an undersized cap manifests as a silent hang rather than a
clean error.

The structural fix is to move from **fixed arrays to "Go slices" semantics
for the Z80 data structures** — per-component bump arenas living on known
physical pages, each table allocating out of its own arena so it can grow
without a hardcoded ceiling and without colliding with a neighbouring
table. The direction is recorded in `memory/m6_strand_a_complete.md:51`
("once strand B disassembler lands, this is the durable answer to the
entire fixed-table-overflow class … arena dissolves that") and in the
memory-layout brainstorm at
`docs/notes/2026-05-28-memory-layout-brainstorm.md:122` ("Build per-component
bump arenas from the start … per-component, not global, so each table stays
in a known page") and `:129` ("Bump-arena lands with the disasm-aux PR.
Retrofitting later is more work"). It is a **named direction, not a
designed feature** — no spec or plan yet, hence 🧭.

**Pete's framing (2026-05-29) — needs-driven, not speculative.** Build a
growable-array mechanism (which needs an allocator) **only if we actually
have a fixed-size structure we append to that can realistically overrun**;
do NOT build one for its own sake. Growable arrays cost complexity, an
allocator, and per-op overhead, but are ultimately safer and can keep a
smaller footprint (freeing memory). The decision hinges on a concrete
question the design must answer FIRST: *is any current fixed array a live
time-bomb* — i.e. can a plausible (not pathological) future spectrum4
source push it past its cap, turning today's hard-fail into a recurring
hazard? We earlier replaced several array overruns with hard fails (the
bounds-check work in M6 strand A); if those caps are comfortably above any
realistic demand, leave them. If any is genuinely at risk, do the
groundwork so the **only** failure mode is true whole-program OOM, never an
artificial per-table ceiling. So the design doc's Step 1 is a
risk-census (extend `2026-05-28-z80-table-sizing-census.md`): per fixed
structure, record cap vs observed peak vs *plausible-growth* peak, and
classify safe / at-risk. Only the at-risk set justifies the arena. Pete is
happy for the agent to drive this design solo (review later).

### Codegen sysreg / mnemonic / form tables from Go authority

Today the sysreg / mnemonic / form tables are hand-maintained on both the
Go side and the Z80 side and must agree by hand. The M6-closure plan's M7
sketch (PR-A) and the repo audit (§5) both flag this as a real maintenance
hazard: adding a sysreg on one side and forgetting the other passes each
side's own tests but diverges, and the release byte-match only catches it
if a fixture exercises the new register. The fix is single-source-of-truth
codegen: `tools/sam-aarch64-format/sysregs.go` (the authoritative sysreg
list) generates `build/sysreg_data.bin` Mac-side, with the same pattern
extended to mnemonics, the form table, and the intercept tables. This
depends on M6 PR-2's page-13 binary build glue (the off-axis payload
mechanism). 📋 — designed in sketch form, awaiting the standalone plan.

### On-SAM disassembler (strand B)

A form-table-driven aarch64 disassembler — the symmetric inverse of
`tools/aarch64enc/` — validated against binutils `objdump`. The Go-side
implementation plan exists at `docs/plans/2026-05-28-go-aarch64-disassembler.md`,
and a start is parked on branch `strand-b-1-disassembler` (5 commits).
Per Pete's 2026-05-28 redirect, strand B is disassembler-first: Go
disassembler → round-trip test → Z80 port → compact `.tbn` → editor
integration. It unlocks the editor's "render the `.tbn`" feature and is the
oracle for the eventual Z80-side disassembler. 📋.

### Compact `.tbn` format

A denser tokenised-source encoding, designed at
`docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`. Future-
proofing: needed only if a future source exceeds the paged-IN ceiling
(release-stripped at ~88 KB is comfortably under, but the full unstripped
`release.s` flatten is ~408 KB and trips the IN buffer cap). 📋 — designed,
not started.

### Editor groundwork (Phase 2)

The longer-term on-SAM editor vision, captured in the `docs/ROADMAP.md`
"Editor vision (Phase 2)" section: inline instruction-explanation panel,
register simulator with user seeds, system-register documentation surfaced
from the cursor line, "did you mean a simpler instruction?" rewrite hints,
and a 1980s keyboard-driven (not pointer-era) interaction model. The
unifying north star is "the editor as a thoughtful guide, not just a
mechanical tool." Not yet spec'd; these are design pointers, not a critical
path. 🧭.

### Repo-cleanup / README housekeeping track

A dedicated housekeeping track defined by the repo audit at
`docs/notes/2026-05-29-repo-audit.md` §6 (prioritised cleanup plan). It
covers: removing confirmed-dead Go symbols, adding `tools/` + `src/`
index READMEs, archiving superseded docs and the LLIST tool cluster,
deleting the orphan `src/stub-border-test.asm`, resolving the unused
`SYMTAB_*` `equ` sentinels, and scheduled deep reviews of the two largest
Z80 files (`main_loop.asm` 2362 lines, `litpool.asm` 1138 lines). The audit
recommends running it as a track that does NOT block M6 closure, with the
tiny tool-verified PRs interleaved now and the larger reviews scheduled into
M7 proper. 📋 — track defined.

### Directory naming & logical organisation (rename `src/m3`)

Guiding principle (Pete, 2026-05-29): **a clean project should read as a
coherent product, organised by logical component — not as a fossil record
of how it was built.** Today's layout encodes development chronology:
`src/m3` is "milestone 3", `tests/m{3..6}`, `ci-m{N}` targets, and many
milestone-named fixtures. That chronology is already captured by git
history; the tree itself should describe *what the thing is*.

Concretely for M7's review/cleanup:
- Rename `src/m3` → a name that says what it is (the SAM-side Z80
  aarch64 assembler — e.g. `src/assembler` / `src/sam-as`). Touches the
  Makefile, the include paths, `tools/*-disk`, CI, and many docs — do it
  as one mechanical, well-reviewed PR.
- Review the wider naming surface: `tests/m{N}` corpora, `ci-m{N}` /
  `test-m{N}` targets, milestone-prefixed fixture names, and whether the
  "M3/M4/M5/M6 fixture" framing should become capability-named suites.
- Keep it a deliberate cleanup PR (or small series), not drive-by renames
  scattered through feature work. Pairs naturally with the repo-audit
  housekeeping track above.

🧭 — principle captured; not yet planned in detail.

### Go-harness fidelity follow-ups

Per the fidelity investigation at
`docs/notes/2026-05-29-go-harness-fidelity-investigation.md` Q4, three cheap
inner-loop follow-ups belong in M7: **activate the harness write-watchpoint**
(the `watchSPLo/Hi` + `stackWrites` scaffold already exists but is unused —
`:88`, `:184`) to flag writes into forbidden physical pages at run time;
promote the divergence sweep to a `make harness-sweep` convenience target
(`:185`) as a fast pre-SimCoupé smoke check; and add the
`tools/z80-test-harness-go/USAGE.md` ledger plus a per-PR `harness-used`
checklist field (`:186`). The investigation explicitly recommends **NOT**
scheduling real-ROM / real-SAMDOS execution (`:14`, `:183`) — the
RAM-encroachment benefit Pete wanted is delivered far more cheaply by the
build-time link-map assertion (see the M6-closure plan PR-5) plus this
watchpoint, without re-implementing SimCoupé. 📋.

### Sysreg Go↔Z80 sync guard

The single concrete Go↔Z80 drift risk recorded by the repo audit (§5,
§6 item 9): the sysreg/sysname tables are independently hand-maintained on
both sides and must agree (the Z80 entries at `src/sysreg_data.asm:214`
and `:256` are annotated "verified vs `tools/sam-aarch64-format/sysregs.go`").
The recommendation is a small dev/CI check that diffs the two tables, or a
cross-link comment making the sync obligation impossible to miss. This is
the tactical near-term guard; the codegen-from-Go-authority strand above is
the strategic fix that dissolves the drift entirely. 📋.

## Beyond-M7 / future ideas (captured 2026-05-29, need organising)

Pete dumped a batch of longer-horizon ideas at the end of the M6/M7
session. Captured here so none are lost; they still need to be
**organised, prioritised, dependency-mapped, and scheduled** (likely
re-homed into ROADMAP milestones / a new milestone doc, and split for
parallel-agent execution). That organisation is itself a pending task.

- **UI-prototyping spike (interactive editor).** Before/at the editor
  milestone (Phase 2), do a spike to prototype what the assembler's
  **user interface** could look like — play with visuals + interaction
  ideas for the main editor UI. Tightly coupled to the SAM screen-mode
  decision (scope-table row) and the hi-res/lo-res user-preference idea.
  Pete: "would be nice to play around with some visuals or ideas."
- **Preprocessor capabilities on SAM: `.if` build-constraints + macros.**
  Pete's macOS spectrum4 workflow uses `.if` for debug/release/test
  builds and `.macro`s; moving development onto the SAM would be painful
  without them. text2bin already handles `.if`/`.macro` Mac-side (the
  flatten/`-E` path) — the question is supporting them *in the on-SAM
  toolchain*. Big enabler for real on-SAM kernel development.
- **Multiple source files + staged/partial loading.** Considering the
  above motivates: support kernels **split across multiple source files**;
  **not** requiring the entire source resident in memory at once; staged
  load + includes; and **assembling part of the code without the full
  source loaded**. Directly relieves the memory-ceiling story and pairs
  with source compression + the Trinity SD/flash storage path (bigger
  binaries). Several interdependent enhancements here.
- **(see also)** the **full ARMv8-A ISA footprint research** and
  **source-compression/compact-`.tbn`** scope rows above, and the
  **Trinity SD/flash storage** beyond-M7 row — this cluster
  (compression ↔ multi-file ↔ staged-load ↔ bigger storage ↔ broader ISA)
  is interlinked and wants a proper dependency map.

## Deferred backlog (smaller items)

These are minor items that belong somewhere in or around M7 but don't rise
to the level of a named strand. One line each, with the cited source.

| Item | Source | Note |
|---|---|---|
| Per-fail-site diagnostic strings | `docs/ROADMAP.md:83` | `fail` body emits a generic "FAIL"; a follow-up can take a string ptr in HL so call sites supply specifics. |
| Paged-IN > 16.5 KB HLOAD-ceiling lift | `docs/notes/2026-05-28-hload-16k-limit-investigation.md`; `docs/notes/m6-status.md:269` | Threshold is 16632 B (works) → 16633 B (hangs); SP-switch patch lifts it. (Largely addressed by PR #39 per memory; verify before scoping.) |
| 64 KB output / 16-bit `OUT_LEN` limit | `docs/notes/m6-status.md:277` | Debug builds (~274 KB) exceed the 16-bit OUT_LEN; needs M7+. |
| `(hksp)` HSAVE/HLOAD error handler | `docs/notes/m6-status.md:279` | HSAVE/HGTHD/HLOAD longjmp on error; the assembler currently crashes. |
| text2bin operand-kind validation (Task 21) | `docs/ROADMAP.md:79` | Deferred — the SAM-side encoder rejects unknown operand kinds via `jp fail`, so the gap doesn't silently produce wrong bytes. Promote when diagnostics improve. |
| Cortex-A53 errata workarounds | `docs/ROADMAP.md:80` | `--fix-cortex-a53-{835769,843419}` not modelled; no-op on release.img today. |
| Multi-section / linker-script refenc | `docs/ROADMAP.md:81-82` | Punted in favour of `text2bin -flatten`; revisit only if a non-spectrum4-shaped project needs it (incl. `SpectrumFourLayout` extraction). |
| Replace `cls` test instruction | `docs/ROADMAP.md:87` | `cls` exists in `manual_forms.go` solely for one test; replace with a real spectrum4 instruction. |
| SimCoupé upstream v1.2.16 bump | `docs/ROADMAP.md:88`; `memory/project_simcoupe_upstream_pr.md` | Switch CI's SimCoupé from build-from-source-with-patch to upstream v1.2.16 (`-exitonhalt` landed as `a65a16e`); drop the vendored patch. |
| Absolute `.set` high-word edge vs Go | `docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md` review §2 | An absolute `.set X, 0xfffffff0_NNNNNNNN` whose high word coincidentally equals ORIGIN_HIGH is misclassified origin-relative (Z80 stores low-32 + reconstructs; Go stores full value). Harmless for release; a divergence-from-Go on non-release inputs → fold into the parity-audit row above. |
| Harden slot self-test PASS_PC dependency | `docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md` review §3 | `run_slot_self_tests` computes the ADRP test vs PASS_PC before `pass_pc_reset` (relies on cold-boot RAM=0). Pre-existing; add an explicit `pass_pc_reset` before the page-12 cluster to harden. |

**Beyond M7** (noted so they're not mistaken for M7 strands): Phase 3 (TFTP
shipper to the Pi 400 over the direct LAN cable, `docs/ROADMAP.md` M-table
row), and the further-future aspirations — terminal, chiptune-backed retro
editor UI, DOS-ops via an emulated SAM — sit past M7.

## Authoritative references

Each strand/item above traces to one of these; cite these, not this doc,
when grounding a claim.

- Bump-arena: `memory/m6_strand_a_complete.md:51`;
  `docs/notes/2026-05-28-memory-layout-brainstorm.md:122,129`;
  `docs/notes/2026-05-28-z80-table-sizing-census.md` (sizing data).
- Codegen-from-Go-authority: `docs/plans/2026-05-29-m6-closure-release-bytematch.md`
  § "M7 — sketch" (PR-A); `tools/sam-aarch64-format/sysregs.go`.
- On-SAM disassembler: `docs/plans/2026-05-28-go-aarch64-disassembler.md`;
  branch `strand-b-1-disassembler`.
- Compact `.tbn`: `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`.
- Editor groundwork: `docs/ROADMAP.md` "Editor vision (Phase 2)".
- Housekeeping track + sysreg sync guard: `docs/notes/2026-05-29-repo-audit.md`
  (§5, §6).
- Go-harness follow-ups: `docs/notes/2026-05-29-go-harness-fidelity-investigation.md`
  (Q4).
- M6 closure (the predecessor milestone): `docs/plans/2026-05-29-m6-closure-release-bytematch.md`;
  `docs/notes/m6-status.md`.
- Roadmap index: `docs/ROADMAP.md`.

---

*This is an initial planning doc. When M6 closes (PR-5 of the M6-closure
plan flips M6 ✅), the standalone M7 plan should be written and the strands
above re-prioritised and sequenced. Until then, treat this as a backlog
register, not a committed plan.*
