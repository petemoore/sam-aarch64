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
; routine into the free space. `inject` runs the real B-DOS init itself, then reads
; the SAMBOOT BIOS config and either auto-boots a record or RETs to &40A1 — which
; is `restore:`, the byte-identical verbatim tail of Colin's bootblock (the screen
; redraw + BASIC exit). On the no-auto-boot path our code never trampled the screen,
; so there is no stripes redraw here (that obsolete i135d fold is dropped; the stripe
; PIXELS are an i230 hardware concern).
;
;   inject:           call &805F            ; the real B-DOS init (returns on hardware)
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
; ===========================================================================
inject:
                call    &805F               ; real B-DOS init (no return in the Go core)
inject_decision:                            ; host decision test enters HERE (skips &805F)
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
