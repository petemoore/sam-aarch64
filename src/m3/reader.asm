; reader.asm — .tbn record stream walker (paged IN — M6 PR 2).
;
; Per docs/specs/2026-05-23-m1-binary-tokenised-format-design.md §2-§3
; for the format; docs/specs/2026-05-27-m6-paged-in-design.md for the
; paging design.
;
;   File layout:
;     ┌────────────────────────────┐
;     │ Magic   "SA64"   (4 bytes) │
;     │ Version u16 LE             │
;     │ Flags   u16 LE             │
;     ├────────────────────────────┤
;     │ Name table                 │
;     │   [count u16][name₀][name₁]…
;     │   each name: [len u16][bytes]
;     ├────────────────────────────┤
;     │ Statement records          │
;     │   [kind u8][len u16][payload]
;     └────────────────────────────┘
;
; Record kinds (M1 §3):
;   0x01 INST       [mnemonic_id u16][operand_count u8][operands…]
;   0x02 LABEL_DEF  [symbol_id u16]                                — M4
;   0x03 LOCAL_DEF  [digit u8]                                     — M4
;   0x04 DIRECTIVE  [directive_id u8][operand_count u8][operands…]
;   0x05 COMMENT    [placement u8][bytes…]                          — skip
;
; The reader's storage now lives in main_loop.asm as a 24-bit (page,
; offset) cursor pair: IN_POS_PAGE / IN_POS_OFFSET (current), and
; IN_END_PAGE / IN_END_OFFSET (one past last byte).  IN is resident in
; physical pages 7..12 (HLOAD'd once by load_in_file at startup); each
; reader_next_kind brackets a brief LMPR=&27-derived window mapping the
; current IN page into section A, stages the record into STAGING_BUF
; (section D), and restores LMPR_ENCTAB before returning.
;
; This means callers see a stable section-D pointer instead of a section-C
; one — the only observable change in the reader's external ABI.


; -----------------------------------------------------------------------
; reader_init — validate magic + version, skip name table, position the
; cursor at the first record's kind byte.
;
; Input:  cursor (IN_POS_*) initialised to the start of IN by
;         reset_reader_to_in_buf; IN_END_* set by load_in_file.
; Output: cursor advanced past the header + name table.  LMPR restored
;         to LMPR_ENCTAB on success.
; On bad magic / version: jp fail (without restoring LMPR — fail's
; printer-channel banner runs under DI'd interrupts; the wrong LMPR
; doesn't affect anything before halt).
; Clobbers: A, BC, DE, HL.
;
; Note on the magic / version / flags reads: HL starts at &0000 and we
; do at most 8 INC HL before reaching the name-count word.  HL stays
; well under &4000 (section A) throughout, so no page-cross
; renormalisation is needed before the name table.
;
; Implementation detail (intentional, not a bug): if a future on-disk
; header were to grow past 8 bytes such that the magic/version/flags
; sequence crossed &4000, the high bytes would be read via section B
; under LMPR=&27.  Section B at LMPR=&27 maps to page 8 = IN[1], the
; next IN page — which gives the correct bytes by virtue of section B =
; LMPR-low+1.  This matches the COMET adjustpo idiom and is robust.
; The name-table walk below DOES call in_normalise_hl after each
; HL += name_len step because BC may be hundreds of bytes — too large
; to rely on the section-B implicit window.
; -----------------------------------------------------------------------
reader_init:
                call    in_map_current

                ld      hl, 0           ; section-A address of IN[0]

; -- Validate "SA64" -----------------------------------------------------
                ld      a, (hl)
                cp      "S"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "A"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "6"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "4"
                jp      nz, fail
                inc     hl

; -- Validate version u16 LE = 1 ---------------------------------------
                ld      a, (hl)
                cp      1
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                or      a
                jp      nz, fail
                inc     hl

; -- Skip flags u16 (whatever it is) -----------------------------------
                inc     hl
                inc     hl

; -- Name table: [count u16][names…] each name = [len u16][bytes] -------
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                  ; HL → first name (if count > 0)
                ld      (reader_name_count), de

reader_init_skip_names:
                ld      a, d
                or      e
                jr      z, reader_init_done

                push    de                  ; remaining count
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                  ; HL → name bytes; BC = len
; HL += BC — may push HL past &3FFF.  Renormalise into section A so
; subsequent name reads stay in [&0000, &4000) with LMPR bumped if we
; crossed an IN page boundary.
                add     hl, bc
                call    in_normalise_hl
                pop     de
                dec     de
                jp      reader_init_skip_names

reader_init_done:
; Persist position back to the cursor, then restore LMPR_ENCTAB so the
; encoder can read ENCTAB.
                call    in_persist_hl
                jp      enctab_map_in           ; tail-call; restores LMPR


; -----------------------------------------------------------------------
; reader_at_end — Z flag = 1 if no records remain.
;
; Output: Z=1 if cursor == end; Z=0 otherwise.
;         Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
reader_at_end:
                ld      a, (IN_POS_PAGE)
                ld      b, a
                ld      a, (IN_END_PAGE)
                cp      b
                jr      nz, reader_at_end_no
                ld      hl, (IN_POS_OFFSET)
                ld      de, (IN_END_OFFSET)
                or      a                   ; clear CF
                sbc     hl, de
                ret     z                   ; equal → Z=1 (at end)
                ; pos > end is impossible under correct walking; we
                ; conservatively treat it as "at end" same as the old
                ; flat-pointer reader did.
                ret     c                   ; SBC underflowed → pos > end (impossible)
reader_at_end_no:
                ld      a, 1
                or      a                   ; Z=0
                ret


; -----------------------------------------------------------------------
; reader_next_kind — fetch the next record into STAGING_BUF and return
; A=kind, BC=payload length, HL=STAGING_BUF.
;
; Per docs/specs/2026-05-27-m6-paged-in-design.md §"reader_next_kind
; (paged)".  Brackets an LMPR=IN_POS_PAGE window around the read; copies
; [payload] bytes from section A into STAGING_BUF; renormalises across
; IN page boundaries during the copy.  Restores LMPR_ENCTAB before
; returning so the encoder window (ENCTAB in section A, OUT-low in
; section B as LMPR_ENCTAB+1) is live on entry to the record handler.
;
; The inner copy loop uses LDI, which sets P/V = (BC != 0).  Continue
; via JP PE saves a `ld a, b / or c / jr nz` per iteration compared to
; the open-coded loop.  Citation: Z80 datasheet, LDI flag effects.
;
; Input:  caller has confirmed not-at-end via reader_at_end.
; Output: A = kind byte, BC = payload length (u16), HL = STAGING_BUF.
;         Cursor advanced past this record.  LMPR = LMPR_ENCTAB.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
reader_next_kind:
                call    in_map_current

                ld      hl, (IN_POS_OFFSET)
                ld      a, (hl)             ; A = kind
                ld      (reader_curr_kind), a
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                  ; HL → payload start; BC = len
                ld      (reader_curr_len), bc

; Bounds check: payload length must fit in STAGING_BUF.
; STAGING_BUF size = STAGING_BUF_END - STAGING_BUF = &400 (1024 B).
; Overflow → silent corruption of LITPOOL_EXPR_BUF; fail cleanly instead.
                ld      a, b
                cp      (STAGING_BUF_END - STAGING_BUF) >> 8
                jr      c, reader_payload_size_ok
                ld      a, &01
                jp      fail_with_tag       ; tag 01: STAGING_BUF overflow
reader_payload_size_ok:
; Note: the 3-byte header above does NOT call in_normalise_hl between
; INC HL steps.  If the header straddled &3FFF..&4001, the high bytes
; would be read via section B under LMPR=IN_POS_PAGE — and section B at
; LMPR=&27+N maps to page 8+N = the NEXT IN page, which is exactly
; where the header's high bytes physically live.  So the read is
; correct by virtue of the COMET-style "LMPR+1 = section B" invariant.
; This is intentional, not a bug — the explicit copy loop below DOES
; renormalise because the loop may step many bytes past section A,
; landing further than section B can cover.

; Copy payload [BC bytes] from section A (HL) to STAGING_BUF (DE).
                ld      de, STAGING_BUF
                ld      (reader_curr_payload), de
                ld      a, b
                or      c
                jr      z, reader_next_kind_no_payload

reader_next_kind_copy_loop:
                ld      a, h
                cp      &40
                jr      c, reader_next_kind_copy_byte
; Page-cross: renormalise.  Subtract &40 from H and bump LMPR low 5 bits.
                sub     &40
                ld      h, a
                in      a, (250)
                inc     a
                out     (250), a
reader_next_kind_copy_byte:
                ldi                          ; (DE) := (HL); HL++; DE++; BC--
                jp      pe, reader_next_kind_copy_loop
                                             ; P/V = (BC != 0); continue while non-zero
                                             ; (Z80 datasheet, LDI flag effects)

reader_next_kind_no_payload:
; HL points one past the last payload byte.  If the record ended
; exactly on a page boundary (or no payload was copied past the
; 3-byte header that may itself have hit &3FFE..&4001), HL may be
; >= &4000.  Renormalise before persisting so the cursor reaches a
; canonical (page, [0, &4000)) form.  Without this, a subsequent
; reader_at_end could compare cursor=(N, &4000) vs end=(N+1, 0) —
; semantically equal but byte-wise different — and falsely report
; "not at end", causing an infinite loop in walk_records.
                call    in_normalise_hl
                call    in_persist_hl
                call    enctab_map_in        ; LMPR back to LMPR_ENCTAB

                ld      hl, (reader_curr_payload)   ; HL = STAGING_BUF
                ld      bc, (reader_curr_len)
                ld      a, (reader_curr_kind)
                ret


; -----------------------------------------------------------------------
; Scratch / globals.
; -----------------------------------------------------------------------
reader_name_count:      defw    0
reader_curr_kind:       defb    0
reader_curr_payload:    defw    0
reader_curr_len:        defw    0
