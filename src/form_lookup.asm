; form_lookup.asm — mnemonic index walk + form lookup.
;
; Z80 port of tools/aarch64enc/data.go::FormsForMnemonic (the linear
; mnemonic-index scan) and tools/aarch64enc/validate.go::matchOperandKinds
; (linear over candidate forms until expected_kind tuple matches).
;
; The mnemonic index lives at the tail of enctab.enc per the M2 spec
; §2 layout.  enctab.enc body in memory:
;
;   ENCTAB_BASE + 0    "ENC1"               4 bytes
;   ENCTAB_BASE + 4    version u16          2 bytes
;   ENCTAB_BASE + 6    flags   u16          2 bytes
;   ENCTAB_BASE + 8    form_count u32       4 bytes
;   ENCTAB_BASE + 12   forms[count] ...
;     Each form: 2+1+4+4 = 11 byte header + slots*4 bytes
;   <after forms>     index_count u32      4 bytes
;     Each entry: 2+4+2 = 8 bytes
;
; The form section is variable-length, so we cache the first-form
; pointer + form_count at load time and also the mnemonic-index pointer
; (encoded by the loader and exposed here via globals).
;
; We RE-COMPUTE those pointers each call to keep loader.asm minimal and
; avoid coupling with a separate "post-load" registration step.  Cost:
; a linear pass over the form table for each instruction.  For M3's
; small fixture corpus this is fine; M4 can optimise.


; -----------------------------------------------------------------------
; form_lookup_init — compute FORMS_BASE and INDEX_BASE pointers by
; walking the form table.
;
; Input:  none (uses ENCTAB_BASE + the count fields stored there).
; Output: form_first_ptr  = pointer to first form (ENCTAB_BASE + 12)
;         form_count      = u16 number of forms (we cap at 16-bit;
;                           form_count u32 in file fits comfortably).
;         index_first_ptr = pointer to first mnemonic-index entry
;                           (after the form table).
;         index_count     = u16 number of mnemonic-index entries.
;
; Walks the form table to find the index section.  Each form header
; is 11 bytes + (operand_count * 4 bytes of slots).  We need to skip
; over every form to find where the index starts.
;
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
form_lookup_init:
; -- form_count is at ENCTAB_BASE + 8 (4-byte LE) ------------------------
                ld      hl, ENCTAB_BASE + 8
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ld      (form_count), de
; (We ignore the top 2 bytes of the form_count u32 — it is bounded by
;  the table emitter to fit in u16 for our subset.)

; -- form_first_ptr = ENCTAB_BASE + 12 ----------------------------------
                ld      hl, ENCTAB_BASE + 12
                ld      (form_first_ptr), hl

; -- Walk all forms, computing the byte offset to the index section -----
; HL = current form pointer; DE = remaining form count.
                ld      de, (form_count)
form_lookup_init_skip_loop:
                ld      a, d
                or      e
                jr      z, form_lookup_init_index

; Read operand_count at offset 2 of current form header (1 byte).
                push    hl
                inc     hl
                inc     hl
                ld      a, (hl)             ; A = operand_count
                pop     hl
; Form size = 11 + operand_count * 4.
                push    de
                ld      e, a
                ld      d, 0
                add     hl, de              ; +1 * operand_count
                add     hl, de              ; +2 *
                add     hl, de              ; +3 *
                add     hl, de              ; +4 *
                ld      de, 11
                add     hl, de              ; +11 header
                pop     de
                dec     de
                jp      form_lookup_init_skip_loop

form_lookup_init_index:
; HL now points to the index_count u32.
                ld      (index_first_ptr_minus4), hl   ; remember start of u32

                push    hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ld      (index_count), de

                pop     hl
                ld      bc, 4
                add     hl, bc
                ld      (index_first_ptr), hl

                ret


; -----------------------------------------------------------------------
; form_lookup_find_first — locate the first form for a mnemonic_id.
;
; Input:  DE = mnemonic_id (u16)
; Output: On hit:
;           HL = pointer to first form header for that mnemonic
;           BC = form_count for that mnemonic
;           Z flag set (jr z = match)
;         On miss:
;           Z flag clear (caller must jp fail or handle).
;
; Walks the mnemonic-index linearly (it's small — <200 entries).
; Each entry: [mnemonic_id u16][first_form_id u32][form_count u16] = 8 bytes.
;
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
form_lookup_find_first:
                ld      (form_lookup_mn), de        ; save target id
                ld      bc, (index_count)
                ld      hl, (index_first_ptr)
form_lookup_find_loop:
                ld      a, b
                or      c
                jr      z, form_lookup_find_miss

; Compare HL[0..1] vs target.
                push    hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = entry's mnemonic_id
                ld      hl, (form_lookup_mn)
                or      a                   ; CY=0
                sbc     hl, de
                pop     hl                  ; HL = entry pointer
                jr      nz, form_lookup_find_next

; Match.  Read first_form_id (u32 at HL+2) and form_count (u16 at HL+6).
                push    hl
                ld      bc, 2
                add     hl, bc
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = first_form_id low 16
; We ignore the high 16 (form_id u32 fits in u16 for our subset).
                pop     hl
                push    de                  ; save first_form_id
                ld      bc, 6
                add     hl, bc              ; HL → form_count u16
                ld      c, (hl)
                inc     hl
                ld      b, (hl)             ; BC = form_count
                pop     de                  ; DE = first_form_id

; Convert first_form_id (DE) to a pointer in the form table.
                push    bc
                call    form_lookup_compute_ptr     ; HL = ptr to that form
                pop     bc
                xor     a                   ; set Z=1 (match)
                ret

form_lookup_find_next:
                ld      de, 8
                add     hl, de
                dec     bc
                jp      form_lookup_find_loop

form_lookup_find_miss:
                ld      a, 1
                or      a                   ; Z=0
                ret


; -----------------------------------------------------------------------
; form_lookup_compute_ptr — given a form_id (DE), compute pointer to
; that form's header by walking the form table from the start.
;
; Input:  DE = form_id (zero-based index into form table)
; Output: HL = pointer to the form header bytes
; Clobbers: A, BC, DE, HL.
;
; Required because forms are variable-length; we cannot just
; index_base + form_id*K.
; -----------------------------------------------------------------------
form_lookup_compute_ptr:
                ld      hl, (form_first_ptr)
form_lookup_compute_ptr_loop:
                ld      a, d
                or      e
                ret     z

                push    de
; Read operand_count at offset 2.
                inc     hl
                inc     hl
                ld      a, (hl)
                dec     hl
                dec     hl                  ; HL back to header start

                ld      e, a
                ld      d, 0
                push    hl
                ld      hl, 0
                add     hl, de
                add     hl, de
                add     hl, de
                add     hl, de              ; HL = operand_count * 4
                ld      de, 11
                add     hl, de              ; HL = operand_count*4 + 11
                ex      de, hl              ; DE = form size
                pop     hl                  ; HL = form ptr
                add     hl, de              ; HL = next form ptr

                pop     de
                dec     de
                jp      form_lookup_compute_ptr_loop


; -----------------------------------------------------------------------
; form_lookup_advance — given a form header pointer in HL, advance HL
; to the next form header (HL += 11 + operand_count*4).
;
; Input:  HL = current form header pointer
; Output: HL = next form header pointer
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
form_lookup_advance:
                push    hl
                inc     hl
                inc     hl
                ld      a, (hl)             ; A = operand_count
                pop     hl

                push    af
                ld      de, 11
                add     hl, de              ; +11 header
                pop     af
                or      a
                ret     z

                ld      b, a
form_lookup_advance_slots:
                ld      de, 4
                add     hl, de
                djnz    form_lookup_advance_slots
                ret


; -----------------------------------------------------------------------
; form_lookup_match — given a mnemonic's first form pointer + count and
; the parsed operand kinds, find the first form whose ExpectedKind tuple
; matches kinds[].
;
; Input:
;   HL = pointer to first form header (the form_lookup_find_first hit).
;   BC = form_count for that mnemonic.
;   DE = pointer to a small array of parsed operand-kind bytes.
;   A  = number of operand kinds in that array.
;
; Output (on match):
;   HL = pointer to the matched form header.
;   Z flag set.
; Output (no match):
;   Z flag clear.
;
; Implementation: for each candidate form, compare operand_count
; (form_header[2]) to A; if equal, walk the slot records and compare
; each slot's expected_kind byte (slot[1]) to kinds[i].  First full
; match wins.
;
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
form_lookup_match:
                ld      (form_lookup_kinds_ptr), de
                ld      (form_lookup_kinds_n), a

form_lookup_match_outer:
                ld      a, b
                or      c
                jr      z, form_lookup_match_miss

; Compare candidate's operand_count (HL+2) with our kinds_n.
                push    hl
                push    bc
                inc     hl
                inc     hl
                ld      a, (hl)             ; A = candidate's operand_count
                dec     hl
                dec     hl                  ; HL back to start

                ld      b, a
                ld      a, (form_lookup_kinds_n)
                cp      b
                jr      nz, form_lookup_match_next

; Operand-count matches.  Walk slots and kinds in lockstep.
                ld      a, (form_lookup_kinds_n)
                or      a
                jr      z, form_lookup_match_hit_pop   ; 0 operands → match

                push    hl                  ; save form ptr again
                ld      de, 11
                add     hl, de              ; HL → first slot record
                ld      de, (form_lookup_kinds_ptr)

                ld      b, a                ; B = remaining slots
form_lookup_match_slot_loop:
                push    hl
                inc     hl                  ; HL → slot[1] (expected_kind)
                ld      a, (hl)             ; A = slot's expected_kind
                pop     hl
                ex      de, hl              ; HL = kinds_ptr
                cp      (hl)
                ex      de, hl              ; restore HL = slot, DE = kinds
                jr      nz, form_lookup_match_slot_fail

                ld      a, 4
                add     a, l
                ld      l, a
                jr      nc, form_lookup_match_no_carry_slot
                inc     h
form_lookup_match_no_carry_slot:
                inc     de
                djnz    form_lookup_match_slot_loop

                pop     hl                  ; discard slot-walker form ptr (3)
                jr      form_lookup_match_hit_pop

form_lookup_match_slot_fail:
                pop     hl                  ; discard slot-walker form ptr (3)
form_lookup_match_next:
                pop     bc                  ; restore form_count
                pop     hl                  ; restore current form ptr
                call    form_lookup_advance
                dec     bc
                jp      form_lookup_match_outer

form_lookup_match_hit_pop:
; Stack still has the outer (BC = form_count) and (HL = form ptr).
                pop     bc                  ; discard saved form_count
                pop     hl                  ; HL = matched form ptr
                xor     a                   ; Z=1
                ret

form_lookup_match_miss:
                ld      a, 1
                or      a                   ; Z=0
                ret


; -----------------------------------------------------------------------
; Scratch / globals computed at startup by form_lookup_init.
; -----------------------------------------------------------------------
form_first_ptr:         defw    0           ; pointer to first form header
form_count:             defw    0           ; total form count (u16; high bytes ignored)
index_first_ptr:        defw    0           ; pointer to first index entry
index_first_ptr_minus4: defw    0           ; pointer to index_count u32 (for debug)
index_count:            defw    0           ; total index entries

form_lookup_mn:         defw    0           ; mnemonic_id being looked up
form_lookup_kinds_ptr:  defw    0           ; pointer to parsed-kinds array
form_lookup_kinds_n:    defb    0           ; number of parsed kinds
