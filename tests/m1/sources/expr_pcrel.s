.text
  adrp x0, msg
  add x0, x0, :lo12:msg
msg:
  .ascii "hi"
