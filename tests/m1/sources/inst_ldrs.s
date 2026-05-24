  // Signed-extend loads: LDRSB, LDRSH, LDRSW.
  // 64-bit (Xt) form uses opc=10; 32-bit (Wt) form uses opc=11.
  // LDRSW has only an Xt form.
  ldrsb w0, [x1]
  ldrsb x0, [x1]
  ldrsb w0, [x1, #3]
  ldrsb x0, [x1, #3]
  ldrsh w0, [x1]
  ldrsh x0, [x1]
  ldrsh w0, [x1, #4]
  ldrsh x0, [x1, #4]
  ldrsh x6, [x3, x5, lsl #1]
  ldrsw x0, [x1]
  ldrsw x0, [x1, #4]
  ldrsw x0, [x1, x2]
  ldrsw x0, [x1, x2, lsl #2]
