; csd_probe.asm — the i145a trinload-pushable SD CSD-read probe.
;
; Pushed to the SAM via trinload (page P, offset &8000), it reads the 16-byte CSD
; register of the inserted SD card via a bare CMD9 (SEND_CSD) over the Trinity
; SD-SPI bus and serves it as a single TFTP file "csd.bin" so the host `tftp get`s
; it and decodes the card capacity. The host owns the CSD->capacity decode (the
; refBlocksV1/V2 formulae); the probe serves the RAW 16 CSD bytes — no on-Z80
; decode.
;
; STRUCTURE. This is the i173 dumper (netboot_dumper.asm) re-pointed: instead of
; serving ROM/EEPROM regions, the single served region is the SD card's CSD,
; filled by a NEW SD-SPI driver (the SD analogue of encdrv.asm / eeprom.asm). It
; reuses serve_serve_once + every helper from netboot_serve.asm verbatim (DUMPER
; arms the rrq_hit refresh hook there) and the EEPROM reader from eeprom.asm (for
; the "Trinity Network " config read in probe_main).
;
; SD-SPI DRIVER. Ported faithfully from docs/notes/trinity-sd-z80-interface.md
; (the i193 port authority, which mirrors Colin Piggot's hardware-proven B-DOS
; 1.5t SD driver): §2 the &DC SD select bytes, §3 the &DF SPI byte mechanics
; (busy-poll &DC bit 3 via wait_ready, the one-byte read-lag), §4 the init ladder
; (the &38 microcontroller wake + CMD0/8/55+ACMD41/58/59/9/16), §6 the CMD9 CSD
; read via the &FE data token. The &38 wake poll breaks on sd.in == &FF (the
; OPPOSITE of a CMD0-response poll — §4 step 2 correction). CRCs are fixed; cards
; ignore them in SPI mode except CMD0 (&95) and CMD8 (&87).
;
; VERIFICATION (emulation-first, CLAUDE.md rule 7). The SD read + serve path is
; host-verifiable under the i145c SD-SPI model (sdcard.go), which was VALIDATED by
; running Colin's REAL B-DOS 1.5t init ladder end-to-end against it (i145f). So a
; faithful port of the same protocol reads the configured CSD through the model;
; a bug (wrong poll sense, byte-lag error, wrong command) the model exposes.
; csd_probe_test.go attaches a v2/64GB and a v1 CSD via AttachSD, drives the SD
; read + a bare RRQ for csd.bin through serve_serve_once, and asserts the streamed
; 16 bytes == the configured CSD byte-for-byte. Emulation-verified is NOT
; hardware-verified — the real-card run is i145g (CLAUDE.md §5).

                org     &8000

DUMPER:         equ 1                          ; arm netboot_serve.asm's rrq_hit hook + own-org suppression

                ; Entry: trinload's X packet does `out (HMPR),P; jp &8000`, landing
                ; here. The host harness invokes routines by symbol and never CALLs
                ; &8000, and probe_main is excluded from the host-test build, so this
                ; jp is the hardware/trinload entry only.
                if defined(NETBOOT_HOSTTEST)==0
                jp      probe_main
                endif

; The serve state machine + every helper + CONFIG/STORE/SRC_TABLE. With DUMPER
; defined it does NOT emit its own org/boot-jp/serve_main and DOES `call
; dumper_refresh_region` at rrq_hit. eeprom.asm is included by THIS file (below).
                include "netboot_serve.asm"

; ===========================================================================
; Region geometry. The CSD is a single 16-byte region.
; ===========================================================================
CSD_BYTES:        equ 16                        ; the served file = 16 CSD bytes
STAGE:            equ &C000                    ; the staging buffer (section D) — CSD read target

; SD select bytes on port &DC (trinity-sd-z80-interface.md §2).
SD_PORT:          equ &DC                       ; microcontroller select + status (shared with ENC)
SD_DATA:          equ &DF                       ; SD transparent SPI byte relay
SD_NULLOFF:       equ &04                       ; all-deselect, auto-null off (transaction bracket)
SD_SEL:           equ &31                       ; SD select, manual mode (Z80 supplies its own &FF)
SD_DESEL:         equ &30                       ; SD deselect
SD_INIT:          equ &38                       ; microcontroller "SD init" wake command
SD_AUTONUL:       equ &3F                       ; SD select with auto-null (unused by this probe's ladder)

SD_DATATOK:       equ &FE                       ; start-of-data token preceding the CSD block
SD_IDLE:          equ &FF                       ; SPI-idle MISO (card not driving)

; ===========================================================================
; dumper_refresh_region — the serve-loop hook (called from rrq_hit). The resolved
; filename is at (PARSE_FILENAME); resolve_src has just set SRC_PTR + XFER_SIZE
; from SRC_TABLE (default STAGE / CSD_BYTES). For "csd.bin" we (re)read the card's
; CSD into STAGE, so the serve loop streams a fresh 16-byte CSD. Any other name is
; left as-is (STORE only contains csd.bin, so resolve never hits another name).
; ===========================================================================
dumper_refresh_region:
                ld      hl, (PARSE_FILENAME)
                ld      de, rgn_csd_prefix
                call    dr_streq3              ; CY set if (HL) begins "csd"
                ret     nc                     ; unknown name: leave STAGE as-is

                jp      csd_read_into_stage    ; read the 16-byte CSD into STAGE

; dr_streq3 — does the NUL-terminated string at HL begin with the 3-char prefix at
; DE? Out: CY set if it matches, CY clear otherwise. (Same shape as dumper's.)
dr_streq3:
                ld      b, 3
dr_s3_loop:
                ld      a, (de)
                cp      (hl)
                jr      nz, dr_s3_no
                inc     hl
                inc     de
                djnz    dr_s3_loop
                scf
                ret
dr_s3_no:
                or      a
                ret

rgn_csd_prefix:   defm "csd"

; ===========================================================================
; SD-SPI primitives. The Trinity microcontroller's busy bit (&DC bit 3) gates
; every byte; wait_ready (encdrv.asm, shared on &DC) is the busy-poll. The probe
; runs the whole ladder in manual select (&31): the Z80 clocks each byte with an
; explicit OUT (&DF) and reads with the one-byte lag.
; ===========================================================================

; sd_out — SPI write byte (manual mode): wait for not-busy, then OUT (&DF),A.
; In: A = byte. Clobbers nothing of interest (A preserved on exit not guaranteed).
sd_out:
                push    af
                call    wait_ready             ; encdrv.asm: IN(&DC); AND 8; loop while busy
                pop     af
                out     (SD_DATA), a
                ret

; sd_in — SPI read byte with the one-byte lag (manual mode): write a dummy &FF
; (sd_out), then IN A,(&DF) reads the byte the dummy clocked in.
; Out: A = byte read. Clobbers F.
sd_in:
                ld      a, SD_IDLE
                call    sd_out                 ; wait + OUT (&DF),&FF
                call    wait_ready
                in      a, (SD_DATA)
                ret

; ===========================================================================
; sd_cmd — send a 6-byte SPI command frame (opcode|0x40, 4 arg bytes, CRC) under
; the &31 manual select, then poll R1 (the card's response: bit7 clear when valid)
; up to 256 reads. In:
;   A  = command opcode already OR'd with &40 (e.g. CMD0 = &40, CMD9 = &49)
;   BC = argument high word (B = bits 31..24, C = bits 23..16)
;   DE = argument low  word (D = bits 15..8,  E = bits 7..0)
;   (the CRC byte is taken from sd_cmd_crc — set it before calling)
; Out: A = R1; CY set if R1 never went valid (timed out). Clobbers AF/B (R1 poll).
; The card is left selected (caller deselects or reads the data phase).
; ===========================================================================
sd_cmd_crc:       defb &FF                      ; CRC byte for the pending command

sd_cmd:
                push    bc
                push    de
                call    sd_out                 ; opcode (A) — command-frame start byte (b7:6 == 01)
                pop     de
                pop     bc
                ld      a, b
                call    sd_out                 ; arg[31:24]
                ld      a, c
                call    sd_out                 ; arg[23:16]
                ld      a, d
                call    sd_out                 ; arg[15:8]
                ld      a, e
                call    sd_out                 ; arg[7:0]
                ld      a, (sd_cmd_crc)
                call    sd_out                 ; CRC

                ; poll R1: read until bit7 clears (a valid response), up to 256 tries.
                ld      b, 0                    ; 256 iterations (djnz wraps 0->255..)
sd_cmd_r1:
                call    sd_in
                bit     7, a
                jr      z, sd_cmd_ok           ; bit7 clear => valid R1 in A
                djnz    sd_cmd_r1
                scf                             ; timed out
                ret
sd_cmd_ok:
                or      a                       ; CY clear; A = R1
                ret

; sd_cmd0arg — convenience: send a command with a zero argument and fixed CRC.
; In: A = opcode|&40, and sd_cmd_crc preset. Out: as sd_cmd.
sd_cmd0arg:
                ld      bc, 0
                ld      de, 0
                jr      sd_cmd

; ===========================================================================
; csd_read_into_stage — run the SD init ladder (faithful to §4) and read the
; 16-byte CSD via CMD9 into STAGE[0..15]. SRC_PTR/XFER_SIZE default to
; STAGE/CSD_BYTES, so the serve loop streams STAGE after this returns.
;
; The whole transaction is bracketed &04 .. &04 and runs under DI (no ESC window
; inside SD I/O, matching the fork). On any timeout the ladder still proceeds —
; a v1 card rejects CMD8 (R1 illegal-command) and that is normal; CMD58 returns a
; CCS-clear OCR; the CSD decode is the host's job. We just need CMD9 to deliver
; the 16 CSD bytes.
; ===========================================================================
csd_read_into_stage:
                di

                ; --- &04 all-deselect bracket (auto-null off) -------------
                ld      a, SD_NULLOFF
                out     (SD_PORT), a

                ; --- microcontroller wake (&38) + the &FF settle poll -----
                ; &31 select, &38 SD-init (B = the MMC/SD reply count), &31 reselect,
                ; then poll sd_in until it returns &FF (the card settling to SPI-idle
                ; MISO-high). This is the OPPOSITE sense to a CMD0-response poll
                ; (trinity-sd-z80-interface.md §4 step 2 correction): waiting for
                ; != &FF here HANGS. We bound the poll so a model/card that never
                ; reaches &FF cannot wedge the probe.
                ld      a, SD_SEL
                out     (SD_PORT), a
                ld      a, SD_INIT
                out     (SD_PORT), a            ; microcontroller SD-init wake
                ld      a, SD_SEL
                out     (SD_PORT), a            ; reselect manual
                ld      b, 0                    ; up to 256 settle reads
csd_wake_poll:
                call    sd_in
                inc     a                       ; &FF -> 0 (Z set) when settled
                jr      z, csd_wake_done
                djnz    csd_wake_poll
csd_wake_done:

                ; --- CMD0 GO_IDLE_STATE: opcode &40, CRC &95, arg 0 -------
                ld      a, &95
                ld      (sd_cmd_crc), a
                ld      a, &40
                call    sd_cmd0arg             ; expect R1 = 1 (idle)

                ; --- CMD8 SEND_IF_COND: opcode &48, CRC &87, arg &000001AA -
                ; SDv2 echoes the check pattern in a 4-byte R7 trailer; a v1 card
                ; rejects it (R1 illegal-command). We read the R7 trailer when the
                ; command was accepted (R1 bit2 clear) so the stream stays in phase;
                ; the host owns the v1/v2 decision via the CSD, so we don't branch on
                ; the result here — we just keep the SPI stream aligned.
                ld      a, &87
                ld      (sd_cmd_crc), a
                ld      bc, &0000
                ld      de, &01AA
                ld      a, &48
                call    sd_cmd                  ; A = R1
                bit     2, a                    ; illegal-command (v1 card)?
                jr      nz, csd_after_cmd8      ; v1: no R7 trailer to consume
                call    sd_in                   ; consume the 4 R7 bytes
                call    sd_in
                call    sd_in
                call    sd_in
csd_after_cmd8:

                ; --- CMD55 + ACMD41 (HCS): init until R1 bit0 (idle) clears -
                ; CRC is ignored for these; send &FF. ACMD41 arg carries HCS
                ; (&40000000) to advertise SDHC support. Loop until ACMD41 R1 == 0
                ; (init complete), bounded so a non-responding card cannot wedge.
                ld      a, &FF
                ld      (sd_cmd_crc), a
                ld      bc, 0                    ; B=0 -> 256 ACMD41 attempts (ample for the model)
csd_acmd41_loop:
                push    bc
                ld      a, &FF
                ld      (sd_cmd_crc), a
                ld      a, &77                  ; CMD55 APP_CMD
                call    sd_cmd0arg
                ld      a, &69                  ; ACMD41 SD_SEND_OP_COND
                ld      bc, &4000               ; HCS set (arg bits 31..16 = &4000)
                ld      de, &0000
                call    sd_cmd                  ; A = R1
                pop     bc
                or      a                       ; R1 == 0 -> init complete (idle bit clear)
                jr      z, csd_acmd41_done
                djnz    csd_acmd41_loop
csd_acmd41_done:

                ; --- CMD58 READ_OCR: opcode &7A (read the CCS addressing bit) ---
                ; The host decodes capacity from the CSD; CMD58 here just keeps the
                ; ladder faithful (and the SPI stream in phase). R1 then 4 OCR bytes.
                ld      a, &7A
                call    sd_cmd0arg
                call    sd_in                   ; OCR[31:24]
                call    sd_in                   ; OCR[23:16]
                call    sd_in                   ; OCR[15:8]
                call    sd_in                   ; OCR[7:0]

                ; --- CMD59 CRC_ON_OFF: opcode &7B (turn CRC off) ----------
                ld      a, &7B
                call    sd_cmd0arg

                ; --- CMD9 SEND_CSD: opcode &49 -> &FE token + 16 CSD + 2 CRC ---
                ld      a, &49
                call    sd_cmd0arg             ; A = R1 (expect 0)

                ; poll for the &FE start-of-data token (up to 256 reads).
                ld      b, 0
csd_tok_poll:
                call    sd_in
                cp      SD_DATATOK
                jr      z, csd_tok_found
                djnz    csd_tok_poll
                jp      csd_deselect           ; no token: leave STAGE unchanged, deselect
csd_tok_found:
                ; read 16 CSD bytes into STAGE, then 2 CRC bytes (discarded).
                ld      hl, STAGE
                ld      b, CSD_BYTES
csd_read_loop:
                call    sd_in
                ld      (hl), a
                inc     hl
                djnz    csd_read_loop
                call    sd_in                   ; CRC byte 1 (discard)
                call    sd_in                   ; CRC byte 2 (discard)

                ; --- CMD16 SET_BLOCKLEN 512: opcode &50, arg 512 ----------
                ; Not load-bearing for the CSD read, but part of the faithful ladder.
                ld      bc, &0000
                ld      de, &0200               ; 512
                ld      a, &50
                call    sd_cmd

csd_deselect:
                ; --- deselect tail: &30 then &04 (auto-null off / all-deselect) ---
                ld      a, SD_DESEL
                out     (SD_PORT), a
                ld      a, SD_NULLOFF
                out     (SD_PORT), a
                ei

                ; SRC_PTR/XFER_SIZE default to STAGE/CSD_BYTES; nothing to override.
                ret

; ===========================================================================
; Bootable / trinload entry (excluded from the host harness build). probe_main
; reads the SAM's MAC/IP from the "Trinity Network " flash chunk, provisions the
; csd.bin STORE/SRC_TABLE, reads the CSD once into STAGE, inits the ENC28J60, then
; loops serve_serve_once with an Esc-to-RET exit so it can be re-pushed. Mirrors
; dumper_main.
; ===========================================================================
                if defined(NETBOOT_HOSTTEST)==0

probe_main:
                di
                ; --- locate + read the "Trinity Network " flash chunk -----
                ld      a, 1
                ld      (part), a
                ld      (total), a
                ld      hl, pm_chunk_name
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index
                ld      a, (value)
                and     a
                jp      z, pm_fail_cfg
                call    read_chunk
                ld      a, (value)
                and     a
                jp      z, pm_fail_cfg

                ; copy sam_mac (chunk+0) / sam_ip (chunk+6) into CONFIG.
                ld      hl, chunk + 0
                ld      de, CONFIG_SERVERMAC
                ld      bc, 6
                ldir
                ld      hl, chunk + 6
                ld      de, CONFIG_SERVERIP
                ld      bc, 4
                ldir

                ; fixed transfer source TID (an ephemeral high port), big-endian.
                ld      a, 40136 >> 8
                ld      (CONFIG_SERVERTID), a
                ld      a, 40136 & &ff
                ld      (CONFIG_SERVERTID + 1), a

                xor     a
                ld      (XFER_ACTIVE), a
                ld      (XFER_JUST_OACKED), a

                ; --- provision the csd.bin STORE + SRC_TABLE --------------
                call    probe_provision

                ; --- read the CSD once at startup (also re-read per RRQ) ---
                call    csd_read_into_stage

                ; --- init the ENC28J60 with the SAM's real MAC ------------
                ld      hl, CONFIG_SERVERMAC
                call    drv_init
                ld      a, b
                or      c
                jp      z, pm_fail_init

pm_serve_loop:
                ; Esc-to-exit (trinload.asm): poll the keyboard; on Esc, RET to
                ; trinload's start (it pushed start as our return addr).
                ld      a, &f7
                in      a, (&f9)
                bit     5, a                   ; Esc pressed?
                ret     z                      ; -> trinload's start

                call    serve_serve_once
                jr      pm_serve_loop

pm_fail_cfg:
                ld      a, 2                   ; red border: no/bad network settings
                out     (&fe), a
                di
                halt
pm_fail_init:
                ld      a, 1                   ; blue border: ENC28J60 init failed
                out     (&fe), a
                di
                halt

; probe_provision — copy the csd.bin STORE + SRC_TABLE templates into the live
; tables resolve + resolve_src walk.
probe_provision:
                ld      hl, probe_store_tmpl
                ld      de, STORE
                ld      bc, probe_store_tmpl_end - probe_store_tmpl
                ldir
                ld      hl, probe_src_tmpl
                ld      de, SRC_TABLE
                ld      bc, probe_src_tmpl_end - probe_src_tmpl
                ldir
                ret

pm_chunk_name:    defm "Trinity Network "     ; the flash chunk holding MAC+IP

                endif  ; !NETBOOT_HOSTTEST

; ===========================================================================
; csd.bin STORE + SRC_TABLE templates (mirror dumper's). Both are needed in the
; host-test build too: csd_probe_test.go provisions them directly so resolve /
; resolve_src match the region name.
;   STORE:     name\0 | 4-byte LE size, then a 0 sentinel.
;   SRC_TABLE: name\0 | 2-byte LE source ptr | 4-byte LE size, then a 0 sentinel.
; ===========================================================================
probe_store_tmpl:
                  defm "csd.bin"
                  defb 0
                  defw CSD_BYTES
                  defw 0                        ; size high word
                  defb 0                        ; end-of-store sentinel
probe_store_tmpl_end:

probe_src_tmpl:
                  defm "csd.bin"
                  defb 0
                  defw STAGE
                  defw CSD_BYTES
                  defw 0                        ; size high word
                  defb 0                        ; end-of-table sentinel
probe_src_tmpl_end:

; ===========================================================================
; The Trinity flash reader (eeprom.asm). netboot_serve.asm suppresses its own
; eeprom.asm include under DUMPER, so the probe owns it — and includes it in EVERY
; build (host-test too): find_index / read_chunk + the value/chunk/name/part/total
; storage all come from here.
; ===========================================================================
                include "eeprom.asm"
