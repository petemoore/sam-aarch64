; test_pagepool.asm — boot self-test for the live IDE page pool (item i2b).
;
; Off-axis suite (relocated below &C000 to relieve the section-C test-variant
; budget, like the other suites in src/test_offaxis_cluster.asm). Included by
; that cluster and invoked from cluster_dispatch. Reaches the pool routines
; (pp_alloc_page / pp_free_page / pp_owner_of / pp_free_count), the constants
; (PP_SCRATCH / PP_RESERVED / PP_FAIL / PP_NPAGES / POOL_FIRST_FREE /
; PP_TABLE_BASE / PP_MAX_PAGES) and fail_with_tag through the cluster's
; --importfile=build/assembler.sym, so each resolves to its main-binary address.
;
; LMPR-swap safety: every routine called here is HMPR-stable (pp_* are pure
; reads/writes of the page_owner[] table in section D; the constants are
; immediates, not section-A inline literals; fail_with_tag is the same
; cluster-safe halt path the other suites use). None reach paged_call or the
; section-B trampoline, so they survive the off-axis LMPR swap.
;
; Authority: docs/specs/ide-memory-model-design.md §6. Pool: src/pagepool.asm;
; boot survey: src/pool_boot.asm (pool_boot_init runs before this suite).

; POOL_TEST_N0 — one byte of scratch in section D's free region, immediately
; after the page_owner[] table (PP_TABLE_BASE + 1 + PP_MAX_PAGES). HMPR-stable,
; so it is reachable under the off-axis LMPR swap.
POOL_TEST_N0:   equ PP_TABLE_BASE + 1 + PP_MAX_PAGES

; ===========================================================================
; run_pool_self_tests — the spec §6 boot self-test on the live, boot-sized
; pool: claim every FREE page, confirm the count, confirm the reserved pages
; never moved, free them all, and confirm the FREE count is restored exactly.
; Machine-size independent (the free count N0 may be 0 on a 256 KB machine, in
; which case every step is a trivially-satisfied no-op). Leaves the pool exactly
; as pool_boot_init left it. On any mismatch: jp fail_with_tag (&F0..&F3).
; Clobbers: A, B, C, D, E, HL.
; ===========================================================================
run_pool_self_tests:
                ; N0 = current free count (pages the pool can hand out).
                call    pp_free_count
                ld      (POOL_TEST_N0), a

                ; Claim every FREE page, tagging it SCRATCH. We don't count in a
                ; register here — pp_alloc_page clobbers B/C/D/E/HL/A — so the
                ; "claimed exactly N0" check is expressed as "free count is 0
                ; after the pool is exhausted" (every FREE page got claimed).
pool_test_claim:
                ld      a, PP_SCRATCH
                call    pp_alloc_page           ; A = page, or PP_FAIL when exhausted
                cp      PP_FAIL
                jr      nz, pool_test_claim     ; got a page — keep claiming
                ; Pool exhausted: no FREE page may remain.
                call    pp_free_count
                or      a
                jr      z, pool_test_count_ok
                ld      a, &F0
                jp      fail_with_tag
pool_test_count_ok:
                ; Reserved pages 0..POOL_FIRST_FREE-1 must still be RESERVED.
                ld      b, POOL_FIRST_FREE
                ld      c, 0
pool_test_resv:
                ld      a, c
                call    pp_owner_of             ; A = owner byte
                cp      PP_RESERVED
                jr      z, pool_test_resv_ok
                ld      a, &F1
                jp      fail_with_tag
pool_test_resv_ok:
                inc     c
                djnz    pool_test_resv

                ; Free every page we claimed: scan all present pages and free
                ; the SCRATCH-tagged ones (the only SCRATCH pages are ours).
                ld      a, (PP_NPAGES)
                or      a
                jr      z, pool_test_freed      ; no pages present
                ld      b, a                    ; pages to scan
                ld      c, 0                    ; page index
pool_test_free:
                ld      a, c
                call    pp_owner_of
                cp      PP_SCRATCH
                jr      nz, pool_test_free_next
                ld      a, c
                push    bc
                ld      b, PP_SCRATCH           ; expected owner for the tag assertion
                call    pp_free_page            ; A = 0 ok / PP_FAIL
                pop     bc
                cp      PP_FAIL
                jr      nz, pool_test_free_next
                ld      a, &F2
                jp      fail_with_tag
pool_test_free_next:
                inc     c
                djnz    pool_test_free
pool_test_freed:
                ; FREE count must be restored to N0.
                call    pp_free_count
                ld      hl, POOL_TEST_N0
                cp      (hl)
                ret     z
                ld      a, &F3
                jp      fail_with_tag
