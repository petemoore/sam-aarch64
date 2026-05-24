# M6 closure — spectrum4 release-bytematch on SAM

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Drive M6's stated headline ("spectrum4 release.bin byte-match on SAM") all the way home. After this plan lands, the SAM-side assembler reads `release-stripped.tbn` (~88 KB), assembles it, and `HSAVE`s a binary byte-identical to GNU `aarch64-none-elf-as + ld + objcopy` on `release.s` — and a CI job enforces that.

**Architecture:** The mechanism foundation is already on `main` (paged_call primitive landed in PR #55; test-corpus off-axis pattern landed in PR #52; release-stripped flatten landed in PR #48; Go Z80 harness landed in PR #54). The remaining work is **(a) the first real consumer of paged_call** (sysreg_table on physical page 13 + the 8 missing sysreg entries that FAIL00 blocks on), **(b) post-FAIL00 failure surfacing** (FAIL40+ closure once FAIL00 unblocks the run), **(c) wiring the spectrum4 byte-match into CI as a gate**, plus **(d) two non-blocking inner-loop housekeeping tasks** that ride alongside: the editorial fix for the architecture doc §4 stale references, and the PR #42 SP-fix re-investigation (which doubles as the Go harness's first real use).

**Tech Stack:** Same as M3..M6 inner loop — pyz80, SimCoupé (Docker) as CI gate, Go (`tools/z80-test-harness-go/`) as inner-loop dev tool, GNU `aarch64-none-elf-as` as oracle, `text2bin -strip-comments` for the release feed.

**Reference docs (READ FIRST):**
- `docs/notes/2026-05-28-eod-session-handoff.md` — authoritative state at the start of this plan.
- `docs/notes/2026-05-28-paged-call-architecture.md` — design for the paged_call mechanism that PR-2 here consumes. §3.3 (final spec) and §6 PR-2 scope are load-bearing; **§4.2, §5 and §6 PR-2 still reference the dropped `paged_data_map_hmpr` / `paged_data_unmap_hmpr` primitives — see PR-1 below for the editorial fix.**
- `docs/notes/m6-status.md` — M6 strands as they stood after PRs #36, #37.
- `docs/notes/2026-05-28-memory-layout-brainstorm.md` — page-axis allocation, especially the page-13 assignment for paged data tables.
- `tools/z80-test-harness-go/README.md` + `docs/notes/2026-05-28-test-harness-bakeoff-evaluation.md` — Go harness positioning (dev tool, not CI gate).

---

## M6 vs M7 — the milestone-boundary call

M6's headline since the milestone opened has been "spectrum4 release.bin byte-match on SAM" (`docs/notes/m6-status.md:23`, "ultimate goal"). The mechanism work to enable it (paged OUT, paged IN, paged_call, multi-digit local labels, release-stripped flatten) has now all landed. What remains — sysregs on page 13 + 8 entries + FAIL40 closure + CI gate — is **direct execution against that headline**, not a new milestone.

**This plan therefore closes M6.** Once the CI gate (PR-5 below) is green, M6 flips to ✅ done.

Two M6-scope strands explicitly NOT in this plan and which form the seed of **M7**:

- **Compact `.tbn` format** (`docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`) — only needed if a future source exceeds the paged-IN ceiling. `release-stripped.tbn` is 88 644 B (well under). M6's status doc lists it as "designed", not "required for the headline".
- **On-SAM disassembler** — parked at the user's request (`docs/notes/2026-05-28-eod-session-handoff.md` open thread #6). Branch `strand-b-1-disassembler` waits with 5 commits.

These plus **codegen sysreg / mnemonic tables from Go-side authority** (architecture doc §6 PR-4) belong in **M7 — "shared data structures + on-SAM disassembler + editor groundwork"**. Sketch at the bottom of this plan; standalone M7 plan to follow once M6 closes.

---

## Conventions

- `g` not `git` for commits (preserves timestamps Pete's workflow depends on).
- Each PR opens **ready-for-review** (project-local override of the global draft default; see `~/git/sam-aarch64/CLAUDE.md`).
- PRs land via `gh pr merge --merge --delete-branch`. Never `--squash` / `--rebase` (PR #43 incident — `memory/feedback_merge_commits.md`).
- After every Z80-side code change, run `make ci-m3 ci-m4 ci-m5 ci-m6` + `-prod` variants locally. The harness (`tools/z80-test-harness-go/`) is the inner-loop tool; SimCoupé is the gate.
- Commit per task. Prefix commits as in M5: `m6: <subject>` for asm changes, `docs: <subject>` for docs-only.
- Co-Authored-By trailer on every commit:
  ```
  Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
  ```
- Test-variant budget watchpoint: `&BF6C` is the current end (`m6-status` post-#55). Headroom under `&C100` is 403 B. Don't push past `&C0C0` without stopping to report — per the test-variant fragility rule (`memory/feedback_test_variant_fragility.md`), the next budget breach hides as deterministic boot-hang.
- Prod budget watchpoint: `&AFE8` post-#55, headroom under `&B000` = 23 B. **Already tight.** Net additions must be data-only (moved to page 13) or balanced by deletions. The 8 sysreg entries land off-axis on page 13, so PR-2's net prod impact is the table-deletion savings minus the page-13 binding glue — should net negative.

---

## Sequence (5 PRs, dependency-ordered)

The PRs land in numerical order. PR-1 is docs-only and can be opened immediately; PR-2 is the heavy lift; PR-3 unblocks PR-4; PR-4 closes the M6 headline; PR-5 is the gate. PR-6 is non-blocking but recommended in parallel with PR-2.

```
PR-1  editorial: paged-call arch doc §4 / §5 / §6 PR-2 stale refs
        │
        ▼
PR-2  m6: sysreg_table off-axis on page 13 + 8 missing sysregs    ← CLOSES FAIL00
        │
        ▼
PR-3  m6: post-FAIL00 surfacing — FAIL40+ closure                 ← CLOSES THE LAST FAIL SITE
        │
        ▼
PR-4  m6: spectrum4 release.bin byte-match on SAM (verification + status doc)
        │
        ▼
PR-5  ci: spectrum4 release-bytematch gate (the m6-headline gate) ← FLIPS M6 ⏳ → ✅

PR-6  (parallel) m6: reader self-test re-enable / PR #42 SP-fix re-investigation
                  via the Go harness  ← FIRST REAL USE OF THE GO HARNESS
```

---

## PR-1 — editorial: paged-call architecture doc stale references

**Why this PR exists**: PR #55 dropped the `paged_data_map_hmpr` / `paged_data_unmap_hmpr` split-bracket primitives (architecture doc §4.2 critique). §3.3 and §4 were patched at salvage time, but §5 (page-axis table) and §6 PR-2's scope still reference those dropped primitives. PR-2 dispatches an implementation subagent that reads §6 PR-2 as its spec — if those stale refs remain, the subagent reproduces them in code. Fix the doc first.

**Files:**
- Modify: `docs/notes/2026-05-28-paged-call-architecture.md` (§5 table footer ~line 870; §6 PR-2 scope ~lines 980-1018)

- [ ] **Step 1: Inspect the existing stale references**

```bash
grep -n "paged_data_map_hmpr\|paged_data_unmap_hmpr" docs/notes/2026-05-28-paged-call-architecture.md
```

Expected output: matches at §4.2 (the explicit "REJECTED" critique block — keep), at §5 footer (~line 870 — stale), at §6 PR-1 (~lines 922, 944, 959 — historic-record block describing the *original* PR-1; keep, marked as superseded), and at §6 PR-2 (~line 993 — stale, mid-scope, needs rewrite).

- [ ] **Step 2: Patch §5's page-axis-table footer paragraph**

The current text (around line 866-871) reads:
```
**Brainstorm doc update needed?** Yes, minor: add a note that the
"new constants alongside LMPR_ENCTAB / LMPR_OUT_HIGH / LMPR_IN_BASE"
mentioned at `memory-layout-brainstorm.md:97` are now
`HMPR_DATA_TABLES = &0D` (page 13 in HMPR low-5 form), etc., and
that the access mechanism is `paged_data_map_hmpr(A)` /
`paged_data_unmap_hmpr` per §4.2 of this note.
```

Rewrite to:
```
**Brainstorm doc update needed?** Yes, minor: add a note that the
"new constants alongside LMPR_ENCTAB / LMPR_OUT_HIGH / LMPR_IN_BASE"
mentioned at `memory-layout-brainstorm.md:97` are now
`HMPR_DATA_TABLES = &0D` (page 13 in HMPR low-5 form), etc.  The
access mechanism per §4 (post-salvage) is `paged_call` into a
target routine that lives on the data page — split-bracket
helpers are NOT used (§4.2 documents why they were rejected).
```

- [ ] **Step 3: Patch §6 PR-2 scope to drop the split-bracket spec text**

Around lines 980-1018, the PR-2 scope block today says (excerpt):

```
- Replace the inline `ld hl, sysreg_table` reads in
  `sysname.asm` with `ld c, HMPR_DATA_TABLES; ld hl, &8000; call
  paged_data_map_hmpr / ldir / call paged_data_unmap_hmpr` per
  §4.2.
```

Rewrite the bullet to:

```
- Replace the inline `ld hl, sysreg_table` reads in
  `sysname.asm` with a `paged_call` to a small lookup routine
  that lives at the head of the page-13 payload.  The lookup
  routine takes the mnemonic-id in a designated register (e.g. C),
  walks the table from its known section-C-shape base (= `&8000`),
  and returns the (op0, op1, CRn, CRm, op2) packed encoding in
  another register (e.g. DE+L) for the caller to compose into the
  instruction word.  See §3 paged_call ABI for register-clobber
  rules.  This is structurally how SAM ROM `R1OFFCLBC`, SAMDOS
  `RST 8`, and the 128K's `RST 28H` all consume their paged code —
  caller hands off, callee runs, callee RETs back through the
  trampoline.
```

Also re-state the "boot-time sequencing" risk-callout immediately below it (currently still mentions page-13 binary generation via "the same HLOAD trampoline mechanism extended to target page 13" — that's fine to keep, but cross-link it to the existing `paged_call_test_payload` Mac-side build glue (the `-paged-call` flag of `tools/build-m3-disk/main.go` added by PR #55) as the closest precedent: this PR-2 generalises that one-payload mechanism to a "data tables" payload.

- [ ] **Step 4: Patch the §6 PR-2 cross-file-addressing risk-callout (open question 8)**

Open question 8 (lines 1147-1162) is in good shape, but it references PR-2 in the phrasing "When `sysname.asm` calls into `paged_call` for sysreg_table lookup". That's already correct. **Leave open question 8 as-is.** No change needed.

- [ ] **Step 5: Verify no broken references remain**

```bash
grep -nC2 "paged_data_map_hmpr\|paged_data_unmap_hmpr" docs/notes/2026-05-28-paged-call-architecture.md
```

The only remaining matches should be inside §4.2 (which documents the rejection — those are intentional historical record) and inside §6 "PR 1 (original, superseded)" (which documents what didn't ship — also intentional historical record). No live spec language should reference the dropped primitives.

- [ ] **Step 6: Commit and open the PR**

```bash
g add docs/notes/2026-05-28-paged-call-architecture.md
g commit -m "$(cat <<'EOF'
docs: drop stale split-bracket refs from paged-call arch §5 / §6 PR-2

PR #55 dropped the paged_data_map_hmpr / paged_data_unmap_hmpr
split-bracket primitives (architecture doc §4.2 rejection block).
§3.3 and §4 intro were patched at salvage time; §5's page-axis
table footer and §6 PR-2's scope still referenced the dropped
mechanism.  This PR restates them in terms of the actual
shipped mechanism: paged_call to a lookup routine that lives at
the head of the data page.

Reviewed against §4.2's REJECTED block and §6 PR-1 history
block — both of those intentionally document the dropped
mechanism and stay as-is.

Pre-req for plan-PR 2 (sysreg_table off-axis); without this fix
an impl subagent reading §6 PR-2 as its spec would reproduce
the broken primitives in code.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
g push -u origin docs/m6-closure-pr1-arch-doc-editorial
gh pr create --title "docs: drop stale split-bracket refs from paged-call arch §5/§6 PR-2" --body "$(cat <<'EOF'
## Why

Pre-requisite for plan-PR 2 (sysreg_table off-axis) — without this fix, an impl subagent reading §6 PR-2 as its spec reproduces the dropped `paged_data_map_hmpr` / `paged_data_unmap_hmpr` primitives in code.

## What

- §5 page-axis table footer: rewrite the "access mechanism is `paged_data_map_hmpr(A)` / `paged_data_unmap_hmpr`" footer to point at §4's post-salvage answer (`paged_call` to a routine living on the data page).
- §6 PR-2 scope block: rewrite the bullet that today says "call `paged_data_map_hmpr` / ldir / call `paged_data_unmap_hmpr`" to describe the actual mechanism (a lookup routine at the head of page 13, called via `paged_call`).
- §4.2 REJECTED block and §6 PR-1 historical block left untouched (they intentionally document what was dropped).

Doc-only, all 11 CI checks should pass.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

Expected: PR opens ready-for-review; CI status checks all pass on doc-only diff. Pete reviews and merges. ~5-10 minutes elapsed.

---

## PR-2 — m6: sysreg_table off-axis on page 13 + 8 missing sysregs (closes FAIL00)

**Why this PR exists**: `src/m3/sysname.asm:716-` holds the (12-entry) sysreg table. The release-stripped flatten needs 8 more (`hcr_el2`, `mair_el1`, `scr_el3`, `spsr_el3`, `tcr_el1`, `ttbr0_el1`, `ttbr1_el1`, `vbar_el1`). Adding ~111 B of sysreg data inline would push the prod variant past `&B000` (current 23-B headroom). The architecture doc's answer: move the table off-axis to physical page 13, call into a lookup routine via `paged_call`, and add the 8 entries "for free" (they don't compete with section-C budget). **This is the first real consumer of `paged_call`** and the PR that closes FAIL00.

**Files:**
- Create: `src/m3/sysreg_data.asm` — the page-13 payload (sysreg table + lookup routine).
- Create: `src/m3/test_sysreg_paged.asm` — boot self-test (BUILD_TESTS only).
- Modify: `src/m3/sysname.asm` — remove the inline `sysreg_table`; rewrite the four `sysname_lookup` / `sysreg_lookup` / `dc_lookup` / `tlbi_lookup` call paths to use `paged_call`.
- Modify: `src/m3/trampoline.asm` — add constants `SYSREG_DATA_PAGE = 13`, `SYSREG_DATA_DST = &8000`, `SYSREG_LOOKUP_ENTRY = &8000` (lookup is at offset 0 of the page).
- Modify: `src/m3/loader.asm` — HLOAD the page-13 payload at boot (uses the trampoline-HLOAD pattern PR #55 introduced for page 14).
- Modify: `tools/build-m3-disk/main.go` — accept `-sysreg-data <path>` flag that deposits the page-13 payload as a CODE file (e.g. `sd13`).
- Modify: `Makefile` — assemble `src/m3/sysreg_data.asm` standalone (org `&8000`), produce `build/sysreg_data.bin`, pass to `build-m3-disk` via the new flag.
- Modify: `src/m3/assembler.asm` — wire `call run_sysreg_paged_self_tests` into the boot test sequence (BUILD_TESTS only) and include `test_sysreg_paged.asm`.

**Prior art / references:**
- `src/m3/paged_call_test_payload.asm` + `src/m3/loader.asm:load_page14_payload` (PR #55) — the load-payload-into-page-N pattern.
- `src/m3/trampoline.asm` — `paged_call` body LDIR'd into section B at boot.
- `docs/notes/2026-05-28-paged-call-architecture.md` §3.3 (ABI), §6 PR-2 (scope, after PR-1's editorial fix), §7 open-question 8 (cross-file addressing convention).
- `docs/notes/2026-05-28-memory-layout-brainstorm.md` — page 13 = "data tables" home.
- `tools/sam-aarch64-format/sysregs.go` — authoritative list of 39 sysreg entries (used only for cross-check here; PR-4 of M7 will use it as codegen input).

### Sub-steps

- [ ] **Step 1: Confirm baseline budget and current sysreg coverage**

```bash
make m3-asm-prod
ls -l build/m3-asm.bin
ls -l build/m3-asm-prod.bin
# Note the byte sizes; m3-asm-prod should be 12056 B; ending at &AFE8 (12056 + &8000 - 1).
grep -E "sysreg_table|pstate_table|dc_table|tlbi_table" src/m3/sysname.asm | head
# Expect 4 tables labelled.
grep -cE "hcr_el2|mair_el1|scr_el3|spsr_el3|tcr_el1|ttbr0_el1|ttbr1_el1|vbar_el1" src/m3/sysname.asm
# Expect 0 — the 8 missing sysregs are not present.
```

Record these in your scratch notes; you'll re-measure at step 11.

- [ ] **Step 2: Write the failing fixture test (Layer 3) — promote a stripped chunk of release.s that uses the missing sysregs**

`release-stripped.tbn` is the integration target, but it's 88 KB — too big to drop into the M6 fixture corpus as a unit. Instead, promote a focused fixture exercising the 8 missing sysregs.

Create `tests/m6/sources/inst_mrs_msr_missing.s`:

```asm
        .text
        // The 8 sysregs that FAIL00 blocks on.  Each tested both directions
        // (mrs reads, msr writes).
        mrs     x0, hcr_el2
        msr     hcr_el2, x0
        mrs     x1, mair_el1
        msr     mair_el1, x1
        mrs     x2, scr_el3
        msr     scr_el3, x2
        mrs     x3, spsr_el3
        msr     spsr_el3, x3
        mrs     x4, tcr_el1
        msr     tcr_el1, x4
        mrs     x5, ttbr0_el1
        msr     ttbr0_el1, x5
        mrs     x6, ttbr1_el1
        msr     ttbr1_el1, x6
        mrs     x7, vbar_el1
        msr     vbar_el1, x7
```

Run the M6 round-trip script:

```bash
./tools/run-m6-roundtrip.sh tests/m6/sources/inst_mrs_msr_missing.s
```

Expected: FAIL ("unknown sysname" or equivalent) — the SAM-side encoder rejects the missing names, the GNU oracle accepts them, byte compare aborts. **This is the failing test that PR-2 is closing.** Confirm the failure mode is what you expected.

- [ ] **Step 3: Write `src/m3/sysreg_data.asm` — the page-13 payload**

The payload contains:
1. A 6-byte header: `paged_call`-callable entry table. Entry 0 is `sysreg_lookup`; entry 1 is `pstate_lookup`; entry 2 is `dc_lookup`; entry 3 is `tlbi_lookup`. Each is a `jp <target>` (3 B per entry). Total 12 B.
2. The four tables themselves: `sysreg_table` (20 entries: 12 existing + 8 new), `pstate_table`, `dc_table`, `tlbi_table`. Move byte-for-byte from `sysname.asm:716-`. Re-base any internal references (`ld hl, sysreg_table`) to the new file's section-C-shape org.
3. The lookup routines: `sysreg_lookup`, `pstate_lookup`, `dc_lookup`, `tlbi_lookup`. Each takes the operand-string pointer in HL (caller's perspective: HL still pointed at the operand text when `paged_call` was issued; section A unchanged across the HMPR swap — `sam-paging.md:140-150` documents the LMPR-HMPR orthogonality), walks its table, and returns either (a) on match: A=1, BCDE=packed (op0, op1, CRn, CRm, op2) encoding; (b) on no-match: A=0.

The 8 new sysreg entries — encoded with their canonical (op0, op1, CRn, CRm, op2) from the aarch64 architecture (cross-check against `tools/sam-aarch64-format/sysregs.go`):

| Sysreg | op0 | op1 | CRn | CRm | op2 |
|---|---|---|---|---|---|
| `hcr_el2` | 3 | 4 | 1 | 1 | 0 |
| `mair_el1` | 3 | 0 | 10 | 2 | 0 |
| `scr_el3` | 3 | 6 | 1 | 1 | 0 |
| `spsr_el3` | 3 | 6 | 4 | 0 | 0 |
| `tcr_el1` | 3 | 0 | 2 | 0 | 2 |
| `ttbr0_el1` | 3 | 0 | 2 | 0 | 0 |
| `ttbr1_el1` | 3 | 0 | 2 | 0 | 1 |
| `vbar_el1` | 3 | 0 | 12 | 0 | 0 |

Verify each against `tools/sam-aarch64-format/sysregs.go` before committing. The Go file is authoritative.

The payload assembles standalone with `pyz80 --obj=build/sysreg_data.bin --org=0x8000 src/m3/sysreg_data.asm`. Final binary is < 4 KB (target: < 2 KB so room exists on page 13 for future data).

Pseudo-skeleton:

```asm
        org     &8000              ; section-C-shape origin

; -------- entry table (paged_call targets) --------
sysreg_lookup_entry:
        jp      sysreg_lookup
pstate_lookup_entry:
        jp      pstate_lookup
dc_lookup_entry:
        jp      dc_lookup
tlbi_lookup_entry:
        jp      tlbi_lookup

; -------- table data (moved verbatim from sysname.asm) --------
sysreg_table:
        ; existing 12 entries here in the same on-disk encoding
        ; +
        ; 8 new entries (hcr_el2, mair_el1, ...) per the table above
sysreg_table_end:

pstate_table:
        ; ... (unchanged)
dc_table:
        ; ...
tlbi_table:
        ; ...

; -------- lookup routines --------
sysreg_lookup:
        ; in:  HL → operand name (NUL-terminated)
        ; out: A=1 on match, BCDE=packed; A=0 on miss
        ; clobbers HL, DE
        ld      bc, sysreg_table
        ; ... walking loop ...
        ret

pstate_lookup:
        ; ...
        ret

dc_lookup:
        ; ...
        ret

tlbi_lookup:
        ; ...
        ret
```

The exact lookup routine bodies should be lifted verbatim from `sysname.asm` (where they live today), so this is a pure relocation — no semantics change. Watch out for any references to other section-C symbols (e.g. `OPVAL_ARRAY` at `&C100`) — section D under `HMPR=13` is page 14, NOT the caller's section D, so any cross-section read inside a lookup body must be avoided. The lookup is leaf-only (it walks its own table and returns).

- [ ] **Step 4: Write `src/m3/test_sysreg_paged.asm` — boot self-test (BUILD_TESTS only)**

Boot test that does:
1. `paged_call` into `sysreg_lookup_entry` with HL pointing at a known operand name in a BUILD_TESTS scratch buffer (e.g. `"hcr_el2\0"`).
2. Assert A=1, BCDE = (3, 4, 1, 1, 0) packed in whatever exact form the lookup returns.
3. Repeat for `"vbar_el1\0"` (the last of the new entries, to catch table-tail off-by-ones).
4. Repeat for one of the existing entries (`"sctlr_el1\0"`) to catch relocation regressions.
5. Repeat for an unknown name (`"bogus_xx\0"`) — expect A=0.
6. Round-trip HMPR: assert that `IN (HMPR)` after `paged_call` returns equals the value before (the `paged_call` trailer's contract).

Each assertion fails via `jp fail` (the M3 fail path; lights the printer-channel "FAIL\n" banner).

- [ ] **Step 5: Wire the boot self-test into `assembler.asm`**

Edit `src/m3/assembler.asm` near the existing `call run_paged_call_self_tests` line (~line 236). Add:

```asm
                call    run_sysreg_paged_self_tests
```

And add the include line in the BUILD_TESTS-only block alongside `test_paged_call.asm`:

```asm
                if      defined(BUILD_TESTS)
                include "test_sysreg_paged.asm"
                endif
```

- [ ] **Step 6: Modify `sysname.asm` — remove the inline tables; route lookups via paged_call**

The four `*_lookup` routines in `sysname.asm` currently inline-walk their tables. Rewrite each to:

```asm
; sysreg_lookup — paged thunk
sysreg_lookup:
        ; in: HL → operand name
        ; out: A=1 + BCDE=packed on hit; A=0 on miss
        call    paged_call
        defw    SYSREG_LOOKUP_ENTRY   ; = &8000 — sysreg_lookup_entry on page 13
        defb    SYSREG_DATA_PAGE      ; = 13 — HMPR low5
        ret
```

Same shape for `pstate_lookup`, `dc_lookup`, `tlbi_lookup`, each pointing at its entry-table offset (`&8003`, `&8006`, `&8009`).

Delete the now-unused table data (`sysreg_table:` through the four `*_table_end` markers) from `sysname.asm`. Net section-C savings: ~600 B (the tables are the bulk of the file).

- [ ] **Step 7: Add constants to `src/m3/trampoline.asm`**

Near the existing `PAGED_CALL_TEST_PAGE` constant:

```asm
SYSREG_DATA_PAGE:       equ     13
SYSREG_DATA_DST:        equ     &8000     ; section-C-shape origin of page 13 payload

; paged_call inline-byte addresses (entry table on page 13 starts at &8000)
SYSREG_LOOKUP_ENTRY:    equ     &8000     ; jp sysreg_lookup
PSTATE_LOOKUP_ENTRY:    equ     &8003     ; jp pstate_lookup
DC_LOOKUP_ENTRY:        equ     &8006     ; jp dc_lookup
TLBI_LOOKUP_ENTRY:      equ     &8009     ; jp tlbi_lookup
```

- [ ] **Step 8: Add `load_page13_payload` in `loader.asm`**

Pattern parallels `load_page14_payload` (PR #55). Boot-time, before the BUILD_TESTS self-tests run (because the self-test calls into page 13):

```asm
load_page13_payload:
        ; Trampoline-HLOAD the sysreg_data binary into physical page 13.
        ; HMPR_SAVE / SP_SAVE on entry, restored on exit.
        ; Filename = "sd13" (matches build-m3-disk's -sysreg-data deposit name).
        ld      a, SYSREG_DATA_PAGE
        ld      hl, sysreg_data_fname
        call    trampoline_hload         ; existing helper from PR #55
        ret

sysreg_data_fname:
        defb    "sd13", 0
```

Wire the call into the boot sequence in `assembler.asm`, AFTER `enctab_trampoline_setup` (because `enctab_trampoline_setup` LDIRs `paged_call` into section B; `load_page13_payload` then uses HMPR — orthogonal to LMPR — but ordering is clearer if all the boot loads happen together).

- [ ] **Step 9: Extend `tools/build-m3-disk/main.go` with `-sysreg-data <path>`**

Add a CLI flag that takes the path to the sysreg-data binary and deposits it on the disk image as a CODE file named `sd13`. Mirror the `-paged-call <path>` flag added in PR #55 (which deposits `p14`). Names: `sysregData string` flag → CODE file slot. Reuse the existing `addCodeFile` helper.

Verify by running:
```bash
make build/sam-test-disk.mgt   # or whichever target builds the disk
samdos2 ls build/sam-test-disk.mgt | grep sd13
# Expect: sd13 listed as a CODE file.
```

- [ ] **Step 10: Add Makefile glue to build the page-13 payload**

In `Makefile`, parallel to the M5/M6 `enctab.enc` target and PR #55's `paged_call_test_payload`:

```makefile
build/sysreg_data.bin: src/m3/sysreg_data.asm
	pyz80 --obj=$@ --org=0x8000 $<

build/sam-test-disk.mgt: ... build/sysreg_data.bin ... 
	# add to the build-m3-disk invocation:
	./tools/build-m3-disk/build-m3-disk \
	    ... existing flags ... \
	    -sysreg-data build/sysreg_data.bin \
	    -o $@
```

And include `build/sysreg_data.bin` in the equivalent `m3-asm-prod` disk target.

- [ ] **Step 11: Measure budget**

```bash
make clean
make m3-asm m3-asm-prod
ls -l build/m3-asm.bin build/m3-asm-prod.bin
```

Expected:
- `m3-asm-prod.bin` should DECREASE in size from 12056 B (post-#55) by ~500-700 B (the tables moved off-axis), net of the new `paged_call` thunks. Target: comfortably under `&B000` headroom (e.g. `&AC00..&AE00` range).
- `m3-asm.bin` (test variant): ditto, plus ~50-80 B for the new `test_sysreg_paged.asm` boot self-test. Should stay well under `&C0C0`.

If either budget overruns, STOP and report — the test-variant fragility memory `feedback_test_variant_fragility.md` is explicit: don't push past the soft boundary.

- [ ] **Step 12: Run the full local CI sweep**

```bash
make ci-m3 ci-m4 ci-m5 ci-m6
make ci-m3-prod ci-m4-prod ci-m5-prod ci-m6-prod
./tools/run-m6-roundtrip.sh tests/m6/sources/inst_mrs_msr_missing.s
```

Expected:
- All M3-M6 fixtures still PASS (the table relocation must not break existing mrs/msr/dc/tlbi tests).
- `inst_mrs_msr_missing.s` round-trip PASSES — the 8 missing sysregs now encode correctly.
- Boot self-tests pass (`paged_call` + `sysreg_paged`).

- [ ] **Step 13: Verify FAIL00 is closed via the release-stripped flatten**

```bash
make release-stripped-tbn
./tools/run-m6-release-stripped.sh   # if the script exists; otherwise drive manually:
# build release-stripped.tbn → load on SAM via paged-IN → assemble → HSAVE → byte-compare against
# the Mac-side authoritative release.bin.
```

If `tools/run-m6-release-stripped.sh` doesn't exist yet, that's the next PR's deliverable (PR-4). For this PR, the check is: the SAM-side assembler no longer aborts at FAIL00. It may abort at FAIL40 (next surfacing) — that's PR-3's territory. Document where the abort moves to in the PR body.

- [ ] **Step 14: Commit and open PR**

```bash
g add src/m3/sysreg_data.asm src/m3/test_sysreg_paged.asm src/m3/sysname.asm \
      src/m3/trampoline.asm src/m3/loader.asm src/m3/assembler.asm \
      tools/build-m3-disk/main.go Makefile \
      tests/m6/sources/inst_mrs_msr_missing.s
g commit -m "$(cat <<'EOF'
m6: sysreg_table off-axis on page 13 + 8 missing sysregs (closes FAIL00)

First real consumer of the paged_call primitive (PR #55).  Moves the
four sysreg tables (sysreg, pstate, dc, tlbi) off-axis to physical
page 13 alongside their lookup routines.  sysname.asm's four
*_lookup entry points become 6-byte paged_call thunks.  The 8 sysregs
release-stripped.tbn needs land for free (no section-C budget cost):

  hcr_el2, mair_el1, scr_el3, spsr_el3,
  tcr_el1, ttbr0_el1, ttbr1_el1, vbar_el1

Budget impact:
  m3-asm-prod: 12056 → <measured>  (target: under &B000 by ≥ 100 B)
  m3-asm:      <measured>          (target: under &C0C0)

Verified:
  - All M3-M6 fixtures still byte-match GNU (regression check).
  - tests/m6/sources/inst_mrs_msr_missing.s now byte-matches.
  - Boot self-test asserts paged sysreg lookup round-trip.

Closes FAIL00 in release-stripped.tbn integration.  FAIL40+ next
(per PR-3 of the M6 closure plan).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
g push -u origin m6-closure-pr2-sysreg-off-axis
gh pr create --title "m6: sysreg_table off-axis on page 13 + 8 missing sysregs (closes FAIL00)" --body "$(cat <<'EOF'
## Summary

First real consumer of `paged_call` (PR #55).  Moves the four sysreg tables off-axis to physical page 13; adds the 8 sysregs release-stripped.tbn needs.  Closes FAIL00.

## What lands

- `src/m3/sysreg_data.asm` — page 13 payload (entry table + four lookup routines + four data tables, including 8 new sysreg entries).
- `src/m3/sysname.asm` — four lookups become paged_call thunks.
- `src/m3/test_sysreg_paged.asm` — boot self-test (BUILD_TESTS only).
- `tools/build-m3-disk/main.go` — new `-sysreg-data` flag (parallels `-paged-call`).
- `Makefile` — assembles `build/sysreg_data.bin`; passes via the new flag.
- `tests/m6/sources/inst_mrs_msr_missing.s` — round-trip fixture for the 8 new sysregs.

## Budget

| variant | pre (post-#55) | this PR | headroom |
|---|---|---|---|
| `m3-asm-prod` | 12056 B / `&AFE8` | <measured> | <measured> |
| `m3-asm` | <measured> | <measured> | <measured> |

## Test plan

- [ ] All M3-M6 fixtures still byte-match GNU.
- [ ] `inst_mrs_msr_missing.s` round-trip passes.
- [ ] Boot self-test `run_sysreg_paged_self_tests` passes.
- [ ] release-stripped.tbn integration no longer aborts at FAIL00 (may now abort at FAIL40 — that's PR-3 of the M6 closure plan).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

Wait for all 11 CI checks; fix failures autonomously (per global PR workflow). Once CI green, ask Pete if he wants to review before merge.

---

## PR-3 — m6: post-FAIL00 surfacing — FAIL40+ closure

**Why this PR exists**: Per the `m6_strand_a` memory entry's "Open thread" section, FAIL40 is the next failure site after FAIL00 — the `m6_strand_a_complete.md` entry notes "release-stripped FAIL00 untagged site" as an open thread. Once FAIL00 closes (PR-2 above), the SAM-side run advances and the next abort surfaces. The pattern of "fix the next FAIL, run again, fix the next FAIL" continues until the SAM-side run completes and produces a binary. **This PR is open-ended in shape** — we don't know yet what surfaces — but bounded in goal: close every FAIL in the path from `release-stripped.tbn` to a complete `release.bin` HSAVE.

**Files (predicted; subject to what FAIL40 actually surfaces):**
- Likely modify: one or more of `src/m3/encoder.asm`, `src/m3/sysname.asm`, `src/m3/operands.asm`, depending on which form / operand / directive the abort points at.
- Likely add: one or more `tests/m6/sources/inst_*.s` focused fixtures per failure cause (mirrors how M5 closed missing operand kinds).

### Sub-steps

- [ ] **Step 1: Reproduce the post-FAIL00 abort and identify the FAIL tag**

```bash
make release-stripped-tbn
# Drive the SAM-side flow against the stripped .tbn — exact command depends on
# the harness; if no script exists yet, write one as part of this step
# (it's also what PR-4 will need for its CI integration).
```

Capture the abort's printer-channel banner. The M3 fail path emits "FAIL\n" via PRINTL1 today (`docs/notes/m6-status.md`'s M5 PR 1 section); per the per-fail-site diagnostic TODO in ROADMAP.md (`docs/ROADMAP.md:82`), the current banner is generic. If banner discrimination is needed, that's a sub-task here.

Open question worth re-checking before this PR starts: does the M6 fail path already include FAIL40 vs FAIL00 distinguishability via printer banner / per-call-site string? If not, surfacing-by-banner is part of PR-3's scope. Otherwise, the FAIL tag is encoded by the *site* of the abort, found via SimCoupé's `-debugger` or the Go harness's last-200-PC trace (`tools/z80-test-harness-go/README.md`).

- [ ] **Step 2: Diagnose root cause**

For each FAIL tag surfacing:
- Bisect by SAM-side debug — Go harness or SimCoupé — to find the failing record in the .tbn.
- Cross-check the failing record against the GNU oracle (`text2bin` + `refenc` Mac-side flow).
- Identify the missing capability: missing mnemonic? missing operand shape? missing form table entry? missing directive?

Per the "research-first" rule (`memory/feedback_docs_first.md`): cite the spec/manual reference for the correct encoding before patching.

- [ ] **Step 3: Write the focused fixture (Layer 3) — TDD**

For each failure, add a small fixture under `tests/m6/sources/` that exercises ONLY the missing capability. Mirror M5's per-encoder fixture pattern. Run it through `./tools/run-m6-roundtrip.sh` and confirm it fails.

- [ ] **Step 4: Implement the fix**

Use the Go harness for fast inner-loop iteration (`tools/z80-test-harness-go/`). Per the dev-tool memory `feedback_go_harness_is_dev_tool_not_ci_gate`: agents are empowered to extend the harness mid-task to improve its diagnostic output. If the harness misbehaves, fall back to SimCoupé in Docker.

- [ ] **Step 5: Verify the fixture passes; re-run release-stripped integration**

```bash
./tools/run-m6-roundtrip.sh tests/m6/sources/<new_fixture>.s
make ci-m3 ci-m4 ci-m5 ci-m6 ci-m3-prod ci-m4-prod ci-m5-prod ci-m6-prod
make release-stripped-tbn  # then drive the SAM-side flow again
```

If the next FAIL surfaces, loop to step 2. If the SAM-side flow completes — that's where PR-4 picks up.

- [ ] **Step 6: Commit each fix as a separate logical commit on the same branch**

Per Pete's convention `memory/feedback_merge_commits.md`: build up the branch locally with the iteration story preserved as separate commits, then merge via `gh pr merge --merge` (PR's commit graph keeps the FAIL40, FAIL80, FAIL120 progress visible to future readers).

Commit messages:
```
m6: <cause> — closes FAIL40 in release-stripped integration

<one-paragraph explanation of root cause + fix>

<budget impact line if any>

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
```

- [ ] **Step 7: Open the PR**

Once the release-stripped run completes end-to-end on the SAM and produces a HSAVE binary, open the PR:

```bash
gh pr create --title "m6: post-FAIL00 surfacing — FAILxx closure (release-stripped integration)" --body "$(cat <<'EOF'
## Summary

After PR-2 closed FAIL00, the SAM-side release-stripped run surfaced a sequence of further FAILs: FAIL40, FAIL80, FAIL120 (etc).  Each commit on this PR closes one site.  After this PR, the SAM-side flow completes end-to-end against `release-stripped.tbn` — the precondition for PR-4's byte-match verification.

## FAILs closed

- FAIL40: <root cause + fix description>
- FAIL80: <ditto>
- (etc)

## Test plan

- [ ] All M3-M6 fixtures still byte-match GNU.
- [ ] Each new focused fixture passes round-trip.
- [ ] release-stripped.tbn flows end-to-end on the SAM without aborting.
- [ ] The produced HSAVE binary differs from the Mac-side `release.bin` (byte-match comes in PR-4) — but it EXISTS.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

If the FAIL sites prove numerous (more than 3-4), consider splitting into PR-3a, PR-3b, etc. — but only if each sub-PR's fix is genuinely independent. Sequential dependencies suggest keeping the branch monotonic.

---

## PR-4 — m6: spectrum4 release.bin byte-match on SAM (the milestone headline)

**Why this PR exists**: PR-3 leaves us with a SAM-side HSAVE'd binary that completes without aborting. PR-4 verifies it byte-matches the Mac-side oracle, then captures the achievement in `docs/notes/m6-status.md` + memory.

**Files:**
- Create: `tools/run-m6-release-stripped.sh` — driver script (assemble release-stripped.tbn on the SAM via SimCoupé, HSAVE, extract the resulting CODE file, byte-compare to `build/release.bin`).
- Modify: `docs/notes/m6-status.md` — flip the "spectrum4 release.bin byte-match on SAM" row to ✅, add the achievement narrative.
- Create (memory): `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/m6_release_bytematch_on_sam.md` — the achievement memory.
- Modify (memory): `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/MEMORY.md` — link the new achievement under "Current state".

### Sub-steps

- [ ] **Step 1: Write `tools/run-m6-release-stripped.sh`**

Mirrors `tools/run-m6-roundtrip.sh` but with these adjustments:
- Input is `build/release-stripped.tbn` (not a `.s` source).
- The SimCoupé run wraps `make m3-asm-prod` (because production variant — the test variant is fragile near `&C100`).
- Expected output is a 21 752 B `release.bin` (the figure from `memory/spectrum4_release_bytematch_achieved.md`).
- Byte-compare against `build/release.bin` (the Mac-side authoritative; built by GNU `as + ld + objcopy`).

Skeleton:
```bash
#!/bin/bash
set -euo pipefail
make release-stripped-tbn
make build/release.bin           # GNU oracle
make m3-asm-prod-disk             # SAM-side disk with release-stripped.tbn loaded
tools/run-simcoupe.sh build/m3-asm-prod-disk.mgt -timeout 120
# Extract the HSAVE'd CODE file from the SAM disk image:
samdos2 extract build/m3-asm-prod-disk.mgt out.bin build/sam-release.bin
cmp build/sam-release.bin build/release.bin && echo "OK: SAM byte-matches GNU" || \
    { echo "FAIL: SAM-side bytes differ from GNU"; exit 1; }
```

- [ ] **Step 2: Run the byte-match**

```bash
./tools/run-m6-release-stripped.sh
```

Expected: `OK: SAM byte-matches GNU`. If diff, drop back to PR-3 (residual missing capability), fix, return.

- [ ] **Step 3: Capture binary diff diagnostics if it doesn't match**

If `cmp` reports a first-differing byte at offset N, slice both binaries around N:

```bash
hexdump -C build/sam-release.bin | grep -A1 -B1 "$(printf '%05x' $(($N & ~0xf)))"
hexdump -C build/release.bin     | grep -A1 -B1 "$(printf '%05x' $(($N & ~0xf)))"
```

…and map back to the .s line that produced bytes at PC=N. If non-trivial, this becomes a PR-3 follow-up.

- [ ] **Step 4: Update `docs/notes/m6-status.md`**

Flip the "spectrum4 release.bin byte-match on SAM" row in the M6 scope table from "📋 ultimate goal" to "✅ done — PR #<this PR number>". Add a new section near the bottom:

```markdown
## SAM byte-matches GNU on release.bin (M6 headline closed)

As of PR #<n>, `release-stripped.tbn` flows through the SAM-side toolchain and produces a 21 752 B `release.bin` byte-identical to the GNU `as + ld + objcopy` reference.

Verification script: `tools/run-m6-release-stripped.sh`.

Pipeline:
1. `release.s` → `release-stripped.tbn` (Mac-side `text2bin -strip-comments`, 88 644 B).
2. `release-stripped.tbn` → SAM-side IN buffer (paged across pages 7..10 via M6 PR 2 reader).
3. SAM-side assembler runs (production variant; ENCTAB on page 4, OUT on pages 5-6, sysreg tables on page 13 via paged_call).
4. SAM-side HSAVE writes the result.
5. CI gates via `m6-release` GH Actions job (PR-5 of this plan).
```

- [ ] **Step 5: Write the achievement memory**

Create `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/m6_release_bytematch_on_sam.md`:

```markdown
# M6 release-bytematch on SAM — ACHIEVED

**Date achieved**: <date PR-4 lands>
**PR**: #<n>

The SAM-side assembler now produces `release.bin` byte-identical to GNU `aarch64-none-elf-as + ld + objcopy` on the full `release.s` source via the `release-stripped.tbn` intermediary (88 644 B input → 21 752 B output).

This is M6's stated headline ("spectrum4 release.bin byte-match on SAM").

Pipeline:
- Source: `~/git/spectrum4/release.s` (and its includes).
- Mac-side: `text2bin -strip-comments release.s > release-stripped.tbn`.
- SAM-side: assembler (production variant) reads the `.tbn` via paged IN, encodes via ENCTAB on page 4, OUTs to pages 5-6, looks up sysregs via `paged_call` to page 13.
- HSAVE writes the result.
- `tools/run-m6-release-stripped.sh` automates the round-trip + byte-compare.

CI gate: `m6-release` GH Actions job (PR-5).

Supersedes the prior milestone-incomplete state in `memory/m6_strand_a_complete.md` — keep m6_strand_a_complete as the historical record of how the mechanism was built up.
```

Then update `MEMORY.md`'s "Current state" section to link this new entry as the top item.

- [ ] **Step 6: Commit + open PR**

```bash
g add tools/run-m6-release-stripped.sh docs/notes/m6-status.md
g commit -m "$(cat <<'EOF'
m6: release-stripped byte-matches GNU on SAM (M6 headline closed)

The SAM-side assembler reads release-stripped.tbn (88 644 B),
assembles release.s end-to-end, and HSAVE's a 21 752 B release.bin
byte-identical to GNU as + ld + objcopy.

Driver script: tools/run-m6-release-stripped.sh.

Closes M6's stated headline ("spectrum4 release.bin byte-match on
SAM").  M6 flips to ✅ done once PR-5 of the M6 closure plan adds
the CI gate.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
# Memory file lands in a separate commit because it's outside the repo:
echo "Write ~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/m6_release_bytematch_on_sam.md per the plan."
g push -u origin m6-closure-pr4-release-bytematch
gh pr create --title "m6: release-stripped byte-matches GNU on SAM (M6 headline closed)" --body "$(cat <<'EOF'
## Summary

The SAM-side assembler now produces `release.bin` byte-identical to GNU on the full `release.s` (via the `release-stripped.tbn` intermediary).  M6's stated headline is closed.

## What lands

- `tools/run-m6-release-stripped.sh` — driver script.
- `docs/notes/m6-status.md` updated: headline row flips ✅; new "M6 headline closed" section captures the pipeline.
- Memory: `m6_release_bytematch_on_sam.md` (out-of-tree, lands in `~/.claude/projects/.../memory/`).

## Test plan

- [ ] `tools/run-m6-release-stripped.sh` prints "OK: SAM byte-matches GNU".
- [ ] All existing M3-M6 fixtures still byte-match.
- [ ] The script runs cleanly in Docker (matches what PR-5 will use for CI).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## PR-5 — ci: spectrum4 release-bytematch gate

**Why this PR exists**: M6 doesn't fully close until the byte-match is gated in CI. Without a CI job, a regression in (say) sysreg encoding silently breaks the headline. This PR adds an `m6-release` job to the GH Actions matrix that runs `tools/run-m6-release-stripped.sh` and fails if the byte-compare diverges. **This is the M6 closure trigger** — when this lands and goes green, M6 flips to ✅ on ROADMAP.md.

**Files:**
- Modify: `.github/workflows/<existing>.yml` — add an `m6-release` job mirroring `m6-prod`'s shape but driving the release-stripped script.
- Add (build/CI glue): a small build-time/CI **budget assertion** (e.g. a Makefile check or a tiny script invoked from CI) that fails the build on a code-size cliff — see the budget-assertion sub-step below.
- Modify: `docs/ROADMAP.md` — flip M6's state column from "⏳ in progress" to "✅ done", add the PR link.
- Modify: `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/MEMORY.md` — move m6_strand_a_complete from "⭐ Current state" to "Past milestones" (or wherever the auto-loaded file groups closed milestones).

### Sub-steps

- [ ] **Step 1: Identify the existing m6-prod job and clone its structure**

```bash
ls .github/workflows/
grep -n "m6-prod" .github/workflows/*.yml
```

Find the matrix entry for `m6-prod` (the SimCoupé-in-Docker run for the production-variant M6 fixtures).

- [ ] **Step 2: Add the `m6-release` matrix entry**

Mirror `m6-prod` but:
- Build target: `make release-stripped-tbn` + `make m3-asm-prod-disk` (the disk with release-stripped.tbn pre-loaded).
- Run target: `./tools/run-m6-release-stripped.sh`.
- Timeout: 180s (the script's longest path — full release.s assembly on the SAM under SimCoupé).

- [ ] **Step 2b: Add a build-time / CI budget assertion (the structural fix for the test-variant fragility class)**

PR-5 ALSO adds a build-time/CI **budget assertion** so a code-size overrun becomes a CI *failure with a number* rather than a silent boot-hang. Two checks:

- **Test variant (`BUILD_TESTS`):** fail the build if the test (`m3-asm`) variant's `code_end ≥ &C000` — the reader-self-test stack-collision cliff. Past that boundary the assembler's own scratch/stack growth collides with the loader spillover and the failure manifests as a deterministic boot-hang (rc=124) with no diagnostic, exactly the failure class that bit PR #43 and that the test-variant fragility memory (`memory/feedback_test_variant_fragility.md`) tracks.
- **Prod variant:** fail the build if `m3-asm-prod` exceeds its ceiling.

pyz80 emits symbol addresses, so the assertion can read `code_end` from the link map (or the binary size + org) and `exit 1` with a message like `code_end &C0xx ≥ &C000 cliff — N bytes over`. This **converts the silent boot-hang into a CI failure with a number** — the structural fix for the whole test-variant fragility class. This recommendation originates in the Go-harness fidelity investigation: `docs/notes/2026-05-29-go-harness-fidelity-investigation.md:14` (encroachment is a static memory-map fact, checkable with a link-map assertion at build time), `:87` (the build-time link-map assertion that asserts `code_end < &C000`), and `:183` (schedule the link-map assertion as a small inner-loop task — promoted here into the M6-closure gate because it directly protects the headline).

- [ ] **Step 3: Test the workflow change locally via `act` or a draft PR**

Since this is the M6 closure trigger, draft the PR first to confirm CI passes:

```bash
g push -u origin m6-closure-pr5-ci-release-gate
gh pr create --draft --title "ci: spectrum4 release-bytematch gate (m6-release job)" ...
```

Wait for all checks. If green, mark ready for review per the project's PR workflow.

- [ ] **Step 4: Update `docs/ROADMAP.md`**

Modify the M6 row:
- State column: "⏳ in progress" → "✅ done (PR #<n>)".
- Spec column: append `+ M6 closure plan docs/plans/2026-05-29-m6-closure-release-bytematch.md`.
- Add to "Achievements worth keeping visible" section:

```markdown
- **2026-05-29 (or whenever PR-5 lands): M6 complete** — spectrum4 release.bin byte-match on SAM. PRs #<n-3>..#<n>. release-stripped.tbn (88 644 B Mac-side → SAM-side encode → 21 752 B HSAVE) byte-identical to GNU `as + ld + objcopy`. `m6-release` CI job is the standing gate. See `docs/notes/m6-status.md`.
```

- [ ] **Step 5: Update `MEMORY.md`'s Current State section**

After M6 closes, `memory/m6_strand_a_complete.md` is no longer the top of "Current state". The new top entry should be the M7-kickoff entry (TBD), with m6_release_bytematch_on_sam.md as the immediate predecessor closed milestone. Adjust links accordingly.

- [ ] **Step 6: Commit and open PR**

```bash
g add .github/workflows/<file>.yml docs/ROADMAP.md
g commit -m "$(cat <<'EOF'
ci: spectrum4 release-bytematch gate — closes M6

Adds m6-release GH Actions job that runs
tools/run-m6-release-stripped.sh.  The job fails if the SAM-side
HSAVE'd release.bin diverges from the Mac-side authoritative byte
sequence.  This is the M6 closure trigger — once green, M6 is ✅
on ROADMAP.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
gh pr create --title "ci: spectrum4 release-bytematch gate — closes M6" --body "$(cat <<'EOF'
## Summary

Adds the standing CI gate for M6's stated headline: every PR that lands now verifies SAM-side `release.bin` matches GNU's bytes.  This PR is the M6 closure trigger.

## What lands

- New `m6-release` matrix entry in `.github/workflows/<file>.yml`.
- ROADMAP.md updated: M6 flips ✅; new "M6 complete" achievement entry.

## Test plan

- [ ] `m6-release` job passes on this PR's branch.
- [ ] All 11 existing checks still pass.
- [ ] After merge, the first subsequent PR sees `m6-release` as a required check.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

Once green and merged, **M6 is complete.**

---

## PR-6 — (parallel) m6: reader self-test re-enable / PR #42 SP-fix re-investigation via the Go harness

**Why this PR exists**: `src/m3/assembler.asm:295-308` has `run_reader_paged_self_tests` commented out, blocked on the PR #42 SP-fix mystery (`memory/m6_strand_a_complete.md` open thread). Three branches exist as starting points: `m6-reader-self-test-sp-fix`, `m6-trampoline-sp-switch`, `investigate-reader-paged-self-test`. Pete asked specifically: *"this time using our new go framework that will allow it to understand the crash symptom better, and it can be encouraged also to iterate on the go tooling itself if that helps it uncover what the cause is."*

**This is the first real adversarial use of the Go harness.** Pete pre-authorised the harness-modification scope: subagent owns both `src/m3/` and `tools/z80-test-harness-go/` for the duration of this PR. The harness can be extended (better trace, register-snapshot capture, watchpoints) as part of the work — that's the design intent per `memory/feedback_go_harness_is_dev_tool_not_ci_gate`.

This PR is **parallel-mergeable with PR-2 / PR-3** (different files, different concerns). Land it whenever the investigation completes; no ordering constraint with the FAIL00 → FAIL40+ stream.

**Files (predicted):**
- Modify: `src/m3/trampoline.asm` (probable SP-switch addition; see the three pre-existing branches for prior attempts).
- Modify: `src/m3/assembler.asm` (uncomment the `call run_reader_paged_self_tests`).
- Modify (likely): `tools/z80-test-harness-go/*.go` — add whatever instrumentation the investigation needs (PC trace, register snapshot at fail, watch on SP_SAVE slot, etc.).
- Possibly modify: `src/m3/reader.asm`, `src/m3/test_reader_paged.asm` — if the root cause is in the reader rather than the trampoline.

### Sub-steps

- [ ] **Step 1: Read the three pre-existing investigation branches as prior art**

```bash
g log --oneline m6-reader-self-test-sp-fix       | head -10
g log --oneline m6-trampoline-sp-switch          | head -10
g log --oneline investigate-reader-paged-self-test | head -10
g diff main m6-reader-self-test-sp-fix           --stat
g diff main m6-trampoline-sp-switch              --stat
g diff main investigate-reader-paged-self-test   --stat
```

Read what each branch tried. PR #42 hypothesised the SP=`&FFFE` move; that was reverted. The three branches above are subsequent attempts. Identify what was tried and what didn't conclude.

- [ ] **Step 2: Reproduce the failure deterministically via the Go harness**

```bash
# Uncomment the call run_reader_paged_self_tests line in src/m3/assembler.asm
# (don't commit yet — staging the harness's view of the broken state)
make m3-asm
cd tools/z80-test-harness-go
go test -v -run TestBoot -- # or whichever harness invocation drives the test variant
```

Capture the harness's output: last-N PCs, register state at fault, OUT bytes, whatever it provides. If output is sparse, **extend the harness** to give you what you need — that's the explicit dev-tool authority.

- [ ] **Step 3: Cross-check the harness's reproduction against SimCoupé under Docker**

```bash
make ci-m3   # this should now FAIL on the test-variant boot
```

If SimCoupé reports a different failure mode than the harness, **SimCoupé wins** (per the harness scope memory). Diagnose the harness fidelity gap before continuing — that's part of the harness's first-real-use shakedown.

- [ ] **Step 4: Form a hypothesis grounded in the harness's trace**

Per `memory/feedback_correctness_over_workarounds.md`: don't shotgun fixes. Each hypothesis must be specific (e.g. "the trampoline's RST 8 pushes a 2-byte return address at &C0F8/&C0F9, which under HMPR=7 maps to page 8 offset &00F8 — overwritten by HLOAD's spillover into page 8") and falsifiable.

Useful starting hypotheses (carry over from `docs/notes/2026-05-28-hload-16k-limit-investigation.md`):
- The 16 KB HLOAD ceiling is the same SP-vs-section-D-spillover seen by the IN PR's caveat (`m6-status.md`). The reader self-test's failure may be the same root cause manifesting at a different boundary.
- A code-layout-dependent JR offset that the rebase shifted into a misassembled boundary.
- An interaction between PR #35's multi-digit-local-label test scratch and the new reader's section-D scratch.

- [ ] **Step 5: Validate the hypothesis with the harness (then SimCoupé)**

Use the harness's fast iteration loop (~1 ms/run) to test the hypothesis. Apply the candidate fix (e.g. add the SP-switch around RST 8 — possibly already done in `m6-trampoline-sp-switch`); re-run; cross-validate in SimCoupé.

- [ ] **Step 6: Land the fix + re-enable the boot self-test**

When the fix is verified end-to-end:
- Uncomment `call run_reader_paged_self_tests` in `assembler.asm:308`.
- Confirm both the test-variant boot AND all M3-M6 fixtures still pass.
- Run the full Docker CI sweep:

```bash
docker run --rm -v "$(pwd):/work" -w /work \
    -e SDL_VIDEODRIVER=x11 -e SDL_AUDIODRIVER=dummy \
    -e AARCH64_AS=aarch64-linux-gnu-as -e AARCH64_LD=aarch64-linux-gnu-ld \
    -e AARCH64_OBJCOPY=aarch64-linux-gnu-objcopy \
    ghcr.io/petemoore/sam-aarch64-dev:latest \
    bash -c 'Xvfb :99 -screen 0 1280x1024x24 >/dev/null 2>&1 & sleep 1; export DISPLAY=:99; rm -rf build/; make ci-m3 ci-m4 ci-m5 ci-m6 ci-m3-prod ci-m4-prod ci-m5-prod ci-m6-prod'
```

- [ ] **Step 7: Commit harness improvements separately from the fix**

If the investigation drove harness improvements (better trace, watchpoint, snapshot dump), commit those as their own logical commit so future readers can understand which harness changes were dictated by which Z80 bug.

- [ ] **Step 8: Open the PR**

```bash
gh pr create --title "m6: re-enable reader paged self-test (PR #42 SP-fix re-investigation)" --body "$(cat <<'EOF'
## Summary

Re-enables `run_reader_paged_self_tests` (disabled in PR #43 / #45 after PR #42's SP-fix was reverted).  Root cause identified via the Go harness's <Nth iteration>:

<one-paragraph root-cause description>

Fix: <one-line>.

## Harness improvements

This PR is the harness's first real adversarial use.  Improvements landed:

- <e.g. last-200-PC trace shows source line, not just address>
- <e.g. watchpoint on SP_SAVE slot>
- <etc>

These improvements survive on `main` and benefit future investigations.

## Test plan

- [ ] Boot self-test `run_reader_paged_self_tests` passes on both test-variant and (where applicable) production-variant boots.
- [ ] All M3-M6 fixtures still byte-match.
- [ ] Harness `go test` passes.
- [ ] Docker CI matrix passes.

Closes the "reader self-test re-enable" open thread from `memory/m6_strand_a_complete.md`.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## M7 — sketch (open after M6 closes via PR-5)

Not part of this plan's deliverable, but the natural next milestone — captured here so it's not lost. Standalone plan doc to follow.

**M7 — "shared data structures + on-SAM disassembler + editor groundwork":**

- **PR-A — codegen sysreg + mnemonic tables from Go-side authority.** Architecture doc §6 PR-4. Single-source-of-truth: `tools/sam-aarch64-format/sysregs.go` (39 entries) generates `build/sysreg_data.bin` Mac-side. Same pattern applied to mnemonics, form table, intercept tables. Eliminates hand-sync drift between Mac-side and SAM-side encoders. Depends on M6 PR-2's page-13 binary build glue.
- **PR-B / PR-C / ... — on-SAM disassembler.** Branch `strand-b-1-disassembler` (5 commits) is the start; plan at `docs/plans/2026-05-28-go-aarch64-disassembler.md`. After M6 closes, resume per Pete's redirect.
- **PR-X — compact `.tbn` format** (`docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`). Future-proofing: needed if a future source exceeds the paged-IN ceiling (release-stripped at 88 KB is comfortably under, but the original 407 KB `release.s` is not).
- **PR-Y onwards — editor groundwork.** Per the Phase 2 vision in ROADMAP.md (instruction explanation panel, register simulator, sysreg docs, etc.). Each PR is small; the unifying north star is "the editor as thoughtful guide, not just a mechanical tool".

The harness (`tools/z80-test-harness-go/`) is expected to mature significantly through M7 — every M7 PR's investigation feeds back diagnostic improvements. By M7's close, the harness should be a credible counterpart to SimCoupé for inner-loop iteration on any Z80 change.

---

## Self-review (writing-plans skill discipline)

**Spec coverage check** — every open thread from `docs/notes/2026-05-28-eod-session-handoff.md` mapped to a plan task:

| Handoff thread | This plan's slot |
|---|---|
| Plan-PR 2: sysreg off-axis + 8 entries | PR-2 |
| Editorial fix: architecture doc §4 PR-2 stale refs | PR-1 (pre-step to PR-2) |
| PR #42 SP-fix re-investigation via Go harness | PR-6 (parallel) |
| FAIL40+ closure | PR-3 |
| Spectrum4 Z80 CI gate | PR-5 |
| Plan-PR 4: codegen sysreg from Go authority | M7 (sketched at the bottom) |
| Strand B: disassembler resume | M7 (sketched at the bottom) |
| First real use of Go harness | PR-6 (explicit) |

**Placeholder scan** — every step has either concrete commands or concrete file edits. The one explicit uncertainty (PR-3 — FAIL40+ closure scope) is structurally unavoidable: we don't yet know what FAIL40 will be. The plan handles this by sequencing the investigation as repeatable sub-steps within PR-3 rather than predicting the exact fixes.

**Type consistency** — `paged_call` ABI references (HL clobbered, BC/IX/IY preserved, A/F via `ex af, af'`) are consistent across PR-2 and PR-6. `SYSREG_DATA_PAGE = 13` and `PAGED_CALL_TEST_PAGE = 14` don't collide. Constants live in `trampoline.asm` per the §7 open-question 8 convention.

**M6 vs M7 boundary** — the plan justifies M6 closure (release-bytematch is the headline, the mechanism foundation is done, what remains is direct execution against the headline) and explicitly defers codegen + disassembler + compact `.tbn` to M7. This avoids the trap of either (a) opening M7 prematurely with M6 still flagged ⏳, or (b) hauling all of compact-`.tbn`/disassembler/codegen into M6 closure when none of those is needed to flip the headline.
