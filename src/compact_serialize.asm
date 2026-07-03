; compact_serialize.asm — the compact v2 .tbn file serializer (item
; i48c-b8c): the Z80 port of format.WriteFile and the on-disk table
; encoders it calls, turning the compact-core walk outputs (header
; label/local rows, the compacted record stream, the editor-region
; sidecar rows and global flags) plus the interned name list into the
; full .tbn byte stream.
;
; Z80 port of:
;   tools/sam-aarch64-format/writer.go::WriteFile (114-148) — the file
;       layout: 12-byte header (magic "SA64" + version u16 + flags u16 +
;       editor_region_offset u32 LE), label/local tables, record stream
;       (verbatim), editor region; the editor_region_offset backpatch.
;   tools/sam-aarch64-format/header_tables.go::writeLabelTable (35-58)
;       and writeLocalTable (64-85) — [count u16 LE] then sorted rows of
;       [name_id u16 LE][offset_delta uvarint] / [digit u8][delta].
;   tools/sam-aarch64-format/editor_region.go::appendNameTable (96-113,
;       front-coded names), commonPrefixLen (83-93), appendGlobalFlags
;       (152-164, ascending id list), appendSidecar (201-231, tagged
;       rows with delta-uvarint anchors).
;   encoding/binary::PutUvarint — 7-bit little-endian groups with a
;       continuation bit (ser_emit_uvarint).
;
; INPUTS are parameter cells (the caller writes them before calling
; compact_serialize) — the memory mirror of WriteFile's signature:
;
;   ser_labels_ptr/count    label rows [name_id:2 LE][offset:4 LE] in the
;                           b8b capture format (COMPACT_LABELROWS,
;                           src/test_compact_ir.asm); any order — sorted
;                           in place here.
;   ser_locals_ptr/count    local rows [digit:1][offset:4 LE]; ditto.
;   ser_recs_ptr/len        the compacted record stream, copied verbatim
;                           (WriteFile treats records as opaque bytes).
;   ser_names_ptr           the interned name list in ID order:
;                           [count:2 LE] then per name [len:1][bytes].
;   ser_globals_ptr/count   `.global` name_ids [id:2 LE]* in the b8b
;                           capture format (COMPACT_GLOBALS); sorted in
;                           place here.
;   ser_sidecar_ptr/count   sidecar rows in the b8b capture format
;                           (COMPACT_SIDECAR): [kind:1][anchor:4 LE] then
;                           kind 0 [placement:1][len:2 LE][body] /
;                           kind 1 [run_len:4 LE]. MUST be anchor-sorted
;                           (see the guard note below).
;   ser_out_base/end        the output window (end one past; base <= end
;                           as u16 — the same no-wrap contract as
;                           cemit_init, so a window may reach &FFFF at
;                           most).
;
; OUTPUT: the full .tbn at (ser_out_base), length in ser_out_len.
;
; Offsets are u32: the b8a/b8b harness contract (PASS_PC carries the low
; 32 bits of the VMA; the origin high words cancel in the subtraction).
; headerRows' int64 offsets fit u32 for every in-scope fixture — a
; label/local before the origin-seeding `.org` (a negative offset) is
; screened out host-side (compact_serialize_test.go), like the walk's
; other cap screens.
;
; THE SIDECAR SORT (a porting decision): appendSidecar STABLE-SORTS rows
; by anchor (editor_region.go:203). This port emits rows in input order
; under a LOUD monotonicity guard instead: the b8b walk produces rows in
; source order with non-decreasing anchors (RecordPC never decreases — a
; backward `.org` is a pass-1 error — and OriginVMA is fixed once
; seeded), so the stable sort is the identity on every input the walk
; can produce, and order-emission is byte-identical. An out-of-order
; anchor is an input-contract bug, failed with &d1 rather than silently
; re-sorted. The label/local/global tables DO sort here (their capture
; order genuinely differs from disk order on ties / unordered `.global`s).
;
; Fail tags (this file's &d0-&d3 range; the harness parks the tag):
;   &d0  SER_TAG_OVERFLOW       output window overflow
;   &d1  SER_TAG_SIDECAR_ORDER  sidecar anchors not non-decreasing
;   &d2  SER_TAG_ROW_ORDER      label/local delta borrow after the sort
;                               (a sort bug — defensive, never a data
;                               condition)
;   &d3  SER_TAG_SIDECAR_KIND   sidecar row kind > 1
; -----------------------------------------------------------------------

SER_TBN_VERSION:        equ     2           ; format.Version (format.go:13)
SER_TBN_FLAGS:          equ     1           ; format.Flags = FlagTaggedSidecar
                                            ; (format.go:18/27)

SER_TAG_OVERFLOW:       equ     &d0
SER_TAG_SIDECAR_ORDER:  equ     &d1
SER_TAG_ROW_ORDER:      equ     &d2
SER_TAG_SIDECAR_KIND:   equ     &d3

; -----------------------------------------------------------------------
; compact_serialize — entry point. Serialize the full .tbn from the
; parameter cells above. Clobbers everything.
; -----------------------------------------------------------------------
compact_serialize:
                ld      hl, (ser_out_base)
                ld      (ser_out_cursor), hl

; -- Sort the inputs into on-disk order. The Go writers sort copies
; -- (header_tables.go:36/65, editor_region.go:153); sorting in place is
; -- equivalent here — the row buffers are this serializer's inputs and
; -- the capture order is not consumed again.
                ld      hl, (ser_labels_ptr)
                ld      de, (ser_labels_count)
                ld      a, 6
                call    ser_sort_rows           ; (offset, name_id) — header_tables.go:37-42
                ld      hl, (ser_locals_ptr)
                ld      de, (ser_locals_count)
                ld      a, 5
                call    ser_sort_rows           ; (offset, digit) — header_tables.go:66-71
                ld      hl, (ser_globals_ptr)
                ld      de, (ser_globals_count)
                ld      a, 2
                call    ser_sort_rows           ; id ascending — editor_region.go:154

; -- 12-byte header (writer.go:120-132): magic, version, flags, and a
; -- zero placeholder for editor_region_offset, backpatched once the
; -- record stream is in place (its low word is the only nonzero part:
; -- the output window is 16-bit, so the offset always fits u16).
                ld      a, "S"
                call    ser_emit_a
                ld      a, "A"
                call    ser_emit_a
                ld      a, "6"
                call    ser_emit_a
                ld      a, "4"
                call    ser_emit_a
                ld      hl, SER_TBN_VERSION
                call    ser_emit_u16
                ld      hl, SER_TBN_FLAGS
                call    ser_emit_u16
                ld      hl, 0
                call    ser_emit_u16
                ld      hl, 0
                call    ser_emit_u16            ; editor_region_offset placeholder

; -- header label table (writeLabelTable, header_tables.go:35-58):
; -- [count u16 LE] then per row [name_id u16 LE][offset_delta uvarint],
; -- deltas accumulating from 0.
                ld      hl, (ser_labels_count)
                call    ser_emit_u16
                call    ser_prev32_zero
                ld      hl, (ser_labels_count)
                ld      (ser_count), hl
                ld      hl, (ser_labels_ptr)
                ld      (ser_ptr), hl
ser_lt_loop:
                ld      hl, (ser_count)
                ld      a, h
                or      l
                jr      z, ser_lt_done
                dec     hl
                ld      (ser_count), hl
                ld      hl, (ser_ptr)
                ld      a, (hl)
                call    ser_emit_a              ; name_id lo
                inc     hl
                ld      a, (hl)
                call    ser_emit_a              ; name_id hi
                inc     hl
                ex      de, hl                  ; DE -> offset u32
                call    ser_delta32             ; uv_buf = offset - prev; HL -> next row
                jp      c, ser_fail_row_order   ; sorted rows never borrow
                ld      (ser_ptr), hl
                call    ser_emit_uvarint
                jr      ser_lt_loop
ser_lt_done:

; -- header local table (writeLocalTable, header_tables.go:64-85):
; -- [count u16 LE] then per row [digit u8][offset_delta uvarint], with
; -- an independent delta accumulator.
                ld      hl, (ser_locals_count)
                call    ser_emit_u16
                call    ser_prev32_zero
                ld      hl, (ser_locals_count)
                ld      (ser_count), hl
                ld      hl, (ser_locals_ptr)
                ld      (ser_ptr), hl
ser_ot_loop:
                ld      hl, (ser_count)
                ld      a, h
                or      l
                jr      z, ser_ot_done
                dec     hl
                ld      (ser_count), hl
                ld      hl, (ser_ptr)
                ld      a, (hl)
                call    ser_emit_a              ; digit
                inc     hl
                ex      de, hl                  ; DE -> offset u32
                call    ser_delta32
                jp      c, ser_fail_row_order
                ld      (ser_ptr), hl
                call    ser_emit_uvarint
                jr      ser_ot_loop
ser_ot_done:

; -- record stream (writer.go:140): copied verbatim — WriteFile never
; -- parses the records.
                ld      hl, (ser_recs_ptr)
                ld      bc, (ser_recs_len)
                call    ser_copy

; -- editor_region_offset backpatch (writer.go:118-121): the byte count
; -- before the editor region = cursor - base, stored u32 LE at base+8
; -- (high word stays the placeholder zero).
                ld      hl, (ser_out_cursor)
                ld      de, (ser_out_base)
                or      a
                sbc     hl, de                  ; HL = editor offset
                push    hl
                ld      hl, (ser_out_base)
                ld      de, 8
                add     hl, de
                pop     de                      ; DE = editor offset
                ld      (hl), e
                inc     hl
                ld      (hl), d

; -- editor region: name table (appendNameTable, editor_region.go:96-113).
; -- [count u16 LE] then per name, front-coded against the PREVIOUS name:
; -- [shared_prefix_len uvarint][suffix_len uvarint][suffix bytes].
                ld      hl, (ser_names_ptr)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (ser_ptr), hl           ; -> first name's len byte
                ex      de, hl                  ; HL = count
                ld      (ser_count), hl
                call    ser_emit_u16
                xor     a
                ld      (ser_prev_name_len), a  ; prev = the empty string
ser_nt_loop:
                ld      hl, (ser_count)
                ld      a, h
                or      l
                jr      z, ser_nt_done
                dec     hl
                ld      (ser_count), hl
                ld      hl, (ser_ptr)
                ld      a, (hl)
                inc     hl
                ld      (ser_cur_name_len), a
                ld      (ser_cur_name_ptr), hl
; shared = commonPrefixLen(prev, cur) (editor_region.go:83-93).
                ld      b, a                    ; B = cur_len
                ld      a, (ser_prev_name_len)
                cp      b
                jr      nc, ser_nt_have_min     ; prev_len >= cur_len: min = cur_len (B)
                ld      b, a                    ; min = prev_len
ser_nt_have_min:
                xor     a
                ld      (ser_shared), a
                ld      a, b
                or      a
                jr      z, ser_nt_shared_done
                ld      hl, (ser_prev_name_ptr)
                ld      de, (ser_cur_name_ptr)
ser_nt_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, ser_nt_shared_done
                inc     hl
                inc     de
                ld      a, (ser_shared)
                inc     a
                ld      (ser_shared), a
                djnz    ser_nt_cmp
ser_nt_shared_done:
                ld      a, (ser_shared)
                call    ser_uv_from_a
                call    ser_emit_uvarint        ; shared_prefix_len
                ld      a, (ser_cur_name_len)
                ld      hl, ser_shared
                sub     (hl)
                ld      (ser_suffix_len), a
                call    ser_uv_from_a
                call    ser_emit_uvarint        ; suffix_len
                ld      hl, (ser_cur_name_ptr)
                ld      a, (ser_shared)
                ld      c, a
                ld      b, 0
                add     hl, bc                  ; -> suffix bytes
                ld      a, (ser_suffix_len)
                ld      c, a
                ld      b, 0
                call    ser_copy                ; suffix
; prev := cur; advance the walk past the name bytes.
                ld      hl, (ser_cur_name_ptr)
                ld      (ser_prev_name_ptr), hl
                ld      a, (ser_cur_name_len)
                ld      (ser_prev_name_len), a
                ld      c, a
                ld      b, 0
                add     hl, bc
                ld      (ser_ptr), hl
                jr      ser_nt_loop
ser_nt_done:

; -- global flags (appendGlobalFlags, editor_region.go:152-164):
; -- [count u16 LE] then the ids (sorted ascending above) u16 LE each.
                ld      hl, (ser_globals_count)
                call    ser_emit_u16
                ld      hl, (ser_globals_count)
                ld      (ser_count), hl
                ld      hl, (ser_globals_ptr)
                ld      (ser_ptr), hl
ser_gf_loop:
                ld      hl, (ser_count)
                ld      a, h
                or      l
                jr      z, ser_gf_done
                dec     hl
                ld      (ser_count), hl
                ld      hl, (ser_ptr)
                ld      a, (hl)
                call    ser_emit_a
                inc     hl
                ld      a, (hl)
                call    ser_emit_a
                inc     hl
                ld      (ser_ptr), hl
                jr      ser_gf_loop
ser_gf_done:

; -- sidecar (appendSidecar, editor_region.go:201-231): [count u16 LE]
; -- then per row [kind u8][anchor_delta uvarint] and the kind tail:
; -- comment [placement u8][len u16 LE][body]; blank [run_len uvarint].
; -- Rows are emitted in input order under the anchor-monotonicity guard
; -- (see the header note); the Go delta<0 clamp (editor_region.go:213)
; -- therefore never fires — a borrow fails loudly instead.
                ld      hl, (ser_sidecar_count)
                call    ser_emit_u16
                call    ser_prev32_zero
                ld      hl, (ser_sidecar_count)
                ld      (ser_count), hl
                ld      hl, (ser_sidecar_ptr)
                ld      (ser_ptr), hl
ser_sc_loop:
                ld      hl, (ser_count)
                ld      a, h
                or      l
                jr      z, ser_sc_done
                dec     hl
                ld      (ser_count), hl
                ld      hl, (ser_ptr)
                ld      a, (hl)
                inc     hl
                ld      (ser_row_kind), a
                cp      2
                jp      nc, ser_fail_sidecar_kind
                call    ser_emit_a              ; kind (rows always tagged:
                                                ; the writer sets FlagTaggedSidecar)
                ex      de, hl                  ; DE -> anchor u32
                call    ser_delta32             ; uv_buf = anchor - prev; HL -> tail
                jp      c, ser_fail_sidecar_order
                push    hl
                call    ser_emit_uvarint        ; anchor delta
                pop     hl
                ld      a, (ser_row_kind)
                or      a
                jr      z, ser_sc_comment
; blank run (editor_region.go:218-220): run_len uvarint.
                ld      de, ser_uv_buf
                ld      bc, 4
                ldir                            ; uv_buf := run_len; HL -> next row
                ld      (ser_ptr), hl
                call    ser_emit_uvarint
                jr      ser_sc_loop
ser_sc_comment:
; comment (editor_region.go:221-227): [placement u8][len u16 LE][body].
                ld      a, (hl)
                inc     hl
                call    ser_emit_a              ; placement
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                      ; -> body; BC = body len
                ld      a, c
                call    ser_emit_a              ; len lo
                ld      a, b
                call    ser_emit_a              ; len hi
                call    ser_copy                ; body (BC may be 0); HL -> next row
                ld      (ser_ptr), hl
                jr      ser_sc_loop
ser_sc_done:

; -- result: total file length.
                ld      hl, (ser_out_cursor)
                ld      de, (ser_out_base)
                or      a
                sbc     hl, de
                ld      (ser_out_len), hl
                ret

ser_fail_row_order:
                ld      a, SER_TAG_ROW_ORDER
                jp      fail_with_tag
ser_fail_sidecar_order:
                ld      a, SER_TAG_SIDECAR_ORDER
                jp      fail_with_tag
ser_fail_sidecar_kind:
                ld      a, SER_TAG_SIDECAR_KIND
                jp      fail_with_tag

; -----------------------------------------------------------------------
; ser_need — room check: DE bytes must fit between the output cursor and
; ser_out_end. Wrap-safe (the cemit_out_hdr shape, i48c-b8c): room is
; computed as end - cursor, never cursor + need, so a window near
; top-of-memory cannot wrap mod 65536 into a false fit. Preserves all
; registers; fails loudly (&d0) on overflow.
; -----------------------------------------------------------------------
ser_need:
                push    hl
                push    bc
                ld      hl, (ser_out_end)
                ld      bc, (ser_out_cursor)
                or      a
                sbc     hl, bc                  ; HL = room = end - cursor
                jr      c, ser_need_fail        ; cursor past end: state corrupt
                sbc     hl, de                  ; room - need (CF clear from above)
                jr      c, ser_need_fail
                pop     bc
                pop     hl
                ret
ser_need_fail:
                ld      a, SER_TAG_OVERFLOW
                jp      fail_with_tag

; -----------------------------------------------------------------------
; ser_emit_a — append the byte in A to the output stream (room-checked).
; Preserves A, BC, DE, HL.
; -----------------------------------------------------------------------
ser_emit_a:
                push    hl
                push    de
                ld      de, 1
                call    ser_need
                ld      hl, (ser_out_cursor)
                ld      (hl), a
                inc     hl
                ld      (ser_out_cursor), hl
                pop     de
                pop     hl
                ret

; -----------------------------------------------------------------------
; ser_emit_u16 — append HL as u16 LE. Preserves BC, DE, HL.
; -----------------------------------------------------------------------
ser_emit_u16:
                ld      a, l
                call    ser_emit_a
                ld      a, h
                call    ser_emit_a
                ret

; -----------------------------------------------------------------------
; ser_copy — append BC bytes from (HL) to the output stream (BC = 0 is a
; no-op: a bare LDIR with BC=0 copies 65536 bytes). Returns HL advanced
; past the source bytes. Clobbers A, BC, DE.
; -----------------------------------------------------------------------
ser_copy:
                ld      a, b
                or      c
                ret     z
                ld      d, b
                ld      e, c
                call    ser_need
                ld      de, (ser_out_cursor)
                ldir
                ld      (ser_out_cursor), de
                ret

; -----------------------------------------------------------------------
; ser_uv_from_a — ser_uv_buf := A (zero-extended to u32). Preserves BC,
; DE, HL.
; -----------------------------------------------------------------------
ser_uv_from_a:
                ld      (ser_uv_buf + 0), a
                xor     a
                ld      (ser_uv_buf + 1), a
                ld      (ser_uv_buf + 2), a
                ld      (ser_uv_buf + 3), a
                ret

; -----------------------------------------------------------------------
; ser_prev32_zero — reset the delta accumulator (the per-table prev = 0 of
; header_tables.go:46/75 and editor_region.go:207). Preserves BC, DE.
; -----------------------------------------------------------------------
ser_prev32_zero:
                xor     a
                ld      (ser_prev32 + 0), a
                ld      (ser_prev32 + 1), a
                ld      (ser_prev32 + 2), a
                ld      (ser_prev32 + 3), a
                ret

; -----------------------------------------------------------------------
; ser_delta32 — ser_uv_buf := value - ser_prev32 (u32 LE, value at (DE)),
; then ser_prev32 := value. The delta step of header_tables.go:52-55 /
; editor_region.go:211-216.
; Input:  DE -> the 4-byte LE value.
; Output: CF = 1 on borrow (value < prev — unsorted input);
;         HL = DE + 4 (past the value — the caller's walk continues there).
; Clobbers: A, BC, DE.
; -----------------------------------------------------------------------
ser_delta32:
                push    de                      ; value ptr, for the prev update
                ld      hl, ser_prev32
                ld      a, (de)
                sub     (hl)
                ld      (ser_uv_buf + 0), a
                inc     de
                inc     hl
                ld      a, (de)
                sbc     a, (hl)
                ld      (ser_uv_buf + 1), a
                inc     de
                inc     hl
                ld      a, (de)
                sbc     a, (hl)
                ld      (ser_uv_buf + 2), a
                inc     de
                inc     hl
                ld      a, (de)
                sbc     a, (hl)
                ld      (ser_uv_buf + 3), a
                pop     hl                      ; HL = value ptr
                push    af                      ; save the borrow flag
                ld      de, ser_prev32
                ld      bc, 4
                ldir                            ; prev := value; HL = value + 4
                pop     af
                ret

; -----------------------------------------------------------------------
; ser_emit_uvarint — append the u32 at ser_uv_buf as a uvarint
; (encoding/binary PutUvarint: 7-bit groups LSB-first, bit 7 =
; continuation; a u32 emits at most 5 bytes). Destroys ser_uv_buf.
; Clobbers A, B, HL. Preserves C, DE.
; -----------------------------------------------------------------------
ser_emit_uvarint:
ser_uv_loop:
                ld      a, (ser_uv_buf + 1)
                ld      hl, ser_uv_buf + 2
                or      (hl)
                inc     hl
                or      (hl)
                jr      nz, ser_uv_more         ; value >= 256: continuation
                ld      a, (ser_uv_buf)
                cp      &80
                jr      c, ser_uv_last          ; value < 0x80: final byte
ser_uv_more:
                ld      a, (ser_uv_buf)
                or      &80                     ; (v & 0x7F) | 0x80
                call    ser_emit_a
; value >>= 7.
                ld      b, 7
ser_uv_shift:
                ld      hl, ser_uv_buf + 3
                srl     (hl)
                dec     hl
                rr      (hl)
                dec     hl
                rr      (hl)
                dec     hl
                rr      (hl)
                djnz    ser_uv_shift
                jr      ser_uv_loop
ser_uv_last:
                jp      ser_emit_a              ; final byte (bit 7 clear)

; -----------------------------------------------------------------------
; ser_sort_rows — in-place insertion sort of fixed-stride rows in
; REVERSED-BYTE lexicographic order: rows compare byte-wise from the LAST
; byte down to the first. Because every row stores its most-significant
; key field LAST in little-endian bytes, this single comparator realises
; all three Go sort orders:
;   label row [name_id:2 LE][offset:4 LE] -> (offset, name_id) asc
;       (writeLabelTable, header_tables.go:37-42)
;   local row [digit:1][offset:4 LE]      -> (offset, digit) asc
;       (writeLocalTable, header_tables.go:66-71)
;   global id [id:2 LE]                   -> id asc
;       (appendGlobalFlags, editor_region.go:154)
; Insertion sort keeps equal rows in input order (stability is moot:
; equal keys mean identical rows for all three row types).
; Input:  HL = row base, DE = row count, A = row stride (bytes, <= 6).
; Clobbers: everything.
; -----------------------------------------------------------------------
ser_sort_rows:
                ld      (ser_sort_base), hl
                ld      (ser_sort_stride), a
                ex      de, hl                  ; HL = count
                ld      de, 1
                or      a
                sbc     hl, de
                ret     c                       ; count 0: sorted
                ld      a, h
                or      l
                ret     z                       ; count 1: sorted
                ld      (ser_sort_remaining), hl
                ld      a, (ser_sort_stride)
                ld      c, a
                ld      b, 0
                ld      hl, (ser_sort_base)
                add     hl, bc
                ld      (ser_sort_pi), hl       ; row 1
ser_so_outer:
; temp := *pi.
                ld      hl, (ser_sort_pi)
                ld      de, ser_tmp_row
                ld      a, (ser_sort_stride)
                ld      c, a
                ld      b, 0
                ldir
                ld      hl, (ser_sort_pi)
                ld      (ser_sort_pj), hl
ser_so_inner:
                ld      hl, (ser_sort_pj)
                ld      de, (ser_sort_base)
                or      a
                sbc     hl, de
                jr      z, ser_so_place         ; pj at base: insert here
                ld      a, (ser_sort_stride)
                ld      e, a
                ld      d, 0
                ld      hl, (ser_sort_pj)
                or      a
                sbc     hl, de
                ld      (ser_sort_pk), hl       ; pk = pj - stride
                call    ser_row_gt              ; CF=1 iff *pk > temp
                jr      nc, ser_so_place
; *pj := *pk; pj := pk.
                ld      hl, (ser_sort_pk)
                ld      de, (ser_sort_pj)
                ld      a, (ser_sort_stride)
                ld      c, a
                ld      b, 0
                ldir
                ld      hl, (ser_sort_pk)
                ld      (ser_sort_pj), hl
                jr      ser_so_inner
ser_so_place:
; *pj := temp.
                ld      hl, ser_tmp_row
                ld      de, (ser_sort_pj)
                ld      a, (ser_sort_stride)
                ld      c, a
                ld      b, 0
                ldir
; pi += stride; remaining--.
                ld      a, (ser_sort_stride)
                ld      c, a
                ld      b, 0
                ld      hl, (ser_sort_pi)
                add     hl, bc
                ld      (ser_sort_pi), hl
                ld      hl, (ser_sort_remaining)
                dec     hl
                ld      (ser_sort_remaining), hl
                ld      a, h
                or      l
                jr      nz, ser_so_outer
                ret

; -----------------------------------------------------------------------
; ser_row_gt — CF=1 iff the row at (ser_sort_pk) is strictly greater than
; ser_tmp_row in reversed-byte lexicographic order (see ser_sort_rows).
; Clobbers A, BC, DE, HL.
; -----------------------------------------------------------------------
ser_row_gt:
                ld      a, (ser_sort_stride)
                ld      c, a
                dec     a
                ld      e, a
                ld      d, 0                    ; DE = stride - 1
                ld      hl, (ser_sort_pk)
                add     hl, de
                push    hl                      ; pk last byte
                ld      hl, ser_tmp_row
                add     hl, de
                ex      de, hl                  ; DE = temp last byte
                pop     hl                      ; HL = pk last byte
                ld      b, c                    ; B = stride (byte count)
ser_rg_loop:
                ld      a, (de)                 ; temp byte
                cp      (hl)                    ; temp - pk
                jr      c, ser_rg_gt            ; temp < pk: pk > temp
                ret     nz                      ; temp > pk: pk < temp (CF=0)
                dec     hl
                dec     de
                djnz    ser_rg_loop
                or      a                       ; equal: CF=0
                ret
ser_rg_gt:
                scf
                ret

; -----------------------------------------------------------------------
; Parameter cells (the caller writes these before compact_serialize) and
; internal state. All are labels (not equ) so the host harness resolves
; them from the mapfile.
; -----------------------------------------------------------------------
ser_labels_ptr:         defw    0
ser_labels_count:       defw    0
ser_locals_ptr:         defw    0
ser_locals_count:       defw    0
ser_recs_ptr:           defw    0
ser_recs_len:           defw    0
ser_names_ptr:          defw    0
ser_globals_ptr:        defw    0
ser_globals_count:      defw    0
ser_sidecar_ptr:        defw    0
ser_sidecar_count:      defw    0
ser_out_base:           defw    0
ser_out_end:            defw    0
ser_out_len:            defw    0               ; result

ser_out_cursor:         defw    0
ser_prev32:             defs    4               ; delta accumulator (u32 LE)
ser_uv_buf:             defs    4               ; uvarint work value (u32 LE)
ser_tmp_row:            defs    6               ; insertion-sort temp row
ser_count:              defw    0               ; generic loop counter
ser_ptr:                defw    0               ; generic walk cursor
ser_sort_base:          defw    0
ser_sort_stride:        defb    0
ser_sort_remaining:     defw    0
ser_sort_pi:            defw    0
ser_sort_pj:            defw    0
ser_sort_pk:            defw    0
ser_prev_name_ptr:      defw    0
ser_prev_name_len:      defb    0
ser_cur_name_ptr:       defw    0
ser_cur_name_len:       defb    0
ser_shared:             defb    0
ser_suffix_len:         defb    0
ser_row_kind:           defb    0
