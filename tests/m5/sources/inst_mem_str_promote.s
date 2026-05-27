// STR / LDR auto-promote to STUR / LDUR when offsets are negative or
// non-aligned to the scaled imm12.  GNU as performs this rewrite
// transparently (refenc/pass2.go:776-779 mirrors); we exercise the
// path explicitly.
  str x0, [x1, #-4]
  ldr x0, [x1, #-4]
  str w0, [x1, #-1]
  ldr w0, [x1, #-1]
