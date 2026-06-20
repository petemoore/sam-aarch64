; sd_csd.asm — read the SD card's CSD register and derive BD_RECORDS (the card's
; total B-DOS record count) from it, at netboot serve/client startup.
;
; THE GAP THIS CLOSES (i145b). BD_RECORDS (bdos_seam.asm) is consumed by
; bdos_find_free_record / the picker but was only ever INJECTED by host tests; no
; production Z80 code wrote it, so on real hardware it was 0 -> "no free record"
; -> every push rejected. This module computes it from the inserted card.
;
; PROVENANCE / AUTHORITY.
;   - SD-SPI driver: the i145a probe (csd_probe.asm), itself a faithful port of
;     Colin Piggot's hardware-proven B-DOS 1.5t SD driver via
;     docs/notes/trinity-sd-z80-interface.md §2-§4,§6. The init ladder (&38 wake,
;     CMD0/8/55+ACMD41/58/59/9, then CMD9 SEND_CSD) is reproduced here, all bounded
;     so a missing/odd card cannot wedge the boot.
;   - CSD -> block count -> records decode: trinity-sd-z80-interface.md §6 and the
;     Go reference refBlocksV2 / refBlocksV1 / refRecords in
;     tools/netboot-oracle/z80/csd_decode_colin_test.go, which i145e proved match
;     Colin's REAL code byte-for-byte (incl. the 16-bit BD_RECORDS wrap, q40).
;
; FAITHFUL 16-BIT BD_RECORDS (q40). Colin stores BD_RECORDS as a 16-bit word that
; WRAPS for cards above ~51 GB (records mod 2^16). We match that: BD_RECORDS is a
; 2-byte word and we store the low 16 bits of the records count. No 32-bit widen.
;
; CARD-INIT CHOICE. The full standalone init ladder (the probe's) is run, not a
; bare CMD9: netboot is a minimal environment where the card is NOT assumed
; B-DOS-initialised, so the safe default is the proven full ladder.
;
; EMULATION. Host-verifiable under the i145c SD model (sdcard.go), validated end
; to end against Colin's real ladder (i145f). csd_to_bd_records_test.go attaches a
; configured CSD and asserts BD_RECORDS is COMPUTED (not injected) and matches the
; Go reference. Emulation-verified is not hardware-verified (CLAUDE.md §5).
;
; Included by netboot_serve.asm / netboot_client.asm. Depends on wait_ready
; (encdrv.asm) and BD_RECORDS (bdos_seam.asm) — the including file pulls those in.

SDC_PORT:         equ &DC                       ; microcontroller select + status
SDC_DATA:         equ &DF                       ; SD transparent SPI byte relay
SDC_NULLOFF:      equ &04                       ; all-deselect, auto-null off
SDC_SEL:          equ &31                       ; SD select, manual mode
SDC_DESEL:        equ &30                       ; SD deselect
SDC_INIT:         equ &38                       ; microcontroller "SD init" wake
SDC_DATATOK:      equ &FE                       ; start-of-data token preceding the CSD
SDC_IDLE:         equ &FF                       ; SPI-idle MISO

; The 16-byte CSD read target. This module ships as a section-D overlay (i145b-b2),
; so CSD_STAGE lives wherever the module lands in the boot image (section C or D);
; both are RAM at boot, so the read/decode write here freely.
CSD_STAGE:        defs 16

; ===========================================================================
; csd_set_bd_records — the i145b startup entry. Read the CSD, decode it, store the
; card's record count into BD_RECORDS. On read failure BD_RECORDS stays 0 (safe
; decline). Called once from serve_main / client_main after the EEPROM config read.
; ===========================================================================
csd_set_bd_records:
                call    csd_read_into_stage    ; CSD_STAGE <- card CSD (CY set on failure)
                ret     c
                call    csd_decode_blocks      ; csd_blocks <- 32-bit block count
                call    csd_blocks_to_records  ; csd_records <- 16-bit record count
                ld      hl, (csd_records)
                ld      (BD_RECORDS), hl
                ret

; ===========================================================================
; SD-SPI primitives (manual &31 select). wait_ready (encdrv.asm) is the shared &DC
; bit-3 busy poll.
; ===========================================================================

; sdc_out — wait for not-busy, then OUT (&DF),A.
sdc_out:
                push    af
                call    wait_ready
                pop     af
                out     (SDC_DATA), a
                ret

; sdc_in — SPI read byte (one-byte lag): dummy &FF OUT, then IN (&DF). Out: A.
sdc_in:
                ld      a, SDC_IDLE
                call    sdc_out
                call    wait_ready
                in      a, (SDC_DATA)
                ret

; sdc_cmd — send the 6-byte command frame, poll R1 (bit7 clear = valid) <=256.
; In: A=opcode|&40, BC=arg[31:16], DE=arg[15:0], (sdc_cmd_crc) preset.
; Out: A=R1; CY set on timeout. Card left selected.
sdc_cmd_crc:      defb &FF
sdc_cmd:
                push    bc
                push    de
                call    sdc_out                ; opcode
                pop     de
                pop     bc
                ld      a, b
                call    sdc_out
                ld      a, c
                call    sdc_out
                ld      a, d
                call    sdc_out
                ld      a, e
                call    sdc_out
                ld      a, (sdc_cmd_crc)
                call    sdc_out                ; CRC
                ld      b, 0
sdc_cmd_r1:
                call    sdc_in
                bit     7, a
                jr      z, sdc_cmd_ok
                djnz    sdc_cmd_r1
                scf
                ret
sdc_cmd_ok:
                or      a
                ret

; sdc_cmd0arg — send a command with a zero argument and the preset CRC.
sdc_cmd0arg:
                ld      bc, 0
                ld      d, b
                ld      e, b
                jr      sdc_cmd

; sdc_set_crc — store A into the pending command CRC. (Factored to a CALL: saves
; the repeated `ld (sdc_cmd_crc),a` store sites.)
sdc_set_crc:
                ld      (sdc_cmd_crc), a
                ret

; ===========================================================================
; csd_read_into_stage — run the init ladder and read 16 CSD bytes via CMD9 into
; CSD_STAGE. Out: CY set if the CSD could not be read, CY clear on success. Under
; DI, bracketed &04..&04.
; ===========================================================================
csd_read_into_stage:
                di
                ld      a, SDC_NULLOFF
                out     (SDC_PORT), a
                ; microcontroller wake (&38) + &FF settle poll
                ld      a, SDC_SEL
                out     (SDC_PORT), a
                ld      a, SDC_INIT
                out     (SDC_PORT), a
                ld      a, SDC_SEL
                out     (SDC_PORT), a
                ld      b, 0
csdr_wake_poll:
                call    sdc_in
                inc     a                       ; &FF -> 0 (Z) when settled
                jr      z, csdr_wake_done
                djnz    csdr_wake_poll
csdr_wake_done:
                ; CMD0 GO_IDLE_STATE (CRC &95)
                ld      a, &95
                call    sdc_set_crc
                ld      a, &40
                call    sdc_cmd0arg
                ; CMD8 SEND_IF_COND (CRC &87, arg &000001AA)
                ld      a, &87
                call    sdc_set_crc
                ld      bc, &0000
                ld      de, &01AA
                ld      a, &48
                call    sdc_cmd
                bit     2, a                    ; illegal-command (v1)?
                jr      nz, csdr_after_cmd8
                call    sdc_in                  ; consume the 4 R7 bytes
                call    sdc_in
                call    sdc_in
                call    sdc_in
csdr_after_cmd8:
                ; CMD55 + ACMD41 (HCS) until R1 == 0
                ld      a, &FF
                call    sdc_set_crc
                ld      b, 0
csdr_acmd41_loop:
                push    bc
                ld      a, &77                  ; CMD55 APP_CMD
                call    sdc_cmd0arg
                ld      a, &69                  ; ACMD41 SD_SEND_OP_COND
                ld      bc, &4000               ; HCS set
                ld      de, &0000
                call    sdc_cmd
                pop     bc
                or      a
                jr      z, csdr_acmd41_done
                djnz    csdr_acmd41_loop
csdr_acmd41_done:
                ; CMD58 READ_OCR (4 OCR bytes) — keeps the stream in phase
                ld      a, &7A
                call    sdc_cmd0arg
                call    sdc_in
                call    sdc_in
                call    sdc_in
                call    sdc_in
                ; CMD59 CRC_ON_OFF
                ld      a, &7B
                call    sdc_cmd0arg
                ; CMD9 SEND_CSD: poll for the &FE token, read 16 CSD + 2 CRC
                ld      a, &49
                call    sdc_cmd0arg
                ld      b, 0
csdr_tok_poll:
                call    sdc_in
                cp      SDC_DATATOK
                jr      z, csdr_tok_found
                djnz    csdr_tok_poll
                scf                             ; no token -> read failure
                jr      csdr_deselect
csdr_tok_found:
                ld      hl, CSD_STAGE
                ld      b, 16
csdr_read_loop:
                call    sdc_in
                ld      (hl), a
                inc     hl
                djnz    csdr_read_loop
                call    sdc_in                  ; 2 CRC bytes (discard)
                call    sdc_in
                or      a                       ; CY clear: success
csdr_deselect:
                push    af                      ; preserve success/failure CY
                ld      a, SDC_DESEL
                out     (SDC_PORT), a
                ld      a, SDC_NULLOFF
                out     (SDC_PORT), a
                ei
                pop     af
                ret

; ===========================================================================
; csd_decode_blocks — decode CSD_STAGE into the 32-bit (LE) csd_blocks.
; §6 / refBlocksV2 / refBlocksV1.
;   v2 (CSD[0]&&C0 == &40): cSize = (CSD[7]&&3F)<<16 | CSD[8]<<8 | CSD[9];
;       blocks = (cSize+1) << 10.
;   v1 (else): READ_BL_LEN = CSD[5]&&0F;
;       cSize = (CSD[6]&&03)<<10 | CSD[7]<<2 | (CSD[8]>>6);
;       cSizeMult = (CSD[9]&&03)<<1 | (CSD[10]>>7);
;       blocks = (cSize+1) << (cSizeMult+2) << READ_BL_LEN >> 9.
; The (cSize+1) seed is placed in csd_blocks (low) with high word zeroed, then the
; net shift is applied via csd_blocks_shift (B = count, C = direction).
; Clobbers AF/BC/DE/HL.
; ===========================================================================
csd_decode_blocks:
                ; zero the 32-bit accumulator high word up front (both paths).
                ld      hl, 0
                ld      (csd_blocks + 2), hl
                ld      a, (CSD_STAGE)
                and     &C0
                cp      &40
                jr      z, csdb_v2

                ; --- v1 -------------------------------------------------
                ; cSize = (CSD[6]&3)<<10 | CSD[7]<<2 | (CSD[8]>>6)
                ld      a, (CSD_STAGE + 6)
                and     &03
                ld      h, a
                ld      a, (CSD_STAGE + 7)
                ld      l, a
                add     hl, hl
                add     hl, hl                  ; ((CSD[6]&3)<<10)|(CSD[7]<<2)
                ld      a, (CSD_STAGE + 8)
                rlca
                rlca
                and     &03                     ; CSD[8]>>6
                or      l
                ld      l, a
                inc     hl                      ; cSize + 1
                ld      (csd_blocks), hl
                ; net shift = (cSizeMult+2) + READ_BL_LEN - 9, then 0 right shifts.
                ; cSizeMult = (CSD[9]&3)<<1 | (CSD[10]>>7)
                ld      a, (CSD_STAGE + 9)
                and     &03
                add     a, a
                ld      c, a
                ld      a, (CSD_STAGE + 10)
                rlca
                and     &01
                or      c                       ; cSizeMult
                add     a, 2                     ; +2
                ld      c, a
                ld      a, (CSD_STAGE + 5)
                and     &0F                     ; READ_BL_LEN
                add     a, c                     ; total left shift
                ; left-shift by A, then right-shift by 9 (the /512).
                ld      b, a
                ld      c, 0                    ; direction: left
                call    csd_blocks_shift
                ld      b, 9
                ld      c, 1                    ; direction: right
                jp      csd_blocks_shift

csdb_v2:
                ; cSize = (CSD[7]&3F)<<16 | CSD[8]<<8 | CSD[9]   (22-bit)
                ld      a, (CSD_STAGE + 9)
                ld      l, a
                ld      a, (CSD_STAGE + 8)
                ld      h, a
                ld      a, (CSD_STAGE + 7)
                and     &3F
                ld      (csd_blocks + 2), a     ; high byte (bits 21..16); +3 stays 0
                inc     hl                      ; (cSize+1) low 16 bits (INC HL: no flags)
                ld      (csd_blocks), hl
                ld      a, h                    ; HL == 0 -> the +1 carried into byte 2
                or      l
                jr      nz, csdb_v2_shift
                ld      hl, csd_blocks + 2
                inc     (hl)
csdb_v2_shift:
                ld      b, 10
                ld      c, 0                    ; left
                jp      csd_blocks_shift

; csd_blocks_shift — shift the 32-bit LE csd_blocks by B bits. C=0 left, C!=0 right.
; B may be 0 (no-op). Clobbers AF/B/HL.
csd_blocks_shift:
                inc     b
                jr      cbs_test
cbs_step:
                bit     0, c
                jr      nz, cbs_right
                ; left: low byte first, CY chains up
                ld      hl, csd_blocks
                or      a
                rl      (hl)
                inc     hl
                rl      (hl)
                inc     hl
                rl      (hl)
                inc     hl
                rl      (hl)
                jr      cbs_test
cbs_right:
                ld      hl, csd_blocks + 3
                or      a
                rr      (hl)
                dec     hl
                rr      (hl)
                dec     hl
                rr      (hl)
                dec     hl
                rr      (hl)
cbs_test:
                djnz    cbs_step
                ret

; ===========================================================================
; csd_blocks_to_records — the §6 records math on csd_blocks, storing the 16-bit
; (wrapping) record count into csd_records. Mirrors refRecords:
;   records1 = blocks / 1600
;   base     = (records1 + 32)/32 + 1
;   usable   = blocks - base
;   records  = usable / 1600  (+1 if (usable % 1600) >= 50)   [low 16 bits stored]
; Clobbers AF/BC/DE/HL.
; ===========================================================================
csd_blocks_to_records:
                ; records1 = blocks / 1600 (32-bit quotient in csd_div_n)
                call    csd_load_dividend
                ld      bc, 1600
                call    csd_div32              ; csd_div_n = quotient, HL = remainder
                ; base = (records1 + 32)/32 + 1. records1 can exceed 16 bits for a
                ; large card, so the +32 and /32 are done on the 32-bit csd_div_n.
                ld      hl, (csd_div_n)
                ld      de, 32
                add     hl, de
                ld      (csd_div_n), hl
                jr      nc, csdr_base_shr
                ld      hl, (csd_div_n + 2)    ; carry into the high word
                inc     hl
                ld      (csd_div_n + 2), hl
csdr_base_shr:
                ; csd_div_n >>= 5 (the /32), 32-bit
                ld      b, 5
csdr_base_shr_loop:
                ld      hl, csd_div_n + 3
                or      a
                rr      (hl)
                dec     hl
                rr      (hl)
                dec     hl
                rr      (hl)
                dec     hl
                rr      (hl)
                djnz    csdr_base_shr_loop
                ld      hl, (csd_div_n)        ; low 16 bits of the list size
                inc     hl                     ; base = listSize + 1
                ld      (csd_base), hl
                ; usable = blocks - base
                call    csd_load_dividend
                ld      de, (csd_base)
                ld      hl, (csd_div_n)
                or      a
                sbc     hl, de
                ld      (csd_div_n), hl
                jr      nc, csdr_usable_hi
                ld      hl, (csd_div_n + 2)
                dec     hl
                ld      (csd_div_n + 2), hl
csdr_usable_hi:
                ; records = usable / 1600, +1 if remainder >= 50
                ld      bc, 1600
                call    csd_div32
                ld      de, 50
                or      a
                sbc     hl, de                  ; CY clear => remainder >= 50
                jr      c, csdr_no_roundup
                ld      hl, (csd_div_n)
                inc     hl
                ld      (csd_div_n), hl
csdr_no_roundup:
                ; BD_RECORDS = low 16 bits of the quotient (q40 16-bit wrap).
                ld      hl, (csd_div_n)
                ld      (csd_records), hl
                ret

; csd_load_dividend — csd_div_n <- csd_blocks (32-bit). Clobbers AF/HL.
csd_load_dividend:
                ld      hl, (csd_blocks)
                ld      (csd_div_n), hl
                ld      hl, (csd_blocks + 2)
                ld      (csd_div_n + 2), hl
                ret

; ===========================================================================
; csd_div32 — 32-bit / 16-bit restoring divide. In: csd_div_n = 32-bit LE dividend,
; BC = 16-bit divisor. Out: csd_div_n = 32-bit quotient (LE), HL = 16-bit
; remainder. 32 iterations, in-place: the dividend shifts left out of csd_div_n
; while the quotient shifts in at the low end. Clobbers AF/BC/DE/HL.
; ===========================================================================
csd_div32:
                ld      hl, 0                   ; remainder
                ld      a, 32
                ld      (csd_div_cnt), a
csd_div_loop:
                push    bc
                ld      bc, (csd_div_n)
                ld      de, (csd_div_n + 2)
                sla     c
                rl      b
                rl      e
                rl      d                       ; CY = bit shifted out of bit31
                ld      (csd_div_n), bc
                ld      (csd_div_n + 2), de
                pop     bc
                adc     hl, hl                  ; remainder<<1 | shifted-out bit
                or      a
                sbc     hl, bc
                jr      c, csd_div_restore
                ld      a, (csd_div_n)
                or      1                       ; set quotient bit0
                ld      (csd_div_n), a
                jr      csd_div_next
csd_div_restore:
                add     hl, bc                  ; restore remainder
csd_div_next:
                ld      a, (csd_div_cnt)
                dec     a
                ld      (csd_div_cnt), a
                jr      nz, csd_div_loop
                ret

; --- decode/divide scratch ---------------------------------------------------
csd_blocks:       defs 4                  ; 32-bit LE block count
csd_records:      defs 2                  ; 16-bit (wrapping) record count -> BD_RECORDS
csd_base:         defs 2                  ; record-list base sector + 1
csd_div_n:        defs 4                  ; csd_div32 dividend / quotient
csd_div_cnt:      defs 1                  ; csd_div32 iteration counter
