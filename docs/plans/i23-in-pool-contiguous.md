# i23 — IN buffer → page pool (contiguous-run), lifting the 96 KB ceiling

**Design decision (Pete, 2026-06-23, this session): Option A — contiguous-run per
buffer.** SAMDOS `HLOAD`/`HSAVE` are contiguous-only (no seek/scatter — `ldblk`
`samdos/src/c.s:575` + the `&C000` page-cross in `ctas` `c.s:318-374`), so the IN
buffer cannot be scattered across non-contiguous pool pages with frozen
interfaces. Instead IN is allocated as **one contiguous run** from the i2 page
pool. The reader's hot-path inner loop (the `inc`-LMPR page advance in
`in_normalise_hl` and the `reader_next_kind` copy loop) is therefore **unchanged**
— only the *base page* becomes a runtime value. No `in_pages[]` scatter table.

**Ceiling after this change:** the largest contiguous free run the pool can
provide. On a 512 KB SAM that is the `16..31` run (16 pages = 256 KB) for inputs
that don't fit the `7..12` run; on a 256 KB SAM the only run is `7..12` (6 pages =
96 KB, unchanged — accepted under option A). A custom scatter loader (true
non-contiguous, fragmentation-proof) was considered and deferred (not tracked —
Pete chose plain A).

## Pieces

### 1. `pp_alloc_run` / `pp_free_run` (new pool primitives)
Go authority `tools/sam-aarch64/pagepool/pagepool.go`:
- `AllocRun(n int, owner Owner) (page int, ok bool)` — first-fit: lowest index `i`
  such that pages `i..i+n-1` are all `Free`; tag them all `owner`; return `i`. `ok
  == false` if no run of `n` consecutive free pages exists. `n<=0` → panic.
- `FreeRun(page, n int, expected Owner) error` — free `n` consecutive pages
  starting at `page`, asserting each currently carries `expected` (any mismatch →
  error, table unchanged).

Z80 port `src/pagepool.asm`:
- `pp_alloc_run` — entry `A=owner`, `B=n`; return `A=first page` or `A=PP_FAIL`.
  Scan `PP_OWNER[0..nPages-n]`; for each start, check `n` consecutive `PP_FREE`;
  on a full match tag all `n` with owner and return the start; else advance.
- `pp_free_run` — entry `A=first page`, `B=n`, `C=expected owner`; verify each of
  the `n` pages carries `C`, then set all to `PP_FREE`; `A=0` ok / `A=PP_FAIL`.

Test `tools/netboot-oracle/z80/pagepool_test.go`: a fragmented-pool case (reserve
a gap so first-fit must skip a too-small run) compared to the Go oracle; plus a
free-run round-trip.

### 2. Pool boot reservation — free the IN pages (`src/pool_boot.asm`)
IN's static pages `7..12` must become `Free` so `pp_alloc_run(PP_IN)` can hand
them out. New reserved set (IN is the first pool consumer): reserve `0..6` and
`13..15`; leave `7..12` (and `16..PRAMTP`) `Free`. ENCTAB(4)/OUT(5,6)/payloads
(13,14)/disasm(15) stay statically reserved — their pool migration is later i2
bricks. Replace the `POOL_FIRST_FREE` single-range reserve with two explicit
reserve ranges (`0..6`, `13..15`).

Update the i2b boot self-test `src/test_pagepool.asm`: its "pages
`0..POOL_FIRST_FREE-1` are RESERVED" check becomes "pages `0..6` and `13..15` are
RESERVED" (7..12 are now Free). The claim-all/free-all round-trip is unaffected
(it claims every Free page incl. 7..12 as SCRATCH then frees them — pool restored).

### 3. Dynamic base page (`src/main_loop.asm`, `src/reader.asm`)
New resident var `IN_BASE_LMPR: defb 0` — the full LMPR value (`&20 | base_page`)
of the allocated run's first page. Set by `load_in_file`. Replaces the
`LMPR_IN_BASE` constant in the IN-position computations:
- `reset_reader_to_in_buf` (`main_loop.asm:123`): `IN_POS_PAGE := (IN_BASE_LMPR)`.
- `reader_init` IN_END calc (`reader.asm:~134`): `IN_END_PAGE = page_index +
  (IN_BASE_LMPR)`.
The page-advance idiom (`in a,(250)/inc a/out (250),a`) stays — contiguous.

### 4. `load_in_file` — allocate the run (`src/main_loop.asm:1496`)
- Phase 0: `pp_alloc_run(n=1, PP_IN)` → head page (fail → `fail_with_tag` tag &04).
  `IN_BASE_LMPR := head | &20`.
- Phase 1 (head read): HGTHD; read whole-file geometry; HLOAD 512 B head into the
  head page (`B = IN_BASE_LMPR & &1F`). Decode `editor_region_offset` → prefix
  `in_file_pages` / `in_file_len`; normalise (page-aligned → pages-1/16384).
- Compute `run_size = in_file_pages + 1` (HLOAD writes `C` full pages + remainder,
  touching `C+1` physical pages; post-norm remainder is always > 0 for a real
  file). `pp_free_page(head, PP_IN)`; `pp_alloc_run(run_size, PP_IN)` → base (fail
  → `fail_with_tag` tag &03 "IN run doesn't fit the pool" — replaces the old `cp
  7` 96 KB bound). `IN_BASE_LMPR := base | &20`.
- Phase 2 (prefix load): re-HGTHD; HLOAD prefix `B=base, C=in_file_pages,
  DE=in_file_len`. The head bytes are reloaded from file start, so it's irrelevant
  whether the head page is inside the final run.
- IN_END: `IN_END_PAGE = (IN_BASE_LMPR) + in_file_pages` (i.e. `&20 |
  (base+pages)`); `IN_END_OFFSET = in_file_len`.

No free-at-assemble-end in this PR (the assembler is single-shot; IDE
edit⇄assemble reuse is future i40/i41).

### 5. Self-test fixups (`src/test_reader_paged.asm`)
Set `IN_BASE_LMPR := &27` before its `reset_reader_to_in_buf` call (it stamps a
synthetic blob into page 7 and expects base page 7). The `in_normalise_hl`
page-cross test is unchanged (it sets `IN_POS_PAGE`/LMPR directly).

## Verification (emulation-first; i207 lesson)
1. **Regression** — for every ≤6-page IN, `pp_alloc_run` returns base 7 on the
   harness's clean 32-page pool, so behaviour is byte-identical: `make` test
   variant + `cd tools/z80-test-harness-go && go test ./...`
   (`TestBootSelfTestsPass`, `TestBootSelfTestsFailProbe`, the release-paged test),
   and the release-gate 3-way byte-match.
2. **New capability** — `pp_alloc_run` Go+Z80 unit test under a fragmented pool
   (base ≠ 7). Plus a harness integration test driving a **>96 KB** synthetic IN
   `.tbn` so the run lands at base 16 and the assemble still produces correct OUT
   — proving the dynamic base end-to-end (the harness HLOAD honors HMPR, so the
   load is faithful).
3. **SimCoupé** — local Docker run of the test-variant `.mgt` (boot self-tests)
   before pushing; CI's SimCoupé matrix is the gate. Per i207, SimCoupé-verify
   any boot-payload/disk-wiring change (the harness pre-loads pages).

## Delete this plan in the completing PR.
