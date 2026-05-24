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
; The Layer-3 fixture corpus under tests/m5/sources/ exercises the same
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
