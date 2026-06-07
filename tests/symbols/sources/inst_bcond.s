main:
  cmp x0, #10
  b.lt main
  b.ne main
  b.eq main
  ret
