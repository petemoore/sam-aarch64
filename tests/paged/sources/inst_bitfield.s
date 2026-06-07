        .text
        // Bitfield aliases bfi / bfxil / ubfx / bfc / sbfx (Mac-side:
        // tools/refenc/pass2.go:1277 encodeBitfieldInst; reached on SAM via
        // the bitfield intercept, mnemonics 49/50/51/83/84).  All are
        // BFM/SBFM/UBFM aliases with computed immr/imms.  Grounded against
        // aarch64-none-elf-as + ARM ARM C6.2.40 (BFI), C6.2.42 (BFXIL),
        // C6.2.335 (UBFX), C6.2.41 (BFC), C6.2.270 (SBFX).
        //
        //   BFI  : immr=(-lsb)%regsize, imms=width-1   base BFM
        //   BFXIL: immr=lsb,            imms=lsb+width-1 base BFM
        //   UBFX : immr=lsb,            imms=lsb+width-1 base UBFM
        //   BFC  : Rn=XZR; immr=(-lsb)%regsize, imms=width-1 base BFM
        //   SBFX : immr=lsb,            imms=lsb+width-1 base SBFM
        bfi     x0, x1, #4, #8
        bfi     w2, w3, #0, #16     // lsb 0 → immr 0
        bfi     x4, x5, #40, #20
        bfxil   x6, x7, #4, #8
        bfxil   w8, w9, #8, #16
        ubfx    x10, x11, #4, #8
        ubfx    w12, w13, #0, #32
        ubfx    x14, x15, #20, #40
        bfc     x16, #4, #8
        bfc     w17, #8, #16
        sbfx    x18, x19, #4, #8
        sbfx    w20, w21, #0, #16
        sbfx    x22, x23, #40, #20
