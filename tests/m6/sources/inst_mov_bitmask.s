        .text
        // MOV (bitmask immediate) alias.  Bare `mov Rd, #imm` where the value
        // is neither a single movz chunk nor a single movn chunk, but IS a
        // valid logical (bitmask) immediate.  GNU as emits ORR Rd, XZR, #imm.
        // Mac-side oracle: tools/refenc/pass2.go tryEncodeMovImm step 3
        // (orr-immediate via encodeLogicalImm).  Class 3 of
        // docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md.
        // (Verified against aarch64-none-elf-as: each value below disassembles
        // to an `orr Rd, xzr, #imm` i.e. base 0xb2.../0x32...).
        mov     x15, #0xcccccccccccccccc
        mov     x4, #0xff00ff00ff00ff00
        mov     x5, #0xff00ff00ff00ff
        mov     x6, #0x5555555555555555
        mov     w10, #0x3ffffffc
