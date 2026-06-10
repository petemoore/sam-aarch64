; symbols.asm — global symbol table for the M4 multi-pass assembler.
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.2.
;
; Hash-bucketed by `symbol_id mod 256` (low byte of the u16 id).  Symbol
; IDs are dense and allocated by text2bin in source order, so the low
; byte gives a reasonable distribution with no per-name string work on
; the Z80 side.
;
; -----------------------------------------------------------------------
; On-disk layout
; -----------------------------------------------------------------------
;
; SYMTAB is a flat array of 256 buckets at &C160.  Each bucket is 8 bytes:
;
;   offset 0   symbol_id u16 LE     (0xFFFF == empty bucket / unused)
;   offset 2   address   u32 LE     (the resolved address for this id)
;   offset 6   next_off  u16 LE     (chain link, see "Chain pointer" below)
;
; SYMTAB_OVERFLOW at &E800 is a parallel flat array of overflow entries
; with the same 8-byte layout.  Up to 256 overflow entries fit in the
; 2 KB reservation (&E800..&EFFF inclusive).  Bumped from 128 to 256
; on 2026-05-28 per `https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-z80-table-sizing-census.md`:
; release.tbn populates 474 unique symbols (282 LabelDef + 193
; .equ/.set/.global; 1 duplicate landed pre-existing), versus 256
; primary buckets, so worst-case 474 − 256 = 218 entries spill into the
; overflow chain.  256 gives ~17% margin.  Moving the overflow region
; out of the section-D &CD60 area also freed 1 KB for the relocated
; LITPOOL_PC_MAP and the bumped LOCAL_LABEL_TABLE.
;
; Chain pointer (`next_off`):
;
;   next_off is the **index** (not byte offset) of the next entry in the
;   chain within SYMTAB_OVERFLOW.  index 0 → first overflow entry at
;   &E800, index 1 → &E808, etc.  The byte offset is `index * 8`.
;   Sentinel 0xFFFF means "end of chain".
;
;   Rationale: index form keeps the limit (256) below 9 bits today but
;   leaves room to expand the overflow region to 64 KB without changing
;   the on-disk layout.  Insert/lookup multiply by 8 (three `add hl, hl`)
;   to convert to a byte offset before indexing.  Index 255 is stored
;   as next_off = 0x00FF, distinct from the 0xFFFF end-of-chain sentinel
;   because the high byte differs.
;
; Empty-bucket sentinel: symbol_id == 0xFFFF.  Since text2bin allocates
; ids densely from 0, 0xFFFF is never a valid id.  symbol_table_init
; writes (0xFF, 0xFF, _, _, _, _, _, _) into every bucket (the trailing
; six bytes are don't-care for an empty bucket).
;
; -----------------------------------------------------------------------
; ABI summary (full per-routine headers below)
; -----------------------------------------------------------------------
;
;   symbol_table_init                                  — reset table
;   symbol_insert    HL=id,  (symbol_value_buf)=addr   — insert / dup→fail
;   symbol_lookup    HL=id                             — CF=0 hit / CF=1 miss
;
; The 4-byte little-endian address is passed via the static buffer
; `symbol_value_buf` rather than a register pair, to avoid register
; pressure on the caller (HL holds the id, BC/DE are scratch).  Both
; insert and lookup read/write this buffer.
;
; Duplicate insert and overflow exhaustion are unrecoverable: both
; `jp fail` (red border + printer-channel "FAIL" banner, ci-core reports fail immediately).  The duplicate /
; overflow paths therefore can't be exercised from the self-test block
; (same caveat as test_slots.asm — the `jp fail` path is one-way).
;
; -----------------------------------------------------------------------

; ---------------------------------------------------------------------
; Memory map (must match the reservation comment in assembler.asm).
; ---------------------------------------------------------------------
SYMTAB:                 equ     &C160          ; 256 buckets × 8 bytes = 2 KB
SYMTAB_OVERFLOW:        equ     &E800          ; 256 entries × 8 bytes = 2 KB (moved 2026-05-28)
SYMTAB_OVERFLOW_MAX:    equ     256            ; max overflow entries (bumped from 128 — census peak 474 total symbols)

; Per-bucket / per-entry constants
SYMTAB_ENTRY_SIZE:      equ     8
SYMTAB_BUCKET_COUNT:    equ     256
SYMTAB_EMPTY_ID:        equ     &FFFF          ; sentinel for empty bucket
SYMTAB_END_OF_CHAIN:    equ     &FFFF          ; sentinel for next_off

; -----------------------------------------------------------------------
; symbol_table_init — reset all buckets to empty and the overflow
; allocator to zero.
;
; Writes symbol_id = 0xFFFF into every bucket (only the first two bytes
; of each 8-byte slot matter for "empty" — the other six are don't-care
; until the bucket gets populated).  Resets symtab_overflow_count to 0.
;
; Input:  none.
; Output: SYMTAB populated with 256 empty markers, symtab_overflow_count = 0.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
symbol_table_init:
                ld      hl, SYMTAB
                ld      bc, SYMTAB_BUCKET_COUNT     ; B = 256 buckets to mark
symbol_table_init_loop:
                ld      a, &FF
                ld      (hl), a                     ; symbol_id low = 0xFF
                inc     hl
                ld      (hl), a                     ; symbol_id high = 0xFF
                inc     hl
                inc     hl                          ; skip address bytes 0..3
                inc     hl
                inc     hl
                inc     hl
                inc     hl                          ; skip next_off bytes
                inc     hl                          ; HL now points at next bucket
                dec     bc
                ld      a, b
                or      c
                jr      nz, symbol_table_init_loop

                xor     a
                ld      (symtab_overflow_count + 0), a
                ld      (symtab_overflow_count + 1), a

; Clear the per-id absolute-symbol bitmap (64 bytes, all origin-relative
; by default).  See assembler.asm SYMTAB_ABS_BITMAP.
                ld      hl, SYMTAB_ABS_BITMAP
                ld      b, 64
                xor     a
symbol_table_init_abs_loop:
                ld      (hl), a
                inc     hl
                djnz    symbol_table_init_abs_loop
                ret


; -----------------------------------------------------------------------
; symbol_insert — insert (symbol_id, address) into the table.
;
; Input:
;   HL                     = symbol_id u16.
;   (symbol_value_buf + 0..3) = address u32 LE.
;
; Output:
;   Returns normally on success.
;
; Errors:
;   Duplicate id          → jp fail.
;   Overflow exhaustion   → jp fail (SYMTAB_OVERFLOW_MAX entries used).
;
; Clobbers: A, BC, DE, HL.
;
; Strategy:
;   1. Bucket = SYMTAB + (L * 8).
;   2. If bucket is empty (symbol_id == 0xFFFF), populate it in place:
;        write id, address, and next_off = END_OF_CHAIN.
;   3. Otherwise walk the chain:
;        - If any entry's id matches the requested id → duplicate → fail.
;        - At end-of-chain, allocate a new overflow entry, populate it,
;          and link the previous tail's next_off to point at it.
; -----------------------------------------------------------------------
symbol_insert:
                ld      (symtab_pending_id), hl     ; stash for compare / write

; -- Compute bucket pointer: SYMTAB + (L * 8) --------------------------
; Bucket index is L (low byte of id).  Multiply by 8 via three `add hl, hl`.
                ld      h, 0                        ; HL = 0..255 (bucket index)
                add     hl, hl                      ; * 2
                add     hl, hl                      ; * 4
                add     hl, hl                      ; * 8
                ld      bc, SYMTAB
                add     hl, bc                      ; HL = &(bucket)

; -- Empty-bucket fast path -------------------------------------------
; Read 16-bit id field at HL+0..1; if 0xFFFF, write in place.
                ld      a, (hl)
                inc     hl
                ld      b, a                        ; B = id low
                ld      a, (hl)
                dec     hl                          ; restore HL to bucket base
                and     b                           ; AND of high and low bytes
                cp      &FF
                jr      z, symbol_insert_write_here

; -- Chain walk: HL points at the first occupied entry ----------------
symbol_insert_chain_walk:
; Compare entry's id at HL+0..1 against pending id.
                ld      a, (hl)                     ; A = entry id low
                ld      bc, (symtab_pending_id)     ; BC = pending id (C=low, B=high)
                cp      c
                jr      nz, symbol_insert_not_dup
                inc     hl
                ld      a, (hl)                     ; A = entry id high
                dec     hl
                cp      b
                jr      nz, symbol_insert_not_dup
                jp      fail

symbol_insert_not_dup:
; Read next_off (entry+6..7); if END_OF_CHAIN, this is the tail.
                push    hl                          ; save bucket-entry base
                ld      bc, 6
                add     hl, bc                      ; HL → next_off LSB
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                     ; DE = next_off
                pop     hl                          ; restore HL = entry base

                ld      a, d
                and     e
                cp      &FF
                jr      z, symbol_insert_extend_chain

; Follow chain: HL := SYMTAB_OVERFLOW + (DE * 8).
                ex      de, hl                      ; HL = next_off
                add     hl, hl                      ; * 2
                add     hl, hl                      ; * 4
                add     hl, hl                      ; * 8
                ld      bc, SYMTAB_OVERFLOW
                add     hl, bc                      ; HL = &overflow[next_off]
                jr      symbol_insert_chain_walk

; -- Tail reached, no duplicate; allocate overflow entry and link -----
; HL points at the current tail entry.  We need to:
;   a) allocate a new overflow index N (< SYMTAB_OVERFLOW_MAX),
;   b) write the new entry at SYMTAB_OVERFLOW + N*8,
;   c) patch tail.next_off = N.
symbol_insert_extend_chain:
                ld      (symtab_tail_ptr), hl       ; remember tail entry base

; Allocate: N = symtab_overflow_count; bump count; range-check.
                ld      hl, (symtab_overflow_count)
                ld      (symtab_new_index), hl

; Range check: N must be < SYMTAB_OVERFLOW_MAX (== 256).  count is u16;
; at the boundary count == 256 the LSB rolls over to 0 and the MSB
; becomes 1, so a non-zero MSB alone signals exhaustion.
                ld      a, h
                or      a
                jr      z, symtab_overflow_room_ok
                ld      a, &21
                jp      fail_with_tag       ; tag 21: SYMTAB_OVERFLOW exhausted
symtab_overflow_room_ok:
; Bump count for next allocation.
                inc     hl
                ld      (symtab_overflow_count), hl

; Compute new-entry address: SYMTAB_OVERFLOW + N*8.
                ld      hl, (symtab_new_index)
                add     hl, hl
                add     hl, hl
                add     hl, hl
                ld      bc, SYMTAB_OVERFLOW
                add     hl, bc                      ; HL = &overflow[N]

; Write entry: id, address, next_off=END_OF_CHAIN.
                call    symbol_insert_populate_entry

; Patch tail.next_off to the new index N.
                ld      hl, (symtab_tail_ptr)
                ld      bc, 6
                add     hl, bc                      ; HL → tail.next_off LSB
                ld      a, (symtab_new_index + 0)
                ld      (hl), a
                inc     hl
                ld      a, (symtab_new_index + 1)
                ld      (hl), a
                ret

; -- Bucket was empty: write id + address + next_off here -------------
symbol_insert_write_here:
                ; fall through into the populate helper, then ret.

; symbol_insert_populate_entry — populate an 8-byte entry at HL with the
; current pending id, the value from symbol_value_buf, and
; next_off = END_OF_CHAIN.  Returns HL = entry base + 8 (one past end).
;
; Used both for the empty-bucket fast path and for newly-allocated
; overflow entries.  Clobbers: A, BC, DE, HL (well, see note: HL is
; deliberately advanced; callers don't rely on it).
symbol_insert_populate_entry:
                ld      a, (symtab_pending_id + 0)
                ld      (hl), a
                inc     hl
                ld      a, (symtab_pending_id + 1)
                ld      (hl), a
                inc     hl

                ld      a, (symbol_value_buf + 0)
                ld      (hl), a
                inc     hl
                ld      a, (symbol_value_buf + 1)
                ld      (hl), a
                inc     hl
                ld      a, (symbol_value_buf + 2)
                ld      (hl), a
                inc     hl
                ld      a, (symbol_value_buf + 3)
                ld      (hl), a
                inc     hl

                ld      a, &FF
                ld      (hl), a                     ; next_off LSB
                inc     hl
                ld      (hl), a                     ; next_off MSB
                ret


; -----------------------------------------------------------------------
; symbol_abs_bit_ptr — compute (HL=ptr to bitmap byte, A=mask) for id.
;   In:  HL = symbol_id u16 (id < 512).
;   Out: HL = SYMTAB_ABS_BITMAP + (id >> 3); A = 1 << (id & 7).
;   Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
symbol_abs_bit_ptr:
                ld      a, l
                and     7                   ; A = id & 7
                ld      b, a
                inc     b                   ; rotate count = (id&7)+1
                ld      a, 1
symbol_abs_bit_ptr_rot:
                rlca
                djnz    symbol_abs_bit_ptr_rot
                rrca                        ; A = 1 << (id & 7)
                ld      c, a                ; C = mask
; HL := id >> 3  (16-bit logical shift right by 3).
                srl     h
                rr      l
                srl     h
                rr      l
                srl     h
                rr      l
                ld      de, SYMTAB_ABS_BITMAP
                add     hl, de              ; HL = &bitmap[id>>3]
                ld      a, c                ; A = mask
                ret

; -----------------------------------------------------------------------
; symbol_mark_absolute — set the absolute bit for symbol id HL.
;   In:  HL = symbol_id.  Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
symbol_mark_absolute:
                call    symbol_abs_bit_ptr  ; HL=byte ptr, A=mask
                or      (hl)
                ld      (hl), a
                ret

; -----------------------------------------------------------------------
; symbol_is_absolute — test the absolute bit for symbol id HL.
;   In:  HL = symbol_id.
;   Out: Z=0 (NZ) if absolute, Z=1 if origin-relative.
;   Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
symbol_is_absolute:
                call    symbol_abs_bit_ptr  ; HL=byte ptr, A=mask
                and     (hl)                ; Z set iff bit clear (origin-rel)
                ret

; -----------------------------------------------------------------------
; symbol_lookup — resolve symbol_id → address.
;
; Input:
;   HL = symbol_id u16.
;
; Output (on hit):
;   CF = 0.
;   (symbol_value_buf + 0..3) = address u32 LE.
;
; Output (on miss):
;   CF = 1.
;   symbol_value_buf is unmodified.
;
; Clobbers: A, BC, DE, HL.
;
; Strategy mirrors symbol_insert's walk: bucket = SYMTAB + L*8; if empty,
; miss; otherwise compare ids along the chain via next_off.
; -----------------------------------------------------------------------
symbol_lookup:
                ld      (symtab_pending_id), hl

; -- Bucket pointer ----------------------------------------------------
                ld      h, 0
                add     hl, hl
                add     hl, hl
                add     hl, hl
                ld      bc, SYMTAB
                add     hl, bc                      ; HL = &bucket

symbol_lookup_chain:
; If entry's id == 0xFFFF → miss.
                ld      a, (hl)
                inc     hl
                ld      b, a                        ; B = id low
                ld      a, (hl)
                dec     hl                          ; restore HL
                ld      c, a                        ; C = id high
                and     b
                cp      &FF
                jr      z, symbol_lookup_miss

; Compare against pending id.
                ld      a, (symtab_pending_id + 0)
                cp      b
                jr      nz, symbol_lookup_next
                ld      a, (symtab_pending_id + 1)
                cp      c
                jr      z, symbol_lookup_hit

symbol_lookup_next:
; Follow chain.  Read next_off; if END_OF_CHAIN → miss.
                push    hl
                ld      bc, 6
                add     hl, bc
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                pop     hl
                ld      a, d
                and     e
                cp      &FF
                jr      z, symbol_lookup_miss

                ex      de, hl                      ; HL = next_off
                add     hl, hl
                add     hl, hl
                add     hl, hl
                ld      bc, SYMTAB_OVERFLOW
                add     hl, bc                      ; HL = &overflow[next_off]
                jr      symbol_lookup_chain

symbol_lookup_hit:
; HL points at the entry base.  Copy 4 bytes at HL+2..HL+5 into
; symbol_value_buf, then clear CF and return.
                inc     hl
                inc     hl                          ; HL → address[0]
                ld      a, (hl)
                ld      (symbol_value_buf + 0), a
                inc     hl
                ld      a, (hl)
                ld      (symbol_value_buf + 1), a
                inc     hl
                ld      a, (hl)
                ld      (symbol_value_buf + 2), a
                inc     hl
                ld      a, (hl)
                ld      (symbol_value_buf + 3), a
                or      a                           ; CF = 0
                ret

symbol_lookup_miss:
                scf                                 ; CF = 1
                ret


; -----------------------------------------------------------------------
; Scratch / state.
; -----------------------------------------------------------------------
; symbol_value_buf — 4-byte LE buffer used by callers to pass the
; address into symbol_insert and to receive the address from
; symbol_lookup.  See ABI note at the top of this file.
symbol_value_buf:       defb    0, 0, 0, 0

; symtab_pending_id — copy of the id passed in HL (preserved across the
; bucket-pointer computation, which shifts HL).
symtab_pending_id:      defw    0

; symtab_overflow_count — number of overflow entries allocated so far
; (next index to hand out).  Reset to 0 by symbol_table_init.
symtab_overflow_count:  defw    0

; symtab_tail_ptr — base pointer of the tail entry whose next_off must
; be patched during chain extension (transient; valid only between
; tail discovery and the next_off patch).
symtab_tail_ptr:        defw    0

; symtab_new_index — overflow index allocated for the chain extension
; (transient; same lifetime as symtab_tail_ptr).
symtab_new_index:       defw    0
