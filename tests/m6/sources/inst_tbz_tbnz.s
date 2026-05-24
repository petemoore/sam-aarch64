        .text
        // TBZ / TBNZ — test bit and branch (Mac-side: tools/refenc/pass2.go:546
        // encodeTbzTbnz; reached on SAM via the tbz/tbnz intercept, mnemonics
        // 22/23).  Grounded against aarch64-none-elf-as + ARM ARM C6.2.317
        // (TBZ) / C6.2.318 (TBNZ).
        //
        //   bit 31    = b5  (bit number bit 5)
        //   bits30:25 = 011011
        //   bit 24    = op  (0 = TBZ, 1 = TBNZ)
        //   bits23:19 = b40 (bit number bits 4:0)
        //   bits18:5  = imm14 (signed PC-relative branch / 4, +/-32 KiB)
        //   bits 4:0  = Rt
tb_back:
        nop
        nop
        tbz     x0, #0, tb_back     // negative offset, bit < 32 (b5=0)
        tbz     x1, #40, tb_fwd     // bit >= 32 → b5=1 (X register)
        tbnz    x2, #5, tb_back     // tbnz, op=1
        tbnz    w3, #15, tb_fwd     // W register
        tbz     x4, #63, tb_fwd     // max bit
        tbz     x5, #0, .           // offset 0
        tbnz    x21, #31, tb_fwd    // high register
        nop
tb_fwd:
        nop
