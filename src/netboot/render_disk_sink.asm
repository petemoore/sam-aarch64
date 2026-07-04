; render_disk_sink.asm — stream a > free-RAM byte sequence into a Trinity SD
; record as a real B-DOS/MGT CODE file (i365d-b1, Wall 1 of the demo — see
; docs/specs/i365-demo-architecture.md).
;
; The renderer streams release.src (~417 KB) one byte at a time; it never fits
; resident, so it cannot be HSAVE'd (64 KB/call cap, whole-region resident) and
; the B-DOS streaming trio (HOFLE/SBYT/CFSM) is broken for external callers even
; in 1.5t (docs/notes/sam-stub-audit.md:242-301). The mechanism that works: raw
; CMD24 chunked writes of one 512-byte MGT sector at a time (bd_record_write_hw,
; sd_csd.asm), re-blocking the stream into the 510-data + 2-byte-forward-link
; sector format B-DOS HGTHD/HLOAD and the i365c serve path read back, plus a
; directory entry so the file is named and findable.
;
; LAYOUT (all in RECORD side-major linear space, mgt_ts_to_record_linear
; netboot_server.asm:1092): the file body is written CONTIGUOUS from linearSec
; RDS_DATA_BASE (40 = track 4 sector 1, just after the 40-sector directory).
; Each body sector is [510 payload][next-track][next-sector]; the final sector's
; link is 0,0. The body is header(9) ++ data(len): the first 9 payload bytes of
; sector 0 are the CODE file header (both HLOAD and the serve skip them and use
; the DIRECTORY length), so RDS_LEN counts DATA bytes only.
;
; Sector 0 is HELD in RDS_FIRST_BUF until finish, because its 9-byte header
; carries length-derived fields (LengthMod16K, Pages) not known until the stream
; ends. Sectors 1.. stream through RDS_WORK_BUF and are written immediately. The
; forward link of a sector points to the NEXT contiguous sector; the truly-last
; sector must carry 0,0 — handled at finish (a boundary-aligned file leaves the
; last full WORK sector still resident in RDS_WORK_BUF, so its phantom link is
; patched to 0,0 and the sector re-written).
;
; AUTHORITY (CLAUDE.md rule 8): the MGT byte-map derives from samfile.go v3
; (FileEntry.Raw / FileHeader.Raw / Sector.SAMMask) cross-checked against
; nb_walk_entry (netboot_server.asm:1766) and store_format_record
; (http_main.asm:735). The "BDOS"@232 record-validity stamp is store_format_
; record's; HRECORD rejects a record without it.
;
; VERIFICATION (CLAUDE.md rule 7): render_disk_write_faithful_test.go drives this
; from render_disk_probe.asm on the FAITHFUL rig (real ROM + B-DOS 1.5t + SPI SD)
; and asserts real HGTHD/HLOAD read the bytes back, plus a Go-side physical-sector
; reconstruction for the 417 KB target size HLOAD cannot hold. NOT the BDOSStore
; mock (i356: its HRECORD skips the +232 gate, its HGTHD is a no-op).
;
; This module references sd_csd.asm's raw-CMD24 write path (bd_record_write_hw,
; BD_REC_REC, BD_REC_LINEAR) and bdos_seam.asm's bdos_id_str; it is included in a
; build defining NETBOOT_WANT_RECORD_WRITE.

; ===========================================================================
; Constants
; ===========================================================================
RDS_DATA_BASE:  equ 40                  ; first body sector = track 4 sector 1
RDS_HDR_LEN:    equ 9                   ; 9-byte CODE header prefixing the body
RDS_TYPE_CODE:  equ &13                 ; MGT file type 19 = CODE
RDS_PAYLOAD:    equ 510                 ; data bytes per sector (last 2 = link)

; ===========================================================================
; State (data first so every label resolves before the code below).
; ===========================================================================
RDS_RECORD:     defs 2                  ; target record (1-based) -> BD_REC_REC. Caller sets.
RDS_LOAD:       defs 2                  ; load address for header/dir fields. Caller sets.
RDS_NAME:       defs 10                 ; file name, space-padded. Caller fills.
RDS_LEN:        defs 4                  ; 32-bit DATA byte count (excludes the 9 header bytes)
RDS_FILL:       defs 2                  ; payload bytes in the current sector (0..510)
RDS_LINEAR:     defs 2                  ; record-linear of the current WORK sector
RDS_SECCOUNT:   defs 2                  ; sectors accounted so far (incl. held sector 0)
RDS_CUR_TS:     defs 2                  ; current sector (D=track_byte, E=sector) via ld de,()
RDS_FIRST_DONE: defs 1                  ; 0 until sector 0 has sealed (filled to 510)
RDS_CURBUF:     defs 2                  ; -> RDS_FIRST_BUF (sector 0) or RDS_WORK_BUF
RDS_ERR:        defs 1                  ; sticky: a bd_record_write_hw call returned CY
RDS_LENMOD:     defs 2                  ; len & 0x3FFF (computed at finish)
RDS_PAGES:      defs 1                  ; len >> 14
RDS_STARTPG:    defs 1                  ; (load>>14)-1
RDS_FIRST_BUF:  defs 512                ; the held sector 0 (header patched at finish)
RDS_WORK_BUF:   defs 512                ; the streaming sector (sectors 1.. and the tail)

; ===========================================================================
; render_disk_sink_reset — begin a fresh file. Caller has already filled
; RDS_RECORD, RDS_LOAD, RDS_NAME. Clobbers AF, HL, DE.
; ===========================================================================
render_disk_sink_reset:
                ld      hl, (RDS_RECORD)
                ld      (BD_REC_REC), hl        ; every write targets this record

                ld      hl, 0
                ld      (RDS_LEN), hl
                ld      (RDS_LEN + 2), hl
                ld      (RDS_SECCOUNT), hl
                xor     a
                ld      (RDS_FIRST_DONE), a
                ld      (RDS_ERR), a

                ld      hl, RDS_DATA_BASE
                ld      (RDS_LINEAR), hl        ; first WORK write lands at 40 (or 41 after seal)

                ; current sector cursor = track 4 sector 1 (D=track, E=sector).
                ld      d, 4
                ld      e, 1
                ld      (RDS_CUR_TS), de

                ; sector 0 accumulates in RDS_FIRST_BUF; reserve the 9-byte header.
                ld      hl, RDS_FIRST_BUF
                ld      (RDS_CURBUF), hl
                ld      hl, RDS_HDR_LEN
                ld      (RDS_FILL), hl          ; data streams from payload offset 9

                ; zero the 9 reserved header bytes (patched at finish).
                ld      hl, RDS_FIRST_BUF
                ld      b, RDS_HDR_LEN
                xor     a
rds_reset_hz:
                ld      (hl), a
                inc     hl
                djnz    rds_reset_hz
                ret

; ===========================================================================
; render_disk_sink_byte — accumulate one DATA byte (A). Clobbers AF, BC, DE, HL.
; render_out_append push/pops around this call, so all registers are free here.
; ===========================================================================
render_disk_sink_byte:
                ld      hl, (RDS_CURBUF)
                ld      de, (RDS_FILL)
                add     hl, de
                ld      (hl), a                 ; curbuf[RDS_FILL] = byte

                ; RDS_LEN += 1 (32-bit LE).
                ld      hl, (RDS_LEN)
                inc     hl
                ld      (RDS_LEN), hl
                ld      a, h
                or      l
                jr      nz, rds_byte_fill
                ld      hl, (RDS_LEN + 2)
                inc     hl
                ld      (RDS_LEN + 2), hl
rds_byte_fill:
                ; RDS_FILL += 1; full when it reaches 510.
                ld      hl, (RDS_FILL)
                inc     hl
                ld      (RDS_FILL), hl
                ld      de, RDS_PAYLOAD
                or      a
                sbc     hl, de
                ret     nz                      ; not full yet
                ; sector payload full -> seal it.
                jp      rds_seal_full

; ===========================================================================
; rds_seal_full — the current sector's 510 payload is complete. Set its forward
; link to the next contiguous sector, then either hold it (sector 0) or write it
; (a WORK sector), and advance the cursor/linear/count. Clobbers AF, BC, DE, HL.
; ===========================================================================
rds_seal_full:
                ; advance the (track,sector) cursor to the NEXT sector; that pair
                ; is both this sector's forward link and the next sector's own TS.
                ld      de, (RDS_CUR_TS)
                call    rds_next_ts             ; DE = next (D=track, E=sector)
                ld      (RDS_CUR_TS), de
                ld      hl, (RDS_CURBUF)
                ld      bc, RDS_PAYLOAD
                add     hl, bc
                ld      (hl), d                 ; [510] = next track
                inc     hl
                ld      (hl), e                 ; [511] = next sector

                ld      a, (RDS_FIRST_DONE)
                or      a
                jr      nz, rds_seal_write
                ; sector 0: hold it, switch streaming to the WORK buffer.
                ld      a, 1
                ld      (RDS_FIRST_DONE), a
                ld      hl, RDS_WORK_BUF
                ld      (RDS_CURBUF), hl
                jr      rds_seal_adv
rds_seal_write:
                ld      hl, (RDS_LINEAR)
                ld      (BD_REC_LINEAR), hl
                ld      hl, RDS_WORK_BUF
                call    bd_record_write_hw
                jr      nc, rds_seal_adv
                ld      a, 1
                ld      (RDS_ERR), a            ; sticky write/guard failure
rds_seal_adv:
                ld      hl, (RDS_SECCOUNT)
                inc     hl
                ld      (RDS_SECCOUNT), hl
                ld      hl, (RDS_LINEAR)
                inc     hl
                ld      (RDS_LINEAR), hl
                ld      hl, 0
                ld      (RDS_FILL), hl
                ret

; ===========================================================================
; rds_next_ts — DE := the next contiguous record sector's (D=track_byte,
; E=sector). Sectors 1..10; at 10 -> 1 and the track advances: side-0 cyl 4..78
; -> +1, side-0 cyl 79 -> side-1 cyl 0 (track_byte 128), side-1 128..206 -> +1.
; In: DE = current. Out: DE = next. Clobbers A.
; ===========================================================================
rds_next_ts:
                ld      a, e
                inc     a
                cp      11
                jr      nc, rds_nts_wrap
                ld      e, a                    ; sector < 10 -> just ++sector
                ret
rds_nts_wrap:
                ld      e, 1                    ; wrap sector to 1, advance track
                ld      a, d
                cp      79
                jr      z, rds_nts_side1
                inc     d                       ; 4..78 -> +1 ; 128..206 -> +1
                ret
rds_nts_side1:
                ld      d, 128                  ; side-0 last cyl -> side-1 cyl 0
                ret

; ===========================================================================
; render_disk_sink_finish — flush the final sector(s) with a terminal 0,0 link,
; patch sector 0's header from the now-known length, write sector 0, then write
; the directory entry. Clobbers AF, BC, DE, HL.
; ===========================================================================
render_disk_sink_finish:
                ; precompute len-derived fields (used by both header and dir entry).
                call    rds_compute_len_fields

                ld      a, (RDS_FIRST_DONE)
                or      a
                jr      z, rds_fin_single       ; sector 0 never sealed: single partial sector

                ; multi-sector: is there a partial tail in RDS_WORK_BUF?
                ld      hl, (RDS_FILL)
                ld      a, h
                or      l
                jr      nz, rds_fin_tail        ; RDS_FILL>0: partial tail to flush

                ; boundary-aligned: the last full sector is already written with a
                ; phantom forward link. It is still resident (nothing streamed since
                ; the seal). Patch its link to 0,0 and re-write it.
                ld      hl, (RDS_LINEAR)
                ld      de, RDS_DATA_BASE + 1
                or      a
                sbc     hl, de                  ; RDS_LINEAR == 41 -> only sector 0 exists
                jr      z, rds_fin_seal0_term   ; fix sector 0's phantom link instead
                ; last WORK sector sits at linearSec RDS_LINEAR-1, resident in WORK_BUF.
                ld      hl, RDS_WORK_BUF + RDS_PAYLOAD
                ld      (hl), 0                 ; terminal link 0,0
                inc     hl
                ld      (hl), 0
                ld      hl, (RDS_LINEAR)
                dec     hl
                ld      (BD_REC_LINEAR), hl
                ld      hl, RDS_WORK_BUF
                call    bd_record_write_hw
                jr      nc, rds_fin_write0
                ld      a, 1
                ld      (RDS_ERR), a
                jr      rds_fin_write0

rds_fin_seal0_term:
                ; sector 0 filled exactly and nothing followed: it is the only+last
                ; sector, so overwrite its phantom link with the terminal 0,0.
                ld      hl, RDS_FIRST_BUF + RDS_PAYLOAD
                ld      (hl), 0
                inc     hl
                ld      (hl), 0
                jr      rds_fin_write0

rds_fin_tail:
                ; pad the partial WORK tail to 510, terminal link 0,0, write it.
                ld      hl, RDS_WORK_BUF
                call    rds_pad_and_terminate
                ld      hl, (RDS_LINEAR)
                ld      (BD_REC_LINEAR), hl
                ld      hl, RDS_WORK_BUF
                call    bd_record_write_hw
                jr      nc, rds_fin_tail_cnt
                ld      a, 1
                ld      (RDS_ERR), a
rds_fin_tail_cnt:
                ld      hl, (RDS_SECCOUNT)
                inc     hl
                ld      (RDS_SECCOUNT), hl
                jr      rds_fin_write0

rds_fin_single:
                ; sector 0 is the whole file (<= 501 data bytes) and is the last
                ; sector: pad it, terminal link 0,0, count it. It is written below.
                ld      hl, RDS_FIRST_BUF
                call    rds_pad_and_terminate
                ld      hl, 1
                ld      (RDS_SECCOUNT), hl

rds_fin_write0:
                ; build the 9-byte header into RDS_FIRST_BUF[0..8], then write
                ; sector 0 at the fixed data base (linearSec 40).
                call    rds_build_header
                ld      hl, RDS_DATA_BASE
                ld      (BD_REC_LINEAR), hl
                ld      hl, RDS_FIRST_BUF
                call    bd_record_write_hw
                jr      nc, rds_fin_dirent
                ld      a, 1
                ld      (RDS_ERR), a
rds_fin_dirent:
                jp      render_disk_write_dirent  ; tail-call: build + write the dir entry

; ===========================================================================
; rds_pad_and_terminate — zero curbuf[RDS_FILL..509] and set link (curbuf+510/511)
; to the terminal 0,0. In: HL = buffer base; RDS_FILL = current payload count.
; Clobbers AF, BC, DE, HL.
; ===========================================================================
rds_pad_and_terminate:
                push    hl                      ; save the buffer base for the link write
                ld      de, (RDS_FILL)
                add     hl, de                  ; HL = &buf[RDS_FILL] (pad dest)
                ex      de, hl                  ; DE = &buf[RDS_FILL]
                ld      hl, RDS_PAYLOAD
                ld      bc, (RDS_FILL)
                or      a
                sbc     hl, bc                  ; HL = pad count = 510 - RDS_FILL
                ld      b, h
                ld      c, l                    ; BC = pad count (0..501)
                ld      a, b
                or      c
                jr      z, rds_pt_link          ; RDS_FILL == 510: nothing to pad
rds_pt_pad:
                xor     a
                ld      (de), a
                inc     de
                dec     bc
                ld      a, b
                or      c
                jr      nz, rds_pt_pad
rds_pt_link:
                pop     hl                      ; HL = buffer base
                ld      de, RDS_PAYLOAD
                add     hl, de                  ; HL = &buf[510]
                ld      (hl), 0
                inc     hl
                ld      (hl), 0
                ret

; ===========================================================================
; rds_compute_len_fields — from RDS_LEN compute RDS_LENMOD (len&0x3FFF),
; RDS_PAGES (len>>14) and RDS_STARTPG ((load>>14)-1). Clobbers AF, BC, DE, HL.
; ===========================================================================
rds_compute_len_fields:
                ; LengthMod16K = len & 0x3FFF  (low 14 bits of the 32-bit len).
                ld      hl, (RDS_LEN)
                ld      a, h
                and     &3F
                ld      h, a
                ld      (RDS_LENMOD), hl

                ; Pages = len >> 14. len is 32-bit LE in RDS_LEN[0..3]; shift right 14
                ; = take bytes 1..3 as a 24-bit value >> 6.
                ; len < 2^24 for every demo file, so byte 3 = 0 and len>>8 fits HL.
                ld      a, (RDS_LEN + 1)        ; bits 8..15
                ld      l, a
                ld      a, (RDS_LEN + 2)        ; bits 16..23
                ld      h, a                    ; HL = len >> 8
                ld      b, 6                    ; Pages = (len>>8) >> 6
rds_clf_pg:
                srl     h
                rr      l
                djnz    rds_clf_pg
                ld      a, l
                ld      (RDS_PAGES), a

                ; StartPage = (load >> 14) - 1 = (load>>8)>>6 = high byte >> 6, minus 1.
                ld      a, (RDS_LOAD + 1)       ; load bits 8..15
                srl     a
                srl     a
                srl     a
                srl     a
                srl     a
                srl     a                       ; A = load >> 14
                dec     a
                ld      (RDS_STARTPG), a
                ret

; ===========================================================================
; rds_build_header — write the 9-byte CODE header into RDS_FIRST_BUF[0..8] from
; the precomputed len fields + RDS_LOAD. Clobbers AF, HL.
; ===========================================================================
rds_build_header:
                ld      hl, RDS_FIRST_BUF
                ld      (hl), RDS_TYPE_CODE     ; +0 type
                inc     hl
                ld      a, (RDS_LENMOD)         ; +1 LengthMod16K low
                ld      (hl), a
                inc     hl
                ld      a, (RDS_LENMOD + 1)     ; +2 LengthMod16K high (<= 0x3F)
                ld      (hl), a
                inc     hl
                ; +3..4 PageOffset = (load & 0x3FFF) | 0x8000, LE.
                ld      a, (RDS_LOAD)
                ld      (hl), a
                inc     hl
                ld      a, (RDS_LOAD + 1)
                and     &3F
                or      &80
                ld      (hl), a
                inc     hl
                ld      (hl), &FF               ; +5 ExecAddrDiv16K (no auto-exec)
                inc     hl
                ld      (hl), &FF               ; +6 ExecAddrMod16K low (no auto-exec)
                inc     hl
                ld      a, (RDS_PAGES)          ; +7 Pages
                ld      (hl), a
                inc     hl
                ld      a, (RDS_STARTPG)        ; +8 StartPage
                ld      (hl), a
                ret

; ===========================================================================
; render_disk_write_dirent — build the 256-byte directory entry for the file and
; APPEND it into the first FREE directory slot, PRESERVING every existing entry (and
; the record's "BDOS"@232 validity stamp in linearSec 0). Clobbers AF, BC, DE, HL.
;
; Why the first free slot, not linearSec 0: B-DOS 1.5t's directory scan HALTS at the
; first type-0 (empty) entry, so a file appended after a type-0 gap is invisible to
; HGTHD (the i365d-b2c chain hang). Zeroing linearSec 0 also destroyed other files'
; entries — a shared-card data-safety bug. So we read-modify-write the sector holding
; the first free slot instead. Needs NETBOOT_WANT_RECORD_READ (bd_record_read_hw).
;
; Builds the entry in RDS_WORK_BUF[0..255]; RDS_FIRST_BUF (spent after the header
; cache is copied below) is reused as the scan + RMW sector buffer.
; ===========================================================================
render_disk_write_dirent:
                ; zero the 256-byte entry image.
                ld      hl, RDS_WORK_BUF
                ld      de, RDS_WORK_BUF + 1
                ld      bc, 255
                ld      (hl), 0
                ldir

                ; --- directory entry at +0 ---
                ld      hl, RDS_WORK_BUF
                ld      (hl), RDS_TYPE_CODE     ; +0x00 type 19 = CODE

                ; +0x01..0A name (10 bytes, already space-padded by the caller).
                ld      de, RDS_WORK_BUF + 1
                ld      hl, RDS_NAME
                ld      bc, 10
                ldir

                ; +0x0B..0C sector count, BIG-endian.
                ld      hl, (RDS_SECCOUNT)
                ld      a, h
                ld      (RDS_WORK_BUF + &0B), a ; high byte first
                ld      a, l
                ld      (RDS_WORK_BUF + &0C), a

                ld      a, 4                    ; +0x0D first-sector track (side0 cyl4)
                ld      (RDS_WORK_BUF + &0D), a
                ld      a, 1                    ; +0x0E first-sector sector (1-based)
                ld      (RDS_WORK_BUF + &0E), a

                ; +0x0F.. 195-byte SectorAddressMap: bits 0..SECCOUNT-1 set (the
                ; file is contiguous from linearSec 40, bitOffset = linear-40).
                call    rds_fill_bitmap

                ; +0xD3..DB 9-byte header cache = RDS_FIRST_BUF[0..8].
                ld      hl, RDS_FIRST_BUF
                ld      de, RDS_WORK_BUF + &D3
                ld      bc, 9
                ldir

                ld      a, (RDS_STARTPG)        ; +0xEC StartAddressPage
                ld      (RDS_WORK_BUF + &EC), a
                ; +0xED..EE StartAddressPageOffset = (load & 0x3FFF) | 0x8000, LE.
                ld      a, (RDS_LOAD)
                ld      (RDS_WORK_BUF + &ED), a
                ld      a, (RDS_LOAD + 1)
                and     &3F
                or      &80
                ld      (RDS_WORK_BUF + &EE), a
                ld      a, (RDS_PAGES)          ; +0xEF Pages
                ld      (RDS_WORK_BUF + &EF), a
                ld      a, (RDS_LENMOD)         ; +0xF0..F1 LengthMod16K, LE
                ld      (RDS_WORK_BUF + &F0), a
                ld      a, (RDS_LENMOD + 1)
                ld      (RDS_WORK_BUF + &F1), a
                ld      a, &FF                  ; +0xF2 ExecAddrDiv16K (no auto-exec)
                ld      (RDS_WORK_BUF + &F2), a
                ld      (RDS_WORK_BUF + &F3), a ; +0xF3..F4 ExecAddrMod16K = 0xFFFF
                ld      (RDS_WORK_BUF + &F4), a

                ; --- find the first FREE directory slot (type 0) --------------
                ; Scan linearSec 0..RDS_DIR_SECS-1, two 256-byte entries each. RDS_
                ; FIRST_BUF is spent (header cached above) -> reuse as the read buffer.
                ld      hl, 0
                ld      (rds_dir_lin), hl
rds_dirent_scan:
                ld      hl, (rds_dir_lin)
                ld      (BD_REC_LINEAR), hl
                ld      hl, RDS_FIRST_BUF
                call    bd_record_read_hw       ; read the dir sector
                jr      c, rds_dirent_err       ; read failure
                ; slot 0 of linearSec 0 is reserved: its +232 holds the "BDOS"
                ; record-validity stamp, which a file entry spliced here would zero.
                ; Never place there — start linearSec 0 at slot 1. (Real records keep
                ; the system 'bdos' entry in slot 0, so this only matters for a
                ; freshly-formatted empty record.)
                ld      hl, (rds_dir_lin)
                ld      a, h
                or      l
                jr      z, rds_dirent_slot1     ; linearSec 0 -> skip slot 0
                ld      a, (RDS_FIRST_BUF)      ; slot 0 type
                and     a
                jr      nz, rds_dirent_slot1
                ld      hl, 0                   ; slot 0 free -> offset 0
                jr      rds_dirent_place
rds_dirent_slot1:
                ld      a, (RDS_FIRST_BUF + 256) ; slot 1 type
                and     a
                jr      nz, rds_dirent_advance
                ld      hl, 256                 ; slot 1 free -> offset 256
                jr      rds_dirent_place
rds_dirent_advance:
                ld      hl, (rds_dir_lin)      ; both full -> next sector
                inc     hl
                ld      (rds_dir_lin), hl
                ld      a, l
                cp      RDS_DIR_SECS
                jr      c, rds_dirent_scan
                jr      rds_dirent_err          ; directory full -> no free slot
rds_dirent_place:
                ; HL = slot offset (0 or 256) within the read sector in RDS_FIRST_BUF.
                ld      de, RDS_FIRST_BUF
                add     hl, de
                ex      de, hl                  ; DE = RDS_FIRST_BUF + offset (dest)
                ld      hl, RDS_WORK_BUF        ; source = the built entry
                ld      bc, 256
                ldir                            ; splice the entry in, preserving the rest
                ; write the modified sector back at its linearSec.
                ld      hl, (rds_dir_lin)
                ld      (BD_REC_LINEAR), hl
                ld      hl, RDS_FIRST_BUF
                call    bd_record_write_hw
                ret     nc
rds_dirent_err:
                ld      a, 1
                ld      (RDS_ERR), a
                ret

RDS_DIR_SECS:   equ     40                     ; record directory sectors (tracks 0-3)
rds_dir_lin:    defw    0                      ; the directory linearSec being scanned

; ===========================================================================
; rds_fill_bitmap — set the first RDS_SECCOUNT bits of the 195-byte map at
; RDS_WORK_BUF+0x0F (whole 0xFF bytes for SECCOUNT>>3, then a partial byte with
; the low SECCOUNT&7 bits set). Clobbers AF, BC, DE, HL.
; ===========================================================================
rds_fill_bitmap:
                ld      hl, RDS_WORK_BUF + &0F
                ld      bc, (RDS_SECCOUNT)
                ; B = full 0xFF bytes = SECCOUNT >> 3 ; C's low 3 bits = remainder.
                ld      a, c
                and     7                       ; A = remainder bits (0..7)
                push    af
                ; whole bytes = SECCOUNT >> 3 (16-bit >> 3 into DE).
                ld      d, b
                ld      e, c
                srl     d
                rr      e
                srl     d
                rr      e
                srl     d
                rr      e                       ; DE = SECCOUNT >> 3
rds_bm_full:
                ld      a, d
                or      e
                jr      z, rds_bm_partial
                ld      (hl), &FF
                inc     hl
                dec     de
                jr      rds_bm_full
rds_bm_partial:
                pop     af                      ; A = remainder bit count (0..7)
                or      a
                ret     z                       ; exact byte boundary: done
                ld      b, a
                xor     a                       ; build a mask of the low B bits
rds_bm_bit:
                scf
                rla                             ; A = (A<<1)|1
                djnz    rds_bm_bit
                ld      (hl), a
                ret
