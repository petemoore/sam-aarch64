; poly1305.asm — the Poly1305 one-time authenticator (RFC 8439 §2.5), a
; from-scratch multi-precision port for the i88 TLS cipher suite
; (ChaCha20-Poly1305).
;
; poly1305_mac(key, msg) computes the 16-byte tag.  The 32-byte one-time key is
; (r, s): r = key[0..16] little-endian, clamped (r &= 0x0ffffffc...0fffffff); s =
; key[16..32] little-endian, used as-is.  With p = 2^130 - 5 and accumulator a:
;   for each 16-byte block: n = le(block || 0x01); a = (a + n) * r mod p
;   a = a + s; tag = a mod 2^128 (the low 16 bytes, little-endian).
; The last block may be short: the 0x01 byte is appended after its bytes.
;
; The Z80 has no multiply, so everything is byte-radix (radix 2^8) big-integer
; arithmetic.  mul8 is an 8x8->16 shift-add; poly_mul_a_r is the 17x16 schoolbook
; product (a < 2^131, r < 2^124, so the product < 2^256, 32 bytes).  Reduction mod
; 2^130 - 5 uses 2^130 == 5 (mod p): split P = low + 2^130*high and replace with
; low + 5*high, folding until the value drops below 2^130.  poly_freeze does the
; final conditional subtract of p so the tag is canonical before adding s.
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/poly1305_test.go assembles this file, runs poly1305_mac
; under the koron-go/z80 harness, and asserts the 16-byte tag equals (a) the RFC
; 8439 §2.5.2 known-answer vector and (b) a math/big reference implementation of
; Poly1305 across many key/message lengths (0, partial, block-boundary, multi-block)
; byte-for-byte.  There is no Go-side asm authority; Poly1305 is specified by RFC
; 8439 and the reference is the §2.5 definition.  NOT host-verifiable: nothing here
; is hardware-gated - pure arithmetic + memory, like sha256.asm / chacha20.asm.

                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

; ===========================================================================
; Inputs + working buffers + scratch (data first, so every label is defined
; before the code references it).
; ===========================================================================
POLY_KEY:       defs 32                 ; in: the 32-byte one-time key (r || s)
POLY_MSG_PTR:   defs 2                  ; in: message pointer
POLY_MSG_LEN:   defs 2                  ; in: message length in bytes

POLY_R:         defs 16                 ; clamped r (little-endian, < 2^124)
POLY_S:         defs 16                 ; s (little-endian)
POLY_A:         defs 17                 ; the accumulator (< 2^131 in flight)
POLY_N:         defs 17                 ; the current block as a number
POLY_PROD:      defs 34                 ; a * r product (32 bytes used + slack)
POLY_HIGH:      defs 17                 ; PROD >> 130 during reduction
POLY_T:         defs 17                 ; 5 * HIGH (and freeze scratch)

poly_msrc:      defs 2                  ; running message source pointer
poly_mrem:      defs 2                  ; message bytes remaining
poly_blen:      defs 1                  ; this block's byte count (1..16)
poly_ap:        defs 2                  ; &A[i] in the schoolbook outer loop
poly_pp:        defs 2                  ; &PROD[i] in the schoolbook outer loop
poly_ai:        defs 1                  ; A[i], the current multiplier byte
poly_rp:        defs 2                  ; running source pointer (R / HIGH)
poly_pq:        defs 2                  ; running PROD / T destination pointer
poly_car:       defs 1                  ; the inner-loop byte carry

; qsq_table — the quarter-square multiply lookup, QSQ[n] = floor(n^2/4) for
; n = 0..510 (511 little-endian 16-bit entries, 1022 bytes). mul8 computes
; a*b = QSQ[a+b] - QSQ[|a-b|]. Pure regenerable scratch (qsq_init rebuilds it via
; a running recurrence, no multiply), so it lives in RAM above the module image —
; costing zero file/ROM bytes — rather than as emitted data.
qsq_table:      equ &C000

; ===========================================================================
; poly1305_mac — the Poly1305 tag of (POLY_MSG_PTR, POLY_MSG_LEN) keyed by
; POLY_KEY; the 16-byte tag is written to (HL).
; In:  HL = output pointer; POLY_KEY / POLY_MSG_PTR / POLY_MSG_LEN set.
; Out: 16 bytes at the output pointer.  Clobbers A, BC, DE, HL + the scratch.
; ===========================================================================
poly1305_mac:
                push    hl                      ; save the output pointer
                call    qsq_init                ; build the multiply table once
                call    poly_clamp              ; POLY_R / POLY_S from the key

                ; a = 0 (17 bytes)
                ld      hl, POLY_A
                ld      (hl), 0
                ld      de, POLY_A + 1
                ld      bc, 16
                ldir

                ld      hl, (POLY_MSG_PTR)
                ld      (poly_msrc), hl
                ld      hl, (POLY_MSG_LEN)
                ld      (poly_mrem), hl

poly_blk_loop:
                ld      hl, (poly_mrem)
                ld      a, h
                or      l
                jp      z, poly_blk_done        ; no bytes left

                ; --- build N = le(block) with the 0x01 appended ---
                ld      hl, POLY_N              ; zero the 17 bytes first
                ld      (hl), 0
                ld      de, POLY_N + 1
                ld      bc, 16
                ldir

                ; blocklen = min(16, remaining)
                ld      hl, (poly_mrem)
                ld      a, h
                or      a
                jr      nz, poly_blk_full       ; remaining >= 256 -> full block
                ld      a, l
                cp      16
                jr      c, poly_blk_part        ; remaining < 16 -> partial
poly_blk_full:
                ld      c, 16
                jr      poly_blk_copy
poly_blk_part:
                ld      c, l                    ; 1..15
poly_blk_copy:
                ld      a, c
                ld      (poly_blen), a
                ld      hl, (poly_msrc)
                ld      de, POLY_N
                ld      b, 0                    ; BC = blocklen
                ldir                            ; N[0..blen-1] = block bytes

                ld      a, (poly_blen)          ; N[blen] = 0x01
                ld      e, a
                ld      d, 0
                ld      hl, POLY_N
                add     hl, de
                ld      (hl), 1

                ld      a, (poly_blen)          ; advance msrc, shrink remaining
                ld      e, a
                ld      d, 0
                ld      hl, (poly_msrc)
                add     hl, de
                ld      (poly_msrc), hl
                ld      hl, (poly_mrem)
                or      a
                sbc     hl, de
                ld      (poly_mrem), hl

                ; a = a + n
                ld      hl, POLY_A
                ld      de, POLY_N
                call    add17

                ; a = (a * r) mod p
                call    poly_mul_a_r            ; PROD = A * R
                call    poly_reduce_prod        ; A = PROD mod p  (A < 2^130)
                jp      poly_blk_loop

poly_blk_done:
                call    poly_freeze             ; A canonical (< p)
                call    poly_add_s              ; A[0..15] = (A + s) mod 2^128
                pop     de                      ; the output pointer
                ld      hl, POLY_A
                ld      bc, 16
                ldir                            ; tag -> (output)
                ret

; ---------------------------------------------------------------------------
; poly_clamp — POLY_R = key[0..16] clamped; POLY_S = key[16..32].
; Clamp clears the top 4 bits of r[3],r[7],r[11],r[15] and the low 2 bits of
; r[4],r[8],r[12] (mask 0x0ffffffc0ffffffc0ffffffc0fffffff).
; ---------------------------------------------------------------------------
poly_clamp:
                ld      hl, POLY_KEY
                ld      de, POLY_R
                ld      bc, 16
                ldir
                ld      hl, POLY_KEY + 16
                ld      de, POLY_S
                ld      bc, 16
                ldir
                ld      a, (POLY_R + 3)
                and     15
                ld      (POLY_R + 3), a
                ld      a, (POLY_R + 7)
                and     15
                ld      (POLY_R + 7), a
                ld      a, (POLY_R + 11)
                and     15
                ld      (POLY_R + 11), a
                ld      a, (POLY_R + 15)
                and     15
                ld      (POLY_R + 15), a
                ld      a, (POLY_R + 4)
                and     252
                ld      (POLY_R + 4), a
                ld      a, (POLY_R + 8)
                and     252
                ld      (POLY_R + 8), a
                ld      a, (POLY_R + 12)
                and     252
                ld      (POLY_R + 12), a
                ret

; ---------------------------------------------------------------------------
; add17 — (HL,17 bytes) += (DE,17 bytes), little-endian (LSB at HL/DE).
; Clobbers A, B, DE, HL.  Preserves C.
; ---------------------------------------------------------------------------
add17:
                ld      b, 17
                or      a                       ; clear carry
add17_loop:
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    add17_loop
                ret

; ---------------------------------------------------------------------------
; add_byte_a — POLY_A += A (an 8-bit addend), with carry propagated across all
; 17 bytes.  Clobbers A, B, HL.
; ---------------------------------------------------------------------------
add_byte_a:
                ld      hl, POLY_A
                add     a, (hl)
                ld      (hl), a
                inc     hl
                ld      b, 16
add_byte_loop:
                ld      a, (hl)
                adc     a, 0
                ld      (hl), a
                inc     hl
                djnz    add_byte_loop
                ret

; ---------------------------------------------------------------------------
; mul8 — HL = A * E (8x8 -> 16) via the quarter-square identity
;   a*b = floor((a+b)^2/4) - floor((a-b)^2/4) = QSQ[a+b] - QSQ[|a-b|],
; exact because a+b and a-b share parity. Two table lookups + a 16-bit subtract
; replace the 8-iteration shift-add loop — the hot inner multiply of the
; schoolbook. qsq_table must already be built (poly1305_mac builds it at entry).
; In: A, E = the two byte operands (D ignored). Out: HL = A*E.
; Clobbers A, BC, DE, HL. (Both callers reload BC/DE after the call.)
mul8:
                ld      b, a                    ; B = multiplier
                ld      c, e                    ; C = multiplicand
                add     a, e                    ; A = (a+b) & 0xFF, CF = bit8
                ld      l, a
                ld      h, 0
                rl      h                       ; H = bit8 -> HL = a+b (0..510)
                add     hl, hl                  ; 2*(a+b) for the 16-bit stride
                ld      de, qsq_table
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = QSQ[a+b]
                push    de
                ld      a, b
                sub     c                       ; A = a-b, CF if a<b
                jr      nc, mul8_diff_pos
                neg                             ; A = |a-b|
mul8_diff_pos:
                ld      l, a
                ld      h, 0
                add     hl, hl                  ; 2*|a-b|
                ld      de, qsq_table
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = QSQ[|a-b|]
                pop     hl                      ; HL = QSQ[a+b]
                or      a
                sbc     hl, de                  ; HL = QSQ[a+b] - QSQ[|a-b|] = a*b
                ret

; qsq_init — build qsq_table: QSQ[n] = floor(n^2/4) for n = 0..510, via the
; recurrence QSQ[n] = QSQ[n-2] + (n-1) (exact, pure 16-bit adds, no multiply).
; Runs once before any mul8. Clobbers A, BC, DE, HL.
qsq_init:
                ld      hl, qsq_table
                ld      (hl), 0                 ; QSQ[0] = 0
                inc     hl
                ld      (hl), 0
                inc     hl
                ld      (hl), 0                 ; QSQ[1] = 0
                inc     hl
                ld      (hl), 0
                inc     hl                      ; HL -> &QSQ[2]
                ld      bc, 1                   ; BC = (n-1); n starts at 2
qsq_init_loop:
                dec     hl
                dec     hl
                dec     hl
                dec     hl                      ; HL -> &QSQ[n-2]
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = QSQ[n-2]
                inc     hl
                inc     hl
                inc     hl                      ; HL -> &QSQ[n]
                ex      de, hl                  ; HL = QSQ[n-2], DE = &QSQ[n]
                add     hl, bc                  ; HL = QSQ[n-2] + (n-1) = QSQ[n]
                ex      de, hl                  ; DE = QSQ[n], HL = &QSQ[n]
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl                      ; HL -> &QSQ[n+1]
                inc     bc                      ; advance (n-1)
                ld      a, b                    ; stop once (n-1) reaches 510: B=1,C=254
                cp      1
                jr      nz, qsq_init_loop
                ld      a, c
                cp      254
                jr      nz, qsq_init_loop
                ret

; ---------------------------------------------------------------------------
; poly_mul_a_r — POLY_PROD (34 bytes) = POLY_A (17) * POLY_R (16), schoolbook.
; PROD is zeroed first; each row i adds A[i]*R[j] into PROD[i+j] with a running
; carry, then the final carry is propagated upward.  Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
poly_mul_a_r:
                ld      hl, POLY_PROD           ; zero PROD (34 bytes)
                ld      (hl), 0
                ld      de, POLY_PROD + 1
                ld      bc, 33
                ldir

                ld      hl, POLY_A
                ld      (poly_ap), hl
                ld      hl, POLY_PROD
                ld      (poly_pp), hl
                ld      b, 17                   ; outer: i = 0..16
poly_mul_outer:
                push    bc
                ld      hl, (poly_ap)           ; ai = A[i]
                ld      a, (hl)
                ld      (poly_ai), a

                ld      hl, POLY_R              ; inner source = &R[0]
                ld      (poly_rp), hl
                ld      hl, (poly_pp)           ; inner dest = &PROD[i]
                ld      (poly_pq), hl
                xor     a
                ld      (poly_car), a           ; carry = 0
                ld      b, 16                   ; inner: j = 0..15
poly_mul_inner:
                push    bc
                ld      hl, (poly_rp)           ; e = R[j]; advance rp
                ld      a, (hl)
                inc     hl
                ld      (poly_rp), hl
                ld      e, a
                ld      d, 0
                ld      a, (poly_ai)            ; multiplier = A[i]
                call    mul8                    ; HL = A[i] * R[j]   (<= 0xFE01)

                ld      de, (poly_pq)           ; HL += PROD[i+j]
                ld      a, (de)
                ld      c, a
                ld      b, 0
                add     hl, bc
                ld      a, (poly_car)           ; HL += carry
                ld      c, a
                ld      b, 0
                add     hl, bc

                ld      a, l                    ; PROD[i+j] = low
                ld      (de), a
                inc     de
                ld      (poly_pq), de
                ld      a, h                    ; carry = high
                ld      (poly_car), a
                pop     bc
                djnz    poly_mul_inner

                ld      de, (poly_pq)           ; add final carry at PROD[i+16]..
                ld      a, (poly_car)
poly_mul_carry:
                or      a
                jr      z, poly_mul_carry_done
                ld      c, a
                ld      a, (de)
                add     a, c
                ld      (de), a
                inc     de
                ld      a, 0
                adc     a, 0                    ; carry-out (0/1)
                jr      poly_mul_carry
poly_mul_carry_done:
                ld      hl, (poly_ap)           ; advance outer pointers
                inc     hl
                ld      (poly_ap), hl
                ld      hl, (poly_pp)
                inc     hl
                ld      (poly_pp), hl
                pop     bc
                djnz    poly_mul_outer
                ret

; ---------------------------------------------------------------------------
; poly_reduce_prod — POLY_A = POLY_PROD mod (2^130 - 5), result < 2^130.
; HIGH = PROD >> 130; A = (PROD & (2^130-1)) + 5*HIGH; then fold any residual
; bits >= 130 (5*high) until clear.  Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
poly_reduce_prod:
                call    poly_extract_high       ; HIGH = PROD >> 130
                call    poly_mul5               ; T = HIGH * 5

                ld      hl, POLY_PROD           ; A = low 130 bits of PROD
                ld      de, POLY_A
                ld      bc, 16
                ldir
                ld      a, (POLY_PROD + 16)
                and     3                       ; bits 128,129 only
                ld      (POLY_A + 16), a

                ld      hl, POLY_A              ; A += T
                ld      de, POLY_T
                call    add17
poly_redsmall:
                ld      a, (POLY_A + 16)
                cp      4                       ; bit 130 set?  (A[16] >= 4)
                ret     c                       ; A < 2^130 -> done
                srl     a                       ; high = A[16] >> 2 (units of 2^130)
                srl     a
                ld      c, a
                ld      a, (POLY_A + 16)        ; clear bits >= 130
                and     3
                ld      (POLY_A + 16), a
                ld      a, c                    ; A += 5*high
                add     a, a
                add     a, a
                add     a, c
                call    add_byte_a
                jr      poly_redsmall

; ---------------------------------------------------------------------------
; poly_extract_high — POLY_HIGH (17 bytes) = POLY_PROD >> 130 (>> 16 bytes,
; then >> 2 bits across the window).  Reads PROD[16..33] (PROD[33] = 0).
; Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
poly_extract_high:
                ld      hl, POLY_PROD + 16
                ld      de, POLY_HIGH
                ld      b, 17
poly_eh_loop:
                ld      a, (hl)                 ; cur >> 2
                srl     a
                srl     a
                ld      c, a
                inc     hl                      ; HL -> next byte (next cur)
                ld      a, (hl)                 ; (nxt & 3) << 6
                rrca
                rrca
                and     &C0
                or      c
                ld      (de), a
                inc     de
                djnz    poly_eh_loop
                ret

; ---------------------------------------------------------------------------
; poly_mul5 — POLY_T (17 bytes) = POLY_HIGH (17 bytes) * 5.
; Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
poly_mul5:
                ld      hl, POLY_HIGH
                ld      (poly_rp), hl
                ld      hl, POLY_T
                ld      (poly_pq), hl
                xor     a
                ld      (poly_car), a
                ld      b, 17
poly_mul5_loop:
                push    bc
                ld      hl, (poly_rp)
                ld      a, (hl)
                inc     hl
                ld      (poly_rp), hl
                ld      e, 5
                ld      d, 0
                call    mul8                    ; HL = HIGH[i] * 5
                ld      a, (poly_car)
                ld      c, a
                ld      b, 0
                add     hl, bc                  ; += carry
                ld      de, (poly_pq)
                ld      a, l
                ld      (de), a
                inc     de
                ld      (poly_pq), de
                ld      a, h
                ld      (poly_car), a
                pop     bc
                djnz    poly_mul5_loop
                ret

; ---------------------------------------------------------------------------
; poly_freeze — canonicalise POLY_A (< 2^130) to < p = 2^130 - 5.  Compute
; t = A + 5; if t >= 2^130 then A >= p, so A = t - 2^130 (bit 130 cleared).
; Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
poly_freeze:
                ld      hl, POLY_A              ; T = A
                ld      de, POLY_T
                ld      bc, 17
                ldir
                ld      hl, POLY_T              ; T += 5
                ld      a, (hl)
                add     a, 5
                ld      (hl), a
                inc     hl
                ld      b, 16
poly_freeze_carry:
                ld      a, (hl)
                adc     a, 0
                ld      (hl), a
                inc     hl
                djnz    poly_freeze_carry

                ld      a, (POLY_T + 16)        ; bit 130 of t set?
                and     4
                ret     z                       ; A < p -> keep A unchanged

                ld      hl, POLY_T              ; A = t - 2^130
                ld      de, POLY_A
                ld      bc, 17
                ldir
                ld      a, (POLY_A + 16)
                and     3
                ld      (POLY_A + 16), a
                ret

; ---------------------------------------------------------------------------
; poly_add_s — POLY_A[0..15] += POLY_S[0..15] (the tag is mod 2^128, so the
; carry out of byte 15 is discarded).  Clobbers A, B, DE, HL.
; ---------------------------------------------------------------------------
poly_add_s:
                ld      hl, POLY_A
                ld      de, POLY_S
                ld      b, 16
                or      a
poly_add_s_loop:
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    poly_add_s_loop
                ret
