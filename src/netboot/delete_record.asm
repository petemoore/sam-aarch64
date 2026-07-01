; delete_record.asm — the small trinload-pushable "free/delete Trinity SD record N"
; program (i317). The counterpart to boot_record (i316, boot a record) and sd_push
; (i293, push a disk into a record): a host names a record number, this program frees
; that record — it clears the record's central record-LIST name entry so the slot
; reads as unnamed/reusable, and the NEXT sd_push lands there. It completes the
; store/boot/delete testing toolkit so the autonomous loop can build a disk, push it
; into a record, boot it, then FREE it again and re-push cleanly, all without a human
; at the SAM and without exhausting records.
;
; THE WHOLE JOB is a thin wrapper around bdos_free_record (bdos_seam.asm, the i317
; primitive — the exact inverse of bdos_claim_record): set BD_DELETE_RECORD to the
; requested record, initialise the card (csd_set_bd_records: read the CSD so the raw
; list read/write use the right block-vs-byte addressing, and learn the record count
; for a range check), then `call bdos_free_record`. That primitive does a single-entry
; read-modify-write of the record list: read the containing list sector (real CMD17),
; ZERO exactly this record's 16-byte name entry, write the sector back (real CMD24) —
; the record-list side of B-DOS's new.rec / RENAME "change label in record list" with
; an all-zero label, which reads back FREE ((entry[0] AND 0x7F) == 0). Every other
; entry in the sector is preserved byte-for-byte (the safety invariant).
;
; DATA-SAFETY (the Trinity SD card is a SHARED user resource — trinity_storage_shared_
; resource): the primitive touches ONLY the named record's 16-byte list entry; no
; neighbour is ever written. The wrapper additionally REFUSES an out-of-range record
; (0, or > the card's record count) so a bad/typo'd number cannot address a stray LBA:
; it exits cleanly without writing. The DECISION to free a record — is it one WE may
; free? — is the operator's (the launcher warns; on real hardware the free itself is
; Pete-gated, i295 family); this program frees whatever in-range record it is handed.
;
; RECORD-NUMBER INPUT — a host-patched config block (the boot_record / sd_push idiom).
; DEL_CONFIG is a small fixed region at the END of the binary that the host launcher
; (tools/trinload-push/delete-record.py) patches by file offset BEFORE pushing:
;   +0  DEL_CONFIG        1 byte   magic/version = DEL_CFG_MAGIC_VAL (&5A); the launcher
;                                  sanity-checks it found the block. Patch anchor (file
;                                  offset DEL_CONFIG - &8000, the load org). Never written.
;   +1  DEL_CFG_RECORD    1 byte   the record number to free (1..255). Patched in.
; An un-patched binary carries the baked default (record 1). Using a patched byte (not
; a network message) keeps the program trivial: there is no wire loop — trinload's X
; packet lands us at &8000, we read RAM, free the record. This mirrors boot_record's
; BOOT_CONFIG and its launcher boot-record.py.
;
; VERIFICATION (emulation-first, CLAUDE.md rule 7): delete_record_test.go loads this
; binary under the flat harness, attaches the SD-SPI model (sdcard.go — the same real
; CMD9/CMD17/CMD24 model sd_push/sd_listread use), seeds a record-list sector with some
; NAMED records, patches DEL_CFG_RECORD to one of them, runs delete_record_main, and
; asserts the card's captured list sector now reads that record FREE while every
; neighbour entry is byte-for-byte intact. The actual on-hardware free routes the same
; CMD17/CMD24 through the real Trinity SD driver and stays hardware-gated (CLAUDE.md §5;
; i295 family) — the real free shot is a SEPARATE follow-up, NOT this program's
; verification. Emulation-verified is not hardware-verified.
;
; TWO FLAGS, no carve-out (i231): built with NETBOOT_REAL_LISTREAD (bdos_read_list_sector
; / bdos_write_list_sector tail-call the real CMD17/CMD24 SPI paths in sd_csd.asm) and
; NETBOOT_WANT_CLAIM (assembles bdos_write_list_sector + bdos_free_record). It contains
; no `if defined(NETBOOT_HOSTTEST)` directive — the harness drives the same real SPI path
; against the modelled card, so there is nothing to carve out.

                org     &8000

                ; Entry: trinload's X packet does `out (HMPR),P; jp &8000`, landing
                ; here. The host harness runs delete_record_main by symbol; this jp is
                ; the hardware/trinload entry.
                jp      delete_record_main

; ===========================================================================
; Composed includes — the B-DOS storage seam supplies bdos_free_record (the i317
; record-list-clear primitive), bdos_read_list_sector / bdos_write_list_sector, and
; BD_DELETE_RECORD / BD_RECORDS; sd_csd.asm supplies csd_set_bd_records (CSD read +
; record count) and the real CMD17/CMD24 list read/write. bdos_seam comes FIRST (right
; after the boot jp) so every symbol it defines is resolved for the code below.
; ===========================================================================
                include "bdos_seam.asm"        ; bdos_free_record / read+write list sector / BD_DELETE_RECORD / BD_RECORDS
                include "sd_csd.asm"            ; csd_set_bd_records (CSD_STAGE + BD_RECORDS) / bd_list_read_hw / bd_list_write_hw

; ===========================================================================
; delete_record_main — the bootable / trinload entry.
; ===========================================================================
delete_record_main:
                di

                ; --- read the host-chosen record number from the config block ------
                ; An un-patched binary carries the baked default (DEL_CFG_RECORD = 1);
                ; the host launcher patches it to the record to free before pushing.
                ; Widen the 1-byte config value to the 2-byte BD_DELETE_RECORD input
                ; (high byte 0) that bdos_free_record's geometry expects.
                ld      a, (DEL_CFG_RECORD)
                ld      l, a
                ld      h, 0
                ld      (BD_DELETE_RECORD), hl

                ; --- initialise the card: read the CSD (block-vs-byte addressing for
                ; the raw list read/write) and learn the record count for the range
                ; check. csd_set_bd_records runs the bounded SD init ladder + CMD9 and
                ; sets CSD_STAGE + BD_RECORDS; CY on a CSD read failure -> bail without
                ; writing (we must not address the card if we can't read its geometry).
                call    csd_set_bd_records
                jr      c, dr_exit

                ; --- DATA-SAFETY range check: refuse record 0 (the floppy has no list
                ; entry) or a record beyond the card's count, so a typo'd number can
                ; never address a stray LBA. Out of range -> exit cleanly, no write.
                ld      hl, (BD_DELETE_RECORD)  ; HL = record (high byte 0)
                ld      a, h
                or      l
                jr      z, dr_exit              ; record 0: invalid, bail
                ld      de, (BD_RECORDS)        ; DE = total record count
                ld      a, e                    ; compute DE - HL: borrow => HL > DE
                sub     l
                ld      a, d
                sbc     a, h
                jr      c, dr_exit              ; record > BD_RECORDS: out of range, bail

                ; --- free the record: zero its 16-byte central record-list entry -----
                ; bdos_free_record (bdos_seam.asm) reads the containing list sector
                ; (CMD17), zeroes ONLY this record's entry, writes the sector back
                ; (CMD24). Data-safe: no neighbour entry is touched.
                call    bdos_free_record

dr_exit:
                ; Exit cleanly back to trinload (it pushed its `start` as our return
                ; address), never a bare di;halt (a raw halt strands the SAM and costs a
                ; power-cycle on every push; Pete, 2026-06-24).
                ei
                ret

; ===========================================================================
; DEL_CONFIG — the record-number config block (the boot_record / sd_push idiom).
; A small, fixed, host-patchable region at the END of the binary so the host launcher
; (tools/trinload-push/delete-record.py) can set the record to free by file offset
; before the program runs. Read straight from RAM at boot (cheap; never re-read). An
; un-patched binary uses the baked default (record 1).
;
; BYTE LAYOUT (the launcher patches by file offset from the DEL_CONFIG symbol — the
; block's anchor at +0; keep STABLE):
;   +0  DEL_CONFIG      1 byte   magic/version = DEL_CFG_MAGIC_VAL (&5A); the launcher
;                                sanity-checks it found the block. Patch anchor (file
;                                offset DEL_CONFIG - &8000). Never written.
;   +1  DEL_CFG_RECORD  1 byte   the record number to free (1..255). Patched in.
; Total: 2 bytes.
; ===========================================================================
DEL_CFG_MAGIC_VAL:    equ &5A                  ; the magic/version byte value

DEL_CONFIG:           defb DEL_CFG_MAGIC_VAL   ; +0 magic/version (patch anchor)
DEL_CFG_RECORD:       defb 1                   ; +1 baked default: record 1 (patched by the host)

length:               equ $ - delete_record_main  ; code length (diagnostic)
