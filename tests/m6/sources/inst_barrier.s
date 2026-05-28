        .text
        // Barrier instructions: isb / dsb / dmb.  text2bin converts the
        // barrier-arg keyword (sy, ish, ...) into an OpImmExpr carrying the
        // CRm field (bits 11:8); the encoder composes base | (CRm << 8).
        // isb with no arg defaults to sy (CRm=15).  These reach the SAM
        // assembler via the barrier intercept (IDs 66/67/68); without it
        // they hit FAIL40 (mnemonic-has-no-form).  Grounded against
        // aarch64-none-elf-as + ARM ARM C6.2.99 (ISB) / C6.2.74 (DSB) /
        // C6.2.73 (DMB).
        isb
        isb     sy
        dsb     sy
        dsb     ish
        dsb     ishst
        dsb     ishld
        dsb     nsh
        dsb     osh
        dsb     oshld
        dsb     oshst
        dmb     sy
        dmb     oshld
        dmb     ld
        dmb     st
