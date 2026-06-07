// LDP / STP — load/store-pair shapes (refenc/pass2.go:919-988).
// Covers signed-offset, pre-index, post-index across X and W destinations.
  stp x0, x1, [sp, #-16]!
  ldp x0, x1, [sp], #16
  stp x0, x1, [sp, #16]
  ldp x0, x1, [sp]
  stp w0, w1, [sp, #-8]!
  ldp w2, w3, [sp], #8
