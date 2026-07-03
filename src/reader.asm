; reader.asm — .tbn record stream walker (paged IN — M6 PR 2).
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-23-m1-binary-tokenised-format-design.md §2-§3
; for the format; docs/specs/paged-in-design.md for the
; paging design.
;
;   File layout (compact `.tbn` v2; editor-region split, M8 / i39b-2):
;     ┌──────────────────────────────────┐
;     │ Magic   "SA64"   (4 bytes)       │
;     │ Version u16 LE                   │
;     │ Flags   u16 LE                   │
;     │ editor_region_offset u32 LE      │ section index → start of editor region
;     ├──────────────────────────────────┤ ── assembler-facing region ──
;     │ Header label table               │  [count u16][name_id u16][delta uvarint]…
;     │ Header local table               │  [count u16][digit u8][delta uvarint]…
;     │ Statement records                │  [kind u8][len u16][payload]…
;     ├──────────────────────────────────┤ ── editor region (at editor_region_offset) ──
;     │ Name table (front-coded)         │  the assembler NEVER reads these:
;     │ .global flags                    │  it stops the record walk at the
;     │ Comment sidecar                  │  boundary (IN_END) and resolves
;     └──────────────────────────────────┘  symbols by name_id via the header tables.
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
; IN_END_PAGE / IN_END_OFFSET (one past last byte).  IN is resident in a
; CONTIGUOUS run of physical pages allocated from the i2 page pool by
; load_in_file at startup (pp_alloc_run; base in IN_BASE_LMPR — page 7 on a
; 256 KB SAM, up to the 16..31 run on 512 KB — i23).  Each reader_next_kind
; brackets a brief IN_BASE_LMPR-derived window mapping the current IN page into
; section A, stages the record into STAGING_BUF (section D), and restores
; LMPR_ENCTAB before returning.  Because the run is contiguous, crossing a page
; boundary is still a plain LMPR increment.
;
; This means callers see a stable section-D pointer instead of a section-C
; one — the only observable change in the reader's external ABI.


; -----------------------------------------------------------------------
; reader_init — validate magic + version, read the section index to bound
; the record walk at the editor region, then parse the header tables and
; position the cursor at the first record's kind byte.
;
; Input:  cursor (IN_POS_*) initialised to the start of IN by
;         reset_reader_to_in_buf; IN_END_* set by load_in_file (overwritten
;         here with the editor-region boundary).
; Output: IN_END_* = the editor-region boundary; cursor advanced past the
;         header tables to the first record.  LMPR restored to LMPR_ENCTAB
;         on success.
; On bad magic / version: jp fail (without restoring LMPR — fail's
; printer-channel banner runs under DI'd interrupts; the wrong LMPR
; doesn't affect anything before halt).
; Clobbers: A, BC, DE, HL.
;
; Note on the header reads: HL starts at &0000 and we do at most 12 INC HL
; (magic 4 + version 2 + flags 2 + section index 4) before the header
; tables.  HL stays well under &4000 (section A) throughout, so no
; page-cross renormalisation is needed reading the fixed header.
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

; -- Validate version u16 LE = 3 (packed patch headers, i39c) -----------
                ld      a, (hl)
                cp      3
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                or      a
                jp      nz, fail
                inc     hl

; -- Skip flags u16 (whatever it is) -----------------------------------
                inc     hl
                inc     hl

; -- Section index: editor_region_offset u32 LE (M8 / i39b-2) ----------
;    The assembler-facing records end at this byte offset from file start;
;    the editor region (front-coded name table, .global flags, comment
;    sidecar) follows it and the assembler NEVER reads it — symbols
;    resolve by name_id via the header tables.  Set the record-walk
;    terminator (IN_END) to this boundary so walk_records stops there,
;    then jump straight to the header tables (no name table at the front).
;    HL is at section-A offset 8 (< &4000), so the bytes read with plain
;    INC HL — no page-cross handling.  A loadable .tbn is < 256 KB (the
;    largest contiguous pool run, i23), so the offset is < &40000: byte 3 is
;    zero and the page index (0..15) fits.
                ld      a, (hl)                     ; b0 = offset bits 0..7
                inc     hl
                ld      e, a
                ld      a, (hl)                     ; b1 = offset bits 8..15
                inc     hl
                ld      d, a
                ld      a, (hl)                     ; b2 = offset bits 16..23
                ld      c, a

;   page_index = (b2 << 2) | (b1 >> 6);  offset = ((b1 & &3F) << 8) | b0.
;   IN_END = (IN_BASE_LMPR + page_index, offset) — one past the last
;   assembler-facing byte.  Overwrites load_in_file's true-file-end IN_END
;   (only reader_at_end reads IN_END; the editor region past the boundary
;   is never walked).  reader_init runs once per pass — the recompute is
;   idempotent.
                ld      a, d                        ; b1
                rlca
                rlca
                and     3                           ; A = b1 >> 6
                ld      b, a
                ld      a, c                        ; b2 (0..3 for ≤256 KB)
                rlca
                rlca                                ; A = b2 << 2
                add     a, b                        ; A = page_index (0..15)
                ld      b, a                        ; B = page_index
                ld      a, (IN_BASE_LMPR)           ; run base LMPR (RAM0 | page)
                add     a, b                        ; A = end LMPR (base + page_index)
                ld      (IN_END_PAGE), a
                ld      a, d                        ; b1
                and     &3F
                ld      h, a
                ld      l, e                        ; HL = ((b1 & &3F) << 8) | b0
                ld      (IN_END_OFFSET), hl

;   The header label/local tables start at the fixed byte offset 12
;   (magic 4 + version 2 + flags 2 + section index 4).
                ld      hl, 12
                jp      reader_init_header_tables


; -----------------------------------------------------------------------
; Header tables (compact `.tbn` v2 — i39a PR(d)).
;
; Between the name table and the first record the file carries two tables
; that move label / numeric-local DEFINITIONS out of the record stream:
;
;   Label table:  [count u16 LE]  then count rows: [name_id u16 LE][delta uvarint]
;   Local table:  [count u16 LE]  then count rows: [digit u8][delta uvarint]
;
; Each row's `delta` is the increase in the byte offset from the origin
; over the previous row; rows are in ascending-offset order, so the
; absolute offset = running sum of deltas (each table's accumulator
; starts at 0).  See tools/sam-aarch64-format/header_tables.go
; (write/readLabelTable, write/readLocalTable) — this mirrors it.
;
; Origin-offset == symbol value (load-bearing assumption): every gated
; target/fixture origin is 4GB-aligned (low 32 bits == 0 — spectrum4 is
; 0xfffffff000000000, fixtures are 0), so `offset` IS the low-32-bits
; value the Z80 stores as a symbol's PASS_PC-style address.  We therefore
; store the accumulated offset DIRECTLY as the symbol/local value — no
; origin arithmetic.  The release-gate byte-match gate is the backstop that
; would catch any future non-4GB-aligned origin (none exists today).
;
; reader_init runs once per pass.  Seeding the symbol / local tables must
; happen exactly once, so it is gated on PASS_MODE == PASS_PASS1
; (symbol_table_init / local_label_table_init run before pass 1's
; reset_reader_to_in_buf; pass 2 reuses the built tables — it must parse
; the tables to advance HL past them, but must NOT re-insert).  We latch
; pass-1-ness once into reader_seed_pass1 and reuse it for both tables.
;
; The accumulator doubles as both the running 32-bit offset AND the value
; symbol_insert / local_def_append consume: symbol_value_buf for labels,
; local_label_pc_buf for locals — both live in fixed RAM, so they survive
; the insert calls (which clobber all registers).
; -----------------------------------------------------------------------
reader_init_header_tables:
; The IN reading cursor lives in (reader_in_cursor) throughout this stage
; — a single memory pointer, since the insert calls (symbol_insert /
; local_def_append) clobber all registers including HL.  Seed it from the
; HL we arrived with (section-A offset of the label-table count word).
                ld      (reader_in_cursor), hl

; Latch pass-1-ness once (1 = seed, 0 = parse-only) for both tables.
                ld      a, (PASS_MODE)
                cp      PASS_PASS1
                ld      a, 0
                jr      nz, reader_seed_set
                inc     a                           ; A = 1
reader_seed_set:
                ld      (reader_seed_pass1), a

; ----- Label table ----------------------------------------------------
; Read count u16 LE → reader_table_count.
                call    reader_read_next_byte       ; A = count LSB
                ld      c, a
                call    reader_read_next_byte       ; A = count MSB
                ld      b, a
                ld      (reader_table_count), bc

; Zero the label accumulator (symbol_value_buf) — running 32-bit offset.
                xor     a
                ld      (symbol_value_buf + 0), a
                ld      (symbol_value_buf + 1), a
                ld      (symbol_value_buf + 2), a
                ld      (symbol_value_buf + 3), a

reader_label_loop:
                ld      bc, (reader_table_count)
                ld      a, b
                or      c
                jr      z, reader_label_done
                dec     bc
                ld      (reader_table_count), bc

; Row: [name_id u16 LE][delta uvarint].
                call    reader_read_next_byte       ; name_id LSB
                ld      c, a
                call    reader_read_next_byte       ; name_id MSB
                ld      b, a
                ld      (reader_row_name_id), bc

; Decode delta and add it (32-bit) into symbol_value_buf in place.
                ld      hl, symbol_value_buf
                call    reader_uvarint_add

; Seed only on pass 1.  symbol_value_buf already holds the accumulated
; value; symbol_insert wants HL = name_id.  The reading cursor is in
; (reader_in_cursor) (untouched by the insert), so no save/restore of it
; is needed; we persist the LMPR page into IN_POS_PAGE first so a later
; in_map_current re-maps the right IN page after the insert clobbers LMPR.
                ld      a, (reader_seed_pass1)
                or      a
                jr      z, reader_label_loop
                in      a, (250)                    ; snapshot current IN LMPR
                ld      (IN_POS_PAGE), a
                ld      hl, (reader_row_name_id)
                call    symbol_insert               ; clobbers A,BC,DE,HL
                call    in_map_current              ; re-map IN page after insert
                jr      reader_label_loop

reader_label_done:

; ----- Local table ----------------------------------------------------
; Read count u16 LE → reader_table_count.
                call    reader_read_next_byte       ; A = count LSB
                ld      c, a
                call    reader_read_next_byte       ; A = count MSB
                ld      b, a
                ld      (reader_table_count), bc

; Zero the local accumulator (local_label_pc_buf).
                xor     a
                ld      (local_label_pc_buf + 0), a
                ld      (local_label_pc_buf + 1), a
                ld      (local_label_pc_buf + 2), a
                ld      (local_label_pc_buf + 3), a

reader_local_loop:
                ld      bc, (reader_table_count)
                ld      a, b
                or      c
                jr      z, reader_local_done
                dec     bc
                ld      (reader_table_count), bc

; Row: [digit u8][delta uvarint].
                call    reader_read_next_byte       ; A = digit
                ld      (reader_row_digit), a

; Decode delta and add it (32-bit) into local_label_pc_buf in place.
                ld      hl, local_label_pc_buf
                call    reader_uvarint_add

                ld      a, (reader_seed_pass1)
                or      a
                jr      z, reader_local_loop
                in      a, (250)                    ; snapshot current IN LMPR
                ld      (IN_POS_PAGE), a
                ld      a, (reader_row_digit)
                call    local_def_append            ; clobbers regs
                call    in_map_current              ; re-map IN page after append
                jr      reader_local_loop

reader_local_done:
; Hand the reading cursor back to HL for reader_init_done's persist.
                ld      hl, (reader_in_cursor)
                jp      reader_init_done


; -----------------------------------------------------------------------
; reader_read_next_byte — page-safe single-byte read from the IN cursor
; held in (reader_in_cursor).
;
; Normalises the cursor into section A (bumping LMPR if a page boundary
; was crossed) so multi-byte rows / varints that straddle &4000 read
; correct bytes, then A := (cursor); cursor++.  Mirrors how the
; name-table skip and the reader_next_kind copy loop renormalise.
;
; The cursor is in memory (not a register) because the table loops call
; symbol_insert / local_def_append between reads, and those clobber HL.
;
; Input:  (reader_in_cursor) = section-A offset of the next byte; IN page
;         mapped (LMPR is only bumped here / by the inserts' re-map, never
;         reset, until reader_init_done).
; Output: A = byte; (reader_in_cursor) advanced by 1 (normalised first).
; Clobbers: A, HL.
; -----------------------------------------------------------------------
reader_read_next_byte:
                ld      hl, (reader_in_cursor)
                call    in_normalise_hl             ; clobbers A only
                ld      a, (hl)
                inc     hl
                ld      (reader_in_cursor), hl
                ret


; -----------------------------------------------------------------------
; reader_uvarint_add — decode an unsigned LEB128 varint from the IN
; cursor and add the decoded 32-bit value to the 4-byte LE accumulator
; whose address is passed in HL.
;
; LEB128: low 7 bits of each byte are data, high bit = continuation;
; groups are little-endian (first byte = least-significant 7 bits).  We
; decode into a 4-byte LE temp (reader_varint_tmp), shifting each group
; into bits [shift, shift+7).  A value wider than 32 bits (a 6th group, or
; any group landing entirely above bit 31) is impossible for this tool
; (>4GB offset) — jp fail, honest rather than silent.
;
; Input:  HL = address of a 4-byte LE accumulator (in fixed RAM).  The IN
;         reading cursor is (reader_in_cursor); reader_read_next_byte
;         advances it.
; Output: (accumulator) += decoded value.  (reader_in_cursor) advanced
;         past the varint.  Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
reader_uvarint_add:
                ld      (reader_varint_acc_ptr), hl

; Zero the 4-byte decode temp and the shift counter.
                xor     a
                ld      (reader_varint_tmp + 0), a
                ld      (reader_varint_tmp + 1), a
                ld      (reader_varint_tmp + 2), a
                ld      (reader_varint_tmp + 3), a
                ld      (reader_varint_shift), a    ; bit position of next group

reader_uvarint_byte:
                call    reader_read_next_byte       ; A = byte (cursor advanced)
                ld      (reader_varint_byte), a

; Merge low 7 bits into reader_varint_tmp at reader_varint_shift.
                push    af
                call    reader_varint_merge_group
                pop     af

; Continuation? high bit set → more groups follow.
                and     &80
                jr      z, reader_uvarint_done

; Advance shift by 7.  If shift reaches 35 (a 6th group) the value cannot
; fit 32 bits — fail.  (The shift==28 group's bits above 31 are caught in
; reader_varint_merge_group's overflow guard.)
                ld      a, (reader_varint_shift)
                add     a, 7
                ld      (reader_varint_shift), a
                cp      35
                jr      c, reader_uvarint_byte
                jp      fail                        ; >32-bit varint impossible

reader_uvarint_done:
; acc += reader_varint_tmp (32-bit LE add).
                ld      hl, (reader_varint_acc_ptr)
                ld      de, reader_varint_tmp
                ld      b, 4
                or      a                           ; clear CF
reader_uvarint_add_loop:
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    reader_uvarint_add_loop
                ret


; reader_varint_merge_group — OR the low 7 bits of reader_varint_byte
; into reader_varint_tmp (4-byte LE) starting at bit reader_varint_shift.
;
; shift is a multiple of 7 in {0,7,14,21,28}.  We compute the byte index
; (shift / 8) and bit position (shift % 8), then shift the 7-bit group
; into a 16-bit window straddling at most two accumulator bytes.  For the
; shift==28 group (bits 28..34), any bit at position >= 32 must be 0
; (the value can't exceed 32 bits) — guarded explicitly.
; Clobbers: A, BC, DE, HL.
reader_varint_merge_group:
                ld      a, (reader_varint_byte)
                and     &7F                         ; A = 7-bit data group
                ld      e, a                        ; E = group value (bits 0..6)
                ld      d, 0                         ; DE = group, 16-bit

; Shift DE left by (reader_varint_shift % 8) bits.
                ld      a, (reader_varint_shift)
                and     7                           ; A = bit offset within a byte
                jr      z, reader_merge_shift_done
                ld      b, a
reader_merge_shift_loop:
                sla     e
                rl      d
                djnz    reader_merge_shift_loop
reader_merge_shift_done:
; Byte index = reader_varint_shift / 8.
                ld      a, (reader_varint_shift)
                rrca
                rrca
                rrca
                and     &1F                         ; A = shift / 8 (0..4)
; index 0..2: the 16-bit window (DE) lands wholly within tmp[idx..idx+1].
; index 3   : low byte → tmp[3]; high byte must be 0 (bits >= 32).
; index >=4 : both bytes would be >= bit 32 — impossible.
                cp      4
                jr      c, reader_merge_idx_ok
                jp      fail                        ; group entirely above bit 31
reader_merge_idx_ok:
                ld      c, a                        ; C = byte index
                ld      b, 0
                ld      hl, reader_varint_tmp
                add     hl, bc                      ; HL → tmp[idx]
; tmp[idx] |= E.
                ld      a, (hl)
                or      e
                ld      (hl), a
; The high half (D) lands in tmp[idx+1].  If idx == 3, tmp[idx+1] would be
; tmp[4] which doesn't exist — D must be 0 (no bits above 31).
                ld      a, c
                cp      3
                jr      nz, reader_merge_high_ok
                ld      a, d
                or      a
                jp      nz, fail                    ; bits above 31 set
                ret
reader_merge_high_ok:
                inc     hl                          ; HL → tmp[idx+1]
                ld      a, (hl)
                or      d
                ld      (hl), a
                ret


; reader_init_done is reached after both header tables are parsed (and,
; on pass 1, seeded).  HL = section-A offset of the first record byte.
reader_init_done:
; Normalise a page-boundary cursor before persisting.  The header-table
; parse (PR(d)) hands HL straight from reader_in_cursor, which may sit at
; &4000 when the last byte read ended on an IN page boundary; persisting
; (page N, &4000) instead of (page N+1, 0) would later confuse
; reader_at_end (same false-"not at end" trap reader_next_kind guards at
; its in_normalise_hl).  No-op on the name-table-skip entry path (HL is
; already < &4000 there).
                call    in_normalise_hl
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
                sbc     hl, de              ; HL = pos - end
                ret     z                   ; pos == end → Z=1 (at end)
                ; sbc borrows (CF=1) exactly when pos < end, the normal
                ; "records remain" case → not at end.  CF=0 here means
                ; pos > end, an overrun (corrupt input or a walking bug);
                ; treat pos >= end as at end so the walk stops rather than
                ; running off the buffer.
                jr      c, reader_at_end_no ; pos < end → not at end (Z=0)
                xor     a                   ; pos > end → Z=1 (at end)
                ret
reader_at_end_no:
                ld      a, 1
                or      a                   ; Z=0
                ret


; -----------------------------------------------------------------------
; reader_next_kind — fetch the next record into STAGING_BUF and return
; A=kind, BC=payload length, HL=STAGING_BUF.
;
; Per docs/specs/paged-in-design.md §"reader_next_kind
; (paged)".  Brackets an LMPR=IN_POS_PAGE window around the read; copies
; [payload] bytes from section A into STAGING_BUF; renormalises across
; IN page boundaries during the copy.  Restores LMPR_ENCTAB before
; returning so the encoder window (ENCTAB in section A) is live on
; entry to the record handler.
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
; A payload of exactly 1024 bytes fills the buffer to STAGING_BUF_END and
; is legal (the copy loop ends one-past-last there); only len > 1024
; overflows, so the rejection boundary is 1025 — the old high-byte-only
; `cp size>>8 / jr c` wrongly rejected the exactly-full 1024-byte payload
; (i73 L4).  HL is live (it is the copy loop's source pointer), so save
; it across the 16-bit compare: fail iff size - len borrows (len > size).
; The bare untagged fail (vs the old tag 01) keeps the test-variant code
; budget under its &C000 cliff; no current input reaches it.
                push    hl
                ld      hl, STAGING_BUF_END - STAGING_BUF    ; HL = size = &400
                or      a                   ; clear carry
                sbc     hl, bc              ; CY=1 iff len > size
                pop     hl
                jp      c, fail             ; len > STAGING_BUF size
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
reader_curr_kind:       defb    0
reader_curr_payload:    defw    0
reader_curr_len:        defw    0

; Header-table parse scratch (i39a PR(d) — reader_init_header_tables).
reader_in_cursor:       defw    0   ; section-A read cursor (held in RAM so
                                    ;   symbol_insert/local_def_append can
                                    ;   clobber registers between byte reads)
reader_seed_pass1:      defb    0   ; 1 = pass 1 (seed tables); 0 = parse-only
reader_table_count:     defw    0   ; rows remaining in the current table
reader_row_name_id:     defw    0   ; current label row's name_id
reader_row_digit:       defb    0   ; current local row's digit
reader_varint_acc_ptr:  defw    0   ; address of the 4-byte LE accumulator
reader_varint_tmp:      defb    0, 0, 0, 0  ; decoded varint (4-byte LE)
reader_varint_byte:     defb    0   ; the LEB128 byte being merged
reader_varint_shift:    defb    0   ; bit position of the next group
