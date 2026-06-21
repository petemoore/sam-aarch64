; eeprom_roundtrip_standalone.asm — non-destructive Trinity EEPROM write
; round-trip test (item i225; first client of test_report.asm).
;
; PURPOSE: prove the on-SAM EEPROM WRITE path (write_chunk -> write_enable /
; write_256 / wait_ready, eeprom.asm) actually works on real silicon BEFORE the
; first destructive write is the patched boot code (i135c). The write path has
; only ever run in emulation (i221) and reads on hardware (i87a) — never a real
; write. So this exercises it on a CONFIRMED-UNUSED free chunk where a bug cannot
; brick boot, B-DOS, or the network config:
;
;   1. read the scratch chunk, save its bytes (EEP_BACKUP);
;   2. write a position-sensitive pattern over it;
;   3. read it back and verify the pattern byte-for-byte;
;   4. restore the saved bytes;
;   5. read back and verify the restore.
;
; The result is reported by test_report.asm — over the network (a "SATR" UDP
; packet) AND on the border (green = pass, red = fail) — so the SAME binary runs
; identically in the koron-go ENC28J60 emulator and on the real Trinity, and the
; result is read off the wire either way.
;
; SCRATCH CHUNK: chunk 20 (device &6C00) is all-0xFF (unprogrammed, unused) in the
; captured eeprom.bin; chunk 1 is the bootblock, 2-13 are B-DOS, the network
; config + index live in the &1E000+ device tail.
;
; Verified in emulation by eeprom_roundtrip_test.go (incl. a write-fault negative
; control) under the eeprom.go 25LC1024 write model; emulation is not hardware
; verification (CLAUDE.md §5) — i226 is the real-hardware run.

                org     &8000

EEP_SCRATCH:     equ 20            ; scratch chunk: all-0xFF (unused) in the capture
TEST_ID_EEPROM:  equ 1            ; SATR test id for the EEPROM round-trip
EEP_PHASE_WRITE:  equ 1            ; FAIL phase: read-back after the test write
EEP_PHASE_RESTORE: equ 2          ; FAIL phase: read-back after the restore

; ---------------------------------------------------------------------------
; eeprom_roundtrip_main — the entry point.
; ---------------------------------------------------------------------------
eeprom_roundtrip_main:
                ; Bring up the ENC driver so test_report can transmit.
                ld      hl, TR_SRC_MAC          ; HL -> our MAC (defined in test_report.asm)
                call    drv_init

                ; 1. read scratch chunk into `chunk`, snapshot it into EEP_BACKUP.
                ld      a, EEP_SCRATCH
                ld      (value), a
                call    read_chunk
                ld      hl, chunk
                ld      de, EEP_BACKUP
                ld      bc, 1024
                ldir

                ; 2. overwrite `chunk` with the test pattern, write it.
                call    eep_fill_pattern
                ld      a, EEP_SCRATCH
                ld      (value), a
                call    write_chunk

                ; 3. read it back and verify it matches the pattern.
                ld      a, EEP_SCRATCH
                ld      (value), a
                call    read_chunk
                call    eep_verify_pattern      ; Z = match; NZ = mismatch, HL = offset
                jr      nz, eep_fail_write

                ; 4. restore the original bytes from the snapshot.
                ld      hl, EEP_BACKUP
                ld      de, chunk
                ld      bc, 1024
                ldir
                ld      a, EEP_SCRATCH
                ld      (value), a
                call    write_chunk

                ; 5. read back and verify the restore matched the snapshot.
                ld      a, EEP_SCRATCH
                ld      (value), a
                call    read_chunk
                call    eep_verify_backup       ; Z = match; NZ = mismatch, HL = offset
                jr      nz, eep_fail_restore

                ; PASS: detail = [scratch, phase=0, off=0].
                ld      hl, 0
                ld      c, 0                    ; phase 0
                ld      a, 0                    ; status 0 = PASS
                jr      eep_report

eep_fail_write:
                ld      c, EEP_PHASE_WRITE
                jr      eep_fail
eep_fail_restore:
                ld      c, EEP_PHASE_RESTORE
eep_fail:
                ld      a, 1                    ; status 1 = FAIL
eep_report:
                ; A = status, C = phase, HL = mismatch offset.
                push    af
                ld      a, EEP_SCRATCH
                ld      (eep_detail+0), a       ; scratch chunk number
                ld      a, c
                ld      (eep_detail+1), a       ; phase
                ld      a, l
                ld      (eep_detail+2), a       ; offset lo
                ld      a, h
                ld      (eep_detail+3), a       ; offset hi
                pop     af                      ; A = status
                ld      de, TEST_ID_EEPROM
                ld      b, 4                    ; detail length
                ld      hl, eep_detail
                call    test_report
                di
                halt

; ---------------------------------------------------------------------------
; eep_fill_pattern — chunk[i] = (i_lo XOR i_hi XOR &A5) for i = 0..1023.
; Position- AND page-sensitive: i_hi steps 0..3 across the four 256-byte pages,
; so a page-stride bug in write_256 (a page written to the wrong address) shows
; up as a verify mismatch rather than passing silently.
; ---------------------------------------------------------------------------
eep_fill_pattern:
                ld      hl, chunk
                ld      bc, 0
efp_loop:
                ld      a, c
                xor     b
                xor     &a5
                ld      (hl), a
                inc     hl
                inc     bc
                ld      a, b
                cp      4                       ; i reached 1024 (B=4, C=0)?
                jr      nz, efp_loop
                ret

; ---------------------------------------------------------------------------
; eep_verify_pattern — compare `chunk` against the pattern.
; Return: Z + A=0 on full match; NZ + HL = first mismatch offset otherwise.
; ---------------------------------------------------------------------------
eep_verify_pattern:
                ld      hl, chunk
                ld      bc, 0
evp_loop:
                ld      a, c
                xor     b
                xor     &a5                     ; expected byte
                cp      (hl)
                jr      nz, evp_bad
                inc     hl
                inc     bc
                ld      a, b
                cp      4
                jr      nz, evp_loop
                xor     a                       ; A=0, Z set: full match
                ret
evp_bad:
                ld      h, b
                ld      l, c                    ; HL = mismatch offset
                or      &ff                     ; A nonzero, NZ
                ret

; ---------------------------------------------------------------------------
; eep_verify_backup — compare `chunk` against EEP_BACKUP.
; Return: Z + A=0 on full match; NZ + HL = first mismatch offset otherwise.
; ---------------------------------------------------------------------------
eep_verify_backup:
                ld      hl, chunk
                ld      de, EEP_BACKUP
                ld      bc, 0
evb_loop:
                ld      a, (de)
                cp      (hl)
                jr      nz, evb_bad
                inc     hl
                inc     de
                inc     bc
                ld      a, b
                cp      4
                jr      nz, evb_loop
                xor     a
                ret
evb_bad:
                ld      h, b
                ld      l, c
                or      &ff
                ret

; ---------------------------------------------------------------------------
; Shared building blocks (same module set + include order as netboot_client).
; ---------------------------------------------------------------------------
                include "build_udp_frame.asm"   ; build_udp_frame + PARAMS/PACKET
                include "encdrv.asm"             ; drv_init/drv_write + wait_ready
                include "eeprom.asm"             ; read_chunk/write_chunk + value/chunk
                include "test_report.asm"        ; test_report + TR_SRC_MAC/TR_*

; ---------------------------------------------------------------------------
; Payload-local storage.
; ---------------------------------------------------------------------------
EEP_BACKUP:       defs 1024         ; snapshot of the scratch chunk's original bytes
eep_detail:       defs 4            ; SATR detail: scratch, phase, off_lo, off_hi
