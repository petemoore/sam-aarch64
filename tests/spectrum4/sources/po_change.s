// Reduced from ~/git/spectrum4/src/spectrum4/roms/po_change.s.
// The original references `CURCHL-sysvars`, two symbols defined in
// libextra/sysvars.s.in that are out of scope for a standalone
// fixture. Replaced with the literal offset that the spectrum4 build
// would produce, so the instruction sequence is byte-identical.

.text
.align 2

po_change:
  ldr     x5, [x28, 8]
  str     x4, [x5]
  ret
