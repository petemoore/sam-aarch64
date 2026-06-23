; viewport.asm — the read-only viewer's scroll/cursor state machine (item i4a).
;
; SAM-side Z80 port of the host authority:
;   tools/editor-prototype/viewer/view.go
;
; This is the STATE MACHINE half of the viewer: the cursor position (a source
; line index) plus the line/page navigation commands and the centre-locked-
; cursor window computation. Rendering — painting glyphs to the SAM screen,
; loading a font, wrapping long lines, the status line — is the i4 rendering
; brick; this brick computes only WHERE the cursor is and WHICH window of source
; lines is visible.
;
; Centre-locked cursor (view.go §5): the cursor line stays at the screen's
; vertical centre while the document scrolls under it, pinned to line 0 near the
; top and clamped at the document ends. With rows text-rows and centre=rows/2:
;   winTop      = max(0, cursor - centre)        (first visible source line)
;   cursorRow   = cursor - winTop = min(cursor, centre)   (cursor's screen row)
;   visibleCount= min(rows, lineCount - winTop)  (source lines shown)
; This is view.go's non-wrapped (truncate-mode) behaviour, where each source
; line occupies exactly one display row; wrapping (which expands a long line
; into several display rows) is render-coupled and belongs to the render brick.
;
; All positions are 16-bit (release.s is ~9k source lines). rows fits 8 bits
; (a SAM screen is at most ~32 text rows).
;
; PROVENANCE: algorithmic port of viewer.View (cursor, SetCursor, Move, Top,
; Bottom, PageDown/PageUp, the rowsAbove/winTop window math in view.go).
;
; VERIFICATION: tools/netboot-oracle/z80/viewport_test.go drives these routines
; under koron-go/z80, replays random nav commands, and asserts the cursor,
; winTop, cursorRow, and visibleCount match a Go oracle mirroring view.go on
; every step.

                ; org only when assembled standalone (host harness uses
                ; -D VP_STANDALONE=1); an including file supplies its own org.
                if defined(VP_STANDALONE)
                org     &8000
                endif

; ===========================================================================
; vp_init — initialise the viewport for a document.
;
; Entry: BC = line count, A = text rows (screen rows available for text).
; Effect: cursor = 0.
; Clobbers: HL.
; ===========================================================================
vp_init:
                ld      (VP_LINECOUNT), bc
                ld      (VP_ROWS), a
                ld      hl, 0
                ld      (VP_CURSOR), hl
                ret

; ===========================================================================
; vp_get_cursor — return the current cursor source-line index in HL.
; ===========================================================================
vp_get_cursor:
                ld      hl, (VP_CURSOR)
                ret

; ===========================================================================
; vp_set_cursor — set the cursor to BC, clamped to [0, lineCount-1].
; Mirrors view.go SetCursor. Clobbers: A, D, E, HL.
; ===========================================================================
vp_set_cursor:
                ld      h, b
                ld      l, c
                jr      vp_store_clamped

; ===========================================================================
; vp_line_down — move the cursor down one source line (clamped). Move(+1).
; vp_line_up   — move the cursor up one source line (floored at 0). Move(-1).
; Clobbers: A, D, E, HL.
; ===========================================================================
vp_line_down:
                ld      hl, (VP_CURSOR)
                inc     hl
                jr      vp_store_clamped

vp_line_up:
                ld      hl, (VP_CURSOR)
                ld      a, h
                or      l
                ret     z               ; already at 0
                dec     hl
                ld      (VP_CURSOR), hl
                ret

; ===========================================================================
; vp_page_down — move the cursor down one screenful (rows source lines, clamped).
; vp_page_up   — move the cursor up one screenful (floored at 0).
; Mirrors view.go PageDown/PageUp (move by the text-row count). Clobbers: A,D,E,HL.
; ===========================================================================
vp_page_down:
                ld      hl, (VP_CURSOR)
                ld      a, (VP_ROWS)
                ld      e, a
                ld      d, 0
                add     hl, de          ; cursor + rows (no 16-bit overflow: < ~9k+32)
                jr      vp_store_clamped

vp_page_up:
                ld      hl, (VP_CURSOR)
                ld      a, (VP_ROWS)
                ld      e, a
                ld      d, 0
                call    vp_sub_floor    ; HL = max(0, cursor - rows)
                ld      (VP_CURSOR), hl
                ret

; ===========================================================================
; vp_top    — jump the cursor to the first line (0).
; vp_bottom — jump the cursor to the last line (lineCount-1, or 0 if empty).
; Clobbers: A, HL.
; ===========================================================================
vp_top:
                ld      hl, 0
                ld      (VP_CURSOR), hl
                ret

vp_bottom:
                ld      hl, (VP_LINECOUNT)
                ld      a, h
                or      l
                jr      z, vp_bottom_store      ; empty document -> 0
                dec     hl
vp_bottom_store:
                ld      (VP_CURSOR), hl
                ret

; ===========================================================================
; vp_win_top — return winTop = max(0, cursor - rows/2) in HL.
; The first visible source line under the centre-locked cursor. Clobbers: A,D,E.
; ===========================================================================
vp_win_top:
                ld      a, (VP_ROWS)
                srl     a               ; A = centre = rows/2
                ld      e, a
                ld      d, 0
                ld      hl, (VP_CURSOR)
                call    vp_sub_floor    ; HL = max(0, cursor - centre)
                ret

; ===========================================================================
; vp_cursor_row — return the cursor's screen row = cursor - winTop in A.
; Equivalent to min(cursor, rows/2); always < rows. Clobbers: A, D, E, HL.
; ===========================================================================
vp_cursor_row:
                call    vp_win_top      ; HL = winTop
                ex      de, hl          ; DE = winTop
                ld      hl, (VP_CURSOR)
                ld      a, l
                sub     e
                ld      l, a
                ld      a, h
                sbc     a, d
                ld      h, a            ; HL = cursor - winTop
                ld      a, l            ; fits 8 bits (< rows)
                ret

; ===========================================================================
; vp_visible_count — return min(rows, lineCount - winTop) in A: the number of
; source lines actually shown (fewer than rows near the document's end).
; Clobbers: A, D, E, HL.
; ===========================================================================
vp_visible_count:
                call    vp_win_top      ; HL = winTop
                ex      de, hl          ; DE = winTop
                ld      hl, (VP_LINECOUNT)
                ld      a, l
                sub     e
                ld      l, a
                ld      a, h
                sbc     a, d
                ld      h, a            ; HL = lineCount - winTop (>= 0)
                ; min(HL, rows): if HL >= 256 it exceeds rows -> rows.
                ld      a, h
                or      a
                jr      nz, vp_vis_rows
                ld      a, (VP_ROWS)
                cp      l               ; rows - L: carry if rows < L
                jr      c, vp_vis_rows  ; L > rows -> rows
                ld      a, l            ; L <= rows -> L
                ret
vp_vis_rows:
                ld      a, (VP_ROWS)
                ret

; ===========================================================================
; vp_store_clamped — clamp HL to [0, lineCount-1] and store it as the cursor.
; HL is assumed already >= 0 (unsigned). Clobbers: A, D, E, HL.
; ===========================================================================
vp_store_clamped:
                ld      de, (VP_LINECOUNT)
                ld      a, d
                or      e
                jr      z, vp_store_zero        ; empty document -> cursor 0
                dec     de                      ; DE = max = lineCount-1
                ; if HL > DE then HL = DE   (16-bit unsigned compare)
                ld      a, h
                cp      d
                jr      c, vp_store_have        ; H < D -> HL < max
                jr      nz, vp_store_setmax     ; H > D -> HL > max
                ld      a, l
                cp      e
                jr      c, vp_store_have        ; L < E -> HL < max
                jr      z, vp_store_have        ; HL == max
vp_store_setmax:
                ex      de, hl                  ; HL = max
vp_store_have:
                ld      (VP_CURSOR), hl
                ret
vp_store_zero:
                ld      hl, 0
                ld      (VP_CURSOR), hl
                ret

; ===========================================================================
; vp_sub_floor — HL = max(0, HL - DE) (16-bit, floored at 0). Internal helper.
; Clobbers: A.
; ===========================================================================
vp_sub_floor:
                ld      a, l
                sub     e
                ld      l, a
                ld      a, h
                sbc     a, d
                ld      h, a            ; HL = HL - DE (mod 65536)
                ret     nc              ; no borrow -> result >= 0
                ld      hl, 0           ; underflow -> 0
                ret

; ===========================================================================
; Resident state (section D on the SAM; standalone harness places it after the
; code).
; ===========================================================================
VP_CURSOR:      defs 2                  ; current source line index (16-bit)
VP_LINECOUNT:   defs 2                  ; total source lines (16-bit)
VP_ROWS:        defs 1                  ; text rows available on screen
