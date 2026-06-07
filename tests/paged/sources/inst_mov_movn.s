        .text
        // MOV (inverted wide immediate) → MOVN alias.  Bare `mov Rd, #imm`
        // where the value is NOT a single movz chunk but ~value IS.  GNU as
        // emits MOVN.  Mac-side oracle: tools/refenc/pass2.go tryEncodeMovImm
        // step 2 (movn chunk search over ~u, masked to 32 bits for W).
        // Class 4 of https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md.
        // These previously produced an INVALID encoding on SAM (hw=11 on a
        // 32-bit op), so this is a correctness fix, not just a byte-match.
        mov     w1, #0xffffffff
        mov     w0, #0xfffffffd
        mov     w8, #0x8fcfffff
        mov     x12, #0xffffffffffffffff
        mov     x13, #0xfffffffffffffffd
        mov     x14, #0xffff0000ffffffff
