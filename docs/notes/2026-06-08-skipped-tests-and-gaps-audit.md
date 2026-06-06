# Skipped-tests & worked-around-gaps audit (item i38)

**Date:** 2026-06-08 · **Item:** i38 · **Type:** audit only (no fixes)

Pete flagged a pattern when i9 (PR #114) pinned two decoder disagreements as a
*skipped test* rather than implementing them. This audit sweeps the whole repo
and this session's merged PRs (#106–#116) for the same shape — skips, relaxed
assertions, ratchet floors, allowed-failure rates, and "documented gap" markers
— and classifies each as **LEGITIMATE** (principled: prerequisite-missing,
data-only fixture, dev-harness-not-a-gate, correctly-declined encoding) or
**GAP** (a real disagreement/bug/unimplemented capability skipped to stay green).

## Method

- `grep` for `t.Skip*`, `SkipNow`, `testing.Short`, build tags, env gates across
  all `*.go`.
- Inspected every round-trip / release-gate shell script in `tools/*.sh` for
  `inst_allowed`, allowed-failure thresholds, ratchet floors, `|| true` on
  assertions, by-name fixture exclusions.
- Grepped `src/`, `tools/aarch64dec/`, `tools/aarch64enc/`, `tools/refenc/` for
  `TODO`/`FIXME`/`unimplemented`/`declined`/`deferred`/`not yet` and triaged
  capability gaps vs control-flow narration.
- Skimmed `gh pr diff` for PRs #106–#116.

## Findings table

| # | Location (file:line) | What it skips / relaxes | Classification | Recommendation |
|---|---|---|---|---|
| 1 | `tools/z80-test-harness-go/synthetic_parity_test.go:326-329` (`TestSyntheticParity_KnownDisagreements`, env gate `PARITY_DISAGREEMENTS=1`) | Skips two genuine Z80↔Go **decoder** disagreements rather than fixing them: (A) `ccmp`/`ccmn` — Go+binutils decode `ccmp`, `src/disasm.asm` has **no handler** → emits `.inst`; (B) base `csinv`/`csneg` (Rn≠Rm) — Z80 decodes them, Go's `DecodeAt` declines → `.inst`. | **GAP** (×2 — this is the i9 known one) | Register both. (A) is a mechanical Z80 decoder port (Go is authority). (B) is a Go-authority decision (extend Go to match binutils, then Z80 already agrees) per the "align with binutils" memory. |
| 2 | `tools/z80-test-harness-go/synthetic_parity_test.go:28-29, 106` (header + body comments) | Comment states `sdiv` is "deliberately excluded (known-missing across encoder + decoder; item i35)". | **LEGITIMATE** but **STALE** | sdiv was fully landed by i35/PR #115 (encoder `manual_forms.go:554`, decoder `disasm.asm:3819`, mnemonics table). The gap is CLOSED; only the comment is stale. Recommend a one-line comment fix (sdiv is now testable and could be promoted into a certified sweep). Not a capability gap. |
| 3 | `tools/z80-test-harness-go/sweep_test.go:31` (`SWEEP=1` gate) | Whole-corpus divergence sweep skipped unless `SWEEP=1`. | **LEGITIMATE** | Throwaway investigation harness, never a CI gate (SimCoupé is the only gate). No assertion is being relaxed — it's a diagnostic table generator. Leave as-is. |
| 4 | `tools/z80-test-harness-go/synthetic_parity_test.go:60,273` | Skips if no GNU `aarch64-*-as` on PATH. | **LEGITIMATE** | Prerequisite-missing (ground-truth encoder). Standard. |
| 5 | `tools/z80-test-harness-go/synthetic_parity_test.go:124,216,220,224,232` | Skips if `build/disasm.bin`/`enctab.enc`/`assembler-prod.bin`/`sysreg_data.bin`/`text2bin` not built. | **LEGITIMATE** | Prerequisite-missing build artifacts. Standard. |
| 6 | `tools/z80-test-harness-go/boot_self_test_test.go:71`, `boot_self_test_fail_probe_test.go:36`, `release_paged_test.go:53`, `test_variant_test.go:32,45` | Skips if paged-boot build artifacts absent. | **LEGITIMATE** | Prerequisite-missing build artifacts with explicit `make` recipe in the skip message. Standard. |
| 7 | `tools/enctab-gen/regen_survives_test.go:28` | Skips if `build/enctab-gen` not built. | **LEGITIMATE** | Prerequisite-missing binary; the same path the Makefile builds. Standard. |
| 8 | `tools/enctab-gen/mra/parser_test.go:75` | Skips if vendored real NOP XML not available. | **LEGITIMATE** | Optional vendored fixture; synthetic-XML tests cover the schema regardless. Standard. |
| 9 | `tools/run-disasm-roundtrip.sh:60-62` | Skips `inst_ldr_litpool` and `dir_hword` from the per-fixture instruction round-trip. | **LEGITIMATE** | Pure-data fixtures (literal pool / `.hword` tables) that are not instruction tests; non-instruction by design. Byte-identity is still covered for instruction fixtures. |
| 10 | `tools/run-disasm-roundtrip.sh:107-113` (`inst_allowed`) | Relaxes the `.inst`-free assertion for `inst_ldr_literal` and `inst_logical_noncanon` — but **the byte-compare still runs**. | **LEGITIMATE** | `inst_logical_noncanon` embeds a non-canonical logical-immediate (`0x32200013`) that `decodeBitMasks` *correctly declines* (it must round-trip as `.inst` with exact bytes preserved). `inst_ldr_literal` embeds a literal-pool data word. Only the cosmetic `.inst`-free check is relaxed; correctness (byte-identity) is unrelaxed. This is the right shape, not gap-hiding. |
| 11 | `tools/z80-test-harness-go/disasm_oracle_test.go:338-347` | (No-op check) — asserts plain `matches != nWords` → 100%. | **LEGITIMATE (confirmed NO ratchet)** | The historical `matchFloor` ratchet is **gone**. Oracle asserts a plain 100% over all 5438 release.img words, with an explicit comment that a feature branch stays red until 100% (no allowed-failure rate). Exactly per project rule §5. |
| 12 | `tools/run-m6-release-gate.sh` + `run-m{3,4,5,6}-roundtrip.sh` `|| true` instances | `|| true` appears on `tr -d` status-log reads, `sed`/`cmp -l` diagnostic output, and `grep -v "cannot find entry symbol _start"` (linker-warning suppression). | **LEGITIMATE** | All `|| true` are on diagnostic/cleanup/output commands, never on the pass/fail assertion itself. The m6-release gate is a strict unrelaxed 3-way byte-match (GNU == Go == Z80); round-trip scripts fail loudly on mismatch. No swallowed failures. |
| 13 | `src/sysname.asm` `jp fail` paths (`sysname_lookup_{pstate,dc,tlbi}_miss`) | Hard-aborts on an unknown PSTATE/DC/TLBI name. | **LEGITIMATE** (was a gap, now fixed) | i9/PR #114 extended the on-SAM tables to full Go parity, so every name Go encodes the SAM now encodes; the remaining `jp fail` only fires for names *Go itself* rejects — the faithful mirror. `sysregs_z80sync_test.go::checkComplete` now guards the reverse direction so the gap can't silently reopen. |
| 14 | `tools/aarch64dec/asm.go:19` | "pair detection for adrp/add(:lo12:) is deferred." | **LEGITIMATE (cosmetic)** | A rendering nicety (collapsing an adrp+add pair into a single `:lo12:` line), not a decode-correctness gap — both instructions still decode and round-trip byte-identically. Not a capability a future input "fails" on. |

## "Deferred" markers in PRs #106–#116 that are NOT gaps

For completeness — these matched the grep but are roadmap/doc bookkeeping, not
test relaxations:

- PR #106/#108: "deferred shared-include de-dup" of the sysreg tables — a
  refactor idea, explicitly *not blocked by the sync guard*; correctness intact.
- PR #110/#111: `i2`, `i14`, `i15`, `i16`, `i22`, `i23` etc. listed as
  `🧭 deferred` in the ROADMAP / item registry — these are the project's *normal*
  item-tracking mechanism (the opposite of papering over: they're registered).
- PR #110: ISA-footprint research table mentioning `ccmp` among "integer-core
  completion gaps" — narrative scoping, the actual gap is captured in finding #1.

## Real gaps to register as items (GAP-classified only)

Only **finding #1** is a genuine worked-around capability gap — and it is two
distinct gaps pinned by one skipped test (`TestSyntheticParity_KnownDisagreements`,
`synthetic_parity_test.go:325`):

1. **`ccmp`/`ccmn` Z80 decoder missing** — `src/disasm.asm` has no
   conditional-compare handler; it emits `.inst` where Go + binutils decode
   `ccmp`. Mechanical Z80 decoder port (Go is the authority — read the
   `aarch64dec` form walk for `ccmp` and mirror it). Pinned at
   `synthetic_parity_test.go:342-343`.
   *Suggested item:* "Port the `ccmp`/`ccmn` conditional-compare decoder to
   `src/disasm.asm` (Go-authority port; remove from KnownDisagreements, add to a
   certified sweep)."

2. **base `csinv`/`csneg` (Rn≠Rm): Z80 decoder more permissive than Go** —
   `src/disasm.asm:~3231` decodes the base forms; Go's `DecodeAt` declines (only
   `csel`/`csinc` have manual forms; `decodeCondSelAlias` fires only for the
   `Rn==Rm` alias shapes), so Go emits `.inst`. binutils renders `csinv`/`csneg`,
   so the likely resolution per "align with binutils" is to *add the base forms
   to the Go decoder* (then Z80 already agrees). This is a Go-authority decision,
   not a pure mechanical port. Pinned at `synthetic_parity_test.go:345-346`.
   *Suggested item:* "Resolve base `csinv`/`csneg` Z80↔Go disagreement — add
   `csinv`/`csneg` base forms to the Go decoder to match binutils (authority
   change), then promote to a certified parity sweep."

### Non-gap follow-up (housekeeping, optional)

- **Finding #2 (stale comment):** `synthetic_parity_test.go:28-29,106` still says
  `sdiv` is "known-missing … item i35", but i35/PR #115 landed sdiv end-to-end.
  A one-line comment correction (and optionally moving `sdiv` into a certified
  sweep) closes the staleness. Not a capability gap — flag for tidy-up, not as an
  `iN`.

## Bottom line

- **Skips/relaxations found:** 14 distinct sites (table above).
- **LEGITIMATE:** 13 (11 prerequisite/dev-harness/data-fixture skips + the
  unrelaxed oracle 100% + the principled `inst_allowed` byte-compare-still-runs
  relaxations + the now-guarded sysname fail-hard). One of these (#2) carries a
  **stale comment** worth a tidy-up but is not a gap.
- **GAP:** 1 site (#1), which pins **2 real capability gaps** —
  `ccmp`/`ccmn` decoder-missing and base `csinv`/`csneg` decoder-disagreement.
- **No new papered-over gaps** were introduced in PRs #106–#116 beyond the
  already-known i9 pair (PR #114). The ratchet/allowed-failure pattern Pete
  worried about is **absent** from the current tree (oracle is a plain 100%;
  m6-release gate is a strict 3-way byte-match). sdiv (i35) and the sysname
  fail-hard (i9) are both genuinely *fixed*, not skipped.
