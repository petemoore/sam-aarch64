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
; returns and the banner draws. RISK: Colin overwrote the UTMSG msg-0 TEXT region
; (&F5D5-&F615) with his EEPROM reader, so the banner text may render garbled on
; hardware — an i230 hardware-confirm item, NOT a reason to change the verbatim code.
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

                ; --- MGT banner: verbatim ROM v3.0 report-&50 banner body (&0F7F),
                ; minus WTFK + teardown. CLSLOWER/UTMSG/PRNUMB1/RST10/RST30 are all
                ; intact on Colin's ROM (colin-rom-fork-diff.md bottom-line table).
                ; CALL &06B5 (CLSLOWER) is prepended because the stock body was entered
                ; via REPORT-50 (which had set the lower-window print position); we call
                ; it directly so we set it ourselves. HARDWARE-ONLY BY POSITION: &805F
                ; above does not return in the Go core, so this is unreached in emulation
                ; (the same honesty line as the post-&805F config read); hardware draws it.
                call    &06b5                   ; CLSLOWER — set lower-window print pos
                xor     a
                call    &3db0                   ; UTMSG msg 0 -> MGT copyright banner
                ld      hl, &5a34               ; BGFLG
                ld      a, &82                  ; e-acute (foreign-set on)
                ld      (hl), a
                rst     &10
                ld      (hl), 0
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

; samboot_read_config + wait_ready + the gated+relocated eeprom.asm reader.
                include "samboot_config.asm"
; bdos_boot_record + bdos_select_record + BD_BOOT_RECORD only (the read-only boot
; primitive). Built WITHOUT NETBOOT_HOSTTEST so the RST-8 ALHK dispatch is present,
; and WITHOUT NETBOOT_WANT_CLAIM so the WRQ write/claim path is excluded; the
; SAMBOOT_BOOTBLOCK gate inside bdos_seam.asm drops every other (unused) routine so
; the image fits the 674 free bytes.
                include "bdos_seam.asm"
