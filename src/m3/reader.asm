; reader.asm — .tbn record stream walker.
;
; Per docs/specs/2026-05-23-m1-binary-tokenised-format-design.md §2-§3:
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
; The reader owns three globals: IN_POS (current read pointer),
; IN_END (one past the last valid byte), and PC (current emit address —
; we track it locally even though we don't emit relocatable code; it's
; useful for diagnostics and for any PC-relative offset that might
; survive the constant fold).
;
; The buffer for the .tbn file is IN_BUF (defined in assembler.asm).
; The reader does NOT do disk I/O — that's the caller's job.  It
; expects (IN_POS) initialised to IN_BUF and IN_END to IN_BUF + filesize.


; -----------------------------------------------------------------------
; reader_init — validate magic + version, skip name table, position
; IN_POS at the first record's kind byte.
;
; Input:  none (IN_POS and IN_END must be set by caller).
; Output: IN_POS advanced past the header + name table.
; On bad magic / version: jp fail.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
reader_init:
                ld      hl, (IN_POS)

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
; HL += BC.
                add     hl, bc
                pop     de
                dec     de
                jp      reader_init_skip_names

reader_init_done:
                ld      (IN_POS), hl
                ret


; -----------------------------------------------------------------------
; reader_at_end — Z flag = 1 if no records remain.
;
; Output: Z=1 if (IN_POS) >= (IN_END); Z=0 otherwise.
;         HL preserved? — clobbered.
; -----------------------------------------------------------------------
reader_at_end:
                ld      hl, (IN_END)
                ld      de, (IN_POS)
                or      a
                sbc     hl, de
                ret     z                   ; pos == end → Z=1
                ; CY=1 means (end - pos) < 0 → pos > end (corrupt); treat as end.
                ret     c
                ld      a, 1                ; Z=0
                or      a
                ret


; -----------------------------------------------------------------------
; reader_next_kind — fetch next record's kind byte and length.
;
; Input:  caller has confirmed not-at-end.
; Output: A = kind byte, BC = payload length (u16), HL = pointer to
;         payload start.  IN_POS is left at HL+BC (start of next record).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
reader_next_kind:
                ld      hl, (IN_POS)
                ld      a, (hl)             ; A = kind
                ld      (reader_curr_kind), a
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                  ; HL → payload start
                ld      (reader_curr_payload), hl
                ld      (reader_curr_len), bc
; Advance IN_POS = HL + BC.
                push    hl
                add     hl, bc
                ld      (IN_POS), hl
                pop     hl
                ld      a, (reader_curr_kind)
                ret


; -----------------------------------------------------------------------
; Scratch / globals.
; -----------------------------------------------------------------------
reader_name_count:      defw    0
reader_curr_kind:       defb    0
reader_curr_payload:    defw    0
reader_curr_len:        defw    0
