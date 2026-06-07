        .text
        // Regression fixture: non-canonical logical-immediate word survives
        // the disasm round-trip byte-identically.
        //
        // 0x32200013 is a non-canonical `orr w19, w0, #0x1`: the element
        // size is 32 but the immr field is 32 (== esize).  Per ARM ARM
        // C4.1.64 "DecodeBitMasks" hardware computes R = immr MOD esize, so
        // an immr >= esize silently wraps.  Decoding to a canonical
        // immediate and re-encoding would change the raw bits (the canonical
        // form uses immr MOD esize), so aarch64dec's decodeBitMasks DECLINES
        // the word and it falls through to `.inst 0x32200013`, preserving the
        // exact 4 bytes.  Re-assembling that `.inst` must reproduce the
        // original word — that byte-identity is what this fixture asserts in
        // the disasm round-trip gate (tools/run-disasm-roundtrip.sh).
        //
        // This word was lifted from spectrum4 release data, where it appears
        // as an interleaved 32-bit data value, not an instruction.  The
        // surrounding nops exercise ordinary decode either side of the
        // declined word.
        //
        // Companion Go unit tests:
        //   tools/aarch64dec/slots_test.go
        //     TestDecode_LogicalImm_NonCanonical_RejectsToInst
        //     TestDecode_LogicalImm_Canonical_BoundaryDecodes
        //     TestDecodeBitMasks_ImmrBoundary
        nop
        .inst   0x32200013
        nop
