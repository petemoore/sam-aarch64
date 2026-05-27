// inst_long_emit.s — M6 PR 1 zone-crossing fixture.
//
// Per docs/specs/2026-05-27-m6-paged-out-design.md.  Emits > 16 KB of
// OUT so that emit_byte crosses the OUT_ZONE 0 → 1 boundary
// (low zone, section B = page 5 under LMPR_ENCTAB → high zone,
// section B = page 6 under LMPR_OUT_HIGH bracket).
//
// .skip is cheap in .tbn (a single directive record) but emits its
// length in zero bytes — perfect for forcing OUT past the 16 KB mark
// without ballooning the IN buffer.  Real ALU instructions on either
// side verify that emit works in both zones (the real bytes show up
// at the start of the low zone and after the .skip in the high zone).

.text
  add x0, x0, x0       // OUT[0..3]    — low zone
  add x1, x1, x1       // OUT[4..7]
  add x2, x2, x2       // OUT[8..11]
  add x3, x3, x3       // OUT[12..15]

  .skip 16384          // OUT[16..16399] zero-fill — crosses &4000
                       //   first 16368 bytes land in low zone,
                       //   then OUT_PC wraps and OUT_ZONE flips
                       //   to 1; remaining 16 bytes land in
                       //   high zone (page 6).

  add x4, x4, x4       // OUT[16400..16403] — high zone
  add x5, x5, x5       // OUT[16404..16407]
  add x6, x6, x6
  add x7, x7, x7
  add x8, x8, x8
  add x9, x9, x9
