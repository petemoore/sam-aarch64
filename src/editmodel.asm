; editmodel.asm — flat-memory block-list document model, Brick 1a+1b.
;
; SAM-side Z80 port of the Go authority:
;   tools/sam-aarch64/editmodel/editmodel.go
; Brick 1a: insert, split, line-at, goto-by-id, line-count, block-count.
; Brick 1b: delete, block merge-on-underflow, descriptor free-list.
; Undo and serialize are later bricks.
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
; Descriptor index is a STABLE BLOCK REFERENCE — descriptors are reused via
; a free-list stack when freed by delete/merge (Brick 1b), else high-water
; allocated.
;
; LOCATION TABLE  EM_LOC[id] = descriptor index holding id, or EM_LOC_ABSENT
; (&FF) if the id has been deleted. Survives order-array shifts on block
; insert/remove because only moved records are re-pointed — the design's
; O(½ block) bounded-cost property (§3/§8.1 of the spec).
;
; PROVENANCE: algorithmic port of editmodel.go; flat-memory layout is
; original for the Z80 port.
;
; VERIFICATION: tools/netboot-oracle/z80/editmodel_test.go drives this under
; koron-go/z80, replays random inserts+deletes, and asserts LineAt and
; goto-by-id match the Go oracle on every step.

                ; org only when assembled standalone (the host harness uses
                ; -D EM_STANDALONE=1 for the flat backend and -D EM_PAGED=1 for
                ; the paged backend); an including file supplies its own org.
                ; The paged backend orgs in LMPR-mapped low memory (sections A+B,
                ; &0000-&7FFF) because OUT (251) moves sections C AND D — the
                ; resident structures must not live there (design §8.1 point 1).
                if defined(EM_PAGED)
                org     &0000
                else
                if defined(EM_STANDALONE)
                org     &8000
                endif
                endif

; ---------------------------------------------------------------------------
; Constants
; ---------------------------------------------------------------------------

; EM_BLOCK_CAP: data capacity per block.
;  - flat backend (EM_STANDALONE): 256, so splits fire with a modest insert
;    count in the small flat arena.
;  - paged backend (EM_PAGED): 512 — small enough that splits still fire with a
;    modest op count, large enough that peak live-block count stays well under
;    the physical-page budget (one 16 KB pool page per block, ~30 free).
; The production editor sets the §8.1 8 KB ½-page cap without structural change:
; block capacity is independent of the paging mechanism and of the
; cap-independent logical results the oracle checks (line order, goto, count).
                if defined(EM_PAGED)
EM_BLOCK_CAP:   equ 512
                else
EM_BLOCK_CAP:   equ 256
                endif

                if defined(EM_PAGED)
; EM_SECTION_C: the section-C window base. em_desc_dataptr_a pages a block's pool
; page into section C via OUT (251) and returns this address; all block-data
; access goes through that single choke point. Section C is &8000-&BFFF; blocks
; use only the low EM_BLOCK_CAP bytes of the 16 KB page.
EM_SECTION_C:   equ &8000
; PORT_HMPR: the High Memory Page Register (sections C+D). OUT (PORT_HMPR), page
; maps physical `page` into section C (and page+1 into the unused section D).
PORT_HMPR:      equ 251
                endif

; EM_MAX_BLOCKS: descriptor pool size. 80 is ample for the Brick 1 test.
EM_MAX_BLOCKS:  equ 80

; EM_MAX_IDS: loc-array size; valid ids are 1..EM_MAX_IDS-1.
EM_MAX_IDS:     equ 2048

; EM_LOC_ABSENT: sentinel stored in EM_LOC[id] when a line has been deleted.
; Descriptor indices are < EM_MAX_BLOCKS (80) < &FF, so &FF is unambiguous.
EM_LOC_ABSENT:  equ &FF

; EM_MERGE_THRESH: used-bytes threshold below which a block is a merge
; candidate after a delete. Mirrors blockMergeThresholdBytes = blockCapacityBytes/4
; in editmodel.go.
EM_MERGE_THRESH: equ EM_BLOCK_CAP/4

; Undo/redo journal (Brick 3) test-scale constants. Like EM_BLOCK_CAP, these are
; sized for the flat-memory harness; the real SAM journal arena is sized to the
; editor's RAM budget without structural change. The harness keeps every line's
; text <= EM_UNDO_TEXT_MAX. Mirrors maxUndoDepth in editmodel.go.
EM_MAX_UNDO:     equ 16             ; ring depth; edits beyond this drop the oldest
EM_UNDO_TEXT_MAX: equ 64            ; max journaled line length
EM_UNDO_SLOT_SIZE: equ 7+EM_UNDO_TEXT_MAX ; op:1 at:2 id:3 len:1 text:MAX
EM_OP_DELETE:    equ 1              ; op byte of a delete entry (insert = 0)

; ===========================================================================
; em_reset — initialise document to empty.
;
; Sets EM_NBLOCKS=0, EM_NEXT_DESC=0, EM_NEXTID=1 (u24 LE),
; EM_FREE_COUNT=0.
; ===========================================================================
em_reset:
                if defined(EM_PAGED)
                ; Return every live block's pool page before discarding the
                ; document, so reset (and em_load, which resets first) never
                ; leaks pages. No-op when the document is already empty.
                call    em_free_all_pages
                endif
                xor     a
                ld      (EM_NBLOCKS), a
                ld      (EM_NEXT_DESC), a
                ld      (EM_FREE_COUNT), a
                ld      a, 1
                ld      (EM_NEXTID), a
                xor     a
                ld      (EM_NEXTID+1), a
                ld      (EM_NEXTID+2), a
                ; Clear the undo/redo journal (A is 0 here).
                ld      (EM_UNDO_START), a
                ld      (EM_UNDO_COUNT), a
                ld      (EM_REDO_COUNT), a
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
                call    em_do_insert        ; structural insert (EM_W_DOCI/LEN/ID + EM_IN_TEXT)
                ; Journal this edit: push an opInsert entry, then clear redo
                ; (a fresh edit invalidates any pending redo).
                call    em_ent_set_insert
                call    em_push_undo
                call    em_clear_redo
                ret

; ===========================================================================
; em_do_insert — structural insert with an EXPLICIT id (no id allocation, no
; journaling). Entry: EM_W_DOCI = doc index, EM_W_LEN = text length, EM_W_ID =
; id (u24 LE), text in EM_IN_TEXT. Used by em_insert (public, which allocates
; the id and journals) and by em_undo/em_redo (which replay with the original
; id and must NOT journal). Mirrors doInsert in editmodel.go.
; ===========================================================================
em_do_insert:
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

                ; desc = EM_LOC[id]. &FF means the id was deleted — not found.
                ld      de, EM_LOC
                add     hl, de              ; HL -> EM_LOC[id]
                ld      a, (hl)             ; A = desc (or EM_LOC_ABSENT if deleted)
                cp      EM_LOC_ABSENT
                jp      z, em_goto_notfound ; &FF sentinel: deleted id
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
; em_alloc_desc — allocate a descriptor, return its index in A.
;
; Pops from EM_FREE_LIST when non-empty (reuse a freed descriptor), else
; falls back to the high-water EM_NEXT_DESC++.
;
; data_ptr = EM_DATA + index * EM_BLOCK_CAP (EM_BLOCK_CAP=256).
; Since multiplying by 256 only affects the high byte:
;   data_ptr_lo = EM_DATA & 0xFF   (constant, low byte of EM_DATA)
;   data_ptr_hi = (EM_DATA >> 8) + index
; ---------------------------------------------------------------------------
em_alloc_desc:
                ; Pop from free-list if available.
                ld      a, (EM_FREE_COUNT)
                or      a
                jr      z, em_alloc_highwater

                ; Free-list non-empty: pop the top entry.
                dec     a
                ld      (EM_FREE_COUNT), a
                ld      hl, EM_FREE_LIST
                ld      d, 0
                ld      e, a
                add     hl, de
                ld      a, (hl)             ; A = recycled descriptor index
                push    af
                jr      em_alloc_init_desc

em_alloc_highwater:
                ld      a, (EM_NEXT_DESC)
                push    af                  ; save index

                ; Increment EM_NEXT_DESC.
                ld      hl, EM_NEXT_DESC
                inc     (hl)

em_alloc_init_desc:
                ; Stack top = descriptor index (pushed AF) from the allocator.
                if defined(EM_PAGED)
                ; Paged backend: claim a pool page for the block and write the
                ; descriptor {page, unused, line_count=0}. The page byte at +0 is
                ; what em_desc_dataptr_a later maps into section C.
                ld      a, PP_DOC
                call    pp_alloc_page       ; A = page (clobbers B,C,D,E,HL)
                pop     bc                  ; B = descriptor index (hi byte of AF)
                ld      c, a                ; C = page number
                ld      a, b                ; A = descriptor index
                call    em_desc_ptr_a       ; HL -> descriptor[index]; A,BC preserved
                ld      (hl), c             ; descriptor+0 = page
                inc     hl
                ld      (hl), 0             ; descriptor+1 = unused
                inc     hl
                ld      (hl), 0             ; descriptor+2 = line_count = 0
                ld      a, b                ; A = allocated descriptor index
                ret
                else
                ; Flat backend: data_ptr = EM_DATA + index * EM_BLOCK_CAP. With
                ; EM_BLOCK_CAP=256 the multiply only affects the high byte.
                pop     af
                push    af                  ; save index again

                ; Low byte = EM_DATA_LO (constant); high byte = EM_DATA_HI + index.
                ld      hl, EM_DATA
                ld      b, h                ; B = EM_DATA_HI
                add     a, b                ; A = EM_DATA_HI + index
                ld      d, a
                ld      e, l                ; E = EM_DATA_LO  (low byte of EM_DATA)
                ; DE = data_ptr

                ; Write descriptor: {data_ptr_lo, data_ptr_hi, line_count=0}.
                pop     af                  ; A = index
                push    af
                call    em_desc_ptr_a       ; HL -> descriptor[index]
                ld      (hl), e             ; data_ptr_lo = EM_DATA_LO
                inc     hl
                ld      (hl), d             ; data_ptr_hi = EM_DATA_HI + index
                inc     hl
                ld      (hl), 0             ; line_count = 0

                pop     af                  ; A = allocated index
                ret
                endif

; ---------------------------------------------------------------------------
; em_free_desc — push descriptor index A onto the free-list for later reuse.
;
; Called when a block becomes empty (delete) or is absorbed by a merge.
; The free-list is a stack with EM_FREE_COUNT as the top index.
; ---------------------------------------------------------------------------
em_free_desc:
                if defined(EM_PAGED)
                ; Paged backend: return the block's pool page to FREE before
                ; recycling the descriptor index. The page byte is at +0; it
                ; survives until the index is re-allocated (which overwrites it).
                push    af                  ; save descriptor index
                call    em_desc_ptr_a       ; HL -> descriptor[index]
                ld      a, (hl)             ; A = page
                ld      b, PP_DOC
                call    pp_free_page        ; assert tag + free (clobbers D,E,HL)
                pop     af                  ; A = descriptor index
                endif
                push    hl
                push    de
                ld      hl, EM_FREE_COUNT
                ld      d, 0
                ld      e, (hl)             ; E = current count
                inc     (hl)                ; EM_FREE_COUNT++
                ld      hl, EM_FREE_LIST
                add     hl, de              ; HL -> EM_FREE_LIST[old_count]
                ld      (hl), a             ; store the freed descriptor index
                pop     de
                pop     hl
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
; em_desc_dataptr_a — HL = the block-data base address for descriptor A.
;
; This is the single choke point through which all block data is reached, so
; every single-block op (line_at / goto / intra-block insert+delete / serialize)
; pages its block in immediately before access and stays correct unchanged.
;  - flat backend: HL = EM_DESC[A].data_ptr (the flat arena pointer).
;  - paged backend: OUT (251) maps the block's pool page into section C and
;    HL = EM_SECTION_C (&8000). Two arbitrary blocks cannot be mapped at once
;    (section D = section-C page + 1), so cross-block copies route through
;    EM_SCRATCH — see em_do_split / em_do_merge_next / em_do_merge_prev.
; ---------------------------------------------------------------------------
em_desc_dataptr_a:
                call    em_desc_ptr_a
                if defined(EM_PAGED)
                ld      a, (hl)             ; A = page number (descriptor+0)
                out     (PORT_HMPR), a      ; map the page into section C
                ld      hl, EM_SECTION_C    ; HL = block-data base (&8000)
                ret
                else
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ex      de, hl
                ret
                endif

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

                ; Copy the second half into the resident scratch buffer while the
                ; source block is still mapped. The paged backend cannot map the
                ; source and destination pages at once (design §8.1 point 2), so
                ; the move routes through scratch in both backends. EM_W_SMID_PTR
                ; is still valid here: nothing has re-paged since the mid-walk.
                ld      hl, (EM_W_SMID_PTR) ; HL = second-half source
                ld      de, EM_SCRATCH      ; DE = resident scratch
                ld      bc, (EM_W_SRSIZE)
                ld      a, b
                or      c
                jr      z, em_sp_to_scratch_done
                ldir
em_sp_to_scratch_done:

                ; Allocate new descriptor D2 (claims its pool page in paged mode).
                call    em_alloc_desc       ; A = D2 index
                ld      (EM_W_SDESC2), a

                ; Copy the scratch buffer into D2's (now-mapped) data region.
                ld      a, (EM_W_SDESC2)
                call    em_desc_dataptr_a   ; HL = D2 data base (pages D2 into C)
                ex      de, hl              ; DE = D2 data base
                ld      hl, EM_SCRATCH      ; HL = scratch source
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
; em_delete — delete the line at document index BC.
;
; Entry: BC = document index (0..line_count-1).
; Effect: marks EM_LOC[id] = EM_LOC_ABSENT; closes the intra-block gap by
; shifting packed records after the deleted one down; decrements line_count.
; Then: if block becomes empty, removes its descriptor from EM_ORDER and frees
; it. Else if block underflows (used_bytes < EM_MERGE_THRESH), tries to merge
; with an adjacent block (em_try_merge). Mirrors doDelete in editmodel.go.
; ===========================================================================
em_delete:                              ; public — structural delete + journal
                call    em_do_delete        ; BC = doc index; captures id/len/text
                ; Journal this edit: push an opDelete entry, then clear redo.
                call    em_ent_set_delete
                call    em_push_undo
                call    em_clear_redo
                ret

; ===========================================================================
; em_do_delete — structural delete of the line at document index BC (no
; journaling). Before closing the intra-block gap it captures the removed
; line's id (EM_W_ID), length (EM_W_LEN), and a copy of its text (EM_DEL_TEXT)
; so the caller can journal the edit. Used by em_delete (public) and by
; em_undo/em_redo. Mirrors doDelete in editmodel.go.
; ===========================================================================
em_do_delete:
                ld      (EM_W_DOCI), bc
                ; Find block + local index.
                call    em_block_and_local  ; sets EM_W_DPOS, EM_W_DESC, EM_W_LOCAL

                ; Walk to the deleted record to find its id and record size.
                ld      a, (EM_W_DESC)
                call    em_desc_dataptr_a   ; HL = data base
                ld      b, 0                ; B = records skipped
                ld      a, (EM_W_LOCAL)
                ld      c, a                ; C = target local index
em_del_skip_loop:
                ld      a, b
                cp      c
                jr      z, em_del_at_rec    ; b == local: arrived
                ; skip record: id(3) + len(1) + text(len)
                inc     hl
                inc     hl
                inc     hl
                ld      a, (hl)             ; len
                inc     hl
                or      a
                jr      z, em_del_skip_nolen
                ld      d, 0
                ld      e, a
                add     hl, de
em_del_skip_nolen:
                inc     b
                jr      em_del_skip_loop

em_del_at_rec:
                ; HL -> start of the record to delete.
                ld      (EM_W_DEL_PTR), hl  ; save pointer to deleted record

                ; Read the id (3 bytes LE) and mark it absent in EM_LOC.
                ld      a, (hl)
                ld      (EM_W_ID), a
                inc     hl
                ld      a, (hl)
                ld      (EM_W_ID+1), a
                inc     hl
                ld      a, (hl)
                ld      (EM_W_ID+2), a
                inc     hl
                ; HL now at len byte
                ld      a, (hl)             ; len
                ld      (EM_W_LEN), a
                ; rec_size = 4 + len
                add     a, 4
                ld      (EM_W_REC_SIZE), a

                ; Capture the deleted line's text into EM_DEL_TEXT (for the undo
                ; journal) BEFORE the gap-close shift overwrites it. Source is
                ; EM_W_DEL_PTR + 4 (past the 3-byte id and 1-byte len).
                ld      a, (EM_W_LEN)
                or      a
                jr      z, em_del_text_done
                ld      c, a
                ld      b, 0
                ld      hl, (EM_W_DEL_PTR)
                inc     hl
                inc     hl
                inc     hl
                inc     hl                  ; HL -> text
                ld      de, EM_DEL_TEXT
                ldir
em_del_text_done:

                ; EM_LOC[id] = EM_LOC_ABSENT (&FF).
                ; id is u24 but EM_LOC is indexed by the low 16 bits (id < EM_MAX_IDS).
                ld      hl, (EM_W_ID)       ; HL = id[1]:id[0]
                ld      de, EM_LOC
                add     hl, de
                ld      (hl), EM_LOC_ABSENT

                ; Compute END_PTR for this block by walking all records.
                ld      a, (EM_W_DESC)
                call    em_desc_dataptr_a   ; HL = data base
                ld      (EM_W_DATA_BASE), hl
                ld      a, (EM_W_DESC)
                call    em_desc_linecount_a ; A = line_count (before decrement)
                ld      (EM_W_NLINES), a
                call    em_find_end_ptr     ; sets EM_W_END_PTR

                ; Close the gap: shift bytes from (DEL_PTR + rec_size) .. (END_PTR-1)
                ; down by rec_size. Use LDIR (src > dst = forward copy is safe).
                ld      hl, (EM_W_DEL_PTR)
                ld      a, (EM_W_REC_SIZE)
                ld      d, 0
                ld      e, a
                add     hl, de              ; HL = src (first byte after deleted record)
                ld      de, (EM_W_DEL_PTR)  ; DE = dst (where deleted record was)

                ; shift_count = END_PTR - src
                ld      (EM_W_TMP2), hl     ; save src
                ld      hl, (EM_W_END_PTR)
                ld      bc, (EM_W_TMP2)
                or      a
                sbc     hl, bc              ; HL = shift_count
                ld      a, h
                or      l
                jr      z, em_del_gap_done  ; nothing to shift
                ld      b, h
                ld      c, l
                ld      hl, (EM_W_TMP2)     ; restore src
                ldir

em_del_gap_done:
                ; Decrement line_count in the descriptor.
                ld      a, (EM_W_DESC)
                call    em_desc_ptr_a       ; HL -> descriptor base
                inc     hl
                inc     hl                  ; HL -> line_count byte
                dec     (hl)

                ; If line_count is now 0, remove this block entirely.
                ld      a, (hl)
                or      a
                jr      z, em_del_remove_block

                ; Block still has lines. Check if it underflows.
                ; Compute used_bytes via em_used_bytes_a.
                ld      a, (EM_W_DESC)
                call    em_used_bytes_a     ; HL = used_bytes

                ; if used_bytes < EM_MERGE_THRESH, try merge.
                ld      de, EM_MERGE_THRESH
                or      a
                sbc     hl, de              ; carry if used_bytes < EM_MERGE_THRESH
                jr      nc, em_del_done
                ld      a, (EM_W_DPOS)
                call    em_try_merge
em_del_done:
                ret

em_del_remove_block:
                ; The block is empty. Remove its descriptor index from EM_ORDER
                ; (shift the order array left from DPOS..NBLOCKS-2), free the descriptor,
                ; and decrement EM_NBLOCKS.
                ld      a, (EM_NBLOCKS)
                dec     a                   ; A = NBLOCKS-1 (= index of last entry)
                ld      c, a                ; C = NBLOCKS-1 (loop limit)
                ld      a, (EM_W_DPOS)
                ld      d, a                ; D = current dst index (starts at DPOS)

em_del_shift_loop:
                ld      a, d
                cp      c                   ; d == NBLOCKS-1?
                jr      nc, em_del_order_shifted ; d >= NBLOCKS-1: done
                ; ORDER[d] = ORDER[d+1]
                ld      hl, EM_ORDER
                ld      e, d
                ld      d, 0
                add     hl, de              ; HL -> ORDER[d]
                inc     hl                  ; HL -> ORDER[d+1]
                ld      a, (hl)
                dec     hl                  ; HL -> ORDER[d]
                ld      (hl), a
                ld      d, e                ; restore loop counter D from E (e = old d)
                inc     d
                jr      em_del_shift_loop

em_del_order_shifted:
                ; Free the descriptor and decrement NBLOCKS.
                ld      a, (EM_W_DESC)
                call    em_free_desc
                ld      hl, EM_NBLOCKS
                dec     (hl)
                ret

; ===========================================================================
; em_try_merge — attempt to merge the underflowing block at order position A
; with an adjacent block.
;
; Mirrors tryMerge in editmodel.go: try next neighbour first, then previous.
; "Absorb next into D" means copy next's packed records into D's data region,
; re-point only next's records' EM_LOC to D, remove next from EM_ORDER, free
; next's descriptor, EM_NBLOCKS--. "Absorb D into prev" is the symmetric case.
; Re-pointing only moved records preserves the O(½ block) cost (§3/§8.1).
;
; Fits test: combined <= EM_BLOCK_CAP.  After "sbc hl,de" (HL=combined,
; DE=cap): carry means combined < cap (fits); Z (no carry, HL=0) means
; combined == cap (also fits); no carry + HL!=0 means combined > cap (skip).
; So "fits" iff carry OR zero — equivalent to checking combined-1 < cap,
; i.e. decrement HL by 1 then check carry-only, or just branch on c or z.
; ===========================================================================
em_try_merge:
                ld      (EM_W_MPOS), a      ; save order position of underflowing block

                ; desc of the underflowing block.
                ld      hl, EM_ORDER
                ld      d, 0
                ld      e, a
                add     hl, de
                ld      a, (hl)
                ld      (EM_W_MDESC), a     ; D (the underflowing descriptor)

                ; ---------------------------------------------------------------
                ; Try next neighbour first: MPOS+1 < NBLOCKS?
                ; ---------------------------------------------------------------
                ld      a, (EM_W_MPOS)
                inc     a                   ; A = MPOS+1
                ld      b, a                ; B = MPOS+1
                ld      a, (EM_NBLOCKS)
                cp      b                   ; NBLOCKS - (MPOS+1); carry if NBLOCKS < MPOS+1
                jr      z, em_merge_try_prev ; NBLOCKS == MPOS+1: no next (MPOS is last)
                jr      c, em_merge_try_prev ; NBLOCKS < MPOS+1: impossible but safe

                ; next descriptor at ORDER[MPOS+1].
                ld      hl, EM_ORDER
                ld      d, 0
                ld      e, b                ; E = MPOS+1
                add     hl, de
                ld      a, (hl)
                ld      (EM_W_MNEXT), a     ; next descriptor

                ; Compute used(D) + used(next). Merge if combined <= EM_BLOCK_CAP.
                ld      a, (EM_W_MDESC)
                call    em_used_bytes_a     ; HL = used(D)
                ld      (EM_W_MTMP), hl
                ld      a, (EM_W_MNEXT)
                call    em_used_bytes_a     ; HL = used(next)
                ld      de, (EM_W_MTMP)
                add     hl, de              ; HL = combined
                ld      de, EM_BLOCK_CAP
                or      a
                sbc     hl, de              ; carry iff combined < cap; Z iff combined == cap
                jr      c, em_do_merge_next ; combined < cap: fits
                jr      z, em_do_merge_next ; combined == cap: fits exactly

                ; combined > cap: fall through to try prev.

em_merge_try_prev:
                ; Try previous neighbour: MPOS > 0?
                ld      a, (EM_W_MPOS)
                or      a
                ret     z                   ; MPOS == 0: no previous neighbour

                ; prev descriptor at ORDER[MPOS-1].
                dec     a                   ; A = MPOS-1
                ld      hl, EM_ORDER
                ld      d, 0
                ld      e, a
                add     hl, de
                ld      a, (hl)
                ld      (EM_W_MPREV), a     ; prev descriptor

                ; Compute used(prev) + used(D). Merge if combined <= EM_BLOCK_CAP.
                ld      a, (EM_W_MPREV)
                call    em_used_bytes_a     ; HL = used(prev)
                ld      (EM_W_MTMP), hl
                ld      a, (EM_W_MDESC)
                call    em_used_bytes_a     ; HL = used(D)
                ld      de, (EM_W_MTMP)
                add     hl, de              ; HL = combined
                ld      de, EM_BLOCK_CAP
                or      a
                sbc     hl, de              ; carry iff combined < cap; Z iff combined == cap
                jp      c, em_do_merge_prev ; combined < cap: fits
                jp      z, em_do_merge_prev ; combined == cap: fits exactly
                ret                         ; combined > cap: no merge possible

; ---------------------------------------------------------------------------
; em_do_merge_next — absorb next block into D (the underflowing block).
; On entry: EM_W_MDESC = D, EM_W_MNEXT = next descriptor, EM_W_MPOS = pos of D.
; ---------------------------------------------------------------------------
em_do_merge_next:
                ; Copy next's packed records to the end of D's data region.
                ; Copy next's records into the resident scratch buffer, then
                ; append them at D's end. The paged backend cannot map next and D
                ; at once (design §8.1 point 2), so the move routes through scratch
                ; in both backends. EM_W_MTMP holds the byte count across the steps.
                ; --- 1. byte count = used(next) ---
                ld      a, (EM_W_MNEXT)
                call    em_used_bytes_a     ; HL = used(next)
                ld      (EM_W_MTMP), hl     ; save byte count
                ld      a, h
                or      l
                jr      z, em_mn_copy_done  ; used(next) == 0: nothing to move
                ; --- 2. next's data -> scratch (next mapped here) ---
                ld      bc, (EM_W_MTMP)     ; BC = byte count
                ld      a, (EM_W_MNEXT)
                call    em_desc_dataptr_a   ; HL = next's data base (maps next)
                ld      de, EM_SCRATCH
                ldir                        ; next -> scratch
                ; --- 3. D's end -> DE (maps D) ---
                ld      a, (EM_W_MDESC)
                call    em_used_bytes_a     ; EM_W_END_PTR = D's end (maps D)
                ld      de, (EM_W_END_PTR)  ; DE = append point (D's end)
                ; --- 4. scratch -> D's end ---
                ld      bc, (EM_W_MTMP)     ; BC = byte count
                ld      hl, EM_SCRATCH
                ldir                        ; scratch -> D

em_mn_copy_done:
                ; Add next's line_count to D's line_count.
                ld      a, (EM_W_MNEXT)
                call    em_desc_linecount_a ; A = next's line_count
                ld      c, a
                ld      a, (EM_W_MDESC)
                call    em_desc_ptr_a
                inc     hl
                inc     hl                  ; HL -> D's line_count byte
                ld      a, (hl)
                add     a, c
                ld      (hl), a

                ; Re-point EM_LOC for next's (now moved) records to D. Walk the
                ; scratch copy of next's records so no block needs re-paging.
                ld      a, (EM_W_MNEXT)
                call    em_desc_linecount_a ; A = next's line_count
                ld      b, a
                ld      a, (EM_W_MDESC)
                ld      c, a                ; C = D (new home for moved records)
                ld      hl, EM_SCRATCH      ; walk the moved records in scratch
em_mn_reloc:
                ld      a, b
                or      a
                jr      z, em_mn_reloc_done
                ld      e, (hl)             ; id[0]
                inc     hl
                ld      d, (hl)             ; id[1]
                inc     hl
                inc     hl                  ; skip id[2]
                push    hl
                push    bc
                ld      hl, EM_LOC
                add     hl, de
                ld      (hl), c             ; EM_LOC[id] = D
                pop     bc
                pop     hl
                ld      a, (hl)             ; len
                inc     hl
                or      a
                jr      z, em_mn_reloc_skip
                ld      d, 0
                ld      e, a
                add     hl, de
em_mn_reloc_skip:
                dec     b
                jr      em_mn_reloc
em_mn_reloc_done:

                ; Remove ORDER[MPOS+1] (next) from ORDER: shift ORDER[MPOS+2..NBLOCKS-1] left.
                ; dst = MPOS+1, src = MPOS+2.
                ld      a, (EM_W_MPOS)
                add     a, 2                ; A = MPOS+2 (source start)
                ld      d, a                ; D = source index
em_mn_shift:
                ld      a, (EM_NBLOCKS)
                cp      d                   ; NBLOCKS - d; carry if NBLOCKS < d
                jr      z, em_mn_shifted    ; d == NBLOCKS: done
                jr      c, em_mn_shifted    ; d > NBLOCKS: done
                ; ORDER[d-1] = ORDER[d]
                ld      hl, EM_ORDER
                ld      e, d
                ld      d, 0
                add     hl, de              ; HL -> ORDER[d]
                ld      a, (hl)             ; ORDER[d]
                dec     hl                  ; HL -> ORDER[d-1]
                ld      (hl), a
                ld      d, e                ; restore D from E (E = old d)
                inc     d
                jr      em_mn_shift
em_mn_shifted:
                ld      a, (EM_W_MNEXT)
                call    em_free_desc
                ld      hl, EM_NBLOCKS
                dec     (hl)
                ret

; ---------------------------------------------------------------------------
; em_do_merge_prev — absorb D (the underflowing block) into the previous block.
; On entry: EM_W_MDESC = D, EM_W_MPREV = prev descriptor, EM_W_MPOS = pos of D.
; ---------------------------------------------------------------------------
em_do_merge_prev:
                ; Copy D's records into the resident scratch buffer, then append
                ; them at prev's end. The paged backend cannot map D and prev at
                ; once (design §8.1 point 2), so the move routes through scratch in
                ; both backends. EM_W_MTMP holds the byte count across the steps.
                ; --- 1. byte count = used(D) ---
                ld      a, (EM_W_MDESC)
                call    em_used_bytes_a     ; HL = used(D)
                ld      (EM_W_MTMP), hl     ; save byte count
                ld      a, h
                or      l
                jr      z, em_mp_copy_done  ; used(D) == 0: nothing to move
                ; --- 2. D's data -> scratch (D mapped here) ---
                ld      bc, (EM_W_MTMP)     ; BC = byte count
                ld      a, (EM_W_MDESC)
                call    em_desc_dataptr_a   ; HL = D's data base (maps D)
                ld      de, EM_SCRATCH
                ldir                        ; D -> scratch
                ; --- 3. prev's end -> DE (maps prev) ---
                ld      a, (EM_W_MPREV)
                call    em_used_bytes_a     ; EM_W_END_PTR = prev's end (maps prev)
                ld      de, (EM_W_END_PTR)  ; DE = append point (prev's end)
                ; --- 4. scratch -> prev's end ---
                ld      bc, (EM_W_MTMP)     ; BC = byte count
                ld      hl, EM_SCRATCH
                ldir                        ; scratch -> prev

em_mp_copy_done:
                ; Add D's line_count to prev's line_count.
                ld      a, (EM_W_MDESC)
                call    em_desc_linecount_a ; A = D's line_count
                ld      c, a
                ld      a, (EM_W_MPREV)
                call    em_desc_ptr_a
                inc     hl
                inc     hl                  ; HL -> prev's line_count byte
                ld      a, (hl)
                add     a, c
                ld      (hl), a

                ; Re-point EM_LOC for D's (now moved) records to prev. Walk the
                ; scratch copy of D's records so no block needs re-paging.
                ld      a, (EM_W_MDESC)
                call    em_desc_linecount_a ; A = D's line_count
                ld      b, a
                ld      a, (EM_W_MPREV)
                ld      c, a                ; C = prev (new home for moved records)
                ld      hl, EM_SCRATCH      ; walk the moved records in scratch
em_mp_reloc:
                ld      a, b
                or      a
                jr      z, em_mp_reloc_done
                ld      e, (hl)             ; id[0]
                inc     hl
                ld      d, (hl)             ; id[1]
                inc     hl
                inc     hl                  ; skip id[2]
                push    hl
                push    bc
                ld      hl, EM_LOC
                add     hl, de
                ld      (hl), c             ; EM_LOC[id] = prev
                pop     bc
                pop     hl
                ld      a, (hl)             ; len
                inc     hl
                or      a
                jr      z, em_mp_reloc_skip
                ld      d, 0
                ld      e, a
                add     hl, de
em_mp_reloc_skip:
                dec     b
                jr      em_mp_reloc
em_mp_reloc_done:

                ; Remove ORDER[MPOS] (D) from ORDER: shift ORDER[MPOS+1..NBLOCKS-1] left.
                ; dst = MPOS, src = MPOS+1.
                ld      a, (EM_W_MPOS)
                inc     a                   ; A = MPOS+1 (source start)
                ld      d, a                ; D = source index
em_mp_shift:
                ld      a, (EM_NBLOCKS)
                cp      d                   ; NBLOCKS - d; carry if NBLOCKS < d
                jr      z, em_mp_shifted    ; d == NBLOCKS: done
                jr      c, em_mp_shifted    ; d > NBLOCKS: done
                ; ORDER[d-1] = ORDER[d]
                ld      hl, EM_ORDER
                ld      e, d
                ld      d, 0
                add     hl, de              ; HL -> ORDER[d]
                ld      a, (hl)             ; ORDER[d]
                dec     hl                  ; HL -> ORDER[d-1]
                ld      (hl), a
                ld      d, e                ; restore D from E (E = old d)
                inc     d
                jr      em_mp_shift
em_mp_shifted:
                ld      a, (EM_W_MDESC)
                call    em_free_desc
                ld      hl, EM_NBLOCKS
                dec     (hl)
                ret

; ---------------------------------------------------------------------------
; em_find_end_ptr — compute EM_W_END_PTR by walking EM_W_NLINES records
; from EM_W_DATA_BASE. Unlike em_find_offsets (which also sets INS_PTR),
; this only computes the end pointer — used internally by delete/merge to
; find the tail of the data region.
; ---------------------------------------------------------------------------
em_find_end_ptr:
                ld      hl, (EM_W_DATA_BASE)
                ld      a, (EM_W_NLINES)
                or      a
                jr      z, em_fep_done
                ld      b, a                ; B = records to walk
em_fep_loop:
                inc     hl                  ; skip id[0]
                inc     hl                  ; skip id[1]
                inc     hl                  ; skip id[2]
                ld      a, (hl)             ; len
                inc     hl                  ; skip len
                or      a
                jr      z, em_fep_notext
                ld      d, 0
                ld      e, a
                add     hl, de
em_fep_notext:
                djnz    em_fep_loop
em_fep_done:
                ld      (EM_W_END_PTR), hl
                ret

; ---------------------------------------------------------------------------
; em_used_bytes_a — given descriptor index in A, return used byte count in HL.
; ---------------------------------------------------------------------------
em_used_bytes_a:
                push    af
                call    em_desc_dataptr_a   ; HL = data base
                ld      (EM_W_DATA_BASE), hl
                pop     af
                push    af
                call    em_desc_linecount_a ; A = line_count
                ld      (EM_W_NLINES), a
                call    em_find_end_ptr     ; EM_W_END_PTR = data end
                ld      hl, (EM_W_END_PTR)
                ld      de, (EM_W_DATA_BASE)
                or      a
                sbc     hl, de              ; HL = used_bytes
                pop     af
                ret

; ===========================================================================
; Undo/redo journal (Brick 3) — bounded ring journal, port of editmodel.go
; i41b (pushUndo / InsertLine / DeleteLine / Undo / Redo).
;
; The undo stack is a RING of EM_MAX_UNDO fixed-size slots with drop-oldest
; semantics; the redo stack is a plain LIFO bounded by the same depth (it never
; holds more entries than the undo stack did). A journal slot is:
;   op:1 | at:2 LE | id:3 LE | len:1 | text:EM_UNDO_TEXT_MAX
; EM_ENT is the staging slot built by the public edits and consumed by push.
; ===========================================================================

; ---------------------------------------------------------------------------
; em_slot_offset — HL = A * EM_UNDO_SLOT_SIZE (the byte offset of slot index A).
; ---------------------------------------------------------------------------
em_slot_offset:
                ld      hl, 0
                or      a
                ret     z
                ld      de, EM_UNDO_SLOT_SIZE
                ld      b, a
em_soff_loop:
                add     hl, de
                djnz    em_soff_loop
                ret

; ---------------------------------------------------------------------------
; em_ent_set_insert / em_ent_set_delete — build the staging entry EM_ENT from
; the current edit's EM_W_DOCI (at), EM_W_ID, EM_W_LEN and the text source
; (EM_IN_TEXT for an insert, EM_DEL_TEXT for a delete).
; ---------------------------------------------------------------------------
em_ent_set_insert:
                xor     a
                ld      (EM_ENT+0), a       ; op = insert
                ld      de, EM_IN_TEXT
                jr      em_ent_fill
em_ent_set_delete:
                ld      a, EM_OP_DELETE
                ld      (EM_ENT+0), a       ; op = delete
                ld      de, EM_DEL_TEXT
em_ent_fill:
                ; DE = text source.
                ld      hl, (EM_W_DOCI)
                ld      (EM_ENT+1), hl      ; at (2 LE)
                ld      a, (EM_W_ID)
                ld      (EM_ENT+3), a
                ld      a, (EM_W_ID+1)
                ld      (EM_ENT+4), a
                ld      a, (EM_W_ID+2)
                ld      (EM_ENT+5), a
                ld      a, (EM_W_LEN)
                ld      (EM_ENT+6), a       ; len
                or      a
                ret     z                   ; len == 0: no text body
                ld      c, a
                ld      b, 0
                ld      h, d
                ld      l, e                ; HL = text source
                ld      de, EM_ENT+7        ; DE = dest (text body)
                ldir
                ret

; ---------------------------------------------------------------------------
; em_push_undo — copy EM_ENT into the undo ring at (START+COUNT) and advance,
; dropping the oldest entry when the ring is full (drop-oldest, design §7.2).
; ---------------------------------------------------------------------------
em_push_undo:
                ld      a, (EM_UNDO_START)
                ld      b, a
                ld      a, (EM_UNDO_COUNT)
                add     a, b                ; A = START + COUNT
                cp      EM_MAX_UNDO
                jr      c, em_pu_nomod
                sub     EM_MAX_UNDO
em_pu_nomod:
                call    em_slot_offset      ; HL = pos * slot
                ld      de, EM_UNDO_BASE
                add     hl, de              ; HL = dest slot
                ex      de, hl              ; DE = dest
                ld      hl, EM_ENT
                ld      bc, EM_UNDO_SLOT_SIZE
                ldir
                ; advance: if COUNT < MAX then COUNT++ else drop oldest (START++).
                ld      a, (EM_UNDO_COUNT)
                cp      EM_MAX_UNDO
                jr      nc, em_pu_full
                inc     a
                ld      (EM_UNDO_COUNT), a
                ret
em_pu_full:
                ld      a, (EM_UNDO_START)
                inc     a
                cp      EM_MAX_UNDO
                jr      c, em_pu_sok
                xor     a
em_pu_sok:
                ld      (EM_UNDO_START), a
                ret

; ---------------------------------------------------------------------------
; em_pop_undo — copy the top undo slot (START+COUNT-1) into EM_ENT, COUNT--.
; Caller must ensure EM_UNDO_COUNT > 0.
; ---------------------------------------------------------------------------
em_pop_undo:
                ld      a, (EM_UNDO_COUNT)
                dec     a
                ld      (EM_UNDO_COUNT), a  ; COUNT-- (now = old top offset from START)
                ld      b, a
                ld      a, (EM_UNDO_START)
                add     a, b                ; START + (COUNT-1)
                cp      EM_MAX_UNDO
                jr      c, em_popu_nomod
                sub     EM_MAX_UNDO
em_popu_nomod:
                call    em_slot_offset
                ld      de, EM_UNDO_BASE
                add     hl, de              ; HL = src slot
                ld      de, EM_ENT
                ld      bc, EM_UNDO_SLOT_SIZE
                ldir
                ret

; ---------------------------------------------------------------------------
; em_push_redo — push EM_ENT onto the redo LIFO at index COUNT, COUNT++.
; (Redo depth is bounded by the undo depth, so no overflow check is needed.)
; ---------------------------------------------------------------------------
em_push_redo:
                ld      a, (EM_REDO_COUNT)
                call    em_slot_offset
                ld      de, EM_REDO_BASE
                add     hl, de              ; HL = dest slot
                ex      de, hl              ; DE = dest
                ld      hl, EM_ENT
                ld      bc, EM_UNDO_SLOT_SIZE
                ldir
                ld      a, (EM_REDO_COUNT)
                inc     a
                ld      (EM_REDO_COUNT), a
                ret

; ---------------------------------------------------------------------------
; em_pop_redo — pop the top redo slot (COUNT-1) into EM_ENT, COUNT--.
; Caller must ensure EM_REDO_COUNT > 0.
; ---------------------------------------------------------------------------
em_pop_redo:
                ld      a, (EM_REDO_COUNT)
                dec     a
                ld      (EM_REDO_COUNT), a
                call    em_slot_offset
                ld      de, EM_REDO_BASE
                add     hl, de              ; HL = src slot
                ld      de, EM_ENT
                ld      bc, EM_UNDO_SLOT_SIZE
                ldir
                ret

; ---------------------------------------------------------------------------
; em_clear_redo — empty the redo stack (a fresh public edit invalidates redo).
; ---------------------------------------------------------------------------
em_clear_redo:
                xor     a
                ld      (EM_REDO_COUNT), a
                ret

; ---------------------------------------------------------------------------
; em_ent_id_to_inid — EM_IN_ID = EM_ENT.id (so em_goto can locate the line).
; ---------------------------------------------------------------------------
em_ent_id_to_inid:
                ld      a, (EM_ENT+3)
                ld      (EM_IN_ID), a
                ld      a, (EM_ENT+4)
                ld      (EM_IN_ID+1), a
                ld      a, (EM_ENT+5)
                ld      (EM_IN_ID+2), a
                ret

; ---------------------------------------------------------------------------
; em_ent_to_insert_params — set up em_do_insert's inputs from EM_ENT:
; EM_W_DOCI = at, EM_W_ID = id, EM_W_LEN = len, EM_IN_TEXT = text body.
; ---------------------------------------------------------------------------
em_ent_to_insert_params:
                ld      hl, (EM_ENT+1)
                ld      (EM_W_DOCI), hl     ; at
                ld      a, (EM_ENT+3)
                ld      (EM_W_ID), a
                ld      a, (EM_ENT+4)
                ld      (EM_W_ID+1), a
                ld      a, (EM_ENT+5)
                ld      (EM_W_ID+2), a
                ld      a, (EM_ENT+6)
                ld      (EM_W_LEN), a
                or      a
                ret     z                   ; len == 0: no text body
                ld      c, a
                ld      b, 0
                ld      hl, EM_ENT+7
                ld      de, EM_IN_TEXT
                ldir
                ret

; ===========================================================================
; em_undo — reverse the most recent public edit. Returns A=1 if an edit was
; undone, A=0 if the undo stack is empty. The popped entry moves to the redo
; stack. Inversion uses the non-journaling primitives so it does not itself
; record history. Mirrors Undo in editmodel.go.
;   undo-of-insert: find the line by id (em_goto, drift-robust) and delete it.
;   undo-of-delete: re-insert at the recorded at with the original id/text
;                   (position valid by the strict-LIFO argument, design §7.2).
; ===========================================================================
em_undo:
                ld      a, (EM_UNDO_COUNT)
                or      a
                jr      z, em_undo_none
                call    em_pop_undo         ; EM_ENT <- top undo entry
                ld      a, (EM_ENT+0)
                or      a
                jr      nz, em_undo_was_del
                ; op == insert: invert by deleting the line with EM_ENT.id.
                call    em_ent_id_to_inid
                call    em_goto             ; HL = current index (must be live)
                ld      b, h
                ld      c, l
                call    em_do_delete
                jr      em_undo_finish
em_undo_was_del:
                ; op == delete: invert by re-inserting at EM_ENT.at, same id.
                call    em_ent_to_insert_params
                call    em_do_insert
em_undo_finish:
                call    em_push_redo
                ld      a, 1
                ret
em_undo_none:
                xor     a
                ret

; ===========================================================================
; em_redo — re-apply the most recently undone edit. Returns A=1 if a redo was
; performed, A=0 if the redo stack is empty. The popped entry moves back to the
; undo stack (respecting the drop-oldest cap). Mirrors Redo in editmodel.go.
;   redo-of-insert: re-insert at the recorded at with the original id/text.
;   redo-of-delete: find the line by id (em_goto) and delete it.
; ===========================================================================
em_redo:
                ld      a, (EM_REDO_COUNT)
                or      a
                jr      z, em_redo_none
                call    em_pop_redo         ; EM_ENT <- top redo entry
                ld      a, (EM_ENT+0)
                or      a
                jr      nz, em_redo_was_del
                ; op == insert: re-apply the insert at EM_ENT.at, same id.
                call    em_ent_to_insert_params
                call    em_do_insert
                jr      em_redo_finish
em_redo_was_del:
                ; op == delete: re-apply the delete of EM_ENT.id.
                call    em_ent_id_to_inid
                call    em_goto
                ld      b, h
                ld      c, l
                call    em_do_delete
em_redo_finish:
                call    em_push_undo
                ld      a, 1
                ret
em_redo_none:
                xor     a
                ret

; ---------------------------------------------------------------------------
; em_can_undo / em_can_redo — A=1 if at least one entry is available, else 0.
; ---------------------------------------------------------------------------
em_can_undo:
                ld      a, (EM_UNDO_COUNT)
                or      a
                jr      z, em_cu_no
                ld      a, 1
                ret
em_cu_no:
                xor     a
                ret
em_can_redo:
                ld      a, (EM_REDO_COUNT)
                or      a
                jr      z, em_cr_no
                ld      a, 1
                ret
em_cr_no:
                xor     a
                ret

; ===========================================================================
; em_serialize — write the document to EM_SER_BUF in the EMDL v1 format
; (port of Serialize in tools/sam-aarch64/editmodel/serialize.go):
;   "EMDL" | ver:1 | linecount:4 LE | per line { id:3 LE | textlen:2 LE | text }
; Block boundaries are NOT recorded (the format is partition-independent), so
; em_serialize(em_load(x)) is byte-stable. Returns the byte length in HL.
; The record's 1-byte length is written zero-extended to the format's 2-byte
; field; the flat-memory model caps a line at 255 bytes (the Brick 1 record).
; ===========================================================================
em_serialize:
                ld      hl, EM_SER_BUF
                ld      (hl), &45           ; 'E'
                inc     hl
                ld      (hl), &4D           ; 'M'
                inc     hl
                ld      (hl), &44           ; 'D'
                inc     hl
                ld      (hl), &4C           ; 'L'
                inc     hl
                ld      (hl), 1             ; version
                inc     hl
                ld      (EM_SER_PTR), hl    ; output ptr (after version)

                call    em_line_count       ; HL = line count (u16)
                ld      (EM_SER_CNT), hl

                ld      hl, (EM_SER_PTR)
                ld      a, (EM_SER_CNT)
                ld      (hl), a             ; count[0]
                inc     hl
                ld      a, (EM_SER_CNT+1)
                ld      (hl), a             ; count[1]
                inc     hl
                ld      (hl), 0             ; count[2]
                inc     hl
                ld      (hl), 0             ; count[3]
                inc     hl
                ld      (EM_SER_PTR), hl    ; output ptr (after header)

                ld      a, (EM_NBLOCKS)
                or      a
                jr      z, em_ser_done
                xor     a
                ld      (EM_SER_P), a       ; order position P = 0
em_ser_block_loop:
                ld      a, (EM_SER_P)
                ld      c, a
                ld      a, (EM_NBLOCKS)
                cp      c
                jr      z, em_ser_done      ; P == NBLOCKS: done
                ; desc = ORDER[P]
                ld      hl, EM_ORDER
                ld      d, 0
                ld      e, c
                add     hl, de
                ld      a, (hl)
                ld      (EM_SER_DESC), a
                call    em_desc_dataptr_a   ; HL = data base
                ld      (EM_SER_REC), hl
                ld      a, (EM_SER_DESC)
                call    em_desc_linecount_a ; A = block line_count
                ld      (EM_SER_N), a
em_ser_rec_loop:
                ld      a, (EM_SER_N)
                or      a
                jr      z, em_ser_block_next
                ; copy id(3) | textlen(2 LE) | text(len) from record to output
                ld      hl, (EM_SER_REC)
                ld      de, (EM_SER_PTR)
                ld      a, (hl)             ; id[0]
                ld      (de), a
                inc     hl
                inc     de
                ld      a, (hl)             ; id[1]
                ld      (de), a
                inc     hl
                inc     de
                ld      a, (hl)             ; id[2]
                ld      (de), a
                inc     hl
                inc     de
                ld      a, (hl)             ; len (1 byte in the record)
                inc     hl                  ; HL -> text
                ld      (de), a             ; textlen[0]
                inc     de
                push    af                  ; save len
                xor     a
                ld      (de), a             ; textlen[1] = 0
                inc     de
                pop     af                  ; A = len
                or      a
                jr      z, em_ser_rec_notext
                ld      c, a
                ld      b, 0
                ldir                        ; copy len text bytes HL->DE
em_ser_rec_notext:
                ld      (EM_SER_REC), hl
                ld      (EM_SER_PTR), de
                ld      a, (EM_SER_N)
                dec     a
                ld      (EM_SER_N), a
                jr      em_ser_rec_loop
em_ser_block_next:
                ld      a, (EM_SER_P)
                inc     a
                ld      (EM_SER_P), a
                jr      em_ser_block_loop
em_ser_done:
                ld      hl, (EM_SER_PTR)
                ld      de, EM_SER_BUF
                or      a
                sbc     hl, de              ; HL = serialized byte length
                ret

; ===========================================================================
; em_load — rebuild the document from an EMDL v1 stream in EM_SER_BUF (port of
; Load in serialize.go). Returns A=1 on success, A=0 if the magic/version is
; unrecognised (fail-loud; the document is left untouched on a bad header). On
; success the document is reset and refilled in stream order, and EM_NEXTID is
; set to maxID+1. Lines are re-blocked by em_do_insert's split-on-overflow;
; since the format is partition-independent, the reserialized bytes still match.
; ===========================================================================
em_load:
                ; Validate the header WITHOUT mutating the document.
                ld      hl, EM_SER_BUF
                ld      a, (hl)
                cp      &45                 ; 'E'
                jp      nz, em_load_fail
                inc     hl
                ld      a, (hl)
                cp      &4D                 ; 'M'
                jp      nz, em_load_fail
                inc     hl
                ld      a, (hl)
                cp      &44                 ; 'D'
                jp      nz, em_load_fail
                inc     hl
                ld      a, (hl)
                cp      &4C                 ; 'L'
                jp      nz, em_load_fail
                inc     hl
                ld      a, (hl)
                cp      1                   ; version
                jp      nz, em_load_fail
                inc     hl
                ld      (EM_LOAD_PTR), hl   ; ptr past version

                call    em_reset            ; empties doc + journal; EM_NEXTID=1

                ld      hl, (EM_LOAD_PTR)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                inc     hl                  ; skip count[2]
                inc     hl                  ; skip count[3]
                ld      (EM_LOAD_N), de     ; remaining lines (low 16 bits)
                ld      (EM_LOAD_PTR), hl

                ld      hl, 0
                ld      (EM_LOAD_I), hl     ; lines inserted so far (= append index)
                ld      (EM_LOAD_MAXID), hl
                xor     a
                ld      (EM_LOAD_MAXID+2), a
em_load_line_loop:
                ld      hl, (EM_LOAD_N)
                ld      a, h
                or      l
                jr      z, em_load_done
                ; read id(3) | textlen(2) | text
                ld      hl, (EM_LOAD_PTR)
                ld      a, (hl)
                ld      (EM_W_ID), a
                inc     hl
                ld      a, (hl)
                ld      (EM_W_ID+1), a
                inc     hl
                ld      a, (hl)
                ld      (EM_W_ID+2), a
                inc     hl
                ld      a, (hl)             ; textlen[0]
                ld      (EM_W_LEN), a
                inc     hl
                inc     hl                  ; skip textlen[1] (0; line <= 255)
                ; copy text into EM_IN_TEXT
                ld      a, (EM_W_LEN)
                or      a
                jr      z, em_load_notext
                ld      c, a
                ld      b, 0
                ld      de, EM_IN_TEXT
                ldir                        ; HL(text) -> EM_IN_TEXT; HL past text
em_load_notext:
                ld      (EM_LOAD_PTR), hl   ; ptr at next record
                call    em_load_track_maxid
                ; insert at end: EM_W_DOCI = lines inserted so far.
                ld      hl, (EM_LOAD_I)
                ld      (EM_W_DOCI), hl
                call    em_do_insert
                ld      hl, (EM_LOAD_I)
                inc     hl
                ld      (EM_LOAD_I), hl
                ld      hl, (EM_LOAD_N)
                dec     hl
                ld      (EM_LOAD_N), hl
                jr      em_load_line_loop
em_load_done:
                ; EM_NEXTID = maxID + 1.
                ld      a, (EM_LOAD_MAXID)
                ld      (EM_NEXTID), a
                ld      a, (EM_LOAD_MAXID+1)
                ld      (EM_NEXTID+1), a
                ld      a, (EM_LOAD_MAXID+2)
                ld      (EM_NEXTID+2), a
                ld      hl, EM_NEXTID
                inc     (hl)
                jr      nz, em_load_nid_done
                inc     hl
                inc     (hl)
                jr      nz, em_load_nid_done
                inc     hl
                inc     (hl)
em_load_nid_done:
                ld      a, 1
                ret
em_load_fail:
                xor     a
                ret

; ---------------------------------------------------------------------------
; em_load_track_maxid — EM_LOAD_MAXID = max(EM_LOAD_MAXID, EM_W_ID) as u24.
; ---------------------------------------------------------------------------
em_load_track_maxid:
                ld      a, (EM_W_ID+2)
                ld      b, a
                ld      a, (EM_LOAD_MAXID+2)
                cp      b
                jr      c, em_ltm_set       ; MAXID2 < ID2 -> ID is larger
                jr      nz, em_ltm_no        ; MAXID2 > ID2 -> ID is smaller
                ld      a, (EM_W_ID+1)
                ld      b, a
                ld      a, (EM_LOAD_MAXID+1)
                cp      b
                jr      c, em_ltm_set
                jr      nz, em_ltm_no
                ld      a, (EM_W_ID)
                ld      b, a
                ld      a, (EM_LOAD_MAXID)
                cp      b
                jr      c, em_ltm_set
                jr      em_ltm_no
em_ltm_set:
                ld      a, (EM_W_ID)
                ld      (EM_LOAD_MAXID), a
                ld      a, (EM_W_ID+1)
                ld      (EM_LOAD_MAXID+1), a
                ld      a, (EM_W_ID+2)
                ld      (EM_LOAD_MAXID+2), a
em_ltm_no:
                ret

                if defined(EM_PAGED)
; ===========================================================================
; Paged backend: page-pool integration (Brick 2)
; ===========================================================================

; ---------------------------------------------------------------------------
; em_free_all_pages — return every live block's pool page to FREE.
;
; Walks EM_ORDER[0..EM_NBLOCKS-1]; for each descriptor frees its page byte
; (descriptor+0) with tag PP_DOC. Called by em_reset before it discards the
; document, so a reset (or em_load, which resets first) never leaks pages.
; No-op when EM_NBLOCKS == 0.
; ---------------------------------------------------------------------------
em_free_all_pages:
                ld      a, (EM_NBLOCKS)
                or      a
                ret     z
                ld      b, a                ; B = blocks remaining
                ld      ix, EM_ORDER
em_fap_loop:
                ld      a, (ix+0)           ; A = descriptor index
                push    bc                  ; save loop counter (pp_free_page clobbers B)
                push    ix
                call    em_desc_ptr_a       ; HL -> descriptor[index]
                ld      a, (hl)             ; A = page
                ld      b, PP_DOC
                call    pp_free_page        ; assert tag + free (clobbers D,E,HL)
                pop     ix
                pop     bc
                inc     ix
                djnz    em_fap_loop
                ret

                ; The page allocator the paged backend allocates/frees/maps blocks
                ; through (pp_alloc_page / pp_free_page + the page_owner[] table).
                ; Included resident (no PP_STANDALONE org, no PP_TABLE_BASE) so its
                ; routines and table follow this code in low memory.
                include "pagepool.asm"
                endif

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
; Delete/merge working storage.
EM_W_DEL_PTR:   defs 2          ; pointer to record being deleted
EM_W_TMP2:      defs 2          ; scratch pointer (gap-close source)
EM_W_MPOS:      defs 1          ; order position of underflowing block
EM_W_MDESC:     defs 1          ; descriptor of underflowing block (D)
EM_W_MNEXT:     defs 1          ; descriptor of next neighbour
EM_W_MPREV:     defs 1          ; descriptor of previous neighbour
EM_W_MTMP:      defs 2          ; scratch for used-byte sums in merge

; ===========================================================================
; Document state
; ===========================================================================
EM_NBLOCKS:     defs 1          ; blocks in use
EM_NEXT_DESC:   defs 1          ; high-water descriptor allocator
EM_NEXTID:      defs 3          ; next id (u24 LE), initialised to 1

; Free-list stack for descriptor reuse (Brick 1b delete/merge).
; EM_FREE_LIST[0..EM_FREE_COUNT-1] holds freed descriptor indices.
; em_free_desc pushes, em_alloc_desc pops when non-empty.
EM_FREE_COUNT:  defs 1
EM_FREE_LIST:   defs EM_MAX_BLOCKS

; Descriptor pool: EM_MAX_BLOCKS × 3 bytes = {data_ptr:2LE, line_count:1}.
EM_DESC:        defs EM_MAX_BLOCKS*3

; Order array: descriptor indices in document order (EM_NBLOCKS entries used).
EM_ORDER:       defs EM_MAX_BLOCKS

; loc: EM_LOC[id] = descriptor index holding id. Valid indices: 1..EM_MAX_IDS-1.
EM_LOC:         defs EM_MAX_IDS

                if defined(EM_PAGED)==0
; Flat backend only: descriptor d's data starts at EM_DATA + d*EM_BLOCK_CAP
; (=256). With EM_BLOCK_CAP=256, each block's region is 256-byte aligned, which
; simplifies the data_ptr computation in em_alloc_desc. The paged backend has no
; arena — each block lives in its own pool page, reached via section C.
EM_DATA:        defs EM_MAX_BLOCKS*EM_BLOCK_CAP
                endif

; EM_SCRATCH: resident cross-block copy buffer. Split and both merges copy a
; source block's records here, page in the destination block, then copy them out
; — the paged backend cannot map two arbitrary pool pages at once (design §8.1
; point 2). Sized to one block (the largest single-copy is < EM_BLOCK_CAP bytes).
; Present in both backends (the flat backend just routes through it harmlessly),
; so the split/merge logic needs no per-backend branch.
EM_SCRATCH:     defs EM_BLOCK_CAP

; ===========================================================================
; I/O buffers
; ===========================================================================
EM_IN_TEXT:     defs 256        ; input text for em_insert
EM_IN_ID:       defs 3          ; input id for em_goto (u24 LE)
EM_OUT_ID:      defs 3          ; output id: em_insert result / em_line_at result
EM_OUT_TEXT:    defs 257        ; output [len:1][text:len] from em_line_at

; ===========================================================================
; Undo/redo journal storage (Brick 3)
; ===========================================================================
EM_DEL_TEXT:    defs EM_UNDO_TEXT_MAX   ; captured text of the line being deleted
EM_ENT:         defs EM_UNDO_SLOT_SIZE  ; staging journal entry
EM_UNDO_START:  defs 1                  ; ring index of the oldest undo entry
EM_UNDO_COUNT:  defs 1                  ; live undo entries (0..EM_MAX_UNDO)
EM_REDO_COUNT:  defs 1                  ; live redo entries (0..EM_MAX_UNDO)
EM_UNDO_BASE:   defs EM_MAX_UNDO*EM_UNDO_SLOT_SIZE  ; undo ring slots
EM_REDO_BASE:   defs EM_MAX_UNDO*EM_UNDO_SLOT_SIZE  ; redo LIFO slots

; ===========================================================================
; EMDL serialize/load storage (Brick 4a)
; ===========================================================================
EM_SER_PTR:     defs 2          ; running output pointer in em_serialize
EM_SER_CNT:     defs 2          ; line count captured for the header
EM_SER_P:       defs 1          ; order position being serialized
EM_SER_DESC:    defs 1          ; descriptor of the block being serialized
EM_SER_REC:     defs 2          ; running record pointer within the block
EM_SER_N:       defs 1          ; records remaining in the block
EM_LOAD_PTR:    defs 2          ; running input pointer in em_load
EM_LOAD_N:      defs 2          ; lines remaining to load
EM_LOAD_I:      defs 2          ; lines inserted so far (append index)
EM_LOAD_MAXID:  defs 3          ; max id seen (u24) -> EM_NEXTID = max+1

; EMDL buffer. Sized for the flat-memory harness (header 9 + per line 5+len);
; the serialize test bounds the document to fit. The real SAM editor sizes this
; to its RAM budget without structural change (like EM_BLOCK_CAP).
EM_SER_BUF:     defs 3072
