    .set X, 0x12340000
    .set Y, 0x00005678
    movz w0, X & 0xffff
    movz w0, (X >> 16) & 0xffff, lsl #16
    movk w0, Y & 0xffff
    movk w0, (Y >> 16) & 0xffff, lsl #16
