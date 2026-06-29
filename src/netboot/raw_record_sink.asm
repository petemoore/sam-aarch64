; raw_record_sink.asm — the i122b streaming raw-record write sink (the Z80 port
; of the Go authority tools/netboot-oracle/bdos/raw_sink.go::RawSink).
;
; This is the DiskRecord storage-class persist leaf (docs/specs/netboot-storage-
; manifest-design.md §6.5): it re-blocks the netboot body byte-stream into 512-byte
; sectors and writes each full sector immediately into the currently-HRECORD-
; selected Trinity record via i114c's HWSAD seam (bdos_write_record), at
; consecutive linear sectors 0, 1, 2, … of that record.
;
; Why streaming, not buffer-then-save: a Trinity record is exactly 819,200 bytes
; (1600 sectors) — far more than the bounded RAM the i99 streaming receive holds at
; once. The fetched disk image can't be buffered whole and HSAVE'd (that is the
; fw_span flat-file path); it must be flushed sector-by-sector as it arrives, so
; RAM never holds more than one sector. raw_record_sink_leaf is the drop-in body
; sink for the bootable DiskRecord fetch: the orchestration points BODY_DST_PTR at
; it (the same seam http_main uses for storage_sink_leaf), HRECORD-selects the
; target record, calls raw_record_sink_reset, streams, then raw_record_sink_finish.
;
; The body arrives in arbitrary-sized chunks (each i99 flush window is forwarded
; whole by body_sink_write, and a window is neither sector-sized nor sector-
; aligned), so the sink carries a partial sector across calls in BD_WRITE_BUF: a
; chunk may complete several sectors, complete none, or straddle a boundary.
;
; BUFFER REUSE: the accumulator IS bdos_seam.asm's BD_WRITE_BUF — the 512-byte
; staging buffer bdos_write_record reads from — so a full sector flushes with no
; extra copy. The sink owns BD_WRITE_BUF / BD_WRITE_START / BD_WRITE_COUNT while
; streaming.
;
; VERIFICATION (CLAUDE.md §5): host-verifiable — raw_record_sink_test.go feeds the
; identical chunk sequence to this sink (under AttachBDOS, capturing the HWSAD
; dispatch in BDOSStore.SectorWrites) and to the Go RawSink, and asserts the
; emitted sector writes (linear index + 512 bytes each) match byte-for-byte. NOT
; host-verifiable: the real RST 8 HWSAD per sector + the real SD write — gated on
; real Trinity, exactly as HSAVE is. Emulation-verified ≠ hardware-verified.
;
; This module references bdos_seam.asm symbols (BD_WRITE_BUF, BD_WRITE_START,
; BD_WRITE_COUNT, bdos_write_record), which exist only in the bootable build
; (NETBOOT_HOSTTEST==0), so it is included there, never built standalone.

; ===========================================================================
; State (data first so every label is defined before the code referencing it).
; ===========================================================================
RRS_FILL:    defs 2          ; bytes pending in BD_WRITE_BUF (0..511 between calls)
RRS_RECORD:  defs 2          ; the 1-based claimed record the OWN-CMD24 path writes into (set by the
                             ;   caller before the first chunk when RRS_OWN_CMD24=1; i194)
RRS_OWN_CMD24: defs 1        ; 0 = write each sector via the B-DOS HWSAD hook (bdos_write_record),
                             ;   the i122 client fetch-and-store path. 1 = write by ABSOLUTE card
                             ;   LBA via our OWN self-healing CMD24 (bd_record_write_hw), the i194
                             ;   serve disk-push path that bypasses the flaky B-DOS hooks (§8ag).
                             ;   raw_record_sink_reset clears it to 0 (HWSAD default); the serve
                             ;   push sets it to 1 after reset.
RRS_LINEAR:  defs 2          ; next linear sector index to write (0-based)
RRS_SRC:     defs 2          ; current source pointer (carried across the copy loop)
RRS_REM:     defs 2          ; bytes of the current chunk still to consume
RRS_N:       defs 2          ; bytes to copy this iteration = min(avail, remaining)
RRS_TOTAL:   defs 4          ; running count of every byte streamed (32-bit LE = image size)

; ===========================================================================
; raw_record_sink_reset — start a fresh record: RRS_FILL = 0, RRS_LINEAR = 0,
; RRS_TOTAL = 0. Call once after HRECORD-selecting the target record, before the
; first chunk. Clobbers HL.
; ===========================================================================
raw_record_sink_reset:
                ld      hl, 0
                ld      (RRS_FILL), hl
                ld      (RRS_LINEAR), hl
                ld      (RRS_TOTAL), hl
                ld      (RRS_TOTAL + 2), hl
                xor     a
                ld      (RRS_OWN_CMD24), a      ; default = HWSAD path (the i122 client); the
                                                ; serve push sets RRS_OWN_CMD24=1 after this reset
                ret

; ===========================================================================
; raw_record_sink_leaf — accumulate one body chunk, flushing each full sector.
; In:  HL = chunk pointer, BC = chunk length (may be 0).
; Clobbers: A, BC, DE, HL.
;
; Mirrors RawSink.Write: while bytes remain, append min(512-fill, remaining) into
; BD_WRITE_BUF at the current fill; when fill reaches 512, write that sector via
; bdos_write_record and reset fill to 0.
; ===========================================================================
raw_record_sink_leaf:
                ld      (RRS_SRC), hl           ; stash chunk ptr + length so LDIR
                ld      (RRS_REM), bc           ; (which consumes BC) can run in the loop

                ; RRS_TOTAL += BC (32-bit image-size counter += this chunk length).
                ld      hl, (RRS_TOTAL)
                add     hl, bc
                ld      (RRS_TOTAL), hl
                jr      nc, rrs_loop            ; no carry into the high word
                ld      hl, (RRS_TOTAL + 2)
                inc     hl
                ld      (RRS_TOTAL + 2), hl

rrs_loop:
                ; done when no chunk bytes remain.
                ld      hl, (RRS_REM)
                ld      a, h
                or      l
                ret     z

                ; avail = 512 - RRS_FILL  (RRS_FILL is 0..511 here, so avail is 1..512).
                ld      hl, 512
                ld      de, (RRS_FILL)
                or      a
                sbc     hl, de                  ; HL = avail

                ; n = min(avail, RRS_REM).
                ld      bc, (RRS_REM)
                push    hl                      ; save avail
                or      a
                sbc     hl, bc                  ; CF = 1 iff avail < remaining
                pop     hl                      ; HL = avail again
                jr      c, rrs_have_n           ; avail < remaining -> n = avail
                ld      h, b
                ld      l, c                    ; avail >= remaining -> n = remaining
rrs_have_n:
                ld      (RRS_N), hl             ; n (1..512)

                ; dest = BD_WRITE_BUF + RRS_FILL.
                ld      hl, BD_WRITE_BUF
                ld      de, (RRS_FILL)
                add     hl, de
                ex      de, hl                  ; DE = dest

                ; copy n bytes: src -> dest.
                ld      hl, (RRS_SRC)
                ld      bc, (RRS_N)
                ldir                            ; HL = src+n, DE = dest+n, BC = 0
                ld      (RRS_SRC), hl           ; advance the source pointer

                ; RRS_FILL += n.
                ld      hl, (RRS_FILL)
                ld      bc, (RRS_N)
                add     hl, bc
                ld      (RRS_FILL), hl

                ; RRS_REM -= n.
                ld      hl, (RRS_REM)
                ld      bc, (RRS_N)
                or      a
                sbc     hl, bc
                ld      (RRS_REM), hl

                ; flush when the sector is full (RRS_FILL == 512).
                ld      hl, (RRS_FILL)
                ld      de, 512
                or      a
                sbc     hl, de
                jr      nz, rrs_loop            ; not full yet: take the next chunk slice
                call    rrs_flush_sector
                jr      rrs_loop

; ===========================================================================
; raw_record_sink_finish — flush a final partial sector, zero-padded to 512, if
; any bytes are buffered (RRS_FILL > 0); a no-op on a sector boundary (the exact
; 819,200-byte disk-record case ends here with nothing to do). Call once at
; end-of-stream. Clobbers A, BC, DE, HL.
; ===========================================================================
raw_record_sink_finish:
                ld      hl, (RRS_FILL)
                ld      a, h
                or      l
                ret     z                       ; ended on a sector boundary: nothing buffered

                ; pad_count = 512 - RRS_FILL  (1..511 here, since fill != 0 and < 512).
                ld      hl, 512
                ld      de, (RRS_FILL)
                or      a
                sbc     hl, de                  ; HL = pad_count
                ld      b, h
                ld      c, l                    ; BC = pad_count
                ; dest = BD_WRITE_BUF + RRS_FILL.
                ld      hl, BD_WRITE_BUF
                add     hl, de                  ; HL = &BD_WRITE_BUF[fill]  (DE = RRS_FILL)
rrs_pad:
                ld      (hl), 0                 ; zero-pad the tail of the sector
                inc     hl
                dec     bc
                ld      a, b
                or      c
                jr      nz, rrs_pad
                jp      rrs_flush_sector        ; tail-call: write the padded sector + advance

; ===========================================================================
; rrs_flush_sector — write the full BD_WRITE_BUF (512 bytes) at linear sector
; RRS_LINEAR, then advance RRS_LINEAR and reset RRS_FILL to 0. Two write backends,
; selected by RRS_OWN_CMD24:
;
;   RRS_OWN_CMD24 == 0 (the i122 client fetch-and-store path): write via the B-DOS
;     HWSAD hook (bdos_write_record, one sector) into the currently HRECORD-selected
;     record — the caller selected it.
;
;   RRS_OWN_CMD24 == 1 (the i194 serve disk-push path): write by ABSOLUTE card LBA
;     with our OWN self-healing CMD24 (bd_record_write_hw), bypassing the B-DOS HWSAD
;     hook. The B-DOS hooks flakily HANG on real hardware (they rely on boot-time
;     SPI-mode persistence the serve's ENC reset disturbs, §8ag); bd_record_write_hw
;     re-runs sdc_init_ladder every call, so it self-heals. The target record is
;     RRS_RECORD (the claimed free record handle_wrq armed the sink with) and the LBA
;     is absolute (csd_base + 1600*(rec-1) + linearSec) — no B-DOS "current record"
;     state is consulted, so HRECORD-select is unneeded for the write.
;
; Clobbers A, BC, DE, HL.
; ===========================================================================
rrs_flush_sector:
                if defined(NETBOOT_DEBUG)
                ; A full sector is staged and we are about to write it. FLUSH_PRE
                ; brackets the write (DBG_FLUSH_PRE -> the next marker gap = the
                ; per-sector write over the bit-banged SPI).
                ld      a, DBG_FLUSH_PRE
                call    dbg_marker
                endif
                ld      a, (RRS_OWN_CMD24)
                or      a
                jr      nz, rrs_flush_own_cmd24

                ; --- HWSAD path (client): bdos_write_record, one sector ---
                ld      hl, (RRS_LINEAR)
                ld      (BD_WRITE_START), hl    ; write at the current linear sector
                ld      hl, 1
                ld      (BD_WRITE_COUNT), hl    ; exactly one sector (BD_WRITE_BUF)
                call    bdos_write_record       ; HWSAD the sector into the selected record
                jr      rrs_flush_advance

rrs_flush_own_cmd24:
                ; --- OWN CMD24 path (serve push): absolute-LBA write of (RRS_RECORD, linear) ---
                ld      hl, (RRS_RECORD)        ; 1-based claimed record
                ld      (BD_REC_WRITE_REC), hl
                ld      hl, (RRS_LINEAR)        ; 0-based linear sector within the record
                ld      (BD_REC_WRITE_LINEAR), hl
                ld      hl, BD_WRITE_BUF        ; source = the 512-byte staging buffer
                call    bd_record_write_hw      ; own CMD24 to absolute LBA of (record, linearSec)

rrs_flush_advance:
                ld      hl, (RRS_LINEAR)
                inc     hl
                ld      (RRS_LINEAR), hl        ; next sector
                ld      hl, 0
                ld      (RRS_FILL), hl          ; buffer drained
                ret
