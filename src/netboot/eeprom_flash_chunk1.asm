; eeprom_flash_chunk1.asm — flash the trinity-autoboot bootloader into the
; Trinity EEPROM bootblock (chunk 1) and verify the write (item i135c).
;
; This is the FIRST destructive EEPROM write: it replaces the boot code itself.
; It reuses the EXACT write path the non-destructive round-trip (i225/i226)
; proved on real hardware — write_chunk -> write_256 / write_enable / wait_ready
; (eeprom.asm) — and the test_report-over-network primitive, so the result is
; read off the wire identically in the koron-go ENC28J60 emulator and on the
; real Trinity.
;
; Chunk 1 (device &2000) is the bootblock the SAM ROM fetches and runs at &4000
; at power-on (i197c-b3). The bootloader is a bring-up build with BOOT_RECORD=3
; hardcoded, so once flashed the SAM auto-boots SD record 3 (trinload) — no
; config chunk required. The bootloader bytes are embedded verbatim
; (bootloader_chunk1_data.asm, generated from trinity-autoboot/build/bootloader.bin).
;
;   1. copy the embedded bootloader bytes into the `chunk` buffer;
;   2. write them to chunk 1;
;   3. read chunk 1 back and verify it matches the embedded bytes byte-for-byte;
;   4. report PASS/FAIL (SATR UDP packet + border) and return to trinload.
;
; SAFETY: bricking is recoverable — the ROM bypasses EEPROM boot when SPACE is
; held, the EEPROM reflashes from a floppy-booted B-DOS, and Pete has a means to
; reflash the physical ROM to factory. The full eeprom.bin capture (i87a-b1) is
; the byte-exact restore backup.
;
; Verified in emulation by eeprom_flash_chunk1_test.go (incl. a write-fault
; negative control) under the eeprom.go 25LC1024 write model; emulation is not
; hardware verification (CLAUDE.md §5) — the hardware flash is i135c proper.

                org     &8000

EEP_BOOTBLOCK:   equ 1             ; chunk 1 = the bootblock (device &2000)
TEST_ID_FLASH:   equ 2             ; SATR test id for the bootblock flash
FLASH_PHASE_VERIFY: equ 1          ; FAIL phase: read-back verify after the write

; ---------------------------------------------------------------------------
; eeprom_flash_main — the entry point.
; ---------------------------------------------------------------------------
eeprom_flash_main:
                ; Bring up the ENC driver so test_report can transmit, and wait
                ; for the 10BASE-T link before the first PROACTIVE transmit
                ; (the report would be dropped during the link-down window, i127).
                ld      hl, TR_SRC_MAC          ; HL -> our MAC (in test_report.asm)
                call    drv_init
                call    drv_wait_link

                ; 1. copy the embedded bootloader bytes into `chunk`.
                ld      hl, BOOTLOADER_DATA
                ld      de, chunk
                ld      bc, 1024
                ldir

                ; 2. write `chunk` to the bootblock (chunk 1).
                ld      a, EEP_BOOTBLOCK
                ld      (value), a
                call    write_chunk

                ; 3. read it back and verify it matches the embedded bytes.
                ld      a, EEP_BOOTBLOCK
                ld      (value), a
                call    read_chunk
                call    flash_verify            ; Z = match; NZ = mismatch, HL = offset
                jr      nz, flash_fail

                ; PASS: detail = [chunk, phase=0, off=0].
                ld      hl, 0
                ld      c, 0                    ; phase 0
                ld      a, 0                    ; status 0 = PASS
                jr      flash_report

flash_fail:
                ld      c, FLASH_PHASE_VERIFY
                ld      a, 1                    ; status 1 = FAIL
flash_report:
                ; A = status, C = phase, HL = mismatch offset.
                push    af
                ld      a, EEP_BOOTBLOCK
                ld      (flash_detail+0), a     ; chunk number
                ld      a, c
                ld      (flash_detail+1), a     ; phase
                ld      a, l
                ld      (flash_detail+2), a     ; offset lo
                ld      a, h
                ld      (flash_detail+3), a     ; offset hi
                pop     af                      ; A = status
                ld      de, TEST_ID_FLASH
                ld      b, 4                    ; detail length
                ld      hl, flash_detail
                call    test_report
                ; Terminate: di;halt in emulation, RET to trinload on hardware
                ; (tr_terminate detects which via the unmapped-port probe, i228),
                ; so trinload survives for the power-cycle into the new bootblock.
                jp      tr_terminate

; ---------------------------------------------------------------------------
; flash_verify — compare the read-back `chunk` against BOOTLOADER_DATA.
; Return: Z + A=0 on full match; NZ + HL = first mismatch offset otherwise.
; ---------------------------------------------------------------------------
flash_verify:
                ld      hl, chunk
                ld      de, BOOTLOADER_DATA
                ld      bc, 0
fv_loop:
                ld      a, (de)
                cp      (hl)
                jr      nz, fv_bad
                inc     hl
                inc     de
                inc     bc
                ld      a, b
                cp      4                       ; reached 1024 (B=4, C=0)?
                jr      nz, fv_loop
                xor     a                       ; A=0, Z set: full match
                ret
fv_bad:
                ld      h, b
                ld      l, c                    ; HL = mismatch offset
                or      &ff                     ; A nonzero, NZ
                ret

; ---------------------------------------------------------------------------
; Shared building blocks (same module set + include order as netboot_client).
; ---------------------------------------------------------------------------
                include "build_udp_frame.asm"   ; build_udp_frame + PARAMS/PACKET
                include "encdrv.asm"             ; drv_init/drv_write + wait_ready
                include "enc_link.asm"           ; drv_wait_link (PHY link-up, i127)
                include "eeprom.asm"             ; read_chunk/write_chunk + value/chunk
                include "test_report.asm"        ; test_report + TR_SRC_MAC/TR_*

; ---------------------------------------------------------------------------
; The bootloader image flashed into chunk 1 (generated; 1024 bytes).
; ---------------------------------------------------------------------------
                include "bootloader_chunk1_data.asm"  ; BOOTLOADER_DATA

; ---------------------------------------------------------------------------
; Payload-local storage.
; ---------------------------------------------------------------------------
flash_detail:     defs 4            ; SATR detail: chunk, phase, off_lo, off_hi
