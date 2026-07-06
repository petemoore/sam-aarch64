  // Pins the packed patch-header length-escape (i353): the movz immediate is a
  // 4-label symbol-difference expression whose 24-byte bytecode overflows the
  // patch header's expr_len nibble, forcing the escape (cemit_ai_hdr_esc). The
  // summed value (2*4)+(4*4)=24 is a valid movz immediate, so it byte-matches GNU.
hdr_start:
  nop
  nop
hdr_end:
body_start:
  nop
  nop
  nop
  nop
body_end:
  movz w0, #((hdr_end - hdr_start) + (body_end - body_start))
  ret
