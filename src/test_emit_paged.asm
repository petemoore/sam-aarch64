; test_emit_paged.asm — boot-time self-tests for the paged emit path.
;
; Verifies the pool-run emit model described in
; docs/specs/paged-out-design.md: reset_out_buffer sizes a contiguous
; PP_OUT run from the pass-1 total, emit_byte LMPR-brackets every store
; into the run's current page via section B, and out_advance_page walks
; the cursor across page boundaries (failing when the run is exceeded).
;
; Off-axis suite: included by src/test_offaxis_cluster.asm (page 12) and
; invoked from cluster_dispatch, reaching the production routines
; (reset_out_buffer, emit_byte, out_advance_page, pp_free_run,
; pp_owner_of, fail_with_tag) and the resident BUILD_TESTS helper
; out_run_peek via the cluster's --importfile=assembler.sym.
;
; LMPR-swap safety (the reason the pre-run-model suite had to stay
; inline no longer applies): this suite never touches LMPR itself.
; emit_byte and out_run_peek bracket LMPR internally while executing
; from section C (HMPR-stable), restoring the caller's LMPR — the
; cluster page — before returning to section A.  The state bytes
; (OUT_* in main_loop.asm's section-C stream, PASS_PC in section D) are
; all HMPR-stable.
;
; Coverage:
;   1. Run sizing: a synthetic pass-1 total of 16384 allocates exactly
;      1 page; re-entry with 40000 frees it (pp_free_run path) and
;      allocates 3 pages, PP_OUT-owned, cursor reset.
;   2. emit_byte stores through the LMPR bracket; read-back via
;      out_run_peek against the run page the pool actually chose (no
;      hardcoded page numbers).
;   3. Page 0 → 1 boundary: the page-filling byte parks OUT_PC at
;      &8000; the next emit advances into run page 1 and lands at its
;      base.  Bytes verified on both sides of the boundary.
;   4. Past 32768 (the old two-page ceiling): the same dance at the
;      page 1 → 2 boundary, with the 24-bit OUT_LEN crossing &8000.
;   5. 24-bit OUT_LEN carry: an emit at OUT_LEN = &00FFFF carries into
;      byte 2 (OUT_LEN = &010000).
;   6. Exactly-full run: the last byte of the last page emits
;      successfully and parks the cursor (OUT_PC = &8000, idx = last).
;   7. Run-exceeded detection: out_advance_page on the parked
;      last-page cursor returns CF set with OUT_PAGE_IDX unchanged —
;      the predicate behind emit_byte's tag-&b0 overrun fail.
;   8. Cleanup: pp_free_run returns the run; OUT_RUN_PAGES zeroed so
;      main_assemble's reset_out_buffer starts with no previous run and
;      the pool is exactly as pool_boot_init left it.
;
; A failure does `jp fail_with_tag` (&d0..&d9) — red border +
; printer-channel FAIL banner, same as every other suite.


; -----------------------------------------------------------------------
; run_emit_paged_self_tests — entry point.
;
; Input:  none.  Output: returns on success.  On any mismatch:
;         jp fail_with_tag.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_emit_paged_self_tests:

; ----- (1a) sizing: total 16384 → exactly 1 page -------------------------
; reset_out_buffer reads the pass-1 total from PASS_PC (bytes 0..2; byte 3
; must be 0).  16384 is the page-multiple edge: ceil(16384/16384) = 1, not 2.
                ld      hl, 16384
                ld      (PASS_PC + 0), hl
                xor     a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a
                call    reset_out_buffer

                ld      a, (OUT_RUN_PAGES)
                cp      1
                jr      z, emit_test_size1_ok
emit_test_d0:
                ld      a, &d0
                jp      fail_with_tag       ; d0: run sizing wrong
emit_test_size1_ok:

; ----- (1b) sizing: total 40000 → 3 pages; previous run freed ------------
; Re-entering reset_out_buffer must pp_free_run the 1-page run above and
; allocate afresh.  40000 = 2×16384 + 7232 → 3 pages.  The pool hands out
; the lowest contiguous fit both times, so the new run starts at the same
; base — proving the old run went back to FREE (a leak would shift it up).
                ld      a, (OUT_RUN_BASE)
                ld      e, a                ; E = the 1-page run's base
                ld      hl, 40000
                ld      (PASS_PC + 0), hl
                push    de                  ; reset_out_buffer clobbers DE
                call    reset_out_buffer
                pop     de

                ld      a, (OUT_RUN_PAGES)
                cp      3
                jr      nz, emit_test_d0
                ld      a, (OUT_RUN_BASE)
                cp      e                   ; same lowest-fit base as before
                jr      z, emit_test_refit_ok
emit_test_d1:
                ld      a, &d1
                jp      fail_with_tag       ; d1: previous run not freed / moved
emit_test_refit_ok:
                ; All 3 pages carry the PP_OUT owner tag.  (pp_owner_of
                ; clobbers D, E, HL and preserves B, C.)
                ld      a, (OUT_RUN_BASE)
                ld      c, a
                ld      b, 3
emit_test_owner_loop:
                ld      a, c
                call    pp_owner_of
                cp      PP_OUT
                jr      nz, emit_test_d1
                inc     c
                djnz    emit_test_owner_loop

                ; Cursor reset: OUT_PC = &4000, idx = 0, OUT_LEN = 0,
                ; OUT_LMPR_CUR maps the base page at section B.
                ld      hl, (OUT_PC)
                ld      de, &4000
                or      a
                sbc     hl, de
                ld      a, h
                or      l
                jr      nz, emit_test_d2
                ld      a, (OUT_PAGE_IDX)
                or      a
                jr      nz, emit_test_d2
                ld      a, (OUT_LEN)
                ld      hl, OUT_LEN + 1
                or      (hl)
                inc     hl
                or      (hl)
                jr      nz, emit_test_d2
                ld      a, (OUT_RUN_BASE)
                dec     a
                or      &20
                ld      e, a
                ld      a, (OUT_LMPR_CUR)
                cp      e
                jr      z, emit_test_cursor_ok
emit_test_d2:
                ld      a, &d2
                jp      fail_with_tag       ; d2: cursor reset state wrong
emit_test_cursor_ok:

; ----- (2) basic emit + read-back through the bracket --------------------
                ld      a, &AB
                call    emit_byte
                ld      a, &CD
                call    emit_byte

                xor     a                   ; page index 0
                ld      hl, &4000
                call    out_run_peek
                cp      &AB
                jr      nz, emit_test_d3
                xor     a
                ld      hl, &4001
                call    out_run_peek
                cp      &CD
                jr      nz, emit_test_d3
                ld      a, (OUT_LEN)
                cp      2
                jr      z, emit_test_basic_ok
emit_test_d3:
                ld      a, &d3
                jp      fail_with_tag       ; d3: basic emit / read-back wrong
emit_test_basic_ok:

; ----- (3) page 0 → 1 boundary -------------------------------------------
; Force the cursor to the last byte of run page 0 (with OUT_LEN kept
; consistent at 16383).  The page-filling emit parks OUT_PC at &8000
; without advancing; the NEXT emit advances into page 1.
                ld      hl, &7FFF
                ld      (OUT_PC), hl
                ld      hl, 16383
                ld      (OUT_LEN), hl

                ld      a, &EE              ; byte 16384 — fills page 0
                call    emit_byte
                ld      hl, (OUT_PC)        ; parked at the &8000 boundary
                ld      a, h
                cp      &80
                jr      nz, emit_test_d4
                ld      a, (OUT_PAGE_IDX)   ; not yet advanced (lazy)
                or      a
                jr      nz, emit_test_d4
                ld      a, &7B              ; byte 16385 — advances into page 1
                call    emit_byte
                ld      a, (OUT_PAGE_IDX)
                cp      1
                jr      nz, emit_test_d4
                ld      hl, (OUT_PC)
                ld      a, h
                cp      &40
                jr      nz, emit_test_d4
                ld      a, l
                cp      1
                jr      z, emit_test_cross01_ok
emit_test_d4:
                ld      a, &d4
                jp      fail_with_tag       ; d4: page 0→1 advance state wrong
emit_test_cross01_ok:
                ; The bytes landed either side of the boundary.
                xor     a
                ld      hl, &7FFF
                call    out_run_peek
                cp      &EE
                jr      nz, emit_test_d5
                ld      a, 1
                ld      hl, &4000
                call    out_run_peek
                cp      &7B
                jr      z, emit_test_bytes01_ok
emit_test_d5:
                ld      a, &d5
                jp      fail_with_tag       ; d5: boundary bytes misplaced
emit_test_bytes01_ok:

; ----- (4) page 1 → 2 boundary — past the old 32 KB ceiling --------------
; Cursor to the last byte of page 1 (logical offset 32767, OUT_LEN kept
; consistent).  Two emits: byte 32768 fills page 1; byte 32769 lands at
; page 2's base — output beyond 32 KB, impossible under the two-page
; model.  OUT_LEN crosses &8000 as a 24-bit count.
                ld      hl, &7FFF
                ld      (OUT_PC), hl
                ld      hl, 32767
                ld      (OUT_LEN), hl       ; byte 2 already 0

                ld      a, &5A              ; byte 32768 — fills page 1
                call    emit_byte
                ld      a, &91              ; byte 32769 — into page 2
                call    emit_byte

                ld      a, (OUT_PAGE_IDX)
                cp      2
                jr      nz, emit_test_d6
                ld      a, (OUT_LEN)        ; OUT_LEN = 32769 = &008001
                cp      &01
                jr      nz, emit_test_d6
                ld      a, (OUT_LEN + 1)
                cp      &80
                jr      nz, emit_test_d6
                ld      a, (OUT_LEN + 2)
                or      a
                jr      nz, emit_test_d6
                ld      a, 1
                ld      hl, &7FFF
                call    out_run_peek
                cp      &5A
                jr      nz, emit_test_d6
                ld      a, 2
                ld      hl, &4000
                call    out_run_peek
                cp      &91
                jr      z, emit_test_cross12_ok
emit_test_d6:
                ld      a, &d6
                jp      fail_with_tag       ; d6: >32 KB emit wrong
emit_test_cross12_ok:

; ----- (5) 24-bit OUT_LEN carry into byte 2 -------------------------------
; Synthetic counter state; the cursor (mid page 2) is valid, so one more
; emit must carry &00FFFF → &010000.
                ld      hl, &FFFF
                ld      (OUT_LEN), hl
                xor     a
                ld      (OUT_LEN + 2), a
                ld      a, &33
                call    emit_byte
                ld      a, (OUT_LEN)
                ld      hl, OUT_LEN + 1
                or      (hl)
                jr      nz, emit_test_d7
                ld      a, (OUT_LEN + 2)
                cp      1
                jr      z, emit_test_carry_ok
emit_test_d7:
                ld      a, &d7
                jp      fail_with_tag       ; d7: 24-bit OUT_LEN carry wrong
emit_test_carry_ok:

; ----- (6) exactly-full run: the final byte must succeed ------------------
; Cursor to the last byte of the last page (idx 2 of 3).  The emit must
; store and park OUT_PC at &8000 with the index unchanged — a run-filling
; output is legal; only a FURTHER emit is the overrun.
                ld      hl, &7FFF
                ld      (OUT_PC), hl
                ld      a, &77              ; the run's final byte
                call    emit_byte
                ld      hl, (OUT_PC)
                ld      a, h
                cp      &80
                jr      nz, emit_test_d8
                ld      a, (OUT_PAGE_IDX)
                cp      2
                jr      nz, emit_test_d8
                ld      a, 2
                ld      hl, &7FFF
                call    out_run_peek
                cp      &77
                jr      z, emit_test_full_ok
emit_test_d8:
                ld      a, &d8
                jp      fail_with_tag       ; d8: exactly-full emit wrong
emit_test_full_ok:

; ----- (7) run-exceeded detection ------------------------------------------
; With the cursor parked in the run's LAST page, out_advance_page must
; refuse (CF set) and leave OUT_PAGE_IDX unchanged — this is the predicate
; emit_byte converts into the tag-&b0 fail (which halts, so the jp itself
; is not testable here).
                call    out_advance_page
                jr      nc, emit_test_d9    ; must refuse: no page 3 exists
                ld      a, (OUT_PAGE_IDX)
                cp      2
                jr      nz, emit_test_d9

; ----- (8) cleanup: free the run, restoring the boot pool state ------------
                ld      a, (OUT_RUN_PAGES)
                ld      b, a
                ld      c, PP_OUT
                ld      a, (OUT_RUN_BASE)
                call    pp_free_run
                or      a                   ; 0 = success
                jr      nz, emit_test_d9
                xor     a
                ld      (OUT_RUN_PAGES), a  ; no live run: main_assemble's
                                            ; reset_out_buffer skips the free
                ret
emit_test_d9:
                ld      a, &d9
                jp      fail_with_tag       ; d9: advance-refusal / cleanup wrong
