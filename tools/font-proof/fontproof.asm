; fontproof.asm — i76 P1b font-proof probe (editor-tui-prototype-design.md §5).
;
; Renders sample editor content (a window of tests/release/release.s, code +
; long comments) on a real SAM MODE 3 screen, so the 6-px-font geometry can
; be judged from an actual SAM frame rather than host-side arithmetic:
;
;   CALL 32768 (&8000)  85x32 with the vendored five-pixel-font 6x6 glyphs
;   CALL 32771 (&8003)  64x24 with the SAM ROM 8x8 charset — the
;                       known-readable reference, same source window
;
; Both screens are MODE 3 (512x192, 2bpp): 85 cells x 6 px = 510 px;
; 64 cells x 8 px = 512 px (Tech Manual p15: MODE 3 "when used with a
; character set 6 pixels wide, will give 85 characters per line").
;
; Mechanism: the probe runs from section C (&8000, the i62test boot recipe:
; CLEAR 32767 / LOAD CODE 32768 / CALL). It renders into pages
; SCREEN_PAGE/SCREEN_PAGE+1 by mapping them at sections A+B via LMPR with
; ROM0 disabled (interrupts stay off; the stack moves to section C first),
; then restores LMPR and points VMPR at the pair with the MODE 3 bits.
; MODE 3 pixel format (TM p20): 2 bits per pixel, most significant bit pair
; = leftmost pixel; the 2-bit value addresses CLUT entries 0-3 (the high two
; CLUT address bits come from HMPR bits 5-6, which BASIC leaves at 0 for the
; pages this probe occupies). Glyphs are drawn with pixel value 3 (CLUT 3 =
; bright white) on value 0 (CLUT 0 = black).
;
; After rendering, the screen is held for ~11 s — long enough for the
; Xvfb + `import -window root` capture route (docs/notes/headless-simcoupe.md)
; — then DI; HALT for SimCoupé's -exitonhalt (and the local -pngonhalt
; capture patch, tools/font-proof/simcoupe-pngonhalt.patch).

SCREEN_PAGE:    equ 8           ; pages 8+9: free mid-memory pair (BASIC sits
                                ; in 0-3 with this tiny program; SAMDOS + the
                                ; boot screen live in the top pages)
LMPR_PORT:      equ 250
HMPR_PORT:      equ 251
VMPR_PORT:      equ 252
BORDER_PORT:    equ 254
CLUT_PORT:      equ 248
LMPR_ROM0_OFF:  equ &20         ; LMPR bit 5: ROM0 disabled, RAM in section A
VMPR_MODE3:     equ &40         ; VMPR bits 6-5 = 10 (MDE1/MDE0 for MODE 3)

CHARS:          equ &5C36       ; sysvar: 256 below the ROM charset (TM p43:
                                ; base &5190 = CHR$ 32, 8 bytes/char)

COLS6:          equ 85
ROWS6:          equ 32
COLS8:          equ 64
ROWS8:          equ 24

                org &8000

                jp      main6           ; &8000: 6x6 screen, 85x32
                jp      main8           ; &8003: 8x8 reference, 64x24

; --------------------------------------------------------------------
; Entry points.
; --------------------------------------------------------------------
main6:          call    setup
                call    render6
                jp      show

main8:          call    setup
                call    render8
                jp      show

; setup — interrupts off, private stack in section C, ROM charset copied out
; of page 0 (it would be unreachable once LMPR points sections A+B at the
; screen pages), palette + border set, screen pages mapped and cleared.
setup:          pop     hl              ; return address (the stack moves)
                di
                ld      sp, &BFF0
                push    hl
                ; ROM charset: chars 32..127, 8 bytes each, at (CHARS)+256.
                ld      hl, (CHARS)
                ld      de, 256
                add     hl, de
                ld      de, charset_buf
                ld      bc, 96 * 8
                ldir
                ; CLUT 0..2 = black, CLUT 3 = bright white (pixel values
                ; used: 0 = paper, 3 = pen). CLUT register index rides the
                ; high address byte (B) of the OUT, per the TM p19 OTDR
                ; example.
                ld      c, CLUT_PORT
                ld      b, 0
                xor     a
                out     (c), a
                ld      b, 1
                out     (c), a
                ld      b, 2
                out     (c), a
                ld      b, 3
                ld      a, 127          ; bright white
                out     (c), a
                ; border black, screen enabled (port 254 bit 7 = SOFF)
                xor     a
                out     (BORDER_PORT), a
                ; map the screen pair at sections A+B and wipe all 24 KB
                in      a, (LMPR_PORT)
                ld      (saved_lmpr), a
                ld      a, SCREEN_PAGE + LMPR_ROM0_OFF
                out     (LMPR_PORT), a
                ld      hl, 0
                ld      de, 1
                ld      bc, 24575
                ld      (hl), l
                ldir
                ret

; show — restore LMPR, display the rendered pair in MODE 3, hold ~11 s for
; the window-capture route, then exit via DI; HALT.
show:           ld      a, (saved_lmpr)
                out     (LMPR_PORT), a
                ld      a, VMPR_MODE3 + SCREEN_PAGE
                out     (VMPR_PORT), a
                ld      b, 40           ; 40 x ~0.28 s = ~11 s
sh_outer:       ld      hl, 0
sh_inner:       dec     hl              ; 26 T per iteration x 65536
                ld      a, h
                or      l
                jr      nz, sh_inner
                djnz    sh_outer
                di                      ; load-bearing for -exitonhalt
                halt

; --------------------------------------------------------------------
; render6 — 85x32 cells of 6x6 glyphs from the vendored font.
;
; Cell-to-cell stride is 6 px = 12 bits = 1.5 bytes, so a cell starts at
; byte (col*6)/4 with sub-byte phase (col*6)&3; the phase advances by 2
; (mod 4, carrying a byte) per column. Pixels are OR-plotted with a
; 2-bit mask walked right by two bits at a time across byte boundaries.
; --------------------------------------------------------------------
render6:        ld      ix, text6
                ld      hl, 0           ; screen offset of the cell row
                ld      a, ROWS6
                ld      (text_rows), a
r6_row:         ld      (row_base), hl
                ld      (cell_addr), hl
                xor     a
                ld      (cell_phase), a
                ld      b, COLS6
r6_col:         push    bc
                ld      a, (ix+0)
                inc     ix
                sub     32              ; glyph index; control bytes -> space
                jr      nc, r6_ok
                xor     a
r6_ok:          ld      l, a            ; glyph ptr = font6 + index*6
                ld      h, 0
                ld      e, l
                ld      d, h
                add     hl, hl
                add     hl, hl
                add     hl, de
                add     hl, de
                ld      de, font6
                add     hl, de
                ld      (glyph_ptr), hl
                call    draw_glyph6
                ld      a, (cell_phase) ; advance one cell: +1 byte +2 phase
                add     a, 2
                ld      hl, (cell_addr)
                inc     hl
                cp      4
                jr      c, r6_adv
                sub     4
                inc     hl
r6_adv:         ld      (cell_phase), a
                ld      (cell_addr), hl
                pop     bc
                djnz    r6_col
                ld      hl, (row_base)  ; next cell row: 6 scanlines down
                ld      de, 6 * 128
                add     hl, de
                ld      a, (text_rows)
                dec     a
                ld      (text_rows), a
                jr      nz, r6_row
                ret

; draw_glyph6 — blit the 6-row glyph at (glyph_ptr) to (cell_addr) with
; sub-byte phase (cell_phase). Font rows carry the 6 pixels in bits 7..2.
draw_glyph6:    ld      a, 6
                ld      (rows_left), a
                ld      hl, (cell_addr)
                ld      (row_addr), hl
dg6_row:        ld      hl, (glyph_ptr)
                ld      d, (hl)         ; D = glyph row bits (b7..b2)
                inc     hl
                ld      (glyph_ptr), hl
                ld      a, (cell_phase) ; E = start mask for this phase
                ld      hl, mask_tab
                add     a, l
                ld      l, a
                ld      a, 0
                adc     a, h
                ld      h, a
                ld      e, (hl)
                ld      hl, (row_addr)
                ld      b, 6
dg6_px:         sla     d
                jr      nc, dg6_sk
                ld      a, e            ; set pixel: OR both mask bits
                or      (hl)
                ld      (hl), a
dg6_sk:         srl     e               ; mask right by one pixel (2 bits)
                srl     e
                jr      nz, dg6_sm
                ld      e, &C0          ; crossed a byte boundary
                inc     hl
dg6_sm:         djnz    dg6_px
                ld      hl, (row_addr)  ; next scanline
                ld      de, 128
                add     hl, de
                ld      (row_addr), hl
                ld      a, (rows_left)
                dec     a
                ld      (rows_left), a
                jr      nz, dg6_row
                ret

mask_tab:       defb    &C0, &30, &0C, &03

; --------------------------------------------------------------------
; render8 — 64x24 cells of the ROM 8x8 charset: each glyph row byte
; expands to exactly two screen bytes via the 4-bit -> 2bpp table (the
; ROM's own EXTAB technique, TM sysvars EXTAB/M3EXTAB).
; --------------------------------------------------------------------
render8:        ld      ix, text8
                ld      hl, 0
                ld      a, ROWS8
                ld      (text_rows), a
r8_row:         ld      (row_base), hl
                ld      (cell_addr), hl
                ld      b, COLS8
r8_col:         push    bc
                ld      a, (ix+0)
                inc     ix
                sub     32
                jr      nc, r8_ok
                xor     a
r8_ok:          ld      l, a            ; glyph ptr = charset_buf + index*8
                ld      h, 0
                add     hl, hl
                add     hl, hl
                add     hl, hl
                ld      de, charset_buf
                add     hl, de
                call    draw_glyph8
                ld      hl, (cell_addr) ; advance one cell: 8 px = 2 bytes
                inc     hl
                inc     hl
                ld      (cell_addr), hl
                pop     bc
                djnz    r8_col
                ld      hl, (row_base)  ; next cell row: 8 scanlines down
                ld      de, 8 * 128
                add     hl, de
                ld      a, (text_rows)
                dec     a
                ld      (text_rows), a
                jr      nz, r8_row
                ret

; draw_glyph8 — blit the 8-row glyph at HL to (cell_addr).
draw_glyph8:    ld      a, 8
                ld      (rows_left), a
                ld      de, (cell_addr)
                ld      (row_addr), de
dg8_row:        ld      a, (hl)
                inc     hl
                push    hl
                ld      c, a            ; C = glyph row byte
                rrca                    ; high nibble -> low
                rrca
                rrca
                rrca
                and     &0F
                call    expand4
                ld      hl, (row_addr)
                ld      (hl), a         ; left 4 pixels
                inc     hl
                ld      a, c
                and     &0F
                call    expand4
                ld      (hl), a         ; right 4 pixels
                ld      hl, (row_addr)  ; next scanline
                ld      de, 128
                add     hl, de
                ld      (row_addr), hl
                pop     hl
                ld      a, (rows_left)
                dec     a
                ld      (rows_left), a
                jr      nz, dg8_row
                ret

; expand4 — A = 4 font bits (b3 = leftmost) -> A = 2bpp byte, pen value 3.
expand4:        push    hl
                push    de
                ld      hl, expand_tab
                ld      e, a
                ld      d, 0
                add     hl, de
                ld      a, (hl)
                pop     de
                pop     hl
                ret

expand_tab:     defb    &00, &03, &0C, &0F, &30, &33, &3C, &3F
                defb    &C0, &C3, &CC, &CF, &F0, &F3, &FC, &FF

; --------------------------------------------------------------------
; State + data.
; --------------------------------------------------------------------
saved_lmpr:     defb    0
cell_addr:      defw    0
cell_phase:     defb    0
glyph_ptr:      defw    0
row_base:       defw    0
row_addr:       defw    0
rows_left:      defb    0
text_rows:      defb    0

charset_buf:    defs    96 * 8          ; ROM charset copy (chars 32..127)

font6:          MDAT    "../../build/font6.bin"     ; 96 glyphs x 6 row bytes
text6:          MDAT    "../../build/text6.bin"     ; 85x32 screen bytes
text8:          MDAT    "../../build/text8.bin"     ; 64x24 screen bytes
