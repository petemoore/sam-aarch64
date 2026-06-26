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
; FAITHFUL FLOW (i259). This demo mirrors the patched-bootblock inject's faithful
; opening-screen sequence (docs/specs/samboot-opening-screen.md) so the i230
; hardware re-test exercises the same ROM-routine path the real inject will: it
; sets the lower-screen print position via CLSLOWER (&06B5) before the banner — so
; the banner lands in the lower screen exactly as the stock report-50 handler did —
; and waits for ANY key via the verbatim stock WTFK (READKEY &1CB1; Z = no key),
; not the earlier Esc-only poll. CLSLOWER and READKEY are real ROM routines (READKEY
; RST-30s into the paged TWOKSC); the Go harness models them — StubReturn for
; CLSLOWER's hardware-only screen-clear, ModelReadkey for the any-key wait — so the
; demo still runs end-to-end in emulation, and the pixels/position are confirmed on
; Pete's real SAM (i230). The banner text is printed via RST &10 (recorded by the
; harness print recorder).
;
; THE RECIPE:
;   1. DI while we rebuild the line table.
;   2. Border baseline = colour 0 (OUT &FE) — LINEINT drives CLUT reg 0, which the
;      border tracks, so both move together once the rainbow is armed.
;   3. Build LINICOLS (&5600) from PALTAB (&55D8+1) — the verbatim stock-ROM
;      RAINBOW SCREEN port (&ED1B): 4-byte entries {scan_lo, 0, colour, colour},
;      scan stepping +&0B from 0 while < &A6, then an &FF terminator.
;   4. CALL CLSLOWER (&06B5) — select channel K / lower screen (verbatim stock
;      MAINER3 print-position step), so the banner lands in the lower screen.
;   5. Print the MGT copyright banner via RST &10.
;   6. EI — arms the line-interrupt rainbow (THE step the flat-yellow demo omitted).
;   7. Hold until ANY key: verbatim stock WTFK — `call READKEY (&1CB1) / jr z`
;      (READKEY returns Z when no key is ready); driven by an injected key in
;      emulation (ModelReadkey).
;   8. Teardown: LINICOLS[0]=&FF disarms the rainbow next frame.
;   9. EI + RET cleanly to trinload via tr_terminate.
;
; The Go harness has no display, so mgt_screen_demo_test.go asserts the demo built
; the exact LINICOLS table and printed the exact banner text and returned cleanly —
; not the rendered pixels, which are confirmed on Pete's real SAM (i230).
; Emulation-verified ≠ hardware-verified.

                org     &8000

PALTAB:          equ &55D8              ; ROM palette table (stripes read from +1)
LINICOLS:        equ &5600              ; line-colour table (stripes write here)
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

                ; --- (4) select the lower screen (verbatim stock MAINER3 step) ---
                ; CLSLOWER (&06B5) clears the lower screen and selects channel K, so
                ; the banner below lands in the lower screen exactly as the stock
                ; report-50 banner did. Real ROM routine (hardware-only screen effect;
                ; the harness stubs it to a RET). Matches the inject's CLSLOWER call.
                call    &06b5

                ; --- (5) print the MGT copyright banner ---
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

                ; --- (6) arm + render: EI so FRAMINT re-arms STATPORT from
                ;     LINICOLS[0] and LINEINT paints CLUT-0 each scan line ---
                ei

                ; --- (7) hold the screen until ANY key — verbatim stock WTFK
                ;     (READKEY &1CB1 returns Z when no key is ready); driven by an
                ;     injected key in emulation (ModelReadkey) ---
mgt_wtfk:
                call    &1cb1           ; READKEY — Z when no key is ready
                jr      z, mgt_wtfk     ; loop until ANY key is pressed (NZ,CY)

                ; --- (8) teardown: disarm the rainbow next frame ---
                di
                ld      a, &ff
                ld      (LINICOLS), a

                ; --- (9) restore interrupts and RET cleanly to trinload ---
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
