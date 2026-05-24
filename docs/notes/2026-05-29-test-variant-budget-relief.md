# Test-variant budget relief — relocating the M5+misc encoder cluster off-axis

**2026-05-29.**  PR #63 (barrier + adr encoders) pushed the BUILD_TESTS
("test") variant of the SAM-side assembler to end at **&C0B6 — 182 bytes
past &C000**, into the &C100 boot-stack / OPVAL_ARRAY region.  This is the
test-variant fragility cliff documented in
`docs/notes/2026-05-28-test-variant-ci-regression.md` and the
`feedback_test_variant_fragility` memory: code above &C000 risks having its
opcodes overwritten by &C100-stack pushes, causing a deterministic boot-hang
(the PR #42 incident).  The 4 remaining FAIL40 encoder families (ldr-literal,
lsl/lsr-imm, bitfield, tbz/tbnz) cannot land until the test variant has real
headroom again.

## What was relocated, and why this choice

The test variant is ~4.2 KB larger than prod; that delta is the BUILD_TESTS
boot self-test suites.  Measured per-suite byte spans on the PR-#63 layout
(from `build/assembler.sym`):

| suite          | bytes |
|----------------|------:|
| slots          |  691  |
| symbol_table   |  247  |
| local_label    |  522  |
| expr_eval_m4   |  449  |
| pc_rel         |  199  |
| directives_m5  |  484  |
| ror_imm        |   84  |
| shifted_reg    |  129  |
| extended_reg   |   60  |
| sysname        |  206  |
| litpool        |  269  |
| trampoline     |   80  |
| emit_paged     |  170  |
| reader_paged   |  192  |
| paged_call     |   35  |
| sysreg_paged   |  310  |

The off-axis precedent to mirror is `test_mem` (PR #52): assemble a
self-test suite at `org &0000` with `--importfile=build/assembler.sym`,
HLOAD it into a spare physical page at boot, and invoke it via a single
**LMPR swap** (`out (250), LMPR_page; call &0000; out (250), default`).
Under the swap, section A maps the off-axis page (so the suite's own code +
inline `defb` literals live there) while **HMPR is unchanged**, so calls
from the off-axis code back to production routines in section C/D resolve to
their real addresses.

### The hard constraint: LMPR-swap safety

The LMPR swap relocates **both** section A and section B.  Therefore an
off-axis suite must NOT depend on anything in section B — specifically the
installed `paged_call` body and its comm buffer.  Anything that reaches
`paged_call` (e.g. the sysreg encoders `encode_mrs/msr/dc/tlbi` used by
`test_sysname`) would jump to garbage under the swap.

A transitive call-graph scan of every production routine the candidate
suites call confirmed which suites are safe:

- **Safe (no `paged_call`, no section-B access, only HMPR-stable section
  C/D production routines + `fail` + `assert_eq32_de_hl_imm`):** slots,
  pc_rel, directives_m5, ror_imm, shifted_reg, extended_reg, litpool.
- **NOT safe (reach `paged_call`):** sysname (and the dedicated
  paged_call / trampoline / reader_paged / emit_paged / sysreg_paged
  suites, which exist precisely to exercise the section-B machinery).

`test_directives_m5` deliberately avoids `walk_records` (which reaches
`reader_next_kind → in_map_current`, an LMPR-touching routine) — it
re-implements the handler bodies "sans the final `jp walk_records`", so its
test path is LMPR-swap-safe.

### The shared-helper extraction

`assert_eq32_de_hl_imm` was defined in `test_slots.asm` but is called by 8
suites (including the already-off-axis `test_mem`).  To relocate `slots`
without breaking the staying suites, the helper was extracted into its own
file `src/m3/test_assert_eq32.asm`, which stays **resident in the main
binary** (section C).  Both inline callers (resolve directly) and off-axis
callers (resolve via `--importfile`) reach it; under the LMPR swap the
helper's `pop bc; (bc)` reads the inline literal via section A (the off-axis
page), exactly the caveat `test_mem` already relies on.

## Result

Relocated suites (slots, pc_rel, directives_m5, ror_imm, shifted_reg,
extended_reg, litpool) into one off-axis binary `build/test_cluster.bin`
(~1911 B incl. dispatcher), HLOADed at boot into **physical page 12** and
run via one LMPR swap (`cluster_dispatch`).  Page 12 is free at the
boot-self-test phase — IN doesn't occupy pages 7..12 until `main_assemble`,
long after these suites complete (same time-multiplex `test_mem`/page-13 and
IN/pages-7..12 already use).

| variant | before    | after     | headroom under &C000 |
|---------|-----------|-----------|---------------------:|
| test    | &C0B6     | **&BA89** | **1399 B**           |
| prod    | &B025     | &B025     | (unchanged)          |

The prod variant is untouched (the relocation is BUILD_TESTS-only).  Test
variant now has ~1.4 KB of headroom below &C000 — comfortably clearing the
≤&BD00 / ≥700 B target with room for the 4 upcoming encoder families.

## Verification

- **SimCoupé Docker sweep (the gate):** `make ci-m{3,4,5,6}` +
  `ci-m{3,4,5,6}-prod` all green — 39 test-variant fixtures and 39
  prod-variant fixtures byte-match GNU; all boot self-tests pass in both
  variants.
- **Go harness:** `go test ./...` passes; `TestVariantBootSelfTests` runs
  again (no panic), printer banner "OK".  `SWEEP=1` corpus sweep:
  **TEST variant 39/39 byte-match-GNU**.  (3 PROD sysreg fixtures TRAP in
  the sweep — a pre-existing harness gap where the prod sweep path deposits
  no `sd13` page-13 file; unrelated to this BUILD_TESTS-only change.)

## Harness fix (>16 KB binary load)

`tools/z80-test-harness-go/harness.go` previously did
`depositPage(2, assemblerBin)`, which **panics** for any binary larger than
one 16 KB page — so `TestVariantBootSelfTests` couldn't run against the
16 KB+ test binary.  Added `depositPagesFrom(startPage, data)` which splits
the binary across consecutive physical pages (page 2 = section C = &8000..,
page 3 = section D = &C000.., …), matching how SAM maps a binary org'd at
&8000 across sections C+D under HMPR.  The test-variant harness tests now
also deposit the page-12 cluster (and page-13 `sd13`).
