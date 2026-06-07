  .text
  ldr x0, =0x30d0088a
  ldr x1, =msg
  ldr w2, =0xdeadbeef
  ldr x3, =0x12345678
msg:
  .asciz "hi"
