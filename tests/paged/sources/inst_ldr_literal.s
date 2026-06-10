        .text
        // LDR (literal) direct-label form: `ldr Xt, <label>` / `ldr Wt, <label>`
        // (no `=`).  PC-relative load whose target IS the label; encoded as a
        // signed 19-bit immediate imm19 = (label - PC) / 4.  64-bit base
        // 0x58000000, 32-bit base 0x18000000.  Distinct from `ldr Xt, =expr`
        // (literal-pool slot, OpLitPool).  The Mac-side dispatch is
        // tools/refenc/pass2.go:281 (encodeLdrLitDirect); the SAM side reaches
        // it via the ldr-literal intercept (mnemonic 5, operand 1 = OpImmExpr).
        // Without the intercept it hits FAIL40 / form-lookup fail.  The
        // spectrum4 release source uses this form heavily.  Grounded against
        // aarch64-none-elf-as + ARM ARM C6.2.131 (LDR literal).
lit_back:
        .quad   0x1122334455667788
        nop
        ldr     x0, lit_back        // negative offset
        ldr     x1, lit_fwd         // positive offset
        ldr     x21, lit_back       // high register
        ldr     w2, lit_fwd         // 32-bit form
        ldr     x3, .               // offset 0 (loads itself, imm19=0)
        nop
lit_fwd:
        .quad   0x99aabbccddeeff00
