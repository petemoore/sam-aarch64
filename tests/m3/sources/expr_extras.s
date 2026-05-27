  // Constant-folded expressions covering ADD/SUB/SHL/SHR/AND/OR/XOR/NEG/NOT.
  // text2bin's constant-folder collapses each #(...) to a single PUSH_IMMn,
  // so the Z80 only ever sees the folded value — but this exercises the
  // expression evaluator's correctness with non-trivial inputs.
  mov x0, #(1 + 2 - 3)
  mov x0, #(0x10 << 4)
  mov x0, #(0xff >> 4)
  mov x0, #(0xff & 0xf0)
  mov x0, #(0x0f | 0xf0)
  mov x0, #(0xff ^ 0x0f)
