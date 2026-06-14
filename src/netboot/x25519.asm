; x25519.asm — Curve25519 field arithmetic over GF(2^255 - 19), the foundation
; layer for the i88 TLS key exchange (X25519, RFC 7748).  This file holds the
; field operations; the Montgomery ladder + scalar mult build on them.
;
; Field elements are 32-byte little-endian numbers kept < 2^255 (the "almost
; reduced" form: every operation folds its result below 2^255 but not necessarily
; below p = 2^255 - 19; fe_freeze produces the canonical < p form for output).
; The Z80 has no multiply, so this is byte-radix (radix 2^8) big-integer code:
;   * mul8          — 8x8 -> 16 quarter-square multiply (qsq.asm)
;   * fe_schoolbook — the 32x32 -> 64-byte product
;   * fe_mul        — schoolbook + reduction mod 2^255-19
;   * fe_add/fe_sub — modular add/subtract
;   * fe_mul121665  — multiply by the curve constant a24 = (A-2)/4 = 121665
;   * fe_freeze     — canonicalise to [0, p)
; Reduction uses 2^256 ≡ 38 and 2^255 ≡ 19 (mod p): fold the high half of the
; product in via *38, then fold any residual bit-255 overflow in via *19 until the
; value drops below 2^255 (fe_reduce33).
;
; NOTE: not constant-time (the reductions branch on data).  For this project's use
; — a one-shot firmware fetch over a slow link to a public-key-pinned server, where
; the content is independently SHA-256-pinned (i88 scope, design §7) — timing side
; channels are not a practical threat; correctness is what matters.
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/x25519_field_test.go assembles this file, runs each
; field op under the koron-go/z80 harness, and asserts the result matches a
; math/big reference (mod 2^255-19) across edge values (0, 1, p, p-1, 2^255-1,
; 19/38, 121665, max·max) — byte-for-byte for fe_freeze (canonical) and value-equal
; mod p for the almost-reduced ops, plus the < 2^255 output invariant.

                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

; ===========================================================================
; Inputs / output / working buffers + scratch (data first).
; ===========================================================================
FE_A:           defs 32                 ; in: operand a (little-endian, < 2^255)
FE_B:           defs 32                 ; in: operand b
FE_OUT:         defs 32                 ; out: the result (< 2^255)

FE_PROD:        defs 65                 ; a*b product (64 bytes used + slack)
FE_T:           defs 34                 ; 38*high during reduction / freeze temp
FE_ACC:         defs 34                 ; the reduction accumulator (< 2^263)

fe_ap:          defs 2                  ; &a[i] in the schoolbook outer loop
fe_pp:          defs 2                  ; &PROD[i] in the schoolbook outer loop
fe_ai:          defs 1                  ; a[i], the current multiplier byte

; --- Karatsuba working set (fe_schoolbook, the 32x32 multiply) ---
k_mul_a:        defs 2                  ; kmul16 operand-A base pointer
k_mul_b:        defs 2                  ; kmul16 operand-B base pointer
k_mul_d:        defs 2                  ; kmul16 32-byte destination pointer
KDA:            defs 16                 ; |a_lo - a_hi| (16-byte absolute difference)
KDB:            defs 16                 ; |b_lo - b_hi|
KM:             defs 34                 ; |a_lo-a_hi| * |b_lo-b_hi| (32 bytes + 2 zero slack)
KZ1:            defs 34                 ; the middle coefficient z1 = z0 + z2 -/+ KM
k_sign:         defs 1                  ; sign of (a_lo-a_hi)*(b_lo-b_hi): 0 = +, 1 = -

INV_I:          defs 32                 ; fe_invert: the saved input i
INV_C:          defs 32                 ; fe_invert: the running accumulator
inv_a:          defs 1                  ; fe_invert: the addition-chain step counter

; --- X25519 scalar-mult I/O + Montgomery-ladder working set ---
X25519_K:       defs 32                 ; in: the 32-byte scalar
X25519_U:       defs 32                 ; in: the 32-byte u-coordinate
X25519_OUT:     defs 32                 ; out: the 32-byte shared u-coordinate

K_CLAMPED:      defs 32                 ; the clamped scalar
LX1:            defs 32                 ; x1 = u (constant through the ladder)
LX2:            defs 32                 ; the ladder state (x2, z2, x3, z3)
LZ2:            defs 32
LX3:            defs 32
LZ3:            defs 32
LA:             defs 32                 ; ladderstep temporaries
LAA:            defs 32
LB:             defs 32
LBB:            defs 32
LE:             defs 32
LC:             defs 32
LD:             defs 32
LDA:            defs 32
LCB:            defs 32
LT1:            defs 32                 ; general scratch
LT2:            defs 32
lad_t:          defs 1                  ; the current scalar bit index (254..0)
lad_swap:       defs 1                  ; the running cswap flag
lad_kt:         defs 1                  ; this iteration's scalar bit

; 2p = 2*(2^255-19) = 2^256 - 38 = 0x...FFDA (byte0 = 0xDA, bytes 1..31 = 0xFF).
FE_2P:          defb &DA, &FF, &FF, &FF, &FF, &FF, &FF, &FF
                defb &FF, &FF, &FF, &FF, &FF, &FF, &FF, &FF
                defb &FF, &FF, &FF, &FF, &FF, &FF, &FF, &FF
                defb &FF, &FF, &FF, &FF, &FF, &FF, &FF, &FF

; 121665 = 0x0001DB41 (little-endian byte0=0x41, byte1=0xDB, byte2=0x01).
; fe_mul121665 reads only these 3 bytes (the narrow schoolbook).
FE_121665:      defb &41, &DB, &01

; ===========================================================================
; fe_add — FE_OUT = (FE_A + FE_B) mod p.  Clobbers A, BC, DE, HL.
; ===========================================================================
fe_add:
                ld      hl, FE_A
                ld      de, FE_ACC
                ld      bc, 32
                ldir                            ; FE_ACC = FE_A
                ld      hl, FE_ACC
                ld      de, FE_B
                ld      b, 32
                or      a                       ; clear carry
fe_add_loop:
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    fe_add_loop
                ld      a, 0
                adc     a, 0
                ld      (FE_ACC + 32), a        ; the carry out of byte 31
                xor     a
                ld      (FE_ACC + 33), a
                call    fe_reduce33
                jp      fe_store_out

; ===========================================================================
; fe_sub — FE_OUT = (FE_A - FE_B) mod p, via FE_A + 2p - FE_B (always positive).
; Clobbers A, BC, DE, HL.
; ===========================================================================
fe_sub:
                ld      hl, FE_A                ; FE_ACC = FE_A + 2p  (33 bytes)
                ld      de, FE_ACC
                ld      bc, 32
                ldir
                ld      hl, FE_ACC
                ld      de, FE_2P
                ld      b, 32
                or      a
fe_sub_add2p:
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    fe_sub_add2p
                ld      a, 0
                adc     a, 0
                ld      (FE_ACC + 32), a
                xor     a
                ld      (FE_ACC + 33), a

                ld      hl, FE_ACC              ; FE_ACC -= FE_B  (33-byte minus 32-byte)
                ld      de, FE_B
                ld      b, 32
                or      a
fe_sub_subb:
                ld      a, (de)                 ; the Z80 has no sbc a,(de)
                ld      c, a
                ld      a, (hl)
                sbc     a, c
                ld      (hl), a
                inc     hl
                inc     de
                djnz    fe_sub_subb
                ld      a, (FE_ACC + 32)        ; absorb the final borrow
                sbc     a, 0
                ld      (FE_ACC + 32), a
                call    fe_reduce33
                jp      fe_store_out

; ===========================================================================
; fe_mul — FE_OUT = (FE_A * FE_B) mod p.  Clobbers A, BC, DE, HL, IX, IY + scratch.
; ===========================================================================
fe_mul:
                call    fe_schoolbook           ; FE_PROD = FE_A * FE_B (64 bytes)
                call    fe_reduce_prod          ; FE_ACC = FE_PROD mod p (< 2^255)
                jp      fe_store_out

; ===========================================================================
; fe_mul121665 — FE_OUT = (FE_A * 121665) mod p.  The curve constant a24 is only
; 3 bytes (0x01DB41), so a narrow schoolbook — FE_A's 32 bytes times the 3
; constant bytes, 96 mul8 — replaces the full 32×32 (1024 mul8) of fe_mul against
; a zero-padded operand.  The 34-byte product reuses the shared reduction path.
; Clobbers A, BC, DE, HL, IX, IY + scratch.
; ===========================================================================
fe_mul121665:
                ld      hl, FE_PROD             ; zero PROD (65 bytes)
                ld      (hl), 0
                ld      de, FE_PROD + 1
                ld      bc, 64
                ldir

                ld      hl, FE_A
                ld      (fe_ap), hl             ; &a[i], i = 0..31
                ld      hl, FE_PROD
                ld      (fe_pp), hl             ; &PROD[i]
                ld      b, 32
fe_121665_outer:
                push    bc
                ld      hl, (fe_ap)
                ld      a, (hl)
                ld      (fe_ai), a              ; a[i]
                ld      ix, FE_121665           ; constant byte source (3 bytes), held in IX
                ld      iy, (fe_pp)             ; PROD dest, held in IY
                ld      c, 0                    ; running inner-loop carry, kept in C
                ld      b, 3                    ; inner: the 3 constant bytes
fe_121665_inner:
                push    bc                      ; preserve counter B + carry C across mul8
                ld      a, (ix+0)
                inc     ix
                ld      e, a
                ld      a, (fe_ai)
                call    mul8                    ; HL = a[i] * c[j] (mul8 ignores D)
                pop     bc                      ; B = counter, C = running carry

                ld      a, c                    ; PROD[i+j] + carry, folded into one add
                add     a, (iy+0)               ; A = low byte, CF = bit 8
                ld      e, a
                ld      a, 0
                adc     a, 0
                ld      d, a                    ; DE = PROD[i+j] + carry (9-bit value)
                add     hl, de                  ; HL = product + PROD[i+j] + carry

                ld      a, l
                ld      (iy+0), a
                inc     iy
                ld      c, h                    ; new carry stays in C
                djnz    fe_121665_inner

                push    iy                      ; propagate the final row carry upward
                pop     de
                ld      a, c
fe_121665_carry:
                or      a
                jr      z, fe_121665_carry_done
                ld      c, a
                ld      a, (de)
                add     a, c
                ld      (de), a
                inc     de
                ld      a, 0
                adc     a, 0
                jr      fe_121665_carry
fe_121665_carry_done:
                ld      hl, (fe_ap)             ; &a[i] += 1
                inc     hl
                ld      (fe_ap), hl
                ld      hl, (fe_pp)             ; &PROD[i] += 1
                inc     hl
                ld      (fe_pp), hl
                pop     bc
                djnz    fe_121665_outer

                call    fe_reduce_prod          ; FE_ACC = FE_PROD mod p (< 2^255)
                jp      fe_store_out

; ===========================================================================
; fe_sqr — FE_OUT = (FE_A)^2 mod p.  Squaring is symmetric — every off-diagonal
; product a[i]*a[j] (i<j) appears twice — so a dedicated squarer computes the 496
; off-diagonal byte products once, doubles, then adds the 32 diagonal a[i]^2
; terms: ~528 mul8 calls vs the 1024 of fe_mul(a,a).  The Montgomery ladder is
; squaring-heavy (4 of every ladderstep's ~10 multiplies, plus 254 in fe_invert),
; so this is the dominant x25519 win.  Clobbers A, BC, DE, HL, IX, IY + scratch.
; ===========================================================================
fe_sqr:
                call    fe_sqr_schoolbook       ; FE_PROD = FE_A^2 (64 bytes)
                call    fe_reduce_prod          ; FE_ACC = FE_PROD mod p (< 2^255)
                jp      fe_store_out

; ---------------------------------------------------------------------------
; fe_store_out — copy FE_ACC[0..31] to FE_OUT.  (Shared op tail.)
; ---------------------------------------------------------------------------
fe_store_out:
                ld      hl, FE_ACC
                ld      de, FE_OUT
                ld      bc, 32
                ldir
                ret

; ===========================================================================
; fe_freeze — FE_OUT = canonical(FE_A) in [0, p).  Requires FE_A < 2^255.
; t = FE_A + 19; if t >= 2^255 then FE_A >= p, so FE_A = t - 2^255 (= FE_A - p);
; else FE_A is already < p.  Clobbers A, BC, DE, HL.
; ===========================================================================
fe_freeze:
                ld      hl, FE_A
                ld      de, FE_T
                ld      bc, 32
                ldir                            ; FE_T = FE_A
                ld      a, (FE_T)
                add     a, 19
                ld      (FE_T), a
                ld      hl, FE_T + 1
                ld      b, 31
fe_frz_carry:
                ld      a, (hl)
                adc     a, 0
                ld      (hl), a
                inc     hl
                djnz    fe_frz_carry

                ld      a, (FE_T + 31)
                and     &80                     ; bit 255 of t set?
                jr      nz, fe_frz_sub
                ld      hl, FE_A                ; FE_A < p -> unchanged
                ld      de, FE_OUT
                ld      bc, 32
                ldir
                ret
fe_frz_sub:
                ld      a, (FE_T + 31)          ; FE_OUT = t with bit 255 cleared
                and     &7F
                ld      (FE_T + 31), a
                ld      hl, FE_T
                ld      de, FE_OUT
                ld      bc, 32
                ldir
                ret

; ---------------------------------------------------------------------------
; mul8 (8x8 -> 16, quarter-square) + qsq_init + the qsq_table equate — the shared
; i102 multiply, extracted to qsq.asm and included by both x25519.asm and
; poly1305.asm. The x25519 entry calls qsq_init once at entry; the host field
; tests call qsq_init after load.
                if defined(NETBOOT_TLS_CLIENT)
                else                            ; standalone build: qsq comes from here
                include "qsq.asm"
                endif                           ; in the 6a composite qsq arrives via
                                                ; tls_record -> aead -> poly1305 (first)

; ---------------------------------------------------------------------------
; fe_schoolbook — FE_PROD (64 bytes) = FE_A (32) * FE_B (32) via one level of
; Karatsuba.  Split each operand at the 16-byte boundary: a = a_lo + a_hi*2^128,
; b = b_lo + b_hi*2^128.  Then
;   a*b = z0 + z1*2^128 + z2*2^256,  where
;   z0 = a_lo*b_lo,  z2 = a_hi*b_hi,  z1 = a_lo*b_hi + a_hi*b_lo.
; Karatsuba computes z1 with one extra half-multiply instead of two, using the
; subtractive identity  z1 = z0 + z2 - (a_lo-a_hi)*(b_lo-b_hi).  Working with the
; *absolute* differences |a_lo-a_hi|, |b_lo-b_hi| keeps both multiply operands
; exactly 16 bytes (no carry-out to special-case); the sign of their product is
; tracked in one bit (k_sign) and decides whether z1 = z0+z2 - m or z0+z2 + m.
; Three 16x16 schoolbooks (768 mul8) replace the flat 32x32 (1024 mul8); the
; combine is a handful of multi-byte adds/subs.  Clobbers A, BC, DE, HL, IX, IY.
; ---------------------------------------------------------------------------
fe_schoolbook:
                ld      hl, FE_A                ; z0 = a_lo * b_lo -> PROD[0..31]
                ld      (k_mul_a), hl
                ld      hl, FE_B
                ld      (k_mul_b), hl
                ld      hl, FE_PROD
                ld      (k_mul_d), hl
                call    kmul16

                ld      hl, FE_A + 16           ; z2 = a_hi * b_hi -> PROD[32..63]
                ld      (k_mul_a), hl
                ld      hl, FE_B + 16
                ld      (k_mul_b), hl
                ld      hl, FE_PROD + 32
                ld      (k_mul_d), hl
                call    kmul16

                ; KDA = |a_lo - a_hi|, KDB = |b_lo - b_hi|; k_sign = sign(product).
                ld      hl, FE_A                ; KDA = a_lo (then subtract a_hi)
                ld      de, KDA
                ld      bc, 16
                ldir
                ld      hl, KDA
                ld      de, FE_A + 16
                or      a
                ld      b, 16
fe_kda_sub:
                ld      a, (de)
                ld      c, a
                ld      a, (hl)
                sbc     a, c
                ld      (hl), a
                inc     hl
                inc     de
                djnz    fe_kda_sub
                ld      a, 0                    ; CF (final borrow) survives ld/inc/djnz
                jr      nc, fe_kda_done         ; a_lo >= a_hi -> positive, sign 0
                ld      hl, KDA                 ; negate KDA in place (16-byte two's complement)
                or      a
                ld      b, 16
fe_kda_neg:
                ld      a, 0
                sbc     a, (hl)
                ld      (hl), a
                inc     hl
                djnz    fe_kda_neg
                ld      a, 1
fe_kda_done:
                ld      (k_sign), a             ; provisional sign = sign(a_lo - a_hi)

                ld      hl, FE_B                ; KDB = b_lo (then subtract b_hi)
                ld      de, KDB
                ld      bc, 16
                ldir
                ld      hl, KDB
                ld      de, FE_B + 16
                or      a
                ld      b, 16
fe_kdb_sub:
                ld      a, (de)
                ld      c, a
                ld      a, (hl)
                sbc     a, c
                ld      (hl), a
                inc     hl
                inc     de
                djnz    fe_kdb_sub
                ld      a, 0
                jr      nc, fe_kdb_done
                ld      hl, KDB                 ; negate KDB in place
                or      a
                ld      b, 16
fe_kdb_neg:
                ld      a, 0
                sbc     a, (hl)
                ld      (hl), a
                inc     hl
                djnz    fe_kdb_neg
                ld      a, 1
fe_kdb_done:
                ld      hl, k_sign              ; k_sign = sign(a_lo-a_hi) XOR sign(b_lo-b_hi)
                xor     (hl)
                ld      (hl), a

                ld      hl, KDA                 ; KM = |a_lo-a_hi| * |b_lo-b_hi|
                ld      (k_mul_a), hl
                ld      hl, KDB
                ld      (k_mul_b), hl
                ld      hl, KM
                ld      (k_mul_d), hl
                call    kmul16
                xor     a                       ; KM is 32 bytes; zero the 2-byte slack
                ld      (KM + 32), a            ; so the 34-byte add/sub below reads 0 there
                ld      (KM + 33), a

                ld      hl, FE_PROD             ; KZ1 = z0 (low 32 bytes of the product)
                ld      de, KZ1
                ld      bc, 32
                ldir
                xor     a
                ld      (KZ1 + 33), a           ; KZ1[33] = 0 (KZ1[32] set from the carry below)
                ld      hl, KZ1                 ; KZ1 += z2 (PROD[32..63]); carry -> KZ1[32]
                ld      de, FE_PROD + 32
                ld      b, 32
                call    kadd
                ld      a, 0
                adc     a, 0
                ld      (KZ1 + 32), a           ; KZ1 = z0 + z2 (33 bytes)

                ld      a, (k_sign)             ; z1 = (z0+z2) - m  (sign +) or + m (sign -)
                or      a
                jr      nz, fe_z1_addm
                ld      hl, KZ1
                ld      de, KM
                ld      b, 34
                call    ksub                    ; KZ1 -= KM
                jr      fe_z1_done
fe_z1_addm:
                ld      hl, KZ1
                ld      de, KM
                ld      b, 34
                call    kadd                    ; KZ1 += KM
fe_z1_done:
                ld      hl, FE_PROD + 16        ; PROD[16..] += z1 (the middle coefficient)
                ld      de, KZ1
                ld      b, 34
                call    kadd                    ; HL -> PROD+50, CF = carry-out of byte 49
                ld      a, 0
                adc     a, 0
fe_asm_cprop:
                or      a                       ; propagate the carry up through PROD[50..63]
                ret     z
                ld      c, a
                ld      a, (hl)
                add     a, c
                ld      (hl), a
                inc     hl
                ld      a, 0
                adc     a, 0
                jr      fe_asm_cprop

; ---------------------------------------------------------------------------
; kmul16 — (k_mul_d)[0..31] = (k_mul_a)[0..15] * (k_mul_b)[0..15], schoolbook.
; Zeroes the 32-byte destination, then for each row i adds A[i]*B[j] into
; dest[i+j] with a running carry (final row carry propagated at dest[i+16]).
; The inner loop holds its pointers in IX (B source) and IY (dest) across the
; mul8 call (mul8 preserves IX/IY).  Clobbers A, BC, DE, HL, IX, IY + fe_ai.
; ---------------------------------------------------------------------------
kmul16:
                ld      hl, (k_mul_d)           ; zero dest (32 bytes)
                ld      (hl), 0
                push    hl
                ld      d, h
                ld      e, l
                inc     de
                ld      bc, 31
                ldir
                pop     hl                      ; HL = dest base
                ld      (fe_pp), hl
                ld      hl, (k_mul_a)
                ld      (fe_ap), hl
                ld      b, 16                   ; outer: i = 0..15
kmul16_outer:
                push    bc
                ld      hl, (fe_ap)
                ld      a, (hl)
                ld      (fe_ai), a
                ld      ix, (k_mul_b)           ; B source, held across the inner loop
                ld      iy, (fe_pp)             ; dest pointer, held across the inner loop
                ld      c, 0                    ; running inner-loop carry, kept in C
                ld      b, 16                   ; inner: j = 0..15
kmul16_inner:
                push    bc                      ; preserve counter B + carry C across mul8
                ld      a, (ix+0)               ; B[j]
                inc     ix
                ld      e, a
                ld      a, (fe_ai)
                call    mul8                    ; HL = A[i] * B[j] (mul8 ignores D)
                pop     bc                      ; B = counter, C = running carry

                ld      a, c                    ; dest[i+j] + carry, folded into one add
                add     a, (iy+0)               ; A = low byte, CF = bit 8
                ld      e, a
                ld      a, 0
                adc     a, 0
                ld      d, a                    ; DE = dest[i+j] + carry (9-bit value)
                add     hl, de                  ; HL = product + dest[i+j] + carry

                ld      a, l
                ld      (iy+0), a
                inc     iy
                ld      c, h                    ; new carry stays in C
                djnz    kmul16_inner

                push    iy                      ; add final carry at dest[i+16]..
                pop     de
                ld      a, c
kmul16_carry:
                or      a
                jr      z, kmul16_carry_done
                ld      c, a
                ld      a, (de)
                add     a, c
                ld      (de), a
                inc     de
                ld      a, 0
                adc     a, 0
                jr      kmul16_carry
kmul16_carry_done:
                ld      hl, (fe_ap)
                inc     hl
                ld      (fe_ap), hl
                ld      hl, (fe_pp)
                inc     hl
                ld      (fe_pp), hl
                pop     bc
                djnz    kmul16_outer
                ret

; ---------------------------------------------------------------------------
; kadd — (HL)[0..B-1] += (DE)[0..B-1], little-endian, with carry.  On return CF
; is the final carry-out and HL/DE point one past the operands.  Clobbers A.
; ksub — the matching (HL) -= (DE); on return CF is the final borrow.
; ---------------------------------------------------------------------------
kadd:
                or      a                       ; clear carry-in
kadd_loop:
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    kadd_loop
                ret

ksub:
                or      a                       ; clear borrow-in
ksub_loop:
                ld      a, (de)                 ; subtrahend (no sbc a,(de) on Z80)
                ld      c, a
                ld      a, (hl)
                sbc     a, c
                ld      (hl), a
                inc     hl
                inc     de
                djnz    ksub_loop
                ret

; ---------------------------------------------------------------------------
; fe_sqr_schoolbook — FE_PROD (64 bytes) = FE_A (32) squared, exploiting symmetry.
;   c = sum_i a[i]^2 * 256^(2i)  +  2 * sum_{i<j} a[i]*a[j] * 256^(i+j)
; Phase 1 accumulates the strictly-upper-triangle off-diagonal sum into PROD (row
; i multiplies a[i] by a[i+1..31] into PROD[2i+1..], 496 products total — half a
; full schoolbook).  Phase 2 doubles PROD in place (left shift by 1).  Phase 3
; adds the 32 diagonal a[i]^2 terms at PROD[2i].  The exact square stays < 2^512,
; so the doubled-then-diagonal sum never carries past PROD[63].  The off-diagonal
; phase holds its pointers in IX/IY across mul8 (the double/diagonal phases don't).
; Clobbers A, BC, DE, HL, IX, IY.
; ---------------------------------------------------------------------------
fe_sqr_schoolbook:
                ld      hl, FE_PROD             ; zero PROD (65 bytes)
                ld      (hl), 0
                ld      de, FE_PROD + 1
                ld      bc, 64
                ldir

                ; --- phase 1: off-diagonal sum_{i<j} a[i]*a[j] at PROD[i+j] ---
                ld      hl, FE_A
                ld      (fe_ap), hl             ; &a[i], i = 0..30
                ld      hl, FE_PROD + 1
                ld      (fe_pp), hl             ; &PROD[2i+1]
                ld      b, 31                   ; outer i = 0..30; B = 31-i = inner count
fe_sqr_outer:
                push    bc                      ; [outer counter]
                ld      hl, (fe_ap)
                ld      a, (hl)
                ld      (fe_ai), a              ; a[i]
                inc     hl
                push    hl                      ; b-source (a[i+1]) held in IX
                pop     ix
                ld      iy, (fe_pp)             ; PROD dest (PROD[2i+1]) held in IY
                ld      c, 0                    ; running inner-loop carry, kept in C
                                                ; B still = outer counter = inner count
fe_sqr_inner:
                push    bc                      ; preserve counter B + carry C across mul8
                ld      a, (ix+0)
                inc     ix
                ld      e, a
                ld      a, (fe_ai)
                call    mul8                    ; HL = a[i] * a[j] (mul8 ignores D)
                pop     bc                      ; B = counter, C = running carry

                ld      a, c                    ; PROD[i+j] + carry, folded into one add
                add     a, (iy+0)               ; A = low byte, CF = bit 8
                ld      e, a
                ld      a, 0
                adc     a, 0
                ld      d, a                    ; DE = PROD[i+j] + carry (9-bit value)
                add     hl, de                  ; HL = product + PROD[i+j] + carry

                ld      a, l
                ld      (iy+0), a
                inc     iy
                ld      c, h                    ; new carry stays in C
                djnz    fe_sqr_inner

                push    iy                      ; propagate the final row carry upward
                pop     de
                ld      a, c
fe_sqr_carry:
                or      a
                jr      z, fe_sqr_carry_done
                ld      c, a
                ld      a, (de)
                add     a, c
                ld      (de), a
                inc     de
                ld      a, 0
                adc     a, 0
                jr      fe_sqr_carry
fe_sqr_carry_done:
                ld      hl, (fe_ap)             ; &a[i] += 1
                inc     hl
                ld      (fe_ap), hl
                ld      hl, (fe_pp)             ; &PROD[2i+1] += 2
                inc     hl
                inc     hl
                ld      (fe_pp), hl
                pop     bc
                djnz    fe_sqr_outer

                ; --- phase 2: PROD *= 2 (little-endian left shift across 64 bytes) ---
                ld      hl, FE_PROD
                ld      b, 64
                or      a                       ; clear carry into bit 0
fe_sqr_double:
                ld      a, (hl)
                rla                             ; A <<= 1 through carry; CF = old bit7
                ld      (hl), a
                inc     hl
                djnz    fe_sqr_double

                ; --- phase 3: add the diagonal a[i]^2 at PROD[2i], i = 0..31 ---
                ld      hl, FE_A
                ld      (fe_ap), hl
                ld      hl, FE_PROD
                ld      (fe_pp), hl             ; &PROD[2i]
                ld      b, 32
fe_sqr_diag:
                push    bc
                ld      hl, (fe_ap)
                ld      a, (hl)
                inc     hl
                ld      (fe_ap), hl
                ld      e, a                    ; E = a[i]; A = a[i] (mul8 ignores D)
                call    mul8                    ; HL = a[i]^2

                ld      de, (fe_pp)
                ld      a, (de)                 ; PROD[2i] += low byte
                add     a, l
                ld      (de), a
                inc     de
                ld      a, (de)                 ; PROD[2i+1] += high byte + carry
                adc     a, h
                ld      (de), a
                inc     de
                jr      nc, fe_sqr_diag_adv
fe_sqr_diag_cprop:
                ld      a, (de)                 ; propagate the carry upward
                inc     a
                ld      (de), a
                inc     de
                jr      z, fe_sqr_diag_cprop    ; a 0xFF->0x00 wrap carries on
fe_sqr_diag_adv:
                ld      hl, (fe_pp)             ; &PROD[2i] += 2
                inc     hl
                inc     hl
                ld      (fe_pp), hl
                pop     bc
                djnz    fe_sqr_diag
                ret

; ---------------------------------------------------------------------------
; fe_reduce_prod — FE_ACC = FE_PROD (64 bytes) mod p, result < 2^255.
; Fold the high half in via 2^256 ≡ 38, then fe_reduce33.
; Clobbers A, BC, DE, HL, IX, IY.
; ---------------------------------------------------------------------------
fe_reduce_prod:
                call    fe_mul38_high           ; FE_T = 38 * FE_PROD[32..63]
                ld      hl, FE_PROD             ; FE_ACC[0..31] = the low half L
                ld      de, FE_ACC
                ld      bc, 32
                ldir
                xor     a
                ld      (FE_ACC + 32), a
                ld      (FE_ACC + 33), a
                call    fe_acc_add_T            ; FE_ACC += FE_T  (FE_ACC < 2^263)
                jp      fe_reduce33

; ---------------------------------------------------------------------------
; fe_mul38_high — FE_T (33 bytes) = 38 * FE_PROD[32..63].  Clobbers A, BC, DE, HL, IX, IY.
; ---------------------------------------------------------------------------
fe_mul38_high:
                ld      ix, FE_PROD + 32        ; high-half source, held in IX
                ld      iy, FE_T                ; dest, held in IY
                ld      c, 0                    ; running carry, kept in C
                ld      b, 32
fe_m38_loop:
                push    bc                      ; preserve counter B + carry C across mul8
                ld      a, (ix+0)
                inc     ix
                ld      e, 38
                call    mul8                    ; HL = H[i] * 38 (mul8 ignores D)
                pop     bc                      ; B = counter, C = running carry

                ld      a, c                    ; HL += carry (the only addend here)
                add     a, l                    ; A = low byte, CF = carry-out
                ld      (iy+0), a               ; store FE_T[i]
                inc     iy
                ld      a, h
                adc     a, 0                    ; new carry = H + carry-out
                ld      c, a                    ; keep it in C
                djnz    fe_m38_loop
                ld      a, c                    ; FE_T[32] = the final carry
                ld      (iy+0), a
                ret

; ---------------------------------------------------------------------------
; fe_acc_add_T — FE_ACC[0..33] += FE_T[0..32] (FE_T zero-extended).
; Clobbers A, B, DE, HL.
; ---------------------------------------------------------------------------
fe_acc_add_T:
                ld      hl, FE_ACC
                ld      de, FE_T
                ld      b, 33
                or      a
fe_acc_addT_loop:
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    fe_acc_addT_loop
                ld      a, (hl)                 ; carry into FE_ACC[33]
                adc     a, 0
                ld      (hl), a
                ret

; ---------------------------------------------------------------------------
; fe_reduce33 — fold FE_ACC (< 2^263, 34 bytes) below 2^255 in place, via
; 2^255 ≡ 19: hi = FE_ACC >> 255, lo = FE_ACC & (2^255-1), FE_ACC = lo + 19*hi,
; until hi == 0.  Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
fe_reduce33:
                ld      a, (FE_ACC + 32)        ; hi = (ACC[32]<<1) | (ACC[31]>>7)
                add     a, a
                ld      c, a
                ld      a, (FE_ACC + 31)
                rlca
                and     1
                or      c
                ret     z                       ; hi == 0 -> FE_ACC < 2^255, done
                ld      c, a                    ; c = hi (save for 19*hi)

                ld      a, (FE_ACC + 31)        ; clear bits >= 255
                and     &7F
                ld      (FE_ACC + 31), a
                xor     a
                ld      (FE_ACC + 32), a
                ld      (FE_ACC + 33), a

                ld      a, c                    ; HL = 19 * hi
                ld      e, 19
                call    mul8                    ; (mul8 ignores D)
                call    fe_acc_add_hl           ; FE_ACC += HL
                jr      fe_reduce33

; ---------------------------------------------------------------------------
; fe_acc_add_hl — FE_ACC (34 bytes) += HL (a 16-bit addend), carry propagated.
; Clobbers A, B, C, DE, HL.
; ---------------------------------------------------------------------------
fe_acc_add_hl:
                ld      c, h                    ; c = addend high
                ld      a, l                    ; a = addend low
                ld      hl, FE_ACC
                add     a, (hl)
                ld      (hl), a
                inc     hl
                ld      a, c
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                ld      b, 32
fe_acc_addhl_loop:
                ld      a, (hl)
                adc     a, 0
                ld      (hl), a
                inc     hl
                djnz    fe_acc_addhl_loop
                ret

; ---------------------------------------------------------------------------
; cp32 — copy 32 bytes from (HL) to (DE).  Clobbers BC, DE, HL.
; ---------------------------------------------------------------------------
cp32:
                ld      bc, 32
                ldir
                ret

; ===========================================================================
; fe_invert — FE_OUT = FE_A^(p-2) mod p = FE_A^-1 (Fermat's little theorem).
; A faithful port of TweetNaCl's inv25519: c = i; for a = 253..0: c = c^2, and
; c = c*i except at a == 2 and a == 4 (the addition chain for the exponent
; p-2 = 2^255 - 21).  Squaring uses fe_sqr.  Clobbers everything + scratch.
; ===========================================================================
fe_invert:
                ld      hl, FE_A                ; INV_I = i ; INV_C = i
                ld      de, INV_I
                call    cp32
                ld      hl, FE_A
                ld      de, INV_C
                call    cp32
                ld      a, 253
                ld      (inv_a), a
fe_inv_loop:
                ; INV_C = INV_C^2
                ld      hl, INV_C
                ld      de, FE_A
                call    cp32
                call    fe_sqr
                ld      hl, FE_OUT
                ld      de, INV_C
                call    cp32

                ld      a, (inv_a)              ; multiply unless a == 2 or a == 4
                cp      2
                jr      z, fe_inv_skip
                cp      4
                jr      z, fe_inv_skip
                ld      hl, INV_C               ; INV_C = INV_C * INV_I
                ld      de, FE_A
                call    cp32
                ld      hl, INV_I
                ld      de, FE_B
                call    cp32
                call    fe_mul
                ld      hl, FE_OUT
                ld      de, INV_C
                call    cp32
fe_inv_skip:
                ld      a, (inv_a)
                or      a
                jr      z, fe_inv_done
                dec     a
                ld      (inv_a), a
                jr      fe_inv_loop
fe_inv_done:
                ld      hl, INV_C
                ld      de, FE_OUT
                jp      cp32

; ---------------------------------------------------------------------------
; to_A / to_B / from_OUT — the field-op marshaling helpers.  to_A/to_B copy 32
; bytes from (HL) into FE_A/FE_B; from_OUT copies FE_OUT to (DE).  These make the
; ladder readable: "compute dst = op(s1,s2)" becomes load s1->A, s2->B, call op,
; store OUT->dst.  Clobber BC, DE, HL (to_A/to_B) / BC, HL (from_OUT).
; ---------------------------------------------------------------------------
to_A:
                ld      de, FE_A
                ld      bc, 32
                ldir
                ret
to_B:
                ld      de, FE_B
                ld      bc, 32
                ldir
                ret
from_OUT:
                ld      hl, FE_OUT
                ld      bc, 32
                ldir
                ret

; ---------------------------------------------------------------------------
; swap_bytes — exchange B bytes between (HL) and (DE).  Clobbers A, B, C, DE, HL.
; ---------------------------------------------------------------------------
swap_bytes:
                ld      a, (de)                 ; a = the (DE) byte
                ld      c, (hl)                 ; c = the (HL) byte
                ld      (hl), a                 ; (HL) = old (DE)
                ld      a, c
                ld      (de), a                 ; (DE) = old (HL)
                inc     hl
                inc     de
                djnz    swap_bytes
                ret

; ---------------------------------------------------------------------------
; cond_swap_state — if A != 0, exchange (LX2,LX3) and (LZ2,LZ3); the Montgomery
; ladder's two cswaps share one bit.  Not constant-time (branches on the bit).
; Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
cond_swap_state:
                or      a
                ret     z
                ld      hl, LX2
                ld      de, LX3
                ld      b, 32
                call    swap_bytes
                ld      hl, LZ2
                ld      de, LZ3
                ld      b, 32
                jp      swap_bytes

; ---------------------------------------------------------------------------
; ladderstep — one Montgomery ladder differential add-and-double (RFC 7748 §5),
; updating (LX2,LZ2,LX3,LZ3) from themselves and x1 = LX1.  a24 = 121665.
; Clobbers everything + scratch.
; ---------------------------------------------------------------------------
ladderstep:
                ld      hl, LX2                 ; A = x2 + z2
                call    to_A
                ld      hl, LZ2
                call    to_B
                call    fe_add
                ld      de, LA
                call    from_OUT

                ld      hl, LA                  ; AA = A^2
                call    to_A
                call    fe_sqr
                ld      de, LAA
                call    from_OUT

                ld      hl, LX2                 ; B = x2 - z2
                call    to_A
                ld      hl, LZ2
                call    to_B
                call    fe_sub
                ld      de, LB
                call    from_OUT

                ld      hl, LB                  ; BB = B^2
                call    to_A
                call    fe_sqr
                ld      de, LBB
                call    from_OUT

                ld      hl, LAA                 ; E = AA - BB
                call    to_A
                ld      hl, LBB
                call    to_B
                call    fe_sub
                ld      de, LE
                call    from_OUT

                ld      hl, LX3                 ; C = x3 + z3
                call    to_A
                ld      hl, LZ3
                call    to_B
                call    fe_add
                ld      de, LC
                call    from_OUT

                ld      hl, LX3                 ; D = x3 - z3
                call    to_A
                ld      hl, LZ3
                call    to_B
                call    fe_sub
                ld      de, LD
                call    from_OUT

                ld      hl, LD                  ; DA = D * A
                call    to_A
                ld      hl, LA
                call    to_B
                call    fe_mul
                ld      de, LDA
                call    from_OUT

                ld      hl, LC                  ; CB = C * B
                call    to_A
                ld      hl, LB
                call    to_B
                call    fe_mul
                ld      de, LCB
                call    from_OUT

                ld      hl, LDA                 ; x3 = (DA + CB)^2
                call    to_A
                ld      hl, LCB
                call    to_B
                call    fe_add
                ld      de, LT1
                call    from_OUT
                ld      hl, LT1
                call    to_A
                call    fe_sqr
                ld      de, LX3
                call    from_OUT

                ld      hl, LDA                 ; z3 = x1 * (DA - CB)^2
                call    to_A
                ld      hl, LCB
                call    to_B
                call    fe_sub
                ld      de, LT1
                call    from_OUT
                ld      hl, LT1
                call    to_A
                call    fe_sqr
                ld      de, LT2
                call    from_OUT
                ld      hl, LX1
                call    to_A
                ld      hl, LT2
                call    to_B
                call    fe_mul
                ld      de, LZ3
                call    from_OUT

                ld      hl, LAA                 ; x2 = AA * BB
                call    to_A
                ld      hl, LBB
                call    to_B
                call    fe_mul
                ld      de, LX2
                call    from_OUT

                ld      hl, LE                  ; z2 = E * (AA + a24 * E)
                call    to_A
                call    fe_mul121665            ; FE_OUT = a24 * E
                ld      de, LT1
                call    from_OUT
                ld      hl, LAA
                call    to_A
                ld      hl, LT1
                call    to_B
                call    fe_add
                ld      de, LT1                 ; T1 = AA + a24*E
                call    from_OUT
                ld      hl, LE
                call    to_A
                ld      hl, LT1
                call    to_B
                call    fe_mul
                ld      de, LZ2
                call    from_OUT
                ret

; ===========================================================================
; x25519 — X25519(scalar, u) per RFC 7748 §5: clamp the scalar, decode u, run
; the Montgomery ladder, and return x2 * z2^-1 (frozen) as 32 LE bytes.
; In:  X25519_K (scalar) and X25519_U (u) filled.  Out: X25519_OUT.
; The caller must allow a large step budget (tens of millions of byte-ops).
; ===========================================================================
x25519:
                call    qsq_init                ; build the multiply table once
                ld      hl, X25519_K            ; clamp the scalar
                ld      de, K_CLAMPED
                ld      bc, 32
                ldir
                ld      a, (K_CLAMPED)
                and     248
                ld      (K_CLAMPED), a
                ld      a, (K_CLAMPED + 31)
                and     127
                or      64
                ld      (K_CLAMPED + 31), a

                ld      hl, X25519_U            ; decode u: mask the top bit, x1 = x3 = u
                ld      de, LX1
                ld      bc, 32
                ldir
                ld      a, (LX1 + 31)
                and     127
                ld      (LX1 + 31), a
                ld      hl, LX1
                ld      de, LX3
                ld      bc, 32
                ldir

                ld      hl, LX2                 ; x2 = 1
                ld      (hl), 0
                ld      de, LX2 + 1
                ld      bc, 31
                ldir
                ld      a, 1
                ld      (LX2), a
                ld      hl, LZ2                 ; z2 = 0
                ld      (hl), 0
                ld      de, LZ2 + 1
                ld      bc, 31
                ldir
                ld      hl, LZ3                 ; z3 = 1
                ld      (hl), 0
                ld      de, LZ3 + 1
                ld      bc, 31
                ldir
                ld      a, 1
                ld      (LZ3), a

                xor     a                       ; swap = 0
                ld      (lad_swap), a
                ld      a, 254                  ; t = 254 down to 0
                ld      (lad_t), a
x25519_loop:
                ; k_t = (K_CLAMPED >> t) & 1
                ld      a, (lad_t)
                and     7
                ld      b, a                    ; b = bit index within the byte
                ld      a, (lad_t)
                srl     a
                srl     a
                srl     a                       ; a = byte index (t >> 3)
                ld      e, a
                ld      d, 0
                ld      hl, K_CLAMPED
                add     hl, de
                ld      a, (hl)                 ; a = K_CLAMPED[byte index]
x25519_shift:
                dec     b
                jp      m, x25519_shifted
                srl     a
                jp      x25519_shift
x25519_shifted:
                and     1
                ld      (lad_kt), a             ; k_t

                ld      a, (lad_swap)           ; swap ^= k_t
                ld      hl, lad_kt
                xor     (hl)
                ld      (lad_swap), a
                call    cond_swap_state         ; A = swap
                ld      a, (lad_kt)             ; swap = k_t
                ld      (lad_swap), a

                call    ladderstep

                ld      a, (lad_t)
                or      a
                jr      z, x25519_after
                dec     a
                ld      (lad_t), a
                jp      x25519_loop
x25519_after:
                ld      a, (lad_swap)           ; the final cswap
                call    cond_swap_state

                ld      hl, LZ2                 ; T1 = z2^-1
                call    to_A
                call    fe_invert
                ld      de, LT1
                call    from_OUT
                ld      hl, LX2                 ; T2 = x2 * z2^-1
                call    to_A
                ld      hl, LT1
                call    to_B
                call    fe_mul
                ld      de, LT2
                call    from_OUT
                ld      hl, LT2                 ; freeze -> X25519_OUT
                ld      de, FE_A
                ld      bc, 32
                ldir
                call    fe_freeze
                ld      hl, FE_OUT
                ld      de, X25519_OUT
                ld      bc, 32
                ldir
                ret
