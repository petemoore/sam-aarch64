; x25519.asm — Curve25519 field arithmetic over GF(2^255 - 19), the foundation
; layer for the i88 TLS key exchange (X25519, RFC 7748).  This file holds the
; field operations; the Montgomery ladder + scalar mult build on them.
;
; Field elements are 32-byte little-endian numbers kept < 2^255 (the "almost
; reduced" form: every operation folds its result below 2^255 but not necessarily
; below p = 2^255 - 19; fe_freeze produces the canonical < p form for output).
; The Z80 has no multiply, so this is byte-radix (radix 2^8) big-integer code:
;   * mul8          — 8x8 -> 16 shift-add
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
fe_rp:          defs 2                  ; running source pointer (b / high)
fe_pq:          defs 2                  ; running PROD / T destination pointer
fe_car:         defs 1                  ; the inner-loop byte carry

INV_I:          defs 32                 ; fe_invert: the saved input i
INV_C:          defs 32                 ; fe_invert: the running accumulator
inv_a:          defs 1                  ; fe_invert: the addition-chain step counter

; 2p = 2*(2^255-19) = 2^256 - 38 = 0x...FFDA (byte0 = 0xDA, bytes 1..31 = 0xFF).
FE_2P:          defb &DA, &FF, &FF, &FF, &FF, &FF, &FF, &FF
                defb &FF, &FF, &FF, &FF, &FF, &FF, &FF, &FF
                defb &FF, &FF, &FF, &FF, &FF, &FF, &FF, &FF
                defb &FF, &FF, &FF, &FF, &FF, &FF, &FF, &FF

; 121665 = 0x0001DB41 (little-endian byte0=0x41, byte1=0xDB, byte2=0x01).
FE_121665:      defb &41, &DB, &01
                defs 29

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
                jr      fe_store_out

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
                jr      fe_store_out

; ===========================================================================
; fe_mul — FE_OUT = (FE_A * FE_B) mod p.  Clobbers A, BC, DE, HL + scratch.
; ===========================================================================
fe_mul:
                call    fe_schoolbook           ; FE_PROD = FE_A * FE_B (64 bytes)
                call    fe_reduce_prod          ; FE_ACC = FE_PROD mod p (< 2^255)
                jr      fe_store_out

; ===========================================================================
; fe_mul121665 — FE_OUT = (FE_A * 121665) mod p, via fe_mul with the constant.
; Clobbers FE_B and the fe_mul scratch.
; ===========================================================================
fe_mul121665:
                ld      hl, FE_121665
                ld      de, FE_B
                ld      bc, 32
                ldir
                jr      fe_mul

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
; mul8 — HL = A * E (8x8 -> 16), shift-add, MSB first.  Requires D = 0.
; Clobbers A, B, HL.  Preserves C, DE.
; ---------------------------------------------------------------------------
mul8:
                ld      hl, 0
                ld      b, 8
fe_mul8_loop:
                add     hl, hl                  ; product <<= 1 (first pass clears carry)
                rla                             ; A <<= 1, bit7 -> carry
                jr      nc, fe_mul8_skip
                add     hl, de                  ; add the multiplicand
fe_mul8_skip:
                djnz    fe_mul8_loop
                ret

; ---------------------------------------------------------------------------
; fe_schoolbook — FE_PROD (64 bytes) = FE_A (32) * FE_B (32), schoolbook.
; PROD zeroed first; each row i adds A[i]*B[j] into PROD[i+j] with a running
; carry, then the final carry is propagated upward.  Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
fe_schoolbook:
                ld      hl, FE_PROD             ; zero PROD (65 bytes)
                ld      (hl), 0
                ld      de, FE_PROD + 1
                ld      bc, 64
                ldir

                ld      hl, FE_A
                ld      (fe_ap), hl
                ld      hl, FE_PROD
                ld      (fe_pp), hl
                ld      b, 32                   ; outer: i = 0..31
fe_sb_outer:
                push    bc
                ld      hl, (fe_ap)
                ld      a, (hl)
                ld      (fe_ai), a

                ld      hl, FE_B
                ld      (fe_rp), hl
                ld      hl, (fe_pp)
                ld      (fe_pq), hl
                xor     a
                ld      (fe_car), a
                ld      b, 32                   ; inner: j = 0..31
fe_sb_inner:
                push    bc
                ld      hl, (fe_rp)
                ld      a, (hl)
                inc     hl
                ld      (fe_rp), hl
                ld      e, a
                ld      d, 0
                ld      a, (fe_ai)
                call    mul8                    ; HL = A[i] * B[j]

                ld      de, (fe_pq)
                ld      a, (de)
                ld      c, a
                ld      b, 0
                add     hl, bc                  ; += PROD[i+j]
                ld      a, (fe_car)
                ld      c, a
                ld      b, 0
                add     hl, bc                  ; += carry

                ld      a, l
                ld      (de), a
                inc     de
                ld      (fe_pq), de
                ld      a, h
                ld      (fe_car), a
                pop     bc
                djnz    fe_sb_inner

                ld      de, (fe_pq)             ; add final carry at PROD[i+32]..
                ld      a, (fe_car)
fe_sb_carry:
                or      a
                jr      z, fe_sb_carry_done
                ld      c, a
                ld      a, (de)
                add     a, c
                ld      (de), a
                inc     de
                ld      a, 0
                adc     a, 0
                jr      fe_sb_carry
fe_sb_carry_done:
                ld      hl, (fe_ap)
                inc     hl
                ld      (fe_ap), hl
                ld      hl, (fe_pp)
                inc     hl
                ld      (fe_pp), hl
                pop     bc
                djnz    fe_sb_outer
                ret

; ---------------------------------------------------------------------------
; fe_reduce_prod — FE_ACC = FE_PROD (64 bytes) mod p, result < 2^255.
; Fold the high half in via 2^256 ≡ 38, then fe_reduce33.
; Clobbers A, BC, DE, HL.
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
; fe_mul38_high — FE_T (33 bytes) = 38 * FE_PROD[32..63].  Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
fe_mul38_high:
                ld      hl, FE_PROD + 32
                ld      (fe_rp), hl
                ld      hl, FE_T
                ld      (fe_pq), hl
                xor     a
                ld      (fe_car), a
                ld      b, 32
fe_m38_loop:
                push    bc
                ld      hl, (fe_rp)
                ld      a, (hl)
                inc     hl
                ld      (fe_rp), hl
                ld      e, 38
                ld      d, 0
                call    mul8                    ; HL = H[i] * 38
                ld      a, (fe_car)
                ld      c, a
                ld      b, 0
                add     hl, bc                  ; += carry
                ld      de, (fe_pq)
                ld      a, l
                ld      (de), a
                inc     de
                ld      (fe_pq), de
                ld      a, h
                ld      (fe_car), a
                pop     bc
                djnz    fe_m38_loop
                ld      de, (fe_pq)             ; FE_T[32] = the final carry
                ld      a, (fe_car)
                ld      (de), a
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
                ld      d, 0
                call    mul8
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
; p-2 = 2^255 - 21).  Squaring is fe_mul(c, c).  Clobbers everything + scratch.
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
                ld      hl, INV_C
                ld      de, FE_B
                call    cp32
                call    fe_mul
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
