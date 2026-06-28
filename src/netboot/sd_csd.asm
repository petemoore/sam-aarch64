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
; Included by netboot_serve.asm / netboot_client.asm. The SD byte path uses this
; module's own bounded sdc_wait_ready (not the vendored encdrv.asm wait_ready);
; depends on BD_RECORDS (bdos_seam.asm), which the including file pulls in.

SDC_PORT:         equ &DC                       ; microcontroller select + status
SDC_DATA:         equ &DF                       ; SD transparent SPI byte relay
SDC_NULLOFF:      equ &04                       ; all-deselect, auto-null off
SDC_SEL:          equ &31                       ; SD select, manual mode
SDC_DESEL:        equ &30                       ; SD deselect
SDC_INIT:         equ &38                       ; microcontroller "SD init" wake
SDC_DATATOK:      equ &FE                       ; start-of-data token preceding the CSD
SDC_IDLE:         equ &FF                       ; SPI-idle MISO
SDC_BUSY_LIMIT:   equ &FFFF                     ; busy-wait poll budget (~65535, ~0.5s) before sdc_wait_ready gives up

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
; SD-SPI primitives (manual &31 select). sdc_wait_ready (below) is the BOUNDED &DC
; bit-3 busy poll — NOT the vendored unbounded wait_ready (encdrv.asm), which spins
; `JR wait_ready` forever on a stuck Trinity and hung the SAM (the 2026-06-24
; 3-restart hang). Fix #5 (hardware-readiness-audit.md): bound this shared path so a
; wedged controller can't wedge serve_main/client_main. The vendored ENC-path
; wait_ready stays untouched (its uses are proven).
; ===========================================================================

; sdc_out — wait for not-busy (bounded), then OUT (&DF),A.
sdc_out:
                push    af
                call    sdc_wait_ready
                pop     af
                out     (SDC_DATA), a
                ret

; sdc_in — SPI read byte (one-byte lag): dummy &FF OUT, then IN (&DF). Out: A.
sdc_in:
                ld      a, SDC_IDLE
                call    sdc_out
                call    sdc_wait_ready
                in      a, (SDC_DATA)
                ret

; sdc_wait_ready — bounded replacement for the vendored (unbounded) wait_ready, used
; ONLY on the SD byte path (sdc_out/sdc_in). Waits for the Trinity busy bit (&DC bit
; 3) to clear, but gives up after SDC_BUSY_LIMIT polls and sets sdc_timed_out
; (sticky) — so a stuck card/controller can NEVER hang serve_main/client_main. Once
; tripped, returns immediately so the rest of the already-bounded ladder unwinds
; fast; sdc_init_ladder clears the flag at the start of every transaction. Preserves
; BC/DE/HL; clobbers AF (sdc_out brackets its data byte with push/pop af, and sdc_in
; reloads A from SDC_DATA after). Faithful port of the gold-standard csd_probe.asm
; sd_wait_ready (i241/fix #5).
sdc_wait_ready:
                ld      a, (sdc_timed_out)
                or      a
                ret     nz                     ; already gave up — don't wait again
                push    bc
                ld      bc, SDC_BUSY_LIMIT
scwr_loop:      in      a, (SDC_PORT)
                and     %00001000              ; busy?
                jr      z, scwr_ready          ; clear -> proceed
                dec     bc
                ld      a, b
                or      c
                jr      nz, scwr_loop
                ld      a, 1
                ld      (sdc_timed_out), a     ; budget exhausted -> sticky abort
scwr_ready:     pop     bc
                ret

sdc_timed_out:  defb    0                      ; set once an SD busy-wait exhausts its budget

; sdc_cmd — send the 6-byte command frame, poll R1 (bit7 clear = valid) <=256.
; In: A=opcode|&40, BC=arg[31:16], DE=arg[15:0], (sdc_cmd_crc) preset.
; Out: A=R1; CY set on timeout. Card left selected.
sdc_cmd_crc:      defb &FF
sdc_cmd:
                push    af                     ; save opcode
                ld      a, &FF
                call    sdc_out                ; LEADING FLUSH — the post-select Ncc sync byte every
                pop     af                     ; command needs (trinity-sd-z80-interface.md §5 a82b;
                                               ; Colin sd.cmd &A7FA). Without it a real card never frames
                                               ; the command. The koron-go model can't see this (it frames
                                               ; by bit-pattern, not clock count); the i145g hardware CSD
                                               ; read came back all-zeros until this flush was added.
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
                call    sdc_init_ladder        ; wake + CMD0/8/41/58/59; card selected
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
                jp      sdc_deselect
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
                jp      sdc_deselect

; ===========================================================================
; sdc_init_ladder — DI, wake the microcontroller (&38 + &FF settle poll), then run
; the standalone init ladder CMD0/8/55+ACMD41/58/59. Leaves the card SELECTED and
; ready for a data command (CMD9 SEND_CSD or CMD17 READ_SINGLE_BLOCK). The caller
; issues its data command, then sdc_deselect undoes the select + EI. Every loop is
; bounded so a missing/odd card cannot wedge the boot. Clobbers AF/BC/DE/HL.
; ===========================================================================
sdc_init_ladder:
                di
                xor     a
                ld      (sdc_timed_out), a      ; fresh transaction — clear the sticky busy-timeout flag
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
sdil_wake_poll:
                call    sdc_in
                inc     a                       ; &FF -> 0 (Z) when settled
                jr      z, sdil_wake_done
                djnz    sdil_wake_poll
sdil_wake_done:
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
                jr      nz, sdil_after_cmd8
                call    sdc_in                  ; consume the 4 R7 bytes
                call    sdc_in
                call    sdc_in
                call    sdc_in
sdil_after_cmd8:
                ; CMD55 + ACMD41 (HCS) until R1 == 0
                ld      a, &FF
                call    sdc_set_crc
                ld      b, 0
sdil_acmd41_loop:
                push    bc
                ld      a, &77                  ; CMD55 APP_CMD
                call    sdc_cmd0arg
                ld      a, &69                  ; ACMD41 SD_SEND_OP_COND
                ld      bc, &4000               ; HCS set
                ld      de, &0000
                call    sdc_cmd
                pop     bc
                or      a
                jr      z, sdil_acmd41_done
                djnz    sdil_acmd41_loop
sdil_acmd41_done:
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
                ret

; ===========================================================================
; sdc_deselect — deselect the card and EI, preserving the success/failure CY the
; caller set (push/pop AF brackets the port writes). Tail of every card read.
; ===========================================================================
; sdc_select — issue a &DC microcontroller select byte, busy-polling FIRST (bounded,
; sdc_wait_ready). Colin's proven driver waits before every &DC OUT; the deselect
; tail (below) uses this. In: A = select byte. Clobbers AF.
sdc_select:
                push    af
                call    sdc_wait_ready
                pop     af
                out     (SDC_PORT), a
                ret

sdc_deselect:
                ; Colin's proven 4-step close (fix #6, bdos15t &A8D7;
                ; trinity-sd-z80-interface.md:99): &30 / dummy &FF flush on &DF / &30 /
                ; &04, each &DC select busy-polled first, the flush via sdc_out. The
                ; 2-step (&30 -> &04) close leaves SPI state a real card mishandles on
                ; silicon; the emulator gates on the proper order (i251). Mirrors the
                ; gold-standard csd_probe.asm csd_deselect.
                push    af                      ; preserve success/failure CY
                ld      a, SDC_DESEL            ; &30 deselect (busy-polled)
                call    sdc_select
                ld      a, SDC_IDLE            ; &FF dummy flush on &DF
                call    sdc_out
                ld      a, SDC_DESEL           ; &30 again
                call    sdc_select
                ld      a, SDC_NULLOFF         ; &04 all-deselect / auto-null off
                call    sdc_select
                ei
                pop     af
                ret

; ===========================================================================
; bd_list_read_hw — the REAL card-absolute list-sector read (i141): the hardware
; implementation of bdos_read_list_sector's BD_HOOK_LISTREAD model. Reads one
; 512-byte list sector off the SD card via a raw CMD17 single-block read on the
; Trinity SPI ports, into BD_LIST_BUF.
;
; In:  BD_LIST_SECTOR  1 byte  1-based list-sector number N (card-absolute LBA N:
;                              sector 0 is the boot block, sectors 1..base-1 hold
;                              the record list — trinity-record-detection-design.md
;                              §4.1). The seam only ever passes a 1-byte value.
; Out: BD_LIST_BUF   512 bytes  the list sector. CY set on read failure (no data
;                              token); the caller treats a failed read as "no free
;                              record" the same way a 0 BD_RECORDS does.
; Clobbers: AF, BC, DE, HL.
;
; ADDRESSING. CMD17 carries a 32-bit address: for an SDHC/v2 card (block-addressed)
; it is the LBA N; for an SDv1 card (byte-addressed) it is N<<9 (the §7 seek <<9).
; The card class is read from CSD_STAGE[0] (bit7:6 == 01 ⇒ v2), populated by the
; csd_set_bd_records startup read that also produced BD_RECORDS — so a list read
; only runs once the CSD has been read.
;
; EMULATION. Host-verified under the i145c/i145h SD model (sdcard.go), whose CMD17
; path streams store[addr] back for the address this frame carries. The standalone
; init ladder + SPI primitives are the same csd_read_into_stage proves against
; Colin's real ladder (i145f). Emulation-verified is not hardware-verified
; (CLAUDE.md §5): the real-Trinity SPI run is the final gate (i141's hardware half).
; ===========================================================================
bd_list_read_hw:
                ; Build the 32-bit LBA (LE) from the 1-byte list-sector number.
                ld      a, (BD_LIST_SECTOR)
                ld      (bd_list_lba), a
                xor     a
                ld      (bd_list_lba + 1), a
                ld      (bd_list_lba + 2), a
                ld      (bd_list_lba + 3), a
                ; SDv1 (byte-addressed) cards need LBA<<9; SDHC/v2 use the LBA verbatim.
                ld      a, (CSD_STAGE)
                and     &C0
                cp      &40
                jr      z, bllr_have_lba        ; v2/SDHC: block address, no shift
                ld      b, 9                    ; v1: bd_list_lba <<= 9
bllr_shl:
                ld      hl, bd_list_lba
                or      a
                rl      (hl)
                inc     hl
                rl      (hl)
                inc     hl
                rl      (hl)
                inc     hl
                rl      (hl)
                djnz    bllr_shl
bllr_have_lba:
                call    sdc_init_ladder        ; wake + CMD0/8/41/58/59; card selected
                ; CMD17 READ_SINGLE_BLOCK, arg = bd_list_lba (sent MSB-first by sdc_cmd:
                ; B=addr[31:24], C=addr[23:16], D=addr[15:8], E=addr[7:0]).
                ld      a, &FF                  ; CRC don't-care (CRC was turned off by CMD59)
                call    sdc_set_crc
                ld      a, (bd_list_lba + 3)
                ld      b, a
                ld      a, (bd_list_lba + 2)
                ld      c, a
                ld      a, (bd_list_lba + 1)
                ld      d, a
                ld      a, (bd_list_lba)
                ld      e, a
                ld      a, &51                  ; CMD17
                call    sdc_cmd
                ; poll for the &FE start-of-data token (bounded)
                ld      b, 0
bllr_tok_poll:
                call    sdc_in
                cp      SDC_DATATOK
                jr      z, bllr_tok_found
                djnz    bllr_tok_poll
                scf                             ; no token -> read failure
                jp      sdc_deselect
bllr_tok_found:
                ; read the 512-byte data block (two 256-byte passes) into BD_LIST_BUF
                ld      hl, BD_LIST_BUF
                ld      b, 0                    ; 256
bllr_rd1:
                call    sdc_in
                ld      (hl), a
                inc     hl
                djnz    bllr_rd1
                ld      b, 0                    ; + 256 = 512
bllr_rd2:
                call    sdc_in
                ld      (hl), a
                inc     hl
                djnz    bllr_rd2
                call    sdc_in                  ; 2 CRC bytes (discard)
                call    sdc_in
                or      a                       ; CY clear: success
                jp      sdc_deselect

; ===========================================================================
; bd_list_write_hw — the REAL card-absolute list-sector write (i198): the
; hardware implementation of bdos_write_list_sector's BD_HOOK_LISTWRITE model.
; Writes one 512-byte list sector to the SD card via a raw CMD24 single-block
; write on the Trinity SPI ports, from BD_LIST_BUF. Mirrors bd_list_read_hw
; (CMD17, above) exactly: same init ladder, same LBA computation, same
; select/deselect bracket. Only the data phase differs (OUT instead of IN).
;
; In:  BD_LIST_SECTOR  1 byte  1-based list-sector number N (same addressing
;                              as the CMD17 read — card-absolute LBA N for
;                              SDHC, N<<9 for SDv1).
;      BD_LIST_BUF   512 bytes  the list sector to write back (a modified copy
;                              from bdos_claim_record's read-modify-write).
; Out: (none — success or failure both return to caller; failure is silent:
;       a wrong write on real hardware is caught by the i194 verification step)
; Clobbers: AF, BC, DE, HL.
;
; PROTOCOL (matched to sdcard.go's CMD24 state machine — the authority for the
; exact handshake, verified by the SD model round-trip test):
;   1. sdc_cmd(&58, bd_list_lba) → R1 ready (consumed by sdc_cmd's poll).
;   2. OUT dummy &FF (the leading gap before the start-of-data token).
;   3. OUT &FE start-of-data token (opens the write data phase).
;   4. OUT 512 bytes from BD_LIST_BUF (two 256-byte loops, mirroring the read).
;   5. OUT 2 dummy CRC bytes (CRC is off; the card ignores them).
;   6. IN  gap byte (discarded — the one-read gap before the data-response).
;   7. IN  data-response token: mask (&1E), subtract 4; zero = accepted.
;   8. IN busy poll: loop while card drives &00 (busy), break on &FF (done);
;      `inc a; jr z` is the standard bounded poll, mirroring sdil_wake_poll.
;   Steps 6-8 mirror hd.svb-t &A893-&A8AE (Colin's real write tail) that
;   sdcard.go's inWriteResp models.
;
; EMULATION. Host-verified under the i145c/i145h SD model (sdcard.go), whose
; CMD24 path captures the 512 bytes into store[addr] and returns the accept
; token + busy poll. The claim read-modify-write round-trip is proven by
; sd_listwrite_test.go (the TestClaimRMWRealCMD17CMD24 family). Emulation-
; verified is not hardware-verified (CLAUDE.md §5): the real-Trinity SPI write
; is the final gate (i194's hardware step).
; ===========================================================================
bd_list_write_hw:
                ; Build the 32-bit LBA from BD_LIST_SECTOR (same computation as the read).
                ld      a, (BD_LIST_SECTOR)
                ld      (bd_list_lba), a
                xor     a
                ld      (bd_list_lba + 1), a
                ld      (bd_list_lba + 2), a
                ld      (bd_list_lba + 3), a
                call    bd_lba_apply_v1_shift   ; SDv1: bd_list_lba <<= 9; v2/SDHC: no-op
                ; Source is the 512-byte list buffer; reuse the shared CMD24 core.
                ld      hl, BD_LIST_BUF
                jp      bd_cmd24_write_core

; ===========================================================================
; bd_lba_apply_v1_shift — for an SDv1 (byte-addressed) card, shift bd_list_lba
; left by 9 (block number -> byte offset); for SDHC/v2 (block-addressed) leave it
; verbatim. The card class is read from CSD_STAGE[0] (bit7:6 == 01 => v2). Shared
; by every CMD17/CMD24 LBA setup so the byte-vs-block addressing is computed once.
; In/Out: bd_list_lba (32-bit LE). Clobbers AF, B, HL.
; ===========================================================================
bd_lba_apply_v1_shift:
                ld      a, (CSD_STAGE)
                and     &C0
                cp      &40
                ret     z                       ; v2/SDHC: block address, no shift
                ld      b, 9                    ; v1: bd_list_lba <<= 9
blwr_shl:
                ld      hl, bd_list_lba
                or      a
                rl      (hl)
                inc     hl
                rl      (hl)
                inc     hl
                rl      (hl)
                inc     hl
                rl      (hl)
                djnz    blwr_shl
                ret

; ===========================================================================
; bd_cmd24_write_core — the shared CMD24 single-block write protocol (the §5/§8
; write tail Colin's hd.svb-t models, matched to sdcard.go's CMD24 state machine).
; Self-healing: runs the full sdc_init_ladder every call, so a disturbed SPI state
; (the serve's ENC ereset) is re-established before the write. Used by both
; bd_list_write_hw (record-list claim) and bd_record_write_hw (record data write),
; so the CMD24 handshake lives in ONE tested place.
;
; In:  bd_list_lba  4 bytes  32-bit LE block/byte address (already v1-shifted).
;      HL                    pointer to the 512-byte source buffer.
; Out: CY clear on success, CY set on failure (command reject / not-accepted /
;      busy timeout). Card deselected + EI on every path.
; Clobbers: AF, BC, DE, HL.
; ===========================================================================
bd_cmd24_write_core:
                ld      (bd_cmd24_src), hl     ; latch the source pointer for the data phase
                call    sdc_init_ladder        ; wake + CMD0/8/41/58/59; card selected
                ; CMD24 WRITE_SINGLE_BLOCK, arg = bd_list_lba (sent MSB-first by sdc_cmd).
                ld      a, &FF                  ; CRC don't-care (CRC off)
                call    sdc_set_crc
                ld      a, (bd_list_lba + 3)
                ld      b, a
                ld      a, (bd_list_lba + 2)
                ld      c, a
                ld      a, (bd_list_lba + 1)
                ld      d, a
                ld      a, (bd_list_lba)
                ld      e, a
                ld      a, &58                  ; CMD24
                call    sdc_cmd                 ; R1 consumed; on entry CY set = fail
                jr      c, blwr_done            ; command failed: bail (sdc_deselect not yet called)
                ; Protocol step 2: dummy &FF (the gap before the &FE token).
                ld      a, SDC_IDLE
                call    sdc_out
                ; Protocol step 3: &FE start-of-data token.
                ld      a, SDC_DATATOK
                call    sdc_out
                ; Protocol step 4: send the 512-byte data block from (bd_cmd24_src)
                ;   (two 256-byte passes, mirroring the read's two-pass INI).
                ld      hl, (bd_cmd24_src)
                ld      b, 0                    ; 256 bytes first pass
blwr_wd1:
                ld      a, (hl)
                call    sdc_out
                inc     hl
                djnz    blwr_wd1
                ld      b, 0                    ; + 256 = 512
blwr_wd2:
                ld      a, (hl)
                call    sdc_out
                inc     hl
                djnz    blwr_wd2
                ; Protocol step 5: 2 dummy CRC bytes (CRC is off; values ignored).
                ld      a, SDC_IDLE
                call    sdc_out
                call    sdc_out
                ; Protocol step 6: gap byte before the data-response (discarded).
                call    sdc_in
                ; Protocol step 7: data-response token. Mask &1E; subtract 4; zero = accepted.
                call    sdc_in
                and     &1E
                sub     4
                jr      nz, blwr_reject         ; not accepted: deselect with CY set
                ; Protocol step 8: busy poll. Card drives &00 while writing, releases &FF when done.
                ; `inc a; jr z` loops on the &00 -> 1 path and exits on the &FF -> 0 path.
                ; Bounded at 256 iterations (B=0): matches the sdil_wake_poll idiom.
                ld      b, 0
blwr_busy:
                call    sdc_in
                inc     a
                jr      z, blwr_ok              ; &FF -> 0: write done, card released MISO
                djnz    blwr_busy
                ; Timed out waiting for ready — treat as failure.
blwr_reject:
                scf
blwr_done:
                jp      sdc_deselect
blwr_ok:
                or      a                       ; CY clear: success
                jp      sdc_deselect

; ===========================================================================
; bd_record_write_hw — write one 512-byte data sector of a claimed Trinity record
; with our OWN self-healing CMD24, by ABSOLUTE card LBA, bypassing the B-DOS HWSAD
; hook (q62 option (a) / i194 / §8ag). The B-DOS hooks flakily hang on real hardware
; (they rely on boot-time SPI-mode persistence the serve's ENC reset disturbs);
; bd_cmd24_write_core re-runs sdc_init_ladder every call, so it self-heals and works.
;
; RECORD -> LBA (the data-safety-critical math; cross-checked against the Go
; authority tools/netboot-oracle/bdos/raw_sink.go + the §6/§7 record geometry):
;
;     LBA = csd_base + 1600*(record-1) + linearSec
;
;   - csd_base (sd_csd.asm) is the first DATA sector of the card (record-list base
;     + 1) computed by csd_blocks_to_records at startup.
;   - record is 1-based; linearSec is 0-based (0..1599 within the record).
;   - 1600*(record-1) is a 16-bit*16-bit -> >=24-bit product; computed by repeated
;     16-bit addition of 1600 into a 32-bit accumulator ((record-1) iterations,
;     <= ~4808 for the largest card). Correctness first; the per-sector cost is
;     dwarfed by the bit-banged SPI transfer.
;
; DATA SAFETY (ENFORCED, i295): the LBA must fall in [csd_base+1600*(n-1),
; csd_base+1600*n) for the claimed record n with linearSec in 0..1599, be >= csd_base,
; and be < the card capacity — so it can never reach another record's blocks, the
; record list (sectors 1..base-1), sector 0, or off the card. This is CHECKED by
; bd_record_lba_in_band after the LBA is computed: a violation ABORTS the write (CY
; set, NO CMD24) and sets bd_rec_guard_tripped. The caller passes the CLAIMED FREE
; record number; bdos_find_record_for_strategy guarantees it was free — but the guard
; does not trust that, so even a miscomputed csd_base or an arithmetic wrap cannot
; silently corrupt Pete's shared card. Checks (b)/(d) are INDEPENDENT of csd_base's
; correctness (>= csd_base, < capacity), which is what lets the guard catch a wrong
; base at all — a band check derived only from csd_base moves with the error and
; catches nothing (the i295 root cause: a wrong csd_base=2438 placed writes 388
; sectors into the NEXT record).
;
; In:  BD_REC_WRITE_REC     2 bytes  1-based claimed record number n (>= 1)
;      BD_REC_WRITE_LINEAR  2 bytes  0-based linear sector within the record (0..1599)
;      HL                            pointer to the 512-byte source buffer
; Out: CY clear on success, CY set on failure (bd_cmd24_write_core's contract OR the
;      data-safety guard refusing an out-of-band LBA). Clobbers: AF, BC, DE, HL.
;
; EMULATION. Host-verified under the i145c/i145h SD model (sdcard.go): the CMD24
; lands in store[LBA], so a test asserts the absolute block address + the 512 bytes
; (netboot_serve_wrq_record_test.go own-CMD24 path; the LBA math is unit-tested in
; isolation in sd_record_write_test.go). Emulation-verified is not hardware-verified
; (CLAUDE.md §5): the real-Trinity write is i194's hardware step (Pete-gated).
; ===========================================================================
bd_record_write_hw:
                push    hl                      ; save the source pointer across the LBA math

                ; --- accumulator (32-bit LE) := 1600 * (record - 1) ---
                ld      hl, 0
                ld      (bd_rec_lba), hl
                ld      (bd_rec_lba + 2), hl
                ld      hl, (BD_REC_WRITE_REC)
                dec     hl                      ; HL = record - 1 (iteration count)
brw_mul_loop:
                ld      a, h
                or      l
                jr      z, brw_mul_done         ; (record-1) additions of 1600 done
                push    hl                      ; save the loop counter
                ; bd_rec_lba += 1600 (32-bit: low word add, then propagate carry).
                ld      hl, (bd_rec_lba)
                ld      bc, 1600
                add     hl, bc
                ld      (bd_rec_lba), hl
                jr      nc, brw_mul_nocarry
                ld      hl, (bd_rec_lba + 2)
                inc     hl
                ld      (bd_rec_lba + 2), hl
brw_mul_nocarry:
                pop     hl                      ; restore the loop counter
                dec     hl
                jr      brw_mul_loop
brw_mul_done:
                ; Save the record OFFSET 1600*(n-1) (the band's start, relative to base)
                ; for the data-safety band check below, BEFORE linearSec/base are added.
                ld      hl, (bd_rec_lba)
                ld      (bd_rec_ofs), hl
                ld      hl, (bd_rec_lba + 2)
                ld      (bd_rec_ofs + 2), hl
                ; --- accumulator += linearSec (16-bit, into the 32-bit accumulator) ---
                ld      hl, (bd_rec_lba)
                ld      bc, (BD_REC_WRITE_LINEAR)
                add     hl, bc
                ld      (bd_rec_lba), hl
                jr      nc, brw_lin_nocarry
                ld      hl, (bd_rec_lba + 2)
                inc     hl
                ld      (bd_rec_lba + 2), hl
brw_lin_nocarry:
                ; --- accumulator += csd_base (16-bit) ---
                ld      hl, (bd_rec_lba)
                ld      bc, (csd_base)
                add     hl, bc
                ld      (bd_rec_lba), hl
                jr      nc, brw_base_nocarry
                ld      hl, (bd_rec_lba + 2)
                inc     hl
                ld      (bd_rec_lba + 2), hl
brw_base_nocarry:
                ; ---------------------------------------------------------------
                ; DEFENSIVE DATA-SAFETY GUARD (i295). Pete's SD card is a SHARED user
                ; resource; a raw absolute CMD24 to the wrong LBA silently corrupts the
                ; record LIST (sectors 1..base-1), sector 0, or a NEIGHBOURING record.
                ; The old "in-band" reasoning was circular — it derived the band from
                ; the SAME csd_base the write used, so a wrong base passed. This guard
                ; refuses the write (CY set, NO CMD24) unless the FINAL LBA is provably
                ; inside the claimed record's own body sectors AND on the card:
                ;   (a) linearSec < 1600           — a valid within-record sector index
                ;   (b) final LBA >= csd_base      — never sector 0 or the record list
                ;   (c) csd_base+ofs <= final < csd_base+ofs+1600  — the claimed record's
                ;                                    band (catches an arithmetic wrap:
                ;                                    final must equal base+ofs+linearSec)
                ;   (d) final LBA < csd_blocks     — within the card's capacity
                ; Any violation is a corruption attempt -> abort. bd_rec_guard_tripped is
                ; a sticky diagnostic flag (host tests assert it). Even if the base were
                ; ever miscomputed to push a write off the card or below base, this makes
                ; the raw-LBA path refuse rather than clobber Pete's data.
                call    bd_record_lba_in_band
                jr      c, brw_out_of_band      ; guard failed -> refuse (CY already set)
                ; Move the computed block LBA into bd_list_lba (the address
                ; bd_cmd24_write_core sends), then apply the v1 byte-shift.
                ld      hl, (bd_rec_lba)
                ld      (bd_list_lba), hl
                ld      hl, (bd_rec_lba + 2)
                ld      (bd_list_lba + 2), hl
                call    bd_lba_apply_v1_shift   ; SDv1: <<9; v2/SDHC: no-op

                pop     hl                      ; restore the source pointer
                jp      bd_cmd24_write_core     ; shared CMD24 protocol; returns to caller
brw_out_of_band:
                ld      a, 1
                ld      (bd_rec_guard_tripped), a ; sticky: an out-of-band write was refused
                pop     hl                      ; balance the entry `push hl`
                scf                             ; CY set = failure (no CMD24 issued)
                ret

; ===========================================================================
; bd_record_lba_in_band — the i295 data-safety validator. Returns CY CLEAR if the
; computed record-write LBA (bd_rec_lba) is provably safe to write, CY SET (refuse)
; otherwise. Checks (a)-(d) above using the record offset bd_rec_ofs (= 1600*(n-1),
; saved before linearSec/base were added), csd_base, and csd_blocks (capacity).
; Read-only w.r.t. bd_rec_lba. Clobbers AF, BC, DE, HL.
; ===========================================================================
bd_record_lba_in_band:
                ; (a) linearSec < 1600  (16-bit: reject linearSec >= 1600)
                ld      hl, (BD_REC_WRITE_LINEAR)
                ld      bc, 1600
                or      a
                sbc     hl, bc                  ; linearSec - 1600 ; CY set => linearSec < 1600 (ok)
                jr      nc, blb_refuse          ; linearSec >= 1600 -> refuse

                ; Compute bandLo = csd_base + bd_rec_ofs (32-bit) into bd_band_lo.
                ld      hl, (bd_rec_ofs)
                ld      bc, (csd_base)
                add     hl, bc
                ld      (bd_band_lo), hl
                ld      hl, (bd_rec_ofs + 2)
                ld      bc, 0
                adc     hl, bc                  ; propagate carry into the high word
                ld      (bd_band_lo + 2), hl

                ; (b) final LBA >= csd_base : refuse if bd_rec_lba < csd_base.
                ; Build csd_base as a 32-bit value in bd_cmp_tmp for the compare.
                ld      hl, (csd_base)
                ld      (bd_cmp_tmp), hl
                ld      hl, 0
                ld      (bd_cmp_tmp + 2), hl
                ld      hl, bd_rec_lba          ; A = bd_rec_lba, B = bd_cmp_tmp (csd_base)
                ld      de, bd_cmp_tmp
                call    bd_cmp32                ; CY set if bd_rec_lba < csd_base
                jr      c, blb_refuse

                ; (c-lo) final LBA >= bandLo : refuse if bd_rec_lba < bd_band_lo.
                ld      hl, bd_rec_lba
                ld      de, bd_band_lo
                call    bd_cmp32
                jr      c, blb_refuse

                ; (c-hi) final LBA < bandLo + 1600 : bandHi = bandLo + 1600, refuse if
                ; bd_rec_lba >= bandHi (i.e. NOT (bd_rec_lba < bandHi)).
                ld      hl, (bd_band_lo)
                ld      bc, 1600
                add     hl, bc
                ld      (bd_cmp_tmp), hl
                ld      hl, (bd_band_lo + 2)
                ld      bc, 0
                adc     hl, bc
                ld      (bd_cmp_tmp + 2), hl
                ld      hl, bd_rec_lba          ; refuse unless bd_rec_lba < bandHi
                ld      de, bd_cmp_tmp
                call    bd_cmp32                ; CY set if bd_rec_lba < bandHi (ok)
                jr      nc, blb_refuse          ; bd_rec_lba >= bandHi -> refuse

                ; (d) final LBA < csd_blocks (capacity) : refuse if bd_rec_lba >= csd_blocks.
                ld      hl, bd_rec_lba          ; refuse unless bd_rec_lba < csd_blocks
                ld      de, csd_blocks
                call    bd_cmp32                ; CY set if bd_rec_lba < csd_blocks (ok)
                jr      nc, blb_refuse          ; bd_rec_lba >= capacity -> refuse

                or      a                       ; CY clear = safe to write
                ret
blb_refuse:
                scf                             ; CY set = refuse
                ret

; bd_cmp32 — unsigned 32-bit compare of two LE values. In: HL -> A (4 bytes LE),
; DE -> B (4 bytes LE). Out: CY SET if A < B, CY CLEAR if A >= B (Z set if A == B).
; Compares high word first, then low word. Clobbers AF, BC, HL, DE (advances them).
bd_cmp32:
                inc     hl
                inc     hl
                inc     hl                      ; HL -> A[3] (MSB)
                inc     de
                inc     de
                inc     de                      ; DE -> B[3] (MSB)
                ld      b, 4
bc32_loop:
                ld      a, (de)
                cp      (hl)                    ; compare B[i] vs A[i]  (CY set if A[i] > B[i])
                jr      c, bc32_a_greater       ; A[i] > B[i] -> A > B -> CY clear
                jr      nz, bc32_a_less         ; A[i] < B[i] -> A < B -> CY set
                dec     hl
                dec     de
                djnz    bc32_loop
                or      a                       ; all bytes equal -> A == B -> CY clear, Z set
                ret
bc32_a_greater:
                or      a                       ; CY clear (A >= B)
                ret
bc32_a_less:
                scf                             ; CY set (A < B)
                ret

bd_list_lba:      defs 4                        ; 32-bit LE CMD17/CMD24 block/byte address
bd_cmd24_src:     defs 2                        ; bd_cmd24_write_core: 512-byte source pointer
bd_rec_lba:       defs 4                        ; bd_record_write_hw: 32-bit LE record data-block LBA accumulator
bd_rec_ofs:       defs 4                        ; bd_record_write_hw: 32-bit LE record offset 1600*(n-1) (band start, rel. base)
bd_band_lo:       defs 4                        ; bd_record_lba_in_band: 32-bit LE band lower bound = csd_base + bd_rec_ofs
bd_cmp_tmp:       defs 4                        ; bd_record_lba_in_band / bd_cmp32: 32-bit LE compare scratch
bd_rec_guard_tripped: defb 0                    ; sticky: set when the data-safety guard REFUSED an out-of-band write
BD_REC_WRITE_REC:    defs 2                     ; bd_record_write_hw: 1-based claimed record number n
BD_REC_WRITE_LINEAR: defs 2                     ; bd_record_write_hw: 0-based linear sector within the record

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
; csd_blocks_to_records — the §6 records math, storing the 16-bit record count into
; csd_records and the record-list base into csd_base. It runs on the EFFECTIVE block
; count csd_eff (csd_compute_eff below), NOT csd_blocks directly, exactly mirroring
; B-DOS 1.5t's HDINIT (&A3D2→&A4A7):
;   eff      = csd_compute_eff(blocks)   ; the +1 (&A340) and 16-bit clamp (&A452)
;   records1 = eff / 1600
;   base     = (records1 + 32)/32 + 1                     -> csd_base (16-bit @ &80C2)
;   usable   = eff - base
;   records  = usable / 1600  (+1 if (usable % 1600) >= 50)  -> csd_records (16-bit @ &80C4)
; Mirrors refRecordsFull (csd_decode_colin_test.go). Clobbers AF/BC/DE/HL.
;
; AUTHORITY (i295, Trinity-HW): B-DOS runs the WHOLE records math on eff, so the
; 16-bit-overflow clamp on records1 propagates into base — the reason B-DOS's base
; for Pete's 64 GB card is 2050, not the un-clamped 2438. Traced live on the real
; B-DOS 1.5t binary in TestBDOSRecordsMathBase64GB; see csd_compute_eff.
csd_blocks_to_records:
                call    csd_compute_eff        ; csd_eff <- effective dividend (+1 & clamp)
                ; records1 = eff / 1600 (32-bit quotient in csd_div_n)
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
                ; usable = eff - base
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

; csd_load_dividend — csd_div_n <- csd_eff (32-bit). The records math divides the
; EFFECTIVE block count (csd_compute_eff), not csd_blocks. Clobbers AF/HL.
csd_load_dividend:
                ld      hl, (csd_eff)
                ld      (csd_div_n), hl
                ld      hl, (csd_eff + 2)
                ld      (csd_div_n + 2), hl
                ret

; ===========================================================================
; csd_compute_eff — csd_eff <- the effective 32-bit block count B-DOS 1.5t's HDINIT
; feeds its records math, from csd_blocks. Two steps, ported byte-for-byte from the
; traced B-DOS boot path (i295, Trinity-HW authority bdos15t-beta6 &A340 + &A452):
;
;   1. +1 (&A340). HDINIT's zero-check does `inc hl` on the block-count low word and,
;      on the normal non-zero branch (&A34E), does NOT undo it, so eff = blocks + 1.
;
;   2. THE 16-BIT-OVERFLOW CLAMP (&A452). `ld hl,&F9C0 (-1600); add hl,de` tests the
;      HIGH word of (blocks+1): if it is >= 1600 (i.e. blocks+1 >= 1600*65536 =
;      104,857,600) the &A454 branch substitutes a SYNTHETIC dividend DE:HL =
;      1600:449 = &064001C1 = 104,858,049 for the whole records math. Dividing that
;      by 1600 gives exactly 65536 (= 2^16): records1 SATURATES at 65536, so the
;      record-list base CEILINGS at (65536+32)/32+1 = 2050. This is B-DOS's "16-bit
;      records" limit — it cannot address more than 65536 records, so on a >51 GB
;      card (Pete's 64 GB) records1 AND the derived base clamp. Using the full
;      capacity would need a wider BD_RECORDS in B-DOS itself (out of scope): the
;      hardware-matching answer today is base=2050, and we MUST mirror it or every
;      raw-LBA record write lands where B-DOS does not look (the i295 root cause).
;
; Mirrors bdosEffBlocks (csd_decode_colin_test.go). Clobbers AF/BC/DE/HL.
CSD_CLAMP_HI:     equ &0640                     ; high word of the &A454 synthetic dividend (1600)
CSD_CLAMP_LO:     equ &01C1                     ; low  word of the &A454 synthetic dividend (449)
csd_compute_eff:
                ; eff = blocks + 1  (32-bit LE increment)
                ld      hl, (csd_blocks)
                ld      de, 1
                add     hl, de
                ld      (csd_eff), hl
                ld      hl, (csd_blocks + 2)
                jr      nc, csdce_hi_done
                inc     hl                     ; carry from the low-word +1
csdce_hi_done:
                ld      (csd_eff + 2), hl      ; HL = high word of (blocks+1)
                ; &A452 test: if high16(blocks+1) >= 1600, clamp eff to &064001C1.
                ; (BC := high word; NC from `sbc hl,1600` means high >= 1600.)
                ld      b, h
                ld      c, l
                ld      hl, CSD_CLAMP_HI       ; 1600
                or      a
                sbc     hl, bc                 ; 1600 - high ; CY set if high > 1600, Z if ==
                jr      z, csdce_clamp         ; high == 1600 -> clamp (>= 1600)
                jr      nc, csdce_done         ; high <  1600 -> no clamp (1600-high > 0, NC)
csdce_clamp:
                ; high >= 1600: substitute the synthetic dividend 1600:449 = 104,858,049.
                ld      hl, CSD_CLAMP_LO       ; low word 449
                ld      (csd_eff), hl
                ld      hl, CSD_CLAMP_HI       ; high word 1600
                ld      (csd_eff + 2), hl
csdce_done:
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
csd_blocks:       defs 4                  ; 32-bit LE block count (raw, from the CSD)
csd_eff:          defs 4                  ; 32-bit LE effective dividend (csd_compute_eff: +1 & &A452 clamp)
csd_records:      defs 2                  ; 16-bit (wrapping) record count -> BD_RECORDS
csd_base:         defs 2                  ; record-list base sector + 1
csd_div_n:        defs 4                  ; csd_div32 dividend / quotient
csd_div_cnt:      defs 1                  ; csd_div32 iteration counter
