; Minimal experiment stub for diagnosing the M0 LOAD-CODE-then-CALL crash.
; Replaces the full stub.asm logic with the absolute simplest visible action:
;   1. Set the SAM border to green (port &FE — paging-independent).
;   2. DI; HALT to give simcoupe (-exitonhalt 1) a clean exit signal.
;
; Page-agnostic: this code body has no jumps and no absolute address
; references — every byte is a fixed instruction with immediate operands.
; The `org` directive only affects the assembler's listing; the produced
; bytes are identical regardless of where the file ends up in memory.
;
; Diagnostic interpretation:
;   - simcoupe exits 0 within 30s AND border was green at exit
;       -> CLEAR + LOAD CODE + CALL all worked end-to-end. The full-stub
;          failure is specifically about its SAMDOS hooks.
;   - simcoupe exits 124 (timeout, 30s)
;       -> stub never reached. Either CLEAR crashed (visualisation symptom)
;          or LOAD CODE failed silently or CALL didn't dispatch.
;   - simcoupe exits 0 but border NOT green
;       -> something HALT'd before reaching our LD A, 4 / OUT (&FE), A.
;          Probably BASIC ROM error path or interrupt-handler weirdness.

                org     &8000

                ld      a, &04          ; border colour 4 = green
                out     (&fe), a        ; port &FE bits 0-2 = border colour
                di                      ; mask interrupts
                halt                    ; exit-on-halt signal to simcoupe
