; mgt_screen_demo_standalone.asm — a trinload-pushable RAM test that reproduces
; the SAM MGT opening screen (rainbow stripes + copyright banner), holds it until
; the operator presses Esc, then tears it down and RETs to trinload.
;
; This is the first on-hardware confirmation of the patched-bootblock screen
; redraw (i229/i135c), run live in RAM via trinload — no EEPROM flash, no brick
; risk.
;
; WHY THE EARLIER bare-stripes demo showed FLAT YELLOW on hardware: it built the
; LINICOLS line-colour table and printed the banner, then RETurned to trinload
; immediately. The rainbow stripes are rendered LIVE by the ROM line-interrupt
; ISRs — FRAMINT re-arms STATPORT from LINICOLS[0] each frame, and LINEINT paints
; CLUT reg 0 per scan line. With no EI and no HOLD the chain never paints; the
; screen stays whatever single colour CLUT-0 last held (the flat yellow). THE FIX
; is the missing interrupt setup: build LINICOLS, baseline the border, EI, and
; HOLD (so the ISRs paint) until the operator presses a key.
;
; NO ROM-ROUTINE CALLS (emulation-first, CLAUDE.md §7 / i231). trinload has already
; CLS'd the display before it pushes us (it does `xor a / call &014E` at start), so
; the stripes fill the paper-0 area without a re-clear; the banner is printed via
; RST &10 (the one ROM interaction, which the Go harness models with a print
; recorder). Everything else — the LINICOLS build, the border baseline, EI, and the
; key-hold poll — is direct RAM/port work that runs verbatim in the flat Go harness
; (no booted-sysvar state needed, so no carve-out and no flaky ROM-call wander). An
; explicit ROM CLS + CLSLOWER print-positioning is a later refinement once a
; boot-from-0 harness (i232) can run them against a real post-boot sysvar state.
;
; THE RECIPE:
;   1. DI while we rebuild the line table.
;   2. Border baseline = colour 0 (OUT &FE) — LINEINT drives CLUT reg 0, which the
;      border tracks, so both move together once the rainbow is armed.
;   3. Build LINICOLS (&5600) from PALTAB (&55D8+1) — the verbatim stock-ROM
;      RAINBOW SCREEN port (&ED1B): 4-byte entries {scan_lo, 0, colour, colour},
;      scan stepping +&0B from 0 while < &A6, then an &FF terminator.
;   4. Print the MGT copyright banner via RST &10.
;   5. EI — arms the line-interrupt rainbow (THE step the flat-yellow demo omitted).
;   6. Hold until a key: trinload's proven `ld a,&f7 / in a,(&f9) / bit 5` poll
;      (Esc), driven by PressEsc in emulation.
;   7. Teardown: LINICOLS[0]=&FF disarms the rainbow next frame.
;   8. EI + RET cleanly to trinload via tr_terminate.
;
; The Go harness has no display, so mgt_screen_demo_test.go asserts the demo built
; the exact LINICOLS table and printed the exact banner text and returned cleanly —
; not the rendered pixels, which are confirmed on Pete's real SAM (i230).
; Emulation-verified ≠ hardware-verified.

                org     &8000

PALTAB:          equ &55D8              ; ROM palette table (stripes read from +1)
LINICOLS:        equ &5600              ; line-colour table (stripes write here)
STATPORT:        equ &F9               ; keyboard/status read (Esc = bit 5 with row &F7)
BORDPORT:        equ &FE               ; border colour out

mgt_demo_main:
                di                      ; quiesce while we rebuild the line table

                ; --- (2) border baseline = colour 0 ---
                xor     a
                out     (BORDPORT), a

                ; --- (3) build the rainbow LINICOLS (verbatim port of ROM &ED1B) ---
                ld      de, PALTAB+1    ; &55D9 — palette table, skip entry 0
                ld      hl, LINICOLS    ; &5600 — line-colour table, L=0
                ld      b, l            ; B = 0 (scan-line counter)
                ld      c, l            ; C = 0
mgt_rbowl:
                ld      (hl), b         ; LINICOLS[i].scan_lo = scan number
                inc     hl
                ld      (hl), c         ; LINICOLS[i].scan_hi = 0
                inc     hl
                ld      a, (de)         ; next PALTAB colour
                inc     de
                ld      (hl), a         ; main colour
                inc     hl
                ld      (hl), a         ; alt colour = main
                inc     hl
                ld      a, b
                add     a, &0b          ; step the scan number by 11
                ld      b, a
                cp      &a6             ; until &A6 (166)
                jr      c, mgt_rbowl
                ld      (hl), &ff       ; terminate the line-colour list

                ; --- (4) print the MGT copyright banner ---
                ; The authoritative stock-ROM banner text (message 0). Printed char
                ; by char via RST &10 (the SAM print-a-char restart). On hardware the
                ; ROM renders it; in emulation the harness intercepts RST &10 and
                ; records the characters. We carry our own copy because Colin's fork
                ; repurposed the ROM's &F5DD banner bytes for the EEPROM SPI reader.
                ld      hl, mgt_banner_text
mgt_banner_loop:
                ld      a, (hl)
                or      a
                jr      z, mgt_banner_done
                rst     &10             ; print the character in A
                inc     hl
                jr      mgt_banner_loop
mgt_banner_done:

                ; --- (5) arm + render: EI so FRAMINT re-arms STATPORT from
                ;     LINICOLS[0] and LINEINT paints CLUT-0 each scan line ---
                ei

                ; --- (6) hold the screen until a key (trinload's &F9 bit-5 Esc poll;
                ;     driven by PressEsc in emulation) ---
mgt_wtfk:
                ld      a, &f7
                in      a, (STATPORT)   ; &F9: bit 5 = Esc (active-low) for row &F7
                bit     5, a
                jr      nz, mgt_wtfk    ; loop while Esc not pressed (bit5 = 1)

                ; --- (7) teardown: disarm the rainbow next frame ---
                di
                ld      a, &ff
                ld      (LINICOLS), a

                ; --- (8) restore interrupts and RET cleanly to trinload ---
                ei
                jp      tr_terminate    ; di;halt in emulation, RET to trinload on hardware

mgt_banner_text:
                defm    "   MILES GORDON TECHNOLOGY plc      "
                defb    &7F             ; © copyright glyph
                defm    " 1990 SAM Coupe 512K"
                defb    0               ; end of string

                ; tr_terminate + its module set (only tr_terminate is called here,
                ; but it lives in test_report.asm).
                include "build_udp_frame.asm"
                include "encdrv.asm"
                include "test_report.asm"
