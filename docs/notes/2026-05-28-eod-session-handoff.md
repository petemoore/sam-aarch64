# End-of-day session handoff — 2026-05-28

**Supersedes** `2026-05-28-session-handoff.md` (which captured the prior-session handoff at the start of this session). Read this for the post-session state.

## Where things stand on `main`

### Landed this session (in order)

| PR | What | Effect |
|----|------|--------|
| #51 | Session handoff doc + spike briefs + ROADMAP music link | Pre-staged context for the bake-off. |
| #52 | plan-PR 3 — `test_mem.asm` ported off-axis to physical page 13 | Test variant `&C1AB → &BED9` (-722 B). Unblocked PR #50 salvage. |
| #53 | Bake-off evaluation doc | Decision: Go harness = agent-side dev tool; SimCoupé = sole CI gate. Workflow + ownership baked into the doc itself. |
| #54 | `tools/z80-test-harness-go/` lands on main + CLAUDE.md pointer | Future agents auto-discover the dev tool via project CLAUDE.md. |
| #55 | plan-PR 1 salvage — `paged_call` primitive + page-14 plumbing + boot self-test | Section-B `paged_call` body installed at boot. AF preserved via `ex af, af'`. §4 split-bracket primitives **dropped** entirely. Architecture doc patched. |

### Binary budget after the dust settled

| Variant | End | Headroom | Notes |
|---|---|---|---|
| prod | `&AFE8` | 23 B under `&B000` | **Tight**. `&B000-&BFFF` is reserved code-headroom (~4 KB available if deliberately spilled). |
| test | `&BF6C` | 403 B under `&C100` | Comfortable. |

### What plan-PR 1 actually delivered (verified against `src/m3/`)

- `paged_call` body in `src/m3/paged_bodies.asm`, LDIR'd into section B at boot by extended `enctab_trampoline_setup` (`trampoline.asm`).
- Constants `PAGED_CALL_TEST_PAGE=14`, `PAGED_CALL_DST=&7E40`, `PAGED_CALL_HMPR_SAVE=&7ED0`, `PAGED_CALL_SP_SAVE=&7ED1` in `trampoline.asm`.
- 3-byte payload (`ld a, &42; ret`) at `src/m3/paged_call_test_payload.asm`, HLOADed into physical page 14 at boot.
- Boot self-test `run_paged_call_self_tests` (BUILD_TESTS only, `src/m3/test_paged_call.asm`) asserts A=&42 + HMPR bit-identity round-trip. **Runs in CI on all four m{3,4,5,6} test-variant jobs; green.**
- ABI fixes vs the original PR #50: SP saved BEFORE the `pop hl` (was wrong in the design doc's §3.3 pseudocode); `ex af, af'` in trailer preserves caller's AF so paged targets can return a byte in A.

## Open threads (verified — these are the things to not forget)

### 1. Plan-PR 2 — sysreg_table off-axis + 8 missing entries (BLOCKED FAIL00; first real consumer of paged_call)

`src/m3/sysname.asm` is **still missing** the 8 sysregs spectrum4 release.tbn needs (`hcr_el2, mair_el1, scr_el3, spsr_el3, tcr_el1, ttbr0_el1, ttbr1_el1, vbar_el1`). Verified: `grep -c` for these names in `sysname.asm` returns 0.

The release-stripped flatten (`make release-stripped-tbn`, builds `build/release-stripped.tbn` ~88 KB) currently fails at FAIL00 because of this gap.

Architecture-doc PR-2 spec is at `docs/notes/2026-05-28-paged-call-architecture.md` §"PR 2 — port sysreg_table to paged data; add 8 missing entries". **The spec text still references the dropped split-bracket primitives (`paged_data_map_hmpr` / `unmap`)** — that needs editorial correction before plan-PR 2 dispatch. The mechanism the salvage actually shipped is `paged_call` to a target routine on the data page that does its work and returns; the PR-2 spec needs to be re-stated in those terms.

### 2. Plan-PR 4 — codegen sysreg + mnemonic tables from Go authoritative sources (THE shared-data-structures piece)

Authoritative list at `tools/sam-aarch64-format/sysregs.go` (39 entries per the memory-layout brainstorm). Hand-sync to `sysname.asm` drifts. Plan-PR 4 generates `sysreg_data.asm` (or the page-13 binary) Mac-side from `sysregs.go`. Hasn't started; design at architecture doc §"PR 4". Depends on plan-PR 2's page-data loading infrastructure.

### 3. Reader self-test re-enable (was the original budget concern)

`src/m3/assembler.asm:52-65` has `call run_reader_paged_self_tests` **still commented out**. The comment cites PR #42 → #43 → #45 history: PR #42 attempted to fix the failure by moving SP from `&C100` to `&FFFE`; the fix's verification was on a pre-#41 tree, and when #41's table relocations landed the SP change broke test variants. **Root cause was never found.** The fix branches still exist: `m6-reader-self-test-sp-fix`, `m6-trampoline-sp-switch`, `investigate-reader-paged-self-test`.

Budget exists to re-enable, but the interaction needs root-causing first. **This is the perfect first real use of the Go harness** — fast iteration on a Z80-side crash whose only signal today is a SimCoupé deterministic boot-hang.

### 4. Spectrum4 release-bytematch on SAM (the M6 headline)

Mac-side byte-match achieved per `memory/spectrum4_release_bytematch_achieved.md` (2026-05-26). SAM-side is **not** done: FAIL00 blocks it (plan-PR 2 closes); FAIL40 likely surfaces next (per the FAIL00 investigation's throwaway-patch verification); spectrum4 Z80 CI gate blocked on FAIL40+ closure.

### 5. Go harness — never used in anger

Zero commits to `tools/z80-test-harness-go/` since landing. The happy path (`inst_nop_ret.s`) was verified by Spike A but **no agent has hit a real failure with it yet**. We don't know empirically whether the failure-mode debug output is useful. First real use is the right opportunity to evolve the tool.

### 6. Strand B (disassembler) — explicitly parked

Plan at `docs/plans/2026-05-28-go-aarch64-disassembler.md`. Branch `strand-b-1-disassembler` exists with 5 commits. Pete redirected to the release-bytematch milestone before this session; resume after release-bytematch closes.

### 7. Editorial: architecture doc §4

§4 still has stale references to `paged_data_map_hmpr` / `paged_data_unmap_hmpr` in the PR-2 scope section. Salvage updated §3.3 and §4's intro but not the PR-2 body. Small editorial fix needed before plan-PR 2 dispatch.

## What the next session should do, in order

1. **Read this doc + `docs/notes/2026-05-28-paged-call-architecture.md` §6.**
2. **Run the planning agent** (already dispatched at session end; see ROADMAP / new milestone plan when it lands) — produces the structured PR sequence for closing M6 (or opening M7, if the planner decides the release-bytematch milestone is a boundary).
3. **Execute the plan in order.** The PR #42 SP-fix re-investigation is the natural first task that exercises the new Go harness on a real failure.

## Pointers

- **Architecture design (the load-bearing doc for the next phase)**: `docs/notes/2026-05-28-paged-call-architecture.md` (§6 PR sequence; §4 needs editorial fix re: dropped split-bracket primitives).
- **Memory-layout cost model**: `docs/notes/2026-05-28-memory-layout-brainstorm.md`.
- **Prior handoff (for context only)**: `docs/notes/2026-05-28-session-handoff.md`.
- **Test-variant cliff history**: `docs/notes/2026-05-28-test-variant-ci-regression.md` + `docs/notes/2026-05-28-z80-bounds-check-audit.md`.
- **Reader-self-test investigation (parked)**: `docs/notes/2026-05-28-reader-paged-self-test-investigation.md`.
- **Bake-off output**: `docs/notes/2026-05-28-test-harness-bakeoff-evaluation.md`.
- **Memory index**: `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/MEMORY.md` (auto-loaded).
- **Project-local Claude Code instructions**: `~/git/sam-aarch64/CLAUDE.md` (now includes the "Development inner loop for Z80 changes" pointer to the Go harness).
