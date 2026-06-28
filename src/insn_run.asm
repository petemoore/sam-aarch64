; insn_run.asm — KindInsnRun (0x09) record handler (compact `.tbn` v2, M8 / i39a).
;
; The v2 instruction overlay unifies literal and symbol/PC-bearing
; instructions into one record kind.  Each INSN_RUN record carries a mode
; byte then a sequence of elements:
;
;   mode 0  "all literal":  payload = packed 4-byte words (== the old
;           LIT_INSTS floor).  Handled by main_handle_lit_insts (memcpy).
;   mode 1  "overlayed":    payload = elements, each
;             [base_word u32 LE][patch_count u8]
;             patch_count × [slot u8][expr_len u8][expr bytes]
;           where base_word is the assembled word with the relocated
;           field(s) zeroed, and each patch carries the expression for one
;           field plus a FoldSlot id naming the fold-rule.
;
; Pass 2 copies the base word, evaluates each patch expression
; (eval_expr_const — already handles PUSH_SYM/PUSH_PC/PUSH_LOCAL/REL_*),
; applies the slot's fold-rule (the same PC-conversion / field-encode the
; literal encoder performs — ported from tools/aarch64enc/overlay.go::Fold),
; ORs the bits into the base word, and emits the 4 bytes.  Pass 1 advances
; PASS_PC by 4 per element and registers a litpool slot for any
; FoldLitpool19 patch (keyed by PASS_PC, exactly as the symbolic path did).
;
; The fold-rules reuse the proven slot encoders where they take a value
; (branch/adr/adrp/imm12/imm16/logical); the simple field-place rules
; (mem imm12/imm9, pair imm7, movk imm16) are computed inline.  Faithful
; port of the Go authority — CLAUDE.md "Go-first ports are mechanical".


; FoldSlot ids — wire contract with tools/aarch64enc/overlay.go.
FSID_BRANCH26:          equ     1
FSID_BRANCH19:          equ     2
FSID_BRANCH14:          equ     3
FSID_ADR:               equ     4
FSID_ADRP:              equ     5
FSID_ADDSUB_IMM12:      equ     6
FSID_MEM_IMM12:         equ     7
FSID_MEM_IMM9:          equ     8
FSID_MOVK_IMM16:        equ     9
FSID_LOGICAL:           equ     10
FSID_PAIR_IMM7:         equ     11
FSID_LITPOOL19:         equ     12
FSID_MOVZ_AUTO:         equ     13


; INSN_RUN_FOLD_ONLY: when defined, skip the production pass-1/2 handlers
; (they depend on walk_records / emit_byte etc. that a fold-only consumer
; such as the b8d paged chain driver does not provide).  insn_fold and the
; fold_* helpers are always included; the FSID_* constants above too.
if defined(INSN_RUN_FOLD_ONLY)==0

; -----------------------------------------------------------------------
; main_handle_insn_run — INSN_RUN (0x09) dispatch.
;
; Input:  HL = payload ptr (STAGING_BUF), BC = payload_len, A = kind.
; Output: jp walk_records (via the pass handlers).
; -----------------------------------------------------------------------
main_handle_insn_run:
                ld      a, (hl)             ; mode byte
                or      a
                jp      z, main_handle_lit_insts    ; mode 0: literal memcpy
                                                    ; (mode byte == the
                                                    ; 1-byte tag it skips)
; mode 1: skip the mode byte; HL → first element, BC = element bytes.
                inc     hl
                dec     bc
                ld      a, (PASS_MODE)
                cp      PASS_PASS1
                jp      z, insn_run_mode1_pass1
                ; fall through into pass 2


; -----------------------------------------------------------------------
; Pass 2 — fold each element and emit it.
; -----------------------------------------------------------------------
insn_run_mode1_pass2:
                ld      (insn_cursor), hl
                ld      (insn_remaining), bc

insn_p2_elem_loop:
                ld      bc, (insn_remaining)
                ld      a, b
                or      c
                jp      z, walk_records             ; all elements done

; -- Read base_word (4 bytes) into the accumulator ---------------------
                ld      hl, (insn_cursor)
                ld      de, insn_base
                ld      bc, 4
                ldir                                ; insn_base = base_word
; -- patch_count -------------------------------------------------------
                ld      a, (hl)
                ld      (insn_patch_count), a
                inc     hl
                ld      (insn_cursor), hl
; remaining -= 5 (base_word + patch_count)
                ld      hl, (insn_remaining)
                ld      de, 5
                or      a
                sbc     hl, de
                ld      (insn_remaining), hl

insn_p2_patch_loop:
                ld      a, (insn_patch_count)
                or      a
                jr      z, insn_p2_elem_emit
                dec     a
                ld      (insn_patch_count), a

; -- Read slot + expr_len; HL → expr bytes -----------------------------
                ld      hl, (insn_cursor)
                ld      a, (hl)
                ld      (insn_slot), a
                inc     hl
                ld      c, (hl)
                ld      b, 0
                ld      (insn_expr_len), bc
                inc     hl                          ; HL → expr bytes
; Advance the cursor + remaining past this patch (slot+len+expr).
                push    hl                          ; expr ptr
                ld      de, (insn_expr_len)
                add     hl, de                      ; HL past expr
                ld      (insn_cursor), hl
                ld      hl, (insn_remaining)
                inc     de
                inc     de                          ; DE = expr_len + 2
                or      a
                sbc     hl, de
                ld      (insn_remaining), hl
                pop     hl                          ; HL = expr ptr

; -- Evaluate the patch expression -> expr_result ----------------------
                ld      bc, (insn_expr_len)
                call    eval_expr_const

; -- Fold -> DEHL bits, OR into the base word --------------------------
                ld      a, (insn_slot)
                call    insn_fold
                call    or_dehl_into_base
                jr      insn_p2_patch_loop

insn_p2_elem_emit:
                ld      a, (insn_base + 0)
                call    emit_byte
                ld      a, (insn_base + 1)
                call    emit_byte
                ld      a, (insn_base + 2)
                call    emit_byte
                ld      a, (insn_base + 3)
                call    emit_byte
                call    pass_pc_advance_4
                jp      insn_p2_elem_loop


; -----------------------------------------------------------------------
; Pass 1 — advance PASS_PC by 4 per element; register litpool slots.
; -----------------------------------------------------------------------
insn_run_mode1_pass1:
                ld      (insn_cursor), hl
                ld      (insn_remaining), bc

insn_p1_elem_loop:
                ld      bc, (insn_remaining)
                ld      a, b
                or      c
                jp      z, walk_records

; -- Read base_word (needed for the litpool width) ---------------------
                ld      hl, (insn_cursor)
                ld      de, insn_base
                ld      bc, 4
                ldir
                ld      a, (hl)
                ld      (insn_patch_count), a
                inc     hl
                ld      (insn_cursor), hl
                ld      hl, (insn_remaining)
                ld      de, 5
                or      a
                sbc     hl, de
                ld      (insn_remaining), hl

insn_p1_patch_loop:
                ld      a, (insn_patch_count)
                or      a
                jr      z, insn_p1_elem_done
                dec     a
                ld      (insn_patch_count), a

                ld      hl, (insn_cursor)
                ld      a, (hl)
                ld      (insn_slot), a
                inc     hl
                ld      c, (hl)
                ld      b, 0
                ld      (insn_expr_len), bc
                inc     hl                          ; HL → expr bytes
                ld      (insn_expr_ptr), hl
; Advance cursor + remaining past the patch.
                ld      de, (insn_expr_len)
                add     hl, de
                ld      (insn_cursor), hl
                ld      hl, (insn_remaining)
                inc     de
                inc     de
                or      a
                sbc     hl, de
                ld      (insn_remaining), hl

; -- Register a litpool slot for a FoldLitpool19 patch -----------------
                ld      a, (insn_slot)
                cp      FSID_LITPOOL19
                jr      nz, insn_p1_patch_loop
; width = (base_word bit 30) ? 8 : 4   (ldr-literal X vs W).
                ld      a, (insn_base + 3)
                and     &40
                ld      a, 4
                jr      z, insn_p1_lp_width
                ld      a, 8
insn_p1_lp_width:
                ld      hl, (insn_expr_ptr)
                ld      bc, (insn_expr_len)
                call    litpool_register            ; keyed by PASS_PC
                jr      insn_p1_patch_loop

insn_p1_elem_done:
                call    pass_pc_advance_4
                jp      insn_p1_elem_loop

endif   ; defined(INSN_RUN_FOLD_ONLY)==0

; -----------------------------------------------------------------------
; insn_fold — apply a FoldSlot rule.
;
; Input:  A = FoldSlot id; expr_result = the evaluated value (8-byte LE);
;         insn_base = the base word (consulted for size/sf/hw); PASS_PC =
;         the instruction's PC.
; Output: DEHL = the field bits (L=bit0..7, H=8..15, E=16..23, D=24..31).
; -----------------------------------------------------------------------
insn_fold:
                cp      FSID_BRANCH26
                jp      z, fold_branch26
                cp      FSID_BRANCH19
                jp      z, fold_branch19
                cp      FSID_BRANCH14
                jp      z, fold_branch14
                cp      FSID_ADR
                jp      z, fold_adr
                cp      FSID_ADRP
                jp      z, fold_adrp
                cp      FSID_ADDSUB_IMM12
                jp      z, fold_addsub_imm12
                cp      FSID_MEM_IMM12
                jp      z, fold_mem_imm12
                cp      FSID_MEM_IMM9
                jp      z, fold_mem_imm9
                cp      FSID_MOVK_IMM16
                jp      z, fold_movk_imm16
                cp      FSID_LOGICAL
                jp      z, fold_logical
                cp      FSID_PAIR_IMM7
                jp      z, fold_pair_imm7
                cp      FSID_LITPOOL19
                jp      z, fold_litpool19
                cp      FSID_MOVZ_AUTO
                jp      z, fold_movz_auto
                jp      fail                        ; unknown slot

; ---- Branch/adr/adrp: absolute target in BCDE; encoder subtracts PC ----
fold_branch26:
                call    load_expr_bcde
                ld      hl, insn_slot_branch26
                jp      encode_branch_imm
fold_branch19:
                call    load_expr_bcde
                ld      hl, insn_slot_branch19
                jp      encode_branch_imm
fold_branch14:
                call    load_expr_bcde
                ld      hl, insn_slot_branch14
                jp      encode_branch_imm
fold_adr:
                call    load_expr_bcde
                ld      hl, insn_slot_adr
                jp      encode_adr_imm
fold_adrp:
                call    load_expr_bcde
                ld      hl, insn_slot_adrp
                jp      encode_adrp_imm

; ---- add/sub #imm12 (sh@22): value used directly -----------------------
fold_addsub_imm12:
                call    load_expr_bcde
                ld      hl, insn_slot_imm12
                jp      encode_imm12_shifted

; ---- orr/and/eor/bic #bitmask: N:immr:imms @10 -------------------------
fold_logical:
                ld      a, (insn_base + 3)
                add     a, a                        ; bit 31 (sf) -> carry
                ld      c, 0
                jr      nc, fold_logical_call
                ld      c, 1
fold_logical_call:
                ld      de, expr_result
                ld      hl, insn_slot_logical
                jp      encode_logical_imm

; ---- movk #imm16 (explicit): imm16 @5; hw stays in the base word -------
fold_movk_imm16:
; FoldMovkImm16: value packed as (hw<<16)|imm16 by encodeImm16Shifted.
; Returns DEHL = (hw<<21)|(imm16<<5) — the combined fold delta.
; The ENCTAB base word carries hw=0 (mask 0xffc00000 / 0xff800000 does not
; fix bits 22:21), so hw must be supplied by this fold.
; The caller (enc_field_done / insn_p2_patch_loop) applies or_dehl_into_base;
; this fold must NOT do so — just return the delta.
; Authority: overlay.go:94-99; encode.go:40-66.
                ld      hl, (expr_result)           ; HL = imm16 (bits 15:0)
                ld      a, 5
                call    field_place                 ; DEHL = imm16 << 5
                push    de                          ; save high bits of delta
                push    hl                          ; save low bits of delta
                ld      a, (expr_result + 2)        ; bits 23:16 of value
                and     3                           ; hw = bits 17:16
                ld      l, a
                ld      h, 0
                ld      a, 21
                call    field_place                 ; DEHL = hw << 21
; Combine: DEHL |= saved (imm16 << 5) from the stack.
                pop     bc                          ; BC = saved HL (C=L, B=H)
                ld      a, l
                or      c
                ld      l, a
                ld      a, h
                or      b
                ld      h, a
                pop     bc                          ; BC = saved DE (C=E, B=D)
                ld      a, e
                or      c
                ld      e, a
                ld      a, d
                or      b
                ld      d, a
                ret                                 ; return combined delta in DEHL

; ---- mov-imm auto-movz: compute hw, imm16 @5 + hw @21 ------------------
fold_movz_auto:
                call    movz_hw_chunk               ; A = hw, DE = chunk
                ld      hl, insn_slot_movz
                jp      encode_imm16_shifted

; ---- ldr/str scaled imm12: byteOff/scale @10 --------------------------
fold_mem_imm12:
                call    check_expr_hi48_zero        ; small non-negative
; scale shift s = (base_word bits 31:30).
                ld      a, (insn_base + 3)
                rlca
                rlca
                and     3
                ld      c, a                        ; C = s
                ld      hl, (expr_result)           ; HL = byte offset (<2^16)
                or      a
                jr      z, fold_mem_imm12_placed    ; s == 0: no scale
; modulo check: the low s bits must be zero (multiple of scale).
                ld      a, 1
                ld      b, c
fold_mem_imm12_mask:
                add     a, a
                djnz    fold_mem_imm12_mask
                dec     a                           ; A = (1<<s) - 1
                and     l
                jp      nz, fail                    ; not a multiple of scale
                ld      b, c
fold_mem_imm12_shr:
                srl     h
                rr      l
                djnz    fold_mem_imm12_shr
fold_mem_imm12_placed:
                ld      a, h
                and     &F0
                jp      nz, fail                    ; imm12 >= 4096
                ld      a, 10
                jp      field_place

; ---- stur/ldur/pre/post imm9 (signed): @12 ----------------------------
; Mirrors overlay.go FoldMemImm9 (overlay.go:88-93): the signed byte
; offset must be in [-256, 255]; otherwise fail rather than silently
; mask to 9 bits.
;
; The range is checked over the low 32 bits of expr_result interpreted as
; a signed i32.  Bytes 4..7 are NOT consulted: this fold needs only the low
; 32 bits, which suffice and match GNU for every offset that fits i32.  (A
; deferred fold of an absolute symbol — e.g. `.set OFF, -256` then
; `stur x0, [x1, #OFF]` — carries the value with its high 32 bits
; reconstructed by sign-extending bit 31 (eval_push_sym_sign_high), but the
; imm9 fold reads only the low 32.)
;   value in [-256,255]  ⟺  byte1 ∈ {0x00 (0..255), 0xFF (-256..-1)} and
;   bytes 2,3 equal byte1 (so the i32 is a clean sign-extension of the
;   imm9, rejecting e.g. 0x0000FF00 = 65280).
fold_mem_imm9:
                ld      hl, (expr_result + 2)       ; H = byte3, L = byte2
                ld      a, (expr_result + 1)        ; byte1
                cp      l
                jr      nz, fold_mem_imm9_oor
                cp      h
                jr      nz, fold_mem_imm9_oor
                or      a
                jr      z, fold_mem_imm9_ok         ; byte1==byte2==byte3==0x00 → 0..255
                inc     a
                jr      z, fold_mem_imm9_ok         ; all == 0xFF → -256..-1
fold_mem_imm9_oor:
                ld      a, &b1
                jp      fail_with_tag               ; tag b1: stur/ldur/pre/post imm9 out of [-256,255]
fold_mem_imm9_ok:
                ld      hl, (expr_result)           ; low 16 bits
                ld      a, h
                and     &01                         ; HL &= 0x01FF (signed 9)
                ld      h, a
                ld      a, 12
                jp      field_place

; ---- ldp/stp imm7 (signed scaled): @15 --------------------------------
; Mirrors overlay.go FoldPairImm7 (overlay.go:127-141): the signed byte
; offset must be a multiple of the scale (4 for W-pair, 8 for X-pair) and
; the scaled offset must be in [-64, 63]; otherwise fail.  As for imm9 the
; range is checked over the low 32 bits as a signed i32 (bytes 4..7 are an
; absolute symbol's unreliable zero-high), which matches GNU for every
; pair offset that fits i32.
fold_pair_imm7:
; First require the byte offset to be a clean i32 sign-extension of its
; low 16 bits (bytes 2,3 == sign of bit 15): the scaled-offset range is
; +-64*8 = +-512, so any in-range value fits i16, and rejecting wider
; values here keeps the arithmetic-shift range check below exact.
                ld      hl, (expr_result + 2)       ; H = byte3, L = byte2
                ld      a, (expr_result + 1)        ; byte1
                add     a, a                        ; bit 15 of low word -> carry
                sbc     a, a                        ; A = sign byte S (0x00 / 0xFF)
                cp      l
                jr      nz, fold_pair_imm7_b2_fail
                cp      h
                jr      nz, fold_pair_imm7_b2_fail
                ld      hl, (expr_result)           ; HL = signed i16 byte offset
                ld      a, (insn_base + 3)
                add     a, a                        ; bit 31 -> carry (X-pair if set)
                ld      b, 2                        ; W-pair: scale 4 (>>2)
                jr      nc, fold_pair_imm7_have_scale
                ld      b, 3                        ; X-pair: scale 8 (>>3)
fold_pair_imm7_have_scale:
; Multiple-of-scale check: the low B bits of the value must be zero.
                ld      a, 1
                ld      c, b
fold_pair_imm7_mask:
                add     a, a
                dec     c
                jr      nz, fold_pair_imm7_mask
                dec     a                           ; A = (1<<scale_bits) - 1
                and     l
                jr      z, fold_pair_imm7_shr
                ld      a, &b2
                jp      fail_with_tag               ; tag b2: ldp/stp byte offset not a multiple of scale, or scaled out of [-64,63]
fold_pair_imm7_shr:
                sra     h
                rr      l                           ; arithmetic /scale
                djnz    fold_pair_imm7_shr
; Range check: the scaled signed value (HL) must be in [-64, 63].  After
; an arithmetic shift the sign-extension is preserved, so HL high byte is
; 0x00 (0..63) or 0xFF (-64..-1); any other high byte is out of range.
                ld      a, h
                or      a
                jr      nz, fold_pair_imm7_neg
                ld      a, l
                cp      64
                jr      c, fold_pair_imm7_place     ; scaled in [0,63]
                ld      a, &b2
                jp      fail_with_tag               ; tag b2: scaled offset >= 64
fold_pair_imm7_neg:
                inc     a
                jr      nz, fold_pair_imm7_b2_fail  ; high byte != 0xFF → out of range
                ld      a, l
                cp      &c0
                jr      nc, fold_pair_imm7_place    ; scaled in [-64,-1] (0xC0 = -64)
fold_pair_imm7_b2_fail:
                ld      a, &b2
                jp      fail_with_tag               ; tag b2: scaled offset < -64
fold_pair_imm7_place:
                ld      a, l
                and     &7F                         ; imm7
                ld      l, a
                ld      h, 0
                ld      a, 15
                jp      field_place

; ---- ldr =expr: imm19 = (poolPC - pc)/4 @5 ----------------------------
fold_litpool19:
                call    litpool_lookup              ; PASS_PC -> HL = slot
                ld      de, 9
                add     hl, de                      ; HL -> entry_pc (u32 LE)
                ld      a, (hl)
                ld      e, a
                inc     hl
                ld      a, (hl)
                ld      d, a
                inc     hl
                ld      a, (hl)
                ld      c, a
                inc     hl
                ld      a, (hl)
                ld      b, a                        ; BCDE = entry_pc
                ld      hl, insn_slot_branch19      ; imm19 @5, /4 — like b.cc
                jp      encode_branch_imm


; -----------------------------------------------------------------------
; Helpers.
; -----------------------------------------------------------------------

; load_expr_bcde — pack expr_result's low 32 bits into BCDE big-endian
; (B = byte 3, C = byte 2, D = byte 1, E = byte 0), the convention the
; branch/adr/adrp/imm12 encoders expect.
load_expr_bcde:
                ld      a, (expr_result + 0)
                ld      e, a
                ld      a, (expr_result + 1)
                ld      d, a
                ld      a, (expr_result + 2)
                ld      c, a
                ld      a, (expr_result + 3)
                ld      b, a
                ret

; field_place — DEHL = (HL & 0xFFFF) << A, a 32-bit left shift.  Used by
; the inline field-place folds (mem imm12/imm9, movk imm16, pair imm7).
field_place:
                ld      d, 0
                ld      e, 0
                or      a
                ret     z
                ld      b, a
field_place_loop:
                sla     l
                rl      h
                rl      e
                rl      d
                djnz    field_place_loop
                ret

; or_dehl_into_base — insn_base |= DEHL (L=byte0, H=byte1, E=byte2, D=byte3).
or_dehl_into_base:
                push    de
                push    hl
                pop     bc                          ; BC = saved HL (B=H, C=L)
                ld      a, c
                ld      hl, insn_base + 0
                or      (hl)
                ld      (hl), a
                ld      a, b
                ld      hl, insn_base + 1
                or      (hl)
                ld      (hl), a
                pop     bc                          ; BC = saved DE (B=D, C=E)
                ld      a, c
                ld      hl, insn_base + 2
                or      (hl)
                ld      (hl), a
                ld      a, b
                ld      hl, insn_base + 3
                or      (hl)
                ld      (hl), a
                ret

; check_expr_hi48_zero — fail unless expr_result[2..7] are all zero (the
; byte offset is a small non-negative value).
check_expr_hi48_zero:
                ld      a, (expr_result + 2)
                ld      hl, expr_result + 3
                ld      b, 5
check_expr_hi48_loop:
                or      (hl)
                inc     hl
                djnz    check_expr_hi48_loop
                or      a
                ret     z
                jp      fail

; movz_hw_chunk — find the single non-zero 16-bit chunk of expr_result
; (W: 2 chunks; X: 4 chunks, sf from base bit 31).  Mirrors
; tryEncodeMovImm (pass2.go) / Fold FoldMovzAuto: the lowest chunk whose
; value, shifted into place, reproduces the whole value.
; Output: A = hw (0..3), DE = chunk (E=lo, D=hi).  Fails if two chunks are
; non-zero (not movz-encodable).
movz_hw_chunk:
                ld      a, (insn_base + 3)
                add     a, a                        ; bit 31 -> carry (X if set)
                ld      a, 2                        ; W: 2 chunks
                jr      nc, movz_have_n
                ld      a, 4                        ; X: 4 chunks
movz_have_n:
                ld      (movz_nchunks), a
                xor     a
                ld      (movz_count), a
                ld      (movz_hw), a
                ld      hl, expr_result
                ld      b, 0                        ; B = chunk index
movz_scan_loop:
                ld      a, (movz_nchunks)
                cp      b
                jr      z, movz_scan_done
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                          ; DE = chunk, HL -> next
                ld      a, e
                or      d
                jr      z, movz_scan_next           ; zero chunk
                ld      a, (movz_count)
                inc     a
                ld      (movz_count), a
                ld      a, b
                ld      (movz_hw), a
                ld      (movz_chunk), de
movz_scan_next:
                inc     b
                jr      movz_scan_loop
movz_scan_done:
                ld      a, (movz_count)
                cp      2
                jp      nc, fail                    ; >1 non-zero chunk
                or      a
                jr      nz, movz_one
                ld      de, 0                       ; value 0 -> movz #0, hw 0
                xor     a
                ret
movz_one:
                ld      de, (movz_chunk)
                ld      a, (movz_hw)
                ret


; -----------------------------------------------------------------------
; Scratch / static slot records.
;
; The reused slot encoders read only bit_position (offset +2) and, for
; branch, bit_width (+3); adr/adrp hard-code their immlo:immhi split and
; ignore the record.  +0/+1 are unread, kept 0 for clarity.
; -----------------------------------------------------------------------
insn_slot_branch26:     defb    0, 0, 0, 26
insn_slot_branch19:     defb    0, 0, 5, 19
insn_slot_branch14:     defb    0, 0, 5, 14
insn_slot_imm12:        defb    0, 0, 10, 12
insn_slot_movz:         defb    0, 0, 5, 16
insn_slot_logical:      defb    0, 0, 10, 13
insn_slot_adr:          defb    0, 0, 0, 0
insn_slot_adrp:         defb    0, 0, 0, 0

insn_cursor:            defw    0
insn_remaining:         defw    0
insn_patch_count:       defb    0
insn_slot:              defb    0
insn_expr_ptr:          defw    0
insn_expr_len:          defw    0
insn_base:              defb    0, 0, 0, 0
movz_nchunks:           defb    0
movz_count:             defb    0
movz_hw:                defb    0
movz_chunk:             defw    0
