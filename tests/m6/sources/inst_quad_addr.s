        .text
        // Class 1 — 64-bit absolute address data (.quad <label>).
        //
        // A `.quad <label>` must emit the label's FULL 64-bit VMA: the low
        // 32 bits are the label's offset within the image, the high 32 bits
        // are the link origin's high word.  The spectrum4 release links at
        // origin 0xfffffff0_00000000, so a label resolves to
        // 0xfffffff0_00xxxxxx and GNU emits high word 0xfffffff0.  The Z80
        // port truncated the high word to 0 (PASS_PC is 32-bit only); the
        // fix re-applies ORIGIN_HIGH in eval_push_sym (and push_pc/local).
        //
        // NOTE ON ORACLE: under the round-trip's `ld -Ttext=0`, GNU also
        // emits high word 0, so this fixture passes trivially in that path
        // and only guards the constant-vs-label width handling.  The bug
        // itself is only observable at the release origin — the authoritative
        // oracle for Class 1 is refenc with -origin 0xfffffff000000000 vs
        // the SAM OUT (and the full release byte-match).  See
        // https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md Class 1.
        // Mac-side reference: tools/refenc/pass2.go:1775 (evalImmsAsBytes)
        // over a symbol value seeded from OriginVMA (pass1.go:154 / 18).
target:
        nop
ptr:
        .quad target            // origin + 0
        .quad ptr               // origin + 4
        .quad 0x93              // plain constant: high word stays 0
        .quad target + 8        // label + offset
