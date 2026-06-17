// i28: a negative absolute .set/.equ value consumed by a 64-bit .quad must
// reconstruct its high word as the sign-extension of bit 31 (0xFFFFFFFF),
// matching the int64 model GNU/Go use — not zero it. The positive case pins
// that non-negative absolutes still get high word 0 (no regression).
.text
.set NEG_OFF, -256
.set NEG_ONE, -1
.set POS_VAL, 0x10000000
.quad NEG_OFF
.quad NEG_ONE
.quad POS_VAL
