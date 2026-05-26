  .text
  ldr x2, =10f
  ldr x3, =1f
  ldr x4, =10f
1:
  .word 0xaabb
10:
  .word 0xccdd
