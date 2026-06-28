# Paged OUT buffer — pool-run design

**Status**: living design doc for the assembler's output buffer. Pairs with `docs/specs/samdos-file-io.md` (the HSAVE call site and the HLOAD trampoline pattern) and `docs/specs/ide-memory-model-design.md` §4 (the page pool the buffer draws from).

The OUT buffer is **one contiguous run of page-pool pages**, sized from the pass-1 total and allocated between the passes (q45 = option A; item i24). There is no compile-time OUT page: the pool places the run wherever a contiguous fit exists, and the ceiling is "free contiguous pool pages" (~up to 27 pages / ~432 KB on a 512 KB SAM), not a fixed page count.

## Where OUT lives

- **A contiguous `pp_alloc_run(PP_OUT, n)` run** (`src/pagepool.asm`), `n = ceil(pass-1 total / 16384)`, minimum 1. `reset_out_buffer` (`src/main_loop.asm`) sizes and allocates it between pass 1 and pass 2, snapshotting the final pass-1 `PASS_PC` (which *is* the output size) as the 24-bit `OUT_TOTAL` before `pass_pc_reset` zeroes it.
- **Section B (`&4000-&7FFF`) during emit.** Every `emit_byte` maps the cursor's run page into section B via a per-byte LMPR bracket (section B = LMPR low5 + 1, so `LMPR = &20 OR (page-1)`).
- **Section C during save.** `save_out_file` fills UIFA[31] with the dynamic `OUT_RUN_BASE`; HSAVE sets HMPR from it and auto-pages across `&C000` through the whole run (`docs/specs/samdos-file-io.md`).

The same physical pages are reached through section B during emit (LMPR-controlled) and through section C during save (HMPR-controlled, by HSAVE itself). These are independent mechanisms and don't conflict.

## Runtime state (`src/main_loop.asm`)

| Variable | Size | Meaning |
|----------|------|---------|
| `OUT_PC` | u16 | next emit position in section B (`&4000..&7FFF`; `&8000` = current page full) |
| `OUT_LEN` | u24 LE | total bytes emitted (logical offset = `OUT_PAGE_IDX*16384 + (OUT_PC-&4000)`) |
| `OUT_TOTAL` | u24 LE | pass-1 total — the run-sizing input |
| `OUT_RUN_BASE` | u8 | first physical page of the run |
| `OUT_RUN_PAGES` | u8 | pages allocated (0 = no run yet; doubles as the free-previous guard) |
| `OUT_PAGE_IDX` | u8 | 0-based index of the cursor's page within the run |
| `OUT_LMPR_CUR` | u8 | cached LMPR value mapping that page at section B |

## Uniform emit (`emit_byte`, `src/encoder.asm`)

Every byte pays the same bracket — there is no zero-cost "low zone" fast path (accepted ~150 ms on a release-sized output; emit-hot-path perf is a separate deferred item):

```
1. push AF                          ; preserve the byte
2. HL := (OUT_PC)
3. if H == &80:                     ; current page is full
      out_advance_page              ; OUT_PAGE_IDX+1; OUT_LMPR_CUR+1
                                    ;   (contiguous run); HL := &4000
      on CF (no next page): fail tag &b0
4. in A,(250); save                 ; snapshot the live LMPR
   out (250), OUT_LMPR_CUR          ; section B = the cursor's run page
   pop AF; ld (HL), A               ; store
   out (250), saved                 ; restore the caller's LMPR
5. inc HL; (OUT_PC) := HL           ; may park at &8000 (page full)
6. inc OUT_LEN [24-bit]
```

The page advance is **lazy**: the page-filling byte parks `OUT_PC` at `&8000` and the *next* emit advances. An exactly-run-filling output is therefore legal — the final byte parks the cursor and no advance is attempted. Snapshotting LMPR live (rather than assuming `LMPR_ENCTAB`) keeps the bracket correct under any caller — the encoder window, the boot self-tests, whatever `LMPR_DEFAULT_RUNTIME` carries.

`emit_bytes_n` and every bulk path (`.skip`/`.org` zero-fill, strings, litpool, the INSN_RUN 4-byte emit) loop over `emit_byte`.

## Failure modes

| Tag | Site | Meaning |
|-----|------|---------|
| `&b0` | `emit_byte` | output exceeded the pass-1-sized run — an **internal invariant break** (pass 2 emitted more than pass 1 counted), not user-facing |
| `&b3` | `reset_out_buffer` | the pool cannot supply the run (total ≥ 4 MB, or no contiguous free run large enough) — the **user-facing out-of-memory** |
| `&b4` | `reset_out_buffer` | pass-1 total ≥ 16 MB (`PASS_PC` byte 3 non-zero) — impossible for any real input |

## Lifecycle

`reset_out_buffer` frees any previous run (`pp_free_run`, guarded on `OUT_RUN_PAGES != 0`) before allocating, so repeated assembles (the IDE edit ⇄ assemble cycle) never leak `PP_OUT` pages. The first assemble of a session sees the binary-image initial `OUT_RUN_PAGES = 0` and skips the free.

## Save call site

`save_out_file` (`src/main_loop.asm`) fills UIFA generally over (`OUT_RUN_BASE`, 24-bit `OUT_LEN`): byte 31 = run base page, bytes 32-33 = `&8000`, byte 34 = `OUT_LEN >> 14` (unmasked — a multi-page run far exceeds the old two-page `&3`), bytes 35-36 = `OUT_LEN & &3FFF`. i33's staged/record-seam output builds on this seam.

## Verification

- **Boot self-test** `run_emit_paged_self_tests` (`src/test_emit_paged.asm`, off-axis in the page-12 cluster): run sizing incl. the page-multiple edge and the free-previous path, bracket store + read-back via `out_run_peek`, both lazy page advances, the 24-bit `OUT_LEN` carry, the exactly-full park, and the run-exceeded refusal (`out_advance_page` CF on an undersized run). The suite is cluster-safe because it never touches LMPR itself — `emit_byte` / `out_run_peek` bracket LMPR from section C.
- **Fixtures** (`tests/paged/sources/`, SimCoupé-gated in `ci-paged` / `ci-paged-prod`): `inst_long_emit.s` crosses one run page boundary (> 16 KB); `inst_out_over32k.s` crosses two (40,932 B — past the old two-page / 32 KB ceiling), byte-compared against GNU `as`.
- **Harness oracle test** `TestOutOver32K` (`tools/z80-test-harness-go/out_over32k_test.go`): the same > 32 KB fixture byte-compared against the Go assembler in ~0.2 s — the inner-loop proof.
