        .text
        // Class 5 — ADRP page-delta under a large (kernel-VMA) link origin.
        //
        // The spectrum4 release links at origin 0xfffffff0_00000000.  An
        // `adrp xN, <abs>` to a target whose page-delta's low 32 bits have
        // bit 31 set (e.g. delta 0xff841000) must NOT be treated as a
        // negative byte offset: the true byte offset is a 33-bit signed
        // value, and 0xff841000 (= +4286775296) is POSITIVE in 33-bit space
        // (bit 32 clear).  pageOffset = 0xff841000 >> 12 = 0xff841, which is
        // < 2^20 and fits the 21-bit signed immhi:immlo field with its sign
        // bit CLEAR.
        //
        // The Z80 port previously computed the page-delta in 32-bit
        // precision and did an arithmetic >>12, so bit 31 of 0xff841000 was
        // mis-read as the sign and immhi's top bit came out SET (GNU clears
        // it).  The fix mirrors tools/refenc/pass2.go:363-369 exactly:
        // diff = (v & ~0xFFF) - (pc & ~0xFFF) computed at full width, masked
        // to 33 bits, then sign-extended from bit 32 — before
        // encodeAdrpImm's >>12 (tools/aarch64enc/slots_adrp.go:11-24).
        //
        // NOTE ON ORACLE: under the round-trip's `ld -Ttext=0` the page
        // delta is small and positive, so this bug is INVISIBLE in that
        // path — the fixture passes trivially there.  The bug is only
        // observable at the release origin; the authoritative oracle for
        // Class 5 is refenc with -origin 0xfffffff000000000 vs the SAM OUT,
        // and the full release byte-match.  See
        // docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md Class 5.
        adrp    x2, 0xff841000
        adrp    x1, 0xff842000
        adrp    x10, 0xfd500000
        adrp    x1, 0xfe104000
