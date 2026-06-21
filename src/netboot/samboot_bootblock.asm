; samboot_bootblock.asm — the combined patched-bootblock injection (i229, stage 1
; of the i135c no-brick plan). This is the REAL splice the SAMBOOT flash adds to
; Colin Piggot's Trinity bootblock: a self-contained routine in the bootblock's
; free space (&415E..&43FF, 674 bytes) that the 3-byte splice at &409E
; (`CALL &805F` -> `CALL inject`) hands control to.
;
; THE SPLICE (q50 decision 1, settled by Pete 2026-06-25). The captured bootblock
; (chunk 1, ORG &4000) calls the real B-DOS init at &805F from &409E (bytes
; CD 5F 80). The combined image changes ONLY those 3 bytes to `CALL inject`; the
; splice tool (tools/samboot-splice) patches the captured chunk-1 and copies this
; routine into the free space. `inject` first restores the MGT opening screen
; (i112) — the rainbow stripes + copyright banner Colin's Trinity ROM patch
; displaced — then runs the real B-DOS init itself, then reads the SAMBOOT BIOS
; config and either auto-boots a record or RETs to &40A1 — which is `restore:`, the
; byte-identical verbatim tail of Colin's bootblock (LINICOLS disarm + BASIC exit).
; The screen-draw is VERBATIM stock ROM v3.0 code (the &ED1B RAINBOW SCREEN + the
; &0F7F report-&50 banner body, minus wait-for-key + teardown — see colin-rom-fork-
; diff.md), NOT a reconstruction; the stripe + banner PIXELS are an i230 hardware-
; confirm item (the screenless Go core builds the LINICOLS table — emulation-
; verified — but does not render them).
;
;   inject:           <stripes + banner>   ; restore the MGT opening screen (i112)
;                     call &805F            ; the real B-DOS init (returns on hardware)
;   inject_decision:  call samboot_read_config   ; i176: CY+HL=record, or NC=no auto-boot
;                     ret  nc                ; no auto-boot -> RET to &40A1 = restore: (verbatim)
;                     ld   a, l
;                     ld   (BD_BOOT_RECORD), a
;                     jp   bdos_boot_record  ; i122a: HRECORD select + ALHK boot, no return
;
; CONFIG READ BY-NAME (q50 decision 2): samboot_read_config (i176) finds the
; "SAMBOOT Config  " chunk by name via find_index — not a fixed chunk number — so
; it works wherever the chunk lives on a real card. The 1 KB chunk buffer + the
; index/name storage relocate to a RAM scratch home (SAMBOOT_SCRATCH) so they stay
; OUT of the flashed image; eeprom.asm gates+relocates them under SAMBOOT_BOOTBLOCK.
;
; BYTE BUDGET. The full eeprom.asm + bdos_seam.asm is ~818 B (over the 674 free) due
; to the write/delete/claim paths auto-boot never uses, so this build gates those
; out (SAMBOOT_BOOTBLOCK in eeprom.asm + bdos_seam.asm) and keeps only the read-only
; closure: find_index, check_index, read_chunk, eeprom_enable/disable, exit,
; write_disable, get_chunk (eeprom.asm) + bdos_boot_record, bdos_select_record,
; BD_BOOT_RECORD (bdos_seam.asm). The Makefile asserts the assembled end <= &43FF.
;
; VERIFICATION (q50 decision 3, LIMITED scope). samboot_bootblock_test.go asserts
; (A) the decision logic (5 cases, entering at inject_decision so &805F is skipped)
; against the gated+relocated eeprom.asm reader, and (B) the SPLICED chunk-1 boots
; coherently to &805F via inject (12 read_chunk loads) using the real capture. The
; post-&805F in-chain config read, the real ALHK record boot, the stripe pixels, and
; the hardware-safety of the SAMBOOT_SCRATCH RAM address are the i230 hardware test
; (Pete present). The emulator is NOT taught past B-DOS init (&805F does not return
; in the Go core, by design — i232). Emulation-verified is not hardware-verified.

; --- build-time configuration (define BEFORE the includes so their guards see it) ---
SAMBOOT_BOOTBLOCK:     equ 1               ; -> eeprom.asm + bdos_seam.asm gate+relocate
SAMBOOT_SCRATCH:       equ &E000           ; >=1107 B RAM scratch home for the config read
                                           ; (value/part/total/name/description/chunk/
                                           ; index_store). EMULATION-VALID (flat RAM in the
                                           ; harness); the HARDWARE-SAFE address is confirmed
                                           ; at i230 (Pete present, post-B-DOS-init free RAM).
SAMBOOT_INJECT_ORG:    equ &415E           ; the bootblock free space (file &15E)

                org     SAMBOOT_INJECT_ORG

; ===========================================================================
; inject — the spliced bootblock routine. The 3-byte CALL at &409E enters here.
;
; First it builds the MGT rainbow-stripe line-colour table (i112) — the &ED1B
; RAINBOW SCREEN code Colin's Trinity probe displaced — so the stripes are up while
; B-DOS initialises. Then it runs the real B-DOS init (CALL &805F). AFTER B-DOS init
; returns it redraws the MGT copyright banner (the &0F7F report-&50 body), then reads
; the SAMBOOT BIOS config and either auto-boots a record or RETs to &40A1 = restore:
; (Colin's verbatim tail: LINICOLS disarm + BASIC exit).
;
; Both screen blocks are VERBATIM stock ROM v3.0 code
; (~/sam-archive/samboot-capture/colin-rom-fork-diff.md), NOT a reconstruction.
;
; PLACEMENT (empirical, measured — see the PR / samboot_bootblock_test.go). The
; stripe table build is PURE RAM writes, so it runs in the screenless Go core and is
; emulation-verified (test B reads back LINICOLS). The banner calls real ROM0 display
; routines (CLSLOWER/UTMSG/RST10/RST30) that the screenless core cannot execute
; cleanly — placing the banner BEFORE &805F made the reset-chain run wander into ROM
; and never reach &805F. So the banner sits AFTER CALL &805F: in emulation &805F does
; not return (q50 decision 3 / i232), so the banner is unreached there — hardware-only
; by position, the same honesty line as the post-&805F config read. On hardware &805F
; returns and the banner draws. The banner prints our OWN embedded copy of the msg-0
; text (sbb_banner), so it renders correctly even though Colin's reader overwrote the
; ROM's UMVAL text at &F5DD — the earlier "text may render garbled" risk is RESOLVED.
; The remaining i230 hardware-confirm items are just: (a) the stripe + banner PIXELS
; actually render, and (b) banner visibility/timing on the no-auto-boot path (it draws
; after &805F, then restore:'s CLSLOWER clears it before BASIC — so it is brief; Pete
; may want it tuned at i230, e.g. moved before &805F behind an emulation-skip gate, or
; a short delay — i230 hardware-tuning, out of scope for i229's emulation deliverable).
; ===========================================================================
inject:
                ; --- MGT rainbow stripes: verbatim ROM v3.0 RAINBOW SCREEN (&ED1B),
                ; the exact code Colin's Trinity probe displaced (colin-rom-fork-diff.md
                ; "&ED1B-&ED45"). Pure RAM writes (builds LINICOLS from PALTAB+1), so it
                ; runs in the screenless harness; SimCoupe / hardware render the pixels.
                ld      de, &55D9               ; PALTAB+1
                ld      hl, &5600               ; LINICOLS (L=0)
                ld      b, l                    ; B = scan number, init 0
                ld      c, l
sbb_rbowl:      ld      (hl), b
                inc     hl
                ld      (hl), c                 ; PAL MEM ZERO
                inc     hl
                ld      a, (de)
                inc     de
                ld      (hl), a                 ; main colour
                inc     hl
                ld      (hl), a                 ; alt colour = main
                inc     hl
                ld      a, b
                add     a, 11                   ; next scan to alter at
                ld      b, a
                cp      166
                jr      c, sbb_rbowl
                ld      (hl), &ff               ; terminate the line-colour list

                ; No EI here: the bootblock already executed EI at &409D (the byte
                ; immediately before the spliced CALL at &409E — confirmed FB in the
                ; capture), so interrupts are already enabled when inject runs and the
                ; stripe ISR renders. A redundant EI also perturbs the emulation reset
                ; chain (the duplicate-EI + B-DOS-init spin lets the timer ISR re-run
                ; the boot), so it is omitted — faithful to hardware and emulation-clean.

                call    &805F               ; real B-DOS init (no return in the Go core)

                ; --- MGT banner. We print our OWN embedded copy of the stock UMVAL
                ; msg-0 text (sbb_banner below) instead of CALL UTMSG (&3DB0), because
                ; Colin's EEPROM reader OVERWROTE the ROM's copy of that text at &F5DD
                ; and relocated the UMVAL table pointer — so on the patched ROM `CALL
                ; UTMSG` for msg 0 would print code/garbage. VERIFIED: stock
                ; rom_stock_v30.bin holds the text at file &75DD ("   MILES GORDON
                ; TECHNOLOGY PLC" + 7 spaces + &7F + " 1990  SAM Cou" + 'p'|&80); the
                ; patched rom.bin has Colin's reader bytes (ED 79 CD 07 F6 ...) there.
                ; sbb_banner is byte-exact to the stock text (the &F0='p'|&80 stock
                ; terminator becomes plain 'p' + a 0 terminator for our RST-10 loop).
                ; The é/RAM-size/"K" tail is the verbatim stock &0F7F sequence;
                ; CLSLOWER/RST10/RST30/PRNUMB1 are intact on Colin's ROM
                ; (colin-rom-fork-diff.md). CALL &06B5 (CLSLOWER) is prepended because
                ; the stock body was entered via REPORT-50, which had set the lower-
                ; window print position; we set it ourselves.
                ;
                ; HARDWARE-ONLY BY POSITION: &805F above does not return in the Go core,
                ; so this whole block is unreached in emulation (same honesty line as
                ; the post-&805F config read); hardware draws it, i230 confirms pixels.
                ;
                ; RST 10 register contract (verified, NOT guessed): the ROM RST-10 entry
                ; RST102 (&019E) brackets the channel dispatch with PUSH/POP of HL, DE,
                ; BC and IX, so it preserves them all (the stock SOP print loop at &0244
                ; relies on DE+BC surviving RST 10). The push/pop hl guard below is thus
                ; belt-and-braces against a non-standard channel; DE/BC need no guard.
                call    &06b5                   ; CLSLOWER — set lower-window print pos
                ld      hl, sbb_banner
sbb_bloop:      ld      a, (hl)
                or      a
                jr      z, sbb_bdone
                push    hl                      ; preserve HL across RST 10 (belt-and-braces)
                rst     &10                     ; print the char in A
                pop     hl
                inc     hl
                jr      sbb_bloop
sbb_bdone:      ld      hl, &5a34               ; BGFLG (foreign set on for é)
                ld      a, &82                  ; é
                ld      (hl), a
                rst     &10
                ld      (hl), 0                 ; foreign set off
                ld      a, " "
                rst     &10
                ld      a, (&5cb4)              ; PRAMTP (16 or 32)
                inc     a
                ld      l, a
                ld      h, 0
                add     hl, hl
                add     hl, hl
                add     hl, hl
                add     hl, hl                  ; HL = (PRAMTP+1) * 16 (256 or 512)
                ld      b, h
                ld      c, l
                rst     &30
                defw    &f5ab                   ; PRNUMB1 (RST 30 inline-pointer call)
                ld      a, "K"
                rst     &10

inject_decision:                            ; host decision test enters HERE (skips screen + &805F)
                call    samboot_read_config ; i176: A=1/HL=record (CY set) or A=0 (CY clr)
                ret     nc                  ; A=0: no auto-boot -> RET to &40A1 = restore:
                ld      a, l                ; BD_BOOT_RECORD is 1 byte (record <= 255)
                ld      (BD_BOOT_RECORD), a
                jp      bdos_boot_record    ; i122a: HRECORD select + ALHK boot, no return

; sbb_banner — our embedded copy of the stock UMVAL msg-0 banner text, byte-exact to
; stock rom_stock_v30.bin @ file &75DD (3 leading spaces, "MILES GORDON TECHNOLOGY
; PLC", 7 trailing spaces, &7F = © glyph (SAM charset code 127), " 1990  SAM Coup").
; The stock ROM terminates the message with 'p'|&80 (&F0); our RST-10 print loop uses
; a plain 'p' followed by a 0 terminator, which is equivalent for our loop. Printed by
; the banner block above because Colin's reader overwrote the ROM's own copy.
sbb_banner:     defm    "   MILES GORDON TECHNOLOGY PLC       "
                defb    &7f                     ; © (SAM copyright glyph, code 127)
                defm    " 1990  SAM Coup"
                defb    0                       ; print-loop terminator

; samboot_read_config + wait_ready + the gated+relocated eeprom.asm reader.
                include "samboot_config.asm"
; bdos_boot_record + bdos_select_record + BD_BOOT_RECORD only (the read-only boot
; primitive). Built WITHOUT NETBOOT_HOSTTEST so the RST-8 ALHK dispatch is present,
; and WITHOUT NETBOOT_WANT_CLAIM so the WRQ write/claim path is excluded; the
; SAMBOOT_BOOTBLOCK gate inside bdos_seam.asm drops every other (unused) routine so
; the image fits the 674 free bytes.
                include "bdos_seam.asm"
