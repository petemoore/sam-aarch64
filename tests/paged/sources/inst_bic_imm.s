        .text
        // Class 7 — `bic Rd, Rn, #imm` (immediate form) = `and Rd, Rn, #~imm`.
        //
        // The release's `bic w7, w7, #1` must encode as
        // `and w7, w7, #0xfffffffe` (121f78e7).  The generic form-table
        // path mapped bic-immediate onto the AND-imm encoding but used the
        // RAW immediate, emitting `and w7, w7, #0x1` (120000e7).  The fix
        // negates operand 2 in place (~imm) before the generic encoder, per
        // tools/refenc/pass2.go:304 (case 47) + encodeBicImm (pass2.go:1397
        // negImm = ^imm).
        //
        // Unlike the origin-only Classes 1/5 and the stray, this is visible
        // under the round-trip's `ld -Ttext=0` (the value is not
        // origin-relative), so this fixture guards it directly in the m6
        // SimCoupé corpus AND in the full release byte-match.
        bic     w7, w7, #1              // -> and w7, w7, #0xfffffffe
        bic     w9, w9, #0x10           // -> and w9, w9, #0xffffffef
        bic     x6, x6, #1              // -> and x6, x6, #0xfffffffffffffffe
        and     w0, w7, #0x07           // plain AND-imm (no negate) — regression guard
