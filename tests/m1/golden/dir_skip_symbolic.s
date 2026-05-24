// .space / .skip / .balign with .set-defined symbolic operands.
// Pass1 must resolve these via the same symbol-aware evaluator pass2
// uses, since `.set` defines symbols *before* the directives use them.
  .set SIZE_A, 16
  .set SIZE_B, 32
  .set BLOCK, (SIZE_A + SIZE_B * 2)
  .set ALIGN, 8
  .data
  .balign ALIGN
  .skip SIZE_A
  .space SIZE_B
  .skip BLOCK
