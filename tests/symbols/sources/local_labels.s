  mov x0, #5
1:
  sub x0, x0, #1
  cbnz x0, 1b
  ret
