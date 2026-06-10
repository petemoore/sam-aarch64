; dzx0_standard.asm — pyz80 port of the ZX0 "Standard" decoder (68 bytes).
;
; Ported from reference/zx0/z80/dzx0_standard.asm (upstream commit
; ecde3a2ae05061fe06469ed46df81a33b7de7d86).
;
; Syntax-only changes from upstream:
;   - Hex literals: $xx  -> &xx  (pyz80 uses & prefix, not $)
;   - No other changes; all mnemonics, labels, and logic are identical.
;
; Parameters (call convention, same as upstream):
;   HL: source address (compressed data)
;   DE: destination address (decompressing)
;
; Assemble standalone for measurement:
;   pyz80 --obj=dzx0_standard.bin dzx0_standard.asm

        org     0

dzx0_standard:
        ld      bc, &ffff               ; preserve default offset 1
        push    bc
        inc     bc
        ld      a, &80
dzx0s_literals:
        call    dzx0s_elias             ; obtain length
        ldir                            ; copy literals
        add     a, a                    ; copy from last offset or new offset?
        jr      c, dzx0s_new_offset
        call    dzx0s_elias             ; obtain length
dzx0s_copy:
        ex      (sp), hl                ; preserve source, restore offset
        push    hl                      ; preserve offset
        add     hl, de                  ; calculate destination - offset
        ldir                            ; copy from offset
        pop     hl                      ; restore offset
        ex      (sp), hl                ; preserve offset, restore source
        add     a, a                    ; copy from literals or new offset?
        jr      nc, dzx0s_literals
dzx0s_new_offset:
        pop     bc                      ; discard last offset
        ld      c, &fe                  ; prepare negative offset
        call    dzx0s_elias_loop        ; obtain offset MSB
        inc     c
        ret     z                       ; check end marker
        ld      b, c
        ld      c, (hl)                 ; obtain offset LSB
        inc     hl
        rr      b                       ; last offset bit becomes first length bit
        rr      c
        push    bc                      ; preserve new offset
        ld      bc, 1                   ; obtain length
        call    nc, dzx0s_elias_backtrack
        inc     bc
        jr      dzx0s_copy
dzx0s_elias:
        inc     c                       ; interlaced Elias gamma coding
dzx0s_elias_loop:
        add     a, a
        jr      nz, dzx0s_elias_skip
        ld      a, (hl)                 ; load another group of 8 bits
        inc     hl
        rla
dzx0s_elias_skip:
        ret     c
dzx0s_elias_backtrack:
        add     a, a
        rl      c
        rl      b
        jr      dzx0s_elias_loop
