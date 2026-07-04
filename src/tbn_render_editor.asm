; tbn_render_editor.asm — editor-region reader for the `.tbn` → source-text
; renderer (i365a slice 2).  Z80 port of the Go authority
; tools/sam-aarch64-format/editor_region.go (readEditorRegion /
; readNameTable / readGlobalFlags) and the `.global` emit loop of
; tools/sam-aarch64/render/emit.go:74-76.
;
; The editor region sits at the tail of a compact `.tbn` v2 file, at the byte
; offset the header carries as editor_region_offset.  reader_init records that
; boundary in IN_END_PAGE / IN_END_OFFSET (the record walk stops there); this
; module opens a read window past it to recover the data the assembler never
; reads:
;
;   name table     [count u16][front-coded entry]…    name_id → string
;   global flags   [count u16][name_id u16]…          which names were .global
;   comment sidecar[count u16]…                        (not consumed this slice)
;
; A name entry is front-coded against the PREVIOUS name (encounter order):
;   [shared uvarint][suffix_len uvarint][suffix bytes]
; decode copies `shared` bytes from the prior name and appends the suffix
; (editor_region.go:125-146).  Reconstructed names are stored null-terminated
; in render_names_buf; render_name_ptrs[name_id] points at each.
;
; The reader's page-safe byte primitive (reader_read_next_byte, on
; reader_in_cursor) is reused: it renormalises across IN page boundaries, so a
; multi-page editor region reads correctly.


; -----------------------------------------------------------------------
; Capacity caps.  The name table + `.global` flags + header label/local tables
; are read ONCE into resident buffers (random-ish access by name_id / PC), so
; they are sized for the full release corpus: 475 names / 6121 name-string bytes
; (+NULs), 282 labels, 172 locals.  The comment/blank-run sidecar is NOT resident
; — it is streamed (one row + one comment body at a time), so only a single body
; buffer is held, sized for the largest release comment (1691 bytes).
; -----------------------------------------------------------------------
RENDER_MAX_NAMES:       equ     512     ; name_id in 0..511 (release: 475 names)
RENDER_NAMES_BUF_SIZE:  equ     7168    ; total decoded name bytes +NULs (release: 6121)
RENDER_CURNAME_SIZE:    equ     128     ; max single name length (release max: 39)
RENDER_MAX_GLOBALS:     equ     64      ; .global entries (release: 1)
RENDER_BODY_BUF_SIZE:   equ     2048    ; a single streamed comment body (release max: 1691)

; SidecarKind tags (editor_region.go:57-60): the leading kind u8 present when
; the header carries FlagTaggedSidecar (bit 0 of the flags word).
SIDECAR_COMMENT:        equ     0
SIDECAR_BLANK:          equ     1


; -----------------------------------------------------------------------
; render_read_editor — parse the editor region into the name lookup and the
; global-flags list.  Mirrors readEditorRegion (editor_region.go:306).
;
; Input:  IN_END_PAGE / IN_END_OFFSET = editor-region start (set by
;         reader_init); render_names_next / render_prevname_len reset here.
; Output: render_name_ptrs[] / render_names_buf populated; render_globals_ids /
;         render_globals_count populated.  LMPR restored to the render window.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_read_editor:
                di
                ; Read the tagged-sidecar flag (header flags u16 at file offset 6;
                ; bit 0 = FlagTaggedSidecar, format.go:30).  The header is in the
                ; first IN page (IN_BASE_LMPR); map it to section A and read &0006.
                ld      a, (IN_BASE_LMPR)
                out     (250), a
                ld      a, (&0006)
                and     1
                ld      (render_sidecar_tagged), a

                ; Position the shared byte cursor at the editor-region start.
                ld      a, (IN_END_PAGE)
                out     (250), a
                ld      hl, (IN_END_OFFSET)
                ld      (reader_in_cursor), hl

                ; Reset name storage: running-prev empty, names buffer at base.
                xor     a
                ld      (render_prevname_len), a
                ld      hl, render_names_buf
                ld      (render_names_next), hl

; ----- name table: [count u16][front-coded entry]… --------------------
                call    render_ed_read_u16          ; HL = name count
                ld      (render_name_count), hl
                ; Overflow guard (i365 #858 YELLOW): render_name_ptrs holds
                ; RENDER_MAX_NAMES entries indexed by name_id (0..count-1); fail
                ; loud rather than overrun when a program supplies more names than
                ; the release-sized cap.  (The loop below reloads render_name_count,
                ; so clobbering HL here is safe.)
                ld      de, RENDER_MAX_NAMES + 1
                or      a
                sbc     hl, de                      ; count - (cap+1)
                jp      nc, fail                    ; count > RENDER_MAX_NAMES

                ld      de, 0                       ; DE = current name_id
render_name_loop:
                ld      hl, (render_name_count)
                ld      a, h
                or      l
                jr      z, render_name_done
                dec     hl
                ld      (render_name_count), hl

                push    de                          ; preserve name_id
                call    render_read_name_entry      ; decodes entry for name_id DE
                pop     de
                inc     de
                jr      render_name_loop
render_name_done:

; ----- global flags: [count u16][name_id u16]… ------------------------
                call    render_ed_read_u16          ; HL = global count
                ld      (render_globals_count), hl

                ld      hl, render_globals_ids
                ld      (render_gp_ptr), hl
render_glob_read_loop:
                ld      hl, (render_globals_count)
                ld      a, h
                or      l
                jr      z, render_glob_read_done
                dec     hl
                ld      (render_globals_count), hl

                call    render_ed_read_u16          ; HL = name_id
                ld      de, (render_gp_ptr)
                ld      a, l
                ld      (de), a
                inc     de
                ld      a, h
                ld      (de), a
                inc     de
                ld      (render_gp_ptr), de
                jr      render_glob_read_loop
render_glob_read_done:
; The count was decremented to zero above; restore it for render_globals.
                ld      hl, (render_gp_ptr)
                ld      de, render_globals_ids
                or      a
                sbc     hl, de                      ; HL = bytes written = count*2
                srl     h
                rr      l                           ; HL = count
                ld      (render_globals_count), hl

; The comment/blank-run sidecar follows the global flags.  Record its stream
; start (row count + cursor) so render_flush_comments can drain it row-by-row in
; PC order, then restore the decode window (section B = paged_call).
                call    render_sc_stream_init
                jp      enctab_map_in


; -----------------------------------------------------------------------
; render_sc_stream_init — record the comment/blank-run sidecar's stream start.
; The sidecar ([count u16][tagged rows]) is NOT parsed up front (the release
; corpus has 7839 rows / 291 KB of bodies — far too large to hold resident); it
; is drained row-by-row in PC order by render_flush_comments, reading each comment
; body in-place from the IN editor region.  This routine reads the row count and
; snapshots the stream cursor (LMPR page + section-A offset) at the first row.
;
; Input:  reader_in_cursor / live LMPR positioned just past the global-flags
;         table; render_sidecar_tagged = FlagTaggedSidecar bit.
; Output: render_sidecar_count / render_sc_remaining = row count; the running
;         delta accumulator + pending flag reset; render_sc_stream_lmpr /
;         render_sc_stream_off = the first row's position.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_sc_stream_init:
                call    render_ed_read_u16          ; HL = sidecar row count
                ld      (render_sidecar_count), hl
                ld      (render_sc_remaining), hl

                ld      hl, 0
                ld      (render_sc_prev_anchor + 0), hl
                ld      (render_sc_prev_anchor + 2), hl
                xor     a
                ld      (render_sc_pending), a       ; no row decoded yet

; Snapshot the stream cursor (LMPR page + section-A offset) at the first row.
                in      a, (250)
                ld      (render_sc_stream_lmpr), a
                ld      hl, (reader_in_cursor)
                ld      (render_sc_stream_off), hl
                ret


; -----------------------------------------------------------------------
; render_sc_stream_decode — decode ONE sidecar row into the pending slot,
; advancing the stream cursor.  Mirrors the per-row body of readSidecar
; (editor_region.go:248-291), but forward-only: anchors are delta-coded
; ascending, so the running 32-bit accumulator (render_sc_prev_anchor) recovers
; each absolute anchor as rows are decoded in order.  A comment's body is copied
; in-place into the single-body buffer render_body_buf (bounded by the largest
; release comment); a blank-run records its run length.  Only ONE row is held at
; a time — render_flush_comments places it (when PC catches its anchor) before
; decoding the next.
;
; Input:  render_sc_remaining > 0; render_sc_stream_lmpr / render_sc_stream_off
;         = this row's position; render_sc_prev_anchor = the prior row's anchor.
; Output: render_sc_pend_* populated, render_sc_pending = 1, render_sc_remaining
;         decremented, the stream cursor advanced past this row; LMPR restored to
;         the decode window (enctab_map_in) so a following decode is valid.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_sc_stream_decode:
                di
                ld      a, (render_sc_stream_lmpr)
                out     (250), a
                ld      hl, (render_sc_stream_off)
                ld      (reader_in_cursor), hl

                ld      hl, (render_sc_remaining)
                dec     hl
                ld      (render_sc_remaining), hl

; kind (tagged) — untagged files (bit clear) are all comments.
                xor     a
                ld      (render_sc_pend_kind), a
                ld      a, (render_sidecar_tagged)
                or      a
                jr      z, rscd_delta
                call    reader_read_next_byte       ; A = kind
                ld      (render_sc_pend_kind), a
rscd_delta:
; anchor += delta (32-bit accumulate); copy into the pending anchor.
                ld      hl, render_sc_prev_anchor
                call    reader_uvarint_add
                ld      hl, render_sc_prev_anchor
                ld      de, render_sc_pend_anchor
                ld      bc, 4
                ldir

                ld      a, (render_sc_pend_kind)
                cp      SIDECAR_BLANK
                jr      z, rscd_blank

; --- comment: placement u8, len u16, body bytes into render_body_buf ---------
                call    reader_read_next_byte       ; A = placement
                ld      (render_sc_pend_place), a
                call    render_ed_read_u16          ; HL = body byte length
                ld      (render_sc_pend_len), hl

; Guard: a body longer than the single-body buffer would overrun it — fail
; loudly rather than corrupt neighbouring state.
                ld      de, RENDER_BODY_BUF_SIZE
                or      a
                sbc     hl, de
                jp      nc, fail                    ; len >= buf size

                ld      hl, (render_sc_pend_len)
                ld      a, h
                or      l
                jr      z, rscd_save                ; empty body
                ld      de, render_body_buf
                ld      (render_sc_body_dst), de
rscd_body_loop:
                push    hl                          ; remaining body length
                call    reader_read_next_byte       ; A = byte (clobbers A, HL)
                ld      hl, (render_sc_body_dst)
                ld      (hl), a
                inc     hl
                ld      (render_sc_body_dst), hl
                pop     hl
                dec     hl
                ld      a, h
                or      l
                jr      nz, rscd_body_loop
                jr      rscd_save

; --- blank run: run_len uvarint ---------------------------------------------
rscd_blank:
                call    render_ed_read_uvarint      ; HL = run length
                ld      (render_sc_pend_len), hl

rscd_save:
                ld      a, 1
                ld      (render_sc_pending), a
; Save the advanced cursor and restore the decode window (section B = paged_call)
; so the record handler's render_decode_literal is valid after the flush.
                in      a, (250)
                ld      (render_sc_stream_lmpr), a
                ld      hl, (reader_in_cursor)
                ld      (render_sc_stream_off), hl
                jp      enctab_map_in


; -----------------------------------------------------------------------
; render_read_name_entry — decode one front-coded name entry into
; render_names_buf and record render_name_ptrs[name_id].  Mirrors the loop
; body of readNameTable (editor_region.go:125-146).
;
; Input:  DE = name_id; render_prevname_len / render_curname hold the previous
;         name (the front-coding base); render_names_next = append cursor.
; Output: render_curname / render_prevname_len updated to this name;
;         render_name_ptrs[name_id] = start of the null-terminated copy;
;         render_names_next advanced past it.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_read_name_entry:
                ld      (render_cur_name_id), de

                call    render_ed_read_uvarint      ; HL = shared prefix len
                ld      a, l
                ld      (render_shared), a
                call    render_ed_read_uvarint      ; HL = suffix len
                ld      a, l
                ld      (render_suffix_len), a

; Read suffix bytes into render_curname starting at offset `shared` (the
; first `shared` bytes are already the prior name's prefix).
                ld      a, (render_shared)
                ld      e, a
                ld      d, 0
                ld      hl, render_curname
                add     hl, de                      ; HL = render_curname + shared
                ld      a, (render_suffix_len)
                or      a
                jr      z, render_rne_copied
                ld      b, a
render_rne_read_loop:
                push    bc
                push    hl
                call    reader_read_next_byte       ; A = suffix byte
                pop     hl
                ld      (hl), a
                inc     hl
                pop     bc
                djnz    render_rne_read_loop
render_rne_copied:
; newlen = shared + suffix_len; becomes the running-prev length.
                ld      a, (render_shared)
                ld      hl, render_suffix_len
                add     a, (hl)
                ld      (render_prevname_len), a
                ld      (render_newlen), a

; render_name_ptrs[name_id] = render_names_next.
                ld      hl, (render_cur_name_id)
                add     hl, hl                      ; name_id * 2
                ld      de, render_name_ptrs
                add     hl, de                      ; HL = &render_name_ptrs[name_id]
                ld      de, (render_names_next)
                ld      (hl), e
                inc     hl
                ld      (hl), d

; Copy render_curname[0..newlen-1] to render_names_next, then a NUL.
                ld      hl, render_curname          ; source
                ld      a, (render_newlen)
                ld      c, a
                ld      b, 0                        ; BC = newlen
                or      a
                jr      z, render_rne_nocopy
                ldir                                ; DE (names_next) advances to end
render_rne_nocopy:
                ex      de, hl                      ; HL = end of copy
                ld      (hl), 0                     ; null-terminate
                inc     hl
                ld      (render_names_next), hl
                ret


; -----------------------------------------------------------------------
; render_globals — emit "  .global NAME\n" for each global name_id.  Mirrors
; emit.go:74-76 (the loop leaves prevWasStatement unchanged, so this does NOT
; touch render_prev_stmt).
;
; Input:  render_globals_ids / render_globals_count; render_name_ptrs resolved.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_globals:
                ld      hl, render_globals_ids
                ld      (render_gp_ptr), hl
render_globals_loop:
                ld      hl, (render_globals_count)
                ld      a, h
                or      l
                ret     z
                dec     hl
                ld      (render_globals_count), hl

                ld      hl, (render_gp_ptr)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (render_gp_ptr), hl         ; DE = name_id

                push    de
                ld      hl, render_str_global
                call    render_copy_cstr            ; "  .global "
                pop     de
                call    render_name_of              ; HL = name ptr
                call    render_copy_cstr            ; append the name
                ld      a, 10                       ; '\n'
                call    render_out_append
                jr      render_globals_loop


; -----------------------------------------------------------------------
; render_name_of — resolve a name_id to its null-terminated string pointer.
;
; Input:  DE = name_id.  Output: HL = string pointer.  Clobbers: A?, DE, HL.
; -----------------------------------------------------------------------
render_name_of:
                ex      de, hl                      ; HL = name_id
                add     hl, hl                      ; name_id * 2
                ld      de, render_name_ptrs
                add     hl, de                      ; HL = &render_name_ptrs[name_id]
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ex      de, hl                      ; HL = string pointer
                ret


; -----------------------------------------------------------------------
; render_ed_read_u16 — read a little-endian u16 from the editor cursor.
; Output: HL = value.  Clobbers: A, HL (BC preserved by reader_read_next_byte).
; -----------------------------------------------------------------------
render_ed_read_u16:
                call    reader_read_next_byte       ; A = LSB
                ld      c, a
                call    reader_read_next_byte       ; A = MSB (C preserved)
                ld      h, a
                ld      l, c
                ret


; -----------------------------------------------------------------------
; render_ed_read_uvarint — decode an unsigned LEB128 varint (little-endian
; 7-bit groups, high bit = continuation).  Result up to 16 bits in HL — the
; editor region's shared/suffix lengths are small.  Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_ed_read_uvarint:
                ld      de, 0                       ; DE = accumulator
                ld      b, 0                        ; B = shift (0,7,14,…)
render_ed_uv_byte:
                push    bc
                call    reader_read_next_byte       ; A = raw byte
                pop     bc
                ld      c, a                        ; C = raw (for continuation bit)
                and     &7F                         ; low 7 data bits
                ld      l, a
                ld      h, 0                        ; HL = group value
                ld      a, b
                or      a
                jr      z, render_ed_uv_merge
render_ed_uv_shift:
                add     hl, hl                      ; HL <<= 1
                dec     a
                jr      nz, render_ed_uv_shift
render_ed_uv_merge:
                ld      a, e
                or      l
                ld      e, a
                ld      a, d
                or      h
                ld      d, a                        ; DE |= (group << shift)
                ld      a, c
                and     &80
                jr      z, render_ed_uv_done
                ld      a, b
                add     a, 7
                ld      b, a
                jr      render_ed_uv_byte
render_ed_uv_done:
                ex      de, hl                      ; HL = decoded value
                ret


; -----------------------------------------------------------------------
; Editor-region storage + scratch (section C/D — HMPR-stable).
; -----------------------------------------------------------------------
render_str_global:      defm    "  .global "
                        defb    0

render_name_count:      defw    0       ; names remaining to read (loop counter)
render_globals_count:   defw    0       ; .global entries
render_gp_ptr:          defw    0       ; walking pointer into render_globals_ids

render_cur_name_id:     defw    0       ; name_id being decoded
render_shared:          defb    0       ; front-coded shared-prefix length
render_suffix_len:      defb    0       ; front-coded suffix length
render_newlen:          defb    0       ; shared + suffix_len
render_prevname_len:    defb    0       ; length of the running-prev name

render_names_next:      defw    0       ; append cursor into render_names_buf

; Comment/blank-run sidecar (editor_region.go) — streamed row-by-row in PC order
; (render_sc_stream_decode / render_flush_comments), never held resident.
render_sidecar_tagged:  defb    0       ; FlagTaggedSidecar bit (kind u8 present)
render_sidecar_count:   defw    0       ; total sidecar rows (diagnostics)
render_sc_remaining:    defw    0       ; rows not yet decoded from the stream
render_sc_prev_anchor:  defb    0, 0, 0, 0      ; running absolute anchor (delta accum)
render_sc_stream_lmpr:  defb    0       ; LMPR page of the stream cursor
render_sc_stream_off:   defw    0       ; section-A offset of the stream cursor
render_sc_body_dst:     defw    0       ; body-copy append cursor (into render_body_buf)
; One decoded row awaiting placement (drained by PC in render_flush_comments).
render_sc_pending:      defb    0       ; 1 = a row is decoded and awaits its PC
render_sc_pend_anchor:  defb    0, 0, 0, 0      ; pending row anchor (u32 LE)
render_sc_pend_kind:    defb    0       ; pending row kind (SIDECAR_COMMENT/BLANK)
render_sc_pend_place:   defb    0       ; pending comment placement (0/1)
render_sc_pend_len:     defw    0       ; comment body length OR blank run length

render_name_ptrs:       defs    RENDER_MAX_NAMES * 2    ; name_id → string ptr
render_globals_ids:     defs    RENDER_MAX_GLOBALS * 2  ; global name_ids (u16 LE)
render_curname:         defs    RENDER_CURNAME_SIZE     ; running front-coding base
render_names_buf:       defs    RENDER_NAMES_BUF_SIZE   ; decoded name strings
render_body_buf:        defs    RENDER_BODY_BUF_SIZE    ; a single streamed comment body
