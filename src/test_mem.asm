; test_mem.asm — Layer-1 self-tests for the OpMem encoder.
;
; Mac-side reference: tools/refenc/pass2.go:721-988 (encodeMemInst +
; encodeUnscaledMemInst + encodePairInst).
;
; Test vectors are added per-sub-task (commits 7..10 in M5 PR C):
;
;   Task 7  - MemBaseOff scaled  (ldr / str)
;           - MemBaseOff unscaled (stur / ldur)
;           - STR auto-promote to STUR for negative scaled offsets.
;
;   Task 8  - MemBaseOffPre / MemBaseOffPost (pre/post-index)
;           - MemBaseIdx / MemBaseIdxShifted (register offset)
;
;   Task 9  - MemBaseIdxExtended (extended-register offset)
;           - Pair (ldp/stp).  Uses encode_pair_word (separate entry).
;
;   Task 10 - Signed loads: ldrsb / ldrsh / ldrsw (sf opc=10/11 selector).
;
; The Layer-3 fixture corpus under tests/operands/sources/ exercises the same
; shapes end-to-end via the parser; this file isolates the encoder for
; per-routine assertion before the corpus sweep runs.


; -----------------------------------------------------------------------
; run_mem_self_tests — entry from assembler.asm start:.
; -----------------------------------------------------------------------
run_mem_self_tests:

; ===================== Task 7: scaled + unscaled =======================

; -- (1) ldr x0, [x1, #8]            →  0xf9400420 -----------------------
; mnem=5(ldr), Rt=x0, base=x1, shape=MemBaseOff, off=+8
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ; Rt=0 already
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1                ; shape = MemBaseOff
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1                ; base = 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 8                ; OPMEM_OFF low byte = 8
                ld      (OPMEM_OFF + 0), a
                xor     a
                ld      (OPMEM_OFF + 1), a
                ld      (OPMEM_OFF + 2), a
                ld      (OPMEM_OFF + 3), a
                ld      (OPMEM_OFF + 4), a
                ld      (OPMEM_OFF + 5), a
                ld      (OPMEM_OFF + 6), a
                ld      (OPMEM_OFF + 7), a
                ld      a, 5                ; mnem = ldr
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &04, &40, &f9

; -- (2) stur x0, [x1, #-4]          →  0xf81fc020 -----------------------
; mnem=74(stur), Rt=x0, base=x1, shape=MemBaseOff, off=-4
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ; OPMEM_OFF = -4 (sign-extended 8 bytes)
                ld      a, &fc
                ld      (OPMEM_OFF + 0), a
                ld      a, &ff
                ld      (OPMEM_OFF + 1), a
                ld      (OPMEM_OFF + 2), a
                ld      (OPMEM_OFF + 3), a
                ld      (OPMEM_OFF + 4), a
                ld      (OPMEM_OFF + 5), a
                ld      (OPMEM_OFF + 6), a
                ld      (OPMEM_OFF + 7), a
                ld      a, 74               ; mnem = stur
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &c0, &1f, &f8

; -- (3) str x0, [x1, #-4]           →  0xf81fc020 (auto-promote to STUR)
; mnem=6(str) with negative offset; encoder auto-promotes to the STUR
; encoding template.  Bytes must equal those of (2).
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, &fc
                ld      (OPMEM_OFF + 0), a
                ld      a, &ff
                ld      (OPMEM_OFF + 1), a
                ld      (OPMEM_OFF + 2), a
                ld      (OPMEM_OFF + 3), a
                ld      (OPMEM_OFF + 4), a
                ld      (OPMEM_OFF + 5), a
                ld      (OPMEM_OFF + 6), a
                ld      (OPMEM_OFF + 7), a
                ld      a, 6                ; mnem = str
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &c0, &1f, &f8

; -- (4) ldr w0, [x1, #4]            →  0xb9400420 -----------------------
; W-form scaled.  sizeBits=10, scale=4, imm12=1.
                call    test_mem_wipe
                ld      a, OP_KIND_REG_W
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 4
                ld      (OPMEM_OFF + 0), a
                xor     a
                ld      (OPMEM_OFF + 1), a
                ld      (OPMEM_OFF + 2), a
                ld      (OPMEM_OFF + 3), a
                ld      (OPMEM_OFF + 4), a
                ld      (OPMEM_OFF + 5), a
                ld      (OPMEM_OFF + 6), a
                ld      (OPMEM_OFF + 7), a
                ld      a, 5
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &04, &40, &b9

; ===================== Task 8: pre/post/register =======================

; -- (5) ldr x0, [x1, #8]!           →  0xf8408c20 (pre-index) ---------
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 2                ; shape = MemBaseOffPre
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 8
                ld      (OPMEM_OFF + 0), a
                ld      a, 5
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &8c, &40, &f8

; -- (6) ldr x0, [x1], #8            →  0xf8408420 (post-index) --------
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 3                ; shape = MemBaseOffPost
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 8
                ld      (OPMEM_OFF + 0), a
                ld      a, 5
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &84, &40, &f8

; -- (7) ldr x0, [x1, x2]            →  0xf8626820 (reg-offset) --------
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 4                ; shape = MemBaseIdx
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 2                ; idx Rm = 2
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 3), a
                ld      a, 1                ; idx_width = X
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 4), a
                ld      a, 5
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &68, &62, &f8

; -- (8) ldr x0, [x1, x2, lsl #3]    →  0xf8627820 (reg-shifted) -------
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 5                ; shape = MemBaseIdxShifted
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 2
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 3), a
                ld      a, 1                ; idx_width = X
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 4), a
                ld      a, 3                ; shift_amt non-zero → S=1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 6), a
                ld      a, 5
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &78, &62, &f8

; ===================== Task 9: extended + pair =========================

; -- (9) ldr x0, [x1, w2, uxtw #3]   →  0xf8625820 (extended UXTW) -----
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 6                ; shape = MemBaseIdxExtended
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1                ; base
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 2                ; idx
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 3), a
                xor     a                   ; idx_width = W
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 4), a
                ld      a, 2                ; extend = UXTW
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 5), a
                ld      a, 3                ; shift_amt (non-zero → S=1)
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 6), a
                ld      a, 5
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &58, &62, &f8

; -- (10) ldr x0, [x1, w2, sxtw]     →  0xf862c820 (extended SXTW, S=0)
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 6
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 2
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 3), a
                xor     a
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 4), a
                ld      a, 6                ; extend = SXTW
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 5), a
                ; shift_amt = 0 (already wiped)
                ld      a, 5
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &c8, &62, &f8

; -- (11) stp x0, x1, [sp, #-16]!    →  0xa9bf07e0 (pair pre-index)
; Rt1=x0, Rt2=x1, base=sp(31), shape=MemBaseOffPre, off=-16 → scaled=-2
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ; Rt1 reg = 0 (already wiped)
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a    ; Rt2 = x1
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 0), a
                ld      a, 2                ; shape = MemBaseOffPre
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 1), a
                ld      a, 31               ; base = sp (xzr/sp = 31)
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2), a
                ; OPMEM_OFF = -16 (sign-extended)
                ld      a, &f0
                ld      (OPMEM_OFF + 0), a
                ld      a, &ff
                ld      (OPMEM_OFF + 1), a
                ld      (OPMEM_OFF + 2), a
                ld      (OPMEM_OFF + 3), a
                ld      (OPMEM_OFF + 4), a
                ld      (OPMEM_OFF + 5), a
                ld      (OPMEM_OFF + 6), a
                ld      (OPMEM_OFF + 7), a
                ld      a, 8                ; mnem = stp
                call    encode_pair_word
                call    assert_eq32_de_hl_imm
                defb    &e0, &07, &bf, &a9

; -- (12) ldp x0, x1, [sp]           →  0xa94007e0 (pair, base only) ---
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 0), a
                xor     a                   ; shape = MemBase
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 1), a
                ld      a, 31
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2), a
                ld      a, 7                ; mnem = ldp
                call    encode_pair_word
                call    assert_eq32_de_hl_imm
                defb    &e0, &07, &40, &a9

; ===================== Task 10: signed loads ==========================

; -- (13) ldrsw x0, [x1]            →  0xb9800020 (signed-load Xt) -----
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                xor     a                   ; shape = MemBase
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 87               ; mnem = ldrsw
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &00, &80, &b9

; -- (14) ldrsb x0, [x1]            →  0x39800020 (signed-load byte, Xt)
                call    test_mem_wipe
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                xor     a
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 85               ; mnem = ldrsb
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &00, &80, &39

; -- (15) ldrsh w0, [x1, #4]        →  0x79c00820 (signed-load hword,Wt)
                call    test_mem_wipe
                ld      a, OP_KIND_REG_W
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, OP_KIND_MEM
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1                ; shape = MemBaseOff
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 4
                ld      (OPMEM_OFF + 0), a
                ld      a, 86               ; mnem = ldrsh
                call    encode_mem_word
                call    assert_eq32_de_hl_imm
                defb    &20, &08, &c0, &79

                ret


; -----------------------------------------------------------------------
; test_mem_wipe — zero OPVAL_ARRAY[0..2] (30 bytes) + OPVAL_KINDS[0..2]
; + OPMEM_OFF (8 bytes).
; -----------------------------------------------------------------------
test_mem_wipe:
                ld      hl, OPVAL_ARRAY
                ld      b, 30
                xor     a
test_mem_wipe_a:
                ld      (hl), a
                inc     hl
                djnz    test_mem_wipe_a
                ld      hl, OPVAL_KINDS
                ld      b, 3
test_mem_wipe_k:
                ld      (hl), a
                inc     hl
                djnz    test_mem_wipe_k
                ld      hl, OPMEM_OFF
                ld      b, 8
test_mem_wipe_o:
                ld      (hl), a
                inc     hl
                djnz    test_mem_wipe_o
                ret
