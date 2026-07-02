// inst_out_over32k.s — i24 OUT-ceiling-lift fixture.
//
// Emits > 40 KB of OUT so the emit path crosses TWO page boundaries of
// the pool-allocated OUT run (bytes 16384 and 32768) — output beyond
// the old two-page / 32 KB ceiling.  Per docs/specs/paged-out-design.md
// the run is sized from the pass-1 total (3 pages here) and every byte
// is stored through the uniform LMPR bracket; HSAVE saves the whole
// run as one contiguous file (UIFA pages count = 2, remainder = 8164).
//
// .skip is cheap in .tbn (a single directive record) but emits its
// length in zero bytes — forcing OUT past 32 KB without ballooning the
// IN buffer.  Distinct real instructions bracket the fill so an
// offset/paging error shows up as a byte mismatch, not just a length
// difference.

.text
  movz x0, #0x1111     // OUT[0..3]      — run page 0
  movz x1, #0x2222     // OUT[4..7]
  movz x2, #0x3333     // OUT[8..11]
  movz x3, #0x4444     // OUT[12..15]

  .skip 40900          // OUT[16..40915] zero-fill — crosses the run's
                       //   page 0 -> 1 boundary at byte 16384 and the
                       //   old 32 KB ceiling at byte 32768 (page 1 -> 2)

  movz x4, #0x5555     // OUT[40916..40919] — run page 2, past 32 KB
  movz x5, #0x6666     // OUT[40920..40923]
  movz x6, #0x7777     // OUT[40924..40927]
  ret                  // OUT[40928..40931]
