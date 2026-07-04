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
; Capacity caps (this slice's fixtures are tiny; these bound the buffers).
; -----------------------------------------------------------------------
RENDER_MAX_NAMES:       equ     128     ; name_id in 0..127
RENDER_NAMES_BUF_SIZE:  equ     512     ; total decoded name bytes (+NULs)
RENDER_CURNAME_SIZE:    equ     128     ; max single name length
RENDER_MAX_GLOBALS:     equ     64      ; .global entries


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

; The comment sidecar follows but is not consumed this slice (S2 fixtures
; carry none).  Restore the render window (section A = IN page, B = paged_call).
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

render_name_ptrs:       defs    RENDER_MAX_NAMES * 2    ; name_id → string ptr
render_globals_ids:     defs    RENDER_MAX_GLOBALS * 2  ; global name_ids (u16 LE)
render_curname:         defs    RENDER_CURNAME_SIZE     ; running front-coding base
render_names_buf:       defs    RENDER_NAMES_BUF_SIZE   ; decoded name strings
