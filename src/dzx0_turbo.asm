; dzx0_turbo.asm — pyz80 port of the ZX0 "Turbo" decoder (126 bytes).
;
; Ported from reference/zx0/z80/dzx0_turbo.asm (upstream commit
; ecde3a2ae05061fe06469ed46df81a33b7de7d86).
;
; PRODUCT copy (i68): this is the shipping decoder chosen by the
; comment-storage design (docs/specs/comment-storage-design.md §6 —
; turbo is the knee of the size/speed curve: 126 B, ~51 T/byte on the
; greedy corpus streams).  It assembles into the page-13 zx0 payload
; (src/zx0_payload.asm) at ZX0_DECODE_ENTRY = &8B00 (design §5) and is
; invoked under HMPR=13 via paged_call.  The harness keeps its own
; org-0 copies of all four decoder variants in
; tools/z80-test-harness-go/testdata/ for variant benchmarks; only
; turbo is product code.
;
; Syntax-only changes from upstream:
;   - Hex literals: $xx  -> &xx  (pyz80 uses & prefix, not $)
;   - org &8B00 (the page-13 section-C address the payload loads to)
;   - No other changes; all mnemonics, labels, and logic are identical.
;
; Parameters (call convention, same as upstream):
;   HL: source address (compressed data)
;   DE: destination address (decompressing)
;
; Note: the turbo decoder uses self-modifying code — it writes the last
; offset directly into the LD HL,0 immediate field (dzx0t_last_offset+1).
; The two bytes at that address are patched at runtime to store the current
; back-reference offset.  The initial value of 0 matches the upstream source.
; Consequences: must run from RAM (page 13 is RAM) and is not reentrant.

        org     &8B00

dzx0_turbo:
        ld      bc, &ffff               ; preserve default offset 1
        ld      (dzx0t_last_offset+1), bc
        inc     bc
        ld      a, &80
        jr      dzx0t_literals
dzx0t_new_offset:
        ld      c, &fe                  ; prepare negative offset
        add     a, a
        jp      nz, dzx0t_new_offset_skip
        ld      a, (hl)                 ; load another group of 8 bits
        inc     hl
        rla
dzx0t_new_offset_skip:
        call    nc, dzx0t_elias         ; obtain offset MSB
        inc     c
        ret     z                       ; check end marker
        ld      b, c
        ld      c, (hl)                 ; obtain offset LSB
        inc     hl
        rr      b                       ; last offset bit becomes first length bit
        rr      c
        ld      (dzx0t_last_offset+1), bc ; preserve new offset
        ld      bc, 1                   ; obtain length
        call    nc, dzx0t_elias
        inc     bc
dzx0t_copy:
        push    hl                      ; preserve source
dzx0t_last_offset:
        ld      hl, 0                   ; restore offset (self-modifying)
        add     hl, de                  ; calculate destination - offset
        ldir                            ; copy from offset
        pop     hl                      ; restore source
        add     a, a                    ; copy from literals or new offset?
        jr      c, dzx0t_new_offset
dzx0t_literals:
        inc     c                       ; obtain length
        add     a, a
        jp      nz, dzx0t_literals_skip
        ld      a, (hl)                 ; load another group of 8 bits
        inc     hl
        rla
dzx0t_literals_skip:
        call    nc, dzx0t_elias
        ldir                            ; copy literals
        add     a, a                    ; copy from last offset or new offset?
        jr      c, dzx0t_new_offset
        inc     c                       ; obtain length
        add     a, a
        jp      nz, dzx0t_last_offset_skip
        ld      a, (hl)                 ; load another group of 8 bits
        inc     hl
        rla
dzx0t_last_offset_skip:
        call    nc, dzx0t_elias
        jp      dzx0t_copy
dzx0t_elias:
        add     a, a                    ; interlaced Elias gamma coding
        rl      c
        add     a, a
        jr      nc, dzx0t_elias
        ret     nz
        ld      a, (hl)                 ; load another group of 8 bits
        inc     hl
        rla
        ret     c
        add     a, a
        rl      c
        add     a, a
        ret     c
        add     a, a
        rl      c
        add     a, a
        ret     c
        add     a, a
        rl      c
        add     a, a
        ret     c
dzx0t_elias_loop:
        add     a, a
        rl      c
        rl      b
        add     a, a
        jr      nc, dzx0t_elias_loop
        ret     nz
        ld      a, (hl)                 ; load another group of 8 bits
        inc     hl
        rla
        jr      nc, dzx0t_elias_loop
        ret
