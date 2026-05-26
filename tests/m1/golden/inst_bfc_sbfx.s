  .arch armv8.2-a
  bfc w0, #5, #10
  bfc x0, #32, #1
  bfc w0, #0, #1
  bfc x0, #0, #64
  sbfx w0, w1, #5, #10
  sbfx x0, x1, #32, #1
  sbfx x0, x1, #0, #64
