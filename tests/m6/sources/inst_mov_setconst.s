        .text
        // Stray @0x3594 — `mov Xd, <.set-constant>` under a large origin.
        //
        // A `.set`/`.equ` constant is ABSOLUTE: its high word is NOT the
        // link origin's high word.  The release links at origin
        // 0xfffffff0_00000000 and does `mov x9, RAM_DISK_SIZE` where
        // `.set RAM_DISK_SIZE, 0x10000000`.  GNU/refenc encode that as
        // movz x9, #0x1000, lsl #16 (d2a20009).
        //
        // The Z80 port's Class-1 fix re-applied ORIGIN_HIGH to EVERY symbol
        // value at eval time, so RAM_DISK_SIZE arrived as
        // 0xfffffff0_10000000 — a multi-chunk value that fails the MOV
        // single-chunk (MOVZ/MOVN) decomposition AND the ORR-bitmask path,
        // falling through to a wide-immediate form that emitted mov x9,#0
        // (d2800009).  The fix marks `.set`/`.equ` constants ABSOLUTE (their
        // evaluated high word != ORIGIN_HIGH) so eval_push_sym zero-fills
        // the high word for them while still re-applying ORIGIN_HIGH to
        // origin-relative labels (.quad <label> etc.).  Faithful to Go,
        // where Symbols[name] holds the full evaluated value and eval adds
        // no origin (tools/refenc/pass1.go:154/296, pass2.go:150).
        //
        // NOTE ON ORACLE: visible only at the release origin and only when
        // flattened (text2bin -flatten -origin bakes ORIGIN_HIGH).  The
        // authoritative oracle is refenc with -flatten -origin
        // 0xfffffff000000000 vs the SAM OUT, plus the full release
        // byte-match.  See
        // https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md.
        .set RAM_DISK_SIZE, 0x10000000
        .set HEAP_SIZE,     0x10000000
        mov     x9, RAM_DISK_SIZE
        sub     x9, x9, #0x60
        mov     x0, HEAP_SIZE
