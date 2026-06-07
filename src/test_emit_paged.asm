; test_emit_paged.asm — boot-time self-tests for paged emit.
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/plans/2026-05-27-m6-paged-out.md Task 5.  Verifies the
; runtime emit path described in
; docs/specs/paged-out-design.md.
;
; Preconditions: load_enctab has completed so LMPR_DEFAULT_RUNTIME is
; live and LMPR is currently = LMPR_DEFAULT_RUNTIME (post-map_out).
; The test runs its own enctab_map_in / map_out bracket so it
; observes the same paging state the encoder would.
;
; Coverage:
;   1. reset_out_buffer sets OUT_PC = &4000, OUT_ZONE = 0, OUT_LEN = 0.
;   2. emit_byte in low zone writes to section B (page 5 via LMPR_ENCTAB).
;      Read-back via the same section-B window.
;   3. Forcing OUT_PC to &7FFF and emitting one more byte flips
;      OUT_ZONE to 1 and wraps OUT_PC to &4000.
;   4. emit_byte in high zone writes to page 6 (read-back via
;      LMPR=LMPR_OUT_HIGH section-B window).
;
; A failure does `jp fail` — same red-border + printer-channel "FAIL"
; banner as every other self-test suite.  When the test owns an LMPR
; window we restore it via the same lmpr_save_test slot.
;
; Compile-time included only when BUILD_TESTS is defined (the test
; build variant); the call site in assembler.asm is also gated.


; -----------------------------------------------------------------------
; run_emit_paged_self_tests — entry point.
;
; Input:  none.  Output: returns on success.  On any mismatch: jp fail.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_emit_paged_self_tests:

; The emit path expects LMPR_ENCTAB throughout — that's its zero-overhead
; low-zone window.  Bracket the whole test in enctab_map_in/map_out so
; we observe the same paging state the encoder would see.
                call    enctab_map_in

                call    reset_out_buffer

; ----- (1) initial state -------------------------------------------------
                ld      a, (OUT_ZONE)
                or      a
                jp      nz, emit_paged_fail
                ld      hl, (OUT_PC)
                ld      a, h
                cp      &40
                jp      nz, emit_paged_fail
                ld      a, l
                or      a
                jp      nz, emit_paged_fail
                ld      hl, (OUT_LEN)
                ld      a, h
                or      l
                jp      nz, emit_paged_fail

; ----- (2) low-zone emit lands in page 5 via section B ------------------
                ld      a, &AB
                call    emit_byte
                ld      a, &CD
                call    emit_byte

                ld      hl, &4000           ; section B = page 5 under LMPR_ENCTAB
                ld      a, (hl)
                cp      &AB
                jp      nz, emit_paged_fail
                inc     hl
                ld      a, (hl)
                cp      &CD
                jp      nz, emit_paged_fail

                ld      hl, (OUT_LEN)
                ld      a, h
                or      a
                jp      nz, emit_paged_fail
                ld      a, l
                cp      2
                jp      nz, emit_paged_fail

; ----- (3) force zone transition (set OUT_PC = &7FFF, emit one more) ----
; Setting OUT_LEN to a consistent value too, so the post-emit count is
; sensible (16384 = end of low zone).
                ld      hl, &7FFF
                ld      (OUT_PC), hl
                ld      hl, 16383
                ld      (OUT_LEN), hl

                ld      a, &EE              ; final low-zone byte
                call    emit_byte           ; writes &EE at &7FFF then wraps

                ld      a, (OUT_ZONE)
                cp      1
                jp      nz, emit_paged_fail
                ld      hl, (OUT_PC)
                ld      a, h
                cp      &40
                jp      nz, emit_paged_fail
                ld      a, l
                or      a
                jp      nz, emit_paged_fail

; ----- (4) high-zone emit lands in page 6 -------------------------------
                ld      a, &7B
                call    emit_byte
                ld      a, &91
                call    emit_byte

; Read back via LMPR=LMPR_OUT_HIGH (page 6 in section B).  Bracket
; ourselves to avoid leaving LMPR off the encoder window.  Port 250 =
; LMPR (NOT port 251 — that's HMPR; see trampoline.asm:309 + the SAM
; Coupé Tech Manual §6.10 for port assignments).
                in      a, (250)
                ld      (lmpr_save_test), a
                ld      a, LMPR_OUT_HIGH
                out     (250), a

                ld      hl, &4000
                ld      a, (hl)
                cp      &7B
                jp      nz, emit_paged_fail_with_lmpr
                inc     hl
                ld      a, (hl)
                cp      &91
                jp      nz, emit_paged_fail_with_lmpr

                ld      a, (lmpr_save_test)
                out     (250), a

; LMPR back at LMPR_ENCTAB; exit the encoder window before returning
; to the boot caller.
                call    enctab_map_out
                ret


; Failure path that needs to restore LMPR_ENCTAB before bailing out
; through the standard map_out + fail sequence.
emit_paged_fail_with_lmpr:
                ld      a, (lmpr_save_test)
                out     (250), a
                ; fall through

; Standard failure path — leave the encoder window first so the
; printer-channel banner runs against the default page map.
emit_paged_fail:
                call    enctab_map_out
                jp      fail

lmpr_save_test: defb    0
