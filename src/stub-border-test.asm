; Minimal experiment stub for diagnosing the M0 LOAD-CODE-then-CALL crash.
; Replaces the full stub.asm logic with the absolute simplest visible action:
;   1. Set the SAM border to green (port &FE — paging-independent).
;   2. Return cleanly (no DI, no HALT, no SAMDOS hooks).
;
; Diagnostic interpretation:
;   - Border turns green   -> stub was loaded and called. URPORT/screen state
;                              is irrelevant for the border; port &FE is on
;                              the ASIC and is unaffected by HMPR.
;   - Border stays default -> stub was never reached at all.
;   - Behaviour after RET  -> tells us whether BASIC/ROM context survives.

                org     &6000

                ld      a, &04          ; border colour 4 = green
                out     (&fe), a        ; port &FE bits 0-2 = border colour
                ret
