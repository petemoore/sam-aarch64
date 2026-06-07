        .text
        // LSL / LSR in both forms (Mac-side: tools/refenc/pass2.go:1196
        // encodeLSLSR; reached on SAM via the lsl/lsr intercept, mnemonics
        // 17/18).  Grounded against aarch64-none-elf-as + ARM ARM C6.2.218
        // (LSL immediate, UBFM alias), C6.2.222 (LSR immediate, UBFM alias),
        // C6.2.219 (LSLV), C6.2.223 (LSRV).
        //
        // Immediate form (UBFM alias):
        //   LSL imm: immr = (-shift) mod regsize, imms = regsize-1-shift
        //   LSR imm: immr = shift,                imms = regsize-1
        //   base = X ? 0xd3400000 : 0x53000000
        lsl     x0, x1, #1
        lsl     x2, x3, #30
        lsl     x4, x5, #63
        lsl     w6, w7, #1
        lsl     w8, w9, #31
        lsr     x10, x11, #1
        lsr     x12, x13, #30
        lsr     x14, x15, #63
        lsr     w16, w17, #1
        lsr     w18, w19, #31
        lsl     x20, x21, #0        // shift 0: immr=0, imms=regsize-1
        lsr     x22, x23, #0
        // Register form (LSLV / LSRV):
        lsl     x24, x25, x26
        lsl     w27, w28, w29
        lsr     x0, x1, x2
        lsr     w3, w4, w5
