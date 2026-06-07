; paged_call_test_payload.asm — tiny payload assembled standalone and
; HLOAD'd into physical page 14 at boot.  Exercised by
; run_paged_call_self_tests in src/test_paged_call.asm; not used in
; any production path.
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-paged-call-architecture.md plan-PR 1.
;
; Contents:
;   &8000  ld a, &42 / ret — trivial paged-target stub used by the
;          paged_call assertion.  Three bytes total.
;
; Assembled with `org &8000` so the bytes appear at &8000-onward in
; section C when HMPR maps page 14 in.  Same idiom as enctab.enc's
; "loaded at &8000 but body lives in page 4" arrangement (see
; src/loader.asm).
;
; Build pipeline: pyz80 emits build/paged_call_test_payload.bin from
; this file; build-m3-disk's -paged-call flag deposits it as CODE file
; "p14" on the test disk image; src/loader.asm::load_page14_payload
; HLOADs it into physical page 14 via the trampoline.

                org     &8000

                ; offset &8000: trivial paged-target stub.  Returning
                ; A=&42 lets the self-test confirm both that paged_call
                ; landed the right physical page in section C (so the
                ; stub's instructions are fetched correctly) and that
                ; the trailer restored HMPR cleanly afterwards.
                ld      a, &42
                ret
