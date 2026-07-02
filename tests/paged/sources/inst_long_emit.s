// inst_long_emit.s — single page-boundary emit fixture.
//
// Per docs/specs/paged-out-design.md.  Emits > 16 KB of OUT so that
// emit_byte crosses the OUT run's first page boundary (out_advance_page:
// the cursor moves from run page 0 to run page 1, OUT_LMPR_CUR bumps).
//
// .skip is cheap in .tbn (a single directive record) but emits its
// length in zero bytes — perfect for forcing OUT past the 16 KB mark
// without ballooning the IN buffer.  Real ALU instructions on either
// side verify that emit works in both run pages (the real bytes show up
// at the start of page 0 and after the .skip in page 1).
//
// The > 32 KB companion (two boundary crossings, past the old two-page
// ceiling) is inst_out_over32k.s.

.text
  add x0, x0, x0       // OUT[0..3]    — run page 0
  add x1, x1, x1       // OUT[4..7]
  add x2, x2, x2       // OUT[8..11]
  add x3, x3, x3       // OUT[12..15]

  .skip 16384          // OUT[16..16399] zero-fill — crosses the run's
                       //   page 0 -> 1 boundary at byte 16384: the
                       //   page-filling byte parks OUT_PC at &8000 and
                       //   the next emit advances into page 1.

  add x4, x4, x4       // OUT[16400..16403] — run page 1
  add x5, x5, x5       // OUT[16404..16407]
  add x6, x6, x6
  add x7, x7, x7
  add x8, x8, x8
  add x9, x9, x9
