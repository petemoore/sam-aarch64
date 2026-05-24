; page13_test_payload.asm — tiny payload assembled standalone and HLOAD'd
; into physical page 13 at boot.  Exercised by run_paged_call_self_tests
; in src/m3/test_paged_call.asm; not used in any production path.
;
; Per docs/notes/2026-05-28-paged-call-architecture.md plan-PR 1.
;
; Contents:
;   &8000  ld a, &42 / ret — trivial paged-target stub used by the
;          paged_call assertion.  Three bytes total.
;
; The file is assembled with org &8000 so the load address recorded
; in the on-disk file header matches the section-C window used during
; HLOAD (the trampoline reprograms HMPR around the RST 8 so the load
; writes land in physical page 13 rather than the caller's section-C
; page).  Same idiom as enctab.enc's "loaded at &8000 but body lives
; in page 4" arrangement (see src/m3/loader.asm).
;
; The plan-PR 1 instructions originally specified BOTH a sentinel byte
; (for the paged_data round-trip assertion) and the trivial stub.  The
; sentinel was dropped because the paged_data primitives cannot be
; safely tested from section-D test code; see test_paged_call.asm's
; header for the gap analysis.

                org     &8000

                ; offset &8000: trivial paged-target stub.  Returning
                ; A=&42 lets the self-test confirm both that paged_call
                ; landed the right physical page in section C (so the
                ; stub's instructions are fetched correctly) and that
                ; the trailer restored HMPR cleanly.
                ld      a, &42
                ret
