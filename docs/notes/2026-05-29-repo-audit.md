# Repo audit — dead code, doc staleness, README gaps, cleanup plan

**Date:** 2026-05-29
**Scope:** repo-wide analysis of `sam-aarch64` at `main` (post-PR-#60), run in parallel
with the M6-closure work. **This is an analysis-only deliverable** — the only file added
is this doc. Every cleanup it identifies is intended to land as a small, separately
reviewed follow-up PR so the in-flight M6 work is not churned.

Method notes / evidence basis:
- Go: `go vet ./...` (clean across all 16 modules) + `staticcheck -checks U1000 ./...`
  (rebuilt against go1.26 — the Homebrew-bundled staticcheck ships a go1.25 type-checker
  and silently skips modules pinned to go1.26, which is why an earlier run reported
  nothing). Plus targeted `grep` for zero-reference symbols.
- Z80: column-0 label extraction across the 16 production includes, then per-label
  whole-tree reference counts. Candidates flagged as "verify" rather than "definitely
  dead" because pyz80 symbols can be referenced indirectly.
- Docs: `git log` provenance + cross-reference against the closure plan and memory index.

Honesty markers: items are tagged **[confirmed]** (grep/tool shows zero live references)
or **[verify]** (looks unused but a reviewer should confirm before removal).

---

## 1. Directory inventory + README gaps

Only **3** directories currently carry a `README.md`: repo root, `docs/notes/archive/`,
and `tools/z80-test-harness-go/`. `tools/z80-test-harness-go/` additionally has a
`SCOPE.md`. Everything else relies on file-level header comments (the Z80 `.asm` files
and most Go files do have good headers) or on the docs/notes status files.

The repo is a single-developer project, so most dirs do **not** warrant a README —
file headers + the memory index + status docs already serve as the entry points. The
gaps worth filling are the dirs a *newcomer or a freshly-spawned agent* hits first and
where the purpose is non-obvious from the contents.

| Directory | Purpose (one line) | README? | Warrants one? |
|---|---|---|---|
| `/` (root) | Project entry; build via `make` / `scripts/` | yes | yes (have) |
| `.github/workflows/` | `ci.yml` — Docker image build + SimCoupé fixture matrix (the CI gate) | no | low (single file, well-commented) |
| `src/` | Z80 sources: M3 assembler (`m3/`) + M0 boot stubs (`stub*.asm`, `sam_io.inc`) | no | **med** — clarify stub-vs-assembler split |
| `src/m3/` | The SAM-side Z80 aarch64 assembler (production + BUILD_TESTS self-tests + off-axis payloads) | no | **HIGH** — 40 files, prod/test/payload mix is the single most confusing dir for a newcomer |
| `src/m3/slots/` | Per-instruction-class operand-slot encoders (one file per encoding family) | no | low (consistent headers) |
| `tests/m{1,3,4,5,6,spectrum4}/` | Per-milestone fixture corpora (`sources/`, `golden/`) | no | low |
| `tests/fixtures/` | Shared fixtures (`nop.s`) | no | low |
| `docs/` | Roadmap + notes + plans + specs + vendored reference PDFs | no | low (ROADMAP.md is the entry) |
| `docs/notes/` | Rolling status + investigation notes (33 files) | no | **med** — a short index/README would help triage stale-vs-live |
| `docs/notes/archive/` | Superseded notes kept for provenance | yes | yes (have) |
| `docs/plans/`, `docs/specs/` | Per-milestone plans/specs (dated filenames) | no | low (dates self-order) |
| `docs/{sam,comet,saa1099}/` | Vendored hardware PDFs + extracted text | no | low |
| `reference/arm-mra/` | ARM Machine-Readable-Architecture XML (encoding source of truth) | no | **med** — provenance/licence of vendored ARM data is worth one line |
| `reference/comet-{decoded,disk}/`, `reference/samdos/` | Reverse-engineered COMET + SAMDOS reference material | no | low |
| `scripts/` | `build-spectrum4-release.sh` (tbn→bin byte-match build) | no | low (single file) |
| `tools/` | ~20 Go tools + ~15 shell wrappers (mixed: live toolchain, dev tools, dead spikes) | no | **HIGH** — the loose `.sh` + spike/live mix is hard to navigate; a top-level tool index is high value |
| `tools/aarch64enc/` | **Live** Go aarch64 encoder core (imported by refenc, enctab-gen, text2bin) | no | **med** |
| `tools/sam-aarch64-format/` | **Live** `.tbn` format library + sysreg/mnemonic tables | no | **med** |
| `tools/refenc/` | **Live** reference encoder: `.tbn` → release `.bin` (the byte-match oracle) | no | **med** |
| `tools/enctab-gen/` | **Live** encoder-table generator (`+ emit/ mra/ scripts/` subpkgs) | no | low |
| `tools/text2bin/`, `tools/bin2text/` | **Live** text↔tbn round-trip tools | no | low |
| `tools/build-disk/`, `build-m3-disk/`, `build-screen-disk/` | **Live** SAM disk image builders | no | low |
| `tools/flatten-s/` | **Live** `.s` include-flattener | no | low |
| `tools/z80-test-harness-go/` | **Live** dev harness (not a CI gate) | yes+SCOPE | yes (have) |
| `tools/basic-emulator-spike/`, `basic-detokeniser-spike/`, `basic-detokeniser-sweep/` | BASIC tokeniser/detokeniser **spikes** (see memory; future-work, not on any build path) | no | **med** — a one-line "spike, not built by CI" marker prevents confusion |
| `tools/llist-capture{,/builder}/`, `llist-normalise/`, `llist-sweep/` | LLIST-based detokeniser cluster — **superseded** approach (memory: replaced by EDIT/EDKY spike) | no | see §3 (recommend archive/delete, not README) |

**Prioritised README list (high value first):**
1. `tools/` top-level index README — one table mapping each tool/script to: live-toolchain / dev-tool / spike / superseded, and which `make` target (if any) builds it. Highest newcomer value.
2. `src/m3/` README — explain the production-vs-`BUILD_TESTS`-vs-off-axis-payload file taxonomy and the `assembler.asm` include order. This dir's structure is genuinely non-obvious.
3. `src/` README (short) — stub-vs-assembler split.
4. `docs/notes/` README/index — live-vs-archived triage pointer (lower value; the memory index partly covers this).
5. `reference/arm-mra/` one-liner on ARM MRA provenance.

---

## 2. Dead / unused code candidates

### Go (tool-confirmed)

| Symbol | Location | Status | Evidence |
|---|---|---|---|
| `func (*Hardware).peekPage` | `tools/z80-test-harness-go/harness.go:401` | **[confirmed]** dead | staticcheck U1000; grep shows def + doc-comment only |
| `func (*Hardware).pokePage` | `tools/z80-test-harness-go/harness.go:407` | **[confirmed]** dead | staticcheck U1000; grep shows def + doc-comment only |
| `const outBasePage` | `tools/z80-test-harness-go/harness.go:73` | **[confirmed]** dead | only other hit (`:662`) is inside a `//` comment — zero live uses |
| `const lmprEnctab` | `tools/z80-test-harness-go/harness.go:101` | **[confirmed]** dead | grep shows definition line only, zero references |
| `func normalizeForCompare` | `tools/llist-sweep/main.go:385` | **[confirmed]** dead | staticcheck U1000 |
| `func stripSpacesOutsideStrings` | `tools/llist-sweep/main.go:423` | **[confirmed]** dead | staticcheck U1000 |
| `func stripSpacesOutsideStrings` | `tools/basic-detokeniser-sweep/main.go:512` | **[confirmed]** dead | staticcheck U1000 |
| `func stripSpacesOutsideStringsLine` | `tools/basic-detokeniser-sweep/main.go:523` | **[confirmed]** dead | staticcheck U1000 |
| `func directiveSize` | `tools/refenc/pass1.go:311` | **[confirmed]** dead | staticcheck U1000 (thin wrapper over `directiveSizeAtPC`, never called) |

Plus one non-dead-code quality finding from full staticcheck (see §6 quality items):
- `tools/aarch64enc/slots_logical.go:80` — `element = expected` is a dead store
  (SA4006: "this value of element is never used"). `element` is reassigned but never
  read afterward. Low-risk tidy.
- `tools/basic-detokeniser-sweep/main.go:254` — `string(b2tOut.Bytes())` should be
  `b2tOut.String()` (S1030). Cosmetic.

The brief's flagged examples (`outBasePage`, `lmprEnctab`, `peekPage`, `pokePage`) are
**all confirmed dead** above. The harness `const`s/methods are the only dead symbols in a
*live* tool; the `llist-sweep` / `basic-detokeniser-sweep` dead funcs sit inside dirs that
§3 recommends archiving anyway, so they'd disappear with that move.

`go vet` is **clean across all 16 modules** — no other vet-class issues.

### Z80 (grep-confirmed defined-but-unreferenced labels)

Whole-`src/m3/`-tree reference count == 1 (definition only):

| Label | Location | Kind | Status | Note |
|---|---|---|---|---|
| `SYMTAB_EMPTY_ID` | `symbols.asm:80` | `equ &FFFF` | **[verify]** unused | documentary sentinel constant; never referenced in code |
| `SYMTAB_END_OF_CHAIN` | `symbols.asm:81` | `equ &FFFF` | **[verify]** unused | ditto |
| `SYMTAB_ENTRY_SIZE` | `symbols.asm:78` | `equ 8` | **[verify]** unused | ditto — the table stride 8 is presumably open-coded elsewhere |
| `main_handle_inst_pass2` | `main_loop.asm:505` | code label | **[verify]** unused name | reached only by fall-through (line 503 comment literally says "fall through"); the label is a dead *name*, the code runs |
| `pass_pc_carry_b1` | `main_loop.asm:308` | code label | **[verify]** unused name | internal fall-through carry-propagation target; dead name, live code |

These are the **only** five defined-but-unreferenced production labels out of 865 column-0
labels. The two code labels are harmless documentation (removing them is cosmetic and
risks confusing the carry-propagation/fall-through reading — low value, arguably keep).
The three `equ` sentinels are the cleaner candidates: either wire `SYMTAB_ENTRY_SIZE`
into the open-coded stride for self-documentation, or drop them. **Verify before removal**
— `equ` names can be referenced from off-axis payloads assembled with `--importfile` or
from comments that double as the spec.

**Files that are *not* dead** (checked because they have zero `include` hits in
`assembler.asm`): `sysreg_data.asm`, `paged_call_test_payload.asm`, `test_mem_offaxis.asm`
are each assembled **standalone** by dedicated `Makefile` targets
(`sysreg-data` / `paged-call-payload` / `test-mem-offaxis`) into off-axis `.bin` payloads.
Not orphaned.

### Genuinely orphaned Z80 file

| File | Status | Evidence |
|---|---|---|
| `src/stub-border-test.asm` | **[confirmed]** orphan | zero references anywhere (`.go`/`.sh`/`.yml`/`Makefile`/`.asm`); `git log` shows it as `exp: minimal border-set stub for CLEAR-crash isolation` — a one-off debug stub from the (long-resolved) CLEAR investigation, never wired into any build |

---

## 3. Docs / spikes / notes staleness

### Already resolved — NOT findings (verified during audit)

- **Split-bracket primitives** (`paged_data_map_hmpr` / `paged_data_unmap_hmpr`, dropped in
  PR #55): the live-spec references were already removed by PR #58 (commit `b963516`,
  "docs: drop stale split-bracket refs from paged-call arch §5 / §6 PR-2"). The 18 remaining
  mentions in `docs/notes/2026-05-28-paged-call-architecture.md` are all inside the §4.2
  "REJECTED" critique block and the §6 "PR-1 (original, superseded)" historical-record
  block — **intentionally retained** per the closure plan's explicit instruction. No action.

### Archive candidates

| Item | Recommendation | Reason |
|---|---|---|
| `docs/notes/2026-05-28-session-handoff.md` | **archive** | Its own header says "**Supersedes**" is claimed by `2026-05-28-eod-session-handoff.md`; the eod doc explicitly supersedes it. Two same-day handoffs, one stale. |
| `tools/llist-capture/`, `tools/llist-capture/builder/`, `tools/llist-normalise/`, `tools/llist-sweep/` + `tools/llist-*.sh` (`llist-capture.sh`, `llist-capture-docker.sh`, `llist-capture-headless.sh`, `llist-vs-b2t.sh`) | **archive or delete** | The LLIST-based basic→text approach is **superseded** by the EDIT/EDKY detokeniser spike (memory: `future_basic_to_text` "SUPERSEDED"). This is a self-contained cluster: the only references to the llist tools are the llist `.sh` scripts themselves; nothing in CI, `Makefile`, or other tools depends on them. Last touched 2026-05-20. Removing the whole cluster also retires the §2 `llist-sweep` dead funcs. **Verify** with Pete that the spike findings are captured in `docs/notes/basic-detokeniser-spike.md` first (they are referenced there). |

### Keep but mark / lower-confidence

- **m-status docs** (`m0`–`m6-status.md`): `m6-status.md` header says "M6 IN PROGRESS — PR 2
  of N landed", which is stale relative to `main` (PRs #55–#60 have since landed and the
  M6-closure plan exists). This is **live in-flight work** — leave it to the M6-closure track
  to refresh rather than touching it here (avoids racing the parallel work). Flag only.
- `m0`–`m5-status.md`: each is correctly marked COMPLETE and serves as the milestone record;
  the memory index already routes readers to the latest. Keep. Could eventually move
  `m0`–`m2` to `docs/notes/archive/` once nobody re-enters those milestones, but value is low.
- **Spike tools** `tools/basic-emulator-spike/`, `tools/basic-detokeniser-spike/`,
  `tools/basic-detokeniser-sweep/`: still the *active* future-work approach per memory
  (`spike_basic_rom_emulation`, `spike_basic_detokeniser`). **Keep**, but a one-line
  "spike — not built by CI" marker (in the §1 tools README) would prevent them being
  mistaken for live toolchain.
- `docs/plans/` + `docs/specs/`: per-milestone, dated, self-ordering. No duplication found.
  These are the historical plan record by design — keep.

No duplicate-content pairs found beyond the two same-day handoffs.

---

## 4. Z80 source file-by-file (high-level pass)

Production includes (in `assembler.asm` order). Sizes in lines.

| File | Lines | Purpose | At-a-glance notes |
|---|---|---|---|
| `io.asm` | 8 | SAMDOS hook wrappers (`fill_uifa`, `open_input`, …) | trivially thin; fine |
| `trampoline.asm` | 654 | Paged-RAM trampoline for HLOAD + ENCTAB + paged_call | dense paging machinery; reviewed deeply in PR #55 era; candidate for a *light* re-read but well-commented |
| `loader.asm` | 396 | `enctab.enc` reader/validator + off-axis payload loaders | fine |
| `ml.asm` | 299 | 64-bit multi-byte arithmetic helpers | fine |
| `expr_eval.asm` | 852 | Constant-expression bytecode evaluator | large but cohesive; OK |
| `form_lookup.asm` | 367 | Mnemonic index walk + form lookup | fine |
| `encoder.asm` | 546 | Top-level instruction encoder | fine |
| `intercepts.asm` | 560 | Mnemonic-ID intercepts before form lookup | fine |
| `sysname.asm` | 785 | OpSysName encoders; split with off-axis `sysreg_data.asm` | see §5 — hand-synced table risk |
| `reader.asm` | 265 | `.tbn` record stream walker (paged IN) | fine |
| **`main_loop.asm`** | **2362** | Top-level M3/M4 driver | **LARGEST file by far; flag for a dedicated deep review.** Holds the two dead fall-through labels (§2). A 2362-line driver is the most likely place for accreted/tangled logic. |
| `symbols.asm` | 412 | Global symbol table (M4 multi-pass) | holds the 3 unused `equ` sentinels (§2) |
| `local_labels.asm` | 431 | Local-label table | fine |
| **`litpool.asm`** | **1138** | Literal-pool structures + pass-1/2 helpers | **second-largest; flag for light review** — large enough to warrant a structure pass |
| `print.asm` | 71 | Status banner over SAM Centronics port → SimCoupé file | **Not a finding**: this is the assembler's own pass/fail banner over a *real* SAM parallel port (works on hardware), distinct from the memory rule against SimCoupé-only test side-channels. Comment is accurate. |

**Deeper-review candidates (for a later scheduled pass, not now):** `main_loop.asm` (2362 lines)
is the clear #1; `litpool.asm` (1138) is #2. Both are flagged as size/complexity candidates,
not as known-defective. No obvious comment-accuracy problems surfaced in this glance-level pass.

---

## 5. Go ↔ Z80 relationship (light)

Per Pete's framing: **Go↔Z80 parity is not a correctness goal.** The authoritative test is
the byte-for-byte match of the full release binary against GNU binutils
(`scripts/build-spectrum4-release.sh`, the `refenc` oracle, and the SimCoupé CI matrix).
Both implementations were written by earlier models; either may have flaws. **No parity
audit is proposed.**

One drift risk worth recording (single concrete instance, not a campaign):

- **Sysreg/sysname tables are hand-maintained on both sides.** `src/m3/sysreg_data.asm:214`
  and `:256` explicitly state the Z80 table entries were "verified vs
  `tools/sam-aarch64-format/sysregs.go`". Two independently hand-edited tables that must
  agree (MRS/MSR/DC/TLBI op0/op1/CRn/CRm/op2 encodings) is a real maintenance hazard: adding
  a sysreg on one side and forgetting the other would pass each side's own tests but diverge.
  The release byte-match would catch it *only if a fixture exercises the new register*.
  **Recommendation (low effort, defer):** a small CI/dev check that diffs the two tables, or
  a comment cross-link making the sync obligation impossible to miss. Not urgent.

---

## 6. Prioritised cleanup plan

| # | Cleanup item | Value | Risk | Effort | Suggested home |
|---|---|---|---|---|---|
| 1 | Remove confirmed dead Go symbols in `z80-test-harness-go` (`peekPage`, `pokePage`, `outBasePage`, `lmprEnctab`) | med | low | S | immediate small PR |
| 2 | Remove `directiveSize` (refenc) + SA4006 dead store (aarch64enc `:80`) + S1030 cosmetic | low | low | S | immediate small PR (bundle with #1) |
| 3 | Add `tools/` top-level index README (live / dev / spike / superseded + make target) | high | low | M | immediate small PR |
| 4 | Add `src/m3/` README (prod/test/off-axis taxonomy + include order) | high | low | S | immediate small PR |
| 5 | Archive `2026-05-28-session-handoff.md` → `docs/notes/archive/` | med | low | S | immediate small PR (bundle with #6) |
| 6 | Archive-or-delete the LLIST cluster (`llist-*` tool dirs + `llist-*.sh`) after confirming spike findings are captured | med | low* | M | small PR (needs one Pete confirm) |
| 7 | Delete orphan `src/stub-border-test.asm` | low | low | S | immediate small PR |
| 8 | Resolve the 3 unused `SYMTAB_*` `equ` sentinels (wire in or drop) | low | low | S | M7 housekeeping (verify first) |
| 9 | Sysreg Go↔Z80 sync guard (diff check or cross-link comment) | med | low | M | defer / M7 housekeeping |
| 10 | Dead-name Z80 labels `main_handle_inst_pass2`, `pass_pc_carry_b1` | low | low | S | defer (arguably keep — documents fall-through) |
| 11 | Deep review of `main_loop.asm` (2362 lines) | med | n/a | L | M7 housekeeping (scheduled review, not blind edit) |
| 12 | Light structure review of `litpool.asm` (1138 lines) | low | n/a | M | M7 housekeeping |
| 13 | Add `src/`, `docs/notes/` index, `reference/arm-mra/` READMEs | med | low | S | M7 housekeeping |

\* #6 risk is low *only* after confirming the spike findings live in
`docs/notes/basic-detokeniser-spike.md` and that nothing Pete still uses depends on the
llist scripts.

### Recommended concrete follow-up PRs

1. **PR: remove confirmed dead Go symbols** — items #1, #2, #7 (harness consts/methods,
   `directiveSize`, SA4006/S1030, orphan stub). Pure deletions, tool-verified, zero risk.
   Re-run `make ci` + `staticcheck -checks U1000` as the gate.
2. **PR: add `tools/` and `src/m3/` index READMEs** — items #3, #4. Highest newcomer/agent
   value; no code touched.
3. **PR: archive superseded docs + LLIST cluster** — items #5, #6 (one Pete confirm on #6).
4. **(M7) PR: deep review of `main_loop.asm`** — item #11, scheduled, with the audit's
   dead-label findings folded in.
5. **(M7) PR: sysreg sync guard** — item #9.

### Track recommendation

Run cleanup as a **dedicated M7 housekeeping track that does NOT block M6 closure**, with
**PRs 1–3 above interleaved now** (they are tiny, tool-verified, and touch nothing on the
M6 critical path), and the larger reviews (#11, #12, #9) scheduled into M7 proper. Defer
anything that touches `main_loop.asm` / `symbols.asm` bodies until the M6-closure PRs that
edit those files have merged, to avoid conflicts.

---

## Headline counts

- Directories without a README: **~30 significant dirs; only 3 have one.** High-value gaps: **2** (`tools/`, `src/m3/`); med-value: ~4.
- Dead-code candidates: **9 Go symbols confirmed dead** (+2 minor quality findings), **1 orphan Z80 file confirmed**, **5 Z80 labels flagged verify** (3 `equ` sentinels + 2 fall-through names).
- Stale/superseded docs: **1 confirmed superseded handoff to archive**, **1 superseded tool cluster (LLIST)**, **1 stale-but-leave-to-M6 status header** (`m6-status.md`). The split-bracket doc cleanup was already done (PR #58) — not a finding.
- Go↔Z80 drift risks: **1 concrete** (hand-synced sysreg tables).
