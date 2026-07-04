// test_print_w0.s — spectrum4 slice for the on-SAM preprocessor corpus gate
// (i31b-b4c). A self-contained excerpt of spectrum4's print_w0 test exercising
// the _str macro (the smallest real macro consumer, spec §7) alongside
// adrp/add/:lo12: relocations, so on-SAM macro expansion is proven byte-exact
// end-to-end through the assemble chain: expanded text byte-equals host `-E`,
// then text→.tbn byte-equals host CompactTBNBytes.
//
// The referenced symbols (CURCHL, fake_*) are given local definitions here so
// the slice assembles standalone; in the real spectrum4 build they resolve to
// kernel externals and the _str macro comes from kernel/macros.s.

.macro _str val, addr
  ldr     x0, =\val
  adrp    x1, \addr
  add     x1, x1, :lo12:\addr
  str     x0, [x1]
.endm

.section text_tests, "ax"
.align 2

print_w0_1_setup:
  _str    fake_channel_block, CURCHL
  ret

print_w0_1_effects:
  adrp    x0, fake_print_buffer_location
  add     x0, x0, :lo12:fake_print_buffer_location
  adrp    x1, fake_print_buffer
  add     x1, x1, :lo12:fake_print_buffer
  mov     w2, 'x'
  strb    w2, [x1], #1
  str     x1, [x0]
  ret

.section data
CURCHL:                     .quad 0
fake_channel_block:         .quad 0
fake_print_buffer_location: .quad 0
fake_print_buffer:          .quad 0
