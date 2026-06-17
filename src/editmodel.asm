; editmodel.asm — flat-memory block-list document model, Brick 1.
;
; SAM-side Z80 port of the Go authority:
;   tools/sam-aarch64/editmodel/editmodel.go
; Scoped to Brick 1: insert, split, line-at, goto-by-id, line-count,
; block-count. Delete, merge, undo, and serialize are later bricks.
;
; FLAT MEMORY (no SAM paging): blocks are descriptor-indexed regions of 64 KB
; flat memory. EM_BLOCK_CAP=256 keeps the test small so splits occur with a
; modest insert count. The logical results (line-at-index, goto-by-id) are
; independent of block capacity: the small-cap Z80 matches the oracle.
;
; RECORD LAYOUT per line:
;   id:3 (u24 little-endian) | len:1 | text:len bytes   total = 4+len
; Records pack back-to-back inside a block's data region;
; EM_DESC[d].line_count records reside there.
;
; DESCRIPTOR TABLE  EM_DESC[d] = { data_ptr:2LE, line_count:1 }  (3 bytes).
; Descriptor index is a STABLE BLOCK REFERENCE — descriptors never move once
; allocated (Brick 1: no delete/merge, so descriptors only grow).
;
; LOCATION TABLE  EM_LOC[id] = descriptor index holding id. Survives order-
; array shifts on block insert because only moved records are re-pointed —
; the design's O(½ block) bounded-cost property (§3/§8.1 of the spec).
;
; PROVENANCE: algorithmic port of editmodel.go; flat-memory layout is
; original for the Z80 port.
;
; VERIFICATION: tools/netboot-oracle/z80/editmodel_test.go drives this under
; koron-go/z80, replays random inserts, and asserts LineAt and goto-by-id
; match the Go oracle on every step.

                ; org only when assembled standalone (host harness uses
                ; -D EM_STANDALONE=1); an including file supplies its own org.
                if defined(EM_STANDALONE)
                org     &8000
                endif

; ---------------------------------------------------------------------------
; Constants
; ---------------------------------------------------------------------------

; EM_BLOCK_CAP: data capacity per block. 256 for flat-memory testing so
; splits occur with a modest insert count. The real SAM value (Brick 2,
; paging integration) is 8192 (½-page); set there without structural change.
EM_BLOCK_CAP:   equ 256

; EM_MAX_BLOCKS: descriptor pool size. 80 is ample for the Brick 1 test.
EM_MAX_BLOCKS:  equ 80

; EM_MAX_IDS: loc-array size; valid ids are 1..EM_MAX_IDS-1.
EM_MAX_IDS:     equ 2048

; ===========================================================================
; em_reset — initialise document to empty.
;
; Sets EM_NBLOCKS=0, EM_NEXT_DESC=0, EM_NEXTID=1 (u24 LE).
; ===========================================================================
em_reset:
                xor     a
                ld      (EM_NBLOCKS), a
                ld      (EM_NEXT_DESC), a
                ld      a, 1
                ld      (EM_NEXTID), a
                xor     a
                ld      (EM_NEXTID+1), a
                ld      (EM_NEXTID+2), a
                ret

; ===========================================================================
; em_line_count — return total line count in HL.
; ===========================================================================
em_line_count:
                ld      hl, 0
                ld      a, (EM_NBLOCKS)
                or      a
                ret     z
                ld      b, a
                ld      ix, EM_ORDER
em_lc_loop:
                ; em_desc_linecount_a clobbers HL (leaves it pointing into DESC);
                ; save the running sum on the stack and restore after the call.
                push    hl
                ld      a, (ix+0)
                call    em_desc_linecount_a ; A = line_count, HL clobbered
                pop     hl
                ld      e, a
                ld      d, 0
                add     hl, de
                inc     ix
                djnz    em_lc_loop
                ret

; ===========================================================================
; em_block_count — return EM_NBLOCKS in HL.
; ===========================================================================
em_block_count:
                ld      a, (EM_NBLOCKS)
                ld      l, a
                ld      h, 0
                ret

; ===========================================================================
; em_insert — insert a line at document index BC.
;
; Entry: BC = document index (0..line_count), A = text length (0..255),
;        text in EM_IN_TEXT.
; Effect: allocates id=EM_NEXTID (u24), increments EM_NEXTID; finds the
;         target block + local index; inserts the packed record; updates
;         EM_LOC[id]; may split the block; writes new id to EM_OUT_ID.
; ===========================================================================
em_insert:
                ld      (EM_W_DOCI), bc
                ld      (EM_W_LEN), a

                ; Allocate id = EM_NEXTID (u24 LE), then increment EM_NEXTID.
                ld      hl, EM_NEXTID
                ld      a, (hl)
                ld      (EM_W_ID), a
                inc     hl
                ld      a, (hl)
                ld      (EM_W_ID+1), a
                inc     hl
                ld      a, (hl)
                ld      (EM_W_ID+2), a
                ; Increment EM_NEXTID (u24).
                ld      hl, EM_NEXTID
                inc     (hl)
                jr      nz, em_ins_id_done
                inc     hl
                inc     (hl)
                jr      nz, em_ins_id_done
                inc     hl
                inc     (hl)
em_ins_id_done:

                ; Handle empty document: allocate first block.
                ld      a, (EM_NBLOCKS)
                or      a
                jr      nz, em_ins_nonempty

                call    em_alloc_desc       ; A = new desc (= 0)
                ld      (EM_ORDER), a       ; ORDER[0] = desc
                ld      a, 1
                ld      (EM_NBLOCKS), a
                xor     a
                ld      (EM_W_DPOS), a
                ld      (EM_W_LOCAL), a
                ld      a, (EM_ORDER)
                ld      (EM_W_DESC), a
                jr      em_ins_do

em_ins_nonempty:
                ; blockAndLocalIndex(EM_W_DOCI) -> EM_W_DPOS, EM_W_DESC, EM_W_LOCAL.
                ld      bc, (EM_W_DOCI)
                call    em_block_and_local

em_ins_do:
                ; Get data base ptr and current line_count for target block.
                ld      a, (EM_W_DESC)
                call    em_desc_dataptr_a   ; HL = data base
                ld      (EM_W_DATA_BASE), hl
                ld      a, (EM_W_DESC)
                call    em_desc_linecount_a ; A = line_count, HL clobbered (not used after)
                ld      (EM_W_NLINES), a

                ; Compute EM_W_INS_PTR and EM_W_END_PTR.
                call    em_find_offsets

                ; rec_size = 4 + len.
                ld      a, (EM_W_LEN)
                add     a, 4
                ld      (EM_W_REC_SIZE), a

                ; Pre-split check: if (END_PTR - DATA_BASE) + rec_size > EM_BLOCK_CAP
                ; and NLINES >= 2, split the block BEFORE inserting to prevent the
                ; insert from physically overflowing into the next descriptor's region.
                ld      a, (EM_W_NLINES)
                cp      2
                jr      c, em_ins_no_presplit   ; NLINES < 2: can't split

                ld      hl, (EM_W_END_PTR)
                ld      de, (EM_W_DATA_BASE)
                or      a
                sbc     hl, de              ; HL = used = END_PTR - DATA_BASE
                ld      a, (EM_W_REC_SIZE)
                ld      e, a
                ld      d, 0
                add     hl, de              ; HL = used + rec_size
                ld      de, EM_BLOCK_CAP
                or      a
                sbc     hl, de              ; HL = (used + rec_size) - EM_BLOCK_CAP
                                            ; carry if < cap (no overflow), no carry if >= cap
                jr      c, em_ins_no_presplit   ; used + rec_size <= EM_BLOCK_CAP: safe

                ; Pre-split: set up em_do_split parameters from current state.
                ; em_do_split entry: A = line_count, EM_W_SPOS = DPOS, EM_W_SDESC = DESC.
                ld      a, (EM_W_DPOS)
                ld      (EM_W_SPOS), a
                ld      a, (EM_W_DESC)
                ld      (EM_W_SDESC), a
                ld      a, (EM_W_NLINES)
                call    em_do_split         ; A = NLINES, EM_W_SPOS/SDESC set above

                ; After split, re-navigate to find the (possibly new) target block.
                ld      bc, (EM_W_DOCI)
                call    em_block_and_local  ; updates EM_W_DPOS, EM_W_DESC, EM_W_LOCAL

                ; Reload data_base and NLINES for the new target block.
                ld      a, (EM_W_DESC)
                call    em_desc_dataptr_a   ; HL = new data base
                ld      (EM_W_DATA_BASE), hl
                ld      a, (EM_W_DESC)
                call    em_desc_linecount_a ; A = new line_count
                ld      (EM_W_NLINES), a

                ; Recompute INS_PTR and END_PTR for new target block.
                call    em_find_offsets

em_ins_no_presplit:
                ; Shift existing bytes up by rec_size = 4+len using LDDR.

                ld      hl, (EM_W_END_PTR)
                ld      de, (EM_W_INS_PTR)
                or      a
                sbc     hl, de              ; HL = shift_count = END_PTR - INS_PTR
                ld      a, h
                or      l
                jr      z, em_ins_write

                ; LDDR: src = END_PTR-1, dst = src + rec_size, count = shift.
                ld      b, h
                ld      c, l                ; BC = shift count
                ld      hl, (EM_W_END_PTR)
                dec     hl                  ; HL = src (last byte of existing data)
                ld      a, (EM_W_REC_SIZE)
                ld      e, a
                ld      d, 0
                add     hl, de
                ex      de, hl              ; DE = dst (src + rec_size)
                ld      hl, (EM_W_END_PTR)
                dec     hl                  ; HL = src (last byte again)
                lddr

em_ins_write:
                ; Write new record at EM_W_INS_PTR: id(3 LE) | len | text.
                ld      hl, (EM_W_INS_PTR)
                ld      a, (EM_W_ID)
                ld      (hl), a
                inc     hl
                ld      a, (EM_W_ID+1)
                ld      (hl), a
                inc     hl
                ld      a, (EM_W_ID+2)
                ld      (hl), a
                inc     hl
                ld      a, (EM_W_LEN)
                ld      (hl), a
                inc     hl
                ld      a, (EM_W_LEN)
                or      a
                jr      z, em_ins_text_done
                ex      de, hl              ; DE = dest (after len byte)
                ld      hl, EM_IN_TEXT
                ld      c, a
                ld      b, 0
                ldir
em_ins_text_done:

                ; Increment line_count in the descriptor.
                ld      a, (EM_W_DESC)
                call    em_desc_ptr_a       ; HL -> descriptor base
                inc     hl
                inc     hl                  ; HL -> line_count byte
                inc     (hl)

                ; EM_LOC[id] = desc (safe u16 index: id < EM_MAX_IDS=2048).
                ld      hl, (EM_W_ID)       ; HL = id[1]:id[0]
                ld      de, EM_LOC
                add     hl, de
                ld      a, (EM_W_DESC)
                ld      (hl), a

                ; Write new id to EM_OUT_ID.
                ld      hl, EM_OUT_ID
                ld      a, (EM_W_ID)
                ld      (hl), a
                inc     hl
                ld      a, (EM_W_ID+1)
                ld      (hl), a
                inc     hl
                ld      a, (EM_W_ID+2)
                ld      (hl), a

                ; Split the block if over capacity.
                ld      a, (EM_W_DPOS)
                call    em_split_if_needed
                ret

; ===========================================================================
; em_line_at — retrieve line at document index BC.
;
; Entry: BC = document index (0..line_count-1).
; Effect: writes id (3 bytes LE) to EM_OUT_ID;
;         writes [len:1][text:len] to EM_OUT_TEXT.
; ===========================================================================
em_line_at:
                call    em_block_and_local  ; BC = doc-index

                ld      a, (EM_W_DESC)
                call    em_desc_dataptr_a   ; HL = data base
                ; Skip EM_W_LOCAL records.
                ld      a, (EM_W_LOCAL)
                or      a
                jr      z, em_lat_at_record
em_lat_skip_loop:
                ; skip record: id(3) + len(1) + text(len)
                inc     hl
                inc     hl
                inc     hl
                ld      e, (hl)             ; len
                inc     hl
                ld      d, 0
                add     hl, de
                dec     a
                jr      nz, em_lat_skip_loop

em_lat_at_record:
                ; HL -> start of target record.
                ld      a, (hl)
                ld      (EM_OUT_ID), a
                inc     hl
                ld      a, (hl)
                ld      (EM_OUT_ID+1), a
                inc     hl
                ld      a, (hl)
                ld      (EM_OUT_ID+2), a
                inc     hl
                ld      a, (hl)             ; len
                ld      (EM_OUT_TEXT), a
                inc     hl
                or      a
                ret     z
                ld      de, EM_OUT_TEXT+1
                ld      c, a
                ld      b, 0
                ldir
                ret

; ===========================================================================
; em_goto — find document index of the line with id in EM_IN_ID (3 bytes LE).
;
; Return: A=1 + HL=doc-index if found, or A=0 + HL=0 if not found.
;
; Algorithm mirrors IndexOf in editmodel.go:
;   1. desc = EM_LOC[id].
;   2. Scan EM_ORDER to get order position P of desc.
;   3. Sum line_counts of blocks 0..P-1 for base doc-index.
;   4. Walk block P's records to find id -> local offset.
;   5. Return base + local.
; ===========================================================================
em_goto:
                ; Validate id >= 1 and < EM_MAX_IDS (using 16-bit id).
                ld      hl, (EM_IN_ID)      ; HL = id[1]:id[0]
                ld      a, (EM_IN_ID+2)
                or      a
                jp      nz, em_goto_notfound ; id_hi != 0 -> out of range

                ld      a, h
                or      l
                jp      z, em_goto_notfound  ; id == 0 -> invalid

                ; id < EM_MAX_IDS?
                ld      de, EM_MAX_IDS
                push    hl
                or      a
                sbc     hl, de              ; HL = id - EM_MAX_IDS; no borrow if id >= EM_MAX_IDS
                pop     hl
                jp      nc, em_goto_notfound ; id >= EM_MAX_IDS

                ; desc = EM_LOC[id].
                ld      de, EM_LOC
                add     hl, de              ; HL -> EM_LOC[id]
                ld      a, (hl)             ; A = desc
                ld      (EM_W_DESC), a

                ; Scan EM_ORDER[0..NBLOCKS-1] for desc to get order position P.
                ld      b, a                ; B = target desc
                ld      a, (EM_NBLOCKS)
                ld      c, a                ; C = NBLOCKS
                ld      ix, EM_ORDER
                ld      d, 0                ; D = order position P
em_goto_scan:
                ld      a, d
                cp      c
                jp      nc, em_goto_notfound
                ld      a, (ix+0)
                cp      b
                jr      z, em_goto_found_block
                inc     d
                inc     ix
                jr      em_goto_scan

em_goto_found_block:
                ; D = P (order position of desc).
                ; Sum line_counts of blocks 0..P-1 into HL.
                ld      hl, 0
                ld      e, 0                ; E = i
                ld      ix, EM_ORDER
em_goto_sum:
                ld      a, e
                cp      d                   ; i == P?
                jr      nc, em_goto_local
                push    de
                push    hl
                push    ix
                ld      b, 0
                ld      c, e
                add     ix, bc
                ld      a, (ix+0)
                call    em_desc_linecount_a ; A = line_count
                pop     ix
                pop     hl
                pop     de
                ld      b, 0
                ld      c, a
                add     hl, bc
                inc     e
                jr      em_goto_sum

em_goto_local:
                ; HL = base doc-index.
                ld      (EM_W_BASE), hl

                ; Walk block P's records looking for id == EM_IN_ID.
                ld      a, (EM_W_DESC)
                call    em_desc_dataptr_a   ; HL = data base
                push    hl                  ; save data base (em_desc_linecount_a clobbers HL)
                ld      a, (EM_W_DESC)
                call    em_desc_linecount_a ; A = line_count, HL clobbered
                pop     hl                  ; HL = data base restored
                ld      b, a                ; B = records remaining
                ld      c, 0                ; C = local index
em_goto_walk:
                ld      a, b
                or      a
                jr      z, em_goto_notfound
                ; Compare record id[0]:id[1]:id[2] with EM_IN_ID.
                ld      a, (hl)             ; record id[0]
                ld      e, a
                ld      a, (EM_IN_ID)
                cp      e
                jr      nz, em_goto_skip_rec
                inc     hl
                ld      a, (hl)             ; record id[1]
                ld      e, a
                ld      a, (EM_IN_ID+1)
                cp      e
                jr      nz, em_goto_skip_rec1
                inc     hl
                ld      a, (hl)             ; record id[2]
                ld      e, a
                ld      a, (EM_IN_ID+2)
                cp      e
                jr      nz, em_goto_skip_rec2
                ; Match! doc-index = base + C.
                ld      hl, (EM_W_BASE)
                ld      d, 0
                ld      e, c
                add     hl, de
                ld      a, 1
                ret

em_goto_skip_rec2:
                dec     hl                  ; back to id[1]
em_goto_skip_rec1:
                dec     hl                  ; back to id[0]
em_goto_skip_rec:
                ; Skip past this record's id(3) + len(1) + text(len).
                inc     hl                  ; past id[0]
                inc     hl                  ; past id[1]
                inc     hl                  ; past id[2]
                ld      a, (hl)             ; len
                inc     hl
                or      a
                jr      z, em_goto_skip_done
                ld      d, 0
                ld      e, a
                add     hl, de
em_goto_skip_done:
                dec     b
                inc     c
                jr      em_goto_walk

em_goto_notfound:
                ld      hl, 0
                xor     a
                ret

; ===========================================================================
; Internal helpers
; ===========================================================================

; ---------------------------------------------------------------------------
; em_alloc_desc — allocate the next descriptor, return its index in A.
;
; data_ptr = EM_DATA + index * EM_BLOCK_CAP (EM_BLOCK_CAP=256).
; Since multiplying by 256 only affects the high byte:
;   data_ptr_lo = EM_DATA & 0xFF   (constant, low byte of EM_DATA)
;   data_ptr_hi = (EM_DATA >> 8) + index
; ---------------------------------------------------------------------------
em_alloc_desc:
                ld      a, (EM_NEXT_DESC)
                push    af                  ; save index

                ; data_ptr = EM_DATA + index * 256.
                ; Low byte = EM_DATA_LO (constant); high byte = EM_DATA_HI + index.
                ld      hl, EM_DATA
                ld      b, h                ; B = EM_DATA_HI
                ld      a, (EM_NEXT_DESC)
                add     a, b                ; A = EM_DATA_HI + index
                ld      d, a
                ld      e, l                ; E = EM_DATA_LO  (low byte of EM_DATA)
                ; DE = data_ptr

                ; Write descriptor: {data_ptr_lo, data_ptr_hi, line_count=0}.
                pop     af                  ; A = index (restored)
                push    af
                call    em_desc_ptr_a       ; HL -> descriptor[index]
                ld      (hl), e             ; data_ptr_lo = EM_DATA_LO
                inc     hl
                ld      (hl), d             ; data_ptr_hi = EM_DATA_HI + index
                inc     hl
                ld      (hl), 0             ; line_count = 0

                ; Increment EM_NEXT_DESC.
                ld      hl, EM_NEXT_DESC
                inc     (hl)

                pop     af                  ; A = allocated index
                ret

; ---------------------------------------------------------------------------
; em_desc_ptr_a — HL = &EM_DESC[A] (3 bytes/entry: HL = EM_DESC + 3*A).
; ---------------------------------------------------------------------------
em_desc_ptr_a:
                push    de
                ld      hl, EM_DESC
                ld      d, 0
                ld      e, a
                add     hl, de
                add     hl, de
                add     hl, de
                pop     de
                ret

; ---------------------------------------------------------------------------
; em_desc_dataptr_a — HL = EM_DESC[A].data_ptr (the data base address).
; ---------------------------------------------------------------------------
em_desc_dataptr_a:
                call    em_desc_ptr_a
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ex      de, hl
                ret

; ---------------------------------------------------------------------------
; em_desc_linecount_a — A = EM_DESC[A].line_count.
; ---------------------------------------------------------------------------
em_desc_linecount_a:
                call    em_desc_ptr_a
                inc     hl
                inc     hl
                ld      a, (hl)
                ret

; ---------------------------------------------------------------------------
; em_block_and_local — given doc-index in BC, set EM_W_DPOS, EM_W_DESC,
; EM_W_LOCAL.
;
; Mirrors blockAndLocalIndex from editmodel.go: accumulate line_counts from
; EM_ORDER until acc+cnt > BC (found), or exhaust all blocks (append-at-end).
; Uses EM_W_ACC as working storage for the running accumulator.
; ---------------------------------------------------------------------------
em_block_and_local:
                ld      hl, 0
                ld      (EM_W_ACC), hl      ; acc = 0
                ld      a, 0
                ld      (EM_W_I), a         ; i = 0

em_bal_loop:
                ; if i >= NBLOCKS: append to last block.
                ; Use D (not B) for the comparison so BC (the doc-index) is not clobbered.
                ld      a, (EM_W_I)
                ld      d, a
                ld      a, (EM_NBLOCKS)
                cp      d                   ; NBLOCKS - i; carry if i > NBLOCKS (shouldn't happen)
                jr      z, em_bal_last      ; i == NBLOCKS: past all blocks
                jr      c, em_bal_last      ; i > NBLOCKS (safety)

                ; cnt = EM_DESC[ORDER[i]].line_count
                ld      a, (EM_W_I)
                ld      hl, EM_ORDER
                ld      d, 0
                ld      e, a
                add     hl, de
                ld      a, (hl)             ; desc
                call    em_desc_linecount_a ; A = cnt
                ld      (EM_W_CNT), a

                ; Is BC < acc + cnt?
                ; (acc is a u16 stored in EM_W_ACC; cnt is u8.)
                ld      hl, (EM_W_ACC)
                ld      d, 0
                ld      e, a                ; DE = cnt
                add     hl, de              ; HL = acc + cnt
                ; Compare BC < HL: no borrow in (HL - BC) means HL >= BC;
                ; borrow means HL < BC. We want BC < HL, i.e. HL > BC.
                ; Use: or a; sbc hl,bc. Carry set if HL < BC.
                or      a
                sbc     hl, bc              ; HL = acc+cnt - BC; carry if acc+cnt < BC
                jr      c, em_bal_next      ; acc+cnt < BC: not this block
                jr      z, em_bal_next      ; acc+cnt == BC: BC not strictly less; next block
                ; BC < acc+cnt: found.
                ; local = BC - acc.
                ld      hl, (EM_W_ACC)
                ld      a, c
                sub     l
                ld      l, a
                ld      a, b
                sbc     a, h
                ld      h, a                ; HL = BC - acc
                ld      a, l
                ld      (EM_W_LOCAL), a     ; local fits in u8 for this test
                ld      a, (EM_W_I)
                ld      (EM_W_DPOS), a
                ; desc = ORDER[i]
                ld      hl, EM_ORDER
                ld      d, 0
                ld      e, a
                add     hl, de
                ld      a, (hl)
                ld      (EM_W_DESC), a
                ret

em_bal_next:
                ; acc += cnt; i++.
                ld      hl, (EM_W_ACC)
                ld      a, (EM_W_CNT)
                ld      d, 0
                ld      e, a
                add     hl, de
                ld      (EM_W_ACC), hl
                ld      a, (EM_W_I)
                inc     a
                ld      (EM_W_I), a
                jr      em_bal_loop

em_bal_last:
                ; Append to last block: local = line_count of last block.
                ld      a, (EM_NBLOCKS)
                dec     a
                ld      (EM_W_DPOS), a
                ld      hl, EM_ORDER
                ld      d, 0
                ld      e, a
                add     hl, de
                ld      a, (hl)             ; desc
                ld      (EM_W_DESC), a
                call    em_desc_linecount_a ; A = line_count
                ld      (EM_W_LOCAL), a
                ret

; ---------------------------------------------------------------------------
; em_find_offsets — compute EM_W_INS_PTR and EM_W_END_PTR.
;
; Walks EM_W_NLINES records from EM_W_DATA_BASE. After EM_W_LOCAL records,
; sets EM_W_INS_PTR. After all EM_W_NLINES records, sets EM_W_END_PTR.
; ---------------------------------------------------------------------------
em_find_offsets:
                ld      hl, (EM_W_DATA_BASE)
                ld      b, 0                ; B = records walked
                ld      a, (EM_W_NLINES)
                ld      c, a                ; C = total lines
                ld      a, (EM_W_LOCAL)
                ld      d, a                ; D = local (insertion point)
em_fo_loop:
                ; Mark insertion point when walked == local.
                ld      a, b
                cp      d
                jr      nz, em_fo_no_mark
                ld      (EM_W_INS_PTR), hl
em_fo_no_mark:
                ; Done when walked == total.
                ld      a, b
                cp      c
                jr      nc, em_fo_done
                ; Advance past record: id(3) + len(1) + text(len).
                inc     hl
                inc     hl
                inc     hl                  ; skip id
                ld      a, (hl)             ; len
                inc     hl                  ; skip len
                or      a
                jr      z, em_fo_notxt
                ld      e, a
                ld      a, l
                add     a, e
                ld      l, a
                jr      nc, em_fo_notxt
                inc     h
em_fo_notxt:
                inc     b
                jr      em_fo_loop
em_fo_done:
                ld      (EM_W_END_PTR), hl
                ; If local == total (append at end), INS_PTR wasn't set in loop.
                ld      a, (EM_W_LOCAL)
                ld      b, a
                ld      a, (EM_W_NLINES)
                cp      b
                jr      nz, em_fo_ret
                ld      (EM_W_INS_PTR), hl  ; insertion at end == END_PTR
em_fo_ret:
                ret

; ---------------------------------------------------------------------------
; em_split_if_needed — split block at order position A if over capacity.
;
; Condition: used_bytes > EM_BLOCK_CAP AND line_count >= 2.
; ---------------------------------------------------------------------------
em_split_if_needed:
                ld      (EM_W_SPOS), a

                ; desc = ORDER[A]
                ld      hl, EM_ORDER
                ld      d, 0
                ld      e, a
                add     hl, de
                ld      a, (hl)
                ld      (EM_W_SDESC), a

                ; line_count must be >= 2 to split.
                call    em_desc_linecount_a ; A = line_count
                cp      2
                ret     c                   ; < 2: don't split
                ld      (EM_W_SNLINES), a

                ; Compute used bytes by walking all records.
                ld      a, (EM_W_SDESC)
                call    em_desc_dataptr_a   ; HL = data_base
                ld      (EM_W_DATA_BASE), hl
                ld      a, (EM_W_SNLINES)
                ld      b, a
                ld      c, 0
em_sin_loop:
                ld      a, c
                cp      b
                jr      nc, em_sin_done
                inc     hl
                inc     hl
                inc     hl
                ld      a, (hl)
                inc     hl
                or      a
                jr      z, em_sin_notxt
                ld      d, 0
                ld      e, a
                add     hl, de
em_sin_notxt:
                inc     c
                jr      em_sin_loop
em_sin_done:
                ; HL = end_ptr; compute used = end_ptr - data_base.
                ld      de, (EM_W_DATA_BASE)
                or      a
                sbc     hl, de              ; HL = used bytes
                ; if used <= EM_BLOCK_CAP: no split.
                ld      de, EM_BLOCK_CAP
                or      a
                sbc     hl, de              ; HL = used - cap; carry if used < cap
                ret     c                   ; used < cap: no split
                ret     z                   ; used == cap: no split
                ; used > cap: split.
                ld      a, (EM_W_SNLINES)   ; restore for em_do_split
                call    em_do_split
                ret

; ---------------------------------------------------------------------------
; em_do_split — split block at order position EM_W_SPOS (desc EM_W_SDESC).
;
; Mirrors splitBlock from editmodel.go:
;   mid = line_count / 2
;   Allocate D2; copy second-half records into D2's data region.
;   Truncate original to first-half records.
;   Insert D2 at ORDER[SPOS+1], shift tail right.
;   EM_NBLOCKS++.
;   Update EM_LOC for moved (second-half) records only.
; Entry: A = line_count of block being split (from em_split_if_needed).
; ---------------------------------------------------------------------------
em_do_split:
                ld      b, a                ; B = total line_count
                srl     a                   ; A = mid = total / 2
                ld      (EM_W_SMID), a      ; first-half count

                ; Walk 'mid' records to find midpoint byte offset.
                ld      a, (EM_W_SDESC)
                call    em_desc_dataptr_a   ; HL = data_base
                ld      c, 0                ; C = records walked
                ld      a, (EM_W_SMID)
                ld      d, a                ; D = mid (target)
em_sp_walk_mid:
                ld      a, c
                cp      d
                jr      nc, em_sp_mid_done
                inc     hl
                inc     hl
                inc     hl                  ; skip id
                ld      a, (hl)
                inc     hl                  ; skip len
                or      a
                jr      z, em_sp_mid_notxt
                ld      e, a
                ld      a, l
                add     a, e
                ld      l, a
                jr      nc, em_sp_mid_notxt
                inc     h
em_sp_mid_notxt:
                inc     c
                jr      em_sp_walk_mid
em_sp_mid_done:
                ; HL = start of second half.
                ld      (EM_W_SMID_PTR), hl

                ; Compute byte size of second half.
                ld      a, b
                ld      d, a                ; D = total (save B which may be clobbered)
                ld      a, (EM_W_SMID)
                ld      d, a
                ld      a, b
                sub     d                   ; A = second_half_count (total - mid)
                ld      (EM_W_SRHALF_CNT), a
                ld      c, 0
em_sp_walk_right:
                ld      a, (EM_W_SRHALF_CNT)
                cp      c
                jr      z, em_sp_right_done
                jr      c, em_sp_right_done
                inc     hl
                inc     hl
                inc     hl
                ld      a, (hl)
                inc     hl
                or      a
                jr      z, em_sp_right_notxt
                ld      d, 0
                ld      e, a
                add     hl, de
em_sp_right_notxt:
                inc     c
                jr      em_sp_walk_right
em_sp_right_done:
                ; HL = end of second half.
                ld      de, (EM_W_SMID_PTR)
                or      a
                sbc     hl, de              ; HL = byte size of second half
                ld      (EM_W_SRSIZE), hl

                ; Allocate new descriptor D2.
                call    em_alloc_desc       ; A = D2 index
                ld      (EM_W_SDESC2), a

                ; Copy second-half records into D2's data region.
                ld      a, (EM_W_SDESC2)
                call    em_desc_dataptr_a   ; HL = D2 data base
                ex      de, hl              ; DE = D2 data base
                ld      hl, (EM_W_SMID_PTR) ; HL = second half source
                ld      bc, (EM_W_SRSIZE)
                ld      a, b
                or      c
                jr      z, em_sp_copy_done
                ldir
em_sp_copy_done:

                ; Set D2.line_count = second_half_count.
                ld      a, (EM_W_SDESC2)
                call    em_desc_ptr_a
                inc     hl
                inc     hl
                ld      a, (EM_W_SRHALF_CNT)
                ld      (hl), a

                ; Truncate original descriptor: line_count = mid.
                ld      a, (EM_W_SDESC)
                call    em_desc_ptr_a
                inc     hl
                inc     hl
                ld      a, (EM_W_SMID)
                ld      (hl), a

                ; Insert D2 at ORDER[SPOS+1]: shift ORDER[SPOS+1..NBLOCKS-1] right.
                ; for i = NBLOCKS-1 downto SPOS+1: ORDER[i+1] = ORDER[i]
                ld      a, (EM_NBLOCKS)
                dec     a                   ; A = NBLOCKS-1 (start of shift loop)
                ld      b, a                ; B = i (starts at NBLOCKS-1, counts down)
em_sp_shift_loop:
                ; Stop when i <= SPOS. Compare SPOS vs i (B): carry set iff SPOS < i,
                ; i.e. i > SPOS (continue shifting). No carry iff i <= SPOS (done).
                ld      c, b                ; C = i (save for address calc below)
                ld      a, (EM_W_SPOS)      ; A = SPOS
                cp      c                   ; SPOS - i; carry set iff SPOS < i
                jr      nc, em_sp_shift_done ; no carry: i <= SPOS: done
                ; ORDER[i+1] = ORDER[i]
                ld      hl, EM_ORDER
                ld      d, 0
                ld      e, c
                add     hl, de              ; HL -> ORDER[i]
                ld      a, (hl)
                inc     hl                  ; HL -> ORDER[i+1]
                ld      (hl), a
                dec     b
                jr      em_sp_shift_loop
em_sp_shift_done:
                ; ORDER[SPOS+1] = D2.
                ld      a, (EM_W_SPOS)
                ld      hl, EM_ORDER
                ld      d, 0
                ld      e, a
                add     hl, de
                inc     hl                  ; HL -> ORDER[SPOS+1]
                ld      a, (EM_W_SDESC2)
                ld      (hl), a

                ; EM_NBLOCKS++.
                ld      hl, EM_NBLOCKS
                inc     (hl)

                ; Update EM_LOC for moved (second-half) records only.
                ld      a, (EM_W_SDESC2)
                call    em_desc_dataptr_a   ; HL = D2 data base
                ld      a, (EM_W_SRHALF_CNT)
                ld      b, a
                ld      a, (EM_W_SDESC2)
                ld      c, a                ; C = D2 index
em_sp_loc_loop:
                ld      a, b
                or      a
                jr      z, em_sp_loc_done
                ; Read id[0]:id[1] as 16-bit EM_LOC index.
                ld      e, (hl)             ; id[0]
                inc     hl
                ld      d, (hl)             ; id[1]
                inc     hl
                inc     hl                  ; skip id[2]
                ; EM_LOC[DE] = C.
                push    hl
                push    bc
                ld      hl, EM_LOC
                add     hl, de
                ld      (hl), c
                pop     bc
                pop     hl
                ; Skip len + text.
                ld      a, (hl)             ; len
                inc     hl
                or      a
                jr      z, em_sp_loc_notxt
                ld      d, 0
                ld      e, a
                add     hl, de
em_sp_loc_notxt:
                dec     b
                jr      em_sp_loc_loop
em_sp_loc_done:
                ret

; ===========================================================================
; Working storage (scratch variables used across routines)
; ===========================================================================
EM_W_DOCI:      defs 2          ; document index for current insert
EM_W_LEN:       defs 1          ; text length for current insert
EM_W_ID:        defs 3          ; allocated id (u24 LE)
EM_W_DPOS:      defs 1          ; order position of target block
EM_W_DESC:      defs 1          ; descriptor index of target block
EM_W_LOCAL:     defs 1          ; local line index within block
EM_W_DATA_BASE: defs 2          ; data base pointer for current block
EM_W_NLINES:    defs 1          ; existing line_count during insert
EM_W_INS_PTR:   defs 2          ; absolute pointer to insertion point
EM_W_END_PTR:   defs 2          ; pointer one past last used byte
EM_W_REC_SIZE:  defs 1          ; 4+len for current record
EM_W_BASE:      defs 2          ; base doc-index for em_goto
EM_W_ACC:       defs 2          ; running accumulator in em_block_and_local
EM_W_I:         defs 1          ; loop counter i in em_block_and_local
EM_W_CNT:       defs 1          ; cnt from line_count in em_block_and_local
EM_W_SPOS:      defs 1          ; order position of block being split
EM_W_SDESC:     defs 1          ; descriptor of block being split
EM_W_SNLINES:   defs 1          ; line_count of block being split
EM_W_SMID:      defs 1          ; mid = line_count/2
EM_W_SMID_PTR:  defs 2          ; pointer to start of second half
EM_W_SRHALF_CNT: defs 1         ; second-half record count
EM_W_SRSIZE:    defs 2          ; byte size of second half
EM_W_SDESC2:    defs 1          ; new descriptor index (D2)

; ===========================================================================
; Document state
; ===========================================================================
EM_NBLOCKS:     defs 1          ; blocks in use
EM_NEXT_DESC:   defs 1          ; high-water descriptor allocator
EM_NEXTID:      defs 3          ; next id (u24 LE), initialised to 1

; Descriptor pool: EM_MAX_BLOCKS × 3 bytes = {data_ptr:2LE, line_count:1}.
EM_DESC:        defs EM_MAX_BLOCKS*3

; Order array: descriptor indices in document order (EM_NBLOCKS entries used).
EM_ORDER:       defs EM_MAX_BLOCKS

; loc: EM_LOC[id] = descriptor index holding id. Valid indices: 1..EM_MAX_IDS-1.
EM_LOC:         defs EM_MAX_IDS

; Data regions: descriptor d's data starts at EM_DATA + d*EM_BLOCK_CAP (=256).
; With EM_BLOCK_CAP=256, each block's region is 256-byte aligned, which
; simplifies the data_ptr computation in em_alloc_desc.
EM_DATA:        defs EM_MAX_BLOCKS*EM_BLOCK_CAP

; ===========================================================================
; I/O buffers
; ===========================================================================
EM_IN_TEXT:     defs 256        ; input text for em_insert
EM_IN_ID:       defs 3          ; input id for em_goto (u24 LE)
EM_OUT_ID:      defs 3          ; output id: em_insert result / em_line_at result
EM_OUT_TEXT:    defs 257        ; output [len:1][text:len] from em_line_at
