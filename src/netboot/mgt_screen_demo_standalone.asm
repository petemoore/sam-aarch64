; mgt_screen_demo_standalone.asm — a trinload-pushable RAM test that redraws the
; SAM MGT opening rainbow stripes, then RETs to trinload.
;
; This is the first on-hardware confirmation of the patched-bootblock screen
; redraw (i229/i135c), run live in RAM via trinload — no EEPROM flash, no brick
; risk. The stripes routine is the verbatim stock-ROM RAINBOW SCREEN code (&ED1B,
; docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:24665) that Colin's ROM
; patch displaced; it builds the LINICOLS &5600 line-colour table from PALTAB
; &55D9. Pure RAM writes — fully emulation-verifiable (the harness reads LINICOLS
; back) with NO build carve-out (CLAUDE.md §7); SimCoupé / real hardware render
; the actual pixels. The MGT copyright banner (a ROM RST-16 print path) is the
; next increment.
;
; Ends with jp tr_terminate (test_report.asm): di;halt in emulation, RET to
; trinload on real hardware — so trinload survives for the next pushed test.

                org     &8000

PALTAB:          equ &55D8                      ; ROM palette table (stripes read from +1)
LINICOLS:        equ &5600                      ; line-colour table (stripes write here)

mgt_demo_main:
                ; --- the rainbow stripes (verbatim port of ROM &ED1B) ---
                ld      de, PALTAB+1            ; &55D9 — palette table, skip entry 0
                ld      hl, LINICOLS           ; &5600 — line-colour table, L=0
                ld      b, l                   ; B = 0 (scan-line counter)
                ld      c, l                   ; C = 0
mgt_rbowl:
                ld      (hl), b                ; LINICOLS[i].scan_lo = scan number
                inc     hl
                ld      (hl), c                ; LINICOLS[i].scan_hi = 0
                inc     hl
                ld      a, (de)                ; next PALTAB colour
                inc     de
                ld      (hl), a                ; main colour
                inc     hl
                ld      (hl), a                ; alt colour = main
                inc     hl
                ld      a, b
                add     a, &0b                 ; step the scan number by 11
                ld      b, a
                cp      &a6                    ; until &A6 (166)
                jr      c, mgt_rbowl
                ld      (hl), &ff              ; terminate the line-colour list

                ; --- the MGT copyright banner ---
                ; The authoritative ROM text (UMVAL message 0, &F5DD,
                ; docs/sam/...annotated-disassembly.txt:26500): the exact bytes the
                ; stock ROM prints, including &7F (the © glyph). Printed char by
                ; char via RST &10 (the SAM ROM print-a-char restart), which writes
                ; to the screen. On hardware the ROM renders it; in emulation the
                ; harness intercepts RST &10 and records the characters (so we prove
                ; it wrote the right text and did not crash — no ROM, no carve-out).
                ; The é accent (BGFLG foreign mode) and the dynamic "nnnK" from
                ; PRAMTP are faithfulness refinements; "512K" is correct for the
                ; target SAM.
                ld      hl, mgt_banner_text
mgt_banner_loop:
                ld      a, (hl)
                or      a
                jr      z, mgt_banner_done
                rst     &10                    ; print the character in A
                inc     hl
                jr      mgt_banner_loop
mgt_banner_done:

                jp      tr_terminate           ; di;halt in emulation, RET to trinload on hardware

mgt_banner_text:
                defm    "   MILES GORDON TECHNOLOGY plc      "
                defb    &7F                    ; © copyright glyph
                defm    " 1990 SAM Coupe 512K"
                defb    0                      ; end of string

                ; tr_terminate + its module set (test_report is not called here;
                ; only tr_terminate is, but it lives in test_report.asm).
                include "build_udp_frame.asm"
                include "encdrv.asm"
                include "test_report.asm"
