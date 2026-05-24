        .text
        // ADR Xd, <label> — PC-relative address with a raw (non-page-aligned)
        // 21-bit signed byte offset, range +/-1 MB.  Encoded as
        // immlo(bits 30:29):immhi(bits 23:5) of the byte offset
        // (target - PC).  The SAM encoder's AdrImm slot (encode_adr_imm in
        // slots/adrp_imm.asm) was previously a `jp fail` stub; the
        // spectrum4 release source uses `adr` heavily (e.g.
        // `adr x1, mailbox_base`).  Grounded against aarch64-none-elf-as +
        // ARM ARM C6.2.10 (ADR).
lbl_back:
        nop
        nop
        adr     x0, lbl_back        // negative offset (-8)
        adr     x1, lbl_fwd         // positive offset
        adr     x2, .               // offset 0
        adr     x21, lbl_fwd        // high register number
lbl_fwd:
        nop
