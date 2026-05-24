# M7 — current status (read me first)

Entry point for any session picking up M7. This is an **initial planning
doc** — a parking place for the M7 backlog that's currently scattered
across notes and memory, gathered ahead of time so nothing is lost in the
M6 → M7 transition. It will be refined (and the strands re-prioritised /
sequenced) when M6 closes and the standalone M7 plan is written.

**M7 — NOT STARTED (planning).** M6 closing via
`docs/plans/2026-05-29-m6-closure-release-bytematch.md`; M7 opens once M6
flips ✅.

## M7 scope (planning snapshot)

Legend: ✅ done · ⏳ in progress · 📋 designed/plan-ready · 🧭 idea/not-yet-designed

| Strand | Status | Spec/Source | Notes |
|---|---|---|---|
| Bump-arena allocator (Go-slices-vs-fixed-arrays for the Z80 data structures) | 🧭 | `memory/m6_strand_a_complete.md:51`; `docs/notes/2026-05-28-memory-layout-brainstorm.md:122,129`; motivation/data in `docs/notes/2026-05-28-z80-table-sizing-census.md` | Headline structural item. Named direction, not yet designed. Durable fix for the fixed-table-overflow class. |
| Codegen sysreg / mnemonic / form tables from Go authority | 📋 | M6-closure plan §M7 sketch (PR-A), `tools/sam-aarch64-format/sysregs.go` | Kills hand-sync drift. Depends on M6 PR-2's page-13 binary build glue. |
| On-SAM disassembler (strand B) | 📋 | `docs/plans/2026-05-28-go-aarch64-disassembler.md`; branch `strand-b-1-disassembler` (5 commits, parked) | Resume per Pete's redirect after M6 closes. |
| Compact `.tbn` format | 📋 | `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md` | Future-proofing for sources beyond the paged-IN ceiling. |
| Editor groundwork (Phase 2) | 🧭 | `docs/ROADMAP.md` "Editor vision" section | Instruction-explanation panel, register simulator, sysreg docs, did-you-mean. Not yet spec'd. |
| Repo-cleanup / README housekeeping track | ⏳ | `docs/notes/2026-05-29-repo-audit.md` §6 (prioritised plan) | Track underway. **Landed: PR #78** (dead Go symbols + orphan stub removed), **PR #80** (`tools/` + `src/README.md` index READMEs). Remaining: deep reviews of `main_loop.asm` (#11) / `litpool.asm` (#12), and the `SYMTAB_*` `equ` sentinels (#8). Does NOT block other M7 work. |
| Directory naming & logical organisation (rename `src/m3`) | ⏳ | Pete 2026-05-29 (decision delegated to agent) | **Decided: flatten `src/m3/` → `src/`** (flat, no component subfolder — see resolved open-question 1). Rename PR in flight. The wider naming review (`tests/m{3..6}`, `ci-m{N}` targets, milestone-named fixtures) remains as later M7 cleanup. |
| Canonical memory-layout reference doc | ✅ | PR #80 → `docs/notes/memory-layout.md` | Done. Mirrors the authoritative `src/m3/assembler.asm` header map (source of truth; doc points to it, no drift). |
| Sysreg Go↔Z80 sync guard | ✅ | PR #79 → `tools/sam-aarch64-format/sysregs_z80sync_test.go` + `sysreg-sync` CI job (now a required check) | Done. Go test parses `src/m3/sysreg_data.asm` and asserts every Z80 entry byte-matches the Go authority (the Z80 table is an intentional subset). |
| Linker-layout coupling (flatten hardcodes `spectrum4.ld`) | 🧭 | Pete 2026-05-29; `tools/text2bin/internal/translate/flatten.go` `SpectrumFourLayout` | Byte-equivalence on the release relies on text2bin's flatten pass **hardcoding** spectrum4.ld's section order + ALIGNs + origin (we do NOT parse the `.ld`). The m6-release 3-way gate guards drift (a `.ld` change → re-vendor → gate fails until flatten is updated), but it's an implicit coupling. Consider: parse `spectrum4.ld` directly, or vendor it + a checked cross-reference, or at minimum document the coupling beside `SpectrumFourLayout`. |
| Go-harness fidelity follow-ups | 📋 | `docs/notes/2026-05-29-go-harness-fidelity-investigation.md` Q4 | Write-watchpoint activation, `make harness-sweep` target, USAGE.md ledger. NOT real-ROM execution. **PLUS: root-cause the paged-path trap** — harness traps (PC→`&0038`) on the full 88 KB / 6-page paged-IN load where SimCoupé succeeds (`docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md`). Pete: ideally M6 (tracked primarily in `m6-status.md`), acceptable M7. |
| Z80↔Go encoding/operator parity audit | 🧭 | Pete 2026-05-29; `tools/sam-aarch64-format` / refenc is authoritative | Systematically ensure the Z80 side implements the same instruction encodings AND expression operators as the Go library. M6 closes only what release-stripped needs; full parity is M7. |
| SAM screen-mode decision (editor) | 🧭 | Pete 2026-05-29; ROADMAP "Editor vision" | MODE 3 currently assumed. Decide mode(s) by colour-vs-resolution + aesthetics nearer the editor; the choice consumes display RAM / pages, so it feeds the memory-layout doc below. |
| Canonical memory-layout reference doc | 📋 | Pete 2026-05-29; `src/m3/assembler.asm:21-122` (authoritative live map) | Consolidate the section/page map + scratch regions + budget ceilings (today scattered across the asm comments, `sam-paging.md`, the layout brainstorm, ~10 notes) into one doc. Keep the asm comments as source-of-truth; the doc mirrors/points to them (no second drifting copy). High value given how central layout is. |
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
   re-org when those components arrive with real boundaries. Rename PR in
   flight (mechanical: `git mv`, all `src/m3` references, README move).
2. **LLIST tool cluster disposition** (`tools/llist-*` + `llist-*.sh`).
   **⏸ PUNTED (Pete 2026-05-29) — revisit later.** Pete hasn't decided;
   leave the cluster exactly in place. `tools/README.md` (PR #80) marks it
   "superseded by the EDIT/EDKY detokeniser spike — disposition pending".
   Nothing to do until Pete chooses archive/delete/keep.
3. **Design-strand handling — ✅ RESOLVED (Pete 2026-05-29).** Pete is
   happy for the agent to drive design + brainstorming solo (review later).
   Editor/screen-mode aesthetic input can come at review time. See the
   bump-arena strand below for Pete's substantive framing on that one.
4. **`src/stub.asm` — retire the M0 round-trip oracle, or keep it?**
   ⏳ NEW (2026-05-29). Pete flagged the old "assemble a nop, write OUT to
   disk" stub as probably no-longer-needed. But it is NOT dead: it's the
   M0 end-to-end round-trip oracle (`make ci` → `make stub` → `build/stub.bin`),
   and the **`test` CI job (a required check) runs `make ci`**. So it's the
   most basic pyz80→SimCoupé→samfile→GNU smoke test. It's arguably subsumed
   now by the M3–M6 fixture corpus + the m6-release gate (all of which do
   the same round-trip with real encoding). Retiring it means removing
   `src/stub.asm` + `tools/build-stub.sh` + the nop fixture + the `test`
   CI job + dropping `test` from branch-protection's required checks — a
   judgement call (keep the minimal smoke test, or rely on M3+?). **Logged,
   not actioned** — needs Pete's decision (and a branch-protection edit).
5. **`tools/llist-normalise/llist-normalise` is a committed binary** (an
   accidental check-in spotted during the rename). Folds into the punted
   LLIST-cluster disposition (open question 2) — handle together.

## Strands in detail

### Bump-arena allocator (headline structural item)

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
covers: removing confirmed-dead Go symbols, adding `tools/` + `src/m3/`
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
both sides and must agree (the Z80 entries at `src/m3/sysreg_data.asm:214`
and `:256` are annotated "verified vs `tools/sam-aarch64-format/sysregs.go`").
The recommendation is a small dev/CI check that diffs the two tables, or a
cross-link comment making the sync obligation impossible to miss. This is
the tactical near-term guard; the codegen-from-Go-authority strand above is
the strategic fix that dissolves the drift entirely. 📋.

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
