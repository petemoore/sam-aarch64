# i24 — OUT-ceiling lift: one contiguous pool run, uniform LMPR-bracketed emit

Registry item **i24** (dependent: i33). Decision: **q45 = option A** (Pete,
2026-07-01) — the OUT buffer becomes ONE contiguous page-pool run sized from
the pass-1 total; EVERY `emit_byte` brackets LMPR the way today's high zone
does; HSAVE saves the >32 KB output as one contiguous file. The free-low-zone
fast path goes away (accepted ~150 ms). Emit-hot-path perf is OUT of scope
(separate deferred item). This plan is deleted in the completing PR.

## Facts (recon-verified, path:line as of branch base)

- `emit_byte` (`src/encoder.asm:59`) is the ONLY writer of OUT; all callers
  (insn_run 4-byte emit, `.inst`, zero-fill, align pads, strings, litpool) go
  through it. Pass 2 is strictly forward (`PASS_PC == OUT_LEN` invariant,
  `src/main_loop.asm:32-37`); no back-patching, no production readback of the
  OUT buffer (the only reader is the `test_emit_paged.asm` self-test).
- Ceiling today: two static pages (5 low / 6 high), `OUT_ZONE` flip at
  `OUT_PC==&8000` (`src/encoder.asm:106-126`), zone-1 sentinel fails with tag
  `&b0` (`src/encoder.asm:83-91`). `OUT_LEN: defw` (16-bit,
  `src/main_loop.asm:1830`).
- High-zone LMPR bracket idiom to generalize: `src/encoder.asm:92-102`
  (live `in a,(250)` snapshot to `emit_lmpr_save`, set, write, restore).
- Section-B mapping arithmetic: to write physical page P at `&4000-&7FFF`,
  `LMPR = &20 OR (P-1)` (LMPR low-5 = section-A page, section B = +1; cf.
  `LMPR_OUT_HIGH equ &25` = page 6 at B, `src/trampoline.asm:252`).
- Pool: `pp_alloc_run` exists (`src/pagepool.asm:197-243`), tag `PP_OUT=4`
  exists; `pp_free_run` (`:255-310`) is `PP_STANDALONE`-gated with a comment
  naming i24 as the first production caller — drop the guard.
- Boot reservation: `pool_boot_init` statically reserves pages 0..6 (range A)
  + 13..15 (`src/pool_boot.asm:27,46-49`); pages 5..6 must leave the static
  reservation and join the pool (the file's own comment anticipates this).
- Pass-1 total: final pass-1 `PASS_PC` (32-bit at `&C159`,
  `src/assembler.asm:128`) IS the output size, but `pass_pc_reset`
  (`src/main_loop.asm:237-249`, called at `:92`) zeroes it before pass 2 and
  NO snapshot exists — capture it between passes.
- `save_out_file` UIFA fill (`src/main_loop.asm:1723-1747`): `UIFA[31]`
  hardcoded page 5; pages field masked `and 3`. The UIFA format itself
  expresses far past 64 KB (full-byte pages + 14-bit remainder), and the
  harness already reconstructs contiguous multi-page saves generically
  (`tools/z80-test-harness-go/harness.go:908-924, 527-535`).
- Budgets: test-variant `code_end` headroom is **11 bytes** (`&BFF5`); prod
  has ~4.9 KB. The uniform emit (dropping the zone branch) should shrink
  prod code; the self-test rewrite is the growth risk — off-axis is the
  escape hatch (established pattern).
- Regression gate: `tools/run-release-gate.sh` (byte-match `release.img`,
  21,752 B — below the old ceiling, so it proves no regression, not the
  lift). SimCoupé runs natively on this host.
- **No >32 KB output vehicle exists** — must be added (see step 6).

## Design (fixed decisions — do not re-litigate)

1. **State** (replacing `OUT_ZONE`): `OUT_RUN_BASE` (first physical page of
   the allocated run), `OUT_RUN_PAGES` (allocated count), `OUT_PAGE_IDX`
   (0-based index of the page the cursor is in). `OUT_PC` keeps walking
   `&4000..&7FFF`. `OUT_LEN` widens to **24-bit** (3 bytes; pool reach
   ~512 KB makes 24 bits ample). Logical offset = `OUT_PAGE_IDX*16384 +
   (OUT_PC-&4000)`.
2. **Uniform emit**: every `emit_byte` brackets LMPR with
   `&20 OR (OUT_RUN_BASE + OUT_PAGE_IDX - 1)` using the existing live
   snapshot/restore idiom. Precompute the current LMPR value into a state
   byte (`OUT_LMPR_CUR`) updated only at page advance, so the per-byte cost
   is the bracket itself, not the arithmetic (still NO other perf work).
3. **Page advance** (`OUT_PC==&8000`): `OUT_PAGE_IDX+1`; if
   `OUT_PAGE_IDX == OUT_RUN_PAGES` → fail tag `&b0` (reinterpreted: "output
   exceeded the allocated run" — an internal invariant break, since pass 1
   sized the run; keep the tag, update its comment). Else recompute
   `OUT_LMPR_CUR`, wrap `OUT_PC` to `&4000`.
4. **Between passes**: snapshot the pass-1 total (3 bytes, `OUT_TOTAL`)
   before `pass_pc_reset`; assert the 32-bit `PASS_PC` byte 3 is zero (fail
   otherwise — output past 16 MB is impossible). Run sizing:
   `pages = ceil(total/16384)`, minimum 1. Allocate `pp_alloc_run(PP_OUT,
   pages)` before pass 2's first emit; on `PP_FAIL` fail with a NEW tag
   (pick a free code near `&b0`; meaning "output too large for free
   memory") — this is the user-facing out-of-memory error, distinct from
   the `&b0` invariant break.
5. **Lifecycle**: no leaked `PP_OUT` pages across assembles — before
   allocating, free any previous run (`pp_free_run` with `OUT_RUN_BASE`/
   `OUT_RUN_PAGES`, guard on `OUT_RUN_PAGES != 0`). Drop `pp_free_run`'s
   `PP_STANDALONE` guard (it becomes production; keep the Go authority in
   sync if it has a parallel gate — check). `pool_boot_init`: range A
   shrinks to pages 0..4 (5..6 join the pool; update the file's comments
   and any count assertions/tests over `pp_free_count`).
6. **`save_out_file`**: `UIFA[31] = OUT_RUN_BASE` (dynamic), pages =
   `OUT_LEN >> 14` computed across the 24-bit value WITHOUT the `and 3`
   mask, remainder = `OUT_LEN & &3FFF`. Keep the computation general and
   parameterised — i33 builds staged/record-seam output on this seam.
7. **`PASS_PC == OUT_LEN` invariant** (`src/main_loop.asm:32-37` checks):
   compare against the widened 24-bit `OUT_LEN` (and PASS_PC byte 3 == 0).
8. **Self-test rewrite** (`src/test_emit_paged.asm`): replace the two-zone
   /32768-sentinel assertions with run-model tests: (a) emit across the
   first page boundary and verify bytes land in run page 0 and 1 (read back
   via LMPR mapping of the actual `OUT_RUN_BASE` pages — no hardcoded page
   5/6); (b) emit past 32768 into a ≥3-page run (loop emits are fine) and
   verify `OUT_LEN`, `OUT_PAGE_IDX`, and a sample byte in page 2; (c) the
   run-exceeded fail path via a deliberately undersized run (restore state
   after). Watch the 11-byte inline budget: if the rewrite grows the test
   variant, move the suite off-axis (the `test_offaxis_cluster.asm` /
   page-12 pattern) — `make check-budget` (both variants) is the hard gate.
9. **>32 KB end-to-end vehicle** (the lift's actual proof): a new harness
   test in `tools/z80-test-harness-go` that assembles a >40 KB-output
   source (generate it in the test or commit a small generator — e.g.
   real instructions bracketing a large `.skip`, plus enough distinct
   instruction bytes after the skip to make offset errors visible),
   byte-compares the saved output against the Go assembler
   (`tools/sam-aarch64`) for the same source, exercising the UIFA
   multi-page reconstruction (`harness.go:908`). Mirror
   `coverage_test.go:78-100` for how sources are driven through the booted
   assembler. A `.skip`-dominated source keeps the fixture tiny while the
   OUTPUT crosses three+ pages.

## Verification ladder

1. `make assembler assembler-prod check-budget` (or the exact existing
   targets — check the Makefile) — both variants build, budgets green.
2. `cd tools/z80-test-harness-go && go test -count=1 ./...` — boot
   self-tests (rewritten emit suite) + the new >32 KB test green.
3. `tools/run-release-gate.sh` locally (SimCoupé native on this host) —
   byte-match must hold (the uniform path emits the identical logical
   stream).
4. `make registry-sync-check` + the usual guards.

## Out of scope

Emit-path T-state optimisation (deferred, separate item — measure only if
free); any i33 record-seam staging; the spec's per-page `alloc_page`
phrasing (`ide-memory-model-design.md:153-166`) is superseded by the
contiguous-run decision — update that spec paragraph to match option A in
this PR (single source of truth).
