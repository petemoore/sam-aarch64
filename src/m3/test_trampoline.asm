; test_trampoline.asm — boot-time self-tests for the section-B HLOAD
; trampoline + the enctab_map_in / enctab_map_out LMPR-swap pair.
;
; Unlike the other test_*.asm suites (which run BEFORE load_enctab on
; inline literal data), this one runs AFTER load_enctab — because the
; pieces it verifies have no useful state to test until enctab.enc has
; been loaded into physical page 4 and LMPR_DEFAULT_RUNTIME has been
; captured at boot.
;
; Assertions:
;   1. The trampoline restored HMPR to its boot value (the page the
;      caller's code lives in).  Read HMPR via `in a, (251)` and
;      compare to a sentinel value we captured at boot.
;   2. enctab_map_out left LMPR at LMPR_DEFAULT_RUNTIME (i.e. the
;      boot LMPR captured before any LMPR-changing call).
;   3. The ENCTAB read wrapper (enctab_map_in + section-A read +
;      enctab_map_out) returns the expected magic bytes at offset 0:
;      'E', 'N', 'C', '1'.
;   4. After the read, both HMPR and LMPR are again at their
;      boot-restored values.
;
; A failure does `jp fail` — same red-border + printer-channel "FAIL"
; banner as every other self-test suite.
;
; Compile-time included only when BUILD_TESTS is defined (the test
; build variant); the call site in assembler.asm is also gated.


; -----------------------------------------------------------------------
; run_trampoline_self_tests — entry point.
;
; Preconditions: load_enctab has completed; HMPR is restored to its
; boot value; LMPR = LMPR_DEFAULT_RUNTIME; ENCTAB body is in physical
; page 4 with magic 'E','N','C','1' at offset 0.
;
; Input:  none.  Output: returns on success.  On any mismatch: jp fail.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_trampoline_self_tests:

; ----- Assertion 1: HMPR restored by load_enctab's trampoline call ----
; The boot value was captured at start:.  Read HMPR and compare.
                in      a, (251)
                ld      hl, boot_hmpr
                cp      (hl)
                jp      nz, fail

; ----- Assertion 2: LMPR == LMPR_DEFAULT_RUNTIME after load_enctab ---
; load_enctab calls enctab_map_in (LMPR_ENCTAB) then enctab_map_out
; (LMPR_DEFAULT_RUNTIME) internally.  After load_enctab returns, LMPR
; should be at LMPR_DEFAULT_RUNTIME.
                in      a, (250)
                ld      hl, LMPR_DEFAULT_RUNTIME
                cp      (hl)
                jp      nz, fail

; ----- Assertion 3: ENCTAB read wrapper returns the magic ------------
; Do the same map_in / read / map_out dance load_enctab does, and
; verify the four magic bytes match 'E', 'N', 'C', '1'.
                call    enctab_map_in

                ld      hl, ENCTAB_BASE
                ld      a, (hl)
                cp      "E"
                jp      nz, trampoline_test_fail_map
                inc     hl
                ld      a, (hl)
                cp      "N"
                jp      nz, trampoline_test_fail_map
                inc     hl
                ld      a, (hl)
                cp      "C"
                jp      nz, trampoline_test_fail_map
                inc     hl
                ld      a, (hl)
                cp      "1"
                jp      nz, trampoline_test_fail_map

                call    enctab_map_out

; ----- Assertion 4: HMPR + LMPR back to boot values after the cycle -
                in      a, (251)
                ld      hl, boot_hmpr
                cp      (hl)
                jp      nz, fail
                in      a, (250)
                ld      hl, LMPR_DEFAULT_RUNTIME
                cp      (hl)
                jp      nz, fail

                ret


; If we hit a magic mismatch while LMPR=ENCTAB, we must restore LMPR
; before jp fail (defensive — fail itself doesn't depend on ROM, but
; restoring keeps the post-mortem state predictable).
trampoline_test_fail_map:
                call    enctab_map_out
                jp      fail


; -----------------------------------------------------------------------
; Storage — captured at start: for use by these assertions.
; assembler.asm's start: writes both bytes before calling the
; self-test suite, alongside the LMPR_DEFAULT_RUNTIME capture.
; -----------------------------------------------------------------------
boot_hmpr:      defb    0
