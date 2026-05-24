        .text
        // MOV (wide immediate) alias — bare `mov Rd, #imm` where the single
        // non-zero 16-bit chunk is NOT in the low slot.  GNU as auto-selects
        // the movz shift (lsl #0/#16/#32/#48).  Mac-side oracle:
        // tools/refenc/pass2.go tryEncodeMovImm step 1 (direct movz chunk
        // search) + tools/aarch64enc/encode.go:53-66.  Class 2 of
        // docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md.
        //
        // 64-bit: chunk in each of the four slots.
        mov     x0, #0x80000000
        mov     x2, #0x600000000
        mov     x9, #0x100000000
        mov     x10, #0x1234000000000000
        mov     x11, #0x1234
        // 32-bit: chunk in low slot and in the lsl #16 slot.
        mov     w0, #0x100000
        mov     w1, #0x10000
        mov     w3, #0x1234
        mov     w4, #0x12340000
