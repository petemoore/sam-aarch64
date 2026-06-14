; intercepts.asm — encode_ror_imm_word (a BUILD_TESTS-only ROR-imm encoder).
;
; The lone survivor of the retired symbolic-intercept encoder path. In
; production the v2 instruction overlay (insn_run.asm) pre-folds every
; instruction, so the per-mnemonic intercept dispatch and its encoders had no
; production caller; they were removed in the i73-L9 / q20 dead-code sweep.
; encode_ror_imm_word remains only because the boot self-test test_ror_imm.asm
; exercises the word computation directly (Layer-1 unit test, no OUT_PC), so
; this whole file is included only under BUILD_TESTS (see assembler.asm).
; Production reaches ROR through the folded overlay, not through here.
;
; Mac-side reference: tools/refenc/pass2.go:1460-1492 —
;   ror Rd, Rs, #shift  ==  EXTR Rd, Rs, Rs, #shift   (ROR alias)
;     sf   := operand0 is X
;     base := sf ? 0x93c00000 : 0x13800000
;     op   := base | (Rs<<16) | (imm6<<10) | (Rs<<5) | Rd
;     range: 0 <= shift < 32 (W) / < 64 (X).

; -----------------------------------------------------------------------
; encode_ror_imm_word — pure word computation for ROR-imm.
;
; Reads OPVAL_ARRAY (operand layout: kind/reg per the M3 parser).
; Output: DE:HL = encoded 32-bit word (HL = bits 0..15, DE = bits 16..31).
; Errors: jp fail on width mismatch / shift overflow / non-u8 shift.
; -----------------------------------------------------------------------
encode_ror_imm_word:
; -- Width from operand 0 kind (X = 0x01, W = 0x02).
                ld      a, (OPVAL_ARRAY + 0)
                cp      OP_KIND_REG_X
                jp      z, encode_ror_imm_x64
                cp      OP_KIND_REG_W
                jp      nz, fail
; 32-bit ROR: base = 0x13800000.  bits 16..31 = 0x1380.
                ld      hl, &0000
                ld      de, &1380
                ld      a, 32               ; max shift bound
                jp      encode_ror_imm_pack

encode_ror_imm_x64:
; 64-bit ROR: base = 0x93c00000.  bits 16..31 = 0x93c0.
                ld      hl, &0000
                ld      de, &93c0
                ld      a, 64

encode_ror_imm_pack:
; A = regsize bound (32 or 64).  HL:DE = base packed (HL=low16, DE=high16).
                ld      c, a
; -- Read shift's low byte from OPVAL_ARRAY[2] at +2 (start of LE result).
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2)
                cp      c
                jp      nc, fail            ; shift >= regsize → out of range
                ld      b, a                ; B = shift (imm6)
; Reject any high byte non-zero — the shift must be a small u8.
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 3)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 4)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 5)
                or      a
                jp      nz, fail

; -- Read Rd and Rs.
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                ld      c, a                ; C = Rd (5 bits)
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                and     &1f
                ld      (encode_ror_imm_rs), a

; -- OR Rd into bits 4:0 (low 5 of HL low byte).
                ld      a, l
                or      c
                ld      l, a

; -- OR Rs into bits 9:5.  Rs (5 bits) << 5 = 0..0x3e0.  Low 3 bits of
;    Rs land in HL low byte bits 5..7; high 2 bits of Rs land in HL
;    high byte bits 0..1.
                ld      a, (encode_ror_imm_rs)
                rrca
                rrca
                rrca                        ; A bits 0..1 = Rs>>3; A bits 5..7 = (Rs&7)
                ld      c, a
                and     &e0                 ; A = (Rs&7) << 5 → bits 5..7
                or      l
                ld      l, a
                ld      a, c
                and     &03                 ; A = Rs >> 3 → bits 8..9 (HL high byte bits 0..1)
                or      h
                ld      h, a

; -- OR imm6 (B, 0..63) into bits 15:10.  imm6 occupies bits 10..15
;    of the word i.e. HL high byte bits 2..7 = imm6 << 2.
                ld      a, b
                add     a, a
                add     a, a                ; A = imm6 << 2 (fits in u8 since imm6 < 64)
                or      h
                ld      h, a

; -- OR Rs into bits 20:16 (DE low byte bits 0..4).
                ld      a, (encode_ror_imm_rs)
                or      e
                ld      e, a
                ret


encode_ror_imm_rs:               defb    0
